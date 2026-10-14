# 06 稳定内部引用与编号兼容验收

**Status:** verified
**验收代码:** `2b64e2df031d35006a7e6acb3e5bf229a789da60`
**集成基线:** `15ee7e23023308d2f50ef9a921429a6b1c7ed646`（包含 01、04）
**工具链:** Go 1.26.9、Node 24.19.0、仓库固定 golangci-lint v2.13.1。

## 行为与 AC-09 映射

| 工单验收 | 当前证据 |
| --- | --- |
| 复用稳定标识与解析契约 | 复用 `work.id`，个人状态 PATCH 使用既有 `workReadID`；没有新增身份、表、字段或迁移。069 预留已交还集成协调。 |
| 编号、别名与版本入口保持含义 | H 经真实 HTTP 服务、生产路由和登录 cookie 写入并回读：数字 canonical、大小写编号、alias、edition code 选同一 canonical；数字 edition 仍选精确版本，个人状态独立。非法引用为 400，不存在引用为 404，未登录写入为 401。 |
| 旧库升级、重复启动与重开 | R 分别以 immutable 067 编号链和 068 v0.8.3 基线建立既有库，随后应用当前完整迁移链。每组关闭 HTTP 服务、调用应用 `Shutdown` 并关闭 SQLite，再从同一文件重开三轮；没有替换数据库或仅模拟恢复函数。 |
| 公开读取和个人状态入口 | R 从详情及 PATCH 响应核对 canonical/edition/alias/legacy origin、两用户评分备注、0 分有效、公共评分与遗漏字段保留；Library 公共列表总数保持 3，版本列表保持原有两项。修改 alias 备注后，数字引用与编号引用在重启后回读同一新备注。 |
| 后续无码调用方范围 | [06 工单的 07 接入契约](../issues/06-stable-identity-compatibility.md#07-接入契约)列出剩余 schema 准入、来源定位、导航、远端 API 和个人传输边界。架构及代理约定同步。 |

## 红绿与最终验证

所有命令从仓库根执行，`go test` 命令从 `backend/` 执行；Go 和 Node 已加入当前进程 PATH，`GOTOOLCHAIN=local`。网络下载固定工具时仅给该进程设置批准的代理。

| 检查 | 命令 | 退出与结果 |
| --- | --- | --- |
| 有效 H 红灯 | `go test ./internal/httpapi -run '^TestWorkReferencePersonalStateUsesCodeCompatibility$' -count=1` | 1：数字写入成功后，编号 PATCH 返回 400 `invalid work id`。首次夹具使用错误 listening status 的失败不计为红灯。 |
| 首轮绿灯 | `go test ./internal/httpapi -run '^(TestWorkReferencePersonalStateUsesCodeCompatibility\|TestResolveIsReadOnlyForCanonicalAliasEditionAndLegacySnapshot)$' -count=1` | 0：新增编号写入口与原有只读解析同时通过。 |
| 最终 H/R、04 联测 | `go test ./internal/httpapi -run '^(TestWorkReferencePersonalStateUsesCodeCompatibility\|TestWorkReferencesPreserveReleasedLibraryAcrossRestart\|TestResolveIsReadOnlyForCanonicalAliasEditionAndLegacySnapshot\|TestPersonalReview.*)$' -count=1 -v` | 0：8 个顶层测试通过，包含两组旧库和 04 登记、字段更新、权限、恢复、出站边界。 |
| 相同范围 race | 上述选择加 `-race -timeout=5m`（不需 `-v`） | 0：无 race 报告。 |
| 后端回归及构建 | `make backend-test backend-build` | 0：全包测试通过并构建应用。之后只改测试关闭返回值处理与等价布尔写法，并重跑上述 H/R/race。 |
| 静态检查 | `make backend-format backend-lint backend-vet GOLANGCI_LINT_BASE=15ee7e23023308d2f50ef9a921429a6b1c7ed646` | 0：格式通过、lint 0 issues、vet 通过。 |
| 文档链接 | `node scripts/check-doc-links.mjs` | 0：121 份公开 Markdown 相对链接通过。 |
| 提交门禁 | `make sensitive-check`、`git diff --check` | 0：每批提交前通过，未包含私有端点、真实数据或日志。 |

lint 首次因直连模块代理超时未执行；配置该进程代理后重试，发现两处测试响应关闭返回值及一处布尔表达式问题，修正后静态检查通过。未改全局网络设置或停止其他进程。

## 交付边界

- 06 的五项验收均已覆盖；无本票未验证项。
- 无码准入、来源映射和完整无码用户链路归 07；系列、共享关联、导入导出与部署由相应后续工单验收。
- 没有新增依赖、迁移或前端行为；未执行 F、LIVE、视觉或部署验收，也不以本票证据代表它们通过。
- 自建 HTTP 测试服务、应用资源及 SQLite 已关闭；本次所有检查进程已正常结束。保留专属 worktree 与分支供集成，未清理其他人的文件或工作区。
