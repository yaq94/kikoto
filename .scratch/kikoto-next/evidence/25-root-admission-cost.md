# 25：卡片根准入查询成本

基点为 `6f516e31`，对应本文件同提交的准入查询修复。原始合成日志位于仓库外 `reports/20261107-remediation/`。生产尚未部署。

## 失败与定位

Full run `42529615957` 的 race 7/8 耗尽原 35 分钟包预算，`TestSourceCatalogIndexContract` 当时运行 3 分 8 秒。调用栈在 `catalogRootAdmissionTx` 的 SQLite 查询，未见 DATA RACE 或单项断言失败。该候选共 29 个成功 job，race 与 Gate 失败。

按 Linux 生效的测试文件核对，唯一测试名从 2,018 增至 2,019，与两次 CI 输出相同。新增系列测试使 1,670 个旧名称换片，完整索引契约从 6/8 移至 7/8。旧 Windows 清单不能证明 Linux 分配；本批保留分片算法、八片数量及全部测试，不通过重新分片规避实际查询成本。

真实迁移 SQLite 上的合成诊断包含 1,000 个根、10,000 个观测。旧查询的三个计数均从仅约束 `file_source_id` 的 observation 索引开始，再查询目录，导致单根计数读取同来源其他根。30 次计数耗时 295.746 ms。目录优先的等价查询使用既有 root 索引，再以 directory/revision 查询观测，30 次为 2.631 ms；计数均为 10 个文件、5 个有界音频和 2 个非音频。

## 实现与验证

只固定三条计数查询的连接顺序，并将相同 SQL 提取为共用查询值。source、generation、root、revision、duplicate 过滤，完整观测要求，拆分根语义及共享阈值保持。没有新增缓存、迁移或依赖。

- `admission-query-diagnosis.log`：同一真实 schema 的查询计划及 30 次对比，exit 0；临时诊断源码已移除。
- `admission-plan-red.log`：既有发布计划回归内新增准入子场景，旧代码因读取非目标目录 revision 的观测而失败，exit 1。
- `admission-green-regression.log`：22 项顶层定向测试通过，exit 0；覆盖准入等值、拒绝、发布队列、拆分、退役恢复、完整树及幂等。1,000 目录、498 卡的普通索引用时 24.605 秒，低于原 60 秒门槛；相同计划子场景通过。
- `admission-baseline-race.log`：旧代码的独立完整树 race 测量 exit 0，索引阶段 458.480 秒，出现两次心跳 SQLITE_BUSY；完整包 482.971 秒。CPU 记录确认准入调用链累计 172.89 秒。该数据是定位证据，不作为普通构建性能门槛。
- `admission-fixed-race.log`：5项顶层race通过，exit 0；原规模完整树索引阶段239.802秒，比旧实现458.480秒减少约47.7%，全部集合断言保持。阈值、既有观测、队列清理及查询计划同时通过，无DATA RACE。race插桩期间仍有两次心跳SQLITE_BUSY日志，不将其称为完全消除锁等待；普通构建及目标机预算另行验收。
- 完整候选 CI 与目标机实际发布验证仍待补齐。
- `admission-static.log`：`make backend-format backend-vet` exit 0。
- `admission-lint-docs-privacy-proxy.log`：固定 lint 2.13.1、文档、隐私及证据检查 exit 0。首次 lint 因未使用已配置的网络代理下载失败，原日志保留；修正命令环境后完成检查。

原测试、样本、断言、35 分钟 Go 包预算、45 分钟 job 预算及重试策略均未放宽。本证据不关闭最终候选验收。
