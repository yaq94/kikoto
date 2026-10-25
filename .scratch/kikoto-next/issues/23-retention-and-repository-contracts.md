# 23：个人引用保留、迁移兼容与仓库规范

**What to build:** 统一清理计划与个人稳定引用保留规则；使数据模型、回滚说明、测试夹具、迁移链和公开证据符合仓库边界。本票只负责文档、回归和迁移兼容，不重写 21 的译文保留行为。

**Blocked by:** [10：来源生命周期与记录保留](10-source-lifecycle-retention.md)；[21：有效译文语料、覆盖状态与严格语言搜索](21-translation-corpus-and-search.md)。ADR-0006 已接受，是本票的规则依据，不是可 `resolved` 的工单依赖。

**Status:** ready-for-agent
**Implementation:** pending；现有 `catalogprune` 已有 source-only alias 保护，但公开证据、数据模型、回滚说明和 095/096/097 迁移链仍未收口。

**依据:** [正式规格](../spec.md)、[AGENTS.md](../../../AGENTS.md)、[ADR-0006](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)、[ADR-0007](../../../docs/decisions/ADR-0007-source-field-translation-cache.md)。

## 验收归属

本票负责 AC-23 的清理保留回归子项、AC-26 的文档与证据规范子项。AC-23 的原始主责仍是 10；AC-26 的原始主责仍是 17。23 不替代 10 的来源生命周期行为，也不替代 17 的部署验收。

## 清理与保留

1. 验证现行 `catalogprune`：带 `personal_transfer_ref` 的 source-only 卡片跳过；work-backed alias 可重指向 work；stale dry-run apply 必须在删除前拒绝。
2. 旧 D-17 的“删除 transfer ref”只作为历史记录；当前规则以 ADR-0006 的 source-only alias 保留为准。旧 dry-run 不能作为 apply 依据。
3. 覆盖两类 alias：work-backed（可重指向 work）与 source-only（无 work，必须保留）；有个人评价、播放进度或标签但无 `personal_transfer_ref` 的卡片不得被误删。
4. 重新生成合成 dry-run；报告只含合成 fixture、计数和原因，不含生产 entryId／sourceId／path。
5. 旧 643/642 dry-run 只作历史，不可 apply；报告需带批次指纹和 stale 检查。

## 迁移 095/096/097–100

- 095 冻结：记录 `25f96c22` 首次加入（无外键）和 `5defaa1c` 修改（`ON DELETE CASCADE`）的事实与两个 SHA-256；当前文件不再改写。
- 096 已被系列暂存占用：主工作区当前存在 `096_catalog_episode_pending.sql` 和 `compat/096_dev.sql`（即使尚未提交）；不得把其他变更塞进 096。
- 097 作为 095 的条件修复迁移：
  - 若确认没有数据库执行过旧 095，记录证据并保留 097 为空操作/规范化说明；新装基线以冻结 095 为准。
  - 若存在旧 095，097 在 096 提交后重建 `source_field_translation`：新表带外键、复制合法 `source_id` 行、丢弃孤儿缓存、重建索引、`PRAGMA foreign_key_check` 0 行。
  - 旧 095 的 checksum 与当前 095 不同，`migration.go` 会在到达 097 前拒绝启动。097 本身不能修复这个门禁，必须在迁移运行器之前提供一次性 preflight：只对精确匹配旧 095 表结构指纹的数据库，在事务中把该 ledger 行的 checksum 规范化为当前 095 checksum；不匹配、部分匹配或存在额外对象时拒绝启动。
  - 禁止增加通用的“忽略 checksum”开关。preflight 必须记录旧 checksum、新 checksum、旧 schema 指纹、执行时间、数据库备份路径和结果；同时提供离线等价命令，供无法启动的实例使用。097 只负责结构重建，不负责绕过门禁。
- 097 之后连续分配：098 目录调度、099 译文语料契约、100 work 父级；任何编号变化必须同步迁移头、`compat/096_dev.sql` 和测试常量。
- 不得修改已发布迁移来“修复”旧环境。

## 文档与证据

- `docs/architecture/data-model.md` 的 Translation Corpus 必须与 21 的最终实现一致：当前 config、有效实体、严格语言、短查询、软预算、title 行不可淘汰；删除历史叙述。
- `docs/development/translation.md` 由 21 提供最终文本；23 只做交叉引用和仓库格式检查。
- `docs/development/release-rollback.md`：有 healthcheck 的 Compose 显示 `healthy`，生产无 healthcheck 只要求 `running`，以 `/health` 200 为门禁。
- `docs/development/migrations.md` 同步 095/096/097–100 的连续链和 compat 基线。
- 测试编号改用 `testfixture.WorkCodeAt`；超过 400 条使用 `HighCardinalityWorkCodeAt`。
- 公开证据去除 `/srv/...`、数据库路径、真实作品编号、生产 entryId/sourceId 和原始日志。
- 新增 tracked-evidence 扫描（脚本或测试），不以 `make sensitive-check` 的 diff 结果代替；允许列表只用于合成占位符。规则见 [23 证据策略](../evidence/23-evidence-policy.md)。
- 验证 `.scratch` 内相对链接；当前 `evidence/17-deployment.md` 与 `evidence/21-final-acceptance.md` 的 `release-rollback.md` 链接需要修正。

## 验收清单

- [ ] source-only alias、work-backed alias、stale apply、个人评价保留均有 H 回归。
- [ ] 合成 dry-run 不含生产路径、记录标识或作品编号。
- [ ] 095 历史、096 占用、097 条件修复和旧库升级测试有明确证据；不修改已发布迁移。
- [ ] 旧 095 的 checksum 兼容在迁移运行器之前完成；有旧库、无旧库、指纹不匹配三条路径均有测试，且不存在通用 checksum 绕过。
- [ ] 数据模型、翻译文档、回滚说明、迁移文档、测试夹具和历史叙述修正。
- [ ] tracked-evidence 扫描、文档链接、locale 和相关行为回归通过。

**测试入口:** H/R 的保留回归、迁移兼容测试、文档与敏感信息检查、合成 dry-run；实际生产清理由 24 另行验收。

## Comments

本票不拥有 `docs/development/translation.md` 或译文保留代码；21 先冻结该合同。任何人不得把已由 `6b4a02ec` 提交的 096 用于迁移修复。
