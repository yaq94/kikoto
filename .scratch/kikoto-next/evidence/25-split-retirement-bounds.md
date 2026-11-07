# 25：旧拆分卡退役资源边界

本批基于 `e9b1cd38b36a99649a002834c5e5453613cd4c9e`，差异限于旧拆分卡退役、发布前准备、恢复游标及迁移 110。采用 implement-spec / TDD；测试入口沿用已确认的 H（真实 HTTP、鉴权、迁移 SQLite）、R（关闭 Server 与数据库后重新打开）及 SQLite 写入观测。无 UI 变更，不执行视觉验收。

## 复现与实现

- 完整 HTTP 索引生成单根 1,103 张独立音声卡，再完整索引为一张卡。原实现最终可见集合为 1，但一次事务写入 1,104 个 entry、1 个 file，包含 FTS 和触发器的总行写入峰值为 36,866。`split-retirement-red.log` 退出 1，失败断言为退役事务最多 100 个 entry；不是功能集合缩减或超时测试。
- 发布批次在 `source_catalog_split_retirement` 记录 operation、root、generation 和 `after_entry_id`，不再构造整根 keep map 或旧卡 id 切片。每个准备事务以索引和 id 游标消费最多 64 个实际卡输入，包含当前代、受保护与不能退役的卡。其入站 alias 和 file 清理由既有 publication entity 队列处理，发布 marker 等待全部准备完成。
- orphan pass 以实际卡而非根为输入，每事务最多 100 张；卡游标和对应变更同事务提交。完整索引在生命周期处理前准备已排队的根，使旧文件仍可提供 replacement 证据。已有 work、series 成员及无法唯一确定 replacement 的入站 alias 保护规则保持。
- 单纯恢复发布操作保留 orphan 游标；只有实际重新观察 failed、restricted、empty 或变更分页的未完成目录时才重置。恢复红测记录 `before=100 after=0`，修复后恢复保留 100；另有失败根重新观察为无音频目录后退役全部旧卡的 H 回归。
- 110 仅增加根检查点表和 orphan 的 `(file_source_id,id)` 部分索引；001–109 未修改。`go generate ./migrations` 生成 `compat/110_dev.sql`。迁移故障完整回滚至 109，重试至 110 保留稳定 id 和已有 publication，再次迁移保留 checkpoint=64；production/development 基线均与完整编号链一致。

## 当前产物证据

原始日志位于工作区外 `reports/20261107-remediation/`，以下均为相对文件名。Go 使用 `GOTOOLCHAIN=go1.26.10`。

| 检查 | 命令 / 结果 | 日志 |
| --- | --- | --- |
| 大根及恢复 H/R | `go test ./internal/httpapi -run '^TestCatalogSplitRetirement' -count=1 -v`；退出 0 | `split-retirement-final-green2.log` |
| 有旧代卡的增量恢复 | `go test ./internal/httpapi -run '^TestCatalogSplitRetirementAfterIncrementalUpdate$' -count=1 -v`；退出 0 | `split-retirement-sync-stale.log` |
| 相关 split/retire/publication/个人记录 | `go test ./internal/httpapi -run 'Test(SourceCatalog.*(Split\|Orphan\|Retir\|Format\|Linked\|Publish)\|Catalog(Retir\|Publication)\|Personal.*(Retir\|Series))' -count=1 -v`；退出 0，95 个顶层测试通过；真实数据库尾部性能 opt-in 测试 1 个 skip | `split-retirement-related-final.log` |
| 110 升级/回滚/基线 | `go test ./internal/storage -run 'TestCatalogSplitRetirementMigration110\|TestMigrateBaselineMatchesCompleteIncrementalChain' -count=1 -v`；退出 0 | `split-retirement-migration.log` |
| race 主体 | `go test -race -timeout=20m ./internal/httpapi -run 'TestCatalogSplitRetirement\|TestSourceCatalog(StaleSplitCard\|OrphanPass\|RetiresOrphan\|PartialRunRetiresOrphan\|SplitSingleSplit)\|TestCatalogPublicationRetirement\|TestCatalogRetirement' -count=1 -v`；退出 0，包含 1,103 张规模与三个 R 中断恢复 | `split-retirement-race.log` |
| 有旧代卡的增量恢复 race | `go test -race -timeout=20m ./internal/httpapi -run '^TestCatalogSplitRetirementAfterIncrementalUpdate$' -count=1 -v`；退出 0，退役峰值 64 | `split-retirement-sync-stale-race.log` |
| 静态检查 | `make backend-format backend-vet backend-build backend-lint`；退出 0，lint 为 0 issues | `split-retirement-static-final.log` |
| 提交前检查 | `make backend-format sensitive-check tracked-evidence-check`；退出 0，13 个变更文件及 86 个证据文件扫描通过 | `split-retirement-precommit.log` |

大根绿测单事务 entry/file 峰值为 64/0，含触发器及 FTS 总写峰值为 2,410；最终仍完整可见 1 张卡。原始总行写入 81,460、3,639 个事务；修复后为 81,516、3,659 个事务。行写入统计不等于 B-tree 页、内存峰值或写锁持有时间，不据此声称目标机墙钟达标。

三个恢复样例各从真实索引的 133 张卡开始，保留一张 HTTP 评分卡。在事务故障、低空间、取消分别发生后，均保留 operation=2、cursor=64，发布集合仍为 133；实际关闭 Server/DB 后由同一 run/operation 恢复，最后退役 132 张、可见 2 张（新卡及个人评分卡），评分与备注保持。132 不是 64 的整倍数，最后一批被完整消费。

增量更新的额外测量区分两种状态：同 generation 的 133→1 更新通过原有文件/可用性路径完成，退役行数为 0，既有生命周期事务写入 200 个 entry；这不是本批退役队列的写入。下一代完整索引失败后由增量更新恢复的样例确实退役旧 generation 卡，退役峰值为 64，总 entry/file 峰值为 100，最终可见集合为 1。后者由 `split-retirement-sync-stale.log` 证明。

## 范围限制

本批不替代最终同候选完整 CI、目标机组合资源测试、真实来源与部署验收；不调用真实外部来源或模型，不操作生产与凭据。相关回归及 race 均已读取终态，所有本子任务启动的后台 job 已退出。真实数据库尾部性能 opt-in 测试缺少指定输入而 skip，不记为通过；内存峰值与目标机写锁时间由最终组合验收负责。
