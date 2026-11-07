# 25：第三批有界算法整改

## 版本与边界

本批基点为 `edab56b42a9df62b4eae450cfd2c4eb66c24f0f0`。独立行为提交为 `b32fd4c8`（staging）、`f85439ad`（基准错误传播）、`f0fc499c`（probe 检查点）。Windows amd64、Go 1.27.3；使用迁移 SQLite、loopback 合成来源和 Python SQLite 3.47.2 内存测量。无生产、真实来源、模型或凭据访问；无 push。SQLite 仍为 105，无新增 DDL。

原始日志、故障注入 overlay 与测量脚本均置于仓库外 `reports/20261107-remediation/`。以下文件名相对此目录。这里的本地耗时不能当作目标机延迟验收。

## staging

消费完 `[start:end]` 后令 `start=end`；每事务原有 100 次 entry/file 写入限制保持。真实扫描 HTTP 测试的 SQLite pre-update/commit hook 统计实际文件写入及含文件写入的事务，覆盖总工作量。

| 101 个单音频 episode | 文件 staging 写入 | staging 事务 | HTTP 扫描耗时 |
| --- | --- | --- | --- |
| 修前 | 3,825 | 101 | 5.646 s |
| 修后 | 101 | 3 | 0.324 s |

`staging-work-red.log` exit 1，原结果虽然完整且单事务上限正确，总工作量断言失败。`staging-work-green.log` exit 0，39.677 s；包含阶段失败、激活失败、补偿、父级保护和恢复等既有回归。最大事务仍为 50 个 entry 加 50 个 file。race 模式的同一规模耗时 6.355 s，仅报告 instrumentation 下的实测，不与未启用 race 的时间比较。

## probe

查询增加 `d.path > after`，使用既有 `(file_source_id,path)` 主键范围。每批实际结果和 workflow job 的 `probeGeneration/probeAfter` 在同一事务写入；内存变量只在提交成功后推进。继续同一 syncRunId 时从既有 job 检查点恢复；新调度 run 保持重新验证全部范围的原语义，未把 startup 清理改成自动重启旧任务。结束前检查尚未验证的较小路径并回绕，已提交的 `last_verified_run` 避免重复请求。

- `probe-checkpoint-red.log`：原实现没有持久进度，断言失败，exit 1。
- `probe-checkpoint-green.log`：目录差异、失败分支、每日/每周调度、hold、低空间、代际不变、startup 清理等组合回归通过，exit 0，19.782 s。
- `probe-resume-green.log`：第二批数据库写失败和请求中途取消时，只提交前 32 个 probe；关闭 Server 与数据库、重新打开后续跑同 run，前 31 个子目录不重请求、后续 9 个全部请求，exit 0，18.535 s。
- `probe-smaller-path-valid.log`：检查点 `/Z` 后新增较小 `/A`，其根 observation 已存在；继续运行仍探测 `/A` 并完整列出新孩子，`/Z` 不重探，exit 0。初次 `probe-smaller-path.log` 使用了不存在的夹具列，修正后才取得有效结果。

从本批实际函数提取 SQL，在相同内存合成集合比较去掉/保留 path 边界；所有 8 次运行均处理完整集合。`catalog-probes-after.jsonl`、`measure-catalog-probes-after.py` exit 0。计划从 `file_source_id=?` 变为 `file_source_id=? AND path>?`。

| 目录数 | 无 keyset 累计 SELECT | 当前累计 SELECT | 无 keyset VM 步数（约） | 当前 VM 步数（约） |
| --- | --- | --- | --- | --- |
| 2,000 | 10.955 ms | 5.456 ms | 765,000 | 185,000 |
| 8,000 | 117.159 ms | 21.589 ms | 9,775,000 | 740,000 |
| 16,000 | 430.516 ms | 47.287 ms | 37,551,000 | 1,480,000 |
| 32,000 | 1,492.892 ms | 95.829 ms | 147,102,000 | 2,961,000 |

规模脚本只计 SQL 遍历，不包含 HTTP、job 检查点写入或生命周期；恢复正确性由独立应用测试验证。此前资源基线原文保留在 [25 资源基线](25-resource-baseline.md)。

## translation-bench

查询失败存入独立 `error`，不会被后续 EXPLAIN 覆盖；已完成样本保留，失败结果不输出 latency percentiles。各查询错误汇总后返回主函数，JSON 仍写入，进程退出非零且不打印该组的成功延迟摘要。编号采用 `HighCardinalityWorkCodeAt`，超过其 400,000 entity 范围明确拒绝。浏览器缓存 incidental 编号在第二批已经改成保留编号；只测试 key 序列化的 `RJ1` 字面例外保留，本批未无因重改。

- `bench-errors-red.log`：真实缺表 SQL 在 warmup 和测量中途失败后被显示为 `p95Ms:0`，EXPLAIN 抹掉错误，exit 1。
- `bench-errors-final.log`：真实错误保留、部分成功样本保留、两项独立错误都向报告传播，exit 0。
- `bench-cli-success-400.log`：实际命令 `go run ./cmd/translation-bench --rows 400 --runs 2 --json-out <report>` exit 0，四语言各查询保留 2 个样本。
- `bench-cli-failure-400.log`：Go overlay 仅将 exact-code SQL 的表改成合成缺表；实际命令 exit 1，JSON 中 exactCode 有缺表错误且无百分位，其余四语言成功样本仍各 2 个；stdout 无伪成功摘要。
- 最初 40 行试运行触及原 sampleTitle offset 的小语料边界，返回 no rows，未作成功证据；本批使用 400 行代表性小规模验证，没有为此改变采样算法。

## 验证与剩余范围

`algorithms-race.log`：四个定向顶层场景及恢复子例通过，exit 0，54.444 s。`make backend-format backend-build` 和 `go vet ./internal/httpapi ./cmd/translation-bench` 均 exit 0，分别见 `algorithms-build-format.log`、`algorithms-vet.log`。每个行为提交前 sensitive-check 通过。交付前合并当前 integration，返回 Already up to date，基点仍为上述 edab56b4；未重复输入不变的全量检查。

25 仍 `in-progress`。整目录分页持久化/单事务、完成语料覆盖空转、写后 retention 全扫仍待下一批。主代理新复审的重复读者取消所有权、已完成 waiter 清理、IDB transaction rejection 和迟到连接关闭问题仅登记待办，本批未修改。全量候选 CI、发布账本和生产验收由主代理负责。
