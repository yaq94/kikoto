# Kikoto 审查整改实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use `implement-spec` and TDD. Execute the dependency graph, not just the first available ticket.

**目标：** 在不缩减既有 46 个用户故事、28 项 AC 与真实部署门槛的前提下，修复审查确认的规则冲突、覆盖漏洞、身份恢复和发布治理问题。

**架构：** 沿用现有目录 generation／增量 sync、内容译文 SQLite/FTS、个人稳定引用及工作流。每项修改从已确认的公共入口验证，避免新增任务框架或平行身份体系。

**技术栈：** Go、SQLite/FTS5、React/Playwright、现有 Makefile、Git worktree。

**依据：** [规格](spec.md)、正式 issues、[词汇表](../../GLOSSARY.md)、ADR-0004、ADR-0005（播放版本）、ADR-0006（整改一致性）与 ADR-0007（来源字段缓存）。

**审查快照：** 审查时文档基线为 `6122b7bf`，代码基线为 `301e6c63`；当时主工作区已提交 `0dee19ea`、`6dee3087`、`134f7c90`。

**当前集成状态（2026-10-27 复核）：** 集成分支 `codex/kikoto-catalog-remediation` 已到 `f727feee`，迁移头 `105`（`101_work_parent_backfill_report.sql`、`102_translation_prefill_status.sql`、`103_translation_short_scan_index.sql`、`104_source_catalog_entry_admission.sql`、`105_translation_search_scope.sql` 已提交，`compat/105_dev.sql` 已生成）。工单 20 的 098 状态机、系列准入与 episode publication 事务内删除 mapping 已合入，其余准入入口（`602afdc0`）已并入并通过定向测试；42k 目标机浏览 p95 199.192 ms、5,001 文件卡拒绝 p95 0.593 ms。21 的 099/102/105、严格语言搜索、共享短查询生成器与 `failed` 终态已合入；1–2 字查询的返回条数与 `total` 共用 1000 张预算，超出以 `truncated` 标记（完整计数 p95 667–693 ms，故不采用）。22 的迁移 100、`work.parent_work_id`、v2 + 可选 `parent` 与 `parent_alias_not_imported` 已合入并记为 `resolved`。23 的文档与保留回归已收口；24 汇总同候选发布证据。逐项结论见 [审查记录](audit-20261025-review.md)。

交接复核时（2026-10-24 至 2026-10-25）主仓库曾前进到 `d77e37e2`：`6b4a02ec` 提交了 `096_catalog_episode_pending.sql`、`compat/096_dev.sql` 和系列暂存实现，`728651a6` 提交了 v2 可选 `parent` 的个人传输实现，`47df9c11` 提交了 wall-clock/partial 调度修复，`9bafa1ea` 合并了部分整改文档。这些提交均已并入当前分支，但仍需在当前候选上重新验收；后续实现必须以实际 SHA、测试和证据重新核对，不能把“已提交”当成“已满足合同”。

## 0. 已核实的裁决

1. **D-16 规则已实现，跨层门禁已收口。** `134f7c90` 加入三条阈值；`531acdf4` 已把 SQL 常量改为引用 `catalogadmission.Probe*`、修正旧注释、给系列详情加 pending 与准入、把 episode mapping 删除移入发布事务。`602afdc0` 已补单卡个人评价写入、播放准备、媒体流、作品目录标签准入，系列零可见成员 404，零文件与旧 generation 集合统一，目录优先 sync 的 due 推进顺序，诊断按来源过滤与可失败的 plan 断言，并已并入集成分支。42k 目标机 p95 199.192 ms、5,001 文件卡拒绝 p95 0.593 ms。
2. **严格目标语言搜索已实现。** 以下描述的是审查基线 `301e6c63` 的历史状态，用于说明为什么必须统一生成器：远程显示语言搜索当时仍并入原文 FTS、原文标题或文件名，1–2 字查询在本地、远程、bench 各有一套分段实现，远程短查询与长查询都没有 join D-16 准入。当前分支已由 `f3dc3a41` 移除原文回退，`bc880d91` 合并共享短查询生成器并给远程短查询补上 D-16 准入，`6f39e888` 用 103 的扫描索引替换 099 的短查询索引，`a2a84617` 写入 `failed` 终态，`0ec8cda0` 的 105 把 `source_id`／`browsable` 物化到译文行；`48c12395` 让返回总数与卡片页共用结果预算，超出以 `truncated` 标记。目标机 456,000 行基准与 114,000 实体短查询复测均已完成，详见[工单 21](issues/21-translation-corpus-and-search.md)。
3. **持久语料在审查基线上会被预算淘汰；当前已改为软预算告警。** 审查时 `content_translation_retention.go` 会删除 model 行，包括当前搜索所需的标题；prefill 的覆盖率也只按语言统计历史行，且只枚举 entry。当前分支已把当前 title／description 行纳入不可淘汰的持久语料，覆盖率改为关联有效实体、当前字段与 `config_key`；超限只报 `over_budget`。剩余证据是真实规模基准与当前候选复验。
4. **空库单集父级在审查基线上未恢复；当前已按 `work.parent_work_id` 实现。** 审查时 `transfer_plan.go` 对未知 episode 只报告 `relationships_not_imported`，随后创建独立 work。`64b25252` 已合入迁移 100、父级写入与系列详情回退，`catalogEntryId=0`、`workId` 非空的单集可以在没有成员行时列出；导出格式为 v2 + 可选 `parent`，`parent_alias_not_imported` 已实现并有测试，仍缺父级登记权限 403 测试与当前候选完整重跑。
5. **ADR 与迁移治理已修正到当前链。** 来源字段缓存 ADR 为 0007；095 冻结为两个已发布指纹；096–105 均已提交，迁移头 105，`compat/105_dev.sql` 已生成。ADR-0006 已声明同时修正 ADR-0007 的 schema 与繁体表述；099、100、101、102、103、104、105 已落地，剩余是当前候选上的真实迁移链复验。
6. **发布依赖已固定。** 24 依赖 17–23；17 只依赖 03、05、09、10、14、15、16、18、19，不再把 20–24 写进 17 的阻塞边。20–24 是整改子项，不是原 AC 的替代主责。
7. **已提交证据含敏感信息。** 21 号目录证据含生产路径、数据库路径和真实作品编号；22 号 dry-run JSON 是生产记录回放。公开证据必须脱敏或改为合成报告；`make sensitive-check` 只扫 diff，不能作为已提交证据的脱敏证明。

## 1. 验收责任模型

每个 AC 有一个或多个原始主责票，按场景分担；全部主责票通过才算该 AC 完整。整改票只声明具体子项，24 只负责同一候选的发布证据。这样既保留 01–19 的历史合同，也避免 20–24 用一个过宽的“主责”取代原票。

| 工单 | 原始主责或整改子项 | 不替代 |
| --- | --- | --- |
| 20 | AC-21 的 partial 变化发现与失败范围重试；AC-22 的固定日／周调度；AC-08 的 D-16 准入子项 | 08 的完整遍历；09 的每日调度主责；07/08 的卡片验收 |
| 21 | AC-06 的当前配置与缓存键子项；AC-03 的显示语言搜索子项；AC-08 的来源范围译文搜索子项 | 02 的两用户偏好、四类内容和交互队列；07/08 的全量收录 |
| 22 | AC-24 的单集父级身份恢复子项 | 16 的编号、无码、全对象和旧格式验收 |
| 23 | AC-23 的清理保留回归子项；AC-26 的文档与证据规范子项 | 10 的来源生命周期；17 的部署验收 |
| 24 | 同一 SHA 的发布证据汇总与门禁 | 20–23 的行为实现；01–19 的原始主责 |

## 2. 文件归属

| 工单 | 本票可写 | 只读或等待交接 |
| --- | --- | --- |
| 20 | `source_catalog_daily.go`、`source_catalog_sync.go`、`source_catalog_schedule.go`、`catalog_series.go` 与 `catalog_episode_staging*.go` 的读取/恢复门禁、对应测试、`catalogadmission/` 边界测试 | `openlist_catalog.go` 的准入 SQL 由 `134f7c90` 交付；不要重写；主仓库 `6b4a02ec` 的系列暂存主体已提交但不是本票产物，其他未提交调度文件不可搬入 |
| 21 | `content_translation_store.go`、`content_translation_retention.go`、`translation_prefill.go`、`library/query.go`、`library/short_search.go`、`openlist_catalog.go` 的搜索分支、`cmd/translation-bench`、对应测试、`docs/development/translation.md` | 先合入主会话已提交的有界写入修复 `6dee3087`；短查询只维护 `library/short_search.go` 一处生成器，本地、远程与 bench 都调用它，不得再各写一套分段 SQL |
| 22 | `backend/internal/personal/transfer_*`、HTTP 传输测试、work 父级迁移 | `catalog_series` 的来源发现逻辑归 11；不要猜测或重建来源成员 |
| 23 | `catalogprune` 回归测试、`backend/cmd/kikoto/main.go` 的早期 preflight 调用、`backend/internal/storage/migration.go` 或新增 preflight 文件、备份实现、`docs/architecture/data-model.md`、`docs/development/release-rollback.md`、`docs/development/migrations.md`、指定测试夹具、证据文件 | 译文保留行为和 `docs/development/translation.md` 归 21；096 已提交，097 固定为下一个连续编号 |
| 24 | `execution.md`、17–24 的发布证据、部署与回滚记录 | 不修改 01–23 的功能代码；只接收已提交候选 |

同一文件同一时刻只能有一个实施者。交接必须给出 commit SHA、已运行命令、退出状态和未覆盖项；不得把未提交工作区文件复制到另一 worktree。

## 3. 依赖与实施顺序

```
01-03 translation chain ───────────────┐
07-10 catalog chain ── 20 ─────────────┤
11-16 personal chain ── 22 ────────────┤
                                      ├─ 23 ─ 24
17 (original combination) ────────────┘
18/19 CI ─────────────────────────────┘
21 (translation corpus/search) ────────┘
```

- 20 依赖 09（08 为传递前置）。
- 21 依赖 03（02 为传递前置）；21 的行为完成后再冻结共享搜索接口。
- 22 依赖 16。
- 23 依赖 10 和 21：21 先冻结译文保留／搜索文档，23 再统一数据模型、迁移和证据规范。
- 24 依赖 17–23；17 不依赖 20–24。
- 另一会话的系列暂存（`6b4a02ec`）和有界事务修复只有进入当前候选并记录 SHA 后才能进入 24 的候选账本。

## 4. Task 1：规则、依赖与文档裁决

**文件：** 本计划、`issues/20`–`24`、`ticket-proposal.md`、`spec.md`、`execution.md`、ADR-0006、ADR-0007。

1. [ ] 在规格 D-04／AC-08 保留 D-16 三项资源准入及等于边界；明确按每个来源目录／成员条目判定，系列卡片不聚合文件数。
2. [ ] 在规格 D-03 写明“显示语言只约束文本子句；发行编号、精确代码等结构化查询不受显示语言过滤”。否则严格语言搜索会误伤编号检索。
3. [ ] 在规格 D-09 写明单集只有一个明确父系列；多个来源给出不同父系列时必须报告 `parent_conflict`，不得猜测。
4. [ ] 固定发布依赖：24 依赖 17–23；17 不依赖 20–24。17 的历史勾选移入历史证据，当前候选保持未勾选。
5. [ ] 把 095/097 写成固定连续链：095 冻结；096 已由 `6b4a02ec` 提交；097 始终存在且不得取消。097 统一重建 `source_field_translation`（有旧表或当前表都执行，复制合法行、丢弃孤儿行、重建索引），不再依赖“是否有旧 095 数据库”的分支。旧 095 ledger checksum 兼容必须在迁移运行器之前完成。
6. [ ] 所有公开证据去除真实路径、数据库路径、作品编号和生产记录标识；敏感检查增加对已跟踪 evidence 的独立扫描。

## 5. Task 2：工单 20，持续目录增量、固定调度与准入回归

**文件：** `backend/internal/httpapi/source_catalog_daily.go`、`source_catalog_sync.go`、`source_catalog_schedule.go`、`source_catalog_index.go`、`catalog_series.go`、`catalog_episode_staging*.go` 的读取/恢复门禁、`catalogadmission/`、对应测试。096 的主体已由另一会话提交；本票只补读取与恢复缺口，不重写 staging 算法。

### 5.1 调度数据模型

新增 `source_catalog_schedule`（计划迁移编号 098；096 已由 `6b4a02ec` 提交，097 为 095 修复槽位），每个来源一行，替代 `app_setting` 中混用的 `source_catalog_daily:`、`source_catalog_daily_run:` 与 `source_catalog_full:` 键：

```sql
CREATE TABLE source_catalog_schedule (
  file_source_id INTEGER PRIMARY KEY REFERENCES file_source(id) ON DELETE CASCADE,
  daily_due_at INTEGER NOT NULL,
  weekly_due_at INTEGER NOT NULL,
  deferred_until INTEGER NOT NULL DEFAULT 0,
  deferred_reason TEXT NOT NULL DEFAULT '',
  manual_pending INTEGER NOT NULL DEFAULT 0 CHECK(manual_pending IN (0,1)),
  resume_full_pending INTEGER NOT NULL DEFAULT 0 CHECK(resume_full_pending IN (0,1)),
  pending_full_run_id INTEGER REFERENCES workflow_run(id) ON DELETE SET NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX source_catalog_schedule_due
  ON source_catalog_schedule(deferred_until, daily_due_at, weekly_due_at);
```

- `daily_due_at`：下一次本地 03:00；`weekly_due_at`：下一次本地周日 03:00；时区 `Asia/Shanghai`。
- `deferred_until`：低空间、维护 hold 或磁盘未知时的统一延后截止时刻；只推进该字段，不破坏原 due 与 pending 状态。
- `deferred_reason`：`low_disk`、`maintenance_hold` 或 `disk_unknown`；延后到期后必须重新检查实际条件，条件仍在时继续延后并保留原因。
- `manual_pending=1`：用户在任务运行期间请求刷新；任务结束后只消费一次。
- `pending_full_run_id`：由 `resume_full_pending` 创建的 full run；只有该 run 终态 succeeded 才清 `resume_full_pending` 并推进 weekly due，failed/cancelled 保留待重试。
- 迁移只处理 `enabled=1 AND source_type='openlist'` 的来源：合法的未过期旧日／周 due 可沿用，否则分别取迁移后的下一个本地 03:00 和下一个本地周日 03:00；`source_catalog_daily_run:` 仅作历史读取参考，非法、过期或矛盾值进入迁移报告。
- 新建或重新启用 openlist source 时在同一事务插入或重置 schedule 行；`daily_due_at` 为下一次本地 03:00，`weekly_due_at` 为下一次本地周日 03:00，`deferred_until=0`，两个 pending 位为 0。禁用来源保留行但跳过调度。

### 5.2 派发状态机

派发必须在一个 `BEGIN IMMEDIATE` 事务里完成“读取运行状态、判定到期与延后、创建任务、推进 due”的整套决策。拆成多次读写会让两个调度 tick 同时看到同一个到期边界，或者出现 due 已推进而任务未创建的窗口；先检查运行中任务、后处理 `resume_full_pending`，也是为了避免重复创建 full。具体分支、字段语义与失败顺序以[工单 20 的派发算法](issues/20-catalog-sync-and-admission.md#派发算法)为准，本计划不重复维护第二份。

### 5.3 force 增量

**裁决（2026-10-25）：** `force` 表示“在 failed／partial／paused 的 generation 之后开始一次新的观测 generation”，不表示目录重探。sync 运行中调用 `/catalog/index` 不再返回 409，而是记 `manual_pending=1` 并返回 202，由调度器在 sync 结束后消费一次；不新增独立 force sync 入口。`executeCatalogIncrementalSync` 不调用 `enqueueSourceCatalogIndex`，这一点已满足。

### 5.4 D-16 三层一致

- 阈值：`audio == 0` 拒绝；`files > 5000` 拒绝；`nonAudio > 2000 && audio < 5` 拒绝。等于阈值允许。
- 适用范围：每个来源目录／卡片根；系列卡片不聚合文件数。系列中每个成员条目分别判定；未通过成员不进入系列列表、系列详情或成员 tracks，不影响同系列其他合格成员。系列至少有一个合格成员才显示，零可见成员时详情返回 404（当前实现返回 200 加空列表，需改）。同一规则还须覆盖单卡详情、个人评价写入、播放准备、媒体流和作品目录标签。
- `catalogadmission.Decision` 是唯一规则源。`catalogEntryAdmittedSQL`、`catalogRootAdmissionTx`、单卡 API 计数必须由同一组常量生成，不得硬编码 5001/2001/5。
- `catalogadmission/admission.go` 顶部注释改为“三项阈值决定准入；Probe* 是 SQL 计数上限”。
- 旧测试中“大附属文件卡允许发布”的断言改为拒绝；正常多轨、音频附带字幕／压缩包仍允许。
- 浏览 API 先在约 4.2 万张卡的历史规模上测量 p95。若三条相关子查询达不到预算，再增加 `source_catalog_entry_admission`（entry_id、files、audio、non_audio、reason、updated_at）并在发布／文件变更事务维护；不得直接信任陈旧计数。

### 5.5 验证

- H：partial 时 complete 分支新增文件被发现；failed/restricted/pending 范围重试；unknown 不判缺失；重复发布不产生重复卡片。
- R：进程关闭重启后同一 generation 续跑；手动请求在运行中只排队一次，结束后执行一次。
- 调度：daily/weekly 同时到期只跑一次；周日只跑 full；等于时间、跨日、重启追赶、低空间恢复。
- force：sync lease 后不再触发 `errCatalogSyncRunning`；同一 source 不产生 full 与 sync 重叠写。
- D-16：0/1/4/5 audio、2000/2001 nonAudio、5000/5001 files；入队／发布／API 三层一致；系列两成员一合格一不合格时只显示合格成员。
- 096 集成：系列详情/成员 tracks 同一 D-16 与 pending-publication 门禁；activation 批次之间不可见，publication 后可见。
- 096 恢复：publication 成功后 mapping 删除失败不得让下一次扫描删除已发布媒体；有故障注入重启测试。
- 性能：42k 卡浏览与 5,001 文件卡拒绝路径的 p95；记录索引和 SQL 计划。

## 6. Task 3：工单 21，有效译文语料、覆盖状态与严格语言搜索

**文件：** `backend/internal/httpapi/content_translation_store.go`、`content_translation_retention.go`、`translation_prefill.go`、`backend/internal/library/query.go`、`openlist_catalog.go` 的搜索分支、相关测试、`docs/development/translation.md`。

### 6.1 数据模型与迁移

21 的迁移是 099（097 是 095 重建，098 是目录调度），102 为 prefill 增加 `failed` 状态和失败表索引，103 用 `idx_content_translation_short_scan` 替换 099 的两个短查询索引，105 把 `source_id`／`browsable` 物化到译文行并把该索引换成 scoped covering 形状。迁移运行器拒绝跳号，实测缺 097/098 时 099 会报 `expected version 097, found 099`；编号只能按已提交链依次落地，不得临时改号绕过。

- `content_translation` 增加 `config_key TEXT NOT NULL DEFAULT ''`；索引 `(language, entity_kind, field, config_key, entity_id)`，并增加短查询覆盖索引 `(language, entity_kind, field, id, translated_text)`。103 删除 099 的 `idx_content_translation_short` 与 `idx_content_translation_short_current`，改用 `(language, entity_kind, field, id, origin, config_key, translated_text)` 的 `idx_content_translation_short_scan`；105 再增加 `source_id`、`browsable` 两列，把索引改成 `(language, entity_kind, field, source_id, browsable, id, origin, config_key, translated_text, entity_id)`，使短查询不再逐行回查实体或计算准入。
- `translation_prefill_state` 增加 `config_key TEXT NOT NULL DEFAULT ''`、`cursor_kind TEXT NOT NULL DEFAULT 'entry'`、`cursor_field TEXT NOT NULL DEFAULT 'title'`、`cursor_language TEXT NOT NULL DEFAULT ''`、`cursor_id INTEGER NOT NULL DEFAULT 0`、`last_error TEXT` 仍保留。`id=1` 单行状态足够，因为一个配置只有一个 prefill 队列；游标顺序固定为 `(kind, field, id, language)`。
- 新增 `translation_prefill_failure(entity_kind, entity_id, field, language, config_key, source_hash, attempts, next_retry_at, last_error, PRIMARY KEY(...))`；失败计数持久化，重启不丢，原文变化后旧失败行不能阻断新文本。
- `config_key` 只覆盖会影响模型输出的配置：model、base URL、template version、disableThinking；不包含 API key、并发、超时或预算。
- 旧 model 行没有 `config_key` 时视为 stale；迁移不猜测旧 endpoint／reasoning。若 `model` 和 `template_version` 与当前一致，可只在迁移报告中列为兼容候选，不直接参与严格搜索。

### 6.2 有效实体投影

有效搜索实体按以下口径生成，entry、series、work 使用同一套查询视图或共享 SQL：

- entry：`relocated_to_id IS NULL`、标题非空、source enabled 且 `source_type='openlist'`，并通过 D-16 准入；搜索字段只有 `title`（`source_catalog_entry` 没有 description 列，远端 listing 也不提供简介）。
- series：标题非空、至少两个成员，且至少一个成员可准入；series 的 work 不作为普通 work 重复加入；搜索字段为 `title`。
- work：标题或简介非空的本地作品；排除作为 `catalog_series.work_id` 的系列 work，避免与 series 重复；搜索字段为 `title`、`description`。

prefill 必须按 `entity_kind × field` 枚举：entry 的 title、series 的 title、work 的 title／description。原生目标语言内容先写 `origin='native'`；已有 `work_title_language`、来源已提供的目标语言标题或目标语言简介，优先作为 native 行，不重复调用模型。`zh-tw` 从 `zh-Hans` 派生，原生 `zh-Hant` 计入；`tag`、`directory` 按需翻译，不进入搜索语料与预填覆盖。

| 显示语言 | 存储／生成语言 | 搜索集合 |
| --- | --- | --- |
| `zh-cn` | `zh-Hans` model 或 native | `zh-Hans` |
| `zh-tw` | `zh-Hans` 经 OpenCC `s2t`；原生 `zh-Hant` 单独保存 | `zh-Hans` 与原生 `zh-Hant` |
| `en-us` | `en-US` model 或 native | `en-US` |
| `ja-jp` | `ja-JP` model 或 native | `ja-JP` |
| `ko-kr` | `ko-KR` model 或 native | `ko-KR` |
| `origin` | 原文，不生成译文 | 原文索引 |

### 6.3 覆盖状态

- 覆盖按 `entity_kind × field × language` 统计 total、covered、missing、stale、failed、native、model。
- 当前行条件：`origin='native'` 或 `config_key=当前 config_key`；source_text 必须等于当前实体的当前字段文本；实体必须仍有效。
- `complete` 只在所有有效实体、所有可搜索字段、所有目标语言均无 missing、stale、failed、retry 时成立；游标到结尾、历史行数量、连续失败跳过都不能写 complete。重启从 `(cursor_kind, cursor_field, cursor_id, cursor_language)` 继续。失败必须写入 `translation_prefill_failure` 后再推进游标；不得把失败当成已覆盖。游标推进与失败重试是两件事：失败行由独立 retry 队列按 `next_retry_at` 重试，不能因为游标继续而丢失该语言，也不能因为一个永久失败把整个扫描停在同一实体。成功或原文／配置变化后清理对应旧失败行，失败表必须有界。
- 新条目、标题／简介变化、模型／base URL／模板／reasoning 变化，必须重置对应实体的覆盖状态和 prefill 游标；series 标题变化也重置。
- 失败条目进入持久 retry；连续失败达到上限后状态应为 `failed`，不是 skipped，也不计入 covered；继续处理其他实体，整体保持 incomplete。102 允许该状态值；`a2a84617` 在扫到末尾、无新条目且无 due retry 时写入 `failed`，该状态不是终态，下一批仍处理 due retry。

### 6.4 搜索

| 查询类型 | 规则 |
| --- | --- |
| `origin` | 只查原文标题／原文 FTS 和文件名；不读 `content_translation` |
| 目标语言文本 | 只查目标语言 native 或当前 `config_key` 的 model 标题与简介；不并入原文 FTS，不回退原文 |
| 编号、精确代码 | 语言无关；始终查 `primary_code`／`work` 代码，不因显示语言被过滤 |
| 3 字以上 | FTS5 trigram，join 有效实体、当前行和 D-16 准入 |
| 1–2 字 | 在完整有效集合上按 `(language, entity_kind, field, id)` 稳定排序并 `instr` 匹配；只限制返回条数，不限制扫描行数；p95 超过 100 ms 时必须增加短词 n-gram 索引或等价索引。共享生成器目前提供稳定排序，但还没有 `after` 续扫参数，分页续扫仍未完成 |
| `zh-tw` | 查询先 t2s；同时查 `zh-Hans` 与原生 `zh-Hant`；原生繁体行按原查询命中 |

本地 `library/query.go`、远程 `openlist_catalog.go` 与 `cmd/translation-bench` 都调用 `library.ShortTranslationSearchSQL` 这一处生成器，并使用同一套 `DisplaySearchTargets` 与 `config_key` 条件；远程短查询把 `catalogEntryAdmittedSQL` 放进行范围，避免不合格卡片占用结果预算。文本搜索覆盖 work 的标题与简介、entry/series 的标题；标签、目录名仍可显示翻译，但不能被当作可搜索文本。删除或改写 `TestContentTranslationSearchRemoteIncludesOriginalText` 等固化错误行为的测试；新增“目标语言下按编号仍可搜索”“原文标题不命中”和“work 简介可命中”的测试。

### 6.5 保留与预算

- `content_translation` 的当前 entry/series `field='title'` 行、work 的 `field IN ('title','description')` 行是持久搜索语料，不得因 `cacheBytes`／`maxEntries` 删除；native 行永不淘汰。
- 只有 `tag`／`directory` 的 model 行、全部 `source_field_translation`、superseded/retired 行是可重建缓存。
- `cacheBytes` 只统计可淘汰行及其索引／FTS 页；持久标题／简介行超出预算时报告 `over_budget`，不牺牲搜索正确性。
- superseded/retired 清理按当前 `config_key` 和 source_text 判定，不能仅按 id 最大者判断；低频、分批执行。
- `source_field_translation` 仍是纯显示缓存，不建 FTS，不参与搜索。

### 6.6 验证与性能

- H/R/F：两用户显示／界面语言独立、四类内容、动态加载、迟到响应、重启、退役、标题／简介变化、配置变化、失败跳过和预算超限。
- H：目标语言严格搜索、origin 搜索、代码搜索、native 内容、短查询完整集合、本地／远程一致。
- R：prefill 中途关闭重启后从 `(kind, field, id, language)` 续跑；失败 retry 持久；游标继续后失败语言仍由 retry 队列处理；配置变化后旧 complete 失效。
- 性能：modernc SQLite、45.6 万行；精确命中 p95 <5 ms；3 字以上 p95 <100 ms；1–2 字 p95 <100 ms；WAL 峰值和数据库大小留证。

## 7. Task 4：工单 22，空库单集父级归属

**文件：** `backend/internal/personal/transfer_format.go`、`transfer_export.go`、`transfer_identity.go`、`transfer_plan.go`、`transfer_import.go`、`backend/internal/httpapi/catalog_series.go`、对应测试、work 父级迁移、`docs/development/personal-transfer.md`、`docs/user/*/personal-data.md`。

### 7.1 数据模型

迁移 100 用 `work.parent_work_id` 表达单集的唯一父系列，而不是把父级塞进共享成员表。这样空库恢复不依赖来源目录行，也不会把一份备份里的父级关系写成所有用户共享的目录事实。字段约束、`kind` 推断、冲突与回填报告的具体规则见[工单 22 的数据模型](issues/22-personal-transfer-parent-identity.md#数据模型)；本计划只保留这条设计理由，避免同一合同在计划与工单各维护一份。

### 7.2 备份格式：v2 + 可选 `parent`

```json
{
  "identity": {
    "ref": "c:...",
    "kind": "episode",
    "title": "Episode One",
    "parent": {
      "ref": "s:...",
      "kind": "series",
      "title": "Example Series",
      "references": []
    }
  }
}
```

- `BackupIdentity.Parent *BackupIdentity` 只允许 `kind=series`；父级不再嵌套 Parent；父级评分、备注、进度不进入该字段。
- 导出 episode 时，即使父系列没有本人评价，也必须导出父级的稳定 `s:` ref 和最小标题；父级 references 只保存为父级别名，不写入 episode 的 `References`。
- 父级的 references 必须都能解析为同一个 series；指向 episode、其他 work 或未知对象的引用报告 `parent_conflict`，不能保存到 episode。
- `personal_transfer_ref` 每个 series 只允许一个 ref；父级有多个同义 `s:` 别名时，保留 `Parent.Ref` 作为主 ref，其余报告 `parent_alias_not_imported`，不能因为别名数量阻止父级恢复。
- 格式保持 `version: 2`，`parent` 为可选字段；不新增 `version: 3`，同一语义不能有两套格式。备份带有 `parent` 时按上述规则校验并恢复；没有父级字段的旧文件按原行为导入并报告该单集未恢复父级，不能标记为“父级已恢复”。

### 7.3 导入顺序与失败边界

1. 预览阶段先解析父 ref：已有父 work 直接复用；已知 catalog series 无 work 时登记 series work 并绑定 `catalog_series.work_id`；完全未知时按备份显式身份创建 series work 和 `catalog_series`（`source_id NULL`）。父级不创建 `catalog_series_member`，因为空库没有 `source_catalog_entry`；系列与单集的可见关系以 `work.parent_work_id` 为准。
2. 父级解析成功后创建／复用 episode work，并在同一事务写入 `parent_work_id`，把 episode 主 ref 的 `object_kind` 改为 `episode`。父级 `s:` ref 保存到 `catalog_series` 行的 `personal_transfer_ref(series_id)`；父 work 只保存自己的 `w:` 主 ref。episode 只保存自己的 refs，不能把父级 `s:` ref 写到 work 行。
3. 父级缺失、冲突、多父级或循环时报告 `parent_conflict`／`parent_cycle`，该条目 blocked，不创建 episode work，不静默降级；其他条目可继续。
4. 父系列同时作为顶层 `BackupWork` 出现时只创建一次，并把父系列自己的评分／备注／进度应用到同一对象；`Parent` 只提供最小身份，不能覆盖父系列已有个人状态。
5. 幂等：同一备份重复导入不重复创建父／子 work，多个 episode 共享同一父 ref 时只创建一个父级；中途失败整批事务回滚；不下载媒体，不读取他人记录。
6. 非空库已有不同父级时，报告冲突并保留原关系；不覆盖共享关系或他人记录。循环检测使用递归父链并设置最大深度，检测到环时整条父级写入回滚。

### 7.4 系列读取路径

- `catalog_series_member` 只表达来源目录发现的成员，仍要求非空 `catalog_entry_id`；空库恢复不能伪造成员行。
- `GET /api/catalog-series/{id}` 在没有 `catalog_series_member` 时必须回退到 `work.parent_work_id`，返回 `catalogEntryId=0`、`workId` 非空的 episode 视图；纯个人 episode 没有媒体路径，不能伪造来源或下载。
- 来源系列同时有成员和 `parent_work_id` 时按 work/entry 去重；来源成员按 position，纯个人成员按标题与 work id 稳定排序。

### 7.5 验证

- H/R：空库只有单集评价、父系列无评价，导出再导入后父级归属保留；父系列有独立评价时只创建一个父对象并保留个人状态；导入后的系列卡能列出该 episode（`catalogEntryId=0`、`workId` 非空）；重复导入不重复；同一备份包含两个系列 work 时不触发空代码唯一约束。
- H：父级缺失、多个不同父级、循环、父 ref 指向错误对象、非空库已有不同父级均可审阅且不写入错误关系。
- v2 旧格式、无媒体下载、用户隔离、迁移前后结构一致。
- 迁移：新装与旧库升级后 `work.parent_work_id` 和索引一致；已发布迁移不回写。
- 文档：`docs/development/personal-transfer.md` 与五个 `docs/user/*/personal-data.md` 按“v2 + 可选 parent”说明父级、失败语义和“仅显式父级恢复”的例外。
- 现状：`64b25252` 已让 `GET /api/catalog-series/{id}` 在没有成员行时回退到 `work.parent_work_id`，返回 `catalogEntryId=0`、`workId` 非空的单集视图；导出器仍写 `version: 3`，格式收口与多父级、父级登记权限测试由 22 子代理完成。在这些项通过前，22 只能标记 `in-progress`。

## 8. Task 5：工单 23，个人引用保留、迁移兼容与仓库规范

**文件：** `backend/internal/catalogprune/` 回归测试、`backend/cmd/kikoto/main.go`、`backend/internal/storage/migration.go`（或新增早期 preflight 文件）、备份实现、`backend/migrations/097_*.sql`、`compat/096_dev.sql`、`docs/architecture/data-model.md`、`docs/development/release-rollback.md`、`docs/development/migrations.md`、指定测试夹具、证据文件。

### 8.1 清理保护

1. 验证并补齐 `catalogprune`：带 `personal_transfer_ref` 的 source-only 卡片跳过；work-backed alias 可重指向 work；stale dry-run apply 必须在删除前拒绝。
2. 覆盖两类 alias：work-backed 与 source-only；有个人评价、播放进度或标签但无 `personal_transfer_ref` 的卡片也不得误删。
3. 重新生成合成 dry-run；报告只含合成 fixture、计数和原因，不含生产 entryId／sourceId／path。
4. 旧 dry-run 只作历史，不可 apply；具体生产计数不进入公开证据。

### 8.2 迁移 095–105

- 095 冻结：记录 `25f96c22` 首次加入（无外键）和 `5defaa1c` 修改（`ON DELETE CASCADE`）的事实与两个 SHA-256；当前文件不再改写。
- 096 已由 `6b4a02ec` 提交（`096_catalog_episode_pending.sql`、`compat/096_dev.sql` 和系列暂存实现）；不得把其他变更塞进 096。
- 确认是否有数据库执行过旧 095：
  - 没有旧库：记录证据；097 仍统一重建 `source_field_translation`，保持 `095 → 096 → 097 → 098 → 099 → 100 → 101 → 102 → 103 → 104 → 105` 连续。新装基线以冻结 095 和已提交 096 为准。
  - 有旧库：097 同样重建 `source_field_translation`：新表带外键、复制合法 source_id 行、丢弃孤儿缓存、重建索引、`PRAGMA foreign_key_check` 0 行。不得根据“是否有旧库”分叉 097。
- 旧 095 的 checksum 与当前 095 不同，`migration.go` 会在到达 097 前拒绝启动。097 本身无法解决这个门禁，必须在 `prepareMigrationState` / `validateAndAdoptHistory` 之前、`MigrateFSWithOptions` 之前提供一次性 preflight：只对精确匹配旧 095 表结构指纹的数据库，先在自身事务中创建备份，再把该 ledger 行的 checksum 规范化为当前 095 checksum；不匹配、部分匹配或存在额外对象时拒绝启动。
- 禁止通用“忽略 checksum”开关。preflight 记录旧／新 checksum、schema 指纹、时间、备份路径和结果，并提供离线等价命令；097 只做结构重建。涉及 `backend/cmd/kikoto/main.go`、`backend/internal/storage/migration.go` 或新的早期 preflight 文件、备份实现、`docs/development/migrations.md` 与 `docs/development/release-rollback.md`。
- 096–105 已提交：101 增加 `work_parent_backfill_report` 并幂等重跑父级回填，102 重建 `translation_prefill_state` 以允许 `failed` 状态，并给 `translation_prefill_failure` 补 `(config_key, source_hash)` 索引，103 替换短查询索引，104 增加每卡准入计数行与其失效触发器，105 物化译文搜索 scope 并建立 scope 视图与维护触发器；`compat/105_dev.sql` 已生成。迁移头、compat 文件与测试常量必须连续一致，097 的 SQL 仍须在新装、当前 095、旧 095 三种数据库上执行同一套重建路径。

### 8.3 文档与证据

- `docs/architecture/data-model.md` 的 Translation Corpus 必须与 21 的最终实现一致：当前 config、有效实体、严格语言、短查询、软预算、title／description 行不可淘汰；删除历史叙述。在 21 落地前，该节必须标明这是目标合同，不能把尚未实现的标题保留和完整短查询写成当前事实。
- `docs/development/translation.md` 由 21 提供最终文本；23 只做交叉引用和仓库格式检查。
- `docs/development/release-rollback.md`：有 healthcheck 的 Compose 显示 `healthy`，生产无 healthcheck 只要求 `running`，以 `/health` 200 为门禁。
- 测试编号改用 `testfixture.WorkCodeAt`；超过 400 条使用 `HighCardinalityWorkCodeAt`。
- 公开证据去除生产主机路径、数据库路径、真实作品编号、生产记录标识和原始日志。
- 新增 tracked-evidence 扫描（脚本或测试），不以 `make sensitive-check` 的 diff 结果代替；允许列表只用于合成占位符。

## 9. Task 6：工单 24，同候选集成与发布证据

**文件：** `execution.md`、17–24、`evidence/`、部署与回滚文档。

24 的价值在于把分散工单的证据绑到同一个 SHA，而不是再写一套验收标准。候选账本、Full CI、迁移恢复、组合负载、真实索引、LIVE、部署与回滚的可执行门禁都在[工单 24](issues/24-release-candidate-evidence.md#发布门禁)；本计划只规定执行顺序：先冻结干净候选并记录提交清单，再按该门禁逐项取得证据，最后才允许汇总为发布结论。任何一项缺失都保持 `pending` 或 `in-progress`，不能以历史构建、健康检查或代理报告替代。

## 10. 完成定义

- 20–23 的每张票都有当前产物、真实入口、失败／恢复场景和未覆盖项。
- 24 的同一 SHA 证据闭环，且没有用历史构建、健康检查、旧 run 或代理报告替代当前验收。
- 公开仓库不含真实部署路径、数据库路径、作品编号或生产记录回放。
- 只有全部 AC 的原始主责与整改子项都有当前候选证据，才可标记 `resolved`；否则保持 `verified-local` 或 `in-progress`。
