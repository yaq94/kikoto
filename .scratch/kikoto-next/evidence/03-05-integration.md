# 03/05 本地集成验收

本记录对应包含此文件的集成提交，分支为 `codex/kikoto-next-integration`。
起点 `08b14696c76c63d3a75e5fb0c984dbb2e568ad12`，03 精确提交为
`2d3f79618e102f7d390fdc6279476bb22cb98ca5`，05 精确提交为
`65117917f17252ca9dd2913801ce4d3e64e39989`。两票依次使用普通 Git merge，
均无冲突；03 合并提交 `02aab1e` 已独立推送。本批在两票原产物之外修正
catalog 评价保存后的状态同步，并更新执行状态、真实来源诊断摘要及本证据。

固定最终审查基线仍为 `9949175e53979bcd590c75a624bc0efc67684da3`。
01、02、03、04、05、06 为 resolved；07 为 verified-local、真实媒体 LIVE
未通过；08 在独立工作区实施。本批不表示全部 17 票或生产部署完成。

## 组合行为与证据

- 03：OpenCC `s2t` 单例、简繁共享简体缓存与在途请求、现成语言字段优先、
  名称保护、转换失败与重试；catalog 访问复核及真实 path 不变。原票两条实际
  Index 文本证据见 [03 验收](03-traditional-conversion.md)，合并没有改变该
  客户端，不增加真实调用。当前组合普通测试的 LIVE SKIP 不计作新成功证据。
- 05：未知及过期来源逐源检查、失败隔离、手动刷新、被取消目录重试、只读目录
  与资料权限；catalog 的数字 edition/编号/alias 只读投影、受限单根刷新、
  有界 Range 不落缓存。合成来源测试覆盖重定向、私网、超限、路径穿越与取消。
- F：切来源或内容语言不重建目录和播放队列，音轨位置继续推进；点另一音轨
  从零开始，继续按钮回读具体文件的持久进度。catalog 独立页和登记作品页使用
  相同 entry id + path 的媒体身份。目录翻译请求保留 catalogEntryId 与真实 path。
- 069：组合 R 从既有库升级并实际关闭重开，编号、无码、关系与两用户记录保留。
  `git diff 08b1469 -- backend/migrations` 为空，最高仍 069，开发基线仍
  `compat/069_dev.sql`；070 已保留给独立实施中的 08，未合入其任何改动。

## 实际命令与退出状态

使用 Go 1.26.9、Node 24.19.0、npm 11.17.0，每进程 PATH 与
`GOTOOLCHAIN=local`。复用现有模块和前端依赖；未安装新工具或改变全局配置。
定向 H/R 使用 `-count=1`，全包检查允许未改包的内容寻址缓存。

| 命令 / 范围 | 结果 |
| --- | --- |
| `go test -mod=readonly ./internal/httpapi -run '^TestContentTranslationTraditional' -count=1 -v -timeout=3m` | 03 合并提交前验收 `j-1f5xb2`，退出 0；6 项 H 通过、1 项 LIVE SKIP。 |
| `go test -race -mod=readonly ./internal/httpapi ./internal/translation -run '^Test(ContentTranslation\|CacheIdentity\|ProtectedNames\|SourceAvailability\|SourcePlayback\|RemoteActionsRequire\|RemoteWorkTrackCache\|InvalidateRemoteWorkCache\|CatalogDirector\|CatalogLocation\|CatalogRefresh\|OpenList)' -count=1 -v -timeout=5m` | `j-ejy39h` 退出 0；39 个 H/R 顶层、2 个 U 顶层通过，无 race；2 项未配置 LIVE SKIP。httpapi 37.462s。Markdown 管道已转义。 |
| `make backend-format backend-test backend-vet backend-verify backend-build backend-lint GOLANGCI_LINT_BASE=9949175e53979bcd590c75a624bc0efc67684da3` | `j-1dmwte` 退出 0；全后端通过、httpapi 82.567s，所有模块校验通过，lint 为 0 issues。共享锁已释放。 |
| `make -o frontend-install frontend-format frontend-lint frontend-i18n frontend-build frontend-docs` | `j-uzo24q` 退出 0；格式、0 lint 警告/错误、E2E 类型和生产构建通过；122 公开 Markdown、5 locale/11 用户页链接一致。保留既有 Vite configLoader 与大 chunk 提示。 |
| `npm run test:unit -- src/features/work-detail/source/sourceContextModel.test.ts src/features/work-detail/persistedWorkDetailModel.test.ts src/player/playerPersistence.test.ts src/player/trackLocations.test.ts src/player/remoteStreamCacheRequest.test.ts src/features/work-detail/media/mediaTreeModel.test.ts` | `j-1o7m33` 退出 0；6 文件、50 项通过。 |
| `make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='content-translation.spec.ts traditional-translation.spec.ts --project=desktop-chromium --workers=1 --output=test-results/merge-03-05-content'` | `j-syi0u1` 退出 0；5/5，12.4s。 |
| `make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='source-playback-live-api.spec.ts --project=desktop-chromium --workers=1 --output=test-results/merge-03-05-source-fixed'` | 修复后 `j-6jkn6g` 退出 0；3/3，14.2s。修复前 `j-zvbjrh` 同样 3/3。 |
| `make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='openlist-catalog.spec.ts --project=desktop-chromium --workers=1 --output=test-results/merge-03-05-catalog-fixed'` | 修复后 `j-52xshf` 退出 0；2/2，9.0s。 |

F 使用当前 Vite 3116 和 production Routes/真实会话/迁移 SQLite；翻译夹具 API
为 17616，来源/播放夹具沿用其固定 17615。外部模型、目录和合成 WAV 是网络边界
夹具，没有拦截自有 API。首轮翻译 F `j-1s7ieb` 退出 2：夹具进程未设置
`PLAYWRIGHT_BASE_URL`，仍只允许默认 3112，导致页面用户菜单不可用；设置实际
3116 origin、关闭旧夹具并创建新临时库后，上述 5 项完整通过。未改产品代码。

所有选择的 F 在文件内关闭 screenshot、trace、video，仅检查语义操作、文本、
请求、持久状态及媒体功能；无像素、几何或人工视觉签收。合成媒体不证明任意
编码、全部浏览器或真实来源可播放。

## 集成回归修正

补跑 07 的真实 API 功能用例后，`j-jq25tb` 退出 2：目录和媒体已正常读取，
评价也已保存，但 `PersonalReviewEditor` 的成功提示消失；同批另一项 catalog
翻译 F 通过。根因是 `CatalogWorkDetail` 保存后把评分、备注结果重新写入传入
编辑器的 value，触发其 props 同步效果清掉完成状态。本批包含 05 的 workId 游标读取与父组件更新，在该实际合并产物上复现了此
同步问题。编辑器本身负责保存结果和字段草稿，
既有 `RemoteOnlyWorkDetail` 的成功路径仅回传作品登记身份。

最小修复沿用这一先例：catalog 父组件只更新返回的 `workId`，不重复回传编辑器
已接收的评分/备注。未修改共享编辑器、API、迁移、播放器或测试断言。
原失败用例保持不变；新临时库上完整 2 项 OpenList F 通过（`j-52xshf`，退出 0，
9.0s），含首次登记、保存提示、0 分、备注、作品页重开、清分与持久 API 回读。
修复前还曾因 OpenList 夹具使用独立 `OPENLIST_FRONTEND_ORIGIN` 而非
`PLAYWRIGHT_BASE_URL` 配置名发生环境失败（`j-6rc2n0`，退出 2）；准确配置
origin 后才取得上述产品回归红绿，不把环境错误当作红灯证明。

修复后重新执行 `make -o frontend-install frontend-format frontend-lint frontend-build
frontend-coverage frontend-docs`（`j-l2j7h5`，退出 0）；格式、0 lint 警告/错误、
E2E 类型、生产构建、153 文件/867 项前端单元测试及文档检查通过。详细终态日志保存在
本机工具任务日志中，不提交原始日志。

## 真实来源诊断边界

[07 证据](07-openlist.md#live-缺口与可复验入口)已同步仓库外
`openlist-client-diagnosis.md` 的脱敏结果：同一文件在 `08b1469` 上直连无应用
代理时连接重置、没有 HTTP；正式 Settings 测试代理下返回 HTTP 403 HTML、
`cf-mitigated: challenge`。本次没有 1010、raw_url 或 Range 读取；生产 fs/get
未重测。IAB 匿名网页正常播放的既有观察保持有效。环境 HTTP_PROXY 不自动成为
数据库代理路由，17 的真实验收必须核对正式应用代理配置。

本批没有重复挑战入口请求、读取 cookies、变更客户端指纹或生产配置。07 LIVE
仍未勾选；08/11 只解除已批准的本地开发先决条件，完整发布依赖仍保留。

## 文档、进程与保留项

复用 `checkFile` 检查 31 份计划/证据 Markdown，相对链接均有效；17 张工单的
状态与验收清单保留。逐行比较确认 46 个用户故事、28 行 AC 与 `08b1469` 一致；
07 的 verified-local 和未勾选 LIVE 保持。没有把工单依赖或验收降为范围外。
提交前 `make sensitive-check` 扫描 42 个修改文件通过；工作区与暂存区
`git diff --check` 均退出 0。

本次翻译夹具 `j-y8lj85`、`j-u6zusx`，来源夹具 `j-79vhkf`、`j-ee7sag`，
OpenList 夹具 `j-a2y5q6`、`j-pr9cjj`、`j-u4w253` 均经自身 stop 端点正常退出 0；
两次自有 Vite 任务 `j-ssvo46`、`j-xum7dr` 已 killed。最终独立绑定检查确认
3116、17616、17615 三个端口空闲。所有检查任务均已读取终态。

本次结果目录没有 png、webm、zip。保留现有被忽略的依赖、构建和测试结果；
未删除或移动其他 worktree、02 的两份已拒删合成缓存或 07 的旧系统临时库。
本次没有新增删除拒绝，也未尝试其他方式处理那些保留项。原始日志仍在仓库外。
