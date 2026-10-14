# Kikoto 下一阶段执行记录

**Status:** ready-for-agent
**Implementation:** in-progress
**阶段:** 首批工单 01、04 实施；其余工单按依赖推进，实际迁移、部署与回滚验收待执行。
**依据:** [正式规格](spec.md) · [工单总览](ticket-proposal.md) · [领域词汇表](../../GLOSSARY.md) · [作品身份与个人记录 ADR](../../docs/adr/0001-work-identity-and-personal-state.md)

## 当前授权与完成条件

用户已授权全部 17 票的功能实现、必要依赖安装、测试、数据库迁移、部署与回滚。当前执行授权适用于全部工单；工单中的发布说明只描述发布行为，不限制本阶段已经确认的授权。功能要求、各票验收清单、权限、个人记录保护和非视觉测试边界保持有效，未完成需求不得删去或改列范围外。

分诊 `Status` 保持 `ready-for-agent`。独立 `Implementation` 字段使用以下值：

| Implementation | 含义 |
| --- | --- |
| pending | 尚未认领，或等待前置票完成。 |
| in-progress | 已认领并准备或实施；不代表任何功能通过。 |
| verified-local | 当前产物已取得所列本地证据，仍有本票其他验收、真实样例或部署证据待补。 |
| resolved | 本票全部主责与共同验收均有对应当前产物的完整证据；17 还须完成部署验收。 |

每票自行完成入口、持久化、权限、页面操作与必要恢复验证，再解除依赖。文档、基础设施准备、单元测试、健康检查或代理报告都不能代替完整验收；由集成负责核对实际差异、产物版本和证据。

## 集成基线与工作区

- 集成分支：`codex/kikoto-next-integration`。
- 固定审查基线：`9949175e53979bcd590c75a624bc0efc67684da3`。后续审查始终包含该基线后的全部实施差异，不以最新准备提交缩小范围。
- 应用起点及兼容要求沿用规格 D-01；准备检查时集成 HEAD 等于上述基线，工作区无已有修改。
- 主代理实际调用原生 App `create_worktree` 返回 `Not a git repository`。聊天绑定外层工作区，源码为其内的独立仓库，原生工具无法指定该嵌套仓库；已核实源码 Git 根后使用 Git worktree 备用路径，不重复相同失败调用或改变 App 项目。
- 项目内工作区使用已忽略的 `.worktrees/`。首批目录为 `.worktrees/kikoto-t01`、`.worktrees/kikoto-t04`，分支分别为 `codex/kikoto-next-t01`、`codex/kikoto-next-t04`，从准备提交后的集成 HEAD 创建。
- 创建前检查路径、分支和现有 worktree，适合者复用，不强制 reset 或删除。此阶段仅检出；依赖与功能修改由后续实施负责。
- 每次集成保留他人并行修改，限定文件归属。提交遵循当前用户既有的正常 Git 配置并执行隐私扫描，不额外强制签名、不关闭已有签名设置或替换签名器；推送仅使用单次命令代理，不修改全局 Git 配置。

## 工单与依赖

本表只记录执行状态。完整交付、主责与共同 AC、测试入口仍以各票和总览为准，验收证据未取得前保持未勾选。

| 工单 | 直接依赖 | Implementation | 当前说明 | 验证证据 |
| --- | --- | --- | --- | --- |
| [01 管理员翻译配置与 Index 试译](issues/01-translation-admin-config.md) | 无 | in-progress | 仅认领准备 | 待执行 |
| [02 浏览内容翻译与有界缓存](issues/02-content-translation-cache.md) | 01 | pending | 等待前置验收 | 待执行 |
| [03 简繁转换](issues/03-traditional-chinese-conversion.md) | 02 | pending | 等待前置验收 | 待执行 |
| [04 编号作品个人评价](issues/04-numbered-work-personal-reviews.md) | 无 | in-progress | 仅认领准备 | 待执行 |
| [05 多源目录与播放隔离](issues/05-multi-source-directory-playback.md) | 无 | pending | 可独立启动 | 待执行 |
| [06 稳定内部引用与编号兼容](issues/06-stable-identity-compatibility.md) | 无 | pending | 可独立启动 | 待执行 |
| [07 OpenList 有限无码链路](issues/07-openlist-first-playable-slice.md) | 04、06 | pending | 等待前置验收 | 待执行 |
| [08 完整来源索引与恢复](issues/08-recoverable-full-source-index.md) | 07 | pending | 等待前置验收 | 待执行 |
| [09 每日更新与扫描复用](issues/09-daily-index-update.md) | 08 | pending | 等待前置验收 | 待执行 |
| [10 来源生命周期与记录保留](issues/10-source-lifecycle-retention.md) | 08 | pending | 等待前置验收 | 待执行 |
| [11 系列卡片与单集归组](issues/11-series-and-episode-grouping.md) | 07 | pending | 等待前置验收 | 待执行 |
| [12 系列与单集个人评价](issues/12-series-episode-personal-reviews.md) | 11 | pending | 等待前置验收 | 待执行 |
| [13 共享关联与本人冲突确认](issues/13-shared-link-conflict-review.md) | 12 | pending | 等待前置验收 | 待执行 |
| [14 高置信唯一候选关联](issues/14-high-confidence-work-matching.md) | 13 | pending | 等待前置验收 | 待执行 |
| [15 拆分与本人记录归属](issues/15-split-personal-record-allocation.md) | 13 | pending | 等待前置验收 | 待执行 |
| [16 个人数据导入导出](issues/16-personal-data-transfer-compatibility.md) | 12 | pending | 等待前置验收 | 待执行 |
| [17 组合验收与部署回退](issues/17-integrated-acceptance-release-plan.md) | 03、05、09、10、14、15、16 | pending | 完整 AC 验证及部署验收待执行 | 待执行 |

## 迁移协调

主代理研究确认现有最高迁移为 068。01 复用 `app_setting`，04 复用既有 rating/note，不预设新增迁移。下一编号 069 暂由集成协调保留给 06 的稳定身份扩展；这是编号分配，不是 SQL 或迁移实现。后续新增编号由集成统一分配，实施前重新核实迁移目录，保持已发布迁移不可变，按仓库规则生成开发基线并验证旧库升级与重复启动。

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
| 生产部署、实际迁移与上线验收 | 待执行 | 同一已验收产物、备份、迁移、重启与功能回读证据。 |
| 备份恢复与回退 | 待执行 | 恢复演练、适用前提及必要回滚的执行结果。 |

## 17 的整体验收与部署门槛

- [ ] 汇总 AC-01 至 AC-28 的主责与共同证据，核实所有前置票已完整验收；组合检查不替代每票测试。
- [ ] 同一当前构建至少一条真实前后端链路贯通编号/无码浏览、翻译、目录、评价及必要共享操作；外部边界桩不替代自有 API 联通。
- [ ] 播放时并行浏览、扫描、翻译，触发限额和低空间；音轨、队列保持，进度继续，暂停与失败可恢复。记录速率、队列、缓存、内存、磁盘并校准预算。
- [ ] 完成有限真实 Index 生成、繁体文本、匿名媒体 Range/定位读取及指定环境播放；列明规模、格式、浏览器和实际索引分区覆盖及失败范围。
- [ ] 验证迁移、任务恢复、待确认操作、来源恢复、个人数据传输，以及测试库备份恢复和回退前提。
- [ ] 仅执行已选定的少量浏览器纯功能用例，关闭截图采集，不增加布局、像素或人工视觉签收门槛。
- [ ] 形成对应同一产物的部署、升级、备份恢复与回退步骤，按授权执行生产部署和实际迁移，再完成上线后非视觉功能验收。
- [ ] 核对部署产物、数据兼容、运行状态和用户可见行为；必要时按已验证方案回滚并记录结果。部署与回滚结论仅使用实际执行证据。
