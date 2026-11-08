# 25：738f2ee0 候选验收记录

候选 `738f2ee0b27af5cd0bd53b0a463c7b84540482d7`，镜像 `kikoto:next-738f2ee0`，schema 111。代码已推送集成分支。完整目标仍在进行，生产仍为 `kikoto:next-53d905e1` / schema 105；以下不是部署完成报告。

## Standards

主代理核对 `44007193..738f2ee0` 的实际差异、仓库 AGENTS、翻译开发文档及工单边界：系列资格提取为同一 helper；统计数据保持在持久层，不增加全局缓存、依赖、迁移、权限或来源特判。合成回归从 HTTP 入口验证真实 SQLite，原 15 秒预算、42,624 条目、600 系列、6,000 成员和 172,896 译文保持。格式、lint（0 issues）、vet、构建及隐私检查已通过。未确认新的规范问题。

独立 Standards 子代理在执行前被自动审核拒绝（possible cybersecurity risk）；本段是主代理核验，不能标为独立通过。历史独立审查仍仅对其记录范围有效。

## Spec

主代理核对系列资格两项集合与原 COUNT/EXISTS 等价、已发布快照依旧控制准备期可见性、缓存页分摊仍排除 native 和持久语料、最终 revision 复查保持。发现运行状态语言汇总误计历史和显示字段，已通过真实 HTTP 红绿回归修复；汇总复用同次覆盖快照。当前差异未确认其他规格缺陷。

独立 Spec 子代理同样被执行前自动审核拒绝；不得把未执行记为通过。原文候选修复的上一轮独立 Spec 记录与本批主代理核验分别保留。

## 已取得证据

原始日志位于仓库外 `reports/20261107-remediation/`，镜像 digest、配置和主机细节仅在受限运维记录。

| 检查 | 结果 | 证据 |
| --- | --- | --- |
| Full CI | run `42520096408` completed/success，31/31 job 成功 | `github-738f2ee0-current.jsonl`、`github-738f2ee0-race7.json` |
| 后端静态与构建 | format/lint/vet/build 退出 0，lint 0 issues | `translation-runtime-checks.log` |
| 预算与系列回归 | race 267.623 秒，退出 0；后续运行状态回归 race 200.250 秒，退出 0 | `translation-series-behavior-race.log`、`translation-runtime-corpus-race.log` |
| 系列统计规模 | 2 vCPU / 2 GiB，原合成规模和 preparing 快照；冷 HTTP 13,047.952 ms，退出 0，原 15 秒预算通过 | `target-738f2ee0-series-resource.log` |
| 42k 卡浏览 | 原 42,624 卡、98,774 文件、30 次，p95 228.507 ms、p99 228.738 ms；≤300/800 ms 通过 | `target-738f2ee0-catalog42k.json` |
| 5,001 文件卡拒绝 | 30 次均404，p95 2.659 ms、p99 2.867 ms，通过 | 同上 |
| 32k 分组 | 峰值每表64行；写锁 p95 11.106 ms、max24.951 ms；堆增量约5 MiB；退出0 | `target-738f2ee0-resources-busy.log` |
| 有限全量、旧卡退役、低空间 H/R | 全部通过；1,103旧卡退役的 entry/file 写入峰值64 | 同上 |
| 实际模型与简繁 | 中性日文首次生成及转换1,271 ms，随后两次切换7 ms并命中；中文原生转换和保护名称正确；试译339 ms；三个测试均通过 | `target-738f2ee0-live-translation.log` |
| 升级和回滚 | 105→111→105，新旧应用实际启动、作品API回读、六表逐行保持、quick_check/FK通过；备份前故障恢复旧镜像；退出0 | `target-738f2ee0-rehearsal.log` |
| 456k 译文查询 | 并行索引条件下英文整串5.38 ms，超过5 ms门槛，待原条件复测；shortComplete约100–114 ms为无上限诊断，不是工单门禁，另以现有TestShortQueryPerformance复验1000张预算下的页面、总数与零命中 | `target-738f2ee0-translation456k-busy.json` |

42k 浏览首次在生产索引、隔离索引及恢复演练并行时测得p95 437.612 ms，报告明确不达标。复测暂时冻结隔离验收容器，保留其内存、检查点和数据库，退出时自动解冻；生产保持运行。没有更改夹具、次数、预算、断言或应用代码。两份原始报告都保留，未用进程退出0替代性能判断。

恢复演练前两次因副本创建后的可用盘不足10 GiB被预检拒绝，没有发生服务切换。清理未使用的构建缓存、旧测试二进制和仅用于被拒绝演练的数据库副本后，第三次使用同一脚本和原磁盘门槛通过；失败日志保留在受限目录，生产备份和回滚镜像保持。

## 组合与真实来源

`2dc873a5` 隔离实例已完成原60秒／8并发组合负载：1 vCPU／768 MiB下1,058请求无错误；2 vCPU／2 GiB且实际来源索引推进期间908请求无错误，151次Range均206。后续 `738f2ee0` 只修改运行状态语言汇总，最终服务切换后仍须核对实际读取。

同一隔离实例的 IAB 非视觉验收：真实音轨在扫描推进期间从约155.79秒播放到237.77秒；换内容语言、在设置与库页之间导航后同stream、readyState4、error为空、未暂停；最后通过UI主动暂停。界面语言仍English，内容改为Simplified Chinese。未采集截图、trace或video。

27个待确认空目录的匿名list/get共54次请求均200，列表total均0且目录存在。4个编码问题的父目录各一次完整listing，仍返回相同损坏名称；不猜测路径、不改状态为已解决、不把partial写成complete。

隔离实例于2026-11-08 21:33:46 UTC完成本次目录发布与清理，API终态为partial。8个分区、14,485个目录中14,458个完成，待处理、失败、受限均为0，27个待确认空目录；观察389,917项、索引42,027条，publishPending=0；另有4个编码问题与14个暂时不可用的sidecar读取。终态和匿名复核是实际覆盖证据，不能外推为上游全部范围已确认。原始报告为受限的`index-publication-final-private.json`，汇总为`index-publication-final-summary.json`。随后已有增量同步任务开始执行。

全部8个托管开发worktree的HEAD均为当前候选祖先，没有未提交源码；9份需要保留的忽略测试日志已复制到仓库外，8个附件均已通过原生archive归档。主集成仓保留。

## 尚未完成

- 456k复测已通过：编号p95 0.028ms、标题1.375–1.639ms、FTS0.352–0.524ms；预算短查询页面9.759–11.838ms、总数9.477–10.093ms，零命中45.595/60.331ms。日志为`target-738f2ee0-translation456k-idle.json`与`target-738f2ee0-short-query.json`，原样本和门槛保持。
- 738实际服务运行状态语言计数与有效覆盖一致，浏览器重载仍保持237.825567秒和暂停状态；来源卡片列表在15秒内两次超时，阻止本候选部署。后续修复见[系列卡片成本](25-series-browse-cost.md)。
- 生产备份、部署和部署后逐项验收。
- 最终候选账本与工单收口。
