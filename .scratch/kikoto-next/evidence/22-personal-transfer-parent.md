# 22：个人备份的单集父级归属证据

## 产物与范围

- 分支为 `codex/kikoto-personal-parent-remediation`，基于 `134f7c90`；实现只修改个人传输、HTTP 传输测试与对应文档。
- 版本 2 的 `BackupIdentity` 增加可选 `parent: {ref, title, entryRef, position, evidence}`。`ref` 是现有 `s:` 系列引用，`entryRef` 是单集的 `c:` 目录引用，不引入第二套身份。
- 父级只在单集有明确 `catalog_series_member` 时导出，即使系列没有本人评价也会写入。导入利用同一引用解析已有系列，或在空库创建最小的 series work、`catalog_series`、目录条目和成员行。
- 没有数据库结构变更，因此没有新增迁移或基线。

## 红例

1. `go test ./internal/httpapi -run '^TestPersonalTransferRestoresEpisodeParentAcrossEmptyLibraryRestart$' -count=1 -timeout=15m`
   - 退出 1：`episode identity has no parent`。导出只有单集自身的 `w:` 和 `c:` 引用。
2. 仅加入父级导出字段后重跑同一条命令：
   - 退出 1：`registered works = 1, want the episode and its series only`。备份已有父级，但导入仍把单集登记成独立作品。

## 绿例

- `go test ./internal/httpapi -run '^TestPersonalTransfer(RestoresEpisodeParentAcrossEmptyLibraryRestart|BlocksEpisodeWithoutRestorableParent|KeepsExistingDifferentEpisodeParent)$' -count=1 -timeout=15m`：退出 0。
- `go test ./internal/personal -run '^TestClaimTransferRef' -count=1 -timeout=15m`：退出 0；缺失触发器别名时创建明确 `s:` 行，引用已属于其他系列时拒绝改写。
- `go test ./internal/personal ./internal/httpapi -run 'PersonalTransfer|Transfer|Import|Export|ClaimTransferRef' -count=1 -timeout=15m`：退出 0。
- `go test ./internal/personal ./internal/httpapi -count=1 -timeout=15m`：退出 0；personal 19.375s，httpapi 484.309s。
- `go vet ./...`：退出 0。
- `go run ../scripts/check-go-format.go internal/personal internal/httpapi`：退出 0。
- `npm run format:check`、`npm run lint`、`npm run build`：退出 0。
- `npm run test:unit -- src/features/user-data`：2 个测试文件、21 项通过。
- `npm run docs:check-locales`：5 个文档语言、11 个用户页面通过；`npm run docs:check-links`：133 个公开 Markdown 文件链接通过。
- `make sensitive-check`：退出 0，22 个变更文件通过。

## 恢复语义

- 空库导入只在备份明确携带父级时创建 series work 和成员关系；不按标题、路径或相似名称匹配。
- 父级引用已有 series 但尚未登记时，只补建该 series work；已有 series work 时直接复用。
- `claimTransferRef` 先确认目标 series/目录存在且请求的 ref 未指向其他对象；目标对象没有触发器生成的别名时，直接创建明确 ref 行，不依赖触发器存在。
- 单集已有同一父级时重复导入保持幂等，不增加 work、series、目录条目或成员行。
- 单集已属于另一个系列时报告 `parent_conflict` 并跳过该单集，既有成员关系不改。
- 单集没有父级信息且资料库也没有既有成员关系时报告 `parent_missing`，不创建独立 work。
- 父级恢复只写 `catalog_series`、`source_catalog_entry`、`catalog_series_member` 和必要 work；`source_catalog_entry.file_source_id` 为空，不建立来源连接、不下载媒体、不启动 workflow。

## 风险与边界

- 现有系列详情只列出来源可见成员；空库恢复的目录条目没有 `file_source_id`，因此不会出现在来源系列详情中，但成员行、稳定引用和后续导出仍保留。该行为沿用既有“无来源成员不展示”契约。
- 一个 work 若同时属于多个不同系列，现有备份模型不能用一个 `parent` 表达。导出不再失败：该条保留 `w:`／`c:` 引用和评分备注，`parent` 留空，并在备份 `issues` 中报告 `parent_conflict`；导入端按 v2 无父级条目报告 `relationships_not_imported`，不猜测系列。
- 父级位置和证据来自导出时的成员行；外部手写备份缺少位置时按该系列下一个位置恢复，缺少证据时使用 `manual`。
- 本机 Go 为 1.27.3，Node 为 24.22.1；项目声明 Node 24.19.0，`npm ci` 仅在关闭 engine 严格检查后完成。`make frontend-docs` 因本机系统 Node 22.23.4 与声明不一致而失败，直接运行同一组文档检查脚本通过。

## 多父级导出修复（当前候选 `3259df47` 之上）

- `exportIdentity` 在 `exportEpisodeParent` 返回内部哨兵 `errExportParentConflict` 时不再冒泡 `ErrConflict`：identity 原样返回，`parent` 为空，同时产出 `{ref: <episode w: ref>, reason: "parent_conflict"}`。
- `Backup` 新增只用于导出的 `issues`（`json:"issues,omitempty"`，复用 `ImportIssue`），导出结果按 `ref`、`reason` 排序；导入忽略该字段。
- 红例：`TestTransferExportReportsMultipleSeriesParentInsteadOfFailing` 在修复前报 `export failed on an ambiguous parent: personal record already exists`；修复后同一备份含两条 episode，冲突条目 `parent=nil` 且 `issues` 恰一条 `parent_conflict`，唯一父系列条目仍带 `parent`。
- 绿例：`go test ./internal/personal/ ./internal/httpapi/ -run 'Personal|Transfer' -count=1` 通过（personal 27.307s，httpapi 54.587s）；`go test ./internal/storage/ -run 'TestWorkParentMigration|TestMigrationChain104|TestMigrateRejectsChangedAppliedMigration|TestMigrateFreshDatabaseUsesLatestReleasedBaseline' -count=1` 通过。
- 权限 403：`TestPersonalTransferParentRegistrationRequiresPermissionAndStaysUserScoped`（`backend/internal/httpapi/personal_transfer_parent_scope_test.go:165`）通过。
