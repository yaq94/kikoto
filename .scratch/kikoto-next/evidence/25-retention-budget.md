# 25：翻译缓存维护预算

本批基于 `ebe174bcf8908706cf60805f7b5fa0a4ac7941ab`，修复缓存维护的十秒预算失败。保留 12,000 行、1 MiB、每批 500 行、每阶段 20 批及原连接池读取断言；不增加超时或重试，不改迁移、当前搜索语料保护或个人记录。

## 定位与实现

原始 `TestContentTranslationRetentionMeasuresCachePages` 在完整 CI 与本地 race 均因维护 context 到期失败。重复整表计数与页测量、两表合并排序，以及空内容表仍编译完整发布语料查询造成重复成本。页数临近预算时，小量删除还可能消耗剩余批次数却没有释放 B-tree 页。

- 一次测得的超额量按原 500 行上限逐事务消耗，再测量；不是每个删除批次重新扫描统计。
- 两张表分别按更新时间和 id 取最多一批，合并后按原顺序选择全局最旧的一批。
- 同一写事务先检查内容表是否为空；为空时只查询来源字段缓存。检查与删除在同一事务中，有内容时仍使用完整当前语料保护条件。
- 仅实测页预算超限时，删除量至少为一个原有大小的批次；行数及逻辑字节限制保留原计算口径。
- 保留原有连接池获取及有界写事务，不引入跨事务 prepared statement 所有权。

## 验证

日志均在仓库外 `reports/20261107-remediation/`。Go 为 `go1.26.10`。

| 边界 | 命令／结果 | 日志 |
| --- | --- | --- |
| 原失败 | 原 12,000 行测试 race，退出 1，`context deadline exceeded` | `retention-budget-red.log`、`retention-budget-measure-plan.log` |
| 最终预算与并发读取 | `go test -race ./internal/httpapi -run '^(TestContentTranslationRetentionMeasuresCachePages\|TestContentTranslationWriteContention)$' -count=1 -timeout=20m -v`，退出 0；实际维护 5.547 秒，原 1 MiB、非空缓存和 400ms 读取门槛全部保留；总 73.470 秒包含迁移初始化 | `retention-bounded-pool-race.log` |
| 语料、生命周期与限制 | 17 个顶层普通回归，退出 0，总 60.818 秒：当前／原生语料、配置和文本变化、源缓存、写竞争与超时、维护取消、下一 tick、通知合并均通过 | `retention-final-behavior.log` |
| 单连接与积压 | 新增 10,051 行缓存跨两次维护，无新写入仍继续；500 行事务的第二次删除注入故障后整批回滚，单连接可读并继续完成。最终普通回归退出 0；最终并发 race 另列，完整候选的 race 仍待 CI | `retention-final-behavior.log` |
| 静态检查 | `make backend-format backend-vet backend-build backend-lint`，退出 0，lint 0 issues | `retention-final-checks.log` |

两项新增生命周期用例属于恢复与边界验证，首次运行即通过，不把它们称作缺陷红测。早期关于积压不能继续的假设已由 revision 机制与实际第二轮清理证伪，没有修改调度。跨事务预编译试验因并发读取回归失败被撤回，最终并发用例保持原实现入口与断言。

## 发布范围

本文件只记录这项改动的定向证据。完整同候选 CI、双轴审查、目标机性能和最终部署验收由 17／24 记录，本票继续进行。原始日志和采样可执行文件位于仓库外，未作为产物提交。
