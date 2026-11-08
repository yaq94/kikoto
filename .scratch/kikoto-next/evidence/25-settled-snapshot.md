# 25：已稳定目录的读取成本

依据 20 的 42,624 卡 HTTP 浏览性能合同及 ADR-0008。原始报告在仓库外 `reports/20261107-remediation/`。

`e49c32aa` 在目标机 2 vCPU / 2 GiB 容器、原 42,624 卡 / 98,774 文件 / 30 次测量下，HTTP 浏览 p95 352.868 ms，未达到 300 ms；p99 362.012 ms。5,001 文件卡拒绝 p95 2.624 ms 达标。报告 `target-e49c32aa-catalog42k.json` 明确标记 browseWithinThreshold=false；测试退出 0 仅表示完成测量，不表示性能验收通过。

浏览与直接 SQL 的差异来自发布投影。读事务建立快照时，从既有 preparing 部分索引判断是否存在未发布操作；没有时，该事务内实时表与发布视图等价，读取使用原表及索引。存在准备中操作、没有固定事务的读取继续使用发布视图。判定只属于当前 SQLite 快照，不能跨请求复用；显式播放登记离开快照后同时移除该标记。

- 同提交的读取改动交叉编译后，在同样容器约束、原样本及原断言下，p95 183.699 ms、p99 197.956 ms；拒绝路径 p95 2.827 ms。`target-settled-snapshot-catalog42k.json` 两项 threshold 均 true，命令退出 0。未新增迁移或缓存。
- 35 项发布、播放、有限扫描、关联行为回归通过，29.138 秒，`catalog-settled-snapshot-behavior.log`。其中固定 reader 在 publication 开始、修改及提交后仍读原轨道，新 reader 才读取新轨道。
- 10 项相关 race（含发布故障重试、播放释放读事务、并发写入）通过，114.426 秒，`translation-insert-scope-race.log`。
- format、vet、build、lint 均退出 0，lint 0 issues；`catalog-settled-scope-static.log`。

`e49c32aa` 已完成隔离 105→111→105 恢复，实际启动新旧应用并回读作品，六张身份及个人数据表逐行核验一致，备份失败恢复旧镜像路径也通过；`target-e49c32aa-rehearsal.log`。最终发布候选继续登记同版本证据。
