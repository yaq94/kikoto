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
2. **默认扫描速率与 D-10 不一致。** `defaultCatalogIndexOptions` 恢复为并发 1、间隔 1000 ms，与规格 D-10 的“并发 1、每秒最多 1 次”一致；管理员仍可在 1–4 / 100–60000 ms 内上调。生产强制全量扫描按管理员显式参数运行，不用默认值。

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
