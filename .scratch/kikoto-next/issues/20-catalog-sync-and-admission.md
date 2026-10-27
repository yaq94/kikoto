# 20：持续目录增量、固定调度与准入回归

**What to build:** 在 08/09 已有完整索引和每日调度基础上，补齐 partial 运行的变化发现与失败范围重试；把每日／周日 03:00 调度、任务排他、重启追赶做成可验证状态机。D-16 三项准入已在 `134f7c90` 实现，本票只负责跨层边界、系列成员语义、性能和回归，不重复实现规则。

**Blocked by:** [09：每日更新与已有扫描复用](09-daily-index-update.md)（08 为传递前置）。

**Status:** ready-for-agent
**Implementation:** verified-local；`531acdf4` 落地 098 `source_catalog_schedule` 与单事务派发状态机，`6cf4ee1c` 合入集成分支的 097。`app_setting` 的 `source_catalog_daily:`／`source_catalog_full:` 只在 098 迁移内读取。当前实现：deferred 保留原 due 并在到期后重查资源条件；`pending_full_run_id` 绑定 resume full，只有该 run 成功才推进 weekly 并清标记；partial/failed/cancelled 走维护同步；`force` 在 failed／partial／cancelled 之后创建新的观测 generation。单卡评价、播放准备、媒体流、作品目录标签的完整准入，系列零可见成员 404，零文件与旧 generation 集合统一，目录优先 sync 的 due 推进顺序，诊断 `Probe*` 按来源过滤，以及可失败的真实 plan 断言均已并入集成分支并通过测试（`TestCatalogAdmissionEntryPointsUsePublishedSet`、`TestCatalogAdmissionCountsCurrentPublishedFiles`、`TestCatalogAdmissionZeroFileCardIsRefused`、`TestCatalogSeriesOmitsEntriesWithoutASource`、`TestCatalogScheduleDailyDueWaitsForSyncCreation`、`TestCatalogDiagnosticsOversizeCardsUseTheProbeBoundPerSource`、`TestCatalogScheduleRunningFullRegistersPendingRun` 与五个 plan 断言）。42k 卡浏览与 5,001 文件卡拒绝路径的 p95 已在目标机取得，数值见下方验收条目。

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

098 落地前，`source_catalog_daily:` 与 `source_catalog_full:` 是当前活存储；098 迁移只读取它们一次，迁移后不再参与判定。禁止在 due SQL 中混用 `setting` 与 `run_setting`。

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

**裁决（2026-10-25）：** `POST /api/remote-sources/{id}/catalog/index` 的 `force` 表示“在 failed／partial／cancelled 的 generation 之后开始一次新的观测 generation”，不表示目录重探（generation 状态没有 `paused`；`paused` 只属于 sync 或运行中的任务）。sync 运行中该入口不再返回 409：请求记成 `manual_pending=1` 并返回 202，由调度器在该 sync 结束后消费一次。同一来源的普通手动刷新与 force 使用这一条语义，不存在“force 不创建 full”的旧分支。

本票不新增独立的 force sync 入口；原“force 只重探目录”的验收项已删除，因为它与已交付入口的行为、以及规格 D-05“首次索引由管理员启动，完成后才每日更新”的边界冲突。

## D-16 三层一致

- 阈值：`audio == 0` 拒绝；`files > 5000` 拒绝；`nonAudio > 2000 && audio < 5` 拒绝。等于阈值允许。
- 适用范围：每个来源目录／卡片根；系列卡片不聚合文件数。系列中每个成员条目分别判定；未通过成员不进入系列卡或详情，不影响同系列其他合格成员；系列至少有一个合格成员才显示。
- 系列列表、系列详情和成员 tracks 入口都必须应用同一成员准入；不能只在列表外层 `EXISTS` 一个合格成员而把不合格成员继续列进详情。成员 tracks 已具备 pending 与准入门禁，本票补回归断言即可；系列详情两者都缺，必须补。
- `catalogadmission.Decision` 是唯一规则源；`catalogEntryAdmittedSQL`、发布事务和单卡 API 的计数必须由同一组常量生成，不得硬编码 5001/2001/5。
- 修正 `admission.go` 顶部“Probe bounds 不决定准入”的旧注释。
- 旧测试中“大附属文件卡允许发布”的断言改为拒绝；正常多轨、音频附带字幕／压缩包仍允许。
- 计数必须与卡片详情读取同一份当前发布文件集合；若旧 generation 行仍可能残留，必须过滤当前 generation，不能把历史文件算进该卡。系列成员的计数只算该成员 entry，不能把同系列其他成员的文件相加。
- 浏览 API 先测 42,624 张卡的 p95；若三条相关子查询不达标，增加 `source_catalog_entry_admission` 统计表并在发布／文件变更事务维护，不能用陈旧计数过滤。已按此实施（迁移 104），结果见验收清单。

**已提交 096 的待补门禁：** 已修复。`getCatalogSeries` 同时应用 `catalogEntryPendingSQL` 与 `catalogEntryAdmittedSQL`，系列列表与成员 tracks 使用同一组门禁；`TestCatalogSeriesHidesPendingPublicationUntilItIsPublished` 覆盖“激活批次之间不可见、publication 后可见”，`TestCatalogAdmissionSeriesFiltersEveryMember` 覆盖列表/详情/tracks 的逐成员过滤。

**096 恢复风险：** 已修复。`finalizeCatalogEpisodeRoots` 在 publication 翻为 `published` 的同一事务内删除 pending mapping；`beginCatalogEpisodePublication` 与清理辅助查询把 `published` 视为不可回退边界，残留 mapping 不会再让 `clearCatalogEpisodePendingFiles` 删除公开文件。`TestCatalogEpisodePublishedFilesSurviveAStalePendingMappingRetry` 注入 mapping 删除失败，证明公开状态与文件都保留，故障解除后重试也不丢文件。

## 验收清单

- [x] partial 时 complete 分支新增文件被发现；failed/restricted/pending 范围重试；unknown 不判缺失；重复发布不产生重复卡片。`TestCatalogSchedulePartialGenerationRunsIncrementalDiscovery`、`TestCatalogSchedulePartialWithPendingRangesStillProbesCompletedBranches`、`TestCatalogScheduleParentListingFailureKeepsChildrenUntilListingSucceeds`。
- [x] 进程关闭重启后同一 generation 续跑；手动刷新在运行中只排队一次，结束后执行一次。`TestSourceCatalogDailyStartsOnlyAfterCompleteAndSurvivesRestart`、`TestCatalogScheduleManualPendingConsumedOnce`、`TestManualFullIndexQueuesWhileIncrementalSyncRuns`。
- [x] 每日／周日 03:00 边界、两者同时到期、等于时间、跨日、重启追赶、低空间恢复均有 H/R 证据。`TestCatalogScheduleDailyBoundaryAndMissedCatchUp`、`TestCatalogScheduleSundayRunsFullReconciliation`、`TestCatalogScheduleSimultaneousDueCreatesOneWeekly`、`TestCatalogScheduleDeferredCatchesUpOnce`、`TestSourceCatalogDailyRetriesALowDiskPauseAfterOneHour`。
- [x] 低空间或维护 hold 只设置一次 `deferred_until`／`deferred_reason`；到期后重新检查条件，条件未解除时继续延后，不能误跑 full。`TestCatalogScheduleDeferredCatchesUpOnce` 与低空间测试断言原因和一次追赶；维护 hold 复用 `TestCatalogSyncPausesForMaintenanceAndRetriesAfterTheHold`。
- [x] queued full 不会被 `resume_full_pending` 重复创建；`resume_full_pending` 只在 full 终态 succeeded 后清零并推进 weekly due，failed/cancelled 保留待重试。`TestCatalogScheduleResumeFullPendingWinsAndClears`、`TestCatalogScheduleFailedResumeFullRetriesWithoutMovingWeekly`。
- [x] sync 运行中的手动刷新记一次 `manual_pending` 并在结束后执行一次；`force` 在 failed／partial／cancelled 后创建新 generation；同一来源不产生 full 与 sync 重叠写。`TestCatalogScheduleRunningSyncDefersFullAndKeepsDue`、`TestManualFullIndexQueuesWhileIncrementalSyncRuns`、`TestIncrementalSyncRefusesWhileFullGenerationRuns`。
- [x] D-16 的 0/1/4/5 audio、2000/2001 nonAudio、5000/5001 files 在入队／发布／浏览／搜索／tracks／单卡详情／个人评价／播放准备／媒体流／作品目录标签一致；系列成员逐条判定且不聚合。`TestCatalogAdmissionPublishingPublishesEveryCardWithAudio`（六个阈值边界）、`TestCatalogAdmissionAPIShowsEveryCardWithAudio`、`TestCatalogAdmissionSeriesFiltersEveryMember`、`TestCatalogAdmissionEntryPointsUsePublishedSet`（评价、播放准备、媒体流、目录标签四入口同判）。
- [x] 单卡个人评价写入、播放准备、媒体流与作品目录标签使用同一完整准入，`missing` 卡片仍可读。`TestCatalogAdmissionEntryPointsUsePublishedSet` 断言五个拒绝卡在 `PATCH /user-state`、`POST /playback`、`GET /media` 全部 404，`GET /works/{id}/catalog-directories` 只返回准入卡；`TestCatalogAdmissionZeroFileCardIsRefused` 断言零文件卡 404、`missing` 卡 200。
- [x] 系列没有任何可见成员时详情返回 404，而不是 200 加空列表。`TestCatalogSeriesOmitsEntriesWithoutASource`：删除来源后 `GET /api/catalog-series/{id}` 返回 404，保留卡片仍以 `missing` 可读且评价保留。
- [x] 零文件卡与旧 generation 残留行按同一集合判定。`TestCatalogAdmissionCountsCurrentPublishedFiles` 逐条断言 8801–8810 的 `catalogPublishedEntryAdmission` 与 `catalogEntryAdmittedSQL` 一致：旧 generation 行不计入、folder 行不计入、5000/5001、2000/2001、4/5 audio 边界全部与 `catalogadmission.Decision` 相同。
- [x] daily 任务创建成功后才推进 `daily_due_at`；已有 sync 或 `errCatalogIndexRunning` 时不推进。`TestCatalogScheduleDailyDueWaitsForSyncCreation`。
- [x] 诊断接口使用 `catalogadmission.ProbeFiles` 并按来源过滤，且排除 folder 与非当前 generation。`TestCatalogDiagnosticsOversizeCardsUseTheProbeBoundPerSource`。
- [x] 运行中的 full 遇 weekly 到期把当前 generation 写入 `pending_full_run_id`，成功后推进 weekly、失败保留待重试，不产生第二个 full。`TestCatalogScheduleRunningFullRegistersPendingRun`、`TestCatalogScheduleResumeFullPendingWinsAndClears`、`TestCatalogScheduleFailedResumeFullRetriesWithoutMovingWeekly`。
- [x] 性能计划断言真实执行计划且可失败。`catalog_plan_assert_test.go` 的 `assertNoCatalogFileTableScan` 在 `TestOpenListCatalogBrowseSinglePassPlan`、`TestOpenListCatalogLanguageSearchPlan`、`TestOpenListCatalogShortTranslationSearchPlan`、`TestCatalogPublishQueryPlanStaysDirectoryDriven`、`TestCatalogSplitPlanIgnoresPreviousRevisionFiles` 上全部通过（2026-10-26 复跑）。
- [x] 42k 卡浏览与 5,001 文件卡拒绝路径的 p95 证据（2 vCPU／3.5 GiB、warm、30 样本）。`104_source_catalog_entry_admission.sql` 把每卡三个有界计数与其 generation 存成一行，文件写入在触发器里失效该行，发布批次与手工快照在写文件的事务内刷新；`catalogEntryAdmittedSQL` 与 `catalogEntryHasAudioSQL` 先读该行，generation 不匹配时回落到实时计数，两条路径共用 `catalogadmission.Decision`。浏览路径的 work 去重改为只对已关联 work 的卡片做分区排名，未登记卡片按自身 id 唯一，搜索路径保留单次排名以维持 rowid 探测。目标机（2 vCPU／3,531 MiB，warm，30 样本，2026-10-27 复测）实测：42,624 卡浏览 p50 185.168 ms、**p95 199.192 ms**、p99 226.981 ms；5,001 文件卡拒绝 p50 0.420 ms、**p95 0.593 ms**、p99 0.596 ms。2026-10-25 的同一路径为 p95 523.334 ms。证据：`.kikoto-work/reports/catalog-42k-perf.json`（改进前）、发布记录中的 catalog-42k 报告（路径不入库）。
- [x] 096 激活期间系列详情/成员 tracks 不可见，publication 切换后才可见；系列详情与列表使用同一 D-16 成员过滤。`TestCatalogSeriesHidesPendingPublicationUntilItIsPublished`、`TestCatalogAdmissionSeriesFiltersEveryMember`。
- [x] 不重复实现 `134f7c90` 的规则；如发现其实现缺口，先报告再最小修正。本票只把常量接入 SQL 并补系列门禁。

**测试入口:** H、R，必要的纯规则 U；真实覆盖由 24 汇总。

## 实现与证据

- 迁移：`backend/migrations/098_catalog_schedule.sql` 创建 `source_catalog_schedule`，含 `deferred_reason` 与 `pending_full_run_id`，并只回填 enabled OpenList 来源。`backend/internal/storage/catalog_schedule_migration_test.go` 覆盖 84→098、合法旧值保留、过期/非法值回退、重复启动不重写；`migration_manager_test.go` 覆盖新装与增量链 schema 一致。`compat/098_dev.sql` 已由 `cad25804` 生成，验收时按已提交文件核对，不再沿用“本分支不生成”的旧说明。
- 调度：`source_catalog_schedule.go` 提供行生命周期与 due 计算；`source_catalog_daily.go` 在 `beginTxWithDatabaseBusyRetry` 中完成资源重查、resume 优先、manual 消费、单任务创建与同事务 due 推进。`source_catalog_schedule_test.go` 覆盖边界、同时到期、跨日追赶、deferred、resume 成功/失败、manual、disabled/enable、维护同步、父 listing 不完整。
- D-16：`catalogadmission.Probe*` 是唯一常量源；`catalogEntryAdmittedSQL`、发布计数和单卡 API 都由它生成。系列列表、详情和成员 tracks 对每个成员独立过滤，计数只取该成员 `source_catalog_file`。
- 096：系列详情在 pending publication 期间隐藏成员；publication 翻为 `published` 后同一查询可见。映射清理与发布状态同事务，`published` 是不可回退边界，注入清理失败后公开文件保持不变。
- 准入收口：个人评价、播放准备、媒体流与作品目录标签复用同一准入，系列空详情返回 404，准入计数限定当前 generation 并排除 folder，sync 先建行再推进 due，诊断按来源过滤，plan 断言可失败。上述行为已在集成分支上通过定向测试复验（2026-10-26）。
- 验证命令与结果记录在交付报告；42k 性能证据在目标机采集，报告保存在发布记录目录。

## 派发边界（裁决 2026-10-25）

`dispatchDueCatalogUpdates` 仍 `JOIN source_catalog_scan`，这是有意的，不是缺口。规格 D-05 规定首次索引由管理员启动、完成后才进入每日更新，所以新建或重新启用的来源只插入 `source_catalog_schedule` 行，在有首个 generation 之前不自动派发。不要为了“让新源立刻被调度”去掉这个 JOIN。

## Comments

本票保留既有目录优先算法，不用祖先 mtime 跳过整棵子树，不用搜索结果代替完整遍历。不能将 resume 同一 generation 当作跨 generation 增量。工作区未提交的 `source_catalog_daily.go`、`source_catalog_sync.go` 和 `source_catalog_schedule.go` 必须先取得绿色结果，否则从 `134f7c90` 重建，不得带入集成。
