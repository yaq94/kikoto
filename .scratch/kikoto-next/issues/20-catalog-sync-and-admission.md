# 20：持续目录增量、固定调度与准入回归

**What to build:** 在 08/09 已有完整索引和每日调度基础上，补齐 partial 运行的变化发现与失败范围重试；把每日／周日 03:00 调度、任务排他、重启追赶做成可验证状态机。D-16 三项准入已在 `134f7c90` 实现，本票只负责跨层边界、系列成员语义、性能和回归，不重复实现规则。

**Blocked by:** [09：每日更新与已有扫描复用](09-daily-index-update.md)（08 为传递前置）。

**Status:** ready-for-agent
**Implementation:** in-progress；工作区存在未提交的调度改动，尚未取得编译、定向测试或全量回归证据，不能作为当前产物。

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

新增 `source_catalog_schedule`（计划编号 098，位于 097 之后；若集成时编号变化，以连续链为准）：

```sql
CREATE TABLE source_catalog_schedule (
  file_source_id INTEGER PRIMARY KEY REFERENCES file_source(id) ON DELETE CASCADE,
  daily_due_at INTEGER NOT NULL,
  weekly_due_at INTEGER NOT NULL,
  deferred_until INTEGER NOT NULL DEFAULT 0,
  manual_pending INTEGER NOT NULL DEFAULT 0 CHECK(manual_pending IN (0,1)),
  resume_full_pending INTEGER NOT NULL DEFAULT 0 CHECK(resume_full_pending IN (0,1)),
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
- `deferred_until=0`，两个 pending 位为 0。删除 source 由外键级联清理。

新建或重新启用 openlist source 时，在同一事务插入或重置 schedule 行：`daily_due_at=下一次本地 03:00`、`weekly_due_at=下一次本地周日 03:00`、`deferred_until=0`、两个 pending 位为 0。禁用 source 不删除计划，但调度器跳过；重新启用时若 due 已过期，按下一次边界重算。

## 派发算法

1. 在 `BEGIN IMMEDIATE` 事务中读取 schedule、当前 generation、当前 workflow run、当前 sync 和 source 状态。source 不满足 `enabled=1 AND source_type='openlist'` 时直接返回。若 `now < deferred_until`，直接返回；若 `deferred_until` 已过，清零后继续。
2. 没有运行任务且 `resume_full_pending=1` 时优先创建 weekly full；成功后推进 `weekly_due_at` 并清零该位，避免 full 完成后再次排队。
3. 否则计算 `effective_due = min(daily_due_at, weekly_due_at)`。`effective_due > now`、`manual_pending=0`、`resume_full_pending=0` 时直接返回；当 `weekly_due_at <= daily_due_at` 且到期时选择 weekly（全量对账），否则选择 daily（目录优先增量）。两者同时到期只创建一个任务。
4. 若已有 queued/running full 或 running sync：不创建第二个任务；`manual_pending`、到期标记或 `resume_full_pending` 只保留待处理状态，不推进 due。运行中的 sync 遇到 weekly 到期时设置 `resume_full_pending=1`，不得在 sync 内创建 full。
5. 没有运行任务时才创建并推进 due；创建失败或事务回滚时不得推进 due：
   - daily：`daily_due_at = next 03:00`；weekly 不变。
   - weekly：`weekly_due_at = next Sunday 03:00`；`daily_due_at = next 03:00`，避免周日重复。
   - manual：消费 `manual_pending`，按当前 generation 状态选择普通增量或维护同步，不改日／周计划。
6. 最后 generation 为 partial/failed/cancelled 时启动维护同步：complete 目录做目录优先探测；failed/restricted/pending 范围重试；不先排 full generation，不并发 resume。只有没有任何可复用 generation 时才请求 full。weekly 同时到期时把 `resume_full_pending=1`，等维护同步结束后再跑 full。
7. 低空间、维护 hold 或磁盘未知时只设置 `deferred_until=now+1h`，保留 `daily_due_at`、`weekly_due_at`、两个 pending 位和当前检查点；恢复后只追赶一次，不因 2 秒调度轮询重复触发。不能只推 daily 而留下已过期的 weekly，否则 weekly 会绕过延后。
8. 只有父目录本次 listing 完整且成功时，才可把未出现的子项判为缺失；pending、listing_complete、failed、restricted、未确认 empty 一律不判删除。

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

## 验收清单

- [ ] partial 时 complete 分支新增文件被发现；failed/restricted/pending 范围重试；unknown 不判缺失；重复发布不产生重复卡片。
- [ ] 进程关闭重启后同一 generation 续跑；手动刷新在运行中只排队一次，结束后执行一次。
- [ ] 每日／周日 03:00 边界、两者同时到期、等于时间、跨日、重启追赶、低空间恢复均有 H/R 证据。
- [ ] 低空间或维护 hold 只设置一次 `deferred_until`，到期后只追赶一次；`resume_full_pending` 在 sync 结束后优先执行，full 完成后不会重复排队。
- [ ] force sync 不再触发 `errCatalogSyncRunning`；同一来源不产生 full 与 sync 重叠写。
- [ ] D-16 的 0/1/4/5 audio、2000/2001 nonAudio、5000/5001 files 在入队／发布／API 三层一致；系列成员逐条判定。
- [ ] 42k 卡浏览与 5,001 文件卡拒绝路径的 p95 证据；不得以全表扫描换取正确性。
- [ ] 不重复实现 `134f7c90` 的规则；如发现其实现缺口，先报告再最小修正。

**测试入口:** H、R，必要的纯规则 U；真实覆盖由 24 汇总。

## Comments

本票保留既有目录优先算法，不用祖先 mtime 跳过整棵子树，不用搜索结果代替完整遍历。不能将 resume 同一 generation 当作跨 generation 增量。工作区未提交的 `source_catalog_daily.go`、`source_catalog_sync.go` 和 `source_catalog_schedule.go` 必须先取得绿色结果，否则从 `134f7c90` 重建，不得带入集成。
