# 25：有限扫描语句准备成本

本批基于 `54c4e6a6fa373809c2df65b18e56638f47cec59c`。范围仅为有限扫描 target 发布阶段的 SQLite 重复语句编译；不修改请求预算、重试、合法集合、触发器、外键、迁移或 CI。沿用 implement-spec/TDD 与 H/R 验收边界，无 UI 变更和视觉验收。

## 红测与定位

主代理交付的现成红测为 `TestCatalogFiniteScanKeepsEveryWorkAboveLegacyRootLimit`：完整 2,101 个独立音声条目的真实 HTTP 有限扫描在 race 环境超过原有五分钟预算，返回 HTTP 503。CI 实测 300.64 秒，本机独立复现 325.06 秒；日志为 `root-race-hotpaths.log`。不重复已确认的失败负载。

使用该次 CPU profile 的逐行 `go tool pprof -list=applyCatalogFiniteTargets` 核实，entry UPSERT 调用累计 78.98 秒、file UPSERT 82.47 秒。`applyCatalogFiniteTargets` 共 163.57 秒，其中语句准备约 140.54 秒；整个进程 SQLite parser 为 176.98 秒。重复编译 publication/search trigger 程序是直接热点，不是网络等待。

## 实现边界

- `openlist_catalog.go` 提取现有 entry/file UPSERT SQL 和参数构造，保持单条持久入口及语义一致。
- `catalog_finite_targets.go` 每个事务显式准备并复用一个 entry UPSERT、file DELETE 或 file UPSERT。读游标先关闭；语句在正常或失败执行后均显式关闭，随后提交或回滚。语句不跨事务，不创建全局 cache，不长期占用连接。
- readiness 阶段调用现有批量 status/admission 接口，一次最多 50 个 target。entry 创建仍最多 50 行，file 写入/删除仍最多 64 行；检查点与对应变更保持同事务。
- 新增 `TestCatalogFiniteFileBatchFailureReleasesWriterAndResumes`：真实 HTTP 扫描 521 个独立作品，在第二个 file batch 的第二次写入注入 SQL 故障，首批 64 行和 cursor 保留、后续批次整体回滚，公开集合保持 0。单连接数据库仍能执行 HTTP 读取；关闭 Server/DB 并重开后恢复至 521 张卡和 521 个音频文件。测得 entry/file 事务峰值为 64 行。

## 当前差异验证

Go 使用 `GOTOOLCHAIN=go1.26.10`。原始日志和 CPU profile 均在仓库外 `reports/20261107-remediation/`，以下仅列相对文件名。

| 项目 | 命令及结果 | 日志 |
| --- | --- | --- |
| 原失败用例 race | `go test -race ./internal/httpapi -run '^TestCatalogFiniteScanKeepsEveryWorkAboveLegacyRootLimit$' -count=1 -v -cpuprofile=...`；退出 0，226.97 秒，完整 2,101 卡/文件断言通过 | `finite-prepare-green.log` |
| 有限扫描 H/R | `go test ./internal/httpapi -run 'TestCatalogFinite(FileBatchFailure\|PartitionInterruption\|LargeEpisodeFormats\|SelectedCard)\|TestCatalogFiniteListingResumesCommittedPage' -count=1 -v`；退出 0，5 个顶层测试，含低空间/取消、分页恢复、大系列与格式副本 | `finite-prepare-related.log` |
| publication、个人评价及播放 | `go test ./internal/httpapi -run 'TestCatalogPublication\|TestOpenList(FiniteCatalog\|PlaybackUses\|ReviewRegisters\|MetadataBoundaries\|CatalogOmits\|CatalogMarksRetained\|CatalogSeriesPlayability)\|TestSourceCatalogFiniteScanBuildsEpisodeSeries\|TestCatalogManualSingleSnapshot' -count=1 -v`；退出 0，29 个顶层测试 | `finite-prepare-publication.log` |
| 有限扫描 race | `go test -race -timeout=20m ./internal/httpapi -run 'TestCatalogFinite(FileBatchFailure\|PartitionInterruption\|LargeEpisodeFormats\|SelectedCard\|InvalidPages\|MultipleRoots)\|TestCatalogFiniteListingResumesCommittedPage' -count=1 -v`；退出 0，7 个顶层测试，456.656 秒 | `finite-prepare-related-race.log` |
| publication/个人记录/系列 race | `go test -race -timeout=20m ./internal/httpapi -run 'TestCatalogPublication(KeepsOldTracks\|HidesNewCards\|KeepsPlaybackAndLibrary)\|TestOpenList(ReviewRegisters\|CatalogSeriesPlayability)\|TestSourceCatalogFiniteScanBuildsEpisodeSeries' -count=1 -v`；退出 0，6 个顶层测试 | `finite-prepare-publication-race.log` |
| 静态检查 | `make backend-format backend-vet backend-build backend-lint`；退出 0，lint 为 0 issues | `finite-prepare-static.log` |
| 提交前检查 | `make sensitive-check tracked-evidence-check`；退出 0；最后的注释编辑另跑 `make backend-format`，退出 0 | `finite-prepare-sensitive.log`、`finite-prepare-format-final.log` |

修复后 CPU profile 总样本为 185.06 秒，`applyCatalogFiniteTargets` 为 30.76 秒，SQLite parser 为 51.11 秒。entry 准备加执行为 1.49+10.16 秒，file 准备加执行为 1.19+6.96 秒。对应文件为 `finite-prepare-green.cpu`、`finite-prepare-green-pprof.txt` 和 `finite-prepare-parser-pprof.txt`。原 profile 对应超时退出，修复后 profile 包含完整成功发布；累计 CPU 用于说明热点变化，不等同于单独锁时间或空闲主机基准。

大系列普通回归保留 601 个单集/1,202 个音频；嵌套格式副本保留 2 个单集/804 个音频。实际 SQLite 总行写峰值分别为 1,067 和 1,003，包含触发器/FTS；file 输入峰值保持 64。

## 范围限制

不重复生产目标机测试，不访问真实来源、模型、凭据或服务器。本批不替代同候选完整 CI 与部署验收。所有列出的定向与 race 均已读取终态，无跳过；本子任务启动的后台 job 均已退出。采样生成的 `httpapi.test.exe` 已从工作树移至仓库外的 reports 目录，命名为 `finite-prepare-httpapi.test.exe`，没有提交二进制或原始日志。
