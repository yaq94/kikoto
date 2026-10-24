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

新增 `source_catalog_schedule`（迁移编号在实现时按实际连续链分配；本分支当前止于 095，系列迁移由并行实施会话负责）：

```sql
CREATE TABLE source_catalog_schedule (
  file_source_id INTEGER PRIMARY KEY REFERENCES file_source(id) ON DELETE CASCADE,
  daily_due_at INTEGER NOT NULL,
  weekly_due_at INTEGER NOT NULL,
  manual_pending INTEGER NOT NULL DEFAULT 0 CHECK(manual_pending IN (0,1)),
  resume_full_pending INTEGER NOT NULL DEFAULT 0 CHECK(resume_full_pending IN (0,1)),
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX source_catalog_schedule_due
  ON source_catalog_schedule(daily_due_at, weekly_due_at);
```

旧的 `source_catalog_daily:`、`source_catalog_daily_run:`、`source_catalog_full:` setting 只用于迁移读取，迁移后不再参与判定。禁止在 due SQL 中混用 `setting` 与 `run_setting`。

新建或重新启用 openlist source 时，在同一事务插入 schedule 行：`daily_due_at=下一次本地 03:00`、`weekly_due_at=下一次本地周日 03:00`、两个 pending 位为 0。删除 source 由外键级联清理。

## 派发算法

1. 在 `BEGIN IMMEDIATE` 事务中读取 schedule、当前 generation、当前 workflow run、当前 sync 和 source 状态。若 `effective_due > now`、`manual_pending=0`、`resume_full_pending=0`，直接返回。
2. `effective_due = min(daily_due_at, weekly_due_at)`；当 `weekly_due_at <= daily_due_at` 且到期时选择 weekly（全量对账），否则选择 daily（目录优先增量）。两者同时到期只创建一个任务。
3. 若已有 queued/running full 或 running sync：不创建第二个任务；`manual_pending`、到期标记或 `resume_full_pending` 只保留待处理状态，不推进 due。
4. 没有运行任务时才创建并推进 due：
   - daily：`daily_due_at = next 03:00`；weekly 不变。
   - weekly：`weekly_due_at = next Sunday 03:00`；`daily_due_at = next 03:00`，避免周日重复。
   - manual：消费 `manual_pending`，按当前 generation 状态选择普通增量或维护同步，不改日／周计划。
5. 最后 generation 为 partial/failed/cancelled 时启动维护同步：complete 目录做目录优先探测；failed/restricted/pending 范围重试；不先排 full generation，不并发 resume。只有没有任何可复用 generation 时才请求 full。weekly 同时到期时把 `resume_full_pending=1`，等维护同步结束后再跑 full。
6. weekly 全量必须与 sync 串行；sync 运行期间只记录 `resume_full_pending`，结束后由调度器创建 full；full 完成后清理该标记。
7. 只有父目录本次 listing 完整且成功时，才可把未出现的子项判为缺失；pending、listing_complete、failed、restricted、未确认 empty 一律不判删除。

## force 增量

`force` 只表示重新探测所有目录，不表示创建 full。当前 `executeCatalogIncrementalSync` 在 sync lease 前/中调用 `enqueueSourceCatalogIndex` 会因 running sync 返回 `errCatalogSyncRunning`；删除该路径，改为在 sync 结束时写 `resume_full_pending`，由调度器串行处理。sync 自身不得调用会检查 running sync 的完整入口。

## D-16 三层一致

- 阈值：`audio == 0` 拒绝；`files > 5000` 拒绝；`nonAudio > 2000 && audio < 5` 拒绝。等于阈值允许。
- 适用范围：每个来源目录／卡片根；系列卡片不聚合文件数。系列中每个成员条目分别判定；未通过成员不进入系列卡或详情，不影响同系列其他合格成员；系列至少有一个合格成员才显示。
- 系列列表、系列详情和成员 tracks 入口都必须应用同一成员准入；不能只在列表外层 `EXISTS` 一个合格成员而把不合格成员继续列进详情。
- `catalogadmission.Decision` 是唯一规则源；`catalogEntryAdmittedSQL`、发布事务和单卡 API 的计数必须由同一组常量生成，不得硬编码 5001/2001/5。
- 修正 `admission.go` 顶部“Probe bounds 不决定准入”的旧注释。
- 旧测试中“大附属文件卡允许发布”的断言改为拒绝；正常多轨、音频附带字幕／压缩包仍允许。
- 浏览 API 先测 42,624 张卡的 p95；若三条相关子查询不达标，增加 `source_catalog_entry_admission` 统计表并在发布／文件变更事务维护，不能用陈旧计数过滤。

## 验收清单

- [ ] partial 时 complete 分支新增文件被发现；failed/restricted/pending 范围重试；unknown 不判缺失；重复发布不产生重复卡片。
- [ ] 进程关闭重启后同一 generation 续跑；手动刷新在运行中只排队一次，结束后执行一次。
- [ ] 每日／周日 03:00 边界、两者同时到期、等于时间、跨日、重启追赶、低空间恢复均有 H/R 证据。
- [ ] force sync 不再触发 `errCatalogSyncRunning`；同一来源不产生 full 与 sync 重叠写。
- [ ] D-16 的 0/1/4/5 audio、2000/2001 nonAudio、5000/5001 files 在入队／发布／API 三层一致；系列成员逐条判定。
- [ ] 42k 卡浏览与 5,001 文件卡拒绝路径的 p95 证据；不得以全表扫描换取正确性。
- [ ] 不重复实现 `134f7c90` 的规则；如发现其实现缺口，先报告再最小修正。

**测试入口:** H、R，必要的纯规则 U；真实覆盖由 24 汇总。

## Comments

本票保留既有目录优先算法，不用祖先 mtime 跳过整棵子树，不用搜索结果代替完整遍历。不能将 resume 同一 generation 当作跨 generation 增量。工作区未提交的 `source_catalog_daily.go`、`source_catalog_sync.go` 和 `source_catalog_schedule.go` 必须先取得绿色结果，否则从 `134f7c90` 重建，不得带入集成。
