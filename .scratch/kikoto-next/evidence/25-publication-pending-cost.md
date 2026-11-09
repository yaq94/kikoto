# 25：发布准备队列跳过已处理前缀

基点为 `6b353094`，对应本次提交的两个发布准备查询和规模计划回归。原始日志保存在仓库外 `reports/20261107-remediation/`。本批不修改schema、批次、检查点、测试规模或超时。

## 触发与原因

一个发布操作含有许多条目时，已处理实体仍须保留到提交，以便迟到的来源或译文写入重新失效它们。媒体和译文队列查询虽然要求 prepared=0，SQLite 却选择实体表主键，重复扫描每次增长的已处理前缀。107已有两个匹配的partial索引，但计划未使用。

对5,101个合成实体、仅最后一项待处理的同表结构测量：原媒体查询20,436 VM步，原译文查询20,427步；指定既有pending索引后分别27步和24步，返回对象与游标完全一致。该VM对照使用本地原生SQLite，仅作定位；实际项目modernc迁移库的回归也在两条旧查询上失败，均打印PRIMARY KEY计划，见`publication-pending-index-red.log`，exit1。

## 改动与验证

两条生产查询固定使用对应pending索引；SQL常量由执行入口和计划回归共用。回归同时验证尾部未处理对象，以及先前对象重新失效后能优先读到，避免用单调游标跳过迟到变化。没有新增索引或跨事务缓存，没有更改媒体、译文或个人记录内容。

普通回归五项通过，exit0：长历史与pending计划、原生/模型搜索一起发布、读事务快照与冲突写者、播放及作品来源快照、媒体投影有界更新。日志`publication-pending-index-green.log`。同五项race退出0，63.783秒，见`publication-pending-index-race.log`。

`make backend-format backend-vet backend-build`退出0；首轮lint因直连包代理失败，配置当前网络代理后同版本2.13.1检查通过、0issues。`make frontend-docs sensitive-check tracked-evidence-check`退出0，见`publication-pending-index-static.log`及`publication-pending-index-lint-docs.log`。主代理Spec核对确认排序、剩余对象、迟到失效与提交边界保持；Standards核对复用107既有索引及当前查询约束，没有新迁移、跨事务缓存或依赖。完整门禁继续待办；本证据不将局部修复当作35分钟分片超时已解决。
