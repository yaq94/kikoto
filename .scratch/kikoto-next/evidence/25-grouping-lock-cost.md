# 25：分组查询与写锁成本

本批从 `ebe174bcf8908706cf60805f7b5fa0a4ac7941ab` 开始，保留原分组规则、64 行输入批次、2,000／8,000／32,000 项样本及 500ms 写锁门槛。新增迁移 111；001–110 未修改。

## 原因与改动

CI 原始 32,000 项回归在 `folders` 阶段持有写锁 784.314ms。查询计划显示 observation 和 listing 都按名称范围读取后再判断类型；纯音频目录确认无子目录也会扫描所有音频行。四项新查询计划断言在原 schema 上全部失败，见仓库外 `reports/20261107-remediation/grouping-range-red.log`。

111 为 observation 的目录、revision、kind、name 和 listing 的目录、kind、name 提供有序索引。observation 的旧索引由其覆盖前缀相同的新索引替换，不保留重复索引。开发快照为 `compat/111_dev.sql`。

名称、fold 和 target 批次在同一事务内复用写语句，关闭语句后再提交或回滚。没有跨事务语句缓存，也没有新增连接所有权。分类输入、检查点、恢复和完整集合不变。

## 验证

使用 `GOTOOLCHAIN=go1.26.10`；以下日志均在仓库外 `reports/20261107-remediation/`。

| 检查 | 命令／结果 | 日志 |
| --- | --- | --- |
| 有序范围 | `TestCatalogGroupingReadsKindOrderedRanges`，observation/listing × audio/folder 四项红转绿 | `grouping-range-red.log`、`grouping-related-green.log` |
| 行为与恢复 | 名称证据、单前缀聚合、旧版本清理、实际重启后恢复和范围计划 5 个顶层测试，退出 0 | `grouping-statements-behavior.log` |
| 迁移故障与幂等 | `TestCatalogGroupingKindMigration111UpgradeAndRollback`：SQL 故障保留 schema110 和旧索引；恢复后到111，原 observation 保留，重复启动幂等，完整性通过；退出 0 | `grouping-111-rollback.log` |
| 双模式迁移一致性 | `TestMigrateBaselineMatchesCompleteIncrementalChain`，生产与开发两模式均通过，退出 0 | `grouping-migration-final.log` |
| 完整规模 race | `go test -race ./internal/httpapi -run '^TestCatalogGroupingResourceBudget$' -count=1 -timeout=20m -v`，退出 0，三个原规模全部通过，总 303.928 秒 | `grouping-statements-resource-race.log` |

最终 race 数值：

| 输入 | 准备时间 | 写锁 p95 | 最长写锁 |
| --- | --- | --- | --- |
| 2,000 | 6.576s | 122.341ms | 136.789ms |
| 8,000 | 29.447s | 138.095ms | 175.089ms |
| 32,000 | 115.501s | 140.831ms | 188.640ms |

32,000 项全部分类并产生32,000个目标；重复读取640次不重新分类，堆增量约10.1 MiB，1,510个事务，实际写入总129,513行，单事务峰129行（含检查点等），输入批次仍为64。保留完整测试中的旧算法对照，不将其时间算入当前算法准备时间。

## 发布范围

`make backend-format backend-vet backend-build backend-lint sensitive-check tracked-evidence-check` 和文档链接检查退出0，lint为0 issues，见 `grouping-final-checks.log`。独立运行原并发启动迁移 race 退出0（21.76秒），见 `migration-concurrent-isolated-race.log`；该结果不能解释共享CI负载下此前的30秒锁等待失败。

本批不关闭25或最终候选。仍需同候选完整 CI、并发迁移负载核查、目标机规模复核、111恢复演练及实际部署验收；这些后续结果由集成记录与17／24汇总。
