# 20：持续目录增量、固定调度与准入回归

**What to build:** 在 08/09 已有完整索引和每日调度基础上，补齐 partial 运行的变化发现与失败范围重试；把每日／周日 03:00 调度、任务排他、重启追赶做成可验证状态机。D-16 三项准入已在 `134f7c90` 实现，本票只负责跨层边界、系列成员语义、性能和回归，不重复实现规则。

**Blocked by:** [09：每日更新与已有扫描复用](09-daily-index-update.md)（08 为传递前置）。

**Status:** ready-for-agent
**Implementation:** in-progress；主仓库 `47df9c11` 已提交 wall-clock 和 partial probe 修复，并在最新 SHA 通过专项调度测试；但该实现仍以 `app_setting` 保存 due，尚未落地本票的 `source_catalog_schedule`、`manual_pending` 和 `deferred_reason`。必须按行为验收决定保留简化实现还是补齐表；旧审查 worktree 的未提交调度代码不能作为当前产物。

**依据:** [正式规格](../spec.md)、[整改计划](../review-remediation-plan.md)、[ADR-0006](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)、`kikoto-remediation-addendum-20261021.md` 的 D-16/D-22/D-25。

## 验收归属

本票负责 AC-21 的 partial 变化发现与失败范围重试、AC-22 的固定日／周调度、AC-08 的 D-16 准入子项。AC-21/22 的原始主责仍是 08/09；AC-08 的原始主责仍是 07/08。本票不取代全量遍历、卡片验收或最终发布证据。

## 状态模型

- **generation**：`queued`、`running`、`succeeded`、`partial`、`failed`、`cancelled`；只有 `succeeded` 且全部目录 complete 才能视为完整覆盖。
- **directory**：`pending`、`listing_complete`、`complete`、`failed`、`restricted`、`empty`。`empty + not_found` 是确认空；其他 empty 是未确认空。
- **sync**：`running`、`succeeded`、`partial`、`failed`、`cancelled`。
- **排他**：同一来源同一时刻只允许一个 full generation 或一个 sync 持有写阶段；手动刷新、定时刷新和重启恢复不能产生重叠任务。
- **调度**：`daily_due_at` 与 `weekly_due_at` 使用 Asia/Shanghai；每日下一次 03:00；每周下一次周日 03:00。触发时间与扫描完成时间分离，不能用“完成时间 + 24h/7d”替代。

## 调度数据模型

新增 `source_catalog_schedule`（计划编号 098，位于已提交的 096 和 097 之后；若集成时编号变化，以连续链为准）：

```sql
CREATE TABLE source_catalog_schedule (
  file_source_id INTEGER PRIMARY KEY REFERENCES file_source(id) ON DELETE CASCADE,
  daily_due_at INTEGER NOT NULL,
  weekly_due_at INTEGER NOT NULL,
  deferred_until INTEGER NOT NULL DEFAULT 0,
  deferred_reason TEXT NOT NULL DEFAULT '',
  manual_pending INTEGER NOT NULL DEFAULT 0 CHECK(manual_pending IN (0,1)),
  resume_full_pending INTEGER NOT NULL DEFAULT 0 CHECK(resume_full_pending IN (0,1)),
  pending_full_run_id INTEGER REFERENCES workflow_run(id) ON DELETE SET NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX source_catalog_schedule_due
  ON source_catalog_schedule(deferred_until, daily_due_at, weekly_due_at);
```

旧的 `source_catalog_daily:`、`source_catalog_daily_run:`、`source_catalog_full:` setting 只用于迁移读取，迁移后不再参与判定。禁止在 due SQL 中混用 `setting` 与 `run_setting`。

迁移初始化只处理 `file_source.enabled=1 AND source_type='openlist'` 的来源，每个来源恰好一行：

- `daily_due_at`：旧 `source_catalog_daily:<id>` 若是合法且未过期的时刻则沿用；否则取迁移时刻之后的下一个 Asia/Shanghai 03:00。
- `weekly_due_at`：旧 `source_catalog_full:<id>` 若是合法且未过期的时刻则沿用；否则取迁移时刻之后的下一个本地周日 03:00。
- `source_catalog_daily_run:<id>` 只作历史读取顺序参考，不和上述两个键合并成同一个 due；非法、过期或互相矛盾的旧值记录到迁移报告，不猜测。
- `deferred_until=0`、`deferred_reason=''`，两个 pending 位为 0，`pending_full_run_id=NULL`。删除 source 由外键级联清理。

新建或重新启用 openlist source 时，在同一事务插入或重置 schedule 行：`daily_due_at=下一次本地 03:00`、`weekly_due_at=下一次本地周日 03:00`、`deferred_until=0`、`deferred_reason=''`、两个 pending 位为 0、`pending_full_run_id=NULL`。禁用 source 不删除计划，但调度器跳过；重新启用时若 due 已过期，按下一次边界重算。

## 派发算法

1. 在 `BEGIN IMMEDIATE` 事务中读取 schedule、当前 generation、当前 workflow run、当前 sync 和 source 状态。source 不满足 `enabled=1 AND source_type='openlist'` 时直接返回。
2. **先检查运行中状态。** 已有 queued/running full 或 running sync 时：不创建第二个任务；`manual_pending`、到期标记和 `resume_full_pending` 只保留；运行中的 sync 遇到 weekly 到期时设置 `resume_full_pending=1`，不得在 sync 内创建 full。此分支必须先于 `resume_full_pending` 创建分支，避免 queued full 被重复创建。
3. 若 `now < deferred_until`，直接返回。若 `deferred_until` 已过，重新检查低空间、维护 hold 和磁盘未知；条件仍存在时写回 `deferred_until=now+1h` 和对应 `deferred_reason`，不能把时间到期当作条件解除；条件解除后才清空两个延后字段。
4. 若 `resume_full_pending=1`：
   - `pending_full_run_id` 指向的 full 仍为 queued/running：保留标记，直接返回。
   - 该 full 已 succeeded：推进 `weekly_due_at`，清零 `resume_full_pending`、`pending_full_run_id` 和 `deferred_reason`，返回。
   - 该 full 为 failed/cancelled：清空 `pending_full_run_id`，保留 `resume_full_pending=1`，本轮返回，下一轮再创建；不推进 weekly due。
   - 没有 `pending_full_run_id` 且没有 queued/running full：创建 weekly full，写入 `pending_full_run_id`，保留 `resume_full_pending=1`；创建成功不等于完成，不能提前清标记或推进 weekly due。
5. 否则计算 `effective_due = min(daily_due_at, weekly_due_at)`。`effective_due > now`、`manual_pending=0` 时直接返回；当 `weekly_due_at <= daily_due_at` 且到期时选择 weekly（全量对账），否则选择 daily（目录优先增量）。两者同时到期只创建一个任务。
6. 没有运行任务时才创建任务；创建失败或事务回滚时不得推进 due。daily/weekly 任务创建后按下列规则推进：
   - daily：`daily_due_at = next 03:00`；weekly 不变。
   - weekly：`weekly_due_at = next Sunday 03:00`；`daily_due_at = next 03:00`，避免周日重复。
   - manual：消费 `manual_pending`，按当前 generation 状态选择普通增量或维护同步，不改日／周计划。若同一轮已因 daily/weekly 到期创建任务，则该任务覆盖手动刷新，消费 `manual_pending`，不得再排第二个任务。
7. 最后 generation 为 partial/failed/cancelled 时启动维护同步：complete 目录做目录优先探测；failed/restricted/pending 范围重试；不先排 full generation，不并发 resume。只有没有任何可复用 generation 时才请求 full。weekly 同时到期时把 `resume_full_pending=1`，等维护同步结束后再跑 full。
8. 低空间、维护 hold 或磁盘未知时只设置 `deferred_until=now+1h` 和 `deferred_reason`，保留 `daily_due_at`、`weekly_due_at`、两个 pending 位、`pending_full_run_id` 和当前检查点；恢复后只追赶一次，不因 2 秒调度轮询重复触发。不能只推 daily 而留下已过期的 weekly，否则 weekly 会绕过延后。
9. 只有父目录本次 listing 完整且成功时，才可把未出现的子项判为缺失；pending、listing_complete、failed、restricted、未确认 empty 一律不判删除。

## force 增量

`force` 只表示重新探测所有目录，不表示创建 full。当前 `executeCatalogIncrementalSync` 在 sync lease 前/中调用 `enqueueSourceCatalogIndex` 会因 running sync 返回 `errCatalogSyncRunning`；删除该路径，改为在 sync 结束时写 `resume_full_pending`，由调度器串行处理。sync 自身不得调用会检查 running sync 的完整入口。

## D-16 三层一致

- 阈值：`audio == 0` 拒绝；`files > 5000` 拒绝；`nonAudio > 2000 && audio < 5` 拒绝。等于阈值允许。
- 适用范围：每个来源目录／卡片根；系列卡片不聚合文件数。系列中每个成员条目分别判定；未通过成员不进入系列卡或详情，不影响同系列其他合格成员；系列至少有一个合格成员才显示。
- 系列列表、系列详情和成员 tracks 入口都必须应用同一成员准入；不能只在列表外层 `EXISTS` 一个合格成员而把不合格成员继续列进详情。
- `catalogadmission.Decision` 是唯一规则源；`catalogEntryAdmittedSQL`、发布事务和单卡 API 的计数必须由同一组常量生成，不得硬编码 5001/2001/5。
- 修正 `admission.go` 顶部“Probe bounds 不决定准入”的旧注释。
- 旧测试中“大附属文件卡允许发布”的断言改为拒绝；正常多轨、音频附带字幕／压缩包仍允许。
- 计数必须与卡片详情读取同一份当前发布文件集合；若旧 generation 行仍可能残留，必须过滤当前 generation，不能把历史文件算进该卡。系列成员的计数只算该成员 entry，不能把同系列其他成员的文件相加。
- 浏览 API 先测 42,624 张卡的 p95；若三条相关子查询不达标，增加 `source_catalog_entry_admission` 统计表并在发布／文件变更事务维护，不能用陈旧计数过滤。

**已提交 096 的待补门禁：** `backend/internal/httpapi/catalog_series.go` 的 `getCatalogSeries` 当前只要求 `source_catalog_entry.file_source_id IS NOT NULL`，没有使用 `catalogEntryAdmittedSQL`（D-16）或 `catalogEntryPendingSQL`（096 pending publication）。096 激活期间，entry 已获得 `file_source_id` 但父 publication 仍为 `pending`，系列详情会提前列出该成员；超过 D-16 的成员也不会被过滤。20 必须给系列详情和成员 tracks 加同一组门禁，并补“激活批次之间不可见、publication 后可见”的 H 测试。

**096 恢复风险：** `deleteCatalogEpisodeMappings` 在 publication 提交后只记录删除失败并返回；下一次扫描的 `beginCatalogEpisodePublication` 会把 state 重置为 `pending`，随后 `clearCatalogEpisodePendingFiles` 会按残留 pending mapping 删除已发布 entry 的文件。必须让 mapping 清理可重试或在清理时排除已公开发布 entry，并补“publication 成功后注入 mapping 删除失败，重启再扫描不丢公开媒体”的 R 测试。

## 验收清单

- [ ] partial 时 complete 分支新增文件被发现；failed/restricted/pending 范围重试；unknown 不判缺失；重复发布不产生重复卡片。
- [ ] 进程关闭重启后同一 generation 续跑；手动刷新在运行中只排队一次，结束后执行一次。
- [ ] 每日／周日 03:00 边界、两者同时到期、等于时间、跨日、重启追赶、低空间恢复均有 H/R 证据。
- [ ] 低空间或维护 hold 只设置一次 `deferred_until`／`deferred_reason`；到期后重新检查条件，条件未解除时继续延后，不能误跑 full。
- [ ] queued full 不会被 `resume_full_pending` 重复创建；`resume_full_pending` 只在 full 终态 succeeded 后清零并推进 weekly due，failed/cancelled 保留待重试。
- [ ] force sync 不再触发 `errCatalogSyncRunning`；同一来源不产生 full 与 sync 重叠写；手动请求与同一轮已到期任务只执行一次。
- [ ] D-16 的 0/1/4/5 audio、2000/2001 nonAudio、5000/5001 files 在入队／发布／API 三层一致；系列成员逐条判定。
- [ ] 42k 卡浏览与 5,001 文件卡拒绝路径的 p95 证据；不得以全表扫描换取正确性。
- [ ] 096 激活期间系列详情/成员 tracks 不可见，publication 切换后才可见；系列详情与列表使用同一 D-16 成员过滤。
- [ ] 不重复实现 `134f7c90` 的规则；如发现其实现缺口，先报告再最小修正。

**测试入口:** H、R，必要的纯规则 U；真实覆盖由 24 汇总。

## Comments

本票保留既有目录优先算法，不用祖先 mtime 跳过整棵子树，不用搜索结果代替完整遍历。不能将 resume 同一 generation 当作跨 generation 增量。工作区未提交的 `source_catalog_daily.go`、`source_catalog_sync.go` 和 `source_catalog_schedule.go` 必须先取得绿色结果，否则从 `134f7c90` 重建，不得带入集成。
