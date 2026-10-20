# 双轴审查后修复与复验（2026-10-20）

**基线：** `af59653b` → 本批 HEAD；集成分支 `codex/kikoto-next-integration`。
**范围：** 目录拆分身份、删除确认、sidecar 证据与重试、翻译变体、共享关联、个人导入 keep 语义、显示语言回退。

## 修复

1. 无编号单作品不再因音轨名不同被拆卡：`Prologue`/`Main Story`/`Epilogue`、`Opening`/`Ending`、`表妹上/中/下`、`01..03 + Bonus` 保持一卡。
2. 尾随数字只在形成 1..N 连续序列时视为音轨号；`Room 101`/`Room 102`、有标题的 `T001`/`T002` 与裸 `T001`/`T002` 保持独立。零填充编号不剥离。
3. `第1章`/`第2章` 是作品内章节，不再成为单集；`第1話`/`第2集` 仍是单集。`Episode 1 (MP3)` 折叠进相邻的 `Episode 1`。
4. 已评价的 creator 卡只保留 episode、格式、附属与分轨子目录；新的无编号作品获得自己的卡。
5. 有限扫描/刷新重写整个根后，未再出现的卡标为 `missing`，保留 id、work 关联与个人记录；再次出现恢复 `available`。
6. 全量索引最终阶段退役“根不再拆分”的旧 split 卡；每批最多 100 个根，带 work/系列/别名的根跳过。
7. 404/410 记为确定缺失（`empty` + `not_found`），不再让整源永久 `partial`；刷新路径同样标 `missing`。
8. sidecar 证据只在该卡范围完整观察后清除；partial 扫描保留证据并清空失败/重试字段。队列查询使用 partial index，写入失败不再无 tick 空转；cleanup 游标只前进，uncertain 范围保留旧行直到后续 generation 完整观察；旧代 `source_catalog_audio_root` 回收。
9. 目录页与 sidecar 共用同一 origin 速率令牌。
10. `keep` 导入只写备份中确实存在的字段；同一 shared identity 的任一成员冲突保护整个身份。
11. 翻译使用读者选中的 edition 文本，既有字段只在对应同一源文本时复用；catalog 翻译沿 `relocated_to_id` 读取 successor 文件。
12. 共享关联拒绝已 relocated 的 catalog 对象（409 stale），写入使用 successor。
13. 显示语言偏好读取失败时保留上次成功选择，不再固定回退 `origin`。

## 验证

| 检查 | 结果 |
| --- | --- |
| `cd backend && go test ./... -count=1` | 通过，exit 0（32 个包含测试的包） |
| `make backend-format backend-vet backend-build` | 通过；lint 首次因默认 GOPROXY 超时，改用 `GOPROXY=https://goproxy.cn,direct` 后 `0 issues` |
| `go vet ./internal/httpapi ./internal/personal` | 通过 |
| 前端 `npm run typecheck:e2e`、`npm run lint`、`npm run build` | 通过（Node 24.19.0 / npm 11.17.0） |
| `npm run test:unit -- --run --maxWorkers=4` | 155 文件、874 测试通过 |
| 新增 H 用例 | `TestSourceCatalogPublishesNamedPartsAsOneCard`、`...NumberedReleaseWithBonus...`、`TestCatalogSplitModeKeepsNumberedTitlesDistinct`、`TestCatalogSplitModeKeepsBareCodesApart`、`TestSourceCatalogEpisodeFormatCopyStaysWithEpisode`、`TestSourceCatalogDeletedDirectoryBecomesMissing`、`TestSourceCatalogPartialRescanKeepsSidecarEvidence`、`TestSourceCatalogReviewedCreatorAdmitsNewNumberlessWork`、`TestSourceCatalogRetiresOrphanSplitCards` |
| `make sensitive-check` | 通过 |

## 未覆盖

- 真实 sources 上的分页/429/改名恢复未在本批重新构造；以现有 H/R 用例与部署后实跑为准。
- 浏览器功能用例未在本批重跑；前端仅执行类型、静态、构建与 874 项纯逻辑测试。
- 生产数据清理与索引终态在部署后记录于部署证据。

## 收尾修复（2026-10-20 18:xx）

14. `catalogRangeUncertain` 把配置根 `/`（以及空 base）按整棵树判断；根卡下的失败子目录不再让 lifecycle 误删旧文件。
15. `cleanupCatalogPublishBatch` 对根卡复用同一范围谓词，不再因“根卡存在”永久跳过清理。
16. `catalogRootIncomplete` 不再把 `empty + not_found` 当作未完成；确认 404 的格式卡可以退休，未确认空目录仍然保留。
17. 格式目录质量后缀改为逐段解析，`FLAC 24bit 96kHz`、`FLAC 24bit/96kHz`、`WAV 24-96` 不再被当成标题。
18. 系列详情过滤 `missing` 与已 relocated 的成员；单集重新发布后自动回到系列视图。

新增回归：
- `TestCatalogRangeUncertainAtConfiguredRoot`
- `TestCatalogFormatDirectoryAcceptsMultipleQualitySuffixes`
- `TestCatalogSeriesOmitsMissingEpisodesUntilRepublished`

复验：后端 `go test ./...` exit 0；前端 typecheck/lint/build 与 155 文件、874 测试通过；`make backend-lint` 0 issues；`make sensitive-check` 通过。

## 目录索引写锁与吞吐（2026-10-20 19:xx）

依据 `E:\项目\服务器\.kikoto-work\index-write-contention.md`（本机施工单，不入库）。
基线 run 50：并发 2、间隔 300ms、99 目录/分钟、146 分钟；期间 116 次 `SQLITE_BUSY`、202 次 `context deadline exceeded`，与目录作业起止强相关。

改动：

1. 批次常量与哨兵成对定义：发布 8 批 × 50 行（每 pass 仍 400 行），清理保持 4 批 × 100 行；`LIMIT` 与"队列已满"哨兵共用同一个常量。
2. 每个写事务成功后让出 8ms 写锁；页面写同样暂停。`catalogIndexNoPacingKey` 仍可在契约测试中关闭。
3. 扫描默认值曾改为并发 2、间隔 500ms；第三轮审查后恢复为 D-10 的并发 1、间隔 1000ms，见下一节。UI 1–4 上限不变。
4. 扫描开始前清除旧 generation 的 `publish_pending` 标志（只改标志，不动行、entry、file、个人记录），每次最多 16,000 行。
5. `work_matching_worker` 三处状态写入改用 `sqlutil.ExecBounded`，锁冲突时归还连接。
6. 清理游标曾在末页写负值重试标记。第三轮审查确认该标记在整页、跨页或本 pass 直接成功时无法可靠消费，已按下一节改为只前进。
7. 单次写事务超过 500ms 记录一条 Warn（kind、rows、elapsed_ms）。

测试：

- `TestCatalogWritePauseHonoursContext`
- `TestPublishCatalogQueueSentinelMatchesBatchRows`
- `TestPublishCatalogBatchLimitMatchesConstant`
- `TestSourceCatalogPurgesStalePendingBeforePublish`
- `TestSourceCatalogPendingPurgeBounded`
- `TestWorkMatchingWriteBoundDoesNotHoldConnectionWhileLocked`
- `TestSourceIndexWriteContention`（`KIKOTO_SOURCE_INDEX_WRITE_PERF_ASSERT=1` 时断言 busy=0、最长获锁 <5s、drain <2×持锁）

本批无迁移，schema 仍为 82。

补充测量（2026-10-20 20:xx，`.kikoto-work/probe-publish2.db`，14,459 个当前代目录 / 14,459 条 audio_root / 266,296 条旧代 pending）：

| 发布查询形态 | LIMIT 100 |
| --- | --- |
| 原 `WITH roots AS MATERIALIZED` + JOIN | 88.3 ms |
| 无 CTE、观察行优先 + EXISTS | 0.3 ms |
| 目录优先 + EXISTS（采用） | 0.2 ms |

`EXPLAIN QUERY PLAN` 显示原查询对每个候选目录扫一遍物化的 roots（`SCAN r`）并额外做临时排序；目录优先的形态用 `source_catalog_directory` 主键顺序，无需临时排序。schema 不变，仍为 82。

发布几何保持施工单确认的 8 批 × 50 行（每 pass 400 行）。曾一度改回 4 × 100，依据是 -race 契约套件的 891s 与 846s 对比；复测后确认那 45s 差异来自每个事务的固定成本（当时的 88ms CTE 扫描），而不是批次大小，且 -race 本身放大约 5–10 倍。CTE 修掉后单批固定成本降到 0.2ms，更小的批次才真正缩短单次持锁时间，因此恢复 8 × 50。清理保持 4 × 100，避免把清理扫描量翻倍。

`catalogSlowWriteThreshold` 随 `catalogIndexNoPacingKey` 一起关闭，避免 race 开销淹没真实信号。

## 审查后修复（2026-10-20 21:xx）

第三轮双轴审查发现两项必须修，均已修复：

1. **清理游标的“回卷重试”没有达到契约。** 回卷只在一页以内、且下一次调用仍在同一 run 内发生时才被消费；恰好整页、跳过卡在前页、或本 pass 直接 `succeeded` 时，负值标记要么永不被消费，要么重复扫描。`catalogRangeUncertain` 只读当前 generation 的目录状态，这些状态在 publish 阶段已经固定，所以同一 run 内重扫不可能改变结论。现改为：
   - `cleanupCatalogPublishBatch` 不再写负值标记，游标保持单调前进；uncertain 范围保留上一代文件行，等后续 generation 完整观察后再删除。
   - 读取到旧版本写入的负游标时从 0 开始一次，随后继续前进。
   - **resume 同一 generation 时把 `lifecycle_stage`、`lifecycle_cursor`、`publish_cleanup_cursor` 一并归零。** resume 会把 failed/restricted/empty 目录重新排队，因此可能发布上次已经越过的条目；只归零发布游标不够，lifecycle 若停在 `done` 会让这些条目永远没有 available/missing 状态。两项派生游标都从头重跑，操作幂等（只重写状态、只删上一代文件行）。新增 `TestSourceCatalogResumeRestartsDerivedState`、`TestSourceCatalogSucceededRunStartsANewGeneration`。
   - `source_catalog_index_worker.go` 的批次上限与 `scanned` 判断不变，一次 pass 必然终止。
   - 新增 `TestCatalogCleanupKeepsUncertainRowsAndAdvancesCursor`：3 张卡、其中一张子树 `failed`，断言 uncertain 卡保留旧行、其余删除、游标停在尾部、第二次调用扫描 0 行、旧版负游标被消费一次、子树恢复 complete 后旧行被删除。
2. **默认扫描速率与 D-10 不一致。** `defaultCatalogIndexOptions` 恢复为并发 1、间隔 1000 ms，与规格 D-10 当时的默认起点一致；管理员仍可在 1–4 / 100–60000 ms 内上调。生产强制全量扫描按管理员显式参数运行，不用默认值。规格随后修订：D-10 的目录扫描一行改为“按来源独立配置，内置值只作为未校准来源的起点”，并明确起点值不是性能上限，本条不再构成对实测调参的限制。

文档同步：

- `docs/development/openlist.md`：发布改为目录优先 + `EXISTS`，几何改为 8×50；删除“游标回卷重试”的错误描述。
- `docs/architecture/data-model.md`：同一处发布查询与清理语义。
- `docs/development/source-catalog-index-performance.md`：记录生产规模副本上 0.2 ms / 88.3 ms 的对比，并把旧的 4×100、物化 roots 描述标记为历史形态。
- 发布查询提取为 `catalogPublishQuery`，性能 harness 直接打印生产查询的计划，避免文档与实现再次分叉。

复验：`go build ./...`、`go vet ./internal/httpapi`、`go test ./internal/httpapi -run TestCatalogCleanupKeepsUncertainRowsAndAdvancesCursor` 通过；文档链接检查 131 个文件全部解析。

## 第四轮复审（2026-10-20 22:xx）

复审确认三项：

1. resume 路径不重置派生状态，已按上一节最后一条修复；两个新用例覆盖“resume 归零”和“succeeded 开新代”，均通过。
2. `data-model.md` 中“resumed generation 复位游标”的表述已改成与实现一致：resume 归零，单次 attempt 内只前进。
3. 性能文档里的 0.2 ms 实测是 `LIMIT 100`，现已注明当前批次为 50 行、查询计划相同；`evidence/20` 的表格保留原始测量条件。

测试缺口（清理游标的整页 100、跨页 150、worker 直接 succeeded）不是缺陷：worker 只在 `failed+restricted==0` 且根目录非空时进入清理阶段，partial 运行不会推进游标；`TestCatalogCleanupKeepsUncertainRowsAndAdvancesCursor` 用三张卡直接覆盖“uncertain 保留 + 游标前进 + 二次调用 0 行 + 旧负游标消费一次 + 子树恢复后删除”，整页/跨页只改变批次数，不改变每次调用的判定。

## 发布查询的联接顺序（2026-10-20 22:4x，部署后实跑发现）

部署 `bd120ea6` 后以 `force:true`、页 500、批 32、并发 2、间隔 300 ms 启动 run 51。约 2 分钟内出现 27 条 `SQLITE_BUSY` / `context deadline`、连接池等待 25.1 s、`publish_batch` 单批 8–16 s，登录返回 503，索引速率约 14 目录/分钟。已在 47 请求处取消 run 51。

原因：生产库上 `EXPLAIN QUERY PLAN` 选择了**观察行优先**的联接顺序，并对全部待发布观察做临时排序（`USE TEMP B-TREE FOR ORDER BY`）。上一代 run 50 留下 266,354 条 `publish_pending`，每批发布都会先排序整个积压，才会按 `LIMIT` 截断。审查阶段的探针库统计信息不同，因此当时看到的是目录优先计划，没有发现这个分支。

修复：`catalogPublishQuery` 用 `CROSS JOIN` 固定「目录 → 观察」的联接顺序。目录主键 `(file_source_id,path)` 直接提供 `ORDER BY d.path` 前缀，观察行按 `(file_source_id,directory_path)` 与 pending 部分索引点查。生产库只读实测：同一批 50 行从数秒降到 10 ms。新增 `TestCatalogPublishQueryPlanStaysDirectoryDriven`：先造 400 条上一代 pending，再断言计划没有 `TEMP B-TREE`、且 `SEARCH d` 在 `SEARCH o` 之前；这个用例在旧写法下会失败。

注意：这一批问题的触发条件是「上一代留下大批 pending」。新库或已清空积压的源上，旧写法也能通过功能测试，因此性能回归必须用计划断言而不是只看结果行数。

## 审计收敛修复（2026-10-21）

审计 A–F 的阻断项以 `a50cd62c` 为固定基线逐项复现。下列每项都先有失败测试或可观察红例，修复后在同一工作区通过。

### 系列改名与删除来源（工单 10/11）

- **B1 改名**：原 relocation 只迁移 entry 与 file，不迁移 `catalog_series_member`，并把新位置当成 survivor，旧 id 的成员与卡片脱节。现以已建立的 entry 为 survivor：新位置的系列成员并入旧 entry（同系列合并，异系列保留给复核），新位置成为 alias；系列详情从 successor 取标题、来源与可用性；详情接口沿 alias 链返回 `seriesId`。
- **B2 删除来源**：原系列查询只看 `availability_status<>'missing'`，删除 `file_source` 后仍列出成员。现要求当前源或 successor 仍存在；全源删除的卡片保留为 `missing`，个人记录仍可读。
- **reconcile 前置**：新发布的 successor 在本代 fingerprint 尚未落盘时会被判 `insufficient_evidence`。reconcile 前先补齐本代 manifest；改名不再因遍历顺序失败。
- 测试：`TestCatalogSeriesRenameKeepsOneMemberAndSuccessorNavigation`（真实扫描/发布，`-count=3` 通过）、`TestCatalogSeriesOmitsEntriesWithoutASource`、`TestCatalogSeriesFollowsRelocatedEpisodeToItsSuccessor`、`TestCatalogSeriesOmitsMissingEpisodesUntilRepublished`。
- 另补真实发布卡：`TestCatalogScanPublishesZeroPaddedWorksApart`、`TestCatalogScanKeepsDifferentEpisodeCountersApart`；第1話与第1集共享数字时按不同计数器保留两条成员。

### 08 完整覆盖与低空间

- 未确认空目录不再让 run 显示 `succeeded`；只有 404/410（`empty + not_found`）可排除。测试 `TestSourceCatalogIndexNonRootUnconfirmedEmptyStaysPartial`、`TestCatalogScanTreatsGoneDirectoryAsMissing`。
- 首次 publish 前检查磁盘；恢复运行不会先发布积压。测试 `TestSourceCatalogIndexLowDiskBlocksTheFirstPublishOfAResumedRun`。
- lifecycle 仍可为已完整观察的范围结算，未确认空子树由 `catalogRangeUncertain` 单独跳过。

### 09 低空间暂停与每日调度

- `pause_reason='low_disk_space'` 时忽略已存每日时间，不再 24 小时后自动重试；测试保留旧设置后断言不派发。
- 到达树尾、仅剩未确认空的 run 会安排次日同代重观察；这是恢复同一 generation，不是独立全站任务。文档与证据按实际行为更正。

### 14 匹配

- sidecar 双方都有 circle 且不同时判为冲突，不再因声优相同命中 `title_voice_credits`；新增反例 H。
- 补 2 MiB 证据上限、1,000 次/日自动操作上限、第三次失败转终态三项测试。

### 13 / 15 共享关联与拆分

- 13：`myConflictFields` 的 rating、note、组合、相同与空值规则有纯规则表；关联后两个 work 的 edition 标签保持原值。
- 15：一个系列加两个单集的拆分保留成员、work、edition 与 media 定位；新增 `TestSharedSplitSeriesKeepsTwoEpisodesAndTheirPlaybackEditions`。

### 02 翻译

- 远端 listing 没有 description，`sourceId + field=description` 明确返回 400 `translation_invalid_input`，不再落成 stale 409。
- 已有目标语言 edition description 直接复用，不调用模型；测试 `TestContentTranslationReusesAnExistingEditionDescription`。
- F 先回到 work root，再展开目录，断言展开后的 `ZH: Example track.wav`，覆盖“展开目录后的新增文字”。
- 全局手动简介没有语言列，属于所选原文而不是“已有目标语言字段”，不参与 existing 复用。

### 19 CI

- 新增 `mobile-shell` group，按 AC-28 只选纯功能用例：移动收藏页脚返回与排序保持、封面单击/双击、目录骨架出现与消失，共 5 项。原有骨架高度、收藏布局与播放器拖拽测试保留定义，但不进入该门禁。
- runner 支持多 spec、project 与 grep；`validate.yml` 增加矩阵项。本地 5 passed。

### 01 / 03 真实模型

- 服务器使用 Go 1.26.9 与生产模型端点运行两个 opt-in LIVE。测试新增显式 `KIKOTO_TRANSLATION_LIVE_ALLOW_LOOPBACK=1` 开关，默认仍只接受 HTTPS。
- `TestTranslationLiveConfiguredService`：model `Index-Translate-35B-A3B`，template `index-user-v1`，526ms，输出“今天天气很好。”
- `TestContentTranslationTraditionalLiveConfiguredService`：两个样例均 `translated`；zh-tw 复用 zh-cn 生成，`cacheStatus` 为 stored/hit/hit；示例社团、示例声优保持原文。
- `docs/development/translation.md` 与 01 证据的 `max_tokens` 改为按输入长度 256–4096。

### 复验

| 检查 | 结果 |
| --- | --- |
| `go test ./... -count=1` | 32 个含测试包全部 ok，exit 0 |
| `npm run typecheck:e2e && npm run lint && npm run build && npm run test:unit -- --run` | 通过；lint 0 warnings/0 errors；155 文件、874 测试 |
| content-translation / traditional-translation | 3 + 2 passed；含真实目录展开 |
| translation-settings / personal | 2 + 2 passed |
| source-playback / series / matching | 5 + 3 + 3 passed |
| integrated-acceptance / openlist | 1 + 4 passed |
| mobile-shell | 5 passed |
| LIVE | 两个用例通过，见上 |

## 二次代码审查修复（2026-10-21）

独立审查在 `d7d29bc1` 上复现了系列目录整体改名、根卡目录行、指纹超限、混合 hash、遗留 alias、集号位置、中断代次和翻译资源优先级八项。每项都有对应的行为测试，修复后在同一工作区通过。

### 系列目录整体改名（10/11）

- 系列 key 含父目录路径，改名后生成第二个系列，旧单集全部 missing。
- 现在若新旧系列标题相同，或两侧仍然存在的单集目录名逐一相同，则认定为同一系列：新系列成员并入已建立系列，空系列行删除，系列 id、个人记录与 episode id 保持在旧系列。
- 单文件单集只有在“系列整体改名且集目录名不变”时才接受 manifest 证据；单个同大小文件本身仍不足以认回。
- 测试：`TestCatalogSeriesDirectoryRenameKeepsOneSeries`（真实发布，断言 1 个系列、2 个旧 id、1 个 alias）。

### 根卡与拆分目录（11）

- 拆分卡的 `root_path` 是 `/Creator::key`，真实目录在 `source_root`；根卡清理现在按有效目录比较。
- 测试：`TestRootCardDropsSplitChildFolder`（根直接音频 + Creator 拆分，根卡不再列 Creator 目录）。

### relocation 证据与恢复（10/11）

- 旧卡有 provider hash、新位置没有 hash 时，manifest 仍作为次级证据；候选自身存在不同 hash 时仍判冲突。
- 改名目标由中断的上一代发布时，`discovered >= 旧卡 last_seen` 即可认回，不再要求恰好等于当前代。
- 测试：`TestCatalogRelocationUsesLayoutWhenTheOldCardHasHashes`、`TestCatalogRelocationAcceptsAnInterruptedGeneration`。

### 系列成员与集号（11）

- 兼容性检查与合并读取 alias 闭包；同一卡片被两个系列声明时保留冲突，不静默合并。
- 单集改名后按新目录的显式集号更新 position；槽位已被其他单集占用时保留原顺序。
- 测试：`TestCatalogSeriesMembershipConflictReadsAliases`、`TestCatalogSeriesRenameUpdatesEpisodeNumberPosition`。

### 指纹超限终态（08）

- 10,001 个以上音频的卡片永远不会产生 manifest。reconcile 预扫描只选择可计算范围的卡片，超限卡不再被反复选中。
- 测试：`TestCatalogLifecycleFingerprintLimitSettles`（10,001 行，pre-pass 在有限次调用内结束）。

### 翻译资源优先级（02）

- `sourceId + description` 仍返回 400 `translation_invalid_input`，但只在没有更高优先级资源时；同时给出卡片或作品时由该资源提供字段。远端字段加载器返回类型化的“字段不存在”，不再依赖 handler 的宽泛判断。
- 已登记卡片若文本等于作品本名，复用该作品已有的目标语言标题，不重复调用模型。
- 测试：`TestContentTranslationRejectsRemoteDescription`、`TestContentTranslationRemoteDescriptionUsesAHigherPriorityResource`、`TestContentTranslationReusesAWorkTitleOnACatalogCard`。

### 前端单测稳定性

- `toast.test.ts` 原在用例体内动态导入 i18n 与 API 模块；并行全量套件下偶发超过默认 5 秒（单文件稳定通过）。改为静态导入同一模块，行为与断言不变。
- `npm run test:unit -- --run` 874/874 通过，默认并发。

## 第三轮审查修复（2026-10-21）

### 未登记卡片的侧车名称（02/03）

- 未登记卡片没有 work 行，但 sidecar 已保存社团与声优。翻译标题时现在从 sidecar 读取这些名称并加入保护名单，不再把已发布身份送进模型。
- 系列标题对未登记成员做同样处理。
- 测试：`TestContentTranslationProtectsSidecarNamesOfAnUnregisteredCard`。

### 未知与过期 catalog 标签（05/AC-10）

- 作品详情返回 catalog 目录后，服务端把从未检查（`status_checked_at` 为空）或超过 24 小时未检查的卡片放入有界后台队列；worker 复用单目录刷新逻辑，失败只更新该卡状态，已有目录快照保持可读。
- 目录列表的 `checkedAt` 不再用 `updated_at` 冒充检查时间，从未检查的卡片明确为空。
- 前端在收到待检查目录后做最多两次、间隔 3 秒的刷新，使后台结果可见且不会形成轮询循环。
- 测试：`TestCatalogDirectoryListingChecksUnknownAndStaleTabs`、`TestCatalogDirectoryNeedsCheck`、`sourceContextModel.test.ts` 的 catalog 检查规则。

### AC-08 搜索边界

- 统一搜索入口是库页的搜索框：本地范围搜索已登记作品，来源范围搜索该来源的全部索引卡片。未登记条目通过来源范围可搜索、可打开目录，且不会因浏览而登记 work；保存个人评价时才按需要登记。
- 该边界由 `openlist-catalog` 功能用例和 08 的完整索引证据覆盖。

### AC-28 移动门禁

- `mobile-shell` 只保留文本、操作、请求与持久状态断言；新增 `work detail shows a directory skeleton while media loads`，用可见性与隐藏断言覆盖骨架同步，不测量高度。
- 既有布局测试定义未修改，仍可在本地单独运行，但不作为 AC-28 证据或 Full CI 门禁。
## 第四轮审查修复（2026-10-21）

### 只读 GET 不再占用写锁（13/15）

- `GET /api/shared-splits/{id}` 与 `GET /api/catalog-series/previews/{id}` 只读取快照，改用 `ReadOnly` 事务。生产 DSN 的 `_txlock=immediate` 会让未声明只读的事务执行 `BEGIN IMMEDIATE`，一个预览读取会阻塞目录发布等全部写入，直到 busy timeout。
- 新增 `TestSharedSplitReadDoesNotWaitForTheWriteLock`：另一个连接持有写锁时，GET 在 2 秒内返回。用旧实现注入（`-overlay`，不写入仓库）运行，同一测试等待 30.1 秒后失败，证明测试能在缺陷存在时失败。
- 预览确认仍是写事务，只在 `POST` 时使用。其余 `BeginTx(ctx, nil)` 的 handler 均为写操作。

### catalog 标签检查只由可跟踪来源的查看者启动（05/AC-10）

- 后台检查会抓取来源目录树（单卡最多 5 分钟），因此只在请求者具有 `remote:track` 时入队。匿名或只读查看者仍看到已记录状态；手动刷新入口原本就要求该权限。
- `TestCatalogDirectoryListingChecksUnknownAndStaleTabs` 先以普通用户请求，断言队列与上游调用均为 0；再以 contributor 请求并等待两张卡被实际检查。
- 该行为与 `docs/architecture/workflows.md` 的 "viewers with `remote:track`" 一致。

### 竞争检测下的证据上限测试（14）

- `TestWorkMatchingPausesAtEvidenceByteLimit` 原用 300 个 8 KiB 候选填充 2 MiB。race 检测下逐候选哈希超过 5 秒评估预算，`POST /api/work-matching` 返回 503；该测试是 Full CI `39472781603` 的唯一失败项。
- 改用 60 个 40 KiB 候选，仍越过 2 MiB 上限，但把哈希工作量降低一个数量级。本地 `go test -race` 连续 8 次通过。

### 共享 pinned 判定（08）

- 三处相同的“目录已属于作品、系列或迁移目标”查询合并为 `catalogSplitPinnedSQL`，有限扫描与索引发布使用同一份谓词。
- `TestRootCardDropsSplitChildFolder`、`TestCatalogSeriesFiniteRescanSplitsPublishedRoot` 通过。

### 回滚文档

- 删除 `release-rollback.md` 中引用具体旧镜像与提交的段落；兼容矩阵仍按当前支持关系陈述。
### 发布批次按证据清理侧车（08）

- `markCatalogSidecarCandidatesTx` 原先对批次里的每个已发布 id 都调用 `catalogRootIncomplete`，包括从未有过 readme 证据的新卡片。一次 50 行发布因此在生产上对 14k 目录做重复范围扫描，单批稳定耗时约 15 秒，写锁几乎持续占用，其他写入持续 `SQLITE_BUSY`。
- 现在先用主键查询筛出仍带 `sidecar_json` 的卡片，只对它们检查范围是否完整；没有证据的卡片没有可清除的内容。
- 部署后实测：`publish_pending` 从 222,314 降到 208,464 用时 60 秒（约 230 文件/秒），3 分钟内 0 条 `SQLITE_BUSY`，慢批次告警不再出现。此前的 15 秒/50 行对应约 3.3 文件/秒。
- 侧车套件 `TestCatalogSidecar*` 通过。