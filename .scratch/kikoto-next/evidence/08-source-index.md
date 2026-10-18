# 08 完整来源索引与恢复证据

实施基线 `08b14696c76c63d3a75e5fb0c984dbb2e568ad12`；索引发布队列优化见
`3f38d27`、`e206761`、`c2487f7`。本记录与同次 Git 提交中的产物绑定。
本地开发先决条件沿用 execution.md，07 真实媒体 LIVE 和真实全量索引覆盖仍未通过。

## 已取得的红绿证据

- 完整合成树 HTTP 测试先因完整索引入口 404 失败，实施后通过。预先指定合格集合为编号、无码格式、深层音声、音频附带字幕四张卡片；纯压缩包和纯字幕排除，作品库仍为空。
- 慢 HTTP 并发测试先在配置 2 时观察到最大在途数 1 而失败。实施后配置 1/2 分别观察到最大在途 1/2，100 ms 速率间隔仍受约束，乱序完成保留全部四个对象。
- 单页 201 个子目录先触发一次性入队过大而失败；持久子游标将每次写入限制为 100 个后通过。
- 低磁盘在响应到达后发生时，先观察到新增 staging 条目而失败；落盘前再次检查后暂停并保留未处理游标，恢复空间后可继续。

## H / R 范围

`go test ./internal/httpapi -run '^Test(SourceCatalogIndex|OpenListUpgrade)' -count=1`
通过（退出 0）。使用真实路由、会话、迁移 SQLite、真实 HTTP 上游与同库实际关闭重开。

- 分页总数变化重新观察、重复页不冒充完成；受限、空目录、响应超限及失败分开计数。
- 重试不重读已完成目录，不复制卡片，已有评分备注保留。
- 并发请求取消后释放，恢复后合格集合完整。
- 同一 SQLite 关闭重开保留首个目录页，续扫从下一页继续。
- 068 既有作品关系、两用户评价与播放引用升级至当前迁移后继续有效。

## 迁移与资源

新增 070 持久扫描/目录/观察表及生成的开发基线；001–069 和发布基线不改。
复用现有 SQLite、单工作流执行器、出站策略和请求门控，没有新增依赖。
元数据并发可调 1–4；每批 1–32 步、每次入队/发布最多 100 条；没有全源内存队列。
扫描仅存验证后的目录元数据，不存临时媒体 URL，不创建作品、不下载音频或封面。

`076_source_catalog_publish_queue.sql` adds the publication queue
(`publish_pending`), its partial index, and `source_catalog_audio_root`. The
migration backfills pending rows from `published_generation=0` and rebuilds the
root set once; it does not change published identities or staging rows.

## 发布尾部性能

The live copy `/tmp/kikoto-live-ba9ef02/instance/config/kikoto.db`
(34,234,368 bytes, 3,917 directories, 47,670 observations) reproduced the
pathological tail: with only five unpublished observations left, the
pre-queue query took **36.1–36.9 s** and its plan applied `LIMIT` only after a
correlated audio-root subquery for every complete directory. Making all 47,670
observations unpublished took 0.3 ms because the first 100 rows satisfied the
limit; that contrast identifies the ordering, not the row count, as the defect.

The queue query uses a materialized root set and the partial pending index.
On the same copy it returned those five rows in **0.0032–0.0040 s** with the
plan `source_catalog_observation_pending` → `source_catalog_directory_publish_root`
→ `source_catalog_audio_root`. An empty queue returns in 0.0 s. The synthetic
3,000-directory regression fixture takes about 5 ms for the same tail, and the
legacy query in that fixture still takes 36.5 s.

| Measurement | Legacy | Queue |
| --- | --- | --- |
| Five-row tail, real copy | 36.1–36.9 s | 3.2–4.0 ms |
| Empty queue | 0.3 ms (first 100 unpublished) | 0.0 s |
| 3,000-directory fixture tail | 36.5 s | ~5 ms |
| Query plan | correlated `EXISTS` per complete directory | partial index seek + root join |

The worker no longer publishes after every 32-step batch. It drains at most
four batches of 100 rows before the next page batch and returns the remaining
backlog to the workflow queue; an exhausted queue returns immediately instead
of repeating the publication query 32 times.

## 未完成项

管理员进度、配置、取消和继续已完成 F 红绿：无入口时失败，加入入口后 1/1 通过；
验证真实前后端联通、并发 2/100 ms 配置、取消后刷新重开、同 run 继续、3 张预期卡片和作品库总数 0。
截图、trace、video 均关闭。没有视觉验收。

补充 H 覆盖管理员权限、匿名拒绝、Demo 拒绝、根目录配置可省略、限额验证、
单 JSON 请求、元数据重定向拒绝、提高目录页上限后恢复、并发触发复用同一 run。
低空间暂停时已有卡片与个人评分备注仍可读取。

前端 153 文件/867 测试、格式、类型、lint、构建、i18n、122 份文档链接与 5 语言
文档检查通过（退出 0）；后端首次 lint 0 issues、退出 0。没有修改依赖锁文件。
已合入已验证 integration `dd38bf1e70ca09fcd796621e9dc5f4a741f1b5ae`，保留03/05实现。
真实程序入口已遇到访问限制，本票不重复探测或绕过；真实范围/时间/失败报告须在可访问的实际运行中补齐。

## 合并产物验证

| 入口 / 命令 | 结果与边界 |
| --- | --- |
| `make backend-test` | 首次全包运行中 httpapi（109秒）及其他非storage包通过；storage的当前版本/链清单仍预期69，聚合退出2。 |
| `go test ./internal/storage -count=1` | 同步预期为70及完整链后整包通过，退出0；包含新装、旧库迁移、baseline/增量结构与种子数据一致性、并发启动和未来版本拒绝。 |
| `go test ./internal/httpapi -run '^Test(SourceCatalogIndex\|DatabaseCleanup)' -count=1` | 13个索引顶层及既有清理回归通过，退出0；所有索引观察、权限与恢复仍用真实迁移SQLite。 |
| `go test -race ./internal/httpapi -run '^TestSourceCatalogIndex(ConfiguredConcurrency\|ConcurrentStarts\|Cancel)' -count=1` | 并发、重复提交和取消竞态检查通过，退出0。 |
| `make backend-format backend-vet backend-build backend-lint GOLANGCI_LINT_BASE=dd38bf1e70ca09fcd796621e9dc5f4a741f1b5ae` | 退出0，lint 0 issues。一次未带既定网络代理的lint下载失败不计通过；按既有代理重跑成功。 |
| `make -o frontend-install frontend-lint frontend-build frontend-format frontend-docs` | 合入03/05后退出0；复用已通过严格脚本安装的同一锁文件，避免Windows运行中的Vite依赖占用。 |
| `make -o frontend-install frontend-e2e E2E_ARGS='tests/e2e/source-catalog-index.spec.ts --project=desktop-chromium --workers=1'` | 新合并前后端的1条真实API F通过（9.7秒），退出0；无PNG、WEBM、trace ZIP产物。 |

后端最终修改涉及清理保留条件和索引staging回收，已在对应HTTP回归及静态检查中验证；没有无理由重复其他已通过包。

## 检查点与存储保留

真实清理HTTP回归先因当前扫描引用的workflow被删除而返回外键错误；加入当前scan引用保护后通过。
一源仅有一个 `source_catalog_scan`，只钉住它的当前/最近轮workflow。下一完整轮替换引用后，旧轮按原有历史规则清理，不永久保留全部扫描历史。

完整成功轮分批回收旧generation目录与旧revision观察；每个事务最多100个staging对象。201个音轨的合成旧轮被新轮替换时，公开staging计数每批减少不超过100，最终仅保留当前目录2条/观察2条；旧卡片、文件与本人评价保留。partial、失败与受限范围不触发旧观察回收。

没有新增070以外的迁移。已有001–069和发布baseline保持不变。根选择入口为
`source_catalog_roots.go:catalogAdmissionRoot`，保留已读路径、父root与音频/格式/其他子目录摘要，系列规则由11负责。

## AC与后续验收

- AC-08：完整合成集合、无码搜索/目录及不登记work；真实站点集合尚未核实。
- AC-21：页面/子目录游标持久、真实关闭重开、取消/失败/重试、重复触发以及历史清理保护。
- AC-25：配置1/2并发、共享速率、响应/页数/批次上限、低空间暂停、已有卡片和个人记录保留；播放负载组合由17主责。
- AC-27：配置权限、非法配置/JSON、重定向拒绝、响应上限、取消；沿用完整来源出站边界。
- AC-28：指定 Chromium 纯功能，关闭截图、trace、video，不宣称视觉或全格式真实媒体兼容。

工单保持 verified-local。真实全量运行报告、07 LIVE和生产部署均没有据此关闭。

## 真实增量复测（2026-10-17，当前构建）

产物：`7ed73fa06b38686caf4422f5a9cbd090ba53b7a3`，应用二进制
sha256 `53426547b698bf921e9e3fc51405cb5d08666f613f7fa0231dec3cdbd12d7636`。
实例仍是隔离的 `127.0.0.1:17955`，数据与生产分开；先停旧二进制并保留
`instance/config/kikoto.pre-076.db`（sha256
`f86ec2320b5516db2afd31a782525e54d39b1da246ca72f71beee3e806696bdf`），
再用新二进制把同一实例从 schema 75 迁移到 76。迁移前自动备份
`kikoto-pre-migration-20261017-164607-v075-to-v076.db`（40,710,144 字节）。

续跑没有重新开始 generation，而是同一 `runId=1` 的断点续跑；停止时
4,444/4,534 complete、2,472 张卡片，复测结束为：

| 项 | 终态 |
| --- | --- |
| 运行状态 | `partial`（源站上游失败，未冒充 `succeeded`） |
| 目录 | 14,491：14,460 complete、27 unconfirmed empty、4 failed |
| 失败清单 | 4 条 `upstream_unavailable`，均在 `/asmr/橙澄子汉化组/...` 的乱码子目录 |
| 请求 | 14,738 次页面请求 |
| 观察 | 389,787 条；已发布 124,693 条；`publish_pending` 可发布余量 0 |
| 卡片/文件 | 4,254 张卡片、124,693 条文件快照、69,019 条音频 |
| 根集合 | 4,254 个 audio root；与“完整且有音频的根”差集为 0 |
| 时间 | 2026-10-16T21:11:41Z 开始，2026-10-17T17:37:28Z 收尾 |
| 资源 | 2 vCPU；索引进程约 11.2%–27% CPU、59–102 MiB RSS；磁盘始终有约 17 GiB 可用 |
| 锁与池 | 新二进制日志中 `SQLITE_BUSY` 0、连接池等待告警 0、`context deadline exceeded` 0 |
| 源站 | 4 次 429 退避（`remote source origin backing off`），无紧密重试 |

停止旧运行前，同一库副本上单次发布尾部查询为 36.1–36.9 秒；迁移后
同一库的队列查询在无待发布行时为 0.0 秒，尾部 5 行为 3.2–4.0 毫秒，
见上一节。复测本身没有出现空转：`pending 0` 后立即 settled。

余下 265,094 条观察仍属于没有音频根的 complete 目录（格式/辅助目录），
保留在 staging 中不生成卡片；4 条失败目录和 27 条未确认空目录不计入
完整覆盖。该结果证明真实匿名来源的当前可达范围已索引完毕，但不宣称
站点全量完整，也不包含这 4 条源站不可读目录。

## 生产终态与准入缺陷（2026-10-17/18）

production 的首次全量运行 runId 41 收于 `partial`：14,491 个目录中 14,458
complete、27 未确认空、6 failed、0 pending；14,890 次请求、3,897 个已索引
条目。失败 6 条均为 `upstream_unavailable`，其中 5 条在
`/asmr/橙澄子汉化组/...` 的乱码子目录，1 条在 `/asmr6/aftynrose/...`。这与
隔离实例的 4 条失败不是同一份数据，不能混写。

该轮同时暴露准入缺陷：父卡不释放编码子作品、失败运行无法重新观察已完成目录、
创作者目录把多部独立作品折叠成一张卡。修复见
[17 部署证据的准入缺陷一节](17-deployment.md#索引准入缺陷与修复2026-10-18)，
包含真实 HTTP + 迁移 SQLite 的行为测试、`force` 新 generation 与逐条清理。
修复后的生产 runId 42 正在运行，其终态与覆盖数字以收尾后的记录为准；在
该轮结束前，本票维持 `verified-local`，不把 run 41 的 `partial` 当作完整覆盖。

## 创作者目录拆分（schema 78，2026-10-18）

`078_source_catalog_loose_works.sql` 增加两列，不改已有行语义：

- `source_catalog_entry.source_root`：非空时是真实来源目录，`root_path` 只是卡片身份。
- `source_catalog_directory.work_split`：一次性记录的拆分决定，取值 `single` / `prefix` / `file`。

判定只看目录的**直接音频**（相对路径不含 `/`），顺序固定：

| 条件 | 结果 |
| --- | --- |
| 无直接音频 | `single` |
| 全部 stem 为纯数字（`001.mp3`..`225.mp3`） | `single`，一部有声书 |
| 至少两个作品前缀且其中一个出现两次以上 | `prefix`，每个前缀一张卡 |
| 直接音频 ≥12 且 `distinct(baseTitle)/count > 0.5` | `file`，按 base title 分组 |
| 其余 | `single` |

作品前缀为 `(?i)^(?:RJ|BJ|VJ|CC)[0-9]{5,8}(?:[^0-9A-Za-z]|$)|^[NSAB][0-9]{1,6}(?:[^0-9A-Za-z]|$)`。
`file` 模式不是一文件一卡：`亦弟亦妹1..6`、`Ending1..3` 这类同 base title 的分集留在同一张卡，
组内按字典序取第一个文件作标题。

保护条件：已登记作品、已是系列成员、被其他条目 relocated 指向的卡强制 `single`；
带自身作品码的目录强制 `single`；`/` 根不判定；拆分卡不参与系列归组。

父卡退役：只有在该根下全部 observation 已发布、全部子目录 complete，且父卡无 work / series /
relocated 依赖时，才写 `file_source_id=NULL, relocated_to_id=第一张拆分子卡` 并删除旧文件行。
旧 id 仍可解析，个人记录不会因退役丢失。

### 真实数据校准

用生产库（只读）中 run 42 已完成的观察，按同一规则复算：

| 目录 | 直接音频 | 判定 | 卡片数 |
| --- | ---: | --- | ---: |
| `/asmr/中文音声/有声小说/金麟岂是池中物--叶倩彤` | 225 | `single` | 1 |
| `/asmr/中文音声/小野猫/小野猫-N` | 429 | `prefix` | 344 |
| `/asmr/中文音声/南征` | 823 | `file` | 744（`亦弟亦妹` 6 轨一张、`抖s医生` 5 轨一张） |
| `/asmr/中文音声/岁岁` | 666 | `file` | 632 |
| `/asmr/英语音声/Tags/Harem` | 267 | `file` | 254（`Roommate Roulette - Ending1..3` 一张） |
| `/asmr/中文音声/杏吧骚麦/骚麦` | 225 | `file` | 225 |
| `/asmr/中文音声/骚麦俱乐部` | 6 | `single` | 1 |

`岁岁/CV岁岁林三岁林三小岁z 279部14G` 是父目录音频的整包副本：280 个文件里 271 个与父目录
同大小（±0.1%），文件名加 `林三歲 ` 前缀或转成繁体。按 `file` 规则会重复发布 280 张卡，
因此在判定前增加副本检测：子目录 ≥20 个音频且 ≥90% 与父目录音频大小差在 0.1% 以内时，
强制 `single`。测试 `TestSourceCatalogLooseWorksPackageCopyStaysOneCard` 覆盖该路径；
`岁岁` 下 3–7 个文件的专栏子目录低于 20 条门槛，保持各自卡片。

### 验证

| 命令 | 结果 |
| --- | --- |
| `go build ./...` | 退出 0 |
| `go test ./internal/httpapi -run '^TestSourceCatalogLooseWorks' -count=1 -timeout=15m` | 退出 0，7 项（含副本检测） |
| `go test ./internal/httpapi ./internal/storage -count=1 -timeout=25m` | 退出 0；httpapi 187.841 s，storage 16.409 s |

提交：`41549499`（拆分与浏览器用例拆分）、`f295f8af`（副本检测）。

### 已知边界

- `prefix` 规则要求同目录重复出现，`A1/A2` 这类各出现一次的两部分不会被当成两部作品。
- 副本检测只比较文件大小，没有内容哈希；同大小不同内容会被当成副本，目前生产 `content_hash` 全为空。
- 有限扫描（`/catalog/scan`）与全量索引共用判定函数，但不写 `work_split`，重复扫描仍幂等。
- 父卡退役后原评分备注解析到第一张拆分子卡，其余子卡不继承；拆分归属规则见 ADR-0004。
