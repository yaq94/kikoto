# 10 来源生命周期验收

- 日期：2026-10-15。
- 分支：`codex/kikoto-next-t10`；实现基于 `e5b8ca33074847c90daa58b0ea83ae84813d48f6`，集成底座为 `f0fd5d77be1072f260fc817245c01d1b6334ea0f`。
- 证据对应包含本报告的实现提交；072 迁移及生成基线保留，未改动已发布迁移。
- 入口：真实 HTTP、会话、迁移 SQLite、同一应用/数据库关闭重开、少量 Chromium 纯功能；上游为合成服务，未调用真实来源。

## 已验证行为

| 范围 | 当前证据 |
| --- | --- |
| AC-20 唯一改名 | 完整 SHA-256 文件证据唯一时保留 catalog/work/media/location 引用；旧播放 URL 能解析到新根路径并返回 Range 206。缺哈希、畸形/矛盾哈希、多候选及已评价候选保持独立，返回待确认候选。 |
| AC-23 状态与恢复 | 维护、访问受限、暂时失联、未确认空目录、完整扫描确认缺失及恢复分别保存；失败扫描不判永久删除，原卡片和评价保留。格式子目录受限会反映到所属作品；手动刷新失败不丢保存目录，成功可恢复状态。 |
| AC-23 清理 | 资料权限及确认校验；有其他用户评分、收藏、标签、听取记录、媒体进度、播放游标、歌词偏好或推荐记录时保留共享作品并返回 `personal_records`。同批无个人引用且不可用的作品仍可删除。 |
| D-05 主动媒体清理 | 所选媒体文件仍按清理请求移除；引用该作品的个人记录与共享作品保留，forget 阶段及整体 workflow 返回 partial，不伪报已忘记作品。该行为来自 D-05“仍有个人记录引用时延后共享对象物理删除”。 |
| AC-09 / AC-13 / R | 072 升级保留既有条目与记录；跨连续扫描及同库三次启动保留身份、候选与两用户独立评价。单源故障不改其他源；删除来源配置后，已登记且有个人记录的作品仍出现在两用户的常规 `/api/works` 查询中。 |
| F / AC-28 | 默认目录避开禁用来源并选择可用目录；禁用来源的评价仍可编辑。候选提示提供单独导航。既有三条真实前后端目录/播放回归通过：切换浏览保持队列、当前媒体与时间推进，点播/继续及局部刷新保留语义。 |

删除来源配置与完整扫描确认上游内容缺失分别验收。前者保留稳定 catalog 链接、已登记作品和个人记录，不删除存储中的媒体文件；后者在原来源卡片列表继续呈现缺失条目。未登记孤立条目保留稳定链接，不新增未约定的浏览分类。

## 当前验证命令和结果

工具链：Go 1.26.9、Node 24.19.0、npm 11.17.0；每进程复用既有工具链，`GOTOOLCHAIN=local`。

| 命令 | 结果 |
| --- | --- |
| `make backend-test` | 全包通过，httpapi 167.164 秒；同次后续静态步骤曾因下载网络超时失败，不能将组合命令标为全部通过。 |
| `go test -mod=readonly ./internal/httpapi ./internal/storage -run '^Test(SourceCatalog\|MediaCleanupForgetWork\|DeleteUnlinked\|OpenList\|Catalog)' -count=1 -v -timeout=4m` | 退出 0，56 个顶层测试通过；包括最后的格式子目录修复。明确 opt-in 的 `TestOpenListLiveRangeSample` 因未配置真实样例而跳过，不计 LIVE 证据。 |
| `go test -race -mod=readonly ./internal/httpapi -run '^TestSourceCatalog(Lifecycle\|Relocation\|Cleanup)' -count=1 -v -timeout=3m` | 退出 0，7 个顶层测试通过，30.616 秒，无 race。 |
| `go test -race -mod=readonly ./internal/httpapi -run '^TestSourceCatalogLifecycleRestart' -count=1 -v -timeout=2m` | 退出 0，补充两用户在来源配置删除后的常规作品库可发现性，9.298 秒。 |
| `make backend-format backend-vet backend-lint backend-build` | 退出 0；lint 为 0 issues。复用已有命令级网络代理取得工具，无全局配置变更。最后新增只读断言后再次 format/lint 退出 0。 |
| `make frontend-format frontend-lint frontend-docs frontend-i18n frontend-coverage frontend-build` | 退出 0；867 个测试/153 文件通过；类型、E2E 类型、代码格式、文档链接/locale、文案检查及构建通过。 |
| `npm run test:e2e -- tests/e2e/source-lifecycle.spec.ts tests/e2e/source-playback-live-api.spec.ts --project=desktop-chromium --workers=1` | 退出 0，5 项通过，18.9 秒。使用实际 source-playback-fixture 与 Vite；截图、trace、video 关闭。候选提示一项仅注入展示响应，持久候选及身份保护以 H/R 为据。 |

## 红绿与修复记录

- 新手动刷新测试先观察到 `available`，期望 `maintenance`；修复后维护、空目录与恢复序列通过。
- 新格式子目录测试先观察到所属作品 `available`，期望 `restricted`；修复范围归属后通过。
- 默认目录 F 先无法找到备用音轨；选择可用目录后通过，再明确复现缺少来源状态提示，补齐提示后通过。
- 全后端的旧 forget-work 断言要求级联删除个人记录，与 D-05 冲突；调整为验证文件清理完成、共享对象保留及 partial 原因，并在公开清理 HTTP 测试中验证其他用户的引用与无引用作品。
- F 初期夹具更新遗漏 endpoint，污染后续播放；改成完整保存/恢复该合成源配置并以新夹具复跑 5 项通过。未放宽断言或增加重试。
- lint 的旧重启测试分支触发 QF1003，改成等价 switch 后通过。

## 交付边界

本票本地验收已取得，仍沿用 07/08 的真实服务与索引覆盖发布前置，保持 `verified-local`。本报告不宣称真实来源媒体/全站覆盖、生产迁移、部署或视觉验收完成。原始合成测试日志存放在仓库外，临时测试服务已停止；完整集成及生产验收由主流程统一完成。
