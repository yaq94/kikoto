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
