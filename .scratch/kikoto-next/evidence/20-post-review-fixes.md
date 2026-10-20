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
8. sidecar 证据只在该卡范围完整观察后清除；partial 扫描保留证据并清空失败/重试字段。队列查询使用 partial index，写入失败不再无 tick 空转；cleanup 游标在末尾回卷以重试 uncertain 卡；旧代 `source_catalog_audio_root` 回收。
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
3. 扫描默认并发 2、间隔 500ms；UI 1–4 上限不变。
4. 扫描开始前清除旧 generation 的 `publish_pending` 标志（只改标志，不动行、entry、file、个人记录），每次最多 16,000 行。
5. `work_matching_worker` 三处状态写入改用 `sqlutil.ExecBounded`，锁冲突时归还连接。
6. 清理游标在末页遇到不确定子树时标记一次重试（负值哨兵，下一次 pass 消费，新扫描归零）。
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
