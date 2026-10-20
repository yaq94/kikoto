# 13 共享关联与本人冲突确认

**Implementation:** verified-local。本票本地 H/R/F 与检查已通过；07/08 的 LIVE、真实索引覆盖及完整发布前置保持。

## 已确认实现边界

- 共享身份使用独立可撤销映射，原 `work`、`logical_work`、`work_edition`、catalog 和系列成员引用保留。版本族具有媒体选择语义，不能用它代替关联。
- `POST /api/shared-associations/previews` 接受两个稳定 `work`、`catalog` 或 `series` 引用；浏览者可查看对象、来源、本人字段与冲突，响应不含其他人的值或身份。
- `/api/shared-associations/{id}/start` 由资料编辑者提交预览；`/decision` 仅由有冲突记录的会话本人选择最终评分和备注。全部必要决议前保留原关系和记录；最后确认与身份归属在一个 SQLite 写事务提交。
- 原评价入口继续读写原引用。同一共享身份的评分备注保持一致，空值不覆盖已填写值；播放进度、收听状态、收藏、媒体和来源定位不由评价同步改写。
- 数据库同步只处理同用户评分备注，包含新增、部分更新和清除；关联与拆分提交先处理归属再开放同步，不容许半完成状态。
- 待确认版本覆盖身份、来源/成员关系和评分备注；播放进度更新不会使确认失效。发生相关改变时全部旧决定作废，要求新预览。
- 关联前快照持久保存供拆分；未来个人数据导入须先按共享身份归一，并处理本人字段冲突，不能依靠多行写入触发器的顺序决定最终值。
- 库列表、检索和分页按共享身份返回卡片；详情列出共享来源，但每条媒体继续使用原版本与目录定位。

## 当前证据

- 真实 HTTP 首个红例 `TestSharedAssociationWaitsForEachRecordOwner`：退出 1；预览 POST 返回 404。测试使用迁移 SQLite 和三个真实会话。

## 当前验证

- `go test -mod=readonly ./internal/httpapi -run '^TestSharedAssociation' -count=1 -v`：5 个顶层用例通过，退出 0。覆盖两名普通用户、字段互补、本人确认、私有回读、跨层级拒绝、必要登记、同库应用关闭重开、修改后恢复原值的过期、来源改名、事务失败回滚和两个预览并发提交。
- `go test -race -mod=readonly ./internal/httpapi -run '^TestSharedAssociation' -count=1`：退出 0。
- `make backend-test backend-format backend-vet backend-lint backend-build`：退出 0，全后端、库检索、存储迁移、格式、vet、lint（0 issues）和构建通过。随后系列列表增加共享单集去重红绿，定向 `TestSharedAssociation|TestCatalogSeries` 与最后静态/构建复验退出 0。
- `go generate ./migrations`：退出 0，生成 `compat/073_dev.sql`；存储完整迁移链、新装、升级、并发启动及开发基线一致性通过。旧迁移与发布 baseline 未修改。
- F：`shared-association.spec.ts --project=desktop-chromium --workers=1`，实际浏览器与真实后端通过，退出 0。覆盖预览过期、新预览、资料编辑权限、两用户本人确认及重载；截图、trace、video 关闭。最终复验还覆盖关联来源跳转和原编号详情链接解析，当前用例退出 0。
- `npm run build`、`npm run lint`、`npm run typecheck:e2e`：退出 0。前端单测首次全并发 866 通过、1 个既有 toast 导入测试超时；未改动该用例，定向重跑通过；限定 maxWorkers=4 后 153 文件、867 项全部通过，退出 0。最终前端构建、lint、E2E 类型和文档检查均退出 0。

## 稳定 SQL 交界

`shared_work_identity(work_id,identity_work_id)` 对未关联对象返回相同 id，关联对象共享 identity_work_id。按 identity_work_id 查成员；本人 rating/note 从 user_work_state 回读。`shared_review_revision(user_id,work_id,revision)` 缺行视 0，仅评价变更递增。16 导入必须先归一身份、本人确认冲突，再每身份写一次；不能利用同步触发器覆盖相互冲突的导入行。详见 [共享关联开发契约](../../../docs/development/shared-associations.md)。

## 当前环境事件与待完成

文档目标未跳过 frontend-install，触发 npm ci，沿已有共享 node_modules junction 部分移除了现有依赖，并因运行中的 native 文件锁而失败。自有前端服务停止后，按完全相同的原 lockfile 执行 npm ci --strict-allow-scripts，192 packages 恢复、0 vulnerabilities、退出 0；package.json 与 package-lock 未变化，junction 保留。已向主代理报告恢复。源码和迁移未受影响，恢复后的最终 F、前端构建、867 单测及文档检查通过。

报告前 merge 最新 integration（含12与072），结果 Already up to date。15 尚未实施；本票没有部署或真实来源 LIVE。07/08 的 LIVE 与真实索引覆盖发布前置仍保留。

## 审计后补证（2026-10-21）

- `associationConflicts` 的字段级规则新增纯规则表：仅 rating、仅 note、两者、相同值、单侧空值。
- 两用户真实关联用例增加版本断言：关联后 work 11/12 的 `work_edition.edition_label` 保持各自原值，不被对方覆盖。
- 复验：`TestSharedAssociationWaitsForEachRecordOwner`、`TestSharedAssociationConflictFields` 通过；全后端通过。
