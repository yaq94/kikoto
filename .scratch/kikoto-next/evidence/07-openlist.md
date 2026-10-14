# 07：有限 OpenList 本地验收

Implementation: verified-local

本记录对应 `codex/kikoto-next-t07` 的本票实现，基于 integration
`e5dc48e692b956a5abfe4d681ebf6629ddc93b3a`。真实媒体 LIVE 和 02
集成后的 catalog 翻译联测未完成，不能据此关闭整票。

## 实现与范围

- 未登记来源索引条目用持久 `catalogEntryId`，必要登记后返回 `workId`。
  显示名、真实路径、临时媒体 URL 分开；无码保留空编号。
- 069 扩展非空编号唯一约束，保留既有 work/logical_work 主键、关系和个人状态。
  新来源表保存有限目录观察，不持久音频或签名链接。
- 最多 8 个显式作品根，每根 32 目录、8 层、2,000 条目和 100 页；一次扫描
  最长 5 分钟。纯压缩包、纯字幕根排除；不推断系列或执行全站索引。
- 匿名 OpenList 元数据 POST 使用串行 lane，默认每秒一次，30 秒超时和
  2 MiB 上限。媒体复用既有安全 transport、Range streaming 和字节上限。
- 来源卡片、目录和播放器贯通；评价沿用现有私有评分/备注语义。
  接口、配置和存储契约见 [OpenList 边界](../../../docs/development/openlist.md)。

## AC 映射

| 条款 | 本票证据 | 限制 |
| --- | --- | --- |
| AC-08 有限集合 | H 有编号、无码、zip/srt 排除、未登记搜索；F 卡片与目录 | 全站覆盖由 08 验收 |
| AC-09 | R 从真实 068 库迁移，编号/alias/canonical/精确 edition 及无码引用重开一致 | 仅本票有限准入 |
| AC-12 | H/R/F 必要登记、评分 0/null/遗漏、备注更新和清分、重载保留 | 无媒体落盘及工作流 |
| AC-13 | H 两用户不同记录、未授权拒绝、并发登记幂等 | 共享身份合并由后续票负责 |
| AC-25 | H 元数据大小、目录数、分页完整性、取消、串行及间隔边界 | 组合负载和低空间由 17 验收 |
| AC-27 | H 非法协议、凭据 URL、私网、越界重定向、超大响应、取消 | LIVE 尚未验证站点协议可用性 |
| AC-28 | F 仅文本、点击、API 持久状态、媒体进度 | screenshot/trace/video 全关闭 |

## 红绿记录

1. H 目录发现：先失败于不支持 `sourceType=openlist`（400），加入有限扫描和
   catalog 持久化后通过；目录搜索不会创建 work。
2. H 评价：先失败于缺少 catalog user-state 路由；加入事务必要登记后通过。
   两个无码作品独立，0 分与 null 可区分，遗漏字段保留原值，两用户互不覆盖。
3. H 播放：先失败于缺 media 路由；加入真实 path 选曲、get URL 与单次过期
   重取后通过。`bytes=10-15` 为 `abcdef`、`bytes=2-4` 为 `234`，显示名不接受为路径。
4. H Demo：先观察到 catalog 详情可读；统一 loader 增加 Demo 读取边界后通过。
5. F：先缺扫描按钮；加入扫描入口后继续贯通。测试中的可访问名、URL 查询编码
   断言按现有 UI/API 修正后，真实前后端用户链路通过。合成 WAV 只证明本地播放器。

## 实际验证命令与结果

命令在仓库根执行，Go 子命令在 `backend/`，npm 子命令在 `frontend/`；
环境使用 Go 1.26.9（`GOTOOLCHAIN=local`）、Node 24.19.0。无新增生产依赖。

| 命令 | 退出码 / 结果 |
| --- | --- |
| `go test -mod=readonly ./internal/httpapi -run '^TestOpenList' -count=1 -v -timeout=2m` | 0；8 个本地 H/R 顶层测试通过，LIVE 未配置时跳过 |
| `go test -race -mod=readonly ./internal/httpapi -run '^(TestOpenList\|TestWorkReference\|TestPersonalReview\|TestResolveIsReadOnly)' -count=1 -timeout=5m` | 0；51.585 秒 |
| `make backend-test` | 首次 2（storage 仍预期最高版本 68）；更新测试预期后完整重跑 0 |
| `go test ./internal/storage -count=1` | 0；完整迁移链与 baseline 等价 |
| `make backend-format backend-vet backend-build` | 0 |
| `make backend-lint` | 首次 2（3 个 style/errcheck 项）；修复后 0，0 issues |
| `make frontend-build` | 0 |
| `make frontend-coverage frontend-docs frontend-i18n` | coverage 153 文件/867 测试通过，文档通过；整体 2，两个新 UI 文案硬编码 |
| `make frontend-i18n` | 修复来源类型词典后 0 |
| `make frontend-docs ci-style` | 文档、Prettier、lint、e2e typecheck 和脚本测试通过；整体 2，最后 actionlint 进程 PATH 缺 Go，见补验记录 |
| `make actionlint` | 修复 PATH 后首次 2（直连依赖仓库超时）；使用已授权进程代理重跑 0 |
| `make sensitive-check backend-format backend-vet` | 首次 2（测试中的 URL/IP 字面量）；使用合成凭据构造及文档保留 IP 后重跑 0，55 个改动文件通过 |
| `git diff --check` | 0 |
| `PLAYWRIGHT_BASE_URL=http://127.0.0.1:3117 OPENLIST_API_URL=http://127.0.0.1:17617 npm run test:e2e -- tests/e2e/openlist-catalog.spec.ts --project=desktop-chromium --workers=1` | 0；1 个 F 用例，5.8 秒 |

H/R 测试实际使用 production Routes、登录会话及迁移 SQLite。R 先在 068 库
保存 canonical、edition、alias、收藏、播放游标和两用户评价，关闭 HTTP、Server
与 SQLite 后，同库应用 069 并重开，继续登记无码及保存评价，再关闭重开核对。
不是换空数据库测试。早期 R 断言曾误读响应 envelope，修正后重跑通过。

F 验证扫描→无码卡片→格式目录→音频进度超过 0.5 秒→0 分/备注保存→数字 work
引用重开→清分→重载与 API 回读。没有截图、trace、video 或视觉布局断言。
fixture 使用独立临时库和服务，不读取部署库。

最终修改仅涉及文档、i18n 来源类型标签与测试安全边界。修改后本票 H/R 再跑
退出 0（6.699 秒）；LIVE opt-in 入口编译并按未配置状态 SKIP，未增加 LIVE 证据。
backend-format、backend-vet 和隐私扫描也重新通过。

## LIVE 缺口与可复验入口

指定真实站点的匿名诊断使用 OpenList 公开协议：POST `/api/fs/list`，
JSON `path="/"`、`password=""`、`page=1`、`per_page=6`、`refresh=false`，
Content-Type 与 Accept 为 application/json。

本机代理请求 HTTP 403（curl 退出 22）。获准远程只读诊断也读到 HTTP 层 403：
Server `cloudflare`、Content-Type `application/json; charset=utf-8`，
`error_name=browser_signature_banned`、Cloudflare 1010、`retryable=false`、
`owner_action_required=true`。这不是 OpenList JSON 业务码。诊断命令退出 0 仅证明
读到拒绝原因，不代表媒体成功。主流程决定停止重试；未登录、未绕密码或验证码。

尚无真实可播放文件定位、指定偏移内容或真实媒体解码证据。只有入口得到站方允许
或用户提供可用公开接口后，在隔离环境设置下面两个变量再执行：

```sh
KIKOTO_OPENLIST_LIVE_URL=https://audio.example.invalid \
KIKOTO_OPENLIST_LIVE_FILE='/Example Work/audio.mp3' \
go test ./internal/httpapi -run '^TestOpenListLiveRangeSample$' -count=1 -v
```

示例域名不可用，必须明确替换。工具仅针对一个已知匿名文件，先读 get，再经过
真实应用媒体路由请求 `0-31` 与 `1024-1055`；仅接受 206，每次最多消费 33 字节，
随后关闭响应。拒绝完整 200 响应，不扫描目录、不下载完整媒体。日志只记录定位
摘要、Range、协议头和字节摘要。未配置时退出 0 并 SKIP，不能算 LIVE 通过。

## 清理与交接

本票两个 fixture 服务均经自己的 stop 接口退出 0；Vite 仅终止本票 job 并确认
killed。未修改生产系统。构建产物、node_modules 和临时测试日志按既有忽略规则
留存，不加入提交。没有数据库、真实音频、私有 endpoint 或密钥进入交付。

一次早期失败的 R 测试可能留下一个系统临时目录。仅清理该目录的原生命令被
自动审批策略拒绝（blocked by policy），命令没有执行，也未换工具绕过；该项
清理保留为交接事项。后续 R 测试关闭与清理均成功。

下游可复用 `loadSourceCatalogEntry`（先检查 source 可读性）、
`loadSourceCatalogFiles`（原文 Title 与真实 Path 分开）。08 扩展索引范围，
05 接入多源切换，10 负责来源/路径生命周期。catalog id 不因同名迁移给另一路径。
02 的 catalog 内容翻译接入与联测尚待其主分支集成，不能把 loader 的存在当作已接通。
