# 25：race 分片与真实 schema 夹具成本

本证据对应同提交的 CI 矩阵、分片测试与翻译测试夹具。原始日志位于仓库外 `reports/20261107-remediation/`。

`f33e51be` Full CI run `42498479864` 的三个 race 分片耗尽原 35 分钟整包预算。其中两个结束时正在执行仅运行 2–3 秒的普通用例；另一个结束时正在执行 1,000 目录的完整集合契约。缓存写竞争另有真实失败，由[淘汰候选修复](25-retention-selection.md)处理，不能归为单纯分片问题。

普通翻译存储测试复用仓库现有的真实迁移 schema 镜像，每项仍复制到独立 SQLite 文件、使用生产连接池、WAL 与外键。专门的 schema 创建测试仍从空库实际迁移；所有既有升级、重启和恢复测试保持。`retention-bounded-candidates-race.log` 中普通存储用例耗时 1.4–7 秒，独立迁移测试 23.80 秒；数据库隔离检查也通过。

CI 沿用同一分片算法，分配到八个 runner。每个分片仍为 35 分钟 Go 包预算、45 分钟 job 预算；测试内容、规模、断言、单项性能门槛和 retry 均未改变。代价是增加四个 runner 的初始化与编译开销；完整测试集可并行完成。

- 当前 `go test -json -list '^(Test|Example|Fuzz)' ./...` 退出 0，清单保存为 `nov9-test-list.jsonl`。使用实际清单逐一核对八条 `-run` 正则，2,014 个“包＋测试”组合恰好运行一次，无遗漏、无重复；各片 251–253 个组合。
- `make go-test-shard-test actionlint` 退出 0，分片规则四项测试通过，包含八片分区断言；日志 `nov9-candidate-static-final.log`。
- `make backend-format backend-vet backend-build backend-lint` 分段完成，lint 最终 0 issues；完整命令与失败修正见[淘汰候选证据](25-retention-selection.md)。

本地分区检查不是八个 CI 分片的运行结果。最终候选仍须触发 Full 并读取所有 job 终态。
