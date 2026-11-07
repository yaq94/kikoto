# 25：个人系列导出夹具回归

## 验证对象与范围

- 基线：`72d52434075c1724c5f3fd97f14b5b098826d546`，原生隔离 worktree，分支 `codex/kikoto-personal-series-regression`。
- 绿色验证对象：上述基线加本证据同提交的 `personal_transfer_test.go`、`personal_transfer_restart_test.go` 夹具差异。产品实现、准入查询及迁移没有修改。
- 环境：Windows amd64，Go `1.27.3`；真实鉴权、迁移 SQLite、HTTP 路由及关闭／重开应用。使用合成数据，无生产服务或外部内容。
- 原始日志目录：工作区仓库外 `.kikoto-work/reports/20261107-remediation/`，下文日志名均相对此目录。
- 依据：正式规格 D-07、D-09、D-11、AC-24；ADR-0004、ADR-0005、ADR-0006；22、25 号工单。按 implement-spec/TDD 的已授权 H/R 边界复现并修正。

## 原因与最小修正

两个失败均发生在导出之前：测试手工建立 `catalog_series_member`，但成员没有 `file_source_id`、没有已发布音频，且没有通过个人恢复建立 `work.parent_work_id`。它既不是通过准入的来源成员，也不是具有显式父级关系的个人恢复单集。

基线的 `catalogSeriesMembersSQL` 要求来源成员拥有来源引用，并通过已发布文件准入或 retained missing 分支；`requireCatalogSeriesReadable` 另允许已有 `parent_work_id` 的恢复单集。原夹具两条路径都不满足。该基线的 `TestCatalogSeriesAdmissionMatchesReviewAndTranslation/source_less_member` 明确要求系列详情、评价和翻译拒绝这种成员；`missing` 与 `restored` 分别保护仍有来源引用的缺失成员及个人恢复例外。

修正只为两个测试的导出端夹具补齐合成 OpenList 来源、保留域名 endpoint、两个 entry 的来源引用，以及每个 entry 一条当前 generation 的音频元数据。系列归属仍来自原有显式 `manual` 成员关系，未按标题推断，也未提前写 `parent_work_id` 绕过来源准入。系列和单集仍由原来的公开评价路由各自登记。

两处原始 HTTP 状态、对象数量、稳定引用及评价层级断言完全保留。空库往返用例中的权限、重复导入、个人字段、不可用进度留存、用户隔离、重启、无下载任务、无媒体文件及来源路径不进入备份等断言也完全保留。恢复目标仍是独立空库，新增来源和音频元数据仅存在于导出端。

这两项失败是旧夹具与现行准入合同不一致；本次没有发现需要修改个人传输产品实现的缺陷，也无需修改另一工作项拥有的系列准入文件。

## 红色证据

在干净原始基线上运行，未使用 overlay：

```sh
go test ./internal/httpapi -run '^(TestPersonalTransferEmptyLibraryRoundTripAndRestart|TestPersonalTransferExportsEveryEvaluationLevel)$' -count=1 -v
```

工作目录为仓库 `backend`；退出码 `1`，日志 `personal-series-baseline-72d52434.log`。

- `TestPersonalTransferEmptyLibraryRoundTripAndRestart`：原第 68 行，`PATCH /api/catalog-series/1/user-state` 返回 `404`，期望 `200`。
- `TestPersonalTransferExportsEveryEvaluationLevel`：原第 26 行，同一路由返回 `404`，期望 `200`。
- 错误响应：`code=not_found`、`error=series or preview not found`；两者均未到达导出入口。

## 绿色证据

从同一 `backend` 目录逐项运行，均使用 `-count=1`：

| 命令 | 退出码 | 日志与结果 |
| --- | --- | --- |
| `go test ./internal/httpapi -run '^TestPersonalTransferExportsEveryEvaluationLevel$' -count=1 -v` | 0 | `personal-series-export-fixture-green.log`；1 项通过，包耗时 4.718 s |
| `go test ./internal/httpapi -run '^TestPersonalTransferEmptyLibraryRoundTripAndRestart$' -count=1 -v` | 0 | `personal-series-restart-fixture-green.log`；1 项通过，包耗时 2.224 s |
| 下方相关回归命令 | 0 | `personal-series-related-green.log`；66 个顶层测试通过，无失败或跳过；personal 26.495 s，httpapi 54.441 s |
| 仓库根 `make backend-format` | 0 | `personal-series-format.log` |
| 仓库根 `make sensitive-check`（使用已有 Node 24） | 0 | `personal-series-sensitive-check.log` |
| 仓库根 `make tracked-evidence-check`（使用已有 Node 24） | 0 | `personal-series-tracked-evidence-check.log` |
| `git diff --check`、`git diff --cached --check` | 0 | 无输出 |

相关回归命令：

```sh
go test ./internal/personal ./internal/httpapi -run 'Personal|Transfer|^TestCatalogSeriesAdmissionMatchesReviewAndTranslation$|^TestCatalogSeriesKeepsMissingEpisodesAccessible$' -count=1 -v -timeout=10m
```

相关回归包括：

- 系列准入：`source_less_member`、`old_audio` 保持拒绝；`admitted`、`missing`、`relocated`、`disabled_admitted`、`restored` 保持原行为。
- 显式父级：`TestTransferV2RestoresPersonalOnlyParentAndRoundTrips`、`TestTransferExportWritesSingleMembershipParent`、`TestAuditPersonalTransferExportsParentWithoutRegisteredEntry` 通过。
- 冲突与兼容：多父级、父引用冲突、循环、无父字段旧 v2、稳定引用及已退役来源引用恢复通过。
- 独立私有评价：`TestTransferRestoredSeriesAndEpisodeHaveIndependentReviews`、`TestPersonalTransferRestoredSeriesAndEpisodeReviewIndependently` 通过；后者保留无媒体、无 workflow、无伪造成员的断言。
- 缺失状态：`TestCatalogSeriesKeepsMissingEpisodesAccessible`、`TestPersonalTransferReportsContradictoryAndRetainedSourceReferences`、退役卡片个人评价与进度保留通过。

## 限制与交接

只修直接测试夹具并保留既有行为断言，没有修改 UI，因此没有浏览器或渲染验收。没有运行完整 CI、race、生产或真实外部来源验收；这些结果不替代集成分支合入其他工作项后的验证。工作树与本地提交保留供主代理集成；未推送，也未修改中央工单、规格或执行记录。
