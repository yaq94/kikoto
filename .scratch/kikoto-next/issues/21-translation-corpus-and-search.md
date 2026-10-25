# 21：有效译文语料、覆盖状态与严格语言搜索

**What to build:** 持久译文只对当前有效语料提供正确性；覆盖进度、失败恢复、目标语言搜索与短查询均使用一致定义。严格目标语言搜索不得混入原文；`origin` 才查询原文。编号和精确代码查询不受显示语言过滤。

**Blocked by:** [03：简繁转换与受保护名称保持](03-traditional-chinese-conversion.md)（02 为传递前置）；[07](07-openlist-first-playable-slice.md)（entry 实体）；[08](08-recoverable-full-source-index.md)（有效集合与准入）；[20](20-catalog-sync-and-admission.md)（098 迁移）。

**Status:** ready-for-agent
**Implementation:** in-progress；`f3dc3a41` 补齐 099 配置键、有效语料覆盖、持久 prefill 失败、保留规则与严格语言搜索；`0c945b3e` 增加 102 的 `failed` 状态枚举与失败表 `(config_key, source_hash)` 索引；`bc880d91` 把本地、远程与 bench 的 1–2 字查询统一到共享短查询生成器，并给远程短查询补上 D-16 准入；`6f39e888` 用 103 的 `idx_content_translation_short_scan` 替换 099 的两个短查询索引；`a2a84617` 写入 `failed` 终态并把覆盖拆为 `native`／`model`。迁移头为 103，`compat/103_dev.sql` 已生成。仍缺短查询 keyset `after` 续扫、D-16 join 的目标规模证据、目标硬件基准，以及真实迁移链在当前候选上重跑。

**依据:** [正式规格](../spec.md)、[整改计划](../review-remediation-plan.md)、[ADR-0006](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)、翻译工作包 D-27/D-29/D-30。

## 验收归属

本票负责 AC-06 的当前配置／缓存键子项、AC-03 的显示语言搜索子项、AC-08 的来源范围译文搜索子项。AC-03/06 的原始主责仍是 02；AC-08 的原始主责仍是 07/08。21 不替代 02 的两用户偏好、四类内容、交互队列、超时与淘汰验收。

## 数据模型与迁移

本票迁移编号固定为 099，位于已提交的 097 与 20 的 098 之后（迁移运行器拒绝跳号）。099 只能在 097、098 均已提交后落地；不得为提前测试临时改号或复制到其他编号。短查询索引由后续 103 替换，103 不改本票的数据模型。

- `content_translation` 增加 `config_key TEXT NOT NULL DEFAULT ''`；索引 `(language, entity_kind, field, config_key, entity_id)`，并为短查询增加覆盖索引 `(language, entity_kind, field, id, translated_text)`。103 删除 099 的 `idx_content_translation_short` 与 `idx_content_translation_short_current`，改由 `idx_content_translation_short_scan(language, entity_kind, field, id, origin, config_key, translated_text)` 承担；`id` 紧跟范围前缀，按 keyset 顺序取页时不再为整个范围建临时 b-tree，`origin`、`config_key`、`translated_text` 仍在覆盖尾部，完整集合计数无需回表。
- `translation_prefill_state` 增加 `config_key TEXT NOT NULL DEFAULT ''`、`cursor_kind TEXT NOT NULL DEFAULT 'entry'`、`cursor_field TEXT NOT NULL DEFAULT 'title'`、`cursor_language TEXT NOT NULL DEFAULT ''`、`cursor_id INTEGER NOT NULL DEFAULT 0`；保留 `status`、`last_error`、`updated_at`。游标顺序固定为 `(kind, field, id, language)`。失败不是“跳过”：先把失败行写入持久 retry 表，再推进游标；失败行由独立 retry 队列按 `next_retry_at` 重试，不能因为游标继续而丢失该语言，也不能因为一个永久失败把整个扫描停在同一实体。
- 新增 `translation_prefill_failure(entity_kind, entity_id, field, language, config_key, source_hash, attempts, next_retry_at, last_error, PRIMARY KEY(entity_kind, entity_id, field, language, config_key, source_hash))`；`next_retry_at` 用 `NULL` 表示未调度，失败计数持久化；原文变化后旧失败行不能压住新文本，必须按 `source_hash` 重新尝试。
- 失败表按 `(config_key, source_hash)` 清理：成功写入当前行时删除同实体／字段／语言的旧失败键；实体退役、原文变化或配置切换后删除不再匹配的失败行；后台按 `next_retry_at` 分批处理并设置行数上限，不能让失败表随历史文本无限增长。
- `config_key` 只覆盖影响模型输出的配置：model、base URL、template version、disableThinking；不包含 API key、并发、超时或预算。
- 旧 model 行没有 `config_key` 时视为 stale；迁移不猜测旧 endpoint／reasoning，若 `model` 与 `template_version` 与当前一致，只列为兼容候选，不直接进入严格搜索。

## 目标语言映射

| 显示语言 | 存储／生成语言 | 搜索集合 |
| --- | --- | --- |
| `zh-cn` | `zh-Hans` model 或 native | `zh-Hans` |
| `zh-tw` | 由 `zh-Hans` 经 OpenCC `s2t` 派生；原生 `zh-Hant` 单独保存 | `zh-Hans` 与原生 `zh-Hant` |
| `en-us` | `en-US` model 或 native | `en-US` |
| `ja-jp` | `ja-JP` model 或 native | `ja-JP` |
| `ko-kr` | `ko-KR` model 或 native | `ko-KR` |
| `origin` | 原文，不生成译文 | 原文索引 |

`zh-tw` 不是独立的模型预填队列，也不重复生成一套全文；覆盖统计把 `zh-tw` 视为 `zh-Hans` 加原生 `zh-Hant` 的展示投影。其余显示语言按上表映射到唯一的存储语言。

## 有效实体与覆盖

有效搜索实体与字段：

- entry：`relocated_to_id IS NULL`、标题非空、source enabled 且 `source_type='openlist'`，并通过 D-16 准入；可搜索字段只有 `title`（`source_catalog_entry` 没有 description 列，远端 listing 也不提供简介）。
- series：标题非空、至少两个成员、至少一个成员可准入；series 的 work 不作为普通 work 重复加入；可搜索字段为 `title`。
- work：标题或简介非空的本地作品；排除作为 `catalog_series.work_id` 的系列 work，避免重复；可搜索字段为 `title`、`description`。

prefill 必须按 `entity_kind × field` 枚举：entry 的 title、series 的 title、work 的 title／description。`source_catalog_entry` 没有 description 列，OpenList listing 也不提供简介，本票不得新增该列或伪造该字段。原生目标语言内容写 `origin='native'`；已有 `work_title_language`、来源提供的目标语言标题或目标语言简介优先写 native 行，不重复调用模型。`zh-tw` 从 `zh-Hans` 派生，原生 `zh-Hant` 计入；`tag`、`directory` 仍按需翻译，不进入搜索语料和预填覆盖。

覆盖按 `entity_kind × field × language` 统计 total、covered、missing、stale、failed、native、model。当前行必须满足：`origin='native'` 或 `config_key=当前 config_key`；`source_text` 等于当前实体的当前字段文本；实体仍有效。

`complete` 只在所有有效实体、所有可搜索字段、所有目标语言均无 missing、stale、failed、retry 时成立，空语料不算 complete。游标到结尾、历史行数量、连续失败跳过都不能写 complete。新条目、标题／简介变化、模型／base URL／模板／reasoning 变化必须重置对应实体状态和 prefill 游标；series 标题变化也重置。重启后从 `(cursor_kind, cursor_field, cursor_id, cursor_language)` 继续；失败行不会因游标推进而消失，成功或原文／配置变化后必须清理对应旧失败行，失败表必须有界。

失败条目进入持久 retry；达到重试上限后状态为 `failed`，不是 skipped，也不计入 covered；继续处理其他实体，整体保持 incomplete。扫到末尾、没有新条目且没有 due retry 时，状态行写 `status='failed'`；该状态不是终态，下一批仍处理 due retry，成功或新条目到来后恢复。状态接口返回当前 config key、total/covered/missing/stale/failed、`native`、`model`、预算字节、`over_budget`、最近错误和更新时间。

## 行类别与保留规则

| 行类别 | 例子 | 预算淘汰 | 搜索 |
| --- | --- | --- | --- |
| 持久搜索语料 | `content_translation` 的 `field='title'`（entry/series）及 `field IN ('title','description')`（work） | 不得因 `cacheBytes`／`maxEntries` 删除 | 目标语言搜索读取 |
| 原生行 | `origin='native'` | 永不淘汰 | 同语言目标可搜 |
| 可重建显示缓存 | `tag`／`directory` 的 `content_translation`、全部 `source_field_translation` | 可按预算淘汰 | 不参与内容搜索 |
| superseded | 同一实体／字段／语言、当前 `config_key` 或 source_text 不匹配的旧行 | 低频清理 | 不计数、不返回 |
| retired | 实体已退役或不存在 | 低频清理 | 不计数、不返回 |

`cacheBytes` 是软预算告警；超预算时报告 `over_budget`，不得删除当前有效标题或简介译文。`source_field_translation` 仍是纯显示缓存，不建 FTS，不参与搜索。superseded/retired 清理按当前 config 和 source_text 判定，不能仅按 id 最大者判断。

## 搜索

| 查询类型 | 规则 |
| --- | --- |
| `origin` | 只查原文标题／原文 FTS 和文件名；不读 `content_translation` |
| 目标语言文本 | 只查目标语言 native 或当前 `config_key` 的 model 标题与简介；不得并入原文 FTS，不得回退原文 |
| 编号、精确代码 | 语言无关；始终查 `primary_code`／`work` 代码，不因显示语言被过滤 |
| 3 字以上 | FTS5 trigram，join 有效实体、当前行和 D-16 准入 |
| 1–2 字 | 在完整有效集合上按 `(language, entity_kind, field, id)` keyset 分页 `instr`；只限制返回条数，不限制扫描行数；若 p95 超过 100 ms，必须增加短词 n-gram 索引或等价索引，不能以“记录依据”放弃目标 |
| `zh-tw` | 查询先 t2s；同时查 `zh-Hans` 与原生 `zh-Hant`；原生繁体行按原查询命中 |

## 已验证的算法事实（2026-10-25）

- FTS5 `trigram` 不支持少于 3 个 Unicode 字符的子串查询（官方文档：少于 3 字符不匹配任何行）。本地 SQLite 实测 2 字查询 0 命中，3 字、4 字正常。因此 1–2 字必须走独立的 `instr` 路径，不能合并成单一 FTS 查询。
- 45.6 万行内存原型（单语言 11.4 万行、全部实体有效、索引 `(language, entity_kind, field, id, translated_text)`）：2 字够 100 条即停中位 14.3 ms、最大 14.7 ms（扫描 5,000 行）；2 字零命中最坏中位 64.7 ms、最大 67.1 ms（扫描 22,800 行）；现有 50k 窗口中位 9.3 ms。
- 结论：完整有效集合的短查询路径已验证可行，但仍必须在 Go、磁盘、WAL 与 D-16 准入 join 下于目标环境复测，才能作为验收证据。原型数据不能代替工单要求的 p95。

本地 `library/query.go` 与远程 `openlist_catalog.go` 必须调用同一套 `DisplaySearchTargets` 和 `config_key` 条件。文本搜索覆盖 work 的标题与简介、entry/series 的标题；标签、目录名仍可显示翻译，但不能被当作可搜索文本。删除或改写 `TestContentTranslationSearchRemoteIncludesOriginalText` 等固化错误行为的测试；新增“目标语言下按编号仍可搜索”“目标语言下原文标题不命中”和“目标语言下 work 简介可命中”的对偶测试。

## 验收清单

本票整体保持 `in-progress`。下面已勾选项只表示对应子项在当前分支有本地测试或代码证据，不代表整票通过，也不替代 45.6 万行目标环境基准和真实迁移链重跑。

- [x] 软预算不删除当前 title／description 译文；可重建缓存、superseded、retired 的边界有 H 测试。
- [x] 覆盖统计关联当前有效实体、当前字段、当前 config_key、当前 source_text；连续失败、跳过、退役和配置变化不能显示 complete。覆盖分项含 `Native`／`Model`，`zh-Hant` 单独计 `Missing` 且不阻塞 complete，空语料不算 complete（`a2a84617`）。
- [x] prefill 游标包含语言维度；失败先持久化再推进，重启后 retry 队列仍会重试该语言，不能被后续实体推进掩盖；永久失败不会阻塞其他实体。扫到末尾且无新条目、无 due retry 时写 `failed`，该状态不是终态，下一批仍处理 due retry（`a2a84617`）。
- [x] 目标语言搜索严格只返回目标语言译文或原生同语言内容；`origin` 才出现原文；编号查询不受语言过滤。
- [x] work 的标题与简介、entry/series 的标题都参与目标语言搜索与覆盖；entry 不伪造 description；标签、目录名不进入搜索索引。
- [ ] 1–2 字查询覆盖完整有效集合，明确结果数上限和 p95，不再只扫描每语言最新 50,000 行。`bc880d91` 已把本地、远程与 bench 统一到 `library.ShortTranslationSearchSQL`，远程入口用 `catalogEntryAdmittedSQL` 作为行范围的一部分；`6f39e888` 的 103 索引让有序取页走 keyset 顺序。仍缺 `after` 续扫参数、D-16 join 的目标规模证据和目标硬件 p95。卡片页 `limit=200` 已达标，完整集合未达标，见下方基准。
- [x] 本地与远程搜索、简繁归一化、动态内容、重启和失败恢复 H/R/F 覆盖。
- [ ] modernc SQLite、45.6 万行基准。`bc880d91` 已重写语料与采样：每个实体写四种模型语言、按 id 确定性取样、默认 `--rows 456000`，短查询调用共享生成器。本机基准（`.kikoto-work/reports/bench-456k-final.json`，114,000 实体 × 4 语言，非目标硬件）结果：
  - 卡片页短查询 `limit=200` p95：en 1.094 ms、zh-Hans 0.575 ms、ja 1.316 ms、ko 1.506 ms，达标（≤100 ms）。
  - 完整集合短查询 `limit=0`（用于总数）p95：en 313.119 ms、zh-Hans 317.013 ms、ja 520.704 ms、ko 478.139 ms，**未达标**。
  - 目标语言标题整串等值 p95 1.58–2.505 ms；FTS p95 0.573–1.506 ms；编号等值低于计时器分辨率（0.00 ms，max 0.53 ms）。
  - DB 425.6 MB（`dbBytes` 446,291,968）、FTS 35.9 MB、VACUUM 后 404.5 MB；插入 131–147 s，WAL 峰值 23.3 MB。
  - 该运行未包含 D-16 准入 join；目标硬件（2 vCPU／2 GiB、warm、≥20 样本）结果仍缺。
- [ ] “精确命中”按两条用户路径分别测量并留证。bench 已改为 `primary_code` 等值与目标语言标题整串等值两条查询，不再使用 `cache_key`；本机 p95 已取得（标题 1.58–2.505 ms，编号低于计时器分辨率），仍需在目标语料上复测并确认标题查询走预期索引。

**测试入口:** H、R、少量 F、必要 U；真实模型与规模证据由 24 汇总。

**本地验证限制：** 分支开发期通过 `internal/migrationfixture` 在版本缺失时注入 no-op 097/098 占位；占位不进仓库。098 已由 `cad25804` 合入，099 已由 `f3dc3a41` 合入，101/102 已由 `0c945b3e` 合入，103 已由 `6f39e888` 合入，`compat/103_dev.sql` 已生成；`migration_chain_103_test.go` 覆盖新装到 103、68→103、84→103、重复启动幂等与 `foreign_key_check` 0 行，但真实链上的 storage 与 httpapi 重跑仍未完成，完成前不得把本票标为 `verified-local`。

**迁移设计稿处置：** 另一 worktree 的 099 设计稿给 `source_catalog_entry` 加 `description`，与第 44 行、`docs/architecture/data-model.md` 和 OpenList `Object` 结构冲突，落地时删除该列；`cursor_language` 默认值统一为 `''`，游标顺序统一为 `(kind, field, id, language)`。测试编号使用 `testfixture.WorkCodeAt`，超过 400 条使用 `HighCardinalityWorkCodeAt`。

## Comments

主会话已提交 `6dee3087` 的有界写入修复；21 在其 SHA 之后整合，不直接修改对方未提交的 `content_translation_*`、`openlist_catalog.go`。`docs/development/translation.md` 的目标段与现状段必须分开写清，不能把尚未实现的行为写成当前事实；代码注释中的 oldest-first 淘汰与证据 23 对 5 万行窗口的认可都要按上表规则改写。ADR-0006 明确 `Amends ADR-0007`。
