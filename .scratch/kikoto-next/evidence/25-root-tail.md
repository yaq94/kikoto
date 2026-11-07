# 25：退役别名与来源标志集成核验

基点 `ac66e07d` 集成 108 分组、finite、publication 重试及 exclusion 后端和任务界面。该点的交叉检查：`root-108-integration.log` 中 httpapi 269.779 s、storage 9.498 s，exit 0；`root-108-static.log` 格式/lint exit 0、0 issues。重新生成 compat108 没有 SQL 内容变化。原始日志统一位于仓库外 `reports/20261107-remediation/`。

## 退役别名

`TestCatalogPublicationRetiresAliasesInResumableBatches` 构造 150 个真实 alias。旧代码一事务更新 151 个 entry，红测试退出 1，见 `root-retire-alias-red.log`。publication 退役将 alias 工作保存为待处理实体，按既有 relocated 索引每次更新 64 个仍指向旧卡片的 alias。消费进度与变更同事务；准备期间仍读取 before-image，最终 marker 才切换。

测试注入第二批写失败，确认 working 已处理 64、published 仍有全部 150 个旧引用；实际关闭 Server 和 SQLite，再重新打开后完成同一 operation。修后普通运行 exit 0、2.934 s，日志 `root-retire-alias-restart.log`；包含既有退役个人引用的定向 race exit 0、143.235 s，日志 `root-retire-alias-restart-race.log`。测试约束 entry 峰值，不将其称为触发器/FTS 总写行数上限。

未改变个人记录合并的 savepoint 与冲突规则，没有新增 DDL。publication 外的既有同步事务路径保持原合同；后续调用链核查仍须确认所有可触及大集合的生产入口进入 publisher。

## 来源可用性读取

现有 playback/presence 组合回归增加真实来源可用性标志断言。旧 `workHasSourcePresence` 在 marker 前读到 working missing，红测试 exit 1（`root-derived-presence-red.log`）。来源 presence 和 location 标志改为同一 published 查询入口，准备阶段不提前改变已发布可用性；相关组合 race exit 0、27.411 s（`root-retire-presence-race.log`）。其它衍生页面仍需要调用链核查，此局部结果不宣称全应用读取验收完成。

## 静态与界面文档

Go 1.26.10 的 `make backend-format backend-lint GOLANGCI_LINT_BASE=ac66e07d` exit 0、0 issues，见 `root-tail-static-final.log`。文档、5 locale/11 用户页、sensitive 与 79 个 tracked evidence 检查 exit 0，见 `root-tail-docs.log`。新增排除任务浏览器文件已由 `discoverMockedSpecs()` 实际发现并进入既有 CI，未新增视觉门禁。五种来源使用说明补充保存/完成、后台处理、重试和记录保留语义。

完整 CI、目标机负载、真实模型/媒体、恢复副本和部署仍未完成，工单继续进行中。
