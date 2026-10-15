# 05 多源目录与播放隔离验证

## 实现批次

本票从集成提交 `e5dc48e692b956a5abfe4d681ebf6629ddc93b3a` 创建独立工作树。复用现有来源可用性 API、目录控制器、请求传输和全局播放器；不新增依赖或迁移。下一步吸收 02／07 集成提交后复验来源与语言契约。

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

合成样例为两部保留编号作品、一个本地来源和三个通用远端来源。A 有新检查结果，B 未知，C 过期并返回 500；B 目录可延迟、改名，C 可失败与恢复。媒体为生成的 180 秒 WAV，没有真实目录数据。

## 当前验证

| 命令／入口 | 结果 |
| --- | --- |
| `go test -mod=readonly ./internal/httpapi -run '^Test(SourceAvailability\|SourcePlayback\|RemoteActionsRequire\|RemoteWorkTrackCache\|InvalidateRemoteWorkCache)' -count=1 -v -timeout=3m` | 6 个顶层测试通过，退出 0。H 使用真实路由、会话和迁移 SQLite。出站子场景覆盖非法协议、内嵌凭据、越界私网重定向、32 MiB 响应上限和取消。配置私网正常请求在可用性 H 中覆盖。 |
| `go test -mod=readonly ./internal/outbound ./internal/kikoeru -count=1 -timeout=3m` | 2 包通过，退出 0；共享重定向、地址、DNS 与凭据边界回归。 |
| `playwright test tests/e2e/source-playback-live-api.spec.ts --project=desktop-chromium --workers=1` | 新实例上 2 项通过，7.1 秒，退出 0。真实前后端连接，无自有 API 拦截。 |
| `npm run test:unit -- src/features/work-detail/source/sourceContextModel.test.ts src/features/work-detail/persistedWorkDetailModel.test.ts src/player/playerPersistence.test.ts` | 18 项通过，退出 0。 |
| `make backend-format backend-vet backend-build` | 通过，退出 0。 |
| `make backend-lint GOLANGCI_LINT_BASE=9949175e53979bcd590c75a624bc0efc67684da3` | 0 issues，退出 0。首次直接联网失败；按每进程代理重试通过。 |
| `make frontend-lint frontend-build frontend-docs` | 0 lint errors，类型和构建通过，121 份公共 Markdown 链接、5 种文档语言通过，退出 0。首次发现未用导入和 cleanup ref 规则问题，修正后通过。 |

播放 F 对切标签前后的真实 audio `currentSrc`、两项队列及当前位置作断言，要求时间继续增长且不得超过实际经过时间的容差。显式选择第二音轨从零播放；通过 Seek 保存约 45 秒，公开游标 API 回读具体 media item，重开页面在另一来源目录执行 Resume 后仍恢复原具体音轨与位置。H 另外验证同名不同来源文件不能混用游标、非法位置归属被拒绝、第二账户读不到游标。

所有本期 F 均设置 screenshot、trace、video 为 off，仅检查文本、操作、请求、持久状态和媒体功能。未运行像素、几何或人工视觉验收。

## 重现与边界

在独立后端启动 `go run ./tests/source-playback-fixture`，默认绑定回环端口 17615；前端设置 `VITE_API_BASE_URL=http://127.0.0.1:17615`，运行 `npm run dev -- --port 3115 --strictPort`。测试时设置 `PLAYWRIGHT_BASE_URL=http://127.0.0.1:3115` 与 `SOURCE_PLAYBACK_API_URL=http://127.0.0.1:17615`，再运行上述 F 命令。每次整体验证使用新的 fixture 进程和新建迁移数据库；`POST /__fixture/stop` 结束 fixture，并仅停止自己的前端进程。

当前测试服务均已停止。保留被忽略的项目依赖、构建产物和工具日志；未清理其他任务文件。未部署生产，未主张实际源的全部编码／音质、跨浏览器或无缝切源已通过。02／07 合并和其后最终复验尚待完成。
