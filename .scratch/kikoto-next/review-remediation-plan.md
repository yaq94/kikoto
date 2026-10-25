# Kikoto 审查整改实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use `implement-spec` and TDD. Execute the dependency graph, not just the first available ticket.

**目标：** 在不缩减既有 46 个用户故事、28 项 AC 与真实部署门槛的前提下，修复审查确认的规则冲突、覆盖漏洞、身份恢复和发布治理问题。

**架构：** 沿用现有目录 generation／增量 sync、内容译文 SQLite/FTS、个人稳定引用及工作流。每项修改从已确认的公共入口验证，避免新增任务框架或平行身份体系。

**技术栈：** Go、SQLite/FTS5、React/Playwright、现有 Makefile、Git worktree。

**依据：** [规格](spec.md)、正式 issues、[词汇表](../../GLOSSARY.md)、ADR-0004、ADR-0005（播放版本）、ADR-0006（整改一致性）与 ADR-0007（来源字段缓存）。

**审查快照：** 审查时文档基线为 `6122b7bf`，代码基线为 `301e6c63`；当时主工作区已提交 `0dee19ea`、`6dee3087`、`134f7c90`。

**当前集成状态（2026-10-25 复核）：** 集成分支 `codex/kikoto-catalog-remediation` 已到 `8940ef3a`，迁移头 `098`（`compat/098_dev.sql` 已生成）。工单 20 的 098 状态机、系列准入与 episode publication 事务内删除 mapping 已合入；21、22 尚无代码与迁移；23 只差文档收口；24 未开始。

交接复核时（2026-10-24 至 2026-10-25）主仓库曾前进到 `d77e37e2`：`6b4a02ec` 提交了 `096_catalog_episode_pending.sql`、`compat/096_dev.sql` 和系列暂存实现，`728651a6` 提交了 v2 可选 `parent` 的个人传输实现，`47df9c11` 提交了 wall-clock/partial 调度修复，`9bafa1ea` 合并了部分整改文档。它们都不属于本审查分支，也未被本票验收；后续实现必须以实际 SHA、测试和证据重新核对，不能把“已提交”当成“已满足合同”。

## 0. 已核实的裁决

1. **D-16 规则已实现，跨层门禁仍需收口。** `134f7c90` 加入三条阈值；`531acdf4` 已把 SQL 常量改为引用 `catalogadmission.Probe*`、修正旧注释、给系列详情加 pending 与准入、把 episode mapping 删除移入发布事务。仍缺：单卡个人评价写入、播放准备、媒体流、作品目录标签的完整准入，系列零可见成员时返回 200 空列表，以及 42k p95。
2. **严格目标语言搜索尚未实现。** 远程 `openlist_catalog.go` 仍把原文 FTS 命中并入显示语言结果，`content_translation_search_test.go` 还把该行为写成断言；本地 `library/query.go` 仍有每语言最新 50,000 行的固定窗口；远程短查询没有该窗口，但对原文标题与文件名做无界 `instr` 扫描并混入原文结果。
3. **持久语料仍被预算淘汰。** `content_translation_retention.go` 会删除 model 行，包括当前搜索所需的标题；`translation_prefill.go` 的覆盖率只按语言统计历史行，不能证明当前有效条目、原文、模型和模板已覆盖。prefill 目前只枚举 entry，不枚举 series 与 work。
4. **空库单集父级在审查基线上未恢复；主仓库后续实现仍未闭环。** `transfer_plan.go` 原来对未知 episode 只报告 `relationships_not_imported`，随后 `transfer_import.go` 创建独立 work。主仓库 `728651a6` 已加入 v2 可选 `parent`，但它通过合成 `source_catalog_entry`／`catalog_series_member` 恢复，而系列详情查询会过滤 `file_source_id IS NULL` 的成员；因此“系列卡能列出恢复的单集”仍无证据。该实现与下面 7.1 的 `work.parent_work_id` 合同也不一致，不能视为 22 完成。
5. **ADR 与迁移治理已修正到当前链。** 来源字段缓存 ADR 为 0007；095 冻结为两个已发布指纹；096、097、098 均已提交，迁移头 98。ADR-0006 已声明同时修正 ADR-0007 的 schema 与繁体表述，后续只剩 099／100 的落地。
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
| 21 | `content_translation_store.go`、`content_translation_retention.go`、`translation_prefill.go`、`library/query.go`、`openlist_catalog.go` 的搜索分支、对应测试、`docs/development/translation.md` | 先合入主会话已提交的有界写入修复 `6dee3087`；未提交的 `content_translation_*`、`openlist_catalog.go` 不得直接搬运 |
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

1. 在 `BEGIN IMMEDIATE` 事务中读取 `source_catalog_schedule`、`source_catalog_scan`、当前 `workflow_run`、`source_catalog_sync_run` 和 source 状态；source 不满足 `enabled=1 AND source_type='openlist'` 时直接返回。
2. **先检查运行中状态。** 已有 `queued/running` 的 full 或 `running` 的 sync：不创建第二个任务；`manual_pending`、到期标记和 `resume_full_pending` 只保留；sync 运行中 weekly 到期时设置 `resume_full_pending=1`，不得在 sync 内创建 full。该分支必须先于 `resume_full_pending` 创建分支。
3. 若 `now < deferred_until`，直接返回。若 `deferred_until` 已过，重新检查低空间、维护 hold 和磁盘未知；条件仍存在时写回 `deferred_until=now+1h` 和 `deferred_reason`，不能把时间到期当作条件解除；条件解除后才清空延后字段。
4. 若 `resume_full_pending=1`：`pending_full_run_id` 指向的 full 仍 queued/running 时保留并返回；succeeded 时推进 `weekly_due_at` 并清空 `resume_full_pending`／`pending_full_run_id`；failed/cancelled 时只清空 `pending_full_run_id`，保留 pending 并本轮返回，下一轮再创建；没有 pending run 且没有 queued/running full 时创建 weekly full，写入 `pending_full_run_id`，但保留 `resume_full_pending=1`，创建成功不等于完成。
5. 否则计算 `effective_due = min(daily_due_at, weekly_due_at)`；若 `weekly_due_at <= daily_due_at` 且已到期，选择 `weekly`（全量对账），否则选择 `daily`（目录优先增量）。两者同时到期时只创建一个任务。
6. 没有运行任务时才创建任务，并在同一事务推进 due；创建失败或事务回滚时不得推进 due：
   - daily：`daily_due_at = next 03:00`；`weekly_due_at` 不动。
   - weekly：`weekly_due_at = next Sunday 03:00`；`daily_due_at = next 03:00`，避免周日重复。
   - manual：消费 `manual_pending`；按当前 generation 状态选择增量或维护同步，不改变日／周计划。若同一轮已经因 daily/weekly 到期创建任务，则视为该任务已覆盖手动刷新，消费 `manual_pending`，不得再排第二个任务。
7. 最后 generation 为 `partial/failed/cancelled` 时，启动“维护同步”：对 complete 目录做目录优先探测，对 failed/restricted/pending 目录重试未完成范围；不先排 full generation，不并发 resume。只有没有任何可复用 generation 时才请求 full resume；weekly 同时到期时把 `resume_full_pending=1`，等维护同步结束后再跑 full。
8. `weekly` 全量任务必须与 sync 串行；sync 运行期间只记录 `weekly` 待处理，不能直接插入 full。
9. 低空间、维护 hold 或磁盘未知时只设置 `deferred_until=now+1h` 和 `deferred_reason`，保留原 due、pending、`pending_full_run_id` 和检查点；恢复后只追赶一次，不循环补跑。不能只推 daily 而留下已过期的 weekly，否则 weekly 会绕过延后。

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

在 096 已提交后使用计划编号 099（097 是 095 重建，098 是目录调度；迁移运行器拒绝跳号，实测缺 097/098 时 099 会报 `expected version 097, found 099`）。21 的 099 只能在前两项提交存在后落地，不得临时改号绕过。

- `content_translation` 增加 `config_key TEXT NOT NULL DEFAULT ''`；索引 `(language, entity_kind, field, config_key, entity_id)`，并增加短查询覆盖索引 `(language, entity_kind, field, id, translated_text)`。
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
- 失败条目进入持久 retry；连续失败达到上限后状态为 `failed`，不是 skipped，也不计入 covered；继续处理其他实体，整体保持 incomplete。

### 6.4 搜索

| 查询类型 | 规则 |
| --- | --- |
| `origin` | 只查原文标题／原文 FTS 和文件名；不读 `content_translation` |
| 目标语言文本 | 只查目标语言 native 或当前 `config_key` 的 model 标题与简介；不并入原文 FTS，不回退原文 |
| 编号、精确代码 | 语言无关；始终查 `primary_code`／`work` 代码，不因显示语言被过滤 |
| 3 字以上 | FTS5 trigram，join 有效实体、当前行和 D-16 准入 |
| 1–2 字 | 在完整有效集合上按 `(language, entity_kind, field, id)` keyset 分页 `instr`；只限制返回条数，不限制扫描行数；p95 超过 100 ms 时必须增加短词 n-gram 索引或等价索引 |
| `zh-tw` | 查询先 t2s；同时查 `zh-Hans` 与原生 `zh-Hant`；原生繁体行按原查询命中 |

本地 `library/query.go` 与远程 `openlist_catalog.go` 必须调用同一套 `DisplaySearchTargets` 和 `config_key` 条件。文本搜索覆盖 work 的标题与简介、entry/series 的标题；标签、目录名仍可显示翻译，但不能被当作可搜索文本。删除或改写 `TestContentTranslationSearchRemoteIncludesOriginalText` 等固化错误行为的测试；新增“目标语言下按编号仍可搜索”“原文标题不命中”和“work 简介可命中”的测试。

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

- 采用迁移 100 的 `work.parent_work_id INTEGER REFERENCES work(id) ON DELETE RESTRICT` 和索引；`kind` 由 `personal_transfer_ref.object_kind`／catalog series 成员关系推断，不新增 `work.kind` 列。只允许 `kind=episode` 的 work 设置父级，父级必须是 `kind=series` 的 work。主仓库 `728651a6` 目前走的是 v2 可选 `parent` 加合成 `source_catalog_entry`／`catalog_series_member` 的替代路径；在 22 完成前必须二选一并对齐文档、迁移和系列读取，不能两套合同并存。
- 导入 episode 时把主 `personal_transfer_ref.object_kind` 写成 `episode`；导出时 `work.parent_work_id` 非空也推断为 episode，避免空库往返后退回 work。
- 应用层拒绝自引用、父级不是 series、父级为 episode、循环、以及一个 episode 有多个不同父级。一个 work 只能有一个明确父系列；来源发现出现多个不同父系列时保留 null 并报告 `parent_conflict`，不猜测。
- 未知父级不只创建普通 work：同时创建 `catalog_series`（`source_id NULL`、`source_key NULL`、`work_id` 指向父 work），使系列入口可识别；已有 `catalog_series` 只补 `work_id`。父级不创建 `catalog_series_member`，空库没有 `source_catalog_entry`；可见成员关系由 `work.parent_work_id` 读取。
- 新建 `catalog_series` 的触发器会生成随机 `s:` ref。备份父 ref 空闲时改为备份 ref；已指向同一 series 时复用；指向其他对象时报告 `parent_conflict` 并回滚。`personal_transfer_series` 唯一索引禁止一个 series 保留两个 ref。
- 迁移回填只在 `catalog_series.work_id` 与成员 `source_catalog_entry.work_id` 都非空且不同、且该 entry 只属于一个 series 时写入；多父级、缺失或矛盾记录只进入报告。
- `work.primary_code` 与 `logical_work.canonical_code` 在 069 后允许空值，非空值才有部分唯一索引；系列 work 可以继续使用空代码，但必须测试同一备份包含多个系列 work 时不触发唯一约束。若实现新增内部代码，它只能作为内部标识，不能进入编号解析或用户可见的发行编号。

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
- v3 的 episode 必须有合法 `Parent`；缺失或非法父级报告 `parent_conflict`／`parent_required`，blocked，不创建独立 episode。v2 没有父级字段时按原行为导入并报告 `relationships_not_imported`，不能标记为“父级已恢复”。

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
- 现状阻断：主仓库 `728651a6` 的实现未让 `GET /api/catalog-series/{id}` 返回恢复的单集（空库合成 entry 的 `file_source_id` 为 NULL，被现有查询过滤）；在该缺口关闭前，22 只能标记 `in-progress`，不能标记 `verified-local` 或 `resolved`。

## 8. Task 5：工单 23，个人引用保留、迁移兼容与仓库规范

**文件：** `backend/internal/catalogprune/` 回归测试、`backend/cmd/kikoto/main.go`、`backend/internal/storage/migration.go`（或新增早期 preflight 文件）、备份实现、`backend/migrations/097_*.sql`、`compat/096_dev.sql`、`docs/architecture/data-model.md`、`docs/development/release-rollback.md`、`docs/development/migrations.md`、指定测试夹具、证据文件。

### 8.1 清理保护

1. 验证并补齐 `catalogprune`：带 `personal_transfer_ref` 的 source-only 卡片跳过；work-backed alias 可重指向 work；stale dry-run apply 必须在删除前拒绝。
2. 覆盖两类 alias：work-backed 与 source-only；有个人评价、播放进度或标签但无 `personal_transfer_ref` 的卡片也不得误删。
3. 重新生成合成 dry-run；报告只含合成 fixture、计数和原因，不含生产 entryId／sourceId／path。
4. 旧 dry-run 只作历史，不可 apply；具体生产计数不进入公开证据。

### 8.2 迁移 095/096/097–100

- 095 冻结：记录 `25f96c22` 首次加入（无外键）和 `5defaa1c` 修改（`ON DELETE CASCADE`）的事实与两个 SHA-256；当前文件不再改写。
- 096 已由 `6b4a02ec` 提交（`096_catalog_episode_pending.sql`、`compat/096_dev.sql` 和系列暂存实现）；不得把其他变更塞进 096。
- 确认是否有数据库执行过旧 095：
  - 没有旧库：记录证据；097 仍统一重建 `source_field_translation`，保持 `095 → 096 → 097 → 098 → 099 → 100` 连续。新装基线以冻结 095 和已提交 096 为准。
  - 有旧库：097 同样重建 `source_field_translation`：新表带外键、复制合法 source_id 行、丢弃孤儿缓存、重建索引、`PRAGMA foreign_key_check` 0 行。不得根据“是否有旧库”分叉 097。
- 旧 095 的 checksum 与当前 095 不同，`migration.go` 会在到达 097 前拒绝启动。097 本身无法解决这个门禁，必须在 `prepareMigrationState` / `validateAndAdoptHistory` 之前、`MigrateFSWithOptions` 之前提供一次性 preflight：只对精确匹配旧 095 表结构指纹的数据库，先在自身事务中创建备份，再把该 ledger 行的 checksum 规范化为当前 095 checksum；不匹配、部分匹配或存在额外对象时拒绝启动。
- 禁止通用“忽略 checksum”开关。preflight 记录旧／新 checksum、schema 指纹、时间、备份路径和结果，并提供离线等价命令；097 只做结构重建。涉及 `backend/cmd/kikoto/main.go`、`backend/internal/storage/migration.go` 或新的早期 preflight 文件、备份实现、`docs/development/migrations.md` 与 `docs/development/release-rollback.md`。
- 096 已提交；097 可以直接写，但迁移头、`compat/096_dev.sql`、测试常量必须连续一致。097 的 SQL 必须在新装、当前 095、旧 095 三种数据库上执行同一套重建路径。

### 8.3 文档与证据

- `docs/architecture/data-model.md` 的 Translation Corpus 必须与 21 的最终实现一致：当前 config、有效实体、严格语言、短查询、软预算、title／description 行不可淘汰；删除历史叙述。在 21 落地前，该节必须标明这是目标合同，不能把尚未实现的标题保留和完整短查询写成当前事实。
- `docs/development/translation.md` 由 21 提供最终文本；23 只做交叉引用和仓库格式检查。
- `docs/development/release-rollback.md`：有 healthcheck 的 Compose 显示 `healthy`，生产无 healthcheck 只要求 `running`，以 `/health` 200 为门禁。
- 测试编号改用 `testfixture.WorkCodeAt`；超过 400 条使用 `HighCardinalityWorkCodeAt`。
- 公开证据去除生产主机路径、数据库路径、真实作品编号、生产记录标识和原始日志。
- 新增 tracked-evidence 扫描（脚本或测试），不以 `make sensitive-check` 的 diff 结果代替；允许列表只用于合成占位符。

## 9. Task 6：工单 24，同候选集成与发布证据

**文件：** `execution.md`、17–24、`evidence/`、部署与回滚文档。

1. 候选账本：最终 SHA、分支、镜像 digest、schema、配置指纹、提交清单；工作区必须干净。
2. 明确列出 20–23 的提交和另一会话交付的已提交修复；不包含未审阅工作区文件。096 已提交，但仍须在当前候选上完成迁移和测试验收。
3. 对同一 SHA 完成 matt-code-review 双轴审查、Full CI、类型／构建、H/R/F/U、必要 race。
- 原始 AC-14 的系列证据、单集格式副本、Bonus/轨道容器和成员上限回归必须在当前 SHA 通过；未提交的系列修复或测试不能计入。
4. 迁移链：新装、既有库升级、旧 095 变体（若存在）和恢复；`PRAGMA foreign_key_check` 0 行。
5. 备份、恢复和可执行回滚路径演练；记录镜像、schema、数据库快照和回滚触发条件。
6. 组合负载：播放中扫描、翻译、浏览、评价；音轨／队列／进度保持；低空间暂停与恢复。
7. 真实完整索引：分区、目录、条目、失败、受限、未确认空目录分别报告；未完成不得称 complete。
8. 有限 LIVE：真实 Index 翻译、匿名媒体 Range／定位读取；记录版本、请求、耗时和脱敏文本。
9. 少量非视觉 F：文本、操作、请求、持久状态和媒体功能；截图采集关闭，既有视觉测试不改写。
10. AC 子项矩阵：20 机制、21 语料搜索、22 父级、23 文档规范；24 只汇总发布证据，不冒充行为主责。
11. AC-25 播放中音轨／队列／进度、AC-26 进度证据、AC-27 出站边界、AC-28 截图关闭与既有视觉测试不改写全部列入清单。
12. 部署后真实服务验收：健康、登录、翻译、目录、搜索、个人记录、媒体 Range；失败时按已验证方案回滚并继续。

## 10. 完成定义

- 20–23 的每张票都有当前产物、真实入口、失败／恢复场景和未覆盖项。
- 24 的同一 SHA 证据闭环，且没有用历史构建、健康检查、旧 run 或代理报告替代当前验收。
- 公开仓库不含真实部署路径、数据库路径、作品编号或生产记录回放。
- 只有全部 AC 的原始主责与整改子项都有当前候选证据，才可标记 `resolved`；否则保持 `verified-local` 或 `in-progress`。
