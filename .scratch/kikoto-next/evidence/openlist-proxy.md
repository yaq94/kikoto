# OpenList 单来源代理兼容验收

Implementation: verified-local

本项在 `codex/kikoto-openlist-proxy-compat` 上接续遗留的 8 个未提交文件。
先将这些文件保存为可恢复的 Git stash，再快进合并 integration
`310662b5f4661158d9f721275b8a929fbe46ef6f`，恢复改动无冲突。
以下证据来自合并后的当前产物；07 的真实媒体 LIVE 门槛仍未完成。

## 范围与行为

- Settings 保存来源路由时接受 `openlist`，保留两种 Kikoeru 类型，排除本地和未知来源。
- 管理员可在 Settings → Proxy 为 OpenList 选择代理或直连；无覆盖时继承全局远程路由。
- HTTP 回归使用真实 Routes、管理员/普通用户会话和迁移 SQLite，实际关闭应用与数据库后
  重开同库。验证配置保留、非管理员拒绝、密码脱敏及省略密码后保留凭据；合成 CONNECT
  代理和直连 origin 返回不同的字节，证明选定来源、直连及继承路由实际生效。
- LIVE runner 通过管理员 `PATCH /api/settings` 应用显式的
  `KIKOTO_OPENLIST_LIVE_PROXY_CONFIG`，然后读取元数据并经应用媒体端点读取两次 Range。
  环境 `HTTP_PROXY` / `HTTPS_PROXY` 不会代替应用 Settings 配置。
- 离线 fixture 验证 `bytes=0-31` 与 `bytes=1024-1055` 都返回 206、对应 Content-Range
  和 32 字节；元数据成功但媒体返回完整 200、非法代理、传输失败及挑战响应均不能通过。
  诊断只保留固定类别、状态、路由及摘要，恢复调用方日志配置，不打印敏感应用错误。
- 非视觉 F 使用现有 openlist-fixture 与真实前后端，覆盖选择、保存、API 回读、刷新保留
  和切换直连。测试显式关闭 screenshot、trace、video，没有视觉断言。

本次保留上述遗留实现；新增本记录，并补齐当前产物的红绿及验收证据。
没有新增生产依赖、迁移或生产配置变更。

## 红绿与验证

Go 使用 1.26.9，`GOTOOLCHAIN=local`；Node 使用 24.19.0 和已有 npm/browser 依赖。
下面的 `make -o frontend-install` 与 `-o frontend-playwright-install` 仅跳过依赖安装，
仍执行 Makefile 对应的验证命令。Go 测试在 `backend/`，make 在仓库根运行。

| 命令 / 检查 | 退出码与结果 |
| --- | --- |
| 临时撤下 `remoteSourceIDs` 的 OpenList 一行修复；`go test -mod=readonly ./internal/httpapi -run '^TestOpenListSourceProxySettingsSurviveRestart$' -count=1 -v -timeout=2m` | 1；确定失败于 boot 0 的 Settings 回读缺少 OpenList 覆盖。随后恢复修复。 |
| `go test -mod=readonly ./internal/httpapi -run '^TestOpenList' -count=1 -v -timeout=3m` | 0；15 个本地顶层测试通过，1 个真实 LIVE 因未配置而 SKIP；13.641 秒。包含同库重启和 069 升级回归，数据库使用当前 070 迁移链。 |
| 临时撤下维护页 OpenList 选项；下列 F 命令 | 2；确定失败于目标代理 combobox 缺失。恢复修复时一次宽匹配被保护机制拒绝，未写入；缩小匹配后恢复成功。 |
| `PLAYWRIGHT_BASE_URL=http://127.0.0.1:3127 OPENLIST_API_URL=http://127.0.0.1:17627 make -o frontend-playwright-install frontend-e2e E2E_ARGS='tests/e2e/openlist-proxy.spec.ts --project=desktop-chromium --workers=1'` | 0；当前修复产物 1 个非视觉 F 通过，2.5 秒。 |
| `go test -mod=readonly ./internal/httpapi -run '^Test(UpdateSettingsPersistsProxies\|SettingsReturnEmptyProxy\|LegacyMetadataProxy\|MetadataClient\|RemoteSourceRequestsFollowProxy\|AppUpdateCheckFollowsOtherProxy\|RemoteSourceFallsBackToDirect)' -count=1 -v -timeout=2m` | 0；9 个既有代理契约回归通过，0.363 秒。 |
| `make backend-format backend-vet backend-lint` | 格式与 vet 通过；整体 2，lint 的模块查询直连超时。`GOPROXY=off` 重试仍为 2，模块元信息查询被禁用。 |
| 使用已有单次进程代理运行 `make backend-lint` | 0；0 issues。仅工具依赖查询使用进程代理，不作为应用路由验收。 |
| `make -o frontend-install frontend-format frontend-lint frontend-build` | 0；最终恢复修复后重跑通过，0 lint 警告/错误，e2e typecheck 通过，2469 modules 构建成功。既有 Vite native config 提示及大于 500 kB chunk 提示保留。 |
| `make -o frontend-install frontend-docs`（与前端检查一起运行） | 0；文档检查测试 1 项通过，123 份公开 Markdown 链接、5 个语言版本/11 个用户页面检查通过。 |
| `make sensitive-check`、`git diff --check` | 0；9 个本项变更文件隐私扫描通过，差异无空白错误。 |

本地原始日志由本次 FastCtx jobs 留存在仓库外。关键 job：后端 RED `j-bdnits`，
OpenList GREEN `j-a9hzym`，F RED `j-kph2ic`，F GREEN `j-b9wg2o`，
最终前端检查 `j-76ex4n`，代理回归 `j-mvdz4m`，lint GREEN `j-mc3vms`。
这些日志不随 Git 提交。

## LIVE 缺口

本次没有访问真实站点，也没有重测生产 fs/get。07 保持 `verified-local`，原始
[LIVE 缺口](07-openlist.md#live-缺口与可复验入口)的事实和验收要求保留：

- 早期匿名 fs/list 的程序请求读到 HTTP 403 / Cloudflare 1010；IAB 匿名页面和播放器可用。
- 后续同文件 fs/get 未配置应用代理时 `route_proxies=0`，连接重置 errno 10054，未收到 HTTP。
- 隔离合成库通过正式 Settings 契约配置已有代理后 `route_proxies=1`，返回 HTTP 403 HTML、
  `cf-mitigated: challenge`；这次没有 1010 证据，没有 raw_url 或 Range 字节。
- 后续 fs/get 诊断不能追认为更早单次 LIVE 的确切错误；挑战入口探测已经停止。

本地 fixture 成功不替代真实匿名媒体范围读取、指定偏移或解码/播放验收。
复验入口与显式代理契约见 [single-file verification](../../../docs/development/openlist-live.md)。

## 清理

本次 openlist-fixture 经其 `POST /__fixture/stop` 正常退出 0，应用关闭并清理自建临时库；
Vite 仅终止本次 job `j-9fhlca`，结果为 killed。没有清理其他任务的文件或进程。
合并前的安全 stash 在正常提交验证完成后定向移除。
