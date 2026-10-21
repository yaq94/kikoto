# 23 翻译持久存储、批量接口与按语言搜索

## 1. 范围

本文件记录 2026-10-21 确认的翻译工作包（`.kikoto-work/prompts/kikoto-translation-search-20261021.md`）的落地证据。它不替代规格 AC-01..AC-28，只补充本次新增的存储、队列、搜索与预翻译部分。

## 2. 数据模型

迁移 `092_content_translation_store.sql`：

- `content_translation`：`cache_key` 唯一，`origin` 为 `native|model`，没有 `last_used_at`，不做 LRU。
- `idx_content_translation_entity (entity_kind, entity_id, field, language)`
- `idx_content_translation_language (language, entity_kind, entity_id)`
- `idx_content_translation_language_id (language, entity_kind, id)`：短查询回退的扫描边界。
- `content_translation_search`：FTS5 `trigram`，external content，`ai/ad/au` 三触发器。
- `translation_prefill_state`：单行游标，初始 `idle`。

`compat/092_dev.sql` 由 `go run ./cmd/schema-baseline -migrations ./migrations` 生成；`latestNumberedMigrationVersion = 92`。

## 3. 基准

命令：

```sh
cd backend
GOTOOLCHAIN=go1.26.10 go run ./cmd/translation-bench \
  --rows 8000,32000,65000,456000 --runs 15 \
  --json-out ../.kikoto-work/bench/translation-store.json
```

环境：Windows，`modernc.org/sqlite`（与生产驱动一致），WAL，`synchronous=NORMAL`，单连接，每 5,000 行 `wal_checkpoint(TRUNCATE)`，测量前 checkpoint 并预热执行计划。合成语料按 4 语言轮转，每条实体每种语言一行。

| 行数 | 批量写入 | 库大小 | FTS 大小 | VACUUM 后 | 精确命中 p95 | FTS p95 | 1–2 字回退 p95 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 8,000 | 0.7 s | 6.2 MB | 0.8 MB | 6.0 MB | 0.00 ms | 3.64 ms | 3.65 ms |
| 32,000 | 3.4 s | 18.8 MB | 3.0 MB | 17.3 MB | 0.00 ms | 16.80 ms | 19.51 ms |
| 65,000 | 10.0 s | 34.4 MB | 6.2 MB | 33.2 MB | 0.00 ms | 40.27 ms | 53.68 ms |
| 456,000 | 78.6 s | 230.2 MB | 44.4 MB | 222.0 MB | 0.53 ms | 55.91 ms | 39.75 ms |

对照门槛：精确命中 p95 < 5 ms（实测 0.53 ms）；3 字以上搜索 p95 < 100 ms（实测 55.91 ms）；批量 240 条不出现 `translation_queue_full`（交互通道 256）；预翻译不显著抬高 API p95 见第 6 节。

实测值低于需求估算的 0.4–0.8 GB：456k 行、VACUUM 后 222 MB。合成标题短于真实长标题，因此真实库会更接近上限，部署后以生产副本复测为准。

### 3.1 一个被证伪的写法

最初 `translatedEntityIDs` 带 `ORDER BY t.id DESC`。同一 456k 语料下 FTS p95 为 **262.26 ms**，因为排序要读完所有命中。FTS5 本身按 rowid 顺序返回，去掉 `ORDER BY` 后 `LIMIT` 可以提前停止，降到 55.91 ms。排序留在外层卡片查询。

## 4. 队列与配置

- `DefaultLimits()`：`Concurrency 8`、`QueueLimit 2048`、`MaxEntries 0`（不限）、`CacheBytes 256 MiB`（软预算）。
- 校验：并发 1–16、队列 0–4096、`maxEntries` 0–2,000,000。
- 旧配置（1/32/256 MiB）在加载时升级为新默认；其他取值原样保留。
- 交互通道 256，后台通道沿用 `queueLimit`；交互永远先调度，后台排队项会被同 key 的交互请求提升。
- 白天 08:00–24:00（Asia/Shanghai）后台最多 2 并发；夜间 00:00–08:00 用满配置并发。
- 同 `cache_key` 在途合并；超时 60 秒含排队。

## 5. 语言与搜索语义

- `DetectSourceLanguage`：假名→`ja`，韩文→`ko`，汉字经 OpenCC `t2s` 比对后判 `zh-Hans`/`zh-Hant`，纯拉丁→`en`，其余 `unknown`。
- 目标语言与检测语言一致时写 `origin=native` 行，`translated_text=source_text`，不调模型。
- `zh-tw` 只存 `zh-Hans` 行，展示用 `s2t`；繁体查询先 `t2s` 归一化，再与原生 `zh-Hant` 合并。
- 搜索严格只返回目标语言已有行；没有译文也不属于原生同语言的内容不出现，不回退原文，不提示覆盖率。

## 6. 预翻译

- `translation_prefill` worker 随应用启动，按 `id` 升序每批 50 条，逐条写 native 行并把缺失语言交给后台队列。
- 每批提交游标；重启从游标继续。
- 目录发布后把游标回退到最低的新 entry，`complete` 状态改回 `running`，不阻塞发布。
- 进度：`GET /api/settings/translation/prefill`；暂停/恢复：`POST` 同路径。

## 7. 未完成与风险

- 生产副本上的迁移演练、45.6 万行真实语料基准、API p95 影响尚未测量；需部署后在生产副本执行。
- `content_translation` 尚无自动清理旧内容哈希行的任务；当前只在迁移说明中保留设计，未实现低频清理，避免在热路径删除。
- 原生繁体检测按 20% 汉字变化比例判定；标题极短（<4 汉字）一律按简体处理，可能漏判，需真实样本校准。
- 短查询只覆盖每种语言最新 5 万行；更早的行对 1–2 字查询不可见，这是需求 §6.5 允许的上限方案。
