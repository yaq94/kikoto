# 25：目录排除异步任务界面证据

## 版本与范围

- 基点：`8c51572eef27f708dbaa9cd1018e08e518d95bbd`（本地 `codex/kikoto-exclusion-bounds`）。
- 被测代码：`29ef9ae326573c07804e98dc8441eadcf185ef0c`。
- 分支：`codex/kikoto-exclusion-task-ui`；独立托管 worktree 名称 `kikoto-exclusion-task-ui`。
- 使用已指定的 `implement-spec`、`tdd`；测试接缝为真实前端公开任务观察 API、fetch 传输边界、浏览器操作与本地真实后端 HTTP/SQLite 夹具。
- 范围为目录排除客户端、界面、父级完成回调、任务读取的可选取消信号、五种界面语言及直接测试。没有修改后端、DDL、中央工单、规格或执行记录。
- 依据：[规格 D-05/D-10/D-11、AC-21/23/25/28](../spec.md)、[工单 25](../issues/25-review-remediation.md)、[ADR-0008](../../../docs/decisions/ADR-0008-catalog-publication-snapshots.md)、[后端合同](25-exclusion-resource.md)。

## 行为

POST/DELETE 的响应保留 `runId`。界面先呈现规则已保存，随后读取公开任务状态；只有 `succeeded` 才调用既有 `OpenListScan.onDone` 刷新来源内容并重读规则。queued、running、failed、cancelled、partial 和未知状态都有独立、固定翻译文案，低空间、来源禁用和维护原因通过白名单映射展示。原始 summary、路径诊断和错误正文不进入任务提示。

重开页面通过既有 Activity 列表按 workflowCode 分页，读取公开 node input 的 sourceId 匹配本来源最新任务；失败的同步请求没有 runId 时也走同一恢复路径。每页最多 25 条，顺序读取详情，跨页等待 2 秒。列表缺少 sourceId 过滤，查找成本随可见排除任务历史增长；发现最新匹配任务后停止，不创建新任务，也不扫描个人数据。

正常轮询间隔 2 秒，隐藏页或已识别延期原因为 15 秒；传输失败等待 4 秒、8 秒，连续三次失败后停下并保留手动刷新。请求串行，没有重叠轮询。取消信号同时绑定组件生命周期和 API session；source、账号、权限或 session 切换重建表单，迟到写响应不能更改新来源。失败读保留当前已知规则。

Retry 复用既有 workflow retry API，要求当前 sources:write、workflows:run 和服务端 canManage，恢复原 run。缺少 workflow 权限时允许原规则写入能力，但明确提示无法确认目录更新完成。界面语言仅使用既有 i18n，内容语言参数与翻译状态没有改动。

共享 `useWorkflowRunWatcher` 的缓存仅以 runId 为键，且同时读取 events/candidates；本实现复用其下层 workflow client 和既有 session/abort 合并机制，使用仅属于本表单的可取消观察流程，避免扩大共享 hook 改动范围。

## 红绿与验证

原始日志位于仓库外 `.kikoto-work/reports/20261107-remediation/`。所有夹具均为合成数据，未使用真实来源、媒体、凭据或外部服务。Node 为 24.19.0，npm 为 11.17.0；Go 命令显式使用 `GOTOOLCHAIN=go1.26.10`。

| 验证 | 命令与结果 | 日志 |
| --- | --- | --- |
| 首条任务状态红测 | `npm run test:unit -- src/features/sources/catalogExclusionTask.test.ts`；exit 1，缺失公开观察入口；随后 queued→running→succeeded 绿测通过 | `exclusion-ui-red.log` |
| 恢复及生命周期红测 | 同一命令；exit 1，分页恢复、失败后重读及 session 停止三项失败；修复后通过 | `exclusion-ui-recovery-red.log` |
| 真实 UI 原缺陷 | `PLAYWRIGHT_BASE_URL=http://127.0.0.1:3117 E2E_NO_ARTIFACTS=1 npm run test:e2e -- tests/e2e/catalog-exclusion-task.spec.ts --project=desktop-chromium --workers=1`；原组件没有任务状态，exit 1 | `exclusion-ui-browser-red.log` |
| 低空间 token 红测 | 单元命令 exit 1：后端 `low_disk_space` 未映射；修复后通过 | `exclusion-ui-pause-red.log` |
| 来源和语言单元回归 | `npm run test:unit -- src/features/sources src/i18n`；9 文件、50 测试通过，其中 10 项覆盖排除任务公共状态与生命周期 | `exclusion-ui-unit-final.log` |
| 非视觉浏览器功能 | 上述 Playwright 命令；7 项通过，22.1 秒。覆盖 pending 重开、成功刷新、写失败恢复、同 run 重试、cancelled 归属权限、失败读保留规则、源切换迟到响应、同步成功删除和权限不足提示 | `exclusion-ui-browser-final.log` |
| 后端实际合同复验 | `go test ./internal/httpapi -run '^TestCatalogExclusion(HTTPBoundsMultiRootWrites\|BatchFailureRestartAndHTTPRetry\|RuleRemovalKeepsIdentityAndRemainingRules)$' -count=1 -timeout=5m -v`；exit 0，3 项通过，4.849 秒；真实 HTTP、迁移 SQLite、失败后数据库重开与 retry | `exclusion-ui-backend-contract.log` |
| 格式、lint、类型、构建与界面文案 | `make -o frontend-install frontend-format frontend-lint frontend-build frontend-i18n`；exit 0，lint 0 warnings/0 errors，Vite 生产构建通过。依赖已通过 lockfile 的 `npm ci --strict-allow-scripts` 安装 | `exclusion-ui-static.log`、`exclusion-ui-deps.log` |
| 新增测试最后类型检查 | `npx tsc -b --pretty false`、`npm run typecheck:e2e`、`npx oxlint src/features/sources/catalogExclusionTask.test.ts --deny-warnings`；全部 exit 0。发生在最终测试扩充后；输出由本次工具记录保留 | 本次终端输出 |
| 提交前检查 | `make sensitive-check tracked-evidence-check`；exit 0，11 changed files 与 77 evidence files 通过；`git diff --check` exit 0 | `exclusion-ui-precommit.log` |

构建仅有已有 Vite 配置加载及大于 500 kB chunk 提示。没有升级依赖或修改全局配置。

## 边界与交接

浏览器测试运行真实 Vite/React 应用但在 HTTP 边界返回合成响应；后端合同由独立真实 HTTP/SQLite 测试复验，没有声称浏览器与真实后端连通验收。全部浏览器测试显式关闭 screenshot、trace、video，没有截图、布局或像素检查。真实生产、目标硬件、全仓 CI 和整合后的公共使用文档由主流程负责。

实现及证据在本地提交，未 push；测试服务在交接前停止，托管 worktree 保留用于集成。工作区级专项参考 `instructions/specialized-workflows.md` 不存在；本任务按已读取的仓库 design/frontend/testing 与指定 implement-spec/TDD 执行，不依赖缺失参考。
