# 17 组合验收：`15b84ed2`

> 当前部署产物的非视觉组合验收。命令、退出码与观察值均来自本轮实际执行；不把历史候选的结果当作本候选证据。

## 1. 产物

| 项 | 值 |
| --- | --- |
| 镜像 | `kikoto:next-15b84ed2`，`Up (healthy)` |
| schema | 105（部署快照 `quick_check=ok`，`foreign_key_violations=0`） |
| 前端主包 | `assets/index-DNjiWLrG.js` |
| Full CI | run `40602372253`，27/27 job success |
| 回滚 | `rollbacks/pre-15b84ed2-20261027T232854Z`，`rollback.sh` 语法通过，快照 `quick_check=ok` |

## 2. AC-25：播放与后台任务并行（H、R、F）

| 用例 | 层 | 命令 | 结果 |
| --- | --- | --- | --- |
| `TestLowDiskPausesScanAndPrefillWithoutStoppingPlayback` | H | `go test ./internal/httpapi -run ... -count=1` | PASS 1.73 s |
| `TestLowDiskPauseAndPersonalRecordSurviveApplicationRestart` | R | 同上 | PASS 0.23 s |
| `TestSourceCatalogIndexLowDiskPausesBeforePublishingAndResumes` | H | 同上 | PASS 0.43 s |
| `TestTranslationPrefillPausesOnLowDiskAndResumesWhenSpaceReturns` | H | 同上 | PASS 0.10 s |
| `playback and personal records survive a low-disk catalog pause` | F | Full CI `E2E (source-playback)` | success |

H 的观察：低空间时扫描在首次发布前暂停（`indexedEntries=0`）、预填状态 `paused/paused_low_disk` 且模型调用 0 次；暂停期间目录读取、`Range: bytes=2-5` 媒体读取（206，正文 `2345`）、个人评分与备注均正常；空间恢复后两条后台任务都恢复，且不以暂停冒充完成。

F 的观察：播放中触发低空间暂停，队列（2 条、索引 0）、音轨地址与当前播放位置保持；浏览、个人评分/备注继续可用；恢复后播放器未被重启。

## 3. AC-26：真实样例、模型生成、媒体定位读取与进度

| 观察 | 值 |
| --- | --- |
| 真实 Index 生成（未缓存标题，work 30） | `status=translated`，1.19 s，`version=Index-Translate-35B-A3B\|true\|c86e8aea36eaaff2\|index-user-v1` |
| 批量翻译（`/voices/4` 首屏 5 批） | 全部 200；单批 133–722 ms；60 条 `existing` 命中 |
| 媒体 Range | `Range: bytes=0-1023` → `206`，`content-range: bytes 0-1023/26328471`，`content-length: 1024` |
| 播放中的分段读取 | 浏览器实际发出 `bytes=0-` 与 `bytes=2555904-`，均 `206` |
| 播放进度 | `currentTime 1.95 → 5.96`（4 s 内 +4.01 s），`paused=false` |
| 繁体路线 | 切换 `zh-Hant` 后 67 个字段重新请求（5 批：16/16/8/16/11），113 个屏外字段保持原文，无失败 |

## 4. 资源校准（目标机实测）

| 项 | 值 |
| --- | --- |
| 容器 CPU / 内存 | 12.93% / 142.9 MiB（3.449 GiB 的 4.05%） |
| 数据库 / 缓存目录 | 574 MB / 165 MB |
| 磁盘 | 37G used / 20G avail（2 GiB 暂停线之上） |
| 翻译队列 | `active=0, queued=0, queuedBackground=0, readers=0` |
| 译文语料软预算 | `softBytes=268435456`（256 MiB），当前行数 en 35262 / ja 35244 / ko 35244 / zh-Hans 35544 / zh-Hant 156 |
| 索引进度 | 目录 `complete=14473`、`empty=27`、`failed=4`；可播放卡片 41573 |
| 卡片页 p95 | ≤13 ms（21 号票在目标机的记录，本候选未改查询路径） |

## 5. AC-27：出站边界（H）

`go test ./internal/outbound -count=1` → ok 0.154 s。覆盖非法 URL、私有/保留地址、DNS 应答中任一不可用地址、重定向上限与凭据剥离、响应体读取上限、代理直连拒绝与代理重校验。

## 6. AC-28：浏览器功能边界（F）

Full CI 的 E2E 组（personal / openlist / series / content-translation / source-playback / matching-shared / integrated-acceptance / mobile-shell / translation-settings / mocked 1–4）全部 success。本轮生产浏览器只断言文本、操作、请求与持久状态；采集的截图不作为门禁，也没有新增像素或布局签收。

## 7. 迁移、恢复与回滚

- 部署：`deploy-kikoto.sh 15b84ed2 105` 退出 0；快照 `snapshot_schema=(105,66)`、`quick_check=ok`、`foreign_key_violations=0`；启动后 `migration_max=105`，未回滚。
- 回滚前提：快照包含 `kikoto.db`、`env.before`、`docker-compose.yml` 与 `rollback.sh`；`bash -n rollback.sh` 通过；快照库 `quick_check=ok`。
- 备份恢复演练（非破坏）：把快照复制到 `/tmp` 后以只读方式打开，`quick_check=ok`，读到 work 31、目录条目 114085、译文 136834、个人状态 23 行；演练库随即删除，生产库未改动。
- 任务恢复：R 用例覆盖低空间暂停跨重启；Full CI 的 `Backend (race)` 四片覆盖目录、推荐与个人数据的并发恢复。
- 个人数据传输与待确认操作：01–16 的 H/F 证据与 24 号账本的部署后复验继续有效，本票不重复声明。

## 8. 覆盖缺口（明确记录，不冒充完整）

- OpenList run 53 起始于 2026-10-20 20:08，早于编码损坏登记模块（2026-10-21 19:41）。4 个目录在 run 中记录为 `upstream_unavailable`，其路径含 U+FFFD，原始字节已丢失，无法由客户端推测修复。
  - `/asmr/橙澄子汉化组/[橙澄子汉化组][RJ313527]…` 与 `[…][RJ324820]…` 的音轨已由同名的正确路径索引（9 / 4 条音轨）。
  - `RJ305379` 与 `RJ308692` 的工作目录列表为空，唯一子目录名已损坏：这两部作品的音轨在当前来源中不可索引。需要在来源侧重命名该目录后重新扫描；Kikoto 不会猜测替换字符对应的字节。
  - 当前代码对这类名字的行为由 `TestCatalogEncodingDamagedDirectoryIsRecordedNotEnqueued` 与 `TestCatalogEncodingUnresolvedDoesNotMakeRunPartial` 固定：登记待修复项、不发起请求、不因此把运行标为 partial。
- 27 个 `empty` 目录的上游响应为空但未给出 404/410，按规格不能视为已删除，因此运行保持 `partial`。这是正确状态，不是停滞。
- 8192 字节边界的模型调用在历史候选 `cf4ada56` 上超时；本候选未重测该长度。
- 生产未制造真实磁盘耗尽；低空间组合由 H/R 与隔离 E2E 覆盖。

## 9. 结论

AC-25、AC-26、AC-28 在本候选上通过；AC-27 由 H 覆盖。运行 partial 的原因是可解释的上游数据缺陷与未确认空目录，已在上文逐条列出。
