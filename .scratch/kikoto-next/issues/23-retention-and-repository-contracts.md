# 23：个人引用保留、迁移兼容与仓库规范

**What to build:** 统一清理计划与个人稳定引用保留规则；使数据模型、回滚说明、测试夹具、迁移链和公开证据符合仓库边界。本票只负责文档、回归和迁移兼容，不重写 21 的译文保留行为。

**Blocked by:** [10：来源生命周期与记录保留](10-source-lifecycle-retention.md)；[21：有效译文语料、覆盖状态与严格语言搜索](21-translation-corpus-and-search.md)。ADR-0006 已接受，是本票的规则依据，不是可 `resolved` 的工单依赖。

**Status:** ready-for-agent
**Implementation:** in-progress；现有 `catalogprune` 已有 source-only alias 保护。`095` 与 `097` 已提交，`096`（`6b4a02ec`）、`098`（`531acdf4`）、`099`（`f3dc3a41`）、`100`（`64b25252`）、`101`/`102`（`0c945b3e`）也已提交，`compat/102_dev.sql` 已生成。剩余是公开证据、数据模型、回滚说明的收口，以及当前候选上的复验。

**依据:** [正式规格](../spec.md)、[AGENTS.md](../../../AGENTS.md)、[ADR-0006](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)、[ADR-0007](../../../docs/decisions/ADR-0007-source-field-translation-cache.md)。

**涉及文件:** `backend/cmd/kikoto/main.go`、`backend/internal/storage/migration.go`（或新增早期 preflight 文件）、备份实现、只读校验 `backend/migrations/097_*.sql`、`backend/migrations/compat/096_dev.sql`、`docs/development/migrations.md`、`docs/development/release-rollback.md`、`docs/architecture/data-model.md`、指定测试夹具、证据文件。

## 验收归属

本票负责 AC-23 的清理保留回归子项、AC-26 的文档与证据规范子项。AC-23 的原始主责仍是 10；AC-26 的原始主责仍是 17。23 不替代 10 的来源生命周期行为，也不替代 17 的部署验收。

## 清理与保留

1. 验证现行 `catalogprune`：带 `personal_transfer_ref` 的 source-only 卡片跳过；work-backed alias 可重指向 work；stale dry-run apply 必须在删除前拒绝。
2. 旧 D-17 的“删除 transfer ref”只作为历史记录；当前规则以 ADR-0006 的 source-only alias 保留为准。旧 dry-run 不能作为 apply 依据。
3. 覆盖两类 alias：work-backed（可重指向 work）与 source-only（无 work，必须保留）；有个人评价、播放进度或标签但无 `personal_transfer_ref` 的卡片不得被误删。
4. 重新生成合成 dry-run；报告只含合成 fixture、计数和原因，不含生产 entryId／sourceId／path。
5. 旧 643/642 dry-run 只作历史，不可 apply；报告需带批次指纹和 stale 检查。

## 迁移 095/096/097–100

- 095 冻结：`25f96c22` 首次加入（无外键，canonical checksum `70933dd47790fe1f15e49818fe7a5fc394e3f87258c1001a50a3b75d5a370efe`），`5defaa1c` 改为 `ON DELETE CASCADE`（canonical checksum `5ff08c11d8012cced81bc0887fdd87d857d94b0a57b6d185f992cf573f7b07e4`，与当前文件一致）；当前文件不再改写。
- 096 已由 `6b4a02ec` 提交：`096_catalog_episode_pending.sql`、`compat/096_dev.sql` 和系列暂存实现均已在主分支；不得把其他变更塞进 096。
- 097 作为固定的 095 修复槽位（始终存在，统一执行重建）：
  - 097 始终存在，不能取消或跳号。097 不再按“是否有旧 095”分叉：无论旧表还是当前表，都执行同一套重建 `source_field_translation`（新表带外键、复制合法 `source_id` 行、丢弃孤儿缓存、重建索引），用统一路径保持 `095 → 096 → 097 → 098 → 099 → 100 → 101 → 102` 连续，并让新装、当前 095 和旧 095 三种数据库都实际覆盖重建代码。
  - 若存在旧 095，其 ledger checksum 与当前 095 不同，`migration.go` 会在到达 097 前拒绝启动。097 只负责结构，不能修复这个门禁；必须在 `prepareMigrationState`／`validateAndAdoptHistory` 之前、`MigrateFSWithOptions` 之前提供一次性 preflight，不能挂在现有 `BeforeUpgrade` 里（它已经在 checksum 校验之后）。preflight 自身先创建数据库备份，再只对精确匹配旧 095 表结构指纹的数据库把该 ledger 行的 checksum 规范化为当前 095 checksum；不匹配、部分匹配或存在额外对象时拒绝启动。
  - 禁止增加通用的“忽略 checksum”开关。preflight 必须记录旧 checksum、新 checksum、旧 schema 指纹、执行时间、数据库备份路径和结果；同时提供离线等价命令，供无法启动的实例使用。097 只负责结构重建，不负责绕过门禁。
- 097 已由 `ec624bd3` 提交（含 `compat/097_dev.sql`）。097 之后连续分配：098 目录调度、099 译文语料契约、100 work 父级、101 父级回填报告、102 prefill `failed` 状态；任何编号变化必须同步迁移头、对应 `compat/0NN_dev.sql` 和测试常量。
- 不得修改已发布迁移来“修复”旧环境。

## 文档与证据

- `docs/architecture/data-model.md` 的 Translation Corpus 必须与 21 的最终实现一致：当前 config、有效实体、严格语言、短查询、软预算、title／description 行不可淘汰；删除历史叙述。21 落地前，该节必须明确标注为目标合同，不能把尚未实现的保留和短查询行为写成当前事实。
- `docs/development/translation.md` 由 21 提供最终文本；23 只做交叉引用和仓库格式检查。
- `docs/development/release-rollback.md`：有 healthcheck 的 Compose 显示 `healthy`，生产无 healthcheck 只要求 `running`，以 `/health` 200 为门禁。
- `docs/development/migrations.md` 同步 095–102 的连续链和 compat 基线。
- 测试编号改用 `testfixture.WorkCodeAt`；超过 400 条使用 `HighCardinalityWorkCodeAt`。
- 公开证据去除 `/srv/...`、数据库路径、真实作品编号、生产 entryId/sourceId 和原始日志。
- 新增 tracked-evidence 扫描（脚本或测试），不以 `make sensitive-check` 的 diff 结果代替；允许列表只用于合成占位符。规则见 [23 证据策略](../evidence/23-evidence-policy.md)。
- 验证 `.scratch` 内相对链接；当前 `evidence/17-deployment.md` 与 `evidence/21-final-acceptance.md` 的 `release-rollback.md` 链接需要修正。

## 验收清单

- [ ] source-only alias、work-backed alias、stale apply、个人评价保留均有 H 回归。
- [ ] 合成 dry-run 不含生产路径、记录标识或作品编号。
- [ ] 095 历史、096–102 已提交、旧库升级测试有明确证据；不修改已发布迁移。当前链为 `096 6b4a02ec → 097 ec624bd3 → 098 531acdf4 → 099 f3dc3a41 → 100 64b25252 → 101 0c945b3e → 102 0c945b3e`，`compat/102_dev.sql` 已生成。
- [ ] 旧 095 的 checksum 兼容在 `prepareMigrationState` 之前完成；有旧库、无旧库、指纹不匹配三条路径均有测试，且不存在通用 checksum 绕过。
- [ ] 097 始终存在并保持编号连续；新装、当前 095 和旧 095 三种数据库都执行同一套重建，且 `foreign_key_check` 0 行。095–102 连续无缺口，需在当前候选上复验。
- [ ] 数据模型、翻译文档、回滚说明、迁移文档、测试夹具和历史叙述修正。
- [ ] tracked-evidence 扫描、文档链接、locale 和相关行为回归通过。

**测试入口:** H/R 的保留回归、迁移兼容测试、文档与敏感信息检查、合成 dry-run；实际生产清理由 24 另行验收。

## Comments

本票不拥有 `docs/development/translation.md` 或译文保留代码；21 先冻结该合同。任何人不得把 096 用于迁移修复；096 已提交，097 必须在其后连续。
