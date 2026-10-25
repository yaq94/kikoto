# Kikoto 整改工单与计划审查（2026-10-25）

审查对象：分支 `codex/kikoto-catalog-remediation`，快照 `60951b10`（含 099/100 与 22 的实现）。
审查范围：`spec.md`、`review-remediation-plan.md`、`ticket-proposal.md`、`execution.md`、`issues/17..24`、
迁移 095–100、`backend/internal/httpapi`、`backend/internal/library`、`backend/internal/personal`、相关文档与证据。

本文只记录**仍然成立**的问题。已修复项不再复述，避免与后续实现重复计账。

---

## 一、结论摘要

真实有效、可以继续沿用的部分：

- 099 没有给 `source_catalog_entry` 加 `description`，与工单、文档和 OpenList `Object` 结构一致。
- `cursor_language` 默认 `''`、`translation_prefill_failure.next_retry_at` 可空、失败行按 `NULL` 表示未调度，均已落地。
- ADR-0006 已把 ADR-0007 的“schema 不变”纳入 amendment 范围；存储语言 token（`zh-Hans/zh-Hant/en/ja/ko`）与代码一致。
- 远程显示语言搜索已不再并入原文 FTS、原文标题或文件名；编号查询独立于语言过滤。
- work 标题与简介、entry/series 标题已进入覆盖与预填；entry 未伪造 description。
- `config_key` 已计算、写入并参与当前行判定；失败表已被真实读写，重启后仍会重试。
- 系列详情已同时应用 pending 与 D-16 门禁；单卡详情已使用完整 `Admitted()`。
- publication 翻为 `published` 后，残留 pending mapping 不再删除公开文件。
- weekly due 只在 full `succeeded` 后推进；派发按 `min(daily, weekly)`；`manual_pending` 只消费一次；重新启用会重置过期 due。
- 迁移 100 与 `work.parent_work_id` 已存在；系列详情已回退到父级关系并返回 `catalogEntryId=0`。

仍然不成立、会直接导致验收失败的部分：

1. **1–2 字译文搜索不是 keyset，也没有接入 D-16 准入。** 三处各自实现，语义已经分叉。
2. **译文基准的语料与采样不能支撑工单里的 p95 数字。** 29.07 / 49.45 / 0.53 ms 不能作为验收证据。
3. **22 实现了 `version: 3`，与已确认的“保持 v2 + 可选 parent”决定相反。** 文档同时混用 v2/v3。
4. **20 仍有四个读取入口没有完整准入；系列零可见成员仍返回 200；性能断言是空断言。**
5. **21 要求的 `failed` 状态在 schema 上无法表达**，覆盖统计缺 `native/model` 分项，`zh-Hant` 不参与 complete。
6. **24 按原文不可执行**：没有候选账本模板、组合负载脚本、回滚脚本，smoke 不覆盖翻译/目录/搜索/个人记录。
7. **多份状态文档仍停在 `8940ef3a` / 098 / “21、22 无代码”**，会误导后续执行与 24 的候选账本。

---

## 二、P0：契约与实现不一致，必须先裁决

### P0-1　1–2 字短查询不是 keyset，且缺 D-16 准入

事实：

- `backend/internal/httpapi/openlist_catalog.go:1014-1049`：短查询只按 `language/entity_kind/field` 过滤，
  没有 `(id)` 上界推进，也没有 `ORDER BY`，`ct.entity_id` 直接进 `search_hits`。
- `backend/internal/library/query.go:689-708`：用 `MAX(id)` 反向递归分段（4096），段内按 id 范围扫描，
  属于“分段扫描”，不是 keyset 续扫；每个 field 各扫一遍，成本翻倍。
- `backend/internal/httpapi/content_translation_store.go:282-337`：第三套 Go 循环分段。
- 远程短查询 join 了 entry/series 与 source，但**没有** `catalogEntryAdmittedSQL`；长查询分支
  （`openlist_catalog.go:987-1009`）同样没有。
- 三处都用 `translated_text LIKE ? ESCAPE '\'`，而工单与 `docs/architecture/data-model.md` 写 `instr`。
  `LIKE` 对 ASCII 大小写不敏感、对非 ASCII 敏感，与 `instr` 的精确子串语义不同。

影响：工单 21 的“完整有效集合 + D-16 准入 + 统一生成器”三项同时不成立；
短查询在 42k/45.6 万规模下的成本无法由现有证据外推。

修正：

- 抽一个 `ShortTranslationSearchSQL(targets, scope, configKey)`，本地、远程、bench 三处共用。
- 统一为 `WHERE (language,entity_kind,field,id) > (?,?,?,?) ORDER BY language,entity_kind,field,id LIMIT ?`。
- 在命中集合外层 join `source_catalog_entry` / `catalog_series` 的准入谓词，而不是只 join 存在性。
- `LIKE` 与 `instr` 二选一，并在工单、文档、测试里同步；若保留 `LIKE`，必须补英文大小写对偶测试。

### P0-2　译文基准不能证明工单指标

事实（`backend/cmd/translation-bench/main.go`）：

- `:194-201`：`language := benchLanguages[(index/3)%4]`、`entity_id=index/3+1`，
  每个实体只落**一种**语言，与“每实体每语言一行”的生产形态不同。
- `:216-220`：`ORDER BY random()` 取一行；`countFTS`/`countShort` 固定 `zh-Hans`（`:247`、`:263`）。
  抽到英/日/韩标题时，查询在 `zh-Hans` 语料上近乎零命中，计时不可比。
- `:251-282`：短查询 SQL 用 `field IN ('title','description')` 配 `entity_kind='entry'`，
  而 entry 只有 title；与真实路径不一致。
- `:153-157`：`Exact` 仍是 `WHERE cache_key=?`，正是工单明确禁止的“用唯一键代替搜索”。
- `:302-321`：p95 取 `samples[int(n*0.95)]`，小样本时接近最大值；没有 warmup；失败时返回零值而不报错。
- 默认 `--rows` 是 `8000,32000,65000`（`:59`），与工单引用的 456,000 不一致。

影响：工单 21 勾选的 “3 字以上 p95 29.07 ms”“1–2 字 p95 49.45 ms”“精确命中 0.53 ms”
都无法复现，也不能证明用户路径。

修正：

- 按实体生成四语言行；按语言分组采样；每语言各自统计。
- `Exact` 改为两条真实路径：`work.primary_code` / `source_catalog_entry.primary_code` 等值，
  与目标语言标题整串等值（trigram 短语）。
- 短查询 SQL 直接复用生产生成器；加 warmup、输出每次样本与样本数、p95 用 nearest-rank。
- 把可复现命令、语料规则、采样语言写进证据；重跑后再引用数字。

### P0-3　22 的备份格式与已确认决定冲突

事实：

- `backend/internal/personal/transfer_export.go:30` 现在写 `Version: 3`。
- 已确认决定是“保持 `version: 2` + 可选 `parent`，不升 v3”，理由是同一语义不应出现两套格式。
- `docs/development/personal-transfer.md:4,9,18,25` 仍写 v2，`:32,59-64` 已写 v3，同一页自相矛盾。
- 迁移回填只做 UPDATE（`backend/migrations/100_work_parent.sql`），没有工单要求的“可查询报告载体”，
  多父级/缺失/矛盾无法审计。
- 缺少“多个不同父级”“父级登记权限 403”两类专项测试。

影响：如果保留 v3，必须重新确认格式决定并同步五个语种用户页；如果回到 v2，则要改导出、导入、
格式校验与测试。当前两套并存，24 无法判定 AC-24 是否通过。

修正（建议）：回到 v2 + 可选 `parent`，与已确认决定一致；把 `kind=episode` 与 `parent` 的校验保留。
若坚持 v3，则必须在工单、ADR、五个用户页、`transfer_format.go` 的版本校验里统一，并新增一条
“v2 文件仍可读”的兼容测试。

### P0-4　20 仍有读取入口未接入完整准入

事实（快照 `60951b10`）：

- `backend/internal/httpapi/openlist_review.go:77` 仍是 `admission.Files > 0 && admission.Audio == 0`。
- `backend/internal/httpapi/catalog_playback.go:41` 与 `openlist_playback.go:15` 只做 entry 加载，没有准入。
- `backend/internal/httpapi/catalog_directory.go:136` 的作品目录标签没有准入或 pending 门禁。
- 系列零可见成员：`catalog_admission_test.go:386` 只断言 `Episodes` 为空，没有断言 404；实现仍返回 200。
- `backend/internal/httpapi/openlist_catalog_perf_test.go:86,166,338,368` 的
  `strings.Contains(plan, "SCAN source_catalog_file")` 匹配不到实际的
  `SCAN ff USING INDEX source_catalog_file_parent`，是空断言。
- 42k 浏览 p95 没有测量记录。

影响：一张 6000 文件、10 音频的卡，浏览与 tracks 会拒绝，但详情页可写评价、可直接播放；
这与工单“任一入口放宽即未实现”直接冲突。

修正：四个入口统一改为 `admission.Files > 0 && !admission.Admitted()`；
系列详情在无可见成员时返回 404 并改测试；性能断言改为匹配实际 plan 形状；
补 42k 基准或明确标注为未完成。

### P0-5　21 的 `failed` 状态与覆盖口径无法落地

事实：

- `backend/migrations/092_content_translation_store.sql:64-65` 与 `compat/099_dev.sql:1040-1041` 的
  `CHECK(status IN ('idle','running','paused','complete'))` 没有 `failed`；099 未改。
- `translation_prefill.go:317-320` 只写 `running`，`:332-334` 只写 `complete`。
- `translation_corpus.go:251-258` 的覆盖结构没有 `native`/`model` 分项。
- `zh-Hant` 只统计 `Rows`，不参与 `complete`（`:373-382`），与“zh-tw = zh-Hans + 原生 zh-Hant 并集”不一致。
- `coverage.Total=0` 时 `complete()` 为真（`:269-271`），空库会误报完成。
- 失败表索引是 `(config_key, attempts, next_retry_at, entity_kind, field, language)`，
  没有 `source_hash` 前导，工单要求的 `(config_key, source_hash)` 清理无法走索引。

修正：099 重建状态 CHECK 加 `failed`（或改用独立整体状态列）；worker 在
`Failed>0 && 无 due retry` 时写 `failed`；覆盖结构增加 `native`/`model`；
明确 zh-Hant 缺失是否阻塞 complete（建议：不阻塞 zh-Hans 生成，但必须在覆盖视图里可见）；
空语料不算 complete；补 `(config_key, source_hash)` 索引。

### P0-6　24 按原文不可执行

事实：

- 候选账本只有正文条目（`issues/24:16-21`），没有模板文件。
- 仓库没有 `rollback-kikoto.sh`；`docs/development/release-rollback.md:193` 仍引用该脚本。
- 生产 `docker-compose.yml` 无 healthcheck；`deploy/compose/{dev,demo}.yml` 才有。
- `production-smoke.mjs` 覆盖健康、静态资源、认证、本地扫描、媒体 Range，不覆盖内容翻译、目录、搜索、个人记录。
- 没有组合负载脚本；历史观测是人工执行，参数与结果未入库。
- Full CI 只在 `main` push 与 `workflow_dispatch` 触发，候选分支必须显式 dispatch。

修正：新增 `.scratch/kikoto-next/evidence/24-candidate-ledger.md` 模板（SHA、分支、镜像 tag、schema、
配置指纹、提交清单、逐条命令/退出码/证据路径/环境限制）；补 `scripts/mixed-load.mjs` 或等价 Go 入口；
扩展 smoke 覆盖四项；删除或补齐回滚脚本；把“生产无 healthcheck，以 `/health` 200 为门禁”写进门禁。

---

## 三、P1：文档与事实不符

| 文件:行 | 现状 | 应改为 |
| --- | --- | --- |
| `execution.md:10,12` | 两段完全相同，HEAD `8940ef3a`、迁移头 098、“21、22 无代码” | 删除重复段；HEAD 写当前快照、迁移头 100、21/22 已合入 |
| `execution.md:14` | `6b4a02ec`、`728651a6`、`47df9c11` “不属于本审查分支” | 均已并入当前分支，但仍需当前候选验收 |
| `execution.md:24` | 20 标 `implemented`，同表又列三项未完成 | 改 `in-progress`，残余项单列 |
| `execution.md:44` | “最高 098，099 为下一个” | 最高 100；099/100 已提交，`compat/099`、`compat/100` 已生成 |
| `review-remediation-plan.md:15` | 同上，HEAD `8940ef3a`、21/22 无代码 | 改为当前快照与真实状态 |
| `review-remediation-plan.md:22` | “严格目标语言搜索尚未实现”“本地仍有 50k 窗口” | 标为审查基线历史；当前残余是短查询未 keyset、缺 D-16 join |
| `review-remediation-plan.md:25` | “只剩 099/100 落地” | 099/100 已落地，剩真实链复验 |
| `ticket-proposal.md:20` | “21、22 尚无 099/100 与实现” | 21 已合入、22 已合入但格式待裁决 |
| `ticket-proposal.md:350` | 21 共同覆盖 AC-25/26/28 | 21 只认 AC-03/06/08，删除 25/26/28 |
| `ticket-proposal.md:365` | 24 “全部 AC 的原始主责” | 24 只汇总证据，不承担行为主责（与 `issues/24:14` 一致） |
| `ticket-proposal.md:397` | AC-27 主责写 01 | 01 票自己写“共同”，需统一为单主责 + 共同复验 |
| `spec.md:147` vs `:151` | `cacheBytes` 一处“磁盘上限”，一处“软预算告警” | 统一为“软预算告警，超限报 over_budget，不删当前语料” |
| `docs/development/migrations.md:365-369` | “开发基线 098、099 为下一个编号” | 开发基线 100 |
| `docs/development/release-rollback.md:3-4,16-19,135-139` | 兼容矩阵与恢复段止于 098 | 扩到 100，补 099/100 快照路径 |
| `docs/development/translation.md:280-284` | “真实迁移链止于 096、097/098 未合并” | 已到 100，删除占位说明 |
| `evidence/23-translation-store.md:45` | “精确命中 p95 0.53 ms” 未说明是 `cache_key` | 注明该数非搜索路径，已被工单 21 取代 |
| `issues/20:8` vs `:76` | 一处“force 不再创建 full”，一处“force 创建新 generation” | 统一为“新观测 generation”，删除“不再创建 full” |
| `issues/20:103` | D-16 三层一致已勾选，`:104-107` 仍列未完成 | 取消勾选，改为“部分入口已接入” |
| `issues/21:89-97` | 全勾选，`:101` 又说真实链未重跑 | 改 `in-progress`，真实链重跑后再勾 |
| `issues/22:8,36,38` | 仍描述 `728651a6` 的合成 entry 路径 | 改为当前 `parent_work_id` 实现，并写明格式裁决 |
| `issues/23:8` | “095/097 迁移链未收口” | 已提交，待当前候选复验 |
| `issues/24:21` | 旧 schema 列表漏 096–100 | 补全 |
| `GLOSSARY.md:16,87` | `parent_work_id` 与 `catalogEntryId=0` 写成现行定义 | 已落地，可保留；补“v2/v3 待裁决”注记 |

---

## 四、P2：结构性重复与治理

- `review-remediation-plan.md` 的 5.2/7.1/9.x 与 `issues/20:55-72`、`issues/21:68-77`、`issues/22:65-72`
  基本逐字重复。两处同时维护，已经出现 force、v3、`instr` 三处漂移。建议计划只保留“为什么”，
  工单保留“做什么”，互相引用而不复制。
- `issues/24:23-36` 与 `:54-56`、`execution.md:66-75` 与 `issues/17:18-24` 重复门禁。
  保留一处，另一处只引用。
- `ticket-proposal.md:357-365` 与 `:367-398` 是两张 AC 表，24 行互相冲突。合并为一张，
  列名区分“原始主责”和“整改子项”。
- 状态词混用 `implemented` / `implemented-local` / `ready-for-review`，超出 `execution.md:83-88`
  定义的四种。统一为 `pending` / `in-progress` / `verified-local` / `resolved`。
- `spec.md` 没有为“严格目标语言搜索”“1–2 字完整集合”设 AC。21 挂在 AC-03 下，但 AC-03 只验
  偏好与四类内容。建议并入 AC-08 的“搜索”结果，或在 AC 表新增一行。

---

## 五、验收不可执行项与补齐方式

| 验收项 | 现状 | 补齐方式 |
| --- | --- | --- |
| 42k 浏览 p95 | 无阈值、无样本、无环境 | 写死 `2 vCPU/2 GiB、warm、≥20 样本、p95 ≤300 ms、p99 ≤800 ms` |
| 5,001 文件卡拒绝 p95 | 无入口 | 同上，单列一条 |
| 45.6 万行译文 p95 | bench 语料与采样有 bug | 重写语料生成与采样后重跑 |
| 真实完整索引 | 依赖生产 OpenList、2 vCPU、3–5h | 写进 24 的资源与时间预算；允许 partial + 失败清单 |
| 组合负载 | 无脚本 | 新增 `scripts/mixed-load.mjs` 或 Go 入口 |
| 部署后验收 | 无部署入口，smoke 覆盖不足 | 补部署步骤与四项 smoke 断言 |
| 回滚演练 | 无脚本、未演练 | 补脚本或明确手工步骤 + 隔离实例记录 |
| LIVE | 需真实 key 与来源 | 写明环境变量；缺失时 skip 不计通过 |

---

## 六、建议执行顺序

1. 先裁决 22 的备份格式（v2 还是 v3），只保留一套；同步五个语种用户页。
2. 修 20 的四个读取入口与系列 404，替换空性能断言。
3. 修 21 的短查询生成器（keyset + D-16 join + 统一 `instr`/`LIKE`），补 `failed` 状态与覆盖分项。
4. 重写译文 bench 并重跑，更新证据数字。
5. 清理状态文档（execution / plan / proposal / migrations / rollback）。
6. 补 24 的账本模板、组合负载脚本、smoke 扩展与回滚演练。
7. 全部合入后冻结候选 SHA，再跑双轴审查与 Full CI。

---

## 七、过程风险

两个会话同时编辑 `.scratch/kikoto-next/*` 与 `docs/*`，本次审查期间 HEAD 已从 `1ad475ee`
推进到 `60951b10`，其中 22 的实现与迁移 100 是在审查中途合入的。
建议指定单一写入者维护状态文档，另一会话只提交代码与测试，避免同一段落被反复覆盖。
