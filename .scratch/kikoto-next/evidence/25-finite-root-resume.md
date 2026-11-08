# 25：有限扫描恢复后续根目录

基点 `f33e51bed519e514685c6330f40fc2c07b207c64`。Spec 复核发现：配置多个有限扫描根时，第一根发布成功、第二根在 marker 前失败，重试会从第一根重新申请 publication，被第二根的持久 owner 阻塞。

扫描在开始前读取当前来源的 preparing owner，把仍在配置范围内的 manual 根排在最前，恢复其原有 listing 和 publication，再扫描其他根。其他类型的 owner、已不在配置中的根返回 `catalog_busy`，不会接管任务或扫描范围外目录。不新增状态表或迁移，不改变请求和分页预算。

原始日志在仓库外 `reports/20261107-remediation/`：

- `finite-roots-red.log`：第二根 marker 注入 SQL 故障，关闭 Server／数据库并重开后再次 HTTP 扫描，原代码返回 500（owner 冲突），退出1。
- `finite-roots-final.log`：同场景恢复为200/complete，两个根各一张卡、两条音频、无 pending；提交 marker 的 operation id 与失败前相同，退出0。
- `finite-roots-race.log`：同一 H/R 回归在 race 下退出0，38.725秒。
- `finite-roots-green.log`：既有单卡重启、多根部分失败、521卡写事务故障恢复三项通过。新增用例早期将已被下一根清理的完成 operation 当作必须保留，已修正为观察 marker 提交时的 id；不改变恢复合同。

保持根范围、独占 owner 和完整集合。静态检查与最终同候选复核由集成记录汇总，本证据不代表部署完成。
