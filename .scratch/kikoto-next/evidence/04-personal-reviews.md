# 04 个人评分与备注验收

本记录对应与它一起提交的实现，基于集成提交 `71a9315`，审查基线为 `9949175`。工具链为 Go 1.26.9、Node 24.19.0、npm 11.17.0；仅使用进程级 PATH 和既有代理。未增加项目依赖、迁移或更改全局配置。

## 交付与验收映射

| 要求 | 当前产物与证据 |
| --- | --- |
| AC-12：评分、备注、部分更新与清除 | `PATCH /api/works/{id}/user-state` 复用既有存储，评分为 0–5 整数或 null，备注为字符串。RawMessage 区分遗漏/null；SQL 部分更新保留未提交字段。HTTP 用例验证 0、更新、分别清除和收听状态的既有空值清除语义。 |
| AC-12：必要登记与无媒体副作用 | 远端 `user-state` 路由复用 source 校验、身份解析、upsert 与来源 presence；同一事务保存登记和评价。HTTP 回读作品库、空媒体列表及空工作流列表，上游请求记录只有作品元数据；包含封面地址的夹具不触发封面、音轨树或音频请求。 |
| AC-12：跨源、重开及重启 | 两来源保存返回同一 workId，远端与已登记详情回读同一评价。R 实际关闭 HTTP 服务、Server 生命周期和数据库，再打开同一迁移 SQLite，由新 HTTP 服务凭原会话回读 0 与备注。 |
| AC-13：私有数据与权限 | 两个真实登录会话分别写入并交叉回读；本地详情及共享远端缓存均隔离。公共 rating_average 保持。匿名、Demo、无 remote:track 的首次登记拒绝；普通用户可评价已登记对象。 |
| AC-28：实际页面操作 | 3 个 Chromium 桌面纯功能场景通过。2 个场景验证组件请求、失败重试、重开、评分0和单字段清除；1 个场景直接连接生产模式 Routes、真实 cookie 与迁移 SQLite，贯通本地保存、远端登记、第二来源回读与清除。所有本票 F 文件关闭 screenshot/trace/video，无截图、像素、布局断言。 |
| 新出站路径边界 | 显式配置的本地上游可访问；越界私网重定向、超过 2 MiB 的响应、返回编号不符、请求取消均拒绝且库中无残留作品。HTTP 公共错误不含上游地址或合成 token。 |

数字 workId 保持精确版本语义，编号入口继续使用既有 canonical 解析。个人字段命名为 personalRating/personalNote，与公共 rating 分离。此票未实施无码、系列、关联、播放器或翻译能力。

## 红到绿证据

- `go test -mod=readonly ./internal/httpapi -run '^TestPersonalReviewPartialUpdatesAndUserIsolation$' -count=1 -v -timeout=3m`：红退出 1（个人字段不存在）；实现后绿退出 0。回归扩展再发现空收听状态的兼容差异，红退出 1，修复后通过。
- `go test -mod=readonly ./internal/httpapi -run '^TestPersonalReviewRegistersOnlyMetadataAndSharesAcrossSources$' -count=1 -v -timeout=3m`：红退出 1（远端路由 404）；实现登记与远端个人投影后通过。
- `go test -mod=readonly ./internal/httpapi -run '^TestPersonalReviewRejectsUnsafeOrInvalidRemoteMetadata$' -count=1 -v -timeout=3m`：红退出 1（超大响应返回 200）；限制 2 MiB 后 4 个边界场景通过。
- `make -o frontend-playwright-install frontend-e2e E2E_ARGS='tests/e2e/personal-review.spec.ts --project=desktop-chromium --workers=1'`：红 Make 退出 2、Playwright 退出 1（缺少保存入口）；补齐组件并修正标签后 2 项通过，退出 0。

## 当前检查结果

| 命令 | 退出与范围 |
| --- | --- |
| `go test -mod=readonly ./internal/httpapi -run '^(TestPersonalReview.*\|TestResolveIsReadOnlyForCanonicalAliasEditionAndLegacySnapshot)$' -count=1 -v -timeout=3m` | 0；6 个顶层测试，其中远端失败测试包含 4 个子场景；无跳过。 |
| `make backend-format backend-test` | 0；全后端测试包通过，含鉴权、个人状态、来源、库、存储、迁移与集成测试。 |
| `make backend-vet` | 0。 |
| `make backend-format backend-lint` | 0；golangci-lint 0 issues。 |
| `make -o frontend-install frontend-build frontend-lint frontend-format` | 0；构建、oxlint、E2E 类型和全前端格式通过。 |
| `make -o frontend-install frontend-docs frontend-i18n` | 0；120 份公共文档链接、5 种文档语言及 UI 文案检查通过。 |
| `npm run test:unit -- src/features/work-detail/source/sourceContextModel.test.ts src/app/workDetailNavigation.test.ts src/player/playerPersistence.test.ts src/i18n/usedKeys.test.ts --reporter=verbose` | 0；4 文件、21 项通过。 |
| `make -o frontend-playwright-install frontend-e2e E2E_ARGS='tests/e2e/personal-review.spec.ts tests/e2e/personal-review-live-api.spec.ts --project=desktop-chromium --workers=1'` | 0；3 项通过、无跳过；最终执行 9.8 秒。 |

Make 的 `-o` 只跳过已通过的安装前置；先按仓库 `frontend-playwright-install PLAYWRIGHT_INSTALL_ARGS=chromium` 完成依赖与浏览器安装。Windows 运行中 Vite 占用原生依赖会使再次 npm ci 失败，因此安装与服务启动串行。安装中曾有 EPERM；停服务后成功。测试前置的首次设置弹窗由公开 onboarding API 完成。后端 lint 曾报告测试夹具的 3 个未处理 defer 返回值，修正后通过；另一次无代理查询模块超时，使用既有进程代理后通过。上述失败不计为功能通过证据。

## 全栈功能复验

1. 在 backend 运行 `go run ./tests/personal-review-fixture`。此测试专用进程创建临时迁移 SQLite、合成账户/作品及本地元数据上游，默认 API 端口 17614，最长运行 15 分钟。`PERSONAL_REVIEW_FIXTURE_ADDR` 可指定监听地址；使用其他浏览器端口时同时设置 `PLAYWRIGHT_BASE_URL`，使允许来源与浏览器一致，默认来源为 `http://127.0.0.1:3114`。
2. 在 frontend 设置 `VITE_API_BASE_URL=http://127.0.0.1:17614`，运行 `npm run dev -- --port 3114 --strictPort`。
3. 设置 `PLAYWRIGHT_BASE_URL=http://127.0.0.1:3114`、`PERSONAL_REVIEW_API_URL=http://127.0.0.1:17614`，运行上述 3 项 F 命令。使用全新夹具进程复验首次登记。
4. 向测试专用 `POST /__fixture/stop` 发请求，正常关闭 API 和数据库并删除它创建的临时目录；停止自己的 Vite 进程。

本轮夹具正常退出 0，Vite 已停止；仅保留忽略目录中的依赖、构建与纯文本测试产物。检查本票 test-results 未发现 PNG、trace ZIP 或视频。未执行生产部署、真实站点媒体下载、视觉验收或其他票要求；这些不属于 04 的编号作品评价交付。
