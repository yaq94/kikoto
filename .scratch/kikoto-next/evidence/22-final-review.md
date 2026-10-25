# 22 规格轴最终审查（AC-01..AC-28）

> 历史证据，非当前候选。审查对象为部署 `2bae5db9` 与 schema 83；仓库路径
> 已改为通用模板，生产计数改为量级。

## 审查范围与基线

- 仓库 `<repo>`，分支 `codex/kikoto-next-integration`，HEAD `1920a8d0a60b15d4febbe037ea308d9c81dc19ab`。该 HEAD 相对部署提交 `2bae5db98acceff6b45fdb4d168908c7b0da9177` 只多两个 docs 提交（`0f70702f`、`1920a8d0`，仅改 `.scratch/kikoto-next` 证据与执行记录），应用代码等价。
- 工作区不干净：共 8 个已跟踪文件被改，其中 7 个是后端代码/测试（`source_catalog_index.go`、`source_catalog_index_store.go`、`source_catalog_index_worker.go`、`source_catalog_loose_works.go`、`source_catalog_split_identity_test.go`、`work_matching_scope_test.go`、`storage/migration_manager_test.go`），另 1 个是证据 `20-post-review-fixes.md`；`backend/migrations/084_source_catalog_orphan_cursor.sql` 未跟踪。084 未提交、未进镜像；当前部署仍是 `kikoto:next-2bae5db9`、schema 83。
- 合同：`spec.md` 的 AC-01..AC-28 与 Testing Decisions。证据按 01–21、`index-calibration.md`、`issues/*.md` 核对；历史生产 run 采用审查输入（约 1.4 万目录中绝大多数完成、4 failed、27 unconfirmed empty、约 4.2 万张可播放卡、约 4.26 万未登记），不重复验证。
- 本次为只读审查，只新增本文件；未改源码、测试、工单或 Git 状态。

## AC 总表

| AC | 结论 | 实现 | 证据 | 缺口 |
| --- | --- | --- | --- | --- |
| AC-01 | 满足 | `translation_settings.go:getTranslationSettings/updateTranslationSettings`；`translation/settings.go:Store.Update/Load`；`server.go:356-357` | 01 §AC 与入口 1–2 行；21 §内容翻译首段 | — |
| AC-02 | 满足 | `translation_trial.go:testTranslation`；`translation/client.go:Translate/outputTokenBudget` | 01 §AC 与入口 3–4 行；01 §LIVE 结果；01 §HEAD LIVE 复验 | — |
| AC-03 | 满足 | `content_translation.go:translateContent`；`content_translation_fields.go:loadTranslationField`；`translation_runtime.go:cachedTranslation`；`server.go:351-352` | 02 §六项清单 1–2 行；02 §当前组合检查；21 §内容翻译 | 生产只回读标题译文；四类字段由 H/F 覆盖，非阻断 |
| AC-04 | 满足 | `translation/protection.go:Protect/Restore`；`content_translation_fields.go:catalogEntryProtectedNames/catalogSeriesProtectedNames` | 02 §六项清单 3；03 §验收清单 3；21 §内容翻译 | — |
| AC-05 | 满足 | `translation/queue.go:Do`；`components/content-translation/ContentTranslation.tsx:TranslatedText` | 02 §六项清单 4；02 §当前组合检查 F 2/3 | — |
| AC-06 | 满足 | `translation/cache.go:CacheKey/Get/Put/trim`；`translation/queue.go:Do`；`translation/settings.go:DefaultLimits` | 02 §六项清单 5–6；03 §验收清单 1 | — |
| AC-07 | 满足 | `translation/traditional.go:Traditional`；`content_translation.go:translateContent` | 03 §验收清单 1–4；03 §LIVE 正文与次数；03 §HEAD LIVE 复验 | — |
| AC-08 | 部分 | `openlist_catalog.go:listOpenListWorks`；`source_catalog_index_worker.go:executeSourceCatalogIndexJob`；`library_list.go:librarySearchWhere` | 07 §AC 映射；08 §已取得的红绿证据/AC 与后续；21 §来源目录 | 库级搜索不含未登记 catalog；21:49“列表或搜索不过滤”只对来源范围成立；见缺口 4 |
| AC-09 | 满足 | `work_code_read.go:workReadID/resolveWorkCodeIdentity`；`user_state.go:updateWorkUserState` | 06 §实施与证据；07 §AC 映射 AC-09；10 §已验证行为 AC-09 | — |
| AC-10 | 部分 | `catalog_directory.go:getWorkCatalogDirectories`；`catalog_availability.go:catalogDirectoryNeedsCheck/enqueueCatalogDirectoryCheck` | 05 §红绿证据 1–6；20 §第三轮审查修复（未知/过期）；21 §多来源 | `catalog_directory.go:132` 仍回退 `updated_at`；见缺口 5 |
| AC-11 | 满足 | `catalog_playback.go:prepareCatalogPlayback/streamCatalogLocation`；`catalogPlayback.ts:catalogPlaybackQueue/catalogResumeQueue`；`playerPersistence.ts:parsePersistedQueue` | 05 §红绿证据 7–9/§当前验证；21 §多来源/媒体 | — |
| AC-12 | 满足 | `user_state.go:updateWorkUserState`；`personal_review.go:patchWorkUserState`；`openlist_review.go:updateSourceCatalogUserState`；`catalog_series_review.go:updateCatalogSeriesReview` | 04 §验收清单；07 §AC 映射 AC-12；12 §验收清单；21 §多来源 | — |
| AC-13 | 满足 | `personal_review.go:patchWorkUserState`；`openlist_review.go`；`personal.go:Export/Import` | 04 §验收清单；13 §当前验证；16 §当前验收 AC-13；21 §多来源 | — |
| AC-14 | 满足 | `catalog_series_discovery.go:attachCatalogSeriesTx/splitCatalogEpisodeRoots`；`catalog_series.go:createCatalogGroupingPreview/catalogGroupingPreview`；`catalog_series_identity.go:foldCatalogSeriesTx` | 11 §行为与证据；20 §审计收敛 B1/B2、格式副本尺寸匹配 | 副本检测仅比大小（08 §已知边界）；见缺口 9 |
| AC-15 | 满足 | `catalog_series_review.go:updateCatalogSeriesReview`；`catalog_series.go:getCatalogSeries` | 12 §验收清单；15 §审计后补证 | — |
| AC-16 | 满足 | `shared_association.go:createSharedAssociationPreview/startSharedAssociation/decideSharedAssociation/commitReadyAssociationTx` | 13 §当前验证；20 §审计后补证；21 §多来源 | — |
| AC-17 | 满足 | `work_matching.go:matchWorkEvidence/evaluateWorkMatchTx/matchWork`；`work_matching_worker.go`；`work_matching_scope_test.go:TestWorkMatchingSkipsRetiredCatalogSubjects` | 14 §当前产物证据/审计后修复；20 §14 匹配 | 生产 control 未终态、退役对象过滤为未提交改动；按规格不要求队列跑完，见缺口 10 |
| AC-18 | 满足 | `shared_association.go:mutateSharedAssociation`；`shared_split.go:loadSplitSnapshot/splitRecordChange`；073 迁移 | 13 §当前验证；15 §已取得证据；11 §行为与证据 | — |
| AC-19 | 满足 | `shared_split.go:commitReadySplitTx/decideSharedSplit` | 15 §已取得证据；15 §审计后补证 | — |
| AC-20 | 满足 | `source_catalog_relocation.go:reconcileCatalogEntryTx/relocateCatalogEntryTx`；`catalog_series_identity.go:catalogSeriesRenameEvidenceTx` | 10 §已验证行为 AC-20；11 §审计后修复；20 §B1/B2 | — |
| AC-21 | 部分 | `source_catalog_index_worker.go:executeSourceCatalogIndexJob/publishCatalogQueue`；`source_catalog_index_store.go:completeCatalogDirectoryTx`；`source_catalog_loose_works.go:retireStaleFormatCardsTx`；084 | 08 §真实增量复测/生产终态；09 §行为与验收；21 §部署期间重启；20 §部分扫描下的旧拆分卡 | run 53 终态/清理未入 21；生产仍有重复拆分卡；084 未提交未部署；见缺口 1–2、12 |
| AC-22 | 满足 | `source_catalog_daily.go:dispatchDueCatalogUpdates/dispatchCatalogUpdate`；`source_catalog_index_worker.go:catalogScanSchedulesDaily`；`job_runner.go` | 09 §行为与验收对应；09 §红绿与验证 | 生产 next_update 未记录（非合同必需），见缺口 8 |
| AC-23 | 满足 | `source_catalog_lifecycle.go:settleCatalogLifecycleBatch/cleanupCatalogPublishBatch`；`unlinked_works.go:deleteUnlinkedWorkFamiliesTx`；`media_cleanup.go:finishMediaCleanupForget` | 10 §已验证行为 AC-23/清理；10 §审计后修复 | — |
| AC-24 | 满足 | `personal.go:exportPersonalData/importPersonalData`；`personal/transfer_export.go:Export`；`transfer_import.go:ImportWithRegistration`；`transfer_identity.go` | 16 §当前验收；16 §命令与退出状态 | — |
| AC-25 | 部分 | `source_catalog_index.go:defaultCatalogIndexOptions/validate`；`source_catalog_index_worker.go:catalogIndexDiskPause`；`translation/settings.go:DefaultLimits`；`translation/queue.go:Do`；`translation/cache.go:Put/trim` | 02 §六项清单 6；08 §AC 与后续；09 §行为；21 §媒体与组合负载；index-calibration §4 | 低空间与播放/扫描同时触发未验证；见缺口 3 |
| AC-26 | 部分 | 验收产物（无单一实现）：21/17/index-calibration；01/03/07 LIVE | 21 §产物与审查/部署与数据/媒体/浏览器；17 §当前产物/组合验收入口；index-calibration §1–5 | 证据只到 2bae5db9/schema 83；084 未纳入；run 53 终态未写；见缺口 6–7 |
| AC-27 | 满足 | `outbound/policy.go:Policy.ValidateURL/Transport/checkRedirect/dial`；`translation_trial.go:newTranslationClient`；`source_transport.go` | 01 §AC 与入口 AC-27 3 行；02 §共同 AC-27；05 §当前验证；07 §AC 映射；08 §AC 与后续 | — |
| AC-28 | 满足 | `frontend/tests/e2e/{content-translation,traditional-translation,translation-settings,personal-review-live-api,source-playback-live-api,catalog-series,work-matching,integrated-acceptance,openlist-catalog,mobile-workflows}.spec.ts`；`scripts/run-e2e-fixtures.mjs` | 19 §AC-28 证据范围；21 §浏览器与真实样例；20 §19 CI | 仅对应 2bae5db9；084 未跑 F；见缺口 6 |

## 缺口清单（按严重度）

1. **高：AC-21 生产重复拆分卡未消除。** 20:318-343 实测同一来源目录、同一相对音轨出现两张卡（一个历史示例来源目录的旧卡与新卡）；修复 `retireStaleFormatCardsTx`（`source_catalog_loose_works.go:1193`）与 084 仍在工作区，未提交、未部署。修复：提交 084 + worker/store 改动，跑 `TestSourceCatalogPartialRunRetiresOrphanSplitCards` 与 `TestSourceCatalogLinkedSplitCardKeepsSiblings`，部署 schema 84 后强制新 generation（或跑完孤儿清理），用 `source_root + relative_path` 查重复为 0，再更新 21。
2. **高：AC-21 run 53 终态与 generation 清理未入证据。** 21:121-124 仍写“run 53 尚未到达终态”；21:54 仍用前一次运行的约 1.4 万目录与 6 failed。修复：在 21 增加 run 53 表（status、complete/failed/empty/pending、卡片数、`publish_pending`、`orphan_cleanup_cursor`、generation 清理结果、4 条 U+FFFD 失败清单），更新 17 §未完成项；不要把 partial 写成完整覆盖。
3. **高：AC-25 低空间与播放/扫描组合未验证。** index-calibration:97-101 明确低空间是“单独以翻译配置触发”；21:75-82 的组合负载没有暂停。修复：在真实音轨播放 + 全量扫描 + 翻译同时运行时把 `minFreeBytes` 提到可用空间之上，记录扫描/缓存进入 `paused_low_disk`、播放器继续推进、队列与进度保持；恢复阈值后记录续跑和原文/记录可读。
4. **中：AC-08 统一搜索口径。** 21:49 称未登记卡片“不会被列表或搜索过滤”，但 `library_list.go:150 librarySearchWhere` 与 `library/search.go` 没有 `source_catalog_entry` 联结；只有 `openlist_catalog.go:484 listOpenListWorks` 的来源范围搜索能搜到未登记卡。修复：要么把 catalog 卡片并入库级搜索（按共享身份去重），要么把 21 的表述限定为“来源范围搜索”，并补一条库页搜索的 F 断言。
5. **中：AC-10 checkedAt 仍回退 updated_at。** 20:257 声称“目录列表的 checkedAt 不再用 updated_at 冒充检查时间，从未检查的卡片明确为空”，但 `catalog_directory.go:132` 仍是 `COALESCE(status_checked_at,updated_at)`；`catalog_availability.go:27-31` 对解析失败也按需要检查处理。修复：直接选取 `status_checked_at`（NULL 输出空串），`updated_at` 只作独立展示字段；补 H：`status_checked_at=NULL` 且 `updated_at=now` 时 `checkedAt` 为空且入队检查。另需说明 `source_catalog_index_store.go:586` 在发布时写 `status_checked_at` 是否算“已检查”。
6. **中：AC-26 产物与证据版本未覆盖工作区。** 21:8-15 只写 2bae5db9/schema 83；当前分支 HEAD 是 1920a8d0，且工作区有 7 个后端改动、20 证据改动和未跟踪的 084。修复：在 21/17 明确“代码等价提交”或提交并部署 084；对最终 SHA 重跑 Full CI，记录镜像 ID、schema、未纳入证据的未提交改动。
7. **中：工单 17 的部署链接与状态过期。** 17 工单 24/27 行引用 `17-deployment.md#组合前后端验收同一当前构建` 与 `#真实来源索引发布前同机构建族`，但 17 现有标题只有当前产物/部署与迁移/回滚路径/备份恢复/组合验收入口/未完成项；30 行仍写 2026-10-17、schema 68→76。修复：锚点改到 21 §媒体与组合负载、index-calibration §4、21 §部署与数据；更新部署日期/schema；勾选最终交付项。
8. **中：AC-22 生产每日触发证据。** 09 的 H/R 用受控时钟证明调度，但 21 没有记录运行 53 的 `next_update_at` 或“无重叠全站任务”的查询。修复：只读查询该来源的 scan 行与 `app_setting` 调度值，记录 `next_update_at`、`pause_reason`、当前/排队 run；若 run 53 因 failed 不调度，明确写出。
9. **低：AC-14 副本检测只比文件大小。** 08:256 自述 `content_hash` 全为空，`source_catalog_loose_works.go` 的副本检测按尺寸 90% 匹配；20 §格式副本尺寸匹配已补一一匹配和重复尺寸反例，但没有同大小不同内容样例。修复：加内容哈希或同大小不同内容夹具；否则在 14/08 证据中登记“尺寸启发式”的残余风险。
10. **低：AC-17 退役对象过滤未提交。** `084` 的 `work_matching_subject` 视图与 `work_matching_scope_test.go:93 TestWorkMatchingSkipsRetiredCatalogSubjects` 只在工作区；生产 control 仍会为已退役对象消耗预算。修复：提交视图/测试并部署；control 的最终游标与结果数在收尾时记录，但不作为 AC-17 的完成条件。
11. **低：21 的目录分母口径不一致。** 21:50 写“约 1.4 万个来源目录”，run 53 分母略小；21:54 又混用前一次运行的目录数。修复：补一张 run 53 单表，解释目录表与扫描分母的少量行差异（保留/已删除/迁移行），并删除混用的旧运行数字。
12. **低：18/19 工单勾选与 Full CI 终态不一致。** 21:12 记录 `39496305624` 27 job 全 success，但 18 工单 22 行仍 `[ ] 主集成完成最终 Full CI`，19 工单 4 行仍写“最终 Full CI 后签收”。修复：按实际 run 勾选/更新 18/19，或在证据中说明该 run 对应的 SHA 与 18/19 的签收关系。

## 结论

- 28 条 AC：**23 满足、5 部分、0 未满足**。部分项为 AC-08、AC-10、AC-21、AC-25、AC-26。
- 当前部署 `2bae5db9`/schema 83 的功能主链可用，翻译、多源、个人记录、媒体 Range、AC-27 出站边界与 AC-28 非视觉浏览器证据成立；`work_matching_control` 未终态不构成 AC-17 的合同缺口。
- 阻断收尾的是 AC-21：生产仍可见旧拆分卡重复，084 修复未提交/未部署，run 53 终态与 generation 清理未写入 21。AC-25 的低空间组合、AC-26 的工作区版本覆盖、AC-08/AC-10 的搜索与 checkedAt 口径需要补证据或收窄表述后才能给全绿。

## 修复后的状态（2026-10-21）

本审查在修复前执行。下表区分“已修复并有回归证据”和“待最终部署复验”。

| 缺口 | 状态 | 处理与证据 |
| --- | --- | --- |
| 1 生产重复拆分卡 | 已修复，待部署验证 | 孤儿退役按根推进并持久化游标；部分扫描也 retire 完整根。提交 `19580e09`、迁移 084；`TestSourceCatalogPartialRunRetiresOrphanSplitCards`（修复前 64 张、修复后 62 张） |
| 2 run 53 终态 | 已取得，待写入 21 | run 53 于 2026-10-20 23:21:38 收于 `partial`，最终 summary 已读取；具体生产计数保留在受限运维记录 |
| 3 低空间与播放/扫描组合 | 待部署后复验 | 需在真实播放与扫描同时进行时触发暂停并记录 |
| 4 库级搜索口径 | 已确认边界 | 库页搜索不含未登记来源卡；21 的表述将收窄为“来源范围搜索” |
| 5 checkedAt 回退 | 已修复 | 只读 `status_checked_at`；`TestCatalogDirectoryListingChecksUnknownAndStaleTabs` |
| 6/7 产物版本与工单链接 | 待最终部署 | 需按新镜像与 Final CI 更新 17、21 |
| 8 每日触发证据 | 待部署后查询 | 记录 `next_update_at` 与扫描行 |
| 9 尺寸启发式 | 已收紧，仍有残余风险 | 父目录覆盖率 ≥80% 且限本代 `complete`；`TestCatalogDuplicateDirectoryRejectsSubsetOfLargerParent`、`TestCatalogDuplicateDirectoryIgnoresStaleParentSizes` |
| 10 退役对象过滤 | 已修复 | `work_matching_subject` 排除退役卡；`TestWorkMatchingSkipsRetiredCatalogSubjects` |
| 11 目录分母 | 待写入 21 | 统一为扫描分母的量级，并说明目录表与扫描分母的少量行差异 |
| 12 18/19 勾选 | 待最终 Full CI | 按最终 run 更新 |
