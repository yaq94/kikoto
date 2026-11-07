# 25：最终审查发现的恢复与查询成本修复

审查基点为 `53d905e1d212e85d78cb95687044b96b4fc3ebae`，被审候选为 `0deecb1da2c686dc4776ca0c7ae9b7abae99868c`。本文件对应其后的本次代码提交。旧候选未通过完整门禁，不是发布验收结果。

Standards 独立审查发现旧任务清理依赖遗漏、刷新锁等待、有限扫描文档过期，以及两个查询成本风险。Spec 子代理未执行；主代理按规格 D-05、D-10、AC-21/23/25 复现并接手修复。完整独立报告在仓库外 `review/20261108-final-standards.md`。

## 行为修复与证据

原始日志在仓库外 `reports/20261107-remediation/`。测试均为 Go 1.26.10、真实迁移 SQLite 和合成数据，不访问真实来源或媒体。

| 场景 | 失败证据 | 修复后结果 |
| --- | --- | --- |
| 两来源的排除任务，旧来源失败或取消后运行旧任务清理 | `root-cleanup-recovery-red.log`：两种状态的重试均 404 | 保留仍由 publication 引用的 run/job；`root-cleanup-recovery-green.log` exit 0，2.501s，两种状态均可续跑并保留未排除音轨 |
| 一个来源缓慢响应时刷新另一个来源与重复刷新 | `root-refresh-queue-red.log`：被全局锁阻塞 | 三个来源槽位、同来源排他、无等待队列；忙时返回可重试 409。`root-refresh-queue-distinct-green.log` exit 0，4.507s；后续加入 finite 入口同源冲突断言，见 `root-nov8-boundaries.log` |
| 翻译逐项插入解析发布对象 | `root-publication-lookup-red.log`：相关查询 `SCAN live` / `SCAN e`；完整 backend coverage 在 50,001 条插入用例超时 | 109 保持 UNION 两臂整数 affinity，查询可按实体主键定位。`root-50k-lookup-green.log` exit 0，用例 8.31s，原始数量与搜索断言未变 |
| 无码格式比较中名称未匹配后的尺寸范围 | `root-duplicate-range-red.log`：只按 comparison 扫全部候选 | 109 的尺寸前导索引先缩小范围，仍按原名称顺序消费候选；`root-nov8-fixes-targeted.log` exit 0，包含查询计划保护 |
| 空系列处于 preparing 或已提交时手动清理 | preparing 正确保留；`root-nov8-boundaries.log` 中提交后删除返回 500/FK | 显式清理先解除无个人引用的系列作品登记，保留来源系列；有单集父级引用则跳过。`root-nov8-retention-green.log` exit 0，包括派生读取、系列父级和既有清理回归 |

刷新测试初次把两来源配置成同一 origin，被既有出站 origin gate 限制；改成两个独立合成 HTTP 服务后验证跨来源隔离，不放宽超时或状态断言。

## 规模与算法

译文插入临时数据库测量：1,000/2,000/4,000 条分别为修正前约 934/3,789/15,365ms，显式整数 affinity 后约 31/50/98ms，见 `root-insert-cost-red.log`、`root-insert-cost-cast.log`。这是本机观察，最终目标机预算另验。

删除 observation 采用完整 listing 的一次文件 keyset pass，每批最多检查 64 行并保存 `fileAfter`；旧格式 remove cursor 安全从头续扫。所有缺失 observation 共用这次 pass，保留来源 override 与目录边界。对账仍读取文件集合一次，未声称每个修改目录只访问受影响文件。随后删除 observation 的独立事务仍是线性数量。

固定 4,096 个保留文件，移除 64/256/1,024 个文件的本机耗时约从 1,640/1,597/5,437ms 降为 99/322/1,432ms；见 `root-removed-cost-before.log`、`root-removed-cost-after.log`。后者 exit 0，包含真实关闭后页恢复、删除批次故障检查点和最终保留集合。尺寸索引缩小候选范围，密集同尺寸分布仍需检查匹配范围，未宣称任意分布最坏线性。

## 迁移、门禁与剩余验收

新增 `109_catalog_lookup_affinity.sql`，正式生成 `compat/109_dev.sql`，不改 001–108。当前 migration test 头同步为 109。109 故障注入、事务回滚、重试和重复启动通过；production/development baseline 与完整编号链一致，见 `root-nov8-boundaries.log`、`root-nov8-fixes-targeted.log`。后者 exit 0：HTTP 4.592s、storage 2.610s。

`root-nov8-static-final.log`：format/lint exit 0，0 issues。初次 lint 下载失败及后续测试 defer 错误均保留历史输出，未计为通过。

旧候选 GitHub Full `42416098017` 的 content-translation 分组有两项浏览器失败（缓存再次访问请求计数、延迟版本路由生命周期）；仍待修复和当前候选重跑。完整同 SHA CI、目标机组合性能、真实模型与媒体、实际恢复演练及部署验收未完成，本票保持 in-progress。
