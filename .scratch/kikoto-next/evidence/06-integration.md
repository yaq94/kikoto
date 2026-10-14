# 06 集成复验

本次验收日期为 2026-10-15。集成分支为 `codex/kikoto-next-integration`，固定总审查基线保持 `9949175e53979bcd590c75a624bc0efc67684da3`。

## 产物与 AC 核对

- 集成从 `15ee7e23023308d2f50ef9a921429a6b1c7ed646` 快进到候选 `06781bba890caa38c2e7eeb3e79138dbca5871bf`，无冲突；合入前已回读两条远端分支的完整 SHA。作者的 [06 验收记录](06-stable-references.md)及实际差异均已核对。
- 业务变化只在 `updateWorkUserState` 使用现有 `workReadID` 和错误映射。数字引用仍选择精确 edition；编号、alias 和 legacy origin 解析 canonical。权限检查先于解析，个人状态继续限定当前用户，保留 04 的部分更新与清除语义。
- 新增测试使用真实 HTTP 路由、会话与 SQLite。067 样例经已发布编号链建库，068 样例经 v0.8.3 基线建库；分别在同一文件上应用当前迁移链，三轮关闭 HTTP、应用和数据库后重开。公开详情和 Library 回读证明关系、两用户评分备注、评分 0、公共评分和对象总数保持；alias 修改备注在后续重启后仍由数字与编号引用回读。
- 既有只读解析测试启用 SQLite `query_only`，覆盖 canonical、alias、edition、legacy 和数字版本读取，并检查无新增作品或待处理标题投影。
- 本票五项清单及 AC-09 的既有编号兼容范围满足。没有新增表、字段、迁移、依赖或前端行为；本次集成未额外修改应用或测试代码。

## 本轮实际验证

工具链为 Go 1.26.9、Node 24.19.0；仅设置当前进程 PATH 与 `GOTOOLCHAIN=local`。除注明外，命令从仓库根执行。

| 命令 | 退出状态与结果 |
| --- | --- |
| `go test -mod=readonly ./internal/httpapi -run 'TestWorkReference\|TestPersonalReview\|TestResolveIsReadOnly' -count=1 -v -timeout=3m`（backend） | 0；8 个顶层测试通过，无跳过，HTTP 包耗时 9.105 秒；包含 04 五项回归、06 H/R 和只读解析。 |
| `make -o frontend-install backend-format backend-build frontend-docs` | 0；Go 格式检查、当前应用构建、文档检查器测试、121 份公开 Markdown 相对链接、5 种文档语言与 11 份用户页面检查通过。复用已安装前端依赖。 |

本轮定向 Node 检查退出 0：7 份相关 Markdown 链接可解析，17 票的验收清单正文和依赖声明相对合入前保持不变，规格正文保持不变，06 五项清单已完成，069 分配正确，应用和测试代码与候选完全一致。提交前 `make sensitive-check` 扫描 6 个变更文件通过，`git diff --check` 退出 0。

本次代码与候选完全一致，因此没有重复作者的全后端、race、lint/vet 或全前端检查；这些作者证据的范围见原记录，不记作本轮重新执行。没有运行 F、LIVE、视觉、外部媒体、全站索引或生产部署验收。

## 接续范围

- [07：有限 OpenList 入口贯通无码卡片、目录、播放与评价](../issues/07-openlist-first-playable-slice.md)的 04、06 前置均已验收；07 仍为 pending，本轮没有实施其功能。
- 已登记对象继续使用 `GET /api/works/{id}`、`GET /api/works/{id}/media` 和 `PATCH /api/works/{id}/user-state`。实现入口分别为 `backend/internal/httpapi/work_detail.go` 的 `getWork` / `getWorkMedia`、`user_state.go` 的 `updateWorkUserState`，引用解析在 `work_code_read.go`。
- 04 的个人记录复用点为 `personal_review.go` 的 `workUserStatePatch` / `patchWorkUserState`，远端必要登记在 `remote_personal_review.go` 的 `updateRemoteWorkUserState`。07 必须保留当前用户隔离与不下载媒体的边界。
- 无码准入仍需处理 schema、来源索引与定位、`frontend/src/app/workDetailNavigation.ts`、`frontend/src/lib/workCode.ts`、`RemoteOnlyWorkDetail` 和来源 code 参数。完整边界见 [06 的 07 接入契约](../issues/06-stable-identity-compatibility.md#07-接入契约)，本轮不预设具体实现。
- 02 在独立工作区实施，已确认缓存使用独立 CacheRoot，不占用业务 SQLite 迁移；069 已释放并预留给 07。当前迁移目录最高为 068，07 开始修改前仍须核对编号与开发基线生成规则。

## 清理与交付

本轮仅有上述两个检查后台任务，均已取得退出 0；HTTP 服务、应用资源和临时 SQLite 由测试正常关闭与清理。没有启动常驻服务，没有删除作者工作区、共享依赖或其他代理文件。构建产物与共享工具缓存保留供后续复用。工单、规格和总览仅同步进度与证据，验收正文保留。
