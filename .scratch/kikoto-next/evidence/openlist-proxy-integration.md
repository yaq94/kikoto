# OpenList 代理兼容集成复验

Implementation: verified-local

集成起点为 `310662b5f4661158d9f721275b8a929fbe46ef6f`，合入已推送提交
`7d75de59e830312fba90dfad219cf15d751a8ee0`。合并无冲突；本记录随合并提交保存。
08 已包含在起点中，没有重复合入。实际审阅的生产差异只有来源代理白名单增加
OpenList、维护页代理选择器纳入 OpenList；其余为测试、LIVE runner 和开发文档。
无新增依赖、迁移或生产配置变更。

## 当前产物验证

Go 1.26.9、`GOTOOLCHAIN=local`，Node 24.19.0；复用已有依赖。
以下命令在合并后的集成工作区执行，均读取终态输出。

| 命令 | 结果 |
| --- | --- |
| 在 `backend/` 执行 `go test -mod=readonly ./internal/httpapi -run '^Test(OpenList\|SourceCatalog\|UpdateSettingsPersistsProxies\|SettingsReturnEmptyProxy\|LegacyMetadataProxy\|MetadataClient\|RemoteSourceRequestsFollowProxy\|AppUpdateCheckFollowsOtherProxy\|RemoteSourceFallsBackToDirect)' -count=1 -v -timeout=5m` | 退出 0，37 个顶层测试通过，24.598 秒；真实媒体 LIVE 因未配置 SKIP，不能计作通过。 |
| `make -o frontend-install backend-format backend-vet frontend-lint frontend-build frontend-docs` | 退出 0；Go 格式/vet、前端 lint/类型、构建及文档检查通过。2469 modules；123 份公开 Markdown，5 个语言版本、11 个用户页面。 |
| `make sensitive-check` 与 `git diff --check`、`git diff --cached --check` | 退出 0；12 个变更文件隐私扫描通过，无空白错误。 |

后端回归包含 Settings 权限、凭据保留/脱敏、OpenList/两类 Kikoeru 过滤、代理/直连/
继承路由实际字节、关闭并重开应用和 SQLite，以及扫描排他、检查点、暂停/取消/恢复、
完整分页树。非视觉 F 沿用合入提交的已验证 1 项（见[代理证据](openlist-proxy.md)）；
本次集成没有修改页面行为或重跑 F。既有 Vite native config 和大 chunk 提示仍存在。
本次原始日志为仓库外 FastCtx jobs `j-xza3vv` 与 `j-7yt05l`。

## 剩余要求

07/08 保持 `verified-local`。没有请求真实站点，也未取得真实媒体 Range、定位读取
或实际全来源索引覆盖；既有 LIVE 事实见[07 证据](07-openlist.md#live-缺口与可复验入口)。
09/10 可基于当前接口继续本地实施，11 独立分支尚未集成；完整 AC 和发布前置不变。
本次没有自启服务或待清理测试进程，没有删除任何 worktree。
