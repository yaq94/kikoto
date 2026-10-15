# 12 系列与单集评价集成复验

Implementation: verified-local

集成起点 `116a512ff37fb8c302b65160b6729bdec6fd17f3`，合入独立提交
`77cd4449a890f044b07232c3445d0a5b282db1aa` 无冲突。本记录随合并提交保存。
实际核对个人评价新路由、当前用户查询、按需登记事务及通用编辑器新增可选标题。
没有新迁移，071 与 09 的每日调度保留。

Go 1.26.9、`GOTOOLCHAIN=local`，Node 24.19.0；合并后当前产物：

| 命令 | 结果 |
| --- | --- |
| `backend/`：`go test -mod=readonly ./internal/httpapi -run '^Test(CatalogSeries\|SourceCatalogDaily)' -count=1 -v -timeout=3m` | 退出 0；9 个顶层 H/R，通过系列、单集、跨源个人评价、两用户隔离、真实关闭重启及每日索引回归，31.602 秒。 |
| `make -o frontend-install backend-format backend-vet frontend-lint frontend-build frontend-docs` | 退出 0；格式/vet、前端 lint/类型/构建、124 份 Markdown、5 语言文档检查通过。 |
| `PLAYWRIGHT_BASE_URL=http://127.0.0.1:3129 OPENLIST_API_URL=http://127.0.0.1:17629 make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='tests/e2e/catalog-series-review.spec.ts --project=desktop-chromium --workers=1'` | 退出 0；1 项真实前后端 F 通过，6.5 秒。系列/两单集保存与独立回读、部分清除、重载、无媒体请求均通过。 |

F 使用当前 OpenList fixture 和 Vite，开启 `OPENLIST_SERIES_FIXTURE=1`，端口启动前核实
空闲。用例显式关闭 screenshot/trace/video，无视觉断言。fixture 通过自身 stop 端点退出，
清理自己的临时库；仅终止本次 Vite job。原始日志为仓库外 jobs `j-mczdqp`、
`j-0yvh9a`、`j-b4k7mo`。既有 Vite 配置加载和大包提示保留。

07/08/09/11/12 仍 verified-local，真实媒体及真实全来源覆盖和完整发布门槛保留。
本次没有生产部署、真实来源请求或额外浏览器驱动。
