# 09 每日来源更新验收

Implementation: verified-local

当前实现基于集成分支 `codex/kikoto-catalog-remediation`。本记录随本票提交保存。
本票复用 070 和 `app_setting`，没有新迁移或依赖。

## 行为与验收对应

- AC-22：首次扫描到达树尾后启用每日调度。只有未确认空范围的 partial 会安排次日
  重观察，并恢复同一 generation，不创建独立全站任务；失败/受限范围按同一调度重试。
  低空间或磁盘余量未知时暂停并在一小时后重试同一 generation，不把暂停冒充完成。
  有限扫描和未启动来源不启用。关闭期间错过时间仅补一次。
- AC-21：每日和手动共用一个入队事务，复用 queued/running，恢复 cancelled/paused/
  failed/partial 的相同 generation。入队和下一次时间原子提交；停止后队列仍在 SQLite。
- AC-25：定时任务沿用已存选项及原扫描执行器；默认 3 并发、间隔 220ms，低空间暂停。
  暂停跨真实 HTTP 应用与数据库关闭重开后，手动/到期重试均复用原 run，已存卡片和评价可读。
- 真实 `StartJobRunner` 调度和 worker、真实 Routes/会话、迁移 SQLite、本地 OpenList
  边界服务共同运行；控制时钟和磁盘余量，不模拟队列、鉴权或业务状态。观察新增条目可搜索，
  完整前状态不是 complete，取消后定时恢复不更换 run。普通用户不能读取或启动管理扫描。
- 来源 id 可在删除后复用。新来源第一次手动索引会清除该 id 遗留的旧调度，不能继承旧源
  的完整扫描资格。没有凭来源路径、译文或时间推断身份。

## 红绿与验证

Go 1.26.9，`GOTOOLCHAIN=local`；Node 24.19.0。Go 定向命令均在 `backend/`。

| 命令 / 检查 | 退出码与证据 |
| --- | --- |
| `go test -mod=readonly ./internal/httpapi -run '^TestSourceCatalogDailyStartsOnlyAfterCompleteAndSurvivesRestart$' -count=1 -v -timeout=1m`，接入调度前 | 1；真实重启后仍为原 run 1/complete，未触发次日更新；接入后同用例退出 0。 |
| `go test -mod=readonly ./internal/httpapi -run '^TestSourceCatalogDailyReobservesAFinishedUnconfirmedEmptyGeneration$' -count=1 -v -timeout=1m`，旧源调度清除前 | 1；复用 id 的新来源部分扫描被错误自动重试；清除后定向总回归覆盖。 |
| `go test -mod=readonly ./internal/httpapi -run '^TestSourceCatalog(Daily\|Index)' -count=1 -v -timeout=3m` | 退出 0；16 个顶层 H/R 通过，35.478 秒。 |
| `go test -race -mod=readonly ./internal/httpapi -run '^TestSourceCatalogDaily' -count=1 -v -timeout=3m` | 退出 0；3 项通过，无 race，30.184 秒。 |
| `make backend-test` | 退出 0；完整后端通过，httpapi 150.494 秒。此轮后补充旧源 id 调度清理，并由最终定向回归/静态检查覆盖该改动。 |
| `make backend-format backend-vet backend-lint backend-build` | 退出 0；0 lint issues，格式/vet/构建通过。lint 使用已授权进程级代理供工具模块查询。 |
| `node --test scripts/check-doc-links.test.mjs`、`node scripts/check-doc-links.mjs`、`node scripts/check-doc-locales.mjs` | 均退出 0；1 项文档测试、123 份公开 Markdown、5 个语言版本和 11 个用户页面。 |

第一次新增取消用例在未读取合成请求体前阻塞，导致服务端未观察取消；已修复夹具先读请求，
并设置有界等待，随后默认预算与取消恢复通过。本次仅终止该自建测试 job，没有清理他人进程。
原始日志位于仓库外 FastCtx job 存储，不提交运行数据、个人数据库或真实端点。

## 交界与限制

生产改动为 `source_catalog_daily.go`、`job_runner.go` 的协调器调用、`server.go` 的
时钟边界、`source_catalog_index.go` 的事务内复用入口，以及 `source_catalog_index_worker.go`
成功结算时原子保存下一次更新时间。没有修改 11 的 roots、publish 或系列模块。
09 未修改前端交互，沿用已有索引管理入口；无需新增 F，未采集截图。
07 的真实媒体 LIVE、08 的真实全来源覆盖及发布前置继续保留；本票未部署或访问真实站点。

## 审计后修复（2026-10-23）

- 扫描与增量同步互斥：手动全量在增量 sync 运行时返回 409
  `catalog_sync_running`；增量 sync 在全量 queued/running 时拒绝入队，避免重叠全站任务。
  `TestManualFullIndexRefusesWhileIncrementalSyncRuns`、
  `TestIncrementalSyncRefusesWhileFullGenerationRuns` 覆盖双方。
- 低空间和磁盘余量未知统一为暂停后一小时重试同一 generation；全量与增量同步使用
  同一规则，不再永久等待运维。`TestSourceCatalogDailyRetriesALowDiskPauseAfterOneHour`
  和 `TestCatalogSyncPausesOnLowDiskAndDoesNotSpendRequests` 覆盖到期重试与不消耗请求。
- 到达树尾、仅剩未确认空的 run 会安排次日同代重观察；
  `TestSourceCatalogDailyReobservesAFinishedUnconfirmedEmptyGeneration` 覆盖。
- 交付提交：`802dfda0`（`fix(catalog): serialize scans and retry low-disk pauses`）。
  定向复验：`go test ./internal/httpapi -run
  'TestSourceCatalogDaily|TestCatalogSync|TestManualFullIndex|TestIncrementalSyncRefuses'
  -count=1` 退出 0。
