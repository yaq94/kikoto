# Kikoto 下一阶段执行记录

**Status:** ready-for-agent
**Implementation:** in-progress；01–19 的历史证据按对应产物保留；20–24 为当前整改任务，最终候选、真实覆盖与部署证据未闭环。

## 当前状态

**当前部署：待本轮重新核实。** 历史部署 `2bae5db9`、镜像 `kikoto:next-2bae5db9`、schema 83 与 Full CI run `39496305624` 只作历史记录；集成分支 `codex/kikoto-catalog-remediation` 的 `bc880d91` 尚未部署。生产 schema、镜像、备份和回滚前提必须写入 24 的候选账本后才能作为发布证据。

**集成状态（2026-10-26 复核）：** 集成分支 `codex/kikoto-catalog-remediation` 已推进到 `bc880d91`，本 worktree 合并该快照。迁移头 `102`：`101_work_parent_backfill_report.sql` 增加父级回填报告表并幂等重跑回填，`102_translation_prefill_status.sql` 为 prefill 增加 `failed` 状态并给失败表补 `(config_key, source_hash)` 索引；`compat/102_dev.sql` 已生成。20 的 098 状态机、系列准入与 publication 事务内删 mapping 已合入，仍有准入入口、零可见成员 404、due 推进顺序、诊断与性能残余；21 的 099/102、严格语言搜索与共享短查询生成器已合入，远程短查询已带 D-16 准入，仍有短查询 keyset `after` 续扫、D-16 join 的真实规模证据、`failed` 终态、真实规模基准与真实链复验；22 的 100 与 `work.parent_work_id` 已合入，导出格式已裁决回到 v2 + 可选 `parent`，代码与用户页由 22 子代理收口；23 只剩文档收口；24 未开始。

**审查快照：** 审查时文档基线 `6122b7bf`；代码审查基线 `301e6c63`。`6b4a02ec`（096 与系列暂存）、`728651a6`（v2 可选 `parent`）、`47df9c11`（wall-clock/partial 调度）均已并入当前分支，但仍需在当前候选上重新验收；不要把它们当作已签收的外部交付。历史 run 53 为 partial，不能当作完整覆盖。2026-10-25 的逐项审查见 [audit-20261025-review.md](audit-20261025-review.md)。

**当前候选：** 尚未冻结。01–19 的 `resolved` 仅表示其记录 SHA 上的历史本地证据，不等于当前候选已验收。24 必须对同一 SHA 重跑主责 AC、整改子项、Full CI、迁移、LIVE 和部署。

`final-spec-review-20261024.md` 曾在 `a711bc39` 记录 AC-14 的系列发现失败（单集/格式副本/Bonus、成员上限）和 AC-26 的部署证据缺口；后续未提交工作区改动不能替代当前 SHA 的回归，24 必须把 AC-14 列入首要门禁。

## 当前整改依赖与责任

| 工单 | 责任 | Implementation | 完成所需证据 |
| --- | --- | --- | --- |
| [20 持续目录增量、固定调度与准入回归](issues/20-catalog-sync-and-admission.md) | partial 变化发现、固定调度、D-16 跨层边界 | in-progress | H/R、失败重试与恢复已有本地证据；仍缺单卡评价／播放／媒体流／目录标签准入、零可见成员 404、零文件与旧 generation 集合统一、目录优先 sync 的 due 推进顺序、诊断 `Probe*` 过滤、真实 plan 断言、42k p95 |
| [21 有效译文语料与严格语言搜索](issues/21-translation-corpus-and-search.md) | 有效语料、覆盖、严格搜索、短查询 | in-progress | 099/102、严格语言搜索与共享短查询生成器已合入，远程短查询已带 D-16 准入；仍缺短查询 keyset `after` 续扫、D-16 join 的目标规模证据、`failed` 终态写入、真实 456k 语料基准、编号/标题等值与短查询 p95 证据、真实迁移链重跑 |
| [22 个人备份父级归属](issues/22-personal-transfer-parent-identity.md) | 空库单集与父系列恢复 | in-progress | 100 与 `parent_work_id` 已合入；格式已裁决为 v2 + 可选 `parent`，代码、用户页与多父级/权限测试待 22 子代理收口 |
| [23 保留与仓库规范](issues/23-retention-and-repository-contracts.md) | 清理保护、迁移链、模型文档、证据脱敏 | in-progress | 合成 dry-run、保留回归、095–102、文档与敏感检查；当前候选复验未完成 |
| [24 同候选集成与发布证据](issues/24-release-candidate-evidence.md) | 全部修复、双轴审查、部署验收 | pending | 候选账本模板、同一 SHA 的 Full CI、迁移恢复、组合负载、LIVE、生产终态 |

17 不依赖 20–24；24 依赖 17–23。以上新票不覆盖旧 evidence 文件，原 01–19 的需求与主责继续有效。任何 `resolved` 都必须满足当前产物定义。

## 历史执行记录

以下记录只证明注明的历史产物，不能关闭当前 17/20–24：

- 01–07、09–16 的历史本地 H/F/R 证据保留在各自的 evidence 文件；其中 07 的真实媒体 Range 是旧独立实例证据，当前应用代理路径仍需 24 复验。
- 08 的完整合成树、恢复与资源限制已通过；真实 run 53 仍为 partial（约 1.4 万目录中绝大多数完成、4 失败、27 未确认空），真实全量覆盖待 24。
- 17 的旧部署 `7ed73fa0`（schema 68→76）与 `2bae5db9`（schema 83）只作历史；当前构建未部署。
- 18、19 的本地 H/R/F 证据保留；最终 Full CI 终态由 24 在同一 SHA 取得。
- 2026-10-24 前完成的目录身份、sidecar、翻译、关联与导入修复见 [20 证据](evidence/20-post-review-fixes.md)；旧 prune dry-run 只作历史，生产计数不再复述。

## 迁移协调

- 当前已跟踪最高迁移为 `102_translation_prefill_status.sql`（`0c945b3e`），`101_work_parent_backfill_report.sql` 与 `102_translation_prefill_status.sql` 已提交，`compat/102_dev.sql` 已生成；下一个编号为 103。
- 096 已被系列暂存占用，不得把其他 DDL 塞入 096。
- 编号：097 为固定的 095 表重建槽位；098 为目录调度；099 为译文语料契约；100 为 work 父级；101 为父级回填报告与幂等回填；102 为 prefill `failed` 状态与失败表 `(config_key, source_hash)` 索引。任何编号变化必须同步迁移头、compat 文件和测试常量。
- 095 曾在 `25f96c22` 首次加入（无外键），后在 `5defaa1c` 改为带 `ON DELETE CASCADE`；两个文件 SHA-256 不同。已执行旧 095 的数据库会触发 checksum 门禁，必须在 097 前提供兼容路径。是否存在旧 095 数据库仍待核实。
- 新装、旧库升级、重复启动、并发启动和 compat 基线必须由 24 在同一 SHA 复验。

## 验证证据登记

下表只记录当前可复用的检查；历史证据不代表当前候选。

| 检查 | 当前结果 | 说明 |
| --- | --- | --- |
| 文档链接 | 通过 | `node scripts/check-doc-links.mjs`：134 份公开 Markdown，相对链接全部可解析；`.scratch` 链接见同一脚本 |
| 文档 locale | 通过 | `node scripts/check-doc-locales.mjs`：5 个 locale、11 个用户页 |
| 空白检查 | 通过 | `git diff --check` 无输出 |
| 敏感检查 | 变更范围通过 | `make sensitive-check` 覆盖变更行；已跟踪证据另由 `make tracked-evidence-check` 全文扫描 |
| tracked-evidence 扫描 | 通过 | 51 个已跟踪证据文件；规则见 [23 证据策略](evidence/23-evidence-policy.md) |
| 01–16 历史本地证据 | 保留 | 只证明各自旧 SHA；当前候选由 24 重跑 |
| 08 真实覆盖 | partial | run 53 未完成，失败/未确认空目录不计完整 |
| 17 旧部署 | 历史 | `2bae5db9`/schema 83，不是当前候选 |
| 20–24 当前候选 | 未开始/未完成 | 需要各自证据和 24 汇总 |

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

| Implementation | 含义 |
| --- | --- |
| pending | 尚未认领，或等待前置票完成。 |
| in-progress | 已认领并准备或实施；不代表任何功能通过。 |
| verified-local | 当前产物已取得所列本地证据，仍有本票其他验收、真实样例或部署证据待补。 |
| resolved | 本票在其记录 SHA 上的全部主责与共同验收均有完整证据；24 仍需把这个记录纳入当前候选。 |

文档、基础设施准备、单元测试、健康检查或代理报告都不能代替完整验收；由集成负责核对实际差异、产物版本和证据。
