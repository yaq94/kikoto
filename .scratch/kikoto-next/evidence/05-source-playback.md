# 05 多源目录与播放隔离验证

## 实现批次

本票从集成提交 `e5dc48e692b956a5abfe4d681ebf6629ddc93b3a` 创建独立工作树，首批提交为 `c34ae93`。已合入 02／07 集成 `08b14696c76c63d3a75e5fb0c984dbb2e568ad12`，保留内容语言专用事件、目录真实 path 与 catalog 翻译 scope。复用现有来源可用性 API、目录控制器、请求传输和全局播放器；本票不新增依赖或迁移。

来源结果新增逐源 UTC `checkedAt`，直接读取既有 presence 时间；24 小时或未知状态触发逐源、可取消的后台检查。只读用户可读取未知目录但不写检查结果。手动 `force` 检查使成功来源的预览缓存失效。目录取消、刷新或单源失败保留其他来源和已载入文件，不修改全局播放队列。

## 红绿证据

| 回归 | 红 | 绿 |
| --- | --- | --- |
| H 逐源检查时间 | 已知来源无逐源 `checkedAt`，断言失败，退出 1 | 来源时间与未知空时间、单源失败和恢复通过，退出 0 |
| F 未知／过期检查 | 打开作品后两个来源均无真实上游请求，退出 1 | 逐源请求、独立状态与本地目录通过，退出 0 |
| F 切走待完成目录再切回 | B 目录未出现，退出 1 | 取消后重新载入 B，退出 0 |
| F 手动刷新目录 | 已更新的 B 文件名未出现，退出 1 | `force` 刷新当前目录成功，退出 0 |
| F 保留已载入的失败来源 | C 检查失败后旧文件消失，退出 1 | 失败状态与旧文件同时保留，退出 0 |
| F 只读未知来源 | 未出现 A 目录，退出 1 | 目录可读取，没有 POST 可用性请求，退出 0 |
| H catalog 目录投影 | 新 GET 返回 404，退出 1 | 编号／别名、精确数字 edition 和无码双源可读，且不登记作品／媒体，退出 0 |
| H 单目录刷新 | 新 POST 返回 404，退出 1 | 权限、目标 root、目录更新及失败保留通过，退出 0 |

合成样例为两部保留编号作品、一部无码作品、一个本地来源、三个通用编号远端来源和两个 catalog 来源。A 有新检查结果，B 未知，C 过期并返回 500；B 目录可延迟、改名，C 可失败与恢复。catalog 包含编号投影、无码双目录、手动刷新与失败开关。媒体为生成的 180 秒 WAV，没有真实目录数据。

## 当前验证

| 命令／入口 | 结果 |
| --- | --- |
| `go test -mod=readonly ./internal/httpapi -run '^Test(SourceAvailability\|SourcePlayback\|RemoteActionsRequire\|RemoteWorkTrackCache\|InvalidateRemoteWorkCache)' -count=1 -v -timeout=3m` | 6 个顶层测试通过，退出 0。H 使用真实路由、会话和迁移 SQLite。出站子场景覆盖非法协议、内嵌凭据、越界私网重定向、32 MiB 响应上限和取消。配置私网正常请求在可用性 H 中覆盖。 |
| `go test -mod=readonly ./internal/outbound ./internal/kikoeru -count=1 -timeout=3m` | 2 包通过，退出 0；共享重定向、地址、DNS 与凭据边界回归。 |
| `playwright test tests/e2e/source-playback-live-api.spec.ts --project=desktop-chromium --workers=1` | 最终适配后新实例 3 项通过，13.6 秒，退出 0（j-4bkpbp）。真实前后端连接，无自有 API 拦截。 |
| `npm run test:unit -- src/features/work-detail/source/sourceContextModel.test.ts src/features/work-detail/persistedWorkDetailModel.test.ts src/player/playerPersistence.test.ts src/player/trackLocations.test.ts src/player/remoteStreamCacheRequest.test.ts src/features/work-detail/media/mediaTreeModel.test.ts` | 最终适配后 6 文件、50 项通过，退出 0（j-xbdpni）。 |
| `make backend-format backend-vet backend-build` | 通过，退出 0。 |
| `make backend-lint GOLANGCI_LINT_BASE=9949175e53979bcd590c75a624bc0efc67684da3` | 最终追加出站测试后仍为 0 issues，退出 0（j-rwcufz）。 |
| `make frontend-format frontend-lint frontend-build frontend-docs` | 最终格式、0 lint errors、E2E 类型与生产构建通过；122 份公共 Markdown 链接、5 种文档语言／11 用户页面通过，退出 0（j-lvilkw）。 |

最终组合 H：`go test -mod=readonly ./internal/httpapi -run '^Test(SourceAvailability|SourcePlayback|RemoteActionsRequire|RemoteWorkTrackCache|InvalidateRemoteWorkCache|CatalogDirector|CatalogLocation|OpenList|ContentTranslationCatalog)' -count=1 -v -timeout=5m`，19 个顶层测试通过，1 个外部 LIVE 测试因未配置真实样例跳过，退出 0（j-b6z4yj，9.695 秒）。此外 `-run '^TestCatalogRefresh' -count=1 -v -timeout=2m` 两项通过，退出 0（j-q9issn），直接覆盖新刷新入口的 2 MiB 限制、越界私网重定向、路径穿越与取消。取消测试的首个 fixture 没有消费请求体导致服务器无法及时观察断连；消费请求体并给清理路径设置独立完成信号后通过。

catalog 的只读投影按工作引用和已有 alias 解析，不创建 work、media、workflow。显式播放已登记目录时批量幂等准备具体 media/location，播放身份为 entry id 与真实相对 path。H 回读 42 秒游标、拒绝显示名冒充 path，并证明同名不同目录不共用时间轴。`/api/media/{id}/stream` H 验证 206、精确 Content-Range、源归属与无缓存文件。07 既有出站、URL 到期重取、权限、升级与重启回归均在组合 H 中复验。

catalog F 在首次点击前回读 work/media 数量不变；切源和切内容语言不重建目录或修改队列。跨独立 catalog 页与保存作品页保持相同 media/location/playbackKey，42 秒续播恢复原文件，显式点击另一文件从零开始。目录翻译请求保留 catalogEntryId 与真实 path。刷新只针对选中 entry，失败后旧文件仍可见。F 中间运行修复过合成上游 fs/get 文件名不匹配、展开播放器遮挡按钮及同名播放器文本造成选择器歧义；最终实例完整通过。

播放 F 对切标签前后的真实 audio `currentSrc`、两项队列及当前位置作断言，要求时间继续增长且不得超过实际经过时间的容差。显式选择第二音轨从零播放；通过 Seek 保存约 45 秒，公开游标 API 回读具体 media item，重开页面在另一来源目录执行 Resume 后仍恢复原具体音轨与位置。H 另外验证同名不同来源文件不能混用游标、非法位置归属被拒绝、第二账户读不到游标。

所有本期 F 均设置 screenshot、trace、video 为 off，仅检查文本、操作、请求、持久状态和媒体功能。未运行像素、几何或人工视觉验收。

## 重现与边界

在独立后端启动 `go run ./tests/source-playback-fixture`，默认绑定回环端口 17615；前端设置 `VITE_API_BASE_URL=http://127.0.0.1:17615`，运行 `npm run dev -- --port 3115 --strictPort`。测试时设置 `PLAYWRIGHT_BASE_URL=http://127.0.0.1:3115` 与 `SOURCE_PLAYBACK_API_URL=http://127.0.0.1:17615`，再运行上述 F 命令。每次整体验证使用新的 fixture 进程和新建迁移数据库；`POST /__fixture/stop` 结束 fixture，并仅停止自己的前端进程。

当前测试服务均已停止：最终 API fixture 正常退出 0，Vite 的自有后台任务已停止。保留被忽略的项目依赖、构建产物和工作区外工具日志；未清理其他任务文件。本票不部署生产，不主张实际源 LIVE、全部编码／音质、跨浏览器或无缝切源已通过；07 外部 LIVE 与最终部署仍归各自工单。本票 H/F 合成主线和 02／07 兼容验收已经完成。
