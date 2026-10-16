# 16/19 集成复验

## 固定产物

- 唯一集成分支：`codex/kikoto-next-integration`。起点 `5aac34069afb01031387d30fb34b466cd5d50aad`，开始时工作区干净且与 origin 一致。
- 固定审查基线：`9949175e53979bcd590c75a624bc0efc67684da3`；后续完整审查范围保持不变。
- T16 候选 `87e0071f6e7d5cc64bb143e794ce6e390e0b861c` 以 no-ff 合入，合并提交 `25f449de16fe8e6942f0e5ae179447a2df8cc149`。
- CI19 候选 `ce5154e52103dcbebabf62fc2623f95f55c9af6e` 随后以 no-ff 合入，合并提交及本批功能验证产物为 `2180c62dfdbf6659929f3c288ddb1387220130ae`。
- 两次合并均无冲突；提交前分别执行 `make sensitive-check`，32、6 个变更文件通过，退出 0。沿用既有 Git 配置。

## 迁移与组合边界

001–075 连续且无重复编号，最高开发基线为 `compat/075_dev.sql`，应用版本仍为 `v0.8.3`。001–073、全部发布基线及前端依赖声明/锁文件与集成起点一致。074 及其开发基线与冻结提交 `07fb686f4bbcd8717cfeeedd5d5878346aae6e3f` 一致。074 schema 不代表 T14 功能已合入。

传输验收使用真实 HTTP、会话及迁移 SQLite，包含 v1/Kikoeru、v2 四类评价对象、空库必要登记、重复导入、本人冲突、未匹配进度和真实重启。跨票用例在关联后导出两个精确引用，拆分后分别恢复两侧评价，检查他人记录保持且未重建共享关系。关联与拆分的过期预览、并发、失败事务、系列成员及恢复回归一并执行。

## 当前产物验证

使用既有 Go 1.26.9、Node 24.19.0、npm 11.17.0。全部下列命令实际退出 0，无跳过或失败的目标测试。

| 检查 | 结果 |
| --- | --- |
| `go test -mod=readonly ./internal/personal ./internal/httpapi -run 'Test(PersonalTransfer\|PersonalData\|Import\|Export\|Kikoeru\|PlaylistIcons\|PersonalRoutes\|PersonalImport\|DemoRejectsEveryPersonal\|SharedAssociation\|SharedSplit)' -count=1 -v -timeout=5m` | 35 个顶层测试通过；其中 T16 6 个、关联 5 个、拆分 4 个。 |
| `go test -mod=readonly ./internal/storage ./migrations -count=1 -v -timeout=5m` | storage 67 个顶层测试通过；migrations 为嵌入目录，无测试文件。新装、完整增量链、旧库升级、重复/并发启动、开发基线一致性通过。 |
| `go test -race -mod=readonly ./internal/httpapi -run '^Test(PersonalTransfer\|SharedAssociation\|SharedSplit)' -count=1 -v -timeout=5m` | 15 个顶层测试通过，63.648 秒，无 race 报告。 |
| `make -o frontend-install frontend-format frontend-lint frontend-build` | 格式、oxlint、E2E 类型检查、TypeScript 与 Vite 构建通过。 |
| `npm run test:unit -- src/features/user-data src/app/playingWorkNavigation.test.ts src/player/dock/queueDrag.test.ts` | 4 文件、29 项通过。 |
| `make -o frontend-install -o frontend-playwright-install frontend-e2e`，使用外部配置与定向 `E2E_ARGS` | 18 项、2 workers、0 retries，22.5 秒通过；配置与完整选择表达式保存在仓库外日志索引。 |

前端依赖为主工作区已有独立目录，已验证不是 junction；锁文件未变。Make 的 `-o` 仅复用已安装依赖及浏览器，避免测试运行中重建依赖。

浏览器选择包含 T16 真实生产模式 API 导入链路 1 项，以及 CI19 回归 17 项：骨架闸门、同步媒体 hook、四项目录错误状态、八项独立收藏行为、移动封面两种双击与单击。截图、trace、video 均关闭；CI19 保留既有尺寸断言，不能把这组既有 CI 回归全称为规格要求的纯非视觉 F。T16 的 1 项新增 F 只验证交互、文本、HTTP 回读与刷新持久性。

## 证据与未完成项

- 单票依据：[16 个人传输](16-personal-transfer.md)、[19 浏览器可靠性](19-ci-browser.md)；本记录补充合并后实际验证，不替代这些单票证据。
- 原始日志、外部 Playwright 配置、输入事件附件和逐项退出记录由本批仓库外 `integration-16-19` 证据目录保存，不进入 Git。
- 测试 API 收到停止请求并返回 204，进程退出 0，自行清理临时数据库；本批 Vite 已停止，所有测试任务取得终态。其余工作区由主流程负责，未清理 T14 或其他实施工作区。
- Vite 的未来 native-config 兼容提示及大 bundle 提示仍为构建警告，构建成功；未扩大为工具升级或构建架构调整。
- 14 功能、全部真实来源覆盖与媒体 LIVE、资源组合校准、固定基线后的完整审查、最终 SHA 的 Full/Gate、备份恢复、回滚及部署验收仍由后续主流程完成。16/19 保持 `verified-local`；本批未部署。
