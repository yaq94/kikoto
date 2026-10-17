# Kikoto 下一阶段执行记录

**Status:** ready-for-agent
**Implementation:** in-progress
**阶段:** 01–07、14 resolved；08–13、15、16、18、19 已集成并保持 verified-local。双轴审查完成且发现已闭环；部署提交 `7ed73fa06b38686caf4422f5a9cbd090ba53b7a3` 的 Full CI 全绿（run 39049272400）；2026-10-17T16:44:24Z 已部署到现有服务，schema 68→76，备份与回滚演练通过；真实来源索引按断点续跑中。证据见 [17 部署证据](evidence/17-deployment.md)。
**依据:** [正式规格](spec.md) · [工单总览](ticket-proposal.md) · [领域词汇表](../../GLOSSARY.md) · [作品身份与个人记录 ADR](../../docs/adr/0001-work-identity-and-personal-state.md)

## 当前授权与完成条件

用户已授权全部 17 票的功能实现、必要依赖安装、测试、数据库迁移、部署与回滚。当前执行授权适用于全部工单；工单中的发布说明只描述发布行为，不限制本阶段已经确认的授权。功能要求、各票验收清单、权限、个人记录保护和非视觉测试边界保持有效，未完成需求不得删去或改列范围外。

2026-10-15 续做授权明确覆盖工单补齐、依赖调整、Git 提交与合并、推送现有私有仓库、必要迁移、服务更新与重启、实际部署验收及故障回滚，替代旧“生产部署单独决定”约定。新增 CI18、CI19 必须在 17 前集成；备份恢复、可执行回滚路径、全部实际验收和对应版本的 CI 终态仍为发布要求。

分诊 `Status` 保持 `ready-for-agent`。独立 `Implementation` 字段使用以下值：

| Implementation | 含义 |
| --- | --- |
| pending | 尚未认领，或等待前置票完成。 |
| in-progress | 已认领并准备或实施；不代表任何功能通过。 |
| verified-local | 当前产物已取得所列本地证据，仍有本票其他验收、真实样例或部署证据待补。 |
| resolved | 本票全部主责与共同验收均有对应当前产物的完整证据；17 还须完成部署验收。 |

每票自行完成入口、持久化、权限、页面操作与必要恢复验证。发布依赖只在完整验收后解除；已批准的开发先决条件见下文。文档、基础设施准备、单元测试、健康检查或代理报告都不能代替完整验收；由集成负责核对实际差异、产物版本和证据。

## 集成基线与工作区

- 集成分支：`codex/kikoto-next-integration`。
- 固定审查基线：`9949175e53979bcd590c75a624bc0efc67684da3`。后续审查始终包含该基线后的全部实施差异，不以最新准备提交缩小范围。
- 本次续做起点：`f0fd5d77be1072f260fc817245c01d1b6334ea0f`，工作区干净；`main` 仍为固定审查基线，`git diff 9949175e53979bcd590c75a624bc0efc67684da3...HEAD` 非空。10/13/18 合入后的功能代码 SHA 为 `2c2e87c4f454aa3a45e6672980972a2a25d09336`，各合并及组合验收见[本批证据](evidence/10-13-18-integration.md)。
- 追加 15 后的功能代码 SHA：`5341b8d623d6c0363f65765f128c8c14b246b645`；合入前已核实原任务终态与候选，当前产物的拆分及来源交叉证据见[15 集成复验](evidence/15-integration.md)。
- 16/19 固定候选顺序合入后的功能代码 SHA：`2180c62dfdbf6659929f3c288ddb1387220130ae`；T16 合并为 `25f449de16fe8e6942f0e5ae179447a2df8cc149`，CI19 合并为 `2180c62dfdbf6659929f3c288ddb1387220130ae`。无冲突，见[16/19 集成复验](evidence/16-19-integration.md)。
- 应用起点及兼容要求沿用规格 D-01；准备检查时集成 HEAD 等于上述基线，工作区无已有修改。
- 主代理实际调用原生 App `create_worktree` 返回 `Not a git repository`。聊天绑定外层工作区，源码为其内的独立仓库，原生工具无法指定该嵌套仓库；已核实源码 Git 根后使用 Git worktree 备用路径，不重复相同失败调用或改变 App 项目。
- 项目内工作区使用已忽略的 `.worktrees/`。首批目录为 `.worktrees/kikoto-t01`、`.worktrees/kikoto-t04`，分支分别为 `codex/kikoto-next-t01`、`codex/kikoto-next-t04`，从准备提交后的集成 HEAD 创建。
- 创建前检查路径、分支和现有 worktree，适合者复用，不强制 reset 或删除。此阶段仅检出；依赖与功能修改由后续实施负责。
- 每次集成保留他人并行修改，限定文件归属。提交遵循当前用户既有的正常 Git 配置并执行隐私扫描，不额外强制签名、不关闭已有签名设置或替换签名器；推送仅使用单次命令代理，不修改全局 Git 配置。

## 工单与依赖

本表只记录执行状态。完整交付、主责与共同 AC、测试入口仍以各票和总览为准，验收证据未取得前保持未勾选。

| 工单 | 直接依赖 | Implementation | 当前说明 | 验证证据 |
| --- | --- | --- | --- | --- |
| [01 管理员翻译配置与 Index 试译](issues/01-translation-admin-config.md) | 无 | resolved | 已集成；配置、试译、权限及出站边界通过 | [01 证据](evidence/01-translation.md)、[集成复验](evidence/01-04-integration.md) |
| [02 浏览内容翻译与有界缓存](issues/02-content-translation-cache.md) | 01 | resolved | 已集成；含 catalog 标题与真实路径目录翻译；缓存使用独立 CacheRoot | [02 证据](evidence/02-content-translation.md)、[集成复验](evidence/02-07-integration.md) |
| [03 简繁转换](issues/03-traditional-chinese-conversion.md) | 02 | resolved | 已集成；简繁缓存、保护名称、原文/重试及 catalog 路径 H/F 复验通过，既有有限 LIVE 证据保留 | [03 证据](evidence/03-traditional-conversion.md)、[集成复验](evidence/03-05-integration.md) |
| [04 编号作品个人评价](issues/04-numbered-work-personal-reviews.md) | 无 | resolved | 已集成；编号评价、隔离、必要登记与恢复通过 | [04 证据](evidence/04-personal-reviews.md)、[集成复验](evidence/01-04-integration.md) |
| [05 多源目录与播放隔离](issues/05-multi-source-directory-playback.md) | 无 | resolved | 已集成；编号/无码逐源刷新、目录故障恢复、内容语言隔离与精确文件播放/游标 H/F 复验通过 | [05 证据](evidence/05-source-playback.md)、[集成复验](evidence/03-05-integration.md) |
| [06 稳定内部引用与编号兼容](issues/06-stable-identity-compatibility.md) | 无 | resolved | 已集成；数字版本、编号/别名、旧库升级与个人记录保留通过，无新增迁移 | [06 证据](evidence/06-stable-references.md)、[集成复验](evidence/06-integration.md) |
| [07 OpenList 有限无码链路](issues/07-openlist-first-playable-slice.md) | 04、06 | resolved | 本地链路、069 升级、catalog 翻译与真实媒体 LIVE 均通过；真实入口因出站身份被拒，声明 Kikoto 身份并可由管理员覆盖后取得两段 Range 证据 | [07 证据](evidence/07-openlist.md)、[集成复验](evidence/02-07-integration.md) |
| [08 完整来源索引与恢复](issues/08-recoverable-full-source-index.md) | 07 | verified-local | 已集成；完整合成树、070 升级/恢复、资源限制、清理与管理进度 H/R/F 通过；真实来源覆盖待补 | [08 证据](evidence/08-source-index.md)、[集成复验](evidence/08-integration.md) |
| [09 每日更新与扫描复用](issues/09-daily-index-update.md) | 08 | verified-local | 每日调度、同源复用、真实重启及资源 H/R 已集成；发布前置保留 | [09 证据](evidence/09-daily-index.md)、[集成复验](evidence/09-integration.md) |
| [10 来源生命周期与记录保留](issues/10-source-lifecycle-retention.md) | 08 | verified-local | 已集成 072、来源状态/改名恢复与个人引用清理保护；07/08 发布前置保留 | [10 证据](evidence/10-source-lifecycle.md)、[集成复验](evidence/10-13-18-integration.md) |
| [11 系列卡片与单集归组](issues/11-series-and-episode-grouping.md) | 07 | verified-local | 已集成 071、系列/单集发现与手工归组；完整发布前置保留 | [11 证据](evidence/11-series.md)、[集成复验](evidence/11-integration.md) |
| [12 系列与单集个人评价](issues/12-series-episode-personal-reviews.md) | 11 | verified-local | 已集成；系列与单集独立评价、跨源关系、隔离和重启 H/R/F 通过；无新迁移 | [12 证据](evidence/12-series-reviews.md)、[集成复验](evidence/12-integration.md) |
| [13 共享关联与本人冲突确认](issues/13-shared-link-conflict-review.md) | 12 | verified-local | 原实施任务提交 9844975 已集成；073、本人冲突确认及精确播放版本保留，完整发布前置保留 | [13 证据](evidence/13-shared-association.md)、[集成复验](evidence/10-13-18-integration.md) |
| [14 高置信唯一候选关联](issues/14-high-confidence-work-matching.md) | 13 | resolved | 已合入集成；自动关联、证据门槛、预算、并发退避、重启恢复及 F/U 全部通过 | [14 证据](evidence/14-work-matching.md) |
| [15 拆分与本人记录归属](issues/15-split-personal-record-allocation.md) | 13 | verified-local | 原实施任务的 41a3412 已追加集成；本人选择、重启、系列/嵌套关系和来源状态交叉验证通过，完整发布前置保留 | [15 候选证据](evidence/15-shared-split.md)、[集成复验](evidence/15-integration.md) |
| [16 个人数据导入导出](issues/16-personal-data-transfer-compatibility.md) | 12 | verified-local | 87e0071 已集成；v2 四类对象、空库/重复导入、精确引用、冲突和未匹配进度保留通过；完整发布前置保留 | [16 证据](evidence/16-personal-transfer.md)、[集成复验](evidence/16-19-integration.md) |
| [17 组合验收与部署回退](issues/17-integrated-acceptance-release-plan.md) | 03、05、09、10、14、15、16、18、19 | in-progress | 组合验收、双轴审查、Full CI、备份回滚演练与生产部署均完成；真实索引收尾与最终交付报告待执行 | [17 部署证据](evidence/17-deployment.md) |
| [18 迁移完成与维护状态一致性](issues/18-ci-migration-completion-state.md) | 无 | verified-local | 已集成 924f807；最终状态、失败及取消恢复已本地验证，Full CI 待执行 | [18 证据](evidence/18-ci-migration.md)、[集成复验](evidence/10-13-18-integration.md) |
| [19 前端 CI 修复](issues/19-ci-browser-reliability.md) | 无 | verified-local | ce5154e 已集成；骨架同步、收藏独立行为、真实移动触摸和旧目录 fixture 契约复验通过，Full/Gate 待执行 | [19 证据](evidence/19-ci-browser.md)、[集成复验](evidence/16-19-integration.md) |

## 开发依赖与发布依赖

用户允许合理调整开发依赖并要求持续实施。07 的代码接口、权限、069 迁移和
catalog 翻译已在集成产物上取得 H/R/F 证据；其剩余缺口是实际来源的程序访问及
媒体 LIVE。08/11 可先使用这些本地接口开发和执行受控测试，不必等待外部诊断结束。
08 的扫描接口已在集成产物上取得 H/R/F 证据，因此 09/10 也可基于该接口串行本地开发。
这项例外仅解除本地开发先决条件，不把 07/08 标为 resolved，也不解除 07、08、09、10、11、17
的完整验收与发布门槛。所有原 AC、真实媒体、完整索引和部署要求保留。

实际外部访问按独立证据记录：既有程序匿名 fs/list 曾返回 HTTP 403/Cloudflare
1010；IAB 匿名首页、子目录及音轨页可访问，站点播放器时间推进。后续对同一已知
文件的 fs/get 诊断在 `08b1469` 上确认：未配置应用代理时 `route_proxies=0`，
连接重置 errno 10054、未收到 HTTP；隔离合成库通过正式 Settings 契约配置已有
代理后 `route_proxies=1`，返回 HTTP 403 HTML、`cf-mitigated: challenge`。
环境 HTTP_PROXY 不会自动成为应用数据库的代理路由。本次没有 1010 证据，也未
取得 raw_url 或开始 Range；生产 fs/get 未重测。不能将新诊断追认为原单次 LIVE
的确切错误，或把程序 API 失败概括为匿名网页不可用。挑战入口探测已停止。
脱敏分层记录和仓库外诊断指针见[07 证据](evidence/07-openlist.md#live-缺口与可复验入口)；
17 重新验收须显式核对正式应用代理配置，原 LIVE 与完整发布门槛保留。

## 迁移协调

现有最高迁移为 075：071 系列、072 来源生命周期、073 共享关联、074 匹配冻结 schema 和 075 个人传输均已集成；开发模式选择最高 `compat/075_dev.sql`。001–075 连续编号、完整链、新装、旧库升级、重复及并发启动、开发基线一致性通过 16/19 集成复验；001–073 及全部发布基线相对 `5aac340` 保持不变，074 与固定依赖 `07fb686f4bbcd8717cfeeedd5d5878346aae6e3f` 一致。09 复用 070 与 app_setting，12 复用 071，15 复用 073，18 复用既有布局迁移状态；这些票没有新 schema。14 功能仍待合入，不能以冻结 schema 代替本票验收；后续新增迁移继续按最高编号协调。

## 验证证据登记

每项证据记录工单、AC、提交或产物版本、实际命令、退出状态、受控或真实覆盖范围、结果与未完成项。公开记录仅保存脱敏摘要；私有路径、端点、密钥、个人记录、真实数据库和原始运行日志不进入 Git。

| 检查 | 当前结果 | 证据要求 |
| --- | --- | --- |
| 准备文档链接、状态字段、17 票依赖与验收保留 | 通过，退出 0 | Node 定向检查复用 `scripts/check-doc-links.mjs`：20 份 Markdown 相对链接、17 票状态/依赖/清单、46 个用户故事、28 项 AC 及非视觉/真实样例边界均通过。 |
| `git diff --check` | 通过，退出 0 | 当前准备差异无空白错误。 |
| `make sensitive-check` | 通过，退出 0 | 提交前扫描 6 个变更文件，无隐私发现。 |
| 正常提交与集成推送 | 已验证 | 准备提交 `d024d66356ef6018127362fdb30cbad9aa87ed9c` 已推送，远端 HEAD 一致。现有配置未启用签名，沿用原有提交方式，未改变签名或全局配置。 |
| 两个 worktree 检出 | 已验证 | 01、04 独立分支从 `d024d66` 创建，工作区干净；开始实施前并入集成分支最新提交。 |
| 既有后端基线 | 2 项通过，退出 0 | Go 1.26.9；真实会话语言偏好及 canonical/alias/edition/legacy 只读解析。代码与固定基线一致，期间差异仅准备文档。 |
| 既有前端基线 | 20 项通过，退出 0 | Node 24.19.0、npm 11.17.0；来源模型、作品导航、播放器持久化共 3 个测试文件。前端代码未改动。 |
| Index 网关就绪 | 单次短文本返回成功 | 指定模型原样请求与响应，HTTP 200、finish_reason=stop、非空 UTF-8 译文；1.738 秒单次观察值。此结果不替代 Kikoto 翻译链路验收。 |
| 服务器准备观察 | 已记录资源，部署待执行 | 2 核、约 3.45 GiB 内存、23.54 GiB 可用磁盘；当前官方应用和 schema 68。部署前须重新检查并验证备份回退。 |
| 各票 H、R、F、U、LIVE | 待逐票执行 | 各票所需真实 HTTP、迁移 SQLite、重启、非视觉功能与有限真实样例。 |
| 01/04 集成复验 | 已通过 | [本批证据](evidence/01-04-integration.md)：组合 H/R 14 个顶层、F 5 项、U 21 项、全后端及前后端构建/静态/文档检查；01 LIVE 沿用未改变客户端的已取得证据。 |
| 06 集成复验 | 已通过 | [本批证据](evidence/06-integration.md)：8 个 H/R 顶层用例及 04 回归通过，包含 067/068 同库重启与只读解析；没有新增迁移、依赖或前端行为。 |
| 02/07 本地集成复验 | 已通过 | [本批证据](evidence/02-07-integration.md)：catalog 翻译红绿，组合 H/R/race、069 升级、存储、前后端检查及少量非视觉 F；07 LIVE 仍未通过。 |
| 03/05 集成复验 | 已通过 | [本批证据](evidence/03-05-integration.md)：39 个 H/R 顶层及 2 个 U/race、867 项前端 U、全后端/静态/构建/文档及少量非视觉 F；不新增迁移，不重复实际 Index 调用，07 LIVE 缺口保留。 |
| 08 本地集成复验 | 已通过 | [本批证据](evidence/08-integration.md)：40 个 H/R、66 个存储、3 个 race 顶层与 3 个非视觉 F，前后端静态/类型/构建/文档检查通过；070 为最高迁移，07/08 仍 verified-local。 |
| OpenList 单来源代理兼容集成 | 已通过本地复验 | [本批证据](evidence/openlist-proxy-integration.md)：合入 `7d75de59e830312fba90dfad219cf15d751a8ee0`，37 个 H/R 顶层、前端类型/构建及文档检查通过；07/08 的 LIVE/真实覆盖缺口保留。 |
| 10/13/18 本地集成复验 | 见本批证据 | [本批证据](evidence/10-13-18-integration.md)：固定三个候选顺序合入，072/073、来源生命周期与共享身份、迁移完成一致性组合验证；不代替剩余 LIVE、Full CI、最终审查和部署。 |
| 生产部署、实际迁移与上线验收 | 已通过 | [17 部署证据](evidence/17-deployment.md)：停服快照、`docker compose` 切换 `kikoto:next-7ed73fa`、schema 68→76、quick_check/外键 0、健康/登录/作品/翻译配置回读通过。 |
| 备份恢复与回退 | 已通过 | 同一部署提交两阶段演练：快照+新镜像迁移至 76，还原快照+旧镜像回 68，健康均 200，`drill_exit=0`。 |

## 17 的整体验收与部署门槛

- [ ] 汇总 AC-01 至 AC-28 的主责与共同证据，核实所有前置票已完整验收；组合检查不替代每票测试。
- [ ] 同一当前构建至少一条真实前后端链路贯通编号/无码浏览、翻译、目录、评价及必要共享操作；外部边界桩不替代自有 API 联通。
- [ ] 播放时并行浏览、扫描、翻译，触发限额和低空间；音轨、队列保持，进度继续，暂停与失败可恢复。记录速率、队列、缓存、内存、磁盘并校准预算。
- [ ] 完成有限真实 Index 生成、繁体文本、匿名媒体 Range/定位读取及指定环境播放；列明规模、格式、浏览器和实际索引分区覆盖及失败范围。
- [ ] 验证迁移、任务恢复、待确认操作、来源恢复、个人数据传输，以及测试库备份恢复和回退前提。
- [ ] 仅执行已选定的少量浏览器纯功能用例，关闭截图采集，不增加布局、像素或人工视觉签收门槛。
- [ ] 形成对应同一产物的部署、升级、备份恢复与回退步骤，按授权执行生产部署和实际迁移，再完成上线后非视觉功能验收。
- [ ] 核对部署产物、数据兼容、运行状态和用户可见行为；必要时按已验证方案回滚并记录结果。部署与回滚结论仅使用实际执行证据。
