# 09 每日更新与系列索引组合复验

Implementation: verified-local

集成起点 `996e3798c304b0b238bf63c8ac96dc15541457ef` 已包含 11/071，合入
09 独立提交 `afe2fe19cd9a5c80cb4b335fa728d7f5641a049e` 无冲突。
`server.go` 与开发文档自动合并；11 的三个根选择/同事务归组交界完整保留。

Go 1.26.9，`GOTOOLCHAIN=local`；合并后实际执行：

- `backend/`：`go test -mod=readonly ./internal/httpapi -run '^Test(CatalogSeries|SourceCatalogDaily|SourceCatalogIndex)' -count=1 -v -timeout=3m`，退出 0，21 个顶层 H/R 通过，38.805 秒。
- `make backend-format backend-vet backend-build`，退出 0。

组合覆盖每日调度、取消/低空间/同库重启恢复、单源任务排他、完整扫描、系列/单集
归组及已保存评价的父根身份保留。071 的完整迁移验证沿用刚完成的
[11 集成复验](11-integration.md)，09 没有新增迁移或前端行为，因此不重复前端全套。
原始日志为仓库外 jobs `j-rl5w2f`、`j-l5ncul`。各票独立证据见
[09](09-daily-index.md) 和 [11](11-series.md)。

07/08/09/11 保持 verified-local；真实媒体、真实全来源覆盖和完整发布前置保留。
本次没有部署或请求真实站点，没有需要清理的自启服务。
