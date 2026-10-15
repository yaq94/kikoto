# 11 系列与单集归组验收

**Implementation:** verified-local。本票本地 AC-14、共同 AC-13/18/28 已验证；07 的真实媒体 LIVE 发布依赖保留。

产物：`codex/kikoto-next-t11`，复用预备提交 `2cc1ef5`，合入完整索引 070 后补齐
`071_catalog_series.sql` 与生成的 `compat/071_dev.sql`。本记录随本票提交保存。

## 行为与证据

- `go test -mod=readonly ./internal/httpapi -run '^TestCatalogSeries' -count=1 -v`：
  5 个顶层测试、2 个发现子场景通过，退出 0。真实会话、路由和迁移 SQLite 覆盖
  明确集号及系列目录、相似标题不归组、格式与分轨不新增对象、重复扫描身份稳定、
  一卡多集、单集标题搜索、手工预览/确认、资料权限、匿名读取、过期/容量/大小限制、
  拒绝已有系列重新归组和私有评价不变。系列与单集翻译回读通过，私有文本不能冒充标题。
- 070 旧库升级后真实关闭应用与 SQLite、同库启动三次：pending 预览可继续、他人
  私人记录变动使预览失效、重新预览确认及重复确认稳定，成员 work id 和本人评价保持。
- 首轮缺表红例退出 1，明确 `no such table: catalog_series`；071 后转绿。
- 完整索引红例出现 7 张独立卡片；同事务归组后为 2 系列与 2 独立内容。已有评价父目录
  回归红例出现额外系列卡片；修复后原目录、work id、评分备注保留，子集仅作为目录。
- `make backend-test backend-format backend-vet backend-build`：退出 0。storage 全包
  验证新装 production/development 基线与完整增量链一致、旧库升级、并发/重复启动及
  外键约束。迁移测试的当前版本期望同步至 071；001–070 和 released baseline 未改。
- 前端 `npm run build`、`npm run lint`、`npm run typecheck:e2e`：退出 0；
  `npm run test:unit -- --run`：153 文件、867 测试通过，退出 0。
- `make -o frontend-install -o frontend-playwright-install frontend-e2e
  E2E_ARGS='tests/e2e/catalog-series.spec.ts --project=desktop-chromium --workers=1'`：
  2 项通过，退出 0。真实前后端联通，覆盖单集目录与真实相对路径选轨、切回系列及
  另一单集时播放源不变且时间推进、手工预览重载及确认共享。补入系列/动态单集翻译
  后定向重跑首项通过，退出 0。截图、trace、video 关闭，无视觉/几何断言。
- 首轮 F 发现展开播放器遮挡操作及多 h1 选择歧义；用已有折叠按钮和精确语义标题修正
  用例后通过。未通过 force click 绕过交互，也未修改播放器行为。
- 文档链接、5 语言文档、UI copy、`make sensitive-check` 与 `git diff --check`：
  退出 0。Vite 保留已有配置加载与大包提示。

`make backend-lint` 首次因模块查询直连超时退出 2；使用已有进程级代理后重跑，
0 issues、退出 0；未改全局工具配置或安装另一份 lint。

## 接口与交界

当前契约见 [Catalog series](../../../docs/development/catalog-series.md)。系列持久引用为
`catalog_series.id`；可空 `work_id` 专供系列本身。单集保持 `catalogEntryId` 和原有
`work_id`，不合并已有评价对象。12 可按独立 work id 复用个人评价，不需要抢占迁移号。

与扫描票共享的修改仅为 `source_catalog_roots.go` 的明确单集根、
`source_catalog_index_store.go/completeCatalogDirectoryTx` 保留已发布容器，和
`publishCatalogBatch` 同事务 `attachCatalogSeriesTx`。队列、检查点及扫描清理行为保留。

## 限制

未执行生产部署、真实媒体 LIVE、全站真实覆盖、截图或人工视觉验收。所有运行数据为
合成目录、临时数据库和本地模型/音频桩。07 外部缺口不被本票本地证据替代。
