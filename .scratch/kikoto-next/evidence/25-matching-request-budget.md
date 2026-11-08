# 25：作品匹配请求成本

本批受测差异基于 `3f87efda1cb38abd58d9f128b488dd14531405d2`，实现对应本文件所属提交中的 `work_matching.go`、`work_matching_queries.go` 和 `shared_association_snapshot.go`。`work_matching_limits_test.go` 仅增加请求耗时记录，保留原 102 个候选、`candidateLimit=200`、HTTP 200、`review`、`ambiguous_evidence`、完整候选数和无部分关联断言。匹配请求的 5 秒期限、候选与证据上限、重试规则及迁移 001–111 均未修改。

## 原因与修复

Full CI 在上述基点的原回归第二次 POST 返回 503。本地使用原合成数据、真实 HTTP 鉴权和迁移 SQLite 复现同一失败；加入计时后的红测记录请求耗时 5.001s。

CPU profile 将匹配评估中的 3.71 秒 CPU 样本定位到重复查询，其中 SQLite prepare 占 3.35 秒。`loadAssociationObject` 每个候选都重新编译评价层级、目录定位及来源定位的发布投影查询，这三处约占 2.84 秒。上述数字只统计匹配调用栈；测试首次建库的迁移成本不计入请求成本。

匹配评估在已有事务内为这三条原查询各准备一次语句，并在候选和身份成员之间复用。每次执行仍读取真实事务数据，保留全部字段、发布投影、排序及行数限制。准备失败关闭已创建的语句；评估所有返回路径关闭语句；没有跨事务语句、结果或连接缓存。普通关联与拆分调用继续接受原事务并执行同一查询。

## 验证

全部 Go 命令使用 `GOTOOLCHAIN=go1.26.10`。日志和 profile 均保存在仓库外 `reports/20261107-remediation/`，不包含真实作品或真实来源样本。

| 检查 | 命令与结果 | 日志 |
| --- | --- | --- |
| 原回归红测 | `go test -race -count=1 -run '^TestWorkMatchingBudgetDoesNotHideLaterCandidates$' -v ./internal/httpapi`，退出 1；102 候选请求 5.001s，HTTP 503 | `matching-budget-red.log` |
| 修复后原回归及 profile | 同一测试加 `-cpuprofile` 与仓库外 `-o`，退出 0；原结果断言全部通过，1.901s 包含末尾关联读回 | `matching-budget-green.log`、`matching-budget-after.cpu` |
| 匹配、关联及恢复 race | `go test -race -count=1 -run '^(TestWorkMatching\|TestMatchWorkEvidence\|TestSharedAssociation\|TestCatalogAssociationSnapshot)' -v ./internal/httpapi`，40 个顶层测试通过，退出 0，总 317.237s；精确 POST 耗时 1.652s | `matching-budget-related-race.log` |
| 拆分调用方 race | `go test -race -count=1 -run '^TestSharedSplit' -v ./internal/httpapi`，6 个顶层测试通过，退出 0 | `matching-budget-split-race.log` |
| 格式与静态检查 | `make backend-format` 通过；`make backend-lint GOLANGCI_LINT_BASE=3f87efda1cb38abd58d9f128b488dd14531405d2` 退出 0，0 issues | `matching-budget-static.log`、`matching-budget-lint.log` |

红测 profile 为 `matching-budget-before.cpu`，调用位置明细为 `matching-budget-before-profile.log`。相同 `go tool pprof -top -cum -focus=evaluateWorkMatchTx` 输出保存在 `matching-budget-before-evaluation-profile.log` 与 `matching-budget-after-profile.log`：修复后完整评估 CPU 样本为 1.41 秒，prepare 为 0.95 秒；修复前超时中止的评估为 3.71／3.35 秒。该对比是本机 race 采样证据，不外推生产吞吐量。

首次 `make backend-format backend-lint` 在格式检查后因依赖代理连接失败退出 2，没有执行 lint 扫描；随后使用主代理指定的单命令代理环境重跑固定版本 lint 成功。未修改全局代理或依赖版本。

## 交付边界

提交前 `make sensitive-check tracked-evidence-check` 与 `git diff --check` 退出 0，见 `matching-budget-final-checks.log`。只修改上述四个 Go 文件与本证据文件；未清理其他人的文件，未修改翻译代码，未执行推送、部署、凭据操作或视觉验证。本批不关闭 25 号工单；合并后的同候选 Full CI、目标机与部署验收由主代理继续。
