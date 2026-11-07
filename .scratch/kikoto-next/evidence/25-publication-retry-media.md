# 25：目录发布重试与媒体投影证据

## 记录

- 基于提交：`e85df48f2bebd71948712a0e4d9370ef56114113`
- 被测代码提交：`f298b7d18c158824975b0eda7af5a4debaa875b3`；后续证据提交只改变本文。
- 工作分支：`codex/kikoto-publication-retry-media`
- 按已确认的 source/HTTP/真实 SQLite 边界执行 `implement-spec` 与 `tdd`，逐项红绿验证；没有新增架构或产品决定。
- 测试数据库：真实迁移 SQLite；所有来源、作品、译文和媒体均为仓库合成夹具。
- 原始日志：`E:/项目/服务器/.kikoto-work/reports/20261107-remediation/`

## PUB1：marker 失败后的迟到译文

基线反例 `TestReview107LateTranslationAfterFailedMarker` 在基点失败：marker 失败后 dirty=0；重试后旧短词仍为 1、新短词和长词仍为 0，持久化 browsable old/new=1/0。

实现保留 publication entity 到提交后的有界 cleanup；`translation_prepared` 记录每个实体的准备状态。普通译文 scope 写入和 source-owned mutation 会把对应实体重新置脏并重置 keyset 游标，marker 在同一写事务中确认没有待处理实体后切换。`compat/107_dev.sql` 已重生。

命令：

```text
GOTOOLCHAIN=go1.26.10 go test ./internal/httpapi -run '^TestCatalogPublicationRetry' -count=1 -v -timeout=90s
```

结果：3 个重试用例通过，覆盖重启、配置切换、迟到 upsert、130 行翻译批次、marker 前到达的译文和并发 admission。最后补充 marker 前的插入边界，单例 race 另行通过（31.673s）；见 `publication-retry-marker-boundary-race.log`。第一个用例通过真实鉴权 HTTP 路由搜索；batch/admission 用例直接调用真实 HTTP handler。

## PUB2：删除单条已登记音轨

基线反例 `TestReview107RemovedTrackAvailabilitySwitches` 在基点失败：删除 `old.mp3` 后 `keep.mp3` 已发布，但 old 的 `catalog_stream` location 仍报告 available。

实现按 publication 的 source-owned file set 精确计算 `catalog_stream` availability。准备阶段保留旧可用性；marker 后删除的精确 fingerprint 变为 unavailable，兄弟音轨继续 available。cache location、另一 source 的 catalog location、media identity、个人评分、备注和 playback cursor 不受影响；取消、失败、重启和显式 source offline 状态保持原语义。

命令：

```text
GOTOOLCHAIN=go1.26.10 go test ./internal/httpapi -run '^TestCatalogPublication(RemovedTrack|MediaPreserves)' -count=1 -v -timeout=90s
```

结果：删除、失败 marker、关库重启、取消、cleanup、恢复原音轨和显式 offline 两个用例通过。

## 回归与迁移

```text
GOTOOLCHAIN=go1.26.10 go test -race ./internal/httpapi -run '^TestCatalogPublication' -count=1 -v -timeout=8m
GOTOOLCHAIN=go1.26.10 go test ./internal/httpapi -run '^(TestCatalogSyncListing|TestCatalogSyncRemovedFolder|TestCatalogSyncLowSpace|TestContentTranslationSearchRemote(Strict|IgnoresHistorical|TraditionalMerges|Origin)|TestContentTranslationStore)' -count=1 -v -timeout=5m
GOTOOLCHAIN=go1.26.10 go test ./internal/storage -run '^(TestCatalogPublicationMigration107|TestMigrateBaselineMatchesCompleteIncrementalChain)' -count=1 -v -timeout=5m
make backend-format backend-vet backend-build backend-lint
```

结果：publication race 19 项通过（130.767s）；同步、翻译搜索及存储边界通过（21.473s）；107 升级/失败回滚及 production/development baseline 一致性通过（9.912s）；format、vet、build、golangci-lint 全部退出 0。

| 日志文件 | 退出码与关键证据 |
| --- | --- |
| `publication-retry-media-red-probe.log` | 1；独立审查的两个原探针在 e85 基点均失败 |
| `publication-retry-red.log` | 1；新增关库恢复用例旧短词 2、新短长词 0 |
| `publication-media-red.log` | 1；新增媒体用例提交后及恢复准备期间 old location 错为 available |
| `publication-retry-media-green-probe.log` | 0；两个原探针、before-image/rollback、restart/snapshot/disabled-source 共 4 项；old/new browsable=0/1 |
| `publication-retry-media-race-final.log` | 0；19 个 publication 用例，无 race 报告 |
| `publication-retry-marker-boundary-race.log` | 0；最终新增的 marker 前到达窗口及每事务 64 行硬预算 |
| `publication-retry-media-regression.log` | 0；分页、取消/低空间、恢复、译文存储与搜索 |
| `publication-retry-media-migration-final.log` | 0；105/106 升级、恢复、失败回滚、baseline/chain 一致性 |
| `publication-retry-media-static-final.log` | 0；format/vet/build/lint，0 issues |
| `publication-retry-media-final-lint.log` | 0；最终测试增补后的 lint，0 issues |
| `publication-retry-media-sensitive.log` | 0；代码提交前 7 个变更文件隐私扫描通过 |

先前 race 编译任务为补充并发边界而终止，之后完整 race 使用最终实现重新执行并通过；一次未附进程代理的 lint 下载失败，补回既定进程代理后通过。所有本任务后台命令均已退出或明确终止。

性能边界：译文和媒体仍按 64 行/位置批次处理；marker 不扫描或复制全语料，cleanup 按 64 个 before/entity 行回收。保留每个受影响实体的 ownership 到提交后会多保存一行轻量元数据；普通译文仅按实体索引与 operation 主键失效对应 owner。持续的并发源写入可继续推迟 marker，取消和重启保留进度；未设虚假的强制发布截止。

未运行全仓库 CI、真实外部来源、模型或媒体网络请求；无前端改动和渲染验证。001–105 与已集成 106 未改动；107 尚未发布，已经应用旧 107 的开发数据库不能绕过 checksum 校验直接升级。最终 compat108 需在集成分支合并其它 108 migration 后由主代理重生。
