# 11 预备阶段与后续接入

**Implementation:** in-progress。预备阶段保存代码和用例；整票未完成，不可合入生产或部署。

## 基线与实际产物

工作分支 `codex/kikoto-next-t11` 从已验证集成点
`dd38bf1e70ca09fcd796621e9dc5f4a741f1b5ae` 创建。
当前仅实现待接入的 API、目录证据归组服务、卡片/单集入口、五语言表单和测试。
未引入项目新依赖；已安装现有前端 lockfile 的依赖。

071 专门分配本票，但尚未创建。已有迁移管理器要求编号连续，070 属于并行的
08，因此本阶段没有添加占位 070、改变校验或生成不完整 compat。
**当前代码引用尚不存在的 catalog_series 表，不能宣称 HTTP 可用。**
系列接口草案见 [11-interface-draft.md](11-interface-draft.md)；当前系统参考文档
保留集成产物的描述，实际功能就绪后再同步领域/使用文档。

## 实际验证

- H 红例 `TestCatalogSeriesManualPreviewPreservesEpisodeIdentityAndPrivateReviews`
  首次退出 1：`POST /api/catalog-series/previews` 返回 404，预期权限拒绝 403。
  此后有一次开发运行在 SQL 转义错误处退出 1，已修正转义；该运行不算功能证据。
- 当前 `go test -mod=readonly ./internal/httpapi -run '^$'` 退出 0，
  **仅编译，没有执行用例**。`go build ./tests/openlist-fixture` 退出 0，生成的
  自有 fixture 可执行文件已删除，未提交。
- 当前 `npm run build`（TypeScript 与 Vite）、`npm run lint`、
  `npm run typecheck:e2e` 均退出 0。Vite 保留配置加载和既有大包提示。
- `src/app/workDetailNavigation.test.ts` 原有 7 项导航回归退出 0。
  同次指定的 remoteSourceBrowseModel 测试路径没有匹配文件，不能算覆盖。
- 文档相对链接和 locale 检查退出 0。新增代码的 H/R、071 升级、全后端、
  golangci 和两个 F **均未完成**；没有启动浏览器或测试服务。
- 当前 `make backend-format`、`make sensitive-check`（32 个变更文件）、
  `git diff --check`、`git diff --cached --check` 均退出 0。草案移入本期证据目录后，
  文档相对链接检查再次通过（122 份公共 Markdown）。

所有用例仅使用手工合成作品和外部网络桩。F 在用例配置中关闭 screenshot、trace、video，
没有视觉/布局断言，也没有 jsdom/Testing Library。

## 071 应落实的结构

以下字段由当前代码消费，后续 attempt 必须先核对当前源码再编写迁移：

- `catalog_series`: 稳定自增 `id`，`title`，可空 `work_id`，可空 `source_id`，
  可空 `source_key`，`created_at`。自动归组按非空 `(source_id,source_key)` 唯一；
  手工归组不依赖源地址或标题当永久身份。
- `catalog_series_member`: `series_id`、唯一 `catalog_entry_id`、正整数
  `position`、`evidence`。`(series_id,position)` 唯一，重复集号保留独立。
  已有 catalog/work/edition 及个人记录不重建、不搬迁。
- `catalog_series_preview`: 自增 `id`、`actor_id`、`payload_json`、
  `fingerprint`、`status` 默认 pending、可空 `series_id`、`created_at`；
  按 actor/status/time 提供控制记录查询索引。payload 只有系列标题及成员 ID，
  备注仅参与内存 SHA-256，不保存到预览。最多 32 个未过期 pending、最多
  32 个 completed；24 小时后不可确认，创建时按本人清理过期/多余控制记录。

需生成真实 070+071 compat，运行 storage 全包及旧 069 升级/同库重开。
原 001–069 和他人的 070 保持不变。

## 后续必要工作

1. 合入正式含 070 的最新 integration；保留双方修改。
2. 创建 071 和 compat，验证上述结构、权限与私有边界；取得手工归组 H 绿例。
3. 在 08 提取的小文件 `source_catalog_roots.go` 的
   `catalogAdmissionRoot(directoryPath,parentRoot,observed)` 加明确单集根覆盖；
   不让格式/分轨目录覆盖已有单集根。完整扫描的
   `source_catalog_index_store.go/publishCatalogBatch` 每个新条目写入同一事务后，
   调用本票 `attachCatalogSeriesTx`。该扫描接入尚未实施。
4. 完成自动证据与负例 H、完整索引联测、预览版本/权限/数量与大小边界。
   当前自动规则尚需运行真实用例证明，不能把代码阅读当验收。
5. 执行 `catalog_series_restart_test.go`：069 升级，真实关闭应用/SQLite再打开，
   pending 预览恢复、本人评价变更导致 stale 拒绝、再次确认、身份及私人记录保持。
6. 运行 fixture 时设置 `OPENLIST_SERIES_FIXTURE=1`、建议端口 17621/3121，
   先检查空闲。选择 `catalog-series.spec.ts` 的两个非视觉 F，关闭截图/trace/video；
   Playwright 使用现有测试配置与真实前后端，外部目录/媒体由 fixture 提供。
7. 系列/单集标题已接 `seriesId`/`catalogEntryId` 翻译资源，需补其实际 HTTP/F
   联测与受保护名称检查。共享预览保持原文以供确认，实际目录 path 不译写。
8. 做必要 backend 静态/全包、前端回归、文档/敏感扫描；golangci 共享锁由根协调。
   更新实际领域/使用文档、工单逐 AC 和 execution 11 行；提交推送并核对远端。

07 真实外源 LIVE 缺口仍保留，本票没有访问挑战页面、提取 cookies、下载真实音频或部署。
没有清理其他工作区或旧策略拒绝残留。
