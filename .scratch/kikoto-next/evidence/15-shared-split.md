# 15 拆分与本人记录归属

**Implementation:** verified-local。本票本地 H/R/F 已通过，完整发布前置保持。

分支从13的已验收提交 `9844975886de745dae660866bcb3ff0eb04198ee` 创建，复用073，无新增迁移。

## 归属规则

预览展示关联前两侧的来源与本人评价，以及当前共享评价。关联后新增、修改或清除评价由本人选择 left/right/both；所选侧保留当前值，未选侧恢复该用户关联前的值。没有变化的记录按关联前依据恢复。原始记录、版本、catalog引用与来源定位保持可寻址；解除映射和记录归属同事务完成。嵌套关联按逆序拆分，避免拆散后续关联。

## 已取得证据

- 首个HTTP红例：split-preview 返回404，退出1。
- `go test -mod=readonly ./internal/httpapi -run '^TestSharedSplit' -count=1 -v`：4项H/R通过、退出0。三名普通用户分别选择一侧、另一侧、两侧；真实关闭/重开应用和同一磁盘库；未确认时记录保留；完成后两个独立卡片、来源与评价；旧预览拒绝、决议失效、事务失败回滚及重试；编辑者不能代选、匿名/伪造用户拒绝、嵌套逆序恢复。

- H 额外覆盖清除后的空评价仍须本人确认；系列拆分恢复各自成员且不改写单集评价；未修改记录按原侧恢复。所有操作保持原版本和来源定位。
- `go test -race -mod=readonly ./internal/httpapi -run '^TestShared(Split|Association)' -count=1`：退出 0；关联与拆分组合无竞争报告。
- `make backend-test backend-format backend-vet backend-lint backend-build`：退出 0，全后端、迁移存储、格式、vet、lint（0 issues）和构建通过。随后新增系列清除/独立成员和有值的嵌套恢复断言，定向H复验退出0；产品代码不变。
- `npm run build`、`npm run lint`、`npm run typecheck:e2e`：退出 0。`npm run test:unit -- --run --maxWorkers=4`：153文件、867项全部通过，退出 0。
- F：`shared-split.spec.ts --project=desktop-chromium --workers=1`，1项真实前后端通过，退出0。本人从页面检查前/后记录、明确选择归属、重载后恢复选择、其他用户不能代选或读取、最后确认后HTTP回读正确。没有选择时确认按钮禁用。
- 13回归F：`make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='tests/e2e/shared-association.spec.ts --project=desktop-chromium --workers=1'`，1项通过，退出0。两套F均关闭截图、trace、video。
- `make -o frontend-install frontend-docs`：退出0，126份公共文档相对链接、5语言/11用户页检查通过。
- 报告前合入最新integration，结果Already up to date。没有新迁移或依赖，没有修改个人数据导入导出和自动候选匹配。

## 接口与保留事项

`POST /api/shared-associations/{id}/split-preview`、`GET /api/shared-associations/{id}/splits`、`GET /api/shared-splits/{id}`、`POST /api/shared-splits/{id}/start` 和 `/decision` 复用073持久状态。本人归属输入为 left/right/both，关联前与应用后快照保留；拆分先在事务内解除本次映射，再恢复原分组并写本人选定值，无物理删除。

没有部署或执行真实来源LIVE。07/08的LIVE、真实索引覆盖与完整发布门槛保留。

## 审计后补证（2026-10-21）

- 新增 `TestSharedSplitSeriesKeepsTwoEpisodesAndTheirPlaybackEditions`：一个系列加两个单集完成共享后再拆分，系列仍列两集；每个 work 的 edition 标签、`catalog_stream` media 定位保持原值。补齐“三对象与版本”断言。
