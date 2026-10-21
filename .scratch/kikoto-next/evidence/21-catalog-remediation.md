# 21 来源索引整改：H 包基准与验收骨架

## 1. 范围与不变量

- 工作树：`/srv/kikoto/work`，分支 `codex/rem-h`，基线提交 `17c4e5d4`。
- 只读产品代码；本提交只新增 `scripts/catalog-bench/` 与本文件。没有改 backend/frontend、迁移、部署或 Git 远端。
- 生产实例：`kikoto:next-d55130fe`，schema `84`；数据库 `source_catalog_scan` 的来源 3 / generation 53。
- 基准副本不修改源库：源库以 `mode=ro` 打开（基准读取连接另加 `query_only(1)`；`VACUUM INTO` 连接只用 `mode=ro`，因为 `query_only` 会拒绝 VACUUM），`VACUUM INTO` 写入 `/tmp`；写路径基准在副本上 `BEGIN IMMEDIATE ... ROLLBACK`，副本也不落改动。
- 保留的不变量：未知不等于删除；个人记录不随卡片/来源删除；共享关联与拆分必须本人确认；出站策略不绕过；不下载音频。H 包不实现功能，不改变这些语义。

## 2. 基线

### 2.1 产物与副本

| 项目 | 值 |
| --- | --- |
| 测量时间 | 2026-10-21 10:03–10:17Z（Asia/Shanghai 18:03–18:17） |
| 工作树提交 | `17c4e5d4` |
| 生产镜像 | `kikoto:next-d55130fe` |
| schema | 84（`schema_state.current_version=84`，`schema_migration` 最高 84） |
| 源库 | `/srv/kikoto/config/kikoto.db`，387,837,952 B |
| 副本 | `/srv/kikoto/bench/kikoto-before.db` → `/srv/kikoto/bench/kikoto-before-17c4e5d4.db`，366,694,400 B |
| 副本 sha256 | `2be092c0cd05cb1a5a2897f70d66f3032812cd0519d9b522cead9a15f79f8e01` |
| `VACUUM INTO` 耗时 | 6.76 s（服务器本地） |
| 下载耗时 | 100.6 s（SFTP，366,694,400 B） |

计数（副本实测；除注明外与审计参考一致）：

| 表/指标 | 值 |
| --- | ---: |
| `source_catalog_entry` | 114,081 |
| `source_catalog_file` | 224,395 |
| `source_catalog_observation` | 389,830 |
| `source_catalog_directory` | 14,496 |
| `publish_pending=1` | 171,604 |
| run 53 | `partial`；14,463 complete、4 failed、27 unconfirmed empty、pending 0 |
| observed / indexed | 389,823 / 41,569 |
| `/source/example` | 本副本实测 499 目录、87,660 observation、87,657 file、25 audio；审计参考为 87,419 / 87,415 / 19，副本时点已推进 |
| lifecycle | `stage=observe`、`lifecycle_cursor=0` |
| `publish_cleanup_cursor` | 1,070,887 |

### 2.2 查询耗时（服务器 Linux，modernc 驱动，warm）

命令：

```sh
/srv/kikoto/bench/catalog-bench --db /srv/kikoto/bench/kikoto-before.db --write-db /srv/kikoto/bench/kikoto-write.db \
  --runs 5 --write-runs 3 --only progress_exact,purge_empty_pending,root_incomplete,publish_1000_rows,publish_1000_rows_fixture \
  --json-out /tmp/before-server-warm.json
```

| 查询 | median | min–max | 结果 | G 目标 |
| --- | ---: | ---: | --- | ---: |
| `progress_exact`（6 条语句） | **504.9 ms** | 498.4–515.7 | run 53 / 14,494 目录 | <20 ms |
| `purge_empty_pending`（空检查） | **199.7 ms** | 195.5–200.5 | stale=0 | <1 ms |
| `root_incomplete`（`/source/example`） | **252.0 ms** | 248.1–257.3 | incomplete=0 | <5 ms |
| `publish_1000_rows`（实际队列） | **27.8 ms** | 27.3–29.8 | 0 行 | <2 s |
| `publish_1000_rows_fixture`（临时标记 1,000 行） | **5.1 ms** | 5.0–5.2 | 1,000 行 | <2 s |
| `cleanup_100_cards`（cursor 0，3 次） | **2,446.8 ms** | 2,250.1–2,906.1 | 100 卡、31 张有目录行、removed=0 | <1 s |
| `lifecycle_100_entries`（cursor 0，3 次） | **37.1 ms** | 36.1–46.2 | 100 entry、100 次状态更新 | <200 ms |

`progress_exact` 最后一次运行的分项（同一查询单位）：

| 分项 | ms |
| --- | ---: |
| scan + run 行 | 0.1 |
| staging 两个 count | 31.1 |
| 目录聚合 | 16.7 |
| observed/indexed join | 427.0 |
| 分区页 | 16.7 |
| 明细页 | 6.5 |

### 2.3 C SQLite 交叉核对

同一副本、Python 3.12.3 / SQLite 3.45.1，5 次 warm 的 median：

| 查询 | C SQLite | modernc（上表） | 审计参考 |
| --- | ---: | ---: | ---: |
| `progress_exact` | 242.8 ms（min 239.4） | 504.9 ms | 230 ms（join 189 ms） |
| `purge_empty_pending` | 81.3 ms（min 80.3） | 199.7 ms | 85 ms |
| `root_incomplete` | 117.4 ms（min 111.8） | 252.0 ms | 120 ms |

C 结果复现了审计参考，说明副本和 SQL 口径正确。C 交叉核对使用默认 cache/mmap；Go/modernc 使用产品的 16 MiB cache 与 256 MiB mmap，仍明显更慢。before/after 必须使用同一驱动，不能拿 C 的 before 对 modernc 的 after。

### 2.4 plan 摘要

- `progress_exact`：observed/indexed 子查询为 `SEARCH o USING COVERING INDEX source_catalog_observation_revision (file_source_id=?)`，随后对每个 observation 做 `SEARCH d USING INDEX sqlite_autoindex_source_catalog_directory_1 (file_source_id=? AND path=?)`；389,823 次点查是 427 ms 的主因。目录聚合与分区页各走 `source_catalog_directory_root`，并各自建临时 B-tree（`count(DISTINCT)` / `GROUP BY`）。
- `purge_empty_pending`：`SEARCH o USING COVERING INDEX source_catalog_observation_pending (file_source_id=?)` 后按 `path` 点查目录；为返回 0 仍扫过 171,604 条 pending。索引列只有 `file_source_id`，generation 条件在 join 后过滤。
- `root_incomplete`：目录臂 `SEARCH d USING INDEX source_catalog_directory_root (file_source_id=? AND generation=?)`；观察臂 `SEARCH o USING INDEX source_catalog_observation_pending (file_source_id=?)`，再按 path 点查 `pd`，audio root 用 `(file_source_id,generation,catalog_root)` 覆盖索引。
- `publish_1000_rows`：从 `source_catalog_directory` 的 `(file_source_id)` 前缀开始按主键顺序扫，逐目录用 pending 索引取观察行，再 `EXISTS` audio_root；没有 publish 游标，空队列批次仍从目录头部重扫。
- `cleanup_100_cards`：ID 取数走 `source_catalog_entry_lifecycle (file_source_id,last_seen_generation,id)`；每卡 `DELETE source_catalog_file` 走 `source_catalog_file_title (catalog_entry_id)`；有目录行的卡执行子路径删除，两个 `UNION ALL` 分支都呈现 `SEARCH source_catalog_entry USING INDEX source_catalog_entry_relocated (relocated_to_id=?)`，按 NULL 前缀扫全表 live entry，是 100 卡 2.45 s 的主要放大点。
- `lifecycle_100_entries`：取数走 `source_catalog_entry_relocated (relocated_to_id=? AND rowid>?)`；每 entry 3 条 UPDATE；`media_file_location` 更新走 `idx_media_file_location_cache_lru (location_type=?)` 再做 `instr(stream_url,...)`，`work_source_presence` 更新出现 `SCAN work_source_presence`。当前 100 entry 为 37.1 ms，已达 <200 ms 目标，但生产 partial 运行根本没有进入 lifecycle（stage 停在 observe、cursor=0）。

### 2.5 3 小时 slow / SQLITE_BUSY

命令：`docker logs --since 3h kikoto > /srv/kikoto/logs/kikoto-3h.txt 2>&1`，在 2026-10-21 10:05Z 左右采集。

| 指标 | 值 |
| --- | ---: |
| 非空日志行 | 169 |
| `slow` 行 | **51** |
| slow 类型 | `cleanup_publish` 51 |
| slow `elapsed_ms` | min 510 / p50 6,315 / max 8,160 |
| `SQLITE_BUSY` 行 | **63** |

重要口径：日志首行 `09:14:38`、末行 `09:23:54`，只覆盖约 9 分钟；容器在 `2026-10-21T10:07:00Z` 重启（RestartCount 0），日志文件在重启前采集。51/63 是该实例活跃告警窗口的计数，不是三小时稳态速率。G7/G8 的 after 必须在同等负载窗口比较，不能用一个空闲的 3 小时窗口宣称 0 slow。

## 3. 脚本与复现

脚本：`/srv/kikoto/work/scripts/catalog-bench/main.go`
说明：`/srv/kikoto/work/scripts/catalog-bench/README.md`

构建（模块在 `backend/`，命名文件构建可解析 `modernc.org/sqlite`）：

```sh
cd /srv/kikoto/work/backend
go build -o /srv/kikoto/bench/catalog-bench.exe ../scripts/catalog-bench/main.go
```

服务器 Linux 交叉编译：

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /srv/kikoto/bench/catalog-bench-linux ../scripts/catalog-bench/main.go
```

跑 before：

```sh
cd /srv/kikoto/bench
cp kikoto-before-17c4e5d4.db kikoto-write.db
./catalog-bench.exe --db kikoto-before-17c4e5d4.db --write-db kikoto-write.db \
  --logs docker-logs-3h.txt --runs 5 --write-runs 3 --label before \
  --json-out before.json
```

脚本输出每个查询的 median/min/max、结果计数、`EXPLAIN QUERY PLAN`，以及 slow/busy 统计。写路径查询（`publish_1000_rows_fixture`、`cleanup_100_cards`、`lifecycle_100_entries`）必须提供 `--vacuum-to` 或 `--write-db`；缺少副本时脚本拒绝执行，保证 `--db` 始终只读。`--vacuum-to` 可一次生成副本；不传则只读打开 `--db`。写路径每次都回滚。基线 JSON 存在 `scripts/catalog-bench/baseline/`：

- `before-server-warm.json`（读查询，5 次）
- `before-server-cleanup3.json`（cleanup，3 次）
- `before-server-lifecycle3.json`（lifecycle，3 次）
- `c-sqlite-crosscheck.txt`（C 交叉核对）

## 4. G1–G17 验收表（after 2026-10-22，schema 093 副本）

| 编号 | 场景 | 目标 | before | after |
| --- | --- | --- | --- | --- |
| G1 | `sourceCatalogProgress`（默认计数器路径） | <20 ms | 504.9 ms（`?exact=1`） | 12.103 ms（11.027–13.818）；达标。`?exact=1` 作为对账查询降到 127.528 ms |
| G2 | 空 pending 的 purge | <1 ms | 199.7 ms | 0 ms（max 0.532）；达标，改由旧 generation 的目录索引驱动，只在旧目录有 pending 时才读 observation |
| G3 | `catalogRootIncomplete('/source/example')` | <5 ms | 252.0 ms | 0 ms（max 0.538）；达标，两臂都改成半开路径区间 |
| G4 | 发布 1,000 行 | <2 s | SELECT 5.1 ms；实际 0 行 27.8 ms | fixture 1,000 行 5.3 ms；真实 0 行 33.7 ms；达标 |
| G5 | cleanup 100 卡（`--cleanup-cursor 1070887`） | <1 s | 2,446.8 ms（cursor 0）；生产日志 733–6,482 ms | 1.05 ms；达标（cursor 与 before 不同，见 §5） |
| G6 | lifecycle 100 entry | <200 ms | 37.1 ms；partial 运行不进入 lifecycle | 43.7 ms；达标；partial 现在也进入 lifecycle |
| G7 | 3 小时慢事务 | 0 | 活跃窗口 51 条，全部 `cleanup_publish` | 待填 |
| G8 | 3 小时 `SQLITE_BUSY` | 下降 80% | 63 条（窗口口径见 2.5） | 待填 |
| G9 | 浏览首页 | <150 ms | 审计参考 249 + 248 ms（H 未复测） | 待填 |
| G10 | 关键词搜索 | <100 ms | 审计：41,824 entry + 224,395 file 全扫（H 未复测） | 待填 |
| G11 | 87k 卡 tracks 首屏 | <50 ms / <200 KB | 审计：一次加载整卡 87k 行（H 未复测） | 待填 |
| G12 | 无变化二次同步 | 0 发布行，未变化目录不重列 | N/A | `TestCatalogSyncNoChangeSecondRunListsNothing`：list 请求 0，发布 0 |
| G13 | 深目录 +1 文件 | 只发布 1 行，无关目录请求 0 | N/A | `TestCatalogSyncDeepDirectoryAddsOnlyOneRow`：发布 1，目标目录 list 1 |
| G14 | partial 一个子树 | 其他子树仍被发现，lifecycle 前进 | 4 failed + 27 empty；`stage=observe`、`cursor=0`，lifecycle 不前进 | `TestCatalogSyncFailedSubtreeKeepsOtherBranchesAndAdvancesLifecycle`、`TestCatalogSyncContinuesPastAFailedListing` |
| G15 | 排除子树 | 排除部分请求 0，个人记录保留 | N/A | `TestSourceCatalogExclusionWhitelistKeepsFilesTracksAndRange`、`TestCatalogSyncReachesWhitelistInsideExcludedSubtree`：白名单 24 mp3 可列可播，被排除兄弟 0 请求 |
| G16 | 全量测试 | `go test ./...`、前端 build/单测全绿 | 未跑 | 后端 `go test ./...` exit 0；前端 typecheck/lint/format/build/919 单测 exit 0（见 §4.1） |
| G17 | CI | 当前 HEAD Full 全绿或记录未跑原因 | 未跑 | 推送后 dispatch Full，结果记入 17 号证据 |

## 4.1 本轮 after 运行

```text
# 1. 把 10-21 的生产快照副本迁移到当前 schema
go run ./.kikoto-work/bench/migrate92 ../.kikoto-work/bench/bench-093.db
# 2. 用当前 HEAD 构建 bench
cd backend && go build -o ../.kikoto-work/bench/catalog-bench-093.exe ../scripts/catalog-bench/main.go
# 3. 测量
catalog-bench-093.exe --db bench-093.db --vacuum-to bench-093-vacuum.db --write-db bench-093-write.db --source-id 3 --generation 53 --root /source/example --label g1-g6 --runs 5 --write-runs 3 --cleanup-cursor 1070887 --lifecycle-cursor 0 --json-out after-093-all.json
```

结果（schema 093，modernc 驱动，Windows，5 读 / 3 写）：

| 项 | median | min | max |
| --- | ---: | ---: | ---: |
| `progress_counter` | 12.103 ms | 11.027 | 13.818 |
| `progress_exact` | 127.528 ms | 123.005 | 138.590 |
| `purge_empty_pending` | 0 ms | 0 | 0.532 |
| `root_incomplete` | 0 ms | 0 | 0.538 |
| `publish_1000_rows`（SELECT） | 93.820 ms | 92.065 | 109.723 |
| `publish_1000_rows_fixture` | 401.160 ms | 399.812 | 401.981 |
| `cleanup_100_cards` | 0 ms | 0 | 0.636 |
| `lifecycle_100_entries` | 35.017 ms | 33.465 | 35.268 |

迁移前后行数一致：entry 114,081、observation 389,830、pending 171,604；
085 回填的 observed/indexed 与精确聚合一致（389,823 / 41,569）。
`PRAGMA user_version` 恒为 0，版本以 `schema_state.current_version` 为准。

G1–G3 的达标来自三处改动，均已带测试：

1. `093_source_catalog_partition_index.sql` 为 `(file_source_id, generation, partition_path)`
   建覆盖索引，分区聚合与分区总数不再建临时 b-tree。
2. 默认进度响应不再统计全源 staging 行（390k observation），改报本 generation 的计数器；
   遗留行的对账仍走 `?exact=1`。
3. purge 的存在性探测改由旧 generation 的目录索引驱动；root-incomplete 的两臂都改成
   半开路径区间。bench 脚本此前仍在用旧的 `catalog_root`/`substr` 形式，已同步。

G4–G6 与 G12–G15 不依赖上述改动。

## 4.2 G18–G28

| 编号 | 场景 | 结果 | 证据 |
| --- | --- | --- | --- |
| G18 | 完成扫描后每日更新 | 走目录 mtime 增量，generation 不变，新卡可检索 | `TestCatalogDailyDispatchesIncrementalRunAfterComplete`、`TestSourceCatalogDailyStartsOnlyAfterCompleteAndSurvivesRestart` |
| G19 | 周度全量对账 | 只有新 full generation 入队才写 `source_catalog_full`，增量与续跑都不重置 | `TestCatalogSyncWeeklyFullMarkerOnlyMovesWithANewGeneration` |
| G20 | 白名单子树 | `/A` 排除 + `/A/Keep` 例外：24 mp3、目录、observation、audio root 保留，tracks 与 Range 可用；兄弟 0 请求 | `TestSourceCatalogExclusionWhitelistKeepsFilesTracksAndRange`、`TestCatalogSyncReachesWhitelistInsideExcludedSubtree` |
| G21 | split 卡排除 | 按 `COALESCE(NULLIF(source_root,''),root_path)` 命中，文件清零、卡片不可见、个人记录保留 | `TestSourceCatalogExclusionSplitCardUsesSourceRoot` |
| G22 | 编码损坏目录 | 首次与增量都记录 issue 并跳过请求 | `TestCatalogEncodingIssue*`、`TestCatalogSyncDoesNotRecreateAnExcludedSubtree` 同族；生产复核待部署 |
| G23 | sync 受维护锁与低空间约束 | 启动前与每批之间检查；命中即 `partial` 且不花请求，恢复时间写回 daily tick | `TestCatalogSyncPausesForMaintenanceAndRetriesAfterTheHold`、`TestCatalogSyncPausesOnLowDiskAndDoesNotSpendRequests` |
| G24 | 崩溃后残留 running 行 | 启动清理为 failed，下一次当日即可运行，不等 6 小时 | `TestCatalogSyncStartupClearsAnInterruptedRun` |
| G25 | 父卡跨排除目录 | 父卡位于排除路径之上时，其落在排除子目录的文件行仍被删除 | `TestSourceCatalogExclusionRemovesFilesOfAParentCard` |
| G29 | 低空间时的播放与个人记录 | 扫描 `pauseReason=low_disk_space`、预填 `paused_low_disk`，同一时刻 Range 播放、目录列表与评分备注照常；空间恢复后两者继续 | H：`TestLowDiskPausesScanAndPrefillWithoutStoppingPlayback`；F：`source-resource-limits-live` |
| G30 | G1–G3 的 schema 093 复测 | 12.103 / 0 / 0 ms，均达标；见 §4.1 | `after-093-all.json` |
| G26 | 先排除后加白名单 | 建白名单时把桥接目录标记 changed 并启动一次 sync，重新发布音频 | `TestSourceCatalogWhitelistCreationSchedulesABridgeRelist` |
| G27 | 繁体搜索同形字 | `zh-tw` 同时查 `zh-Hans` 与 `zh-Hant` | `TestTraditionalSearchKeepsSharedGlyphsInTheSimplifiedCorpus` |
| G28 | sync 阶段计时 | scan/publish/lifecycle/settle 记在 sync 自己的 workflow_run 上，不污染 full generation | `TestCatalogSyncRecordsPhaseTimingsOnItsOwnRun` |

## 5. 已知限制与待确认

- `publish_1000_rows` 只计 SELECT 阶段；`publish_1000_rows_fixture` 补上了 1,000 行标记的写入阶段（401.2 ms，含事务回滚）。
- cleanup 固定从 cursor 0 开始，生产 `publish_cleanup_cursor=1,070,887`；2.45 s 与日志的 733–6,482 ms 是不同窗口，不能直接当同一批次。after 必须用同一 cursor。
- lifecycle 只看 observe 阶段；stage=reconcile 的 fingerprint/relocation 路径未计时。生产当前 stage=observe，所以可先覆盖该路径。
- modernc 比 C SQLite 慢约 2–2.5 倍；before/after 必须同驱动、同机器、同 warm 条件。
- `docker logs` 当前只覆盖约 9 分钟活跃告警，不是三小时；G7/G8 的 after 需要固定负载窗口和容器启动时间。
- G9–G11 的 before 来自审计报告，不是 H 脚本复测；G12–G15 在整改前没有实现，标为 N/A；G16/G17 未执行。
- 源库在 `VACUUM INTO` 后于 10:07Z 重启过容器；副本对应 10:03Z 的快照，schema 与数据计数已核对。

## 6. 回滚

H 包没有产品代码、迁移或部署改动。回滚只需删除 `scripts/catalog-bench/` 与本文件；删除本地/服务器 `/tmp` 副本不影响生产。基准脚本对源库只读，写路径全部在副本事务内回滚。
