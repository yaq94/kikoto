# 15 拆分与本人归属集成复验

**Implementation:** verified-local。日期：2026-10-15。

原实施任务交接 `41a3412dfaacbe76c007360f7280bff790f48eec`；本次只读确认该任务 completed/idle，T15 分支与 origin 一致且工作区干净。主流程随后追加明确合并授权。候选实现归属原任务，本记录仅报告当前集成结果。

集成起点为 `18dbd4e0fca164f136aa5df2a23565bb877f50d5`，固定候选通过独立 no-ff 合并进入 `5341b8d623d6c0363f65765f128c8c14b246b645`，无冲突。候选 17 个文件完整保留，接口、前端入口与五语言文案在当前树检查。该版本继续包含 10/13/18，最高迁移仍为 073，无迁移、依赖或出站实现变更。本次未写入原 T15 工作区。

## 当前产物证据

Go 1.26.9、Node 24.19.0、npm 11.17.0；每进程复用已有工具链与 `GOTOOLCHAIN=local`。以下验证对应合并树，随后仅补充执行记录与本证据。

| 命令与范围 | 实际结果 |
| --- | --- |
| `backend/`: `go test -mod=readonly ./internal/httpapi -run '^Test(Shared(Split\|Association)\|SourceCatalog(Lifecycle\|Relocation\|Cleanup))' -count=1 -v -timeout=3m` | 退出 0；16 个顶层 H/R 通过，13.178 秒。覆盖三个本人选择 left/right/both、未确认保留、真实同库重启、过期决议、提交失败回滚、嵌套逆序恢复、系列成员/独立评价、来源改名/状态及清理保护。 |
| `backend/`: `go test -race -mod=readonly ./internal/httpapi -run '^TestShared(Split\|Association)' -count=1 -v -timeout=3m` | 退出 0；9 个顶层用例通过，27.501 秒，无 race。 |
| `make backend-format backend-vet backend-build`；`make backend-lint GOLANGCI_LINT_BASE=18dbd4e0fca164f136aa5df2a23565bb877f50d5` | 退出 0；格式、vet、构建及增量 lint 通过，0 issues。 |
| `make -o frontend-install frontend-lint frontend-docs frontend-i18n frontend-build`；`make -o frontend-install frontend-format` | 退出 0；前端 lint、E2E 类型、构建类型、文档链接/locale、文案、格式及构建通过。保留既有 Vite 配置和大包提示。 |
| 设置隔离 fixture 的 `PLAYWRIGHT_BASE_URL`、`OPENLIST_API_URL`，执行 `make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='tests/e2e/shared-split.spec.ts --project=desktop-chromium --workers=1'` | 退出 0；1 项真实前后端 F，6.6 秒。本人与他人边界、必选归属、待确认重载、最终各侧评分备注回读通过。 |
| 在独立新 fixture 上，将前述 `E2E_ARGS` 换成 `tests/e2e/shared-association.spec.ts --project=desktop-chromium --workers=1` | 退出 0；1 项真实前后端关联入口回归通过。 |
| 本批 fixture 上执行 `split-lifecycle-check.mjs` | 退出 0；拆分后双方只返回各自目录及精确 `workId`；来源禁用和配置删除后各自状态正确；两个用户的 left/both 归属与私有评价仍保留。脚本随仓库外交接索引保存。 |

F 复用源码 `backend/tests/openlist-fixture` 与临时 Vite 配置，外部服务边界为合成桩，真实登录、应用路由、迁移 SQLite 和用户操作保持。测试显式关闭 screenshot、trace、video。每次 fixture 使用独立临时数据库，完成后经自身 stop 端点关闭并清理；只停止本任务的 Vite。

合并提交前的 `make sensitive-check` 扫描 17 文件通过，`git diff --cached --check` 退出 0。最终文档提交继续执行敏感扫描、链接和差异检查；推送只作用于 origin 的集成分支，最终 SHA 与远端读回记录在交接索引。

## 剩余边界

15 保持 `verified-local`；没有以候选历史日志代替本次测试，也没有运行生产迁移或真实来源访问。14、16、CI19、Full CI 终态、最终双轴审查及 17 的真实来源、资源、备份恢复、部署验收仍由主流程继续完成。固定最终审查基线 `9949175e53979bcd590c75a624bc0efc67684da3` 保持不变。
