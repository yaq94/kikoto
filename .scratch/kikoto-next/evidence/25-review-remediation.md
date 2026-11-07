# 25：审查整改的本地证据

## 版本与边界

- 基点：`53d905e1d212e85d78cb95687044b96b4fc3ebae`。
- 第一批代码：`64f77e36`（清理准入）与 `5cf1218eceff2cbfb215e77dc5f6a41815c61f3d`（恢复单集层级）。后者包含全部第一批代码；验证时的源码与该提交相同。
- Windows amd64、Go 1.27.3、Node 24.19.0；真实迁移 SQLite、合成数据、真实应用路由与会话。迁移仍为 105。
- 原始日志置于仓库外工作区的 `reports/20261107-remediation/`；下表以 `REPORT_DIR` 表示该目录。原始日志、临时数据库与 overlay 源码不入库。
- 未读凭据、未调用生产或公网服务、未 push 或部署。无 UI 改动，无截图、像素或布局检查；这里的 H/R 不代替后续 F/LIVE 与当前候选全量验收。

## 缺陷、修复与反例

| 问题 | 修复 | 缺陷版本的失败证据 | 修复后证据 |
| --- | --- | --- | --- |
| folder 与旧 generation 被计入清理准入 | 仅统计当前发布 generation 的非 folder 文件；每个删除事务复核音频、非音频、文件数及统一 Decision | `prune-red.log`：folder 和旧 generation 两例把 GET 200 的卡片列为删除候选，exit 1 | `prune-final.log`：9 个顶层 prune 测试通过，含 4 个当前文件集/阈值边界子例，exit 0 |
| 报告校验后、下一批执行前准入变化 | 删除事务再次校验当前准入，不只比较总行数 | `prune-toctou-red.log`：测试触发器在第一批后使第二卡从 4 个音频变成 5 个；总行数不变，旧代码仍删除第二卡，exit 1 | `TestCatalogPruneRechecksAdmissionInsideEachBatch` 拒绝第二批，GET 回读卡片和个人评价保持 |
| 旧音频行错误供给当前准入 | 旧 generation 的 audio 不参与当前 Decision | `prune-stale-audio-red.log`：旧代码漏掉当前无音频卡，exit 1 | `TestCatalogPruneIgnoresStaleAudioAndKeepsPersonalState` 正确清理无音频卡，保留 work 评价与公开导出 |
| 恢复单集被人工/自动关联当作独立作品 | 共享快照识别显式父级及主 portable ref kind，人工、自动匹配和拆分共用；缺少来源集号仅表示证据不足 | `association-red.log`：4 子例失败；人工跨层 preview 201，自动跨层匹配 completed | `association-green.log`：4 子例均通过，人工 409、自动 review/conflicting_evidence，实际重启后仍保持且独立 work 无单集备注 |
| 恢复单集同层关联与拆分身份 | 保留同层关联，按原记录拆分，父级引用与导出不变 | `association-split-red.log`：旧快照把两侧均标成 work，exit 1 | `TestSharedAssociationRestoredEpisodesLinkAndSplitAfterRestart`：episode 层级、关联后的评价共享、跨重启拆分、原评价与父级导出均通过 |

`prune-red.log` 和 `association-red.log` 在修改对应实现前运行。后续反例通过 Go overlay 精确替换基点的单个实现文件运行；未回滚工作区或修改主工作树。反例数据均为手工合成。

## 验证命令

命令工作目录为本批独立 worktree 的 `backend/`，除特别标明外统一前缀 `GOTOOLCHAIN=local GOPROXY=off`。

| 命令 | 结果 | 日志 |
| --- | --- | --- |
| `go test ./internal/httpapi -run '^TestCatalogPrune' -count=1 -timeout=90s`（修改前） | exit 0；既有 6 项测试通过，说明原套件缺少反例 | `prune-baseline.log` |
| `go test ./internal/httpapi -run '^TestCatalogPrune' -count=1 -v -timeout=90s` | exit 0；5.411 s | `prune-final.log` |
| `go test ./internal/httpapi -run 'Test(CatalogPrune\|SharedAssociation\|SharedSplit\|WorkMatching\|PersonalTransfer\|AuditPersonalTransfer)' -count=1 -v -timeout=10m` | exit 0；48.601 s；涵盖个人隔离、冲突确认、来源关联、拆分、恢复及本批新增场景 | `p1-regression.log` |
| 仓库根 `make backend-format backend-build` | exit 0；格式检查及实际后端二进制构建通过 | `p1-build-format.log` |
| `go vet ./internal/catalogprune ./internal/httpapi` | exit 0，无输出 | `p1-vet.log` |
| `go test -race ./internal/httpapi -run '^(TestCatalogPruneUsesCurrentPublishedFiles\|TestCatalogPruneRechecksAdmissionInsideEachBatch\|TestCatalogPruneIgnoresStaleAudioAndKeepsPersonalState\|TestSharedAssociationRestoredEpisodeRejectsCrossLevel\|TestSharedAssociationRestoredEpisodesLinkAndSplitAfterRestart)$' -count=1 -v -timeout=5m` | exit 0；148.007 s；本批全部 5 个新增顶层场景及其子例通过，未报告 data race | `p1-race.log` |
| 仓库根 `make sensitive-check NODE=<Node24>`（每个行为提交前） | exit 0 | `prune-sensitive.log`、`association-sensitive.log` |
| 仓库文档链接检查器检查 2 份产品文档及新 25 工单/证据 | exit 0；4 份文件通过 | `p1-doc-links.log` |
| 22、23、25 工单的必需头字段及顺序检查 | exit 0；3 份改动工单通过 | `p1-changed-ticket-headers.log` |

## 既存文档门禁缺口

- 全仓 `make tracked-evidence-check NODE=<Node24>` 返回 exit 2：未改动的 `17-integrated-acceptance.md` 第 71、72 行各命中 `release-number`。日志为 `p1-tracked-evidence.log`，本批新增证据没有命中。主代理负责在本批交接后脱敏旧证据并保留外部原始记录。
- 全工单头字段检查发现未改动的 `19-ci-browser-reliability.md` 字段顺序不符合要求，日志为 `p1-ticket-headers-utf8.log`。原脚本硬编码主工作树路径和 24 张票，本次用仓库外参数化副本对独立工作树检查；改动的 22、23、25 三张票另行检查通过。主代理负责修复旧 19 的字段顺序。
- 这两项在本批基点已存在，不是本批修改引入的回归；本批未改相关文件、扫描器或豁免规则，未据此声称完整仓库门禁通过。

## 剩余要求

本票保持 `in-progress`。翻译在途版本、系列写入/翻译准入、SPA 配置刷新复现、staging 写放大、增量同步检查点、跨 flush 并发、基准错误传播及夹具标准仍由后续批次落实。未量化的性能疑点不被当作已修复项。主代理负责最终同 SHA 账本、Full CI、实际服务及部署证据。
