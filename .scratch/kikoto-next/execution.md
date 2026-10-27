# Kikoto 下一阶段执行记录

**Status:** ready-for-agent
**Implementation:** resolved；01–19 的历史证据按对应产物保留；20–24 的整改已汇总到候选 `f727feee`，双轴审查、门禁、部署与部署后验收已完成。

## 当前状态

**当前部署：** 生产运行 `kikoto:next-f727feee`（schema 105，`/health` 200 `v0.8.3`）；部署前为同 schema 的 `kikoto:next-2ad508e8`。部署脚本在切换前完成一致性快照，健康门禁 2 秒通过，未触发回滚。部署版本、备份与回滚路径见 [24 候选账本](evidence/24-candidate-f727feee.md)。
**集成状态（2026-10-27 复核）：** 集成分支 `codex/kikoto-catalog-remediation` 已推进到 `f727feee`。迁移头 `105`：`101_work_parent_backfill_report.sql` 增加父级回填报告表并幂等重跑回填，`102_translation_prefill_status.sql` 为 prefill 增加 `failed` 状态并给失败表补 `(config_key, source_hash)` 索引，`103_translation_short_scan_index.sql` 用 `idx_content_translation_short_scan` 替换 099 的两个短查询索引，`104_source_catalog_entry_admission.sql` 为每卡准入计数建表并在文件写入事务内失效、发布事务内刷新，`105_translation_search_scope.sql` 把 `source_id`／`browsable` 物化到译文行并建立 scope 视图与触发器；`compat/105_dev.sql` 已生成。`migration_chain_105_test.go` 覆盖新装到 105、68→105、84→105、重复启动幂等与 `foreign_key_check` 0 行。20 的 098 状态机、系列准入、publication 事务内删 mapping、其余准入入口、零可见成员 404、零文件与旧 generation 集合统一、due 推进顺序、诊断过滤与 plan 断言全部并入并通过定向测试；目标机 42k 卡浏览与 5,001 文件卡拒绝路径的 p95 达标（数值见 20 号票与 24 账本）。21 的 099/102/105、严格语言搜索、共享短查询生成器与 `failed` 终态已合入；1–2 字查询的返回条数与 `total` 共用 1000 张预算并以 `truncated` 标记，目标机卡片页与同预算总数 p95 均 ≤13 ms、零命中 48 ms。22 的 100、`work.parent_work_id`、v2 + 可选 `parent`、`parent_alias_not_imported`、多父级导出改为可审阅 `parent_conflict` 均已合入并通过测试。23 的文档与保留回归已收口。候选 `f727feee` 已冻结并部署：Full CI run `40409566661` success（26/26 job），生产运行 `kikoto:next-f727feee` / schema 105，`/health` 200 `v0.8.3`。本候选新增来源 verdict 与可播放性分离、可见 verdict 标签、`finishedAt`、试译错误分类、prefill 快照缓存、短查询有界 total 与 `truncated`、受保护名称提示。部署后实测：8 并发 prefill 全部 4 ms 内且 `/api/works` 保持 10 ms；组合负载无 5xx；短查询 1000 命中返回 `total=1000`/`truncated=true`；原 3 条受保护名称失败条目全部翻译成功。迁移链四项（新装、068→105、084→105、重复启动）在本候选通过。

**审查快照：** 审查时文档基线 `6122b7bf`；代码审查基线 `301e6c63`。`6b4a02ec`（096 与系列暂存）、`728651a6`（v2 可选 `parent`）、`47df9c11`（wall-clock/partial 调度）均已并入当前分支，但仍需在当前候选上重新验收；不要把它们当作已签收的外部交付。历史 run 53 为 partial，不能当作完整覆盖。2026-10-25 的逐项审查见 [audit-20261025-review.md](audit-20261025-review.md)。

**当前候选：** 已冻结并部署 `f727feee`（见 [24 候选账本](evidence/24-candidate-f727feee.md)）。01–19 的 `resolved` 仅表示其记录 SHA 上的历史本地证据；本候选在门禁中重跑了后端全量、前端全量、迁移链与部署后真实服务验收，逐项结论见账本。

`final-spec-review-20261024.md` 曾在 `a711bc39` 记录 AC-14 的系列发现失败（单集/格式副本/Bonus、成员上限）和 AC-26 的部署证据缺口；后续未提交工作区改动不能替代当前 SHA 的回归，24 必须把 AC-14 列入首要门禁。

## 当前整改依赖与责任

| 工单                                                                                                                                    | 责任                                      | Implementation | 完成所需证据                                                                                                                                                                                                                        |
| --------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------- | -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [20 持续目录增量、固定调度与准入回归](issues/20-catalog-sync-and-admission.md)                                                          | partial 变化发现、固定调度、D-16 跨层边界 | verified-local | H/R、失败重试与恢复、单卡评价／播放／媒体流／目录标签准入、零可见成员 404、零文件与旧 generation 集合统一、due 推进顺序、诊断过滤与 plan 断言均已在集成分支通过定向测试；目标机 42k 卡浏览与 5,001 文件卡拒绝路径的 p95 达标（数值见 20 号票与 24 账本） |
| [21 有效译文语料与严格语言搜索](issues/21-translation-corpus-and-search.md)                                                             | 有效语料、覆盖、严格搜索、短查询          | verified-local | 099/102/105、严格语言搜索、共享短查询生成器与 `failed` 终态已合入；1–2 字查询的返回条数与 `total` 共用 1000 张预算并以 `truncated` 标记，目标机卡片页与同预算总数 p95 均 ≤13 ms；最终候选复验由 24 记录 |
| [22 个人备份父级归属](issues/22-personal-transfer-parent-identity.md)                                                                   | 空库单集与父系列恢复                      | resolved（`f727feee`） | 100 与 `parent_work_id`、v2 + 可选 `parent`、`parent_alias_not_imported`、多父级导出改为可审阅 `parent_conflict`；9 项验收全部勾选并有测试名；权限 403 与往返身份均有 H/R 证据                                                     |
| [23 保留与仓库规范](issues/23-retention-and-repository-contracts.md)                                                                    | 清理保护、迁移链、模型文档、证据脱敏      | verified-local | 6 项清理回归、合成 dry-run、095 历史与 096–105 连续链、旧 095 preflight 三路径、文档与 tracked-evidence 扫描全部通过                                                                                                                |
| [24 同候选集成与发布证据](issues/24-release-candidate-evidence.md)                                                                      | 全部修复、双轴审查、部署验收              | resolved       | 候选 `f727feee`：Full CI run `40409566661` success（26/26）；迁移链四项、后端与前端全量、组合负载、部署后 LIVE 翻译与媒体 Range 均通过；证据见 [24 候选账本](evidence/24-candidate-f727feee.md) |
| 17 不依赖 20–24；24 依赖 17–23。以上新票不覆盖旧 evidence 文件，原 01–19 的需求与主责继续有效。任何 `resolved` 都必须满足当前产物定义。 |

## 历史执行记录

以下记录只证明注明的历史产物，不能关闭当前 17/20–24：

- 01–07、09–16 的历史本地 H/F/R 证据保留在各自的 evidence 文件；其中 07 的真实媒体 Range 是旧独立实例证据，当前应用代理路径仍需 24 复验。
- 08 的完整合成树、恢复与资源限制已通过；真实 run 53 仍为 partial（约 1.4 万目录中绝大多数完成、4 失败、27 未确认空），真实全量覆盖待 24。
- 17 的旧部署 `7ed73fa0`（schema 68→76）与 `2bae5db9`（schema 83）只作历史；当前候选 `f727feee` 已部署，见当前状态。
- 18、19 的本地 H/R/F 证据保留；最终 Full CI 终态已由 24 在 `f727feee` 取得（run `40409566661`，success）。
- 2026-10-24 前完成的目录身份、sidecar、翻译、关联与导入修复见 [20 证据](evidence/20-post-review-fixes.md)；旧 prune dry-run 只作历史，生产计数不再复述。

## 迁移协调

- 当前已跟踪最高迁移为 `105_translation_search_scope.sql`（`0ec8cda0`），`101_work_parent_backfill_report.sql`、`102_translation_prefill_status.sql`、`103_translation_short_scan_index.sql`、`104_source_catalog_entry_admission.sql` 与 `105_translation_search_scope.sql` 已提交，`compat/105_dev.sql` 已生成；下一个可用编号为 106。
- 096 已被系列暂存占用，不得把其他 DDL 塞入 096。
- 编号：097 为固定的 095 表重建槽位；098 为目录调度；099 为译文语料契约；100 为 work 父级；101 为父级回填报告；102 为 prefill `failed` 状态与失败表 `(config_key, source_hash)` 索引；103 为短查询扫描索引；104 为每卡准入计数行；105 为译文短查询的 `browsable` 物化与 scope 触发器。任何编号变化必须同步迁移头、compat 文件和测试常量。
- 095 曾在 `25f96c22` 首次加入（无外键），后在 `5defaa1c` 改为带 `ON DELETE CASCADE`；两个文件 SHA-256 不同。已执行旧 095 的数据库会触发 checksum 门禁，必须在 097 前提供兼容路径。是否存在旧 095 数据库仍待核实。
- 84→105 升级演练已完成；新装、重复启动、并发启动和 compat 基线由 24 在同一 SHA 复验。

## 验证证据登记

下表只记录当前可复用的检查；历史证据不代表当前候选。

| 检查                  | 当前结果     | 说明                                                                                                                                    |
| --------------------- | ------------ | --------------------------------------------------------------------------------------------------------------------------------------- |
| 文档链接              | 通过         | `node scripts/check-doc-links.mjs`：134 份公开 Markdown，相对链接全部可解析；`.scratch` 链接见同一脚本                                  |
| 文档 locale           | 通过         | `node scripts/check-doc-locales.mjs`：5 个 locale、11 个用户页                                                                          |
| 空白检查              | 通过         | `git diff --check` 无输出                                                                                                               |
| 敏感检查              | 变更范围通过 | `make sensitive-check` 覆盖变更行；已跟踪证据另由 `make tracked-evidence-check` 全文扫描                                                |
| tracked-evidence 扫描 | 通过         | 51 个已跟踪证据文件；规则见 [23 证据策略](evidence/23-evidence-policy.md)                                                               |
| 01–16 历史本地证据    | 保留         | 只证明各自旧 SHA；当前候选由 24 重跑                                                                                                    |
| 08 真实覆盖           | partial      | run 53 未完成，失败/未确认空目录不计完整                                                                                                |
| 17 旧部署             | 历史         | `2bae5db9`/schema 83，不是当前候选                                                                                                      |
| Full CI               | 通过         | run `40409566661` @ `f727feee`，success（26/26 job）                                                                                    |
| 迁移演练              | 通过         | 84→105：21 个迁移、总耗时 20,840 ms、`quick_check` ok、FK 0                                                                              |
| 回滚演练              | 通过         | 84 快照 + 旧镜像 `/health` 200；105 库 + 旧镜像启动失败（schema newer than binary）                                                      |
| 生产部署              | 通过         | `kikoto:next-f727feee`，schema 105，`/health` 200 `v0.8.3`；部署前同 schema 的上一候选，健康门禁 2 秒，未回滚                       |
| 20–23 当前候选        | 通过         | 20/21/22/23 的本地、目标机与部署后真实服务证据均已复验；22 记录 SHA `f727feee`                                                          |

## 17 的整体验收与部署门槛

- [ ] 汇总 AC-01 至 AC-28 的原始主责、20–23 的整改子项和 24 的发布证据；组合检查不替代每票测试。
- [ ] 同一当前构建至少一条真实前后端链路贯通编号/无码浏览、翻译、目录、评价及必要共享操作；外部边界桩不替代自有 API 联通。
- [ ] 播放时并行浏览、扫描、翻译，触发限额和低空间；音轨、队列保持，进度继续，暂停与失败可恢复。
- [ ] 完成有限真实 Index 生成、繁体文本、匿名媒体 Range/定位读取及指定环境播放；列明规模、格式、浏览器和实际索引覆盖及失败范围。
- [ ] 验证迁移、任务恢复、待确认操作、来源恢复、个人数据传输，以及测试库备份恢复和回退前提。
- [ ] 仅执行已选定的少量浏览器纯功能用例，关闭截图采集，不增加布局、像素或人工视觉签收门槛。
- [ ] 形成对应同一产物的部署、升级、备份恢复与回退步骤，按授权执行生产部署和实际迁移，再完成上线后非视觉功能验收。
- [ ] 核对部署产物、数据兼容、运行状态和用户可见行为；必要时按已验证方案回滚并记录结果。

## 当前授权与完成条件

用户已授权全部 24 票的功能实现、必要依赖安装、测试、数据库迁移、部署与回滚。功能要求、权限、个人记录保护和非视觉测试边界保持有效；未完成需求不得删去或改列范围外。

分诊 `Status` 保持 `ready-for-agent`。独立 `Implementation` 字段：

| Implementation | 含义                                                                                 |
| -------------- | ------------------------------------------------------------------------------------ |
| pending        | 尚未认领，或等待前置票完成。                                                         |
| in-progress    | 已认领并准备或实施；不代表任何功能通过。                                             |
| verified-local | 当前产物已取得所列本地证据，仍有本票其他验收、真实样例或部署证据待补。               |
| resolved       | 本票在其记录 SHA 上的全部主责与共同验收均有完整证据；24 仍需把这个记录纳入当前候选。 |

文档、基础设施准备、单元测试、健康检查或代理报告都不能代替完整验收；由集成负责核对实际差异、产物版本和证据。

## Comments

2026-10-27：候选 `f727feee` 已冻结并部署；Full CI run `40409566661` 成功（26/26），迁移链四项、组合负载、LIVE 翻译与媒体 Range、部署后逐项复验全部完成。24 记为 `resolved`，证据见 [24 候选账本](evidence/24-candidate-f727feee.md)。
