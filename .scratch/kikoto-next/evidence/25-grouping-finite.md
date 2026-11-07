# 25：持久分组与 finite 输入证据

本批基于 `933cbf2c`，只交付目录分组、duplicate 比较与 finite 输入边界。
107 publication、译文重试和媒体修复是继承代码；本批新增迁移
`108_catalog_grouping.sql`，compat108 由 `go generate ./migrations` 生成。
105、106、107 均不在本批改动中。架构决定见
[ADR-0009](../../../docs/decisions/ADR-0009-catalog-grouping-checkpoints.md)。
本证据不关闭107或工单25，不代替集成、完整CI与部署验收。

## 实现与合同映射

| 条款 | 实现与当前证据 |
| --- | --- |
| 大根不反复物化 names/group maps | `catalog_grouping_index.go` 保存完整revision的证据，`catalog_grouping_buckets.go` 每批64个输入聚合，`catalog_grouping_plan.go` 按键读取target；全局序列使用固定bitset。 |
| 不能只限制输出group数量 | 单prefix含1025文件的红测原来一次只报告1个聚合输出；修后17批、每批最多64输入。日志 `sync108-bucket-red.log` 与 `sync108-bucket-unicode-green.log`。 |
| 父集合与副本比较不重复扫已消费前缀 | 父保留决策保存(path,name)游标；duplicate保存一对一match和待消费candidate，匹配后删除candidate；comparison计数与游标同事务。1025同hash完整匹配、重试不增加匹配；峰129表行。 |
| Unicode语义 | 祖先匹配用完整Unicode SimpleFold cycle；立即父级保留原ToLower规则。Σ/ς与K/K红测原来132音频，修后130。 |
| 完整合法集合 | full index完整发布5101独立作品；finite完整发布2101独立作品。5101红测原来把creator根当单card拒绝，修后只对实际card应用准入。 |
| episode、格式副本、个人稳定身份 | 601单集/1202文件及嵌套2单集/804文件通过；既有series、格式副本、review、alias、个人记录回归通过。未新增作品身份或复制个人记录。 |
| finite流式输入 | 实际HTTP入口使用持久directory queue、listing page和partition/apply cursor；不调用返回整树slice的ScanRoot。每页100，最多100请求、32目录、深度8。最多500输入的兼容分组保持原episode补偿路径。 |
| 页面与cursor原子 | 第2页失败保留第1页100行/page2，恢复不重取第1页；重复页、total变化、提前empty与未知total均保留旧发布文件。 |
| 取消、低空间、关库重启 | partition写入64行后分别触发low-space与cancel；旧列表仍空。关闭Server与SQLite后重开，恢复同listing/op，最终521独立card完整。selected card marker失败后真实重启，521兄弟文件完整。 |
| 多根独立结果 | 一个根失败仍保留旧card，另一根完整发布521，最终522。 |
| 清理 | 12个真实prepared历史revision保留当前1个；删除2541行、77事务、峰64行。finite派生队列在header回收前分批清理。 |
| 迁移 | 107→108失败事务回滚后仍107、无stage表；重试到108、稳定entry不丢、不复制全集、integrity通过。新库/生产及development baseline链由现有migration回归验证。 |

## 资源测量

使用真实迁移SQLite、pre-update/commit hooks统计表行写入。索引B-tree页写量
不属于这个计数；FTS shadow表行属于计数。HeapAlloc每5ms采样，非进程RSS。
日志均在仓库外 remediation reports 目录，仅记录合成对象。

`sync108-resource-delivery.log` 在最终父游标SQL与candidate表结构上以同样输入运行3次：

| names | classify/sequence/target（各） | 重复plan | 总表行写 / 事务 / 峰值 | HeapAlloc增量范围 |
| --- | --- | --- | --- | --- |
| 2000 | 2000 | 40 | 8106 / 103 / 129 | 4.78–5.18 MB |
| 8000 | 8000 | 160 | 32388 / 385 / 129 | 5.00–5.46 MB |
| 32000 | 32000 | 640 | 129513 / 1510 / 129 | 5.66–7.14 MB |

峰值事务为64 grouping_name +64 grouping_target +1 header；不把64输入称总写量。
同规模旧plan重建：2000名字×40次=80000完整名字输入、3.68–4.52秒；
持久plan不会再次分类。target查询为主键查找，bucket使用包含位置的覆盖索引
和三列keyset，父保留检查使用目录及name游标。

原1.027秒异常已细分：`sync108-resource-lock-unicode-red.log` 捕获Commit墙钟
1.6758秒，其间独立连接在9.32毫秒内取得BEGIN IMMEDIATE写锁；另一样本
1.343秒对应7.94毫秒。耗时集中在周期性Commit尾部，GC暂停不足以解释，
符合WAL自动checkpoint在释放writer后的工作。没有提高门槛或改变SQLite
durability设置。三轮32000输入writer锁上界max为15.54/44.37/19.70毫秒，
p95为7.45/7.20/7.36毫秒；probe按1ms轮询，属于保守上界，非内核精确计时。
测试仍检查500ms writer上界，保留原慢日志。

duplicate SQL独立复现实验（非整个应用耗时）：2000/8000同hash输入的已消费
前缀查询分别约26,061,000/416,244,000 VM steps；删除candidate后为
66,000/264,000。脚本及输出为 `sync108-duplicate-vm.py/.jsonl`。
真实1025比较为11375表行写、151事务、峰129行，完整匹配1025。

finite真实触发器放大：601单集/1202文件两次为61662/62170表行写、1524事务、峰1067；
嵌套2单集/804文件为17578表行写、130事务、峰1003。
两者峰值都包含64 source_catalog_file、64 before-image、64 dirty entity、
64 search_doc、64 search_doc before-image、64 docsize、64 shared revision、
1 listing和2 writer行；其余为FTS data/idx：436/180与405/147。
最新 `sync108-finite-heap-final.log` 的最大Commit墙钟分别149.12/152.86毫秒，
HeapAlloc峰值11.10/10.11 MB、增量5.90/4.75 MB。FTS维护表行随合并布局有小幅变化，
不把一次总量作为固定写放大常数。完整文件表批次64，entry批次50；
full publisher继承的50 entry/50 file合同保持。小输入兼容路径仍受500输入
上界及原100行staging测试约束，不宣称所有事务总共只写64或100行。

## 验证与边界

使用Go1.26.10。`sync108-hr-final.log` 的httpapi 546.506秒、storage
67.869秒通过，包含finite、grouping、duplicate、series/episode、格式副本、
publication、listing、低空间、个人记录与迁移回归。最后candidate池与父游标
SQL变化分别由 `sync108-candidates-final.log`、
`sync108-multiroot-admission-final.log` 追加验证，不借旧运行替代变更验证。
`sync108-race-final.log` 定向race 584.868秒通过；candidate变更的 `sync108-candidates-race-final.log` 定向race 105.724秒通过。
`sync108-static-commit.log` 的format/vet/build/lint通过，lint为0 issues。
命令均使用 `-count=1`，资源复测为 `-count=3`。

sensitive-check通过。doclinks检查136份文档通过。
tracked-evidence-check未通过：继承的 `25-publication-retry-media.md` 第10行
触发production-path；该单行由root授权exclusion worker修复，本批不改。
不能将该检查写作通过，合并修复后由root重跑。

保留的总目标余项：exclusion及其enqueue/runner接点、retire入站alias范围、
其它衍生HTTP reader全面审计、全仓CI和生产恢复/部署。本批没有UI变化，
只做代码、真实HTTP与数据库验证，没有渲染或生产来源验证。尚未声称对所有
候选大小分布具有线性最坏复杂度；无hash大小容差匹配仍需要检索满足范围的
候选，完整语义不以截断或ASCII近似换取性能。
