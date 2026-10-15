# 12 系列与单集个人评价验收

**Implementation:** verified-local。本票 AC-15、共同 AC-12/13/28 的本地验收通过；
11/07 的完整发布依赖保持，未以本地证据替代外部 LIVE。

产物：独立分支 `codex/kikoto-next-t12`，从已验证并推送的 11 提交
`75bf67cab66fa10aca89724966fa2c28e36d85e4` 创建，包含集成 `dc790b7`。
无新增迁移或依赖，沿用 071 的系列独立 work 引用。

## 实际验证

- 红例：`go test -mod=readonly ./internal/httpapi -run '^TestCatalogSeriesPersonalReviews' -count=1`
  退出 1，真实 `PATCH /api/catalog-series/1/user-state` 返回 404。
- 绿例及最终补充：上述命令加 `-v`，退出 0。真实会话、迁移 SQLite、应用 HTTP 服务
  覆盖系列与两个单集各自的评分备注、三个不同 work id、两用户独立读写、未提交字段
  保留、评分与备注分别清除、非法输入与伪造用户字段拒绝、匿名写入拒绝及匿名空个人状态。
  普通用户不能为未登记系列自动建作品，但能评价已登记系列；拒绝登记没有部分写入。
- 同一应用与 SQLite 真正关闭后重新打开同库：三个评价对象及两用户的记录不变。
  已有跨源成员关系在两源列表中回读同一个系列；单集别源定位映射到原 work id，
  从别源更新备注后原单集回读一致。关系夹具只表达既有确认关系，不增加自动匹配规则。
- 系列与单集公共标题保持；作品库公开查询正好登记三个必要对象，workflow 列表无新任务，
  受管理数据目录为空。没有保存媒体、触发下载或登记额外成员。
- `make backend-test backend-format backend-vet backend-build`：退出 0，全后端（含 storage、
  11 系列、个人评价与来源回归）通过；`make backend-lint` 使用已有进程级代理，
  0 issues，退出 0。
- `npm run build`、`npm run lint`、`npm run typecheck:e2e`：退出 0。
  `npm run test:unit -- --run`：153 个文件、867 项通过，退出 0。
- F：`make -o frontend-install -o frontend-playwright-install frontend-e2e
  E2E_ARGS='tests/e2e/catalog-series-review.spec.ts --project=desktop-chromium --workers=1'`
  1 项真实前后端测试通过，退出 0。另选定 `personal-review.spec.ts` 的2项既有功能回归
  通过，保护通用编辑器的保存、重试、部分清除和远端登记行为。
- 新 F 保存系列及两单集不同值，切换格式和返回系列不复制记录；清评分保留未提交备注，
  再保存并重载回读正确，未发起媒体或下载 API 请求。初次请求观察误匹配 Vite 源码路径，
  限定为 `/api/` 后定向重跑通过；未改变功能断言。截图、trace、video 关闭。
- 文档链接、5语言文档、UI copy、敏感扫描与 `git diff --check`：退出 0。
  前端保留已有 Vite 配置加载与大包提示。

## 接口和修改边界

- `catalog_series_review.go` 提供 `PATCH /api/catalog-series/{id}/user-state`。
  在事务中取得写锁、按需登记系列、复用 `patchWorkUserState`，仅处理会话本人。
- `getCatalogSeries` 只连接当前用户的评分备注；成员列表没有他人个人字段。
- `CatalogSeriesDetail` 与 `CatalogWorkDetail` 复用 `PersonalReviewEditor`，用明确的
  系列/单集标题区分目标；新增可选标题属性不改变原作品编辑器的默认语义。
- 5 种 UI 语言、API 类型与当前开发文档同步。12 没有修改扫描、071、共享执行记录或总览。

报告前合入 `996e3798c304b0b238bf63c8ac96dc15541457ef`。新增差异仅为 11 集成证据、
共享 execution 与总览三份文档，代码和测试产物未变化；沿用上述本次验收。

## 未覆盖项

没有执行生产部署或真实来源 LIVE；未增加作品匹配、冲突合并、拆分或个人数据传输规则。
这些保留所属工单范围。合成音频与 API 功能证据不代表真实音源编码或视觉验收。
