# 01 与 04 集成验收

本次验收日期为 2026-10-15。集成分支为 `codex/kikoto-next-integration`，固定总审查基线保持 `9949175e53979bcd590c75a624bc0efc67684da3`。

## 产物与差异核对

- 01 候选为 `a51537743b0ea649de35b534dc0b3c631bad5122`，04 候选为 `4e8595d0b88ad2ead5cf13cb013f9bd13574fdd3`；远端分支 SHA 已分别回读核对。
- 01 从 `71a9315` 快进合入；04 随后合并，`server.go` 与后端架构文档自动合并成功，没有文本冲突。两个候选均包含 `71a9315`。
- 验收对应以上源码组合及本证据所在提交。集成新增的代码修改仅为 04 测试夹具读取 `PLAYWRIGHT_BASE_URL` 作为允许来源；默认值保留。应用源码没有额外改写，未新增依赖或迁移。
- 核对了配置存储、密钥投影、模型请求、出站策略、试译路由和前端绑定，以及个人字段部分更新、远端必要登记、用户隔离、详情投影和两类详情页入口。两票的完成清单与 AC 内容保持。

## 本次实际验证

工具链：Go 1.26.9、Node 24.19.0、npm 11.17.0。命令使用进程级 PATH 与 `GOTOOLCHAIN=local`，没有更改全局配置。以下命令在仓库根执行，另注明者除外。

| 命令或入口 | 实际结果与范围 |
| --- | --- |
| `go test -mod=readonly ./internal/httpapi -run '^TestTranslation(AdminConfiguration\|Configuration\|Trial)' -count=1 -v -timeout=2m`（backend，01 快进后） | 退出 0；8 个 H/R 顶层用例、14 类失败场景及目的地边界通过。 |
| `go test -mod=readonly ./internal/httpapi -run '^(TestTranslation(AdminConfiguration\|Configuration\|Trial).*\|TestPersonalReview.*\|TestResolveIsReadOnlyForCanonicalAliasEditionAndLegacySnapshot)$' -count=1 -v -timeout=3m`（backend，两票组合） | 退出 0；14 个顶层用例，无跳过，包含两票真实 HTTP、迁移 SQLite、关闭重启、私有评价隔离与既有身份解析。 |
| `make backend-format backend-vet backend-test` | 退出 0；全后端检查通过，HTTP 包 86.642 秒；迁移、库、账户、个人状态、出站策略及集成测试均通过。 |
| `make -o frontend-install backend-build frontend-lint frontend-build frontend-docs frontend-i18n` | 退出 0；后端应用构建、前端构建、零 lint 警告/错误、E2E 类型、121 份公共文档链接、5 种文档语言和 UI 文案检查通过。 |
| `npm run test:unit -- src/features/work-detail/source/sourceContextModel.test.ts src/app/workDetailNavigation.test.ts src/player/playerPersistence.test.ts src/i18n/usedKeys.test.ts --reporter=verbose`（frontend） | 退出 0；4 文件、21 项通过。 |
| `make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='personal-review.spec.ts personal-review-live-api.spec.ts --project=desktop-chromium --workers=1'` | 退出 0；3/3 通过，10.3 秒；一条连接真实生产模式 API、会话与 SQLite，两条验证界面提交、失败重试和单字段清除。 |
| `make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='translation-settings.spec.ts --project=desktop-chromium --workers=1'` | 退出 0；2/2 通过，5.6 秒；当前构建的真实后端、管理员表单、密钥清除、普通用户权限和取消恢复。 |
| `make backend-format`（夹具来源调整后） | 退出 0；夹具格式通过。 |

F 使用本次专用前端 3119 与后端 17619，按两票顺序运行。评价夹具设置 `PERSONAL_REVIEW_FIXTURE_ADDR` 与 `PLAYWRIGHT_BASE_URL`；翻译用例设置 `KIKOTO_E2E_BACKEND`、`KIKOTO_E2E_API_URL`、`PLAYWRIGHT_BASE_URL`。两票 F 文件均显式关闭 screenshot、trace、video，未使用视觉断言。

夹具可配置性的红绿证据：固定旧来源时，3119 的真实 F 在取评价控件时超时，Playwright 退出 1、Make 退出 2；携带该 Origin 的 API 请求实测 HTTP 403 且无允许来源头。修正后同请求 HTTP 200、允许来源头等于本次浏览器来源，并且上述三条 F 通过。失败来自测试来源配置，不计为功能通过。

## 验收范围与衔接

- 01：AC-01、AC-02 及所属 AC-27/28 的本地行为已在集成组合复验。新客户端的单次真实 Index 生成沿用 [01 已取得的 LIVE 证据](01-translation.md#live-结果)，集成未改动该客户端且未重复付费或外部请求。
- 04：编号作品的 AC-12、AC-13 与所属 AC-28 已复验。首次登记不下载媒体、跨源同用户回读、部分更新、清除及重启均有对应 H/R/F 证据。
- 02 可复用 `internal/translation` 的 `Store`、`Config`、`Input`、`Result`、`Translate` 和 `TemplateVersion`，通过 `httpapi.newTranslationClient` 复用精确 origin 与 Other 代理路由。`Config` 含密钥，不可序列化到 API；当前单试译限制在 HTTP 入口，02 需统一自动翻译与试译的资源预算。
- 04 可复用 `workUserStatePatch`、`patchWorkUserState`、`PersonalReview`、`PersonalReviewPatch` 和 `PersonalReviewEditor`。数字 workId 的精确版本语义及编号 canonical 语义保持。
- 后续协调见 [06 集成复验](06-integration.md)：06 已验收且无新增迁移；02 使用独立 CacheRoot，不占用业务 SQLite 迁移，069 预留给 07。其余票不因本批通过而提前完成。

## 限制与清理

本批没有重跑 01 作者的 race 或全前端 867 项测试，使用以上当前组合的定向与全后端验证；没有生产部署、外部媒体、全站索引或视觉验收。现有 Vite 配置兼容性及大分块提示仍出现，构建退出 0。

两轮评价夹具经自己的停止入口关闭，退出 0；翻译 F 自行关闭后端与模型桩并清理临时库。专用 Vite 由本次后台任务管理器停止；TCP 回读确认 3119 与 17619 均已关闭。当前 test-results 中没有 PNG、trace ZIP 或视频。依赖和可复用构建产物保留，作者 worktree 未改动。提交前 `make sensitive-check` 与暂存/未暂存的 `git diff --check` 均退出 0；定向文档检查确认 7 份链接有效、01/04 清单正文与 28 项规格 AC 保持不变。
