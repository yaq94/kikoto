# 10、13、18 本地集成验收

**Implementation:** verified-local。日期：2026-10-15。此记录只覆盖本批集成；真实来源、完整 CI、最终双轴审查和生产部署由主流程继续验收。

## 产物与合并

集成分支为 `codex/kikoto-next-integration`，起点 `f0fd5d77be1072f260fc817245c01d1b6334ea0f`，开始时工作区干净。固定最终审查基线仍是 `9949175e53979bcd590c75a624bc0efc67684da3`；`main` 解析为该 SHA，三点差异非空。以下候选在合并前确认包含起点，且 origin 分支与固定 SHA 一致。

| 票 | 固定候选 | 独立 no-ff 合并提交 |
| --- | --- | --- |
| 10 来源生命周期 | `d0a26de5b15edaa965b61c1ad28a23555b3008f3` | `17a2275e795cc391d3aabde91f87042bb1e240e3` |
| 13 共享关联 | `9844975886de745dae660866bcb3ff0eb04198ee` | `1cee5e3e03e5b258386f90a65ca07d0a5d9b0a5d` |
| 18 迁移完成状态 | `924f8079a60d7583058f79409cd1afd1826c6474` | `2c2e87c4f454aa3a45e6672980972a2a25d09336` |

功能验证对应 `2c2e87c4f454aa3a45e6672980972a2a25d09336`。随后提交仅更新规格的进度摘要、执行记录、17 发布授权与依赖及本证据，未改变功能代码。13 候选来自原实施任务，本次只负责合并和独立组合验收，不将原实现归为本次重新完成。

13 的三个冲突逐项合并：`catalog_directory.go` 同时保留来源生命周期、缺源 LEFT JOIN、已迁移条目过滤、共享身份查询与目录精确 `workId`；`CatalogWorkDetail.tsx` 保留状态提示和关联入口；`sourceCatalogApi.ts` 保留来源状态及共享关联类型。未用整文件覆盖解决冲突。

072 来源生命周期和 073 共享关联均保留；开发模式最高基线为 `compat/073_dev.sql`。相对起点，迁移目录只增加 072、073 及两份相应 compat，既有编号迁移及发布 baseline 未改。CI18 不新增 schema。10 的个人引用保护与主动媒体清理 partial 语义保留。

## 当前运行证据

使用 Go 1.26.9、Node 24.19.0、npm 11.17.0，`GOTOOLCHAIN=local`；命令位于仓库根，标有 `backend/` 或 `frontend/` 的命令在对应子目录。主工作树依赖目录不是链接，也没有其他现有 worktree 指向它；复用已安装依赖，未重装或修改共享依赖。

| 检查及可复验命令 | 当前结果 |
| --- | --- |
| `backend/`: `go test -mod=readonly ./internal/httpapi ./internal/storage ./internal/library ./internal/openlist -run '^Test(SourceCatalog\|MediaCleanupForgetWork\|DeleteUnlinked\|OpenList\|Catalog\|SharedAssociation\|LibraryMigration\|V061DatabaseBoots)' -count=1 -v -timeout=5m` | 退出 0；73 个顶层用例通过。HTTP 85.462 秒、storage 0.686 秒、library 1.015 秒；openlist 包无独立测试。一个显式 opt-in LIVE 用例未配置真实样例而跳过，不计真实服务证据。 |
| `backend/`: `go test -mod=readonly ./internal/storage -count=1 -v -timeout=3m` | 退出 0；66 个顶层用例通过，15.957 秒。覆盖完整链、新装、旧库升级、重复/并发启动、开发基线等价与备份失败阻断迁移。 |
| `backend/`: `go test -race -mod=readonly ./internal/httpapi -run '^Test(SharedAssociation\|SourceCatalog(Lifecycle\|Relocation\|Cleanup)\|LibraryMigration\|V061DatabaseBoots)' -count=1 -v -timeout=4m` | 退出 0；22 个顶层用例通过，65.619 秒，无 race。包括状态原子发布、写失败/取消后维护与重启恢复、关联/生命周期恢复和个人引用清理。 |
| `make backend-format backend-verify backend-vet backend-build`；`make backend-lint GOLANGCI_LINT_BASE=f0fd5d77be1072f260fc817245c01d1b6334ea0f` | 退出 0；格式、依赖完整性、vet、构建通过，lint 0 issues。 |
| `make -o frontend-install frontend-format frontend-lint frontend-docs frontend-i18n frontend-build` | 退出 0；格式、lint、E2E 类型、构建类型、文案和构建通过；126 个公共 Markdown、5 个 locale / 11 个用户页面通过。 |
| `frontend/`: `npm run test:unit -- src/features/work-detail/source/sourceContextModel.test.ts src/features/work-detail/source/sourceTabVisibility.test.ts src/components/work-card/sourceBadges.test.ts src/lib/catalogSyncState.test.ts --maxWorkers=4` | 退出 0；4 文件、24 用例通过。 |
| 设置 `PLAYWRIGHT_BASE_URL` 和 `OPENLIST_API_URL` 后：`make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='tests/e2e/shared-association.spec.ts --project=desktop-chromium --workers=1'` | 退出 0；1 项真实前后端 F 通过，8.7 秒。包含预览过期、权限、两名本人确认、重载、关联来源导航与编号引用。 |
| 设置 `PLAYWRIGHT_BASE_URL` 和 `SOURCE_PLAYBACK_API_URL` 后：`make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='tests/e2e/source-lifecycle.spec.ts tests/e2e/source-playback-live-api.spec.ts --project=desktop-chromium --workers=1'` | 退出 0；5 项真实前后端 F 通过，18.2 秒。状态/候选提示、禁用来源评价、可用目录默认选择、浏览切源保持精确媒体/队列及进度、独立刷新恢复。 |
| 本批隔离 OpenList fixture 上运行 `shared-lifecycle-check.mjs` | 退出 0；关联后双方目录在 available、disabled、删除配置后的 missing 三种状态均能回读，目录各自精确作品引用不变，两个用户的共享评分备注仍私有且保留。脚本随仓库外交接证据保存。 |

F 使用当前源码的 `go run ./tests/openlist-fixture`、`go run ./tests/source-playback-fixture` 与 Vite；临时 Vite 配置只覆盖端口和 `/api`、`/health` 代理。前者通过 `OPENLIST_HTTP_ADDR`、`OPENLIST_FRONTEND_ORIGIN` 设置地址，后者监听自身固定测试端口并设置 `PLAYWRIGHT_BASE_URL`。启动前确认端口空闲。测试全部显式关闭 screenshot、trace、video；生成 WAV 与上游桩只证明受控功能，不等于真实源媒体。两个 fixture 均通过自身 stop 端点返回 204 并退出 0，自行清理临时库；仅停止本任务两个 Vite jobs。

首次后端启动的 PATH 使用了不适用于 Git Bash 的写法，命令未找到 Go、退出 127；修正为既有 POSIX 工具路径后，以上完整命令实际通过。初次 GitHub 直连连接重置，使用既有进程级代理后读回成功。没有禁用签名、修改 Git/网络持久配置或关闭校验。Vite 已有配置兼容和大包提示保留，不影响本次构建退出状态。

## 状态和交接

每次合并提交前分别运行 `make sensitive-check`，43、29、6 个变更文件扫描通过，`git diff --cached --check` 均退出 0。最终记录的 4 文件敏感扫描、差异空白检查及相对链接检查均退出 0；46 个 US 与 28 项 AC 正文未变化，已保留本地验收状态和固定审查基线。集成推送仅使用 `origin/codex/kikoto-next-integration`，远端 SHA 由独立读回记录在仓库外交接索引。

07、08、09、10、11、12、13 保持 `verified-local`；CI18 的完整 CI 终态仍待主流程。14、16、19、完整 AC、真实媒体/索引覆盖、资源校准、最终双轴审查、备份恢复演练、部署与上线验收继续执行，本记录不关闭这些要求。

收尾只读快照确认原 15 子任务已 completed/idle，候选 `41a3412dfaacbe76c007360f7280bff790f48eec` 的分支与 origin 一致且工作区干净，15 复用最高 073、无新迁移。本次没有修改、测试或合入 15；后续主流程须单独验收该候选。
