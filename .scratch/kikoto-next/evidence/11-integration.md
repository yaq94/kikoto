# 11 系列与单集集成复验

Implementation: verified-local

集成起点 `dc790b790f8757b3e60cec6dec4c2d8ada8a96c0`，合入独立提交
`75bf67cab66fa10aca89724966fa2c28e36d85e4`，无冲突。本记录随合并提交保存。
实际核对 071 增量迁移、系列入口，以及三个扫描交界：明确单集根优先、已发布父根
保持身份、发布批次内同事务 `attachCatalogSeriesTx`。没有修改这些交界或登记私人记录。

Go 1.26.9，`GOTOOLCHAIN=local`，Node 24.19.0；以下为合并后当前产物的实测结果。

| 命令 | 结果 |
| --- | --- |
| 在 `backend/` 执行 `go test -mod=readonly ./internal/httpapi ./internal/storage -run '^(TestCatalogSeries\|TestSourceCatalogIndex\|TestOpenList\|TestContentTranslationCatalog\|TestMigration)' -count=1 -v -timeout=4m` | 退出 0；35 个 HTTP 顶层与 3 个存储顶层通过；httpapi 30.234 秒。LIVE 因未配置 SKIP。 |
| 在 `backend/` 执行 `go test -mod=readonly ./internal/storage -count=1 -v -timeout=3m` | 退出 0；完整 storage 包通过，10.984 秒。含新装、070 升 071、增量链/开发基线一致、重复/并发启动和外键。 |
| `make -o frontend-install backend-format backend-vet frontend-lint frontend-build frontend-docs` | 退出 0；格式/vet、前端 lint/类型/构建、124 份 Markdown 链接及 5 语言文档检查通过。 |

合并不改变独立分支已验收的页面代码，F 沿用[11 证据](11-series.md)的 2 项真实前后端
非视觉场景及翻译复验；本次没有重跑 F 或截图。既有 Vite 配置及大 chunk 提示保留。
原始日志为仓库外 jobs `j-3woh5y`、`j-f6nsn8`、`j-u8xws8`。

071 为当前最高迁移，001–070 和发布基线保持不变。07/08 的真实媒体/全来源覆盖、
完整发布前置继续保留；没有生产部署。09 已独立完成，随后另行集成；12 在独立分支开发。
