# 16：个人数据传输兼容证据

## 产物与范围

- 本记录与工单 16 实现保存在同一提交。实施基线为集成 `5aac340`，固定依赖提交 `07fb686f4bbcd8717cfeeedd5d5878346aae6e3f` 提供已验证的 074；本票新增 075 和生成的 `compat/075_dev.sql`。
- 复用 `/api/user-data/export`、`/import/preview`、`/import` 与 `personal.Store`。v2 保存精确作品的稳定引用、原始标题、评价层级、评分备注、收听标记、标签、列表、统计和续播信息；v1/Kikoeru 保持既有编号与别名规则。
- 导入按当前共享评价身份检查输入冲突；矛盾输入即使选择覆盖也整组保留。明细只展示本人字段，安全记录继续处理。未登记/矛盾来源逐条报告，不重新建立共享关联；备份明确携带的单集父级成员关系由工单 22 恢复。
- 空库仅登记必要对象。系列和单集评价独立；未知来源引用保留供传输，不创建来源连接；未匹配续播信息保留供再次导出，不制造媒体或猜测时间轴。

## 红例

1. `TestPersonalTransferExportsEveryEvaluationLevel`：现实现导出 version 1，缺少新增对象稳定身份，退出 1。
2. `TestPersonalTransferGroupsConflictingIncomingSharedReviews`：同一共享身份的矛盾导入未报告冲突，退出 1。
3. `TestPersonalTransferReportsContradictoryAndUnregisteredSources`：作品仍存在但来源解除登记时，旧来源被忽略，错误记为已导入，退出 1。
4. `TestPersonalTransferEmptyLibraryRoundTripAndRestart`：已有长标题导出后被新字段限制拒绝，预览 400，退出 1；采用原有文件总量上限后可往返。

## 当前验收

| 契约 | 实际证据 |
| --- | --- |
| AC-24：四类对象与完整本人记录 | 六个 `TestPersonalTransfer*` H/R 测试通过。空库恢复后再次导出核对评分备注、状态、标签、带图标及排序的收藏列表、累计统计、未匹配进度；保留编号、无码、系列、单集层级。 |
| AC-24：空库、重复与重启 | 同一 SQLite 文件关闭服务器和数据库，再重新打开、迁移和登录；四个对象及全部字段保留，重复导入不增加作品。无媒体文件、无 workflow run。 |
| AC-09：编号和精确引用兼容 | v1 小写编号别名导入解析规范作品；v2 精确 edition 独立恢复。同一备份在共享关联拆分后分别恢复两侧，不重新关联。 |
| AC-13：本人数据与权限 | 真实会话的另一个账户导出为空，不能查看或改写他人评价；缺少登记权限的空库导入 403 且无对象写入。保留匿名/权限、Demo、输入体积和错误脱敏回归。 |
| 冲突和来源边界 | 矛盾共享评价、矛盾身份、已知未登记来源可审阅且不错误归属；不确定项跳过，其他安全记录仍导入；包含未解决引用的列表保留。 |
| F：现有设置入口 | `personal-transfer.spec.ts` 真实生产模式 API 联通：上传导出文件、阅读本人现有/导入评价、选择覆盖、提交、HTTP 回读与刷新后保持。1 项通过，截图/trace/video 全部关闭。 |

## 命令与退出状态

使用项目已有 Go 1.26.9、Node 24.19.0、npm 11.17.0；依赖仅安装在本票工作区。

- `go generate ./migrations`：0，生成最高开发基线 075。
- `make backend-test backend-format backend-vet backend-lint backend-build`：全后端测试（含 storage 的完整迁移、新装、旧库、重复/并发及开发基线）和 format/vet 成功；首次 lint 下载因网络失败，整体退出 2。按既有进程代理运行后，静态检查指出一处字符判定表达式；修正后最终 `make backend-format backend-lint backend-build sensitive-check` 退出 0，lint 为 0 issues。
- `go test -mod=readonly ./internal/personal ./internal/httpapi -run 'Test(PersonalTransfer|PersonalData|Import|Export|Kikoeru|PlaylistIcons|PersonalRoutes|PersonalImport|DemoRejectsEveryPersonal)' -count=1 -v`：0；六个本票 H/R 与现有传输/权限/进度/原子回滚回归通过。
- `go test -race -mod=readonly ./internal/httpapi -run '^TestPersonalTransfer' -count=1 -timeout=3m`：0，六个本票场景通过。后续只移除不兼容的标题短限制，最终普通回归与静态检查再次通过。
- `npm run test:unit -- src/features/user-data`：0，2 文件、21 项通过；`npm run build`：0。
- `make frontend-lint frontend-docs frontend-format frontend-i18n`：最终退出 0，前端 lint、e2e 类型、五语文档链接/语言、格式和文案检查通过。
- `npx playwright test tests/e2e/personal-transfer.spec.ts --project=desktop-chromium --workers=1`：0，1 passed；复用现有 personal-review-fixture，以隔离端口运行。直接调用避免运行中的本工作区依赖被 Make 的安装前置重建。
- `git merge --no-edit codex/kikoto-next-integration`：0，已包含交接时集成 tip；`git diff --check`：0。
- 每次提交前执行 `make sensitive-check`，原始日志保留在仓库外的本票证据目录。

## 清理与交付边界

fixture 经显式停止接口正常退出并清理自身临时数据库，Vite 后台任务已结束；工作区供集成保留。没有修改部署环境、全局工具或签名配置，没有媒体下载、真实数据或原始日志进入仓库。完整 Actions、跨票组合、实际源及部署验收由 17 负责，本票不宣称这些已完成。
