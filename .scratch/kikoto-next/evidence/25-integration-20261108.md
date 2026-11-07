# 25：2026-11-08 主集成核验

本轮集成分支为 `codex/kikoto-catalog-remediation`，审查基点 `53d905e1`。
此页记录主代理亲自读取的产物和检查，不能关闭完整工单。生产仍是旧构建与 schema 105。

## 已集成产物

- `2b75153c` 合并 staging、probe keyset、benchmark 错误传播及浏览器生命周期修复。
- `72d52434` 合并 106：单次语料物化、持久 revision 与后台保留维护。
- `57cee5f3` 合并 `d7fc68cf` 空分块取消、`b6f8539c` 配置锁取消和 `db0a49a9` 个人系列导出夹具。
- `30cd671d` 合并 `4b87fa36` 的 107 阶段实现。该阶段仍有分组、finite scan、exclusion、retire 与派生读取缺口，见[107 证据](25-sync107-publication.md)。

主代理核对了取消 dispatch、TryLock、个人导出夹具差异，以及 publication writer、marker、短读事务、媒体准备与清理路径。独立复审的发现和失败证据均保留；局部回归通过不等于完整发布通过。

## 主代理执行结果

原始日志在仓库外工作区 `reports/20261107-remediation/`。Go 定向命令 cwd 为 `backend/`，Make 为仓库根，前端 U 为 `frontend/`。

| 产物及命令范围 | 结果 | 日志 |
| --- | --- | --- |
| 第三批：`go test ./internal/httpapi -run '^Test(CatalogSyncProbeCheckpointSurvivesFailureCancellationAndRestart\|SourceCatalogEpisodeDiscoveryPublishes101WithinWriteBound)$' -count=1 -timeout=3m` | exit 0，19.571 s | root-batch3-integration.log |
| 106 与 staging/probe/complete/写后维护交叉回归 | exit 0，27.184 s | root-106-integration.log |
| 57cee5f3：个人系列导出、空库重启和 maintenance runner | exit 0，30.766 s | root-lifecycle-personal-integration.log |
| 57cee5f3：真实 API 调度 U，API/order/prefetch 三文件 | exit 0，30 tests | root-cancel-integration.log |
| 30cd671d：`GOTOOLCHAIN=go1.26.10 go test ./internal/httpapi -run '^Test(CatalogPublication\|CatalogSyncListing\|CatalogSyncProbeExhaustion\|CatalogSyncLowSpace\|CatalogSyncRemovedFolder\|PersonalTransferExportsEveryEvaluationLevel\|PersonalTransferEmptyLibraryRoundTripAndRestart\|TranslationMaintenanceRunner)' -count=1 -timeout=5m` | exit 0，56.394 s | root-107-integration.log |
| 57cee5f3 及四个实现分支推送 origin | exit 0，已核对远程回执 | root-batch4-push.log |
| Node 24.19.0 下重复核对取消、排序及预取 U | exit 0，30 tests | root-cancel-node24.log |
| `GOTOOLCHAIN=go1.26.10 make backend-format backend-lint GOLANGCI_LINT_BASE=53d905e1`，显式资源返回值修正后 | exit 0，0 issues | root-107-lint-final.log |
| Node 24.19.0 / npm 11.17.0，`make -o frontend-install frontend-docs` | exit 0，135 文档、5 locale/11 用户页 | root-docs-final.log |

代码核验使用真实 SQLite、HTTP 路由和合成 fixture；没有以旧测试报告替代当前执行，没有改原行为断言。个人导出修正只补齐导出端已发布音频来源，空库恢复不构建来源或媒体。

## 工具链与未完成检查

系统 Go 1.27.3 导致固定 golangci-lint 无法读取 export data v5；该错误不是 lint 通过。项目声明 Go 1.26.10，主代理通过 `GOTOOLCHAIN=go1.26.10 go version` 实际确认可用，日志 `root-go126-toolchain.log`。固定工具链后 lint 正常进入检查，分别报告 5/16 个 errcheck，日志 `root-go126-lint.log`、`root-107-lint-proxy.log`；资源关闭和合成输出按仓库现有方式显式处理返回值，随后重新运行。网络命令仅使用进程级已有代理，不改全局配置。

第一次文档命令误用了系统 npm 包装器附带的 Node 22，engine 检查拒绝，日志 `root-integration-docs.log`。将进程 PATH 的首项设置为 Git Bash 的 `/e/.../node-v24.19.0-win-x64` 后，实际输出 Node 24.19.0 与 npm 11.17.0，再取得表内文档和 U 结果。没有修改项目 engine 约束或依赖锁。

107 全范围实现、同候选完整 CI/Gate、最终 race、目标机组合负载、真实 Index 和媒体验收、恢复副本启动及生产部署尚未完成。历史 Full run 的失败或 skipped 均不计为通过。签名配置没有变动；正常提交签名状态 N 与现有历史一致，没有读取或更换密钥，没有使用禁签参数。
