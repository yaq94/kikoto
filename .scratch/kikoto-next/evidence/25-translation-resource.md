# 25：翻译覆盖与保留维护资源整改

本轮被测代码：`5fbd7962ba68b7938fd2621b7b3abb10a52719b8`（本证据提交另规范化 106 文件末尾空行，不改变 SQL 行为；重新生成 compat 无内容差异，并复验迁移，日志 `translation-resource-migration-normalized.log`，退出 0、4.688 s），基于 `edab56b42a9df62b4eae450cfd2c4eb66c24f0f0`，分支 `codex/kikoto-translation-resource-nov7`。范围仅为翻译语料、覆盖、预填、保留维护及 106 兼容验证；不代表完整工单或发布验收完成。

环境为本机 Windows amd64、Go 1.27.3、真实迁移 SQLite、合成数据及本地 HTTP 模型桩。未调用真实来源或真实模型，未读取凭据，未部署或推送。没有 UI 改动或视觉验收。原始日志在仓库外工作区 `.kikoto-work/reports/20261107-remediation/`，下文使用文件名定位。

## 行为与资源边界

- 覆盖计算用一个 SQL 快照、一次有效语料物化生成全部语言统计，保持 native/model 共存、stale/failed/retry 分桶及 Traditional native-only 语义。
- 106 增加 `translation_corpus_revision` 与 `translation_prefill_state.checked_revision`。语义变更与 revision 在同一事务提交；完成状态仅复用相同配置与已检查 revision。模板键变化独立于数据库 revision，仍强制重新扫描。
- 并发实体变更通过独立 `entities` revision 拒绝提交旧游标，避免模型等待期间的重置丢失；并发配置/语料变更不会把旧观察标为完成。观察期间变更会返回错误，由下一次有界 tick 或请求重试。
- 写后只设置一个合并通知，不扫描、不创建 goroutine。已有生命周期 worker 启动时检查，之后最多每四个 15 秒 tick 检查一次；每个 Server 最多一个维护者，无等待队列。一次维护使用 10 秒 context、每阶段最多 20 批、每批最多 500 行；失败保留待重试状态。删除复用 `sqlutil.ExecBounded`，避免 SQLite 默认 busy handler 越过取消预算。
- 未变化的 revision 跳过空闲维护；重启重新检查维护，清理自己产生的 revision 允许后续 tick 继续处理剩余批次。当前搜索标题与简介、native、个人记录和来源身份保持原有保护规则。
- `source_field_translation` 使用独立 `cache_revision`，只影响维护/预算快照，不使已完成预填重新全扫。没有外部模块失效 hook 依赖。

## 106 的失效依赖

各表的 INSERT/DELETE 均失效；UPDATE 只监听下列列且仅值实际变化时失效。`revision` 表示覆盖内容，`entities` 防止游标覆盖并发实体变更。

| 表 | UPDATE 列 | 原因 |
| --- | --- | --- |
| source_catalog_entry | title、file_source_id、relocated_to_id、last_seen_generation | 当前原文、来源、退役关系与发布 generation |
| file_source | enabled、source_type | 来源是否进入有效语料 |
| catalog_series | title、work_id | 系列原文与 work 排除关系 |
| catalog_series_member | series_id、catalog_entry_id | 系列成员数和可准入成员 |
| work | title、description | 当前可搜字段 |
| source_catalog_entry_admission | catalog_entry_id、generation、files、audio、non_audio | 当前 generation 的准入结论 |
| source_catalog_file | catalog_entry_id、kind、observed_generation | 准入回退的当前文件集合 |
| source_catalog_episode_pending | catalog_entry_id、source_id、parent_root | 分批发布的可见性门禁 |
| source_catalog_episode_publication | source_id、parent_root、state | 整组发布的可见性门禁 |
| content_translation | entity_kind、entity_id、entity_key、field、language、source_text、translated_text、origin、config_key | 覆盖、历史行与预算变化，仅递增 revision |
| translation_prefill_failure | entity_kind、entity_id、field、language、source_hash、config_key、attempts | 失败/重试分桶，仅递增 revision |
| source_field_translation | source_id、work_code、field、path、language、source_text、translated_text | 可重建显示缓存，仅递增 cache_revision |
| app_setting | value_json，仅 translation_service 键 | 翻译配置和预算变更 |

状态时间戳、播放进度、文件大小、来源显示名、成员排序等无关字段不失效。106 对 schema 105 文件集合采取保守失效：后续 107 引入 staging 与发布指针时，必须同步调整 file/generation/admission/pending/publication 触发器，使新 staging 行不反复使已发布覆盖失效；发布指针的原子切换仍必须推进 revision。

## 可证伪回归

先记录红证据，再实现：

| 回归 | 缺陷输出 | 日志 |
| --- | --- | --- |
| TestTranslationCoverageUsesOneCorpusRead | 完整语料读取 14 次，期望 1 | translation-resource-red-coverage.log |
| TestTranslationWritesDoNotRunRetentionScans | 交互 save 执行完整 retention 查询 | translation-resource-red-retention.log |
| TestTranslationCompleteWorkerDoesNotRescanUnchangedCorpus | complete worker 再次完整扫描 | translation-resource-red-idle.log |
| TestTranslationCompleteRevisionCannotHideConfigKeyChange | 同 revision 配置键变化留下 stale 完成前缀 | translation-resource-red-template.log |

绿证据覆盖：HTTP operator 即时反映原文、来源开关/类型、实体删除、准入文件/摘要、generation、relocation、work 新增、译文删除、series membership、publication 变化；回滚和无关字段不破坏快照；真实关闭并重开应用/数据库后复用完成状态并发现新实体；在途实体/配置切换、纯模板键切换均恢复；维护取消/写锁超时后重试且保留当前语料；source display-cache 写入不使 complete worker 全扫。原有当前语料、native、缓存预算、失败重试与短查询测试继续运行。

## 验证命令与终态

以下 Go 命令在 `backend/` 执行，Make 命令在仓库根执行。所有通过结论均读取了退出状态和日志。

| 命令 | 终态 | 日志 |
| --- | --- | --- |
| `go test ./internal/httpapi -run 'Test(Translation\|ContentTranslation\|SourceFieldTranslation)' -count=1 -timeout=10m` | 0，64.577 s；最终模板小修另有下项与最终 race | translation-resource-scope-final.log |
| `go test ./internal/httpapi -run '^TestTranslationCompleteRevisionCannotHideConfigKeyChange$' -count=1` | 0，2.017 s | translation-resource-green-template.log |
| `go test -race ./internal/httpapi -run 'TestTranslation(Coverage\|Complete\|Revision\|Writes\|Prefill\|Retention\|Operator)' -count=1 -timeout=10m` | 0，79.325 s，最终代码 | translation-resource-race-focused-final.log |
| `go test ./internal/storage -run 'Test(TranslationRevision\|Legacy095\|Migration097\|MigrationChain105\|MigrateBaselineMatchesCompleteIncrementalChain)' -count=1` | 0，31.390 s，最终 106 | translation-resource-migration-final.log |
| `make backend-format backend-vet backend-build` | 0，最终代码 | translation-resource-build-final.log |
| `make sensitive-check NODE=.../node.exe` | 0，提交前扫描 15 个文件 | translation-resource-sensitive-code.log |
| `make backend-lint` | 未通过：工具依赖下载连接 proxy.golang.org 超时，未进入 lint | translation-resource-lint.log |

扩大到全部 translation/content/source-field 测试的 race 尝试在 10 分钟总预算耗尽时仍执行既有 `TestContentTranslationShortQueryCoversFullCorpus` 数据插入，未报告 race；该尝试不是通过证据，日志 `translation-resource-race.log`。改用上表直接覆盖本次变更的 race 集合后通过。常规范围测试包含完整短查询回归；未宣称全仓 race 或全量 CI 通过。

迁移检查包括 105→106、106 新库与生成 compat 对照、重复启动、故障注入后的事务回滚及重试、关闭数据库后恢复旧 105 备份再升级。旧 095 preflight 的生产指纹与未知触发器拒绝逻辑未放宽；仅当前 schema 的测试断言按版本精确接受新增的三个 source-cache revision 触发器，并核对每个 SQL body，篡改 body 回归会拒绝。已发布 001–105、baseline 与旧 compat 文件未改。

## 同规模合成测量

同一外部测量脚本、真实迁移 SQLite、每个规模两个 coverage/stat pass。对照基线见 `25-resource-baseline.md`；本轮命令为 `go test -overlay=<工作区>/review/translation-resource-own-overlay.json ./internal/httpapi -run '^TestRootMeasureTranslationResourceCost$' -count=1 -v`，退出 0，日志 `translation-resource-cost-after.log`。

| 卡片 / 当前译文 | 完整 coverage 两次 ms | retention stats 两次 ms |
| --- | --- | --- |
| 2,000 / 8,000 | 64.482 / 61.416 | 35.381 / 33.763 |
| 8,000 / 32,000 | 269.182 / 254.197 | 141.372 / 147.101 |
| 42,624 / 170,496 | 1854.560 / 1924.602 | 1192.218 / 1465.303 |

42,624 卡原 coverage 为 3927–3999 ms；本轮统计保持 total=42624、missing=0、stale=0、evictable=0。保留统计本身仍为 O(语料规模)，本次将它移出交互写后路径，并通过 revision 避免完成态重复执行，未声称统计函数变成常数时间。

最终代码额外执行 `KIKOTO_TRANSLATION_RESOURCE_SCALE=1 go test ./internal/httpapi -run '^TestTranslationResourceScale$' -count=1 -v`，退出 0，总 50.178 s；日志 `translation-resource-scale-final.log`。单个进程顺序构建独立合成库，比较启用/移除本轮 revision 触发器；每种 seed 一次、title UPDATE 20 次、无关字段 bulk UPDATE 一次、idle tick 与缓存命中各 100 次。

| 卡片 | seed 无/有触发器 ms | title UPDATE 无/有均值 ms | 无关 bulk 无/有 ms | idle tick / operator hit 均值 ms | 16 条交互写 ms |
| --- | --- | --- | --- | --- | --- |
| 2,000 | 357.514 / 599.083 | 1.261 / 2.251 | 3.932 / 3.538 | 0.050 / 0.019 | 18.303 |
| 8,000 | 2288.243 / 2185.629 | 2.279 / 1.994 | 18.005 / 15.349 | 0.032 / 0.012 | 83.549 |
| 42,624 | 16153.647 / 13465.528 | 3.250 / 2.871 | 297.174 / 181.246 | 0.035 / 0.010 | 54.057 |

写成本是本机单轮观察，受缓存与调度噪声影响，不能从有/无差值推导触发器加速或目标机 SLO。每次 revision UPDATE 只触碰单行，未新增语料扫描；真实目标机 CPU、内存与并发播放组合性能仍由集成验收负责。

## 交接与剩余项

- 106 已生成 `compat/106_dev.sql`；部署需按正常链执行。回退使用升级前的已关闭/一致备份与匹配旧二进制，不在 106 数据库上手工删列或降 schema 版本。
- 完成 schema 105 的发布依赖覆盖；107 必须接入其新发布指针，具体表/列见上表。无需在现有 catalog 文件添加应用层 hook。
- `backend-lint` 仍需在工具依赖可达的环境重跑；全量候选集成、生产迁移和目标机 SLO 未执行。
- 普通 `git commit` 按已有配置生成签名状态 N；显式 `-S` 尝试因本机无可用 secret key 失败，未改变配置或读取密钥。主代理确认沿用现有正常提交路径并如实记录，未使用禁签参数。
