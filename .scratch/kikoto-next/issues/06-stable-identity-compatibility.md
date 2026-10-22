# 06: 稳定内部引用与编号兼容入口

**What to build:** 在真实升级后的库中，同一既有编号作品可经原编号入口和稳定内部引用访问，作品关系、版本与个人记录保持一致，为无码准入提供兼容基础。

**Blocked by:** None (can start immediately)

**Status:** resolved
**Implementation:** resolved；本地实现与 H/R 已验收；生产部署验收见 17。

**依据:** [正式规格](../spec.md) · [工单总览](../ticket-proposal.md)

## 验收范围

**主责:** AC-09

**共同:** 无

AC-09 的既有数据与编号兼容部分；无码完整用户链路由 07 接入。

## 验收清单

- [x] 优先复用已有稳定标识和解析契约，采用兼容扩展；不因来源副本、目录或译名新增第二个作品身份。
- [x] 旧编号、别名与既有版本入口保持原有含义；指向同一对象的新旧引用返回一致关系和个人数据。
- [x] 迁移遵循不可变的已发布迁移链；升级、重复启动及关闭重开不丢失或重复已有对象和记录。
- [x] 通过公开读取及个人状态入口验证兼容，不能以新增字段、建表或内部映射测试作为本票完成证据。
- [x] 记录后续无码链路仍需迁移的调用方契约；只扩展已明确需要的接口，不预设全仓重构或删除旧编号入口。

## 测试与交付边界

**测试入口:** H、R，使用包含既有关系和个人记录的迁移样例。

H 使用真实 HTTP 路由、会话与迁移 SQLite；R 实际关闭并重启同一应用。

遵守工单总览的[共同完成要求](../ticket-proposal.md#共同完成要求)，复用既有测试入口；不新增测试技术栈或安装授权。

本票验收证据须对应实际产物，相关文档同步并提交、推送 Git。工单发布不代表功能实施或生产授权。

## 实施与证据

- 复用 `work.id`；只扩展个人状态 PATCH 的引用解析，保持数字精确 edition、编号/alias/legacy origin 解析 canonical。没有新增表、字段或迁移，069 预留已交还集成协调。
- H 红灯：`go test ./internal/httpapi -run '^TestWorkReferencePersonalStateUsesCodeCompatibility$' -count=1`，退出 1；有效数字写入后，编号 PATCH 返回 400 `invalid work id`。首次夹具误用非有效 listening status 已修正，不计作功能红灯。
- H 绿灯及既有只读解析回归：`go test ./internal/httpapi -run '^(TestWorkReferencePersonalStateUsesCodeCompatibility|TestResolveIsReadOnlyForCanonicalAliasEditionAndLegacySnapshot)$' -count=1`，退出 0。验证编号与别名读写同一对象、数字 edition 保持独立、非法/不存在引用返回 400/404。
- 已合入集成 `15ee7e23023308d2f50ef9a921429a6b1c7ed646`。新增 H/R 从真实 067 编号链及 068 v0.8.3 基线建库，保存 canonical/edition/alias/legacy snapshot、两用户评价，再关闭并重开 HTTP、应用与同一 SQLite 三轮；关系、评分 0、备注、公共评分、独立 edition 状态与会话保留，Library 总数不增加。
- 最终 H/R 与 04 五项个人评价回归共 8 个顶层测试通过，退出 0；同一选择的 race 检查通过，退出 0。`make backend-test backend-build`、格式/lint/vet 均通过；121 份公开文档相对链接通过。
- lint 首次遇未配置进程代理的模块查询超时；按已授权代理重试后发现测试中的两处响应关闭返回值和一处布尔写法，修正并复验为 0 issues。未改全局配置或其他服务。
- 当前产物已取得本票全部验收证据；[完整命令、版本与 AC 映射](../evidence/06-stable-references.md)。未进行无码功能或部署验收。

## 07 接入契约

- 已登记对象使用 `work.id`：详情 `GET /api/works/{id}`、目录 `GET /api/works/{id}/media`、个人状态 `PATCH /api/works/{id}/user-state`。编号与别名继续兼容；返回的数字 `id`/`workId` 可继续传给现有数字 API，不增加第二身份。
- 07 仍须扩展 `work.primary_code`、`logical_work.canonical_code` 与 `work_edition.primary_code` 的无码准入约束及来源定位映射，不能填造 RJ 编号。作品版本、来源索引条目和真实文件路径保持分离。
- 仍为编号契约的调用方：前端 `app/workDetailNavigation.ts`/`lib/workCode.ts` 的编号导航、`RemoteOnlyWorkDetail` 的编号准入与登记、来源 availability 和远端 tracks 的 code 参数；07 按有限来源链路逐处接入，不全库替换。编号专用 `/resolve` 保留；无码已登记对象直接使用数字详情入口。
- 个人导入导出的编号格式扩展归 16；共享关联与记录冲突归 13，06 不转移或合并个人记录。

## Comments

- 2026-10-15 集成核验：候选 `06781bba890caa38c2e7eeb3e79138dbca5871bf` 已从 `15ee7e2` 快进合入。逐项核对五条验收正文与实际差异，指定 H/R 和 04 回归 8 个顶层用例全部通过，退出 0；[集成证据](../evidence/06-integration.md)记录实际范围。04、06 前置均已完成，07 可启动；02 不占用业务 SQLite 迁移，069 预留给 07。
