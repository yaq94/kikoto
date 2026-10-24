# 23：个人引用保留、迁移兼容与仓库规范

**What to build:** 统一清理计划与个人稳定引用保留规则；使数据模型、回滚说明、测试夹具、迁移链和公开证据符合仓库边界。本票只负责文档、回归和迁移兼容，不重写 21 的译文保留行为。

**Blocked by:** [10：来源生命周期与记录保留](10-source-lifecycle-retention.md)；[21：有效译文语料、覆盖状态与严格语言搜索](21-translation-corpus-and-search.md)。ADR-0006 已接受，是本票的规则依据，不是可 `resolved` 的工单依赖。

**Status:** ready-for-agent
**Implementation:** in-progress；本分支已提交清理保护回归和译文/回滚文档收口；证据链接、迁移边界与最终验收仍在收口，生产清理由 24 验收。

**依据:** [正式规格](../spec.md)、[AGENTS.md](../../../AGENTS.md)、[ADR-0006](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)、[ADR-0007](../../../docs/decisions/ADR-0007-source-field-translation-cache.md)。

## 验收归属

本票负责 AC-23 的清理保留回归子项、AC-26 的文档与证据规范子项。AC-23 的原始主责仍是 10；AC-26 的原始主责仍是 17。23 不替代 10 的来源生命周期行为，也不替代 17 的部署验收。

## 清理与保留

1. 验证现行 `catalogprune`：带 `personal_transfer_ref` 的 source-only 卡片跳过；work-backed alias 可重指向 work；stale dry-run apply 必须在删除前拒绝。
2. 旧 D-17 的“删除 transfer ref”只作为历史记录；当前规则以 ADR-0006 的 source-only alias 保留为准。旧 dry-run 不能作为 apply 依据。
3. 覆盖两类 alias：work-backed（可重指向 work）与 source-only（无 work，必须保留）；有个人评价、播放进度或标签但无 `personal_transfer_ref` 的卡片不得被误删。
4. 重新生成合成 dry-run；报告只含合成 fixture、计数和原因，不含生产 entryId／sourceId／path。
5. 旧 643/642 dry-run 只作历史，不可 apply；报告需带批次指纹和 stale 检查。

## 译文与清理边界

- `content_translation` 保存可搜索译文语料，`content_translation_search` 是维护其文本的 FTS5 trigram 索引，`translation_prefill_state` 保存预翻译游标和状态；`source_field_translation` 只是来源标题、标签和目录名的可重建显示缓存，不建 FTS，也不参与内容搜索。
- `maxEntries` 与 `cacheBytes` 是模型行的软预算。`cacheBytes` 通过 `dbstat` 计量表和 FTS 影子表页面，低于 1 MiB 时回退为 UTF-8 文本字节；写入先提交，再按批次裁剪，因此突发写入可以暂时超过目标。
- 本分支当前实现仍允许预算淘汰 `content_translation` 模型行（包括标题行），且 1–2 字搜索只扫描每语言最新 50,000 行。这两项是 21 的整改范围；在 21 落地前，23 的文档和证据不得宣称标题译文永不被淘汰或短查询已覆盖完整有效集合。

## 迁移与仓库边界

- 本分支已跟踪最高迁移为 `095_source_field_translation_cache.sql`，并包含 `compat/095_dev.sql`；本分支没有 096 或 `catalog_episode_staging*` 文件。
- 095 冻结：记录 `25f96c22` 首次加入（无外键）和 `5defaa1c` 修改（`ON DELETE CASCADE`）的事实与两个 SHA-256；当前文件不再改写。已执行旧 095 的数据库仍受 checksum 门禁约束，是否存在这类数据库必须核实。
- 系列暂存及其迁移的唯一主责是并行系列实施会话。该会话负责实现、迁移、测试和提交 SHA；本分支不复制其未提交工作区内容，也不为尚未合并的编号预留迁移。
- 本票只验证本分支实际存在的迁移链。若并行会话提交的系列迁移之后合并，再按当时的实际编号同步迁移头、compat、回滚矩阵和测试常量；合并前不写占位迁移。
- 24 只接收已提交并记录 SHA 的系列暂存版本，不接收未提交文件或其他 worktree 的文件。
- 不得修改已发布迁移来“修复”旧环境。

## 文档与证据

- `docs/architecture/data-model.md` 的 Translation Corpus 必须与 21 的最终实现一致：当前 config、有效实体、严格语言、短查询、软预算、title 行不可淘汰；删除历史叙述。
- `docs/development/translation.md` 由 21 提供最终文本；23 只做交叉引用和仓库格式检查。
- `docs/development/release-rollback.md`：有 healthcheck 的 Compose 显示 `healthy`，生产无 healthcheck 只要求 `running`，以 `/health` 200 为门禁。
- `docs/development/migrations.md` 只同步本分支实际存在的连续迁移链和 compat 基线。
- 测试编号改用 `testfixture.WorkCodeAt`；超过 400 条使用 `HighCardinalityWorkCodeAt`。
- 公开证据去除 `/srv/...`、数据库路径、真实作品编号、生产 entryId/sourceId 和原始日志。
- 新增 tracked-evidence 扫描（脚本或测试），不以 `make sensitive-check` 的 diff 结果代替；允许列表只用于合成占位符。

## 验收清单

- [ ] source-only alias、work-backed alias、stale apply、个人评价保留均有 H 回归。
- [ ] 合成 dry-run 不含生产路径、记录标识或作品编号。
- [ ] 095 历史、当前迁移头和旧库升级测试有明确证据；不修改已发布迁移，不把未合并系列迁移计入本分支。
- [ ] 数据模型、翻译文档、回滚说明、迁移文档、测试夹具和历史叙述修正。
- [ ] tracked-evidence 扫描、文档链接、locale 和相关行为回归通过。

**测试入口:** H/R 的保留回归、迁移兼容测试、文档与敏感信息检查、合成 dry-run；实际生产清理由 24 另行验收。

## Comments

本票不拥有 `docs/development/translation.md` 或译文保留代码；21 先冻结该合同。系列暂存和其迁移由并行系列实施会话唯一负责；本分支只验证已跟踪至 095 的实际链。
