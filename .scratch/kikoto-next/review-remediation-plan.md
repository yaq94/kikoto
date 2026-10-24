# Kikoto 审查整改实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use `implement-spec` and TDD. Execute the dependency graph, not just the first available ticket.

**目标：** 在不缩减既有 46 个用户故事、28 项 AC 与真实部署门槛的前提下，修复审查确认的规则冲突、覆盖漏洞、身份恢复和发布治理问题。

**架构：** 沿用现有目录 generation／增量 sync、内容译文 SQLite/FTS、个人稳定引用及工作流。每项修改从已确认的公共入口验证，避免新增任务框架或平行身份体系。

**技术栈：** Go、SQLite/FTS5、React/Playwright、现有 Makefile、Git worktree。

**依据：** `spec.md`、正式 issues、`GLOSSARY.md`、`ADR-0004`、`ADR-0005`、`ADR-0006`、`../../.kikoto-work/prompts/` 的整改与翻译工作包。

## 全局约束

- 目标语言搜索仅返回该语言已有译文或原生内容；原文搜索只在 `origin` 范围执行。
- 有效语料必须对应当前可用条目、当前原文及当前模型/模板配置；扫描结束和连续失败不能冒充覆盖完成。
- 任何有界搜索都必须覆盖完整有效集合；不能用固定最新行窗口静默遗漏早先条目。
- partial 的失败范围单独重试，已成功范围继续目录优先增量；未知范围不能判删除。
- 保留 `work.id`、个人记录、来源身份及既有编号入口；导入不能猜测或覆盖他人共享关系。
- 已发布迁移不可修改；095 是否已执行以真实记录核实。已有环境升级沿编号链，不以新装基线替代。
- 测试沿用已确认的 H/R/F/U/LIVE；每项修复先取得预期失败，再实现并复验。不为通过测试放宽身份或覆盖断言。
- 公共文档只描述当前行为，提交通用合成夹具，不包含真实路径、作品记录、日志或数据库。

## 审查重点

- 目标语言搜索、简繁归一化与未翻译条目的边界：工单 21 的远程 H/F 用例。
- 退役条目、原文或模型变化、失败及重启：工单 21 的有效覆盖与恢复用例。
- 持续 partial 时其他分支新增文件：工单 20 的 H/R 增量与失败重试用例。
- 空库中仅有单集评价、父系列无评价：工单 22 的空库往返与归属用例。
- 清理仍带个人稳定引用的来源卡片：工单 23 的现行 dry-run 和保护回归。

## 文件与任务图

### Task 1: 正式工单与规则裁决

**文件：** 本计划、`issues/20`–`24`、`ticket-proposal.md`、`spec.md`、`execution.md`、`docs/decisions/ADR-0005*`。

1. [ ] 把新增整改映射为正式工单 20–24；保留 01–19，不通过编号覆盖旧 evidence。
2. [ ] 统一严格语言搜索、完整有效集合、持久语料与软预算、partial 增量、固定调度及 D-16 三条准入。
3. [ ] 正式废止 D-17 删除个人稳定引用的旧规则，保留原始出处和新旧决定的关系。
4. [ ] 将 17/18/19 的发布依赖接入 20–24，并标明当前未提交修复不属于已验收产物。
5. [ ] 按文件归属逐项实现；每项独立提交前运行 `make sensitive-check`。

### Task 2: 工单 20，目录增量与准入

**文件：** `source_catalog_daily.go`、`source_catalog_index.go`、`source_catalog_sync.go`、必要迁移、`catalogadmission/` 及对应测试。

1. [ ] RED：持续 partial 时，已完成分支新增文件被目录优先增量发现，失败范围仍重试且不判缺失；调度遵守每日/周日 03:00 Asia/Shanghai。
2. [ ] GREEN：复用 `source_catalog_sync_run` 记录独立增量，不重叠全量任务；需要全量对账时仍走完整 generation。
3. [ ] RED→GREEN：D-16 三项阈值在入队、发布、API 使用同一规则；正常多轨与含附属文件的合格作品保留。
4. [ ] 验证 H/R、恢复、限速、取消、低空间与对应 catalog 回归。

### Task 3: 工单 21，译文持久性、有效覆盖与语言搜索

**文件：** `content_translation_retention.go`、`content_translation_store.go`、`translation_prefill.go`、`openlist_catalog.go`、本地 `library/query.go`、相关前端搜索调用和测试。

1. [ ] RED→GREEN：软预算不删除当前有效译文；确需淘汰的可重建缓存与持久语料分离，退役及 superseded 行有独立清理语义。
2. [ ] RED→GREEN：覆盖统计关联有效条目与当前缓存键；历史行、退役行、淘汰、原文/模型/模板变化及失败跳过均不能计为完成。
3. [ ] RED→GREEN：严格目标语言搜索；1–2 字用完整集合上的有界索引或逐段分页，不能固定截断最近 50,000 行。
4. [ ] 验证远程/本地、简繁、动态内容、重启、队列与当前模型的 H/R/F；按规模测量真实 p95。

### Task 4: 工单 22，空库个人数据归属恢复

**文件：** `backend/internal/personal/transfer_*` 与 HTTP 传输测试。

1. [ ] RED：父系列没有本人评价时，单集导出后空库恢复仍保留原稳定父级归属。
2. [ ] GREEN：备份携带必要父级稳定引用与最小身份；仅重建备份明确表达的归属，不猜测、合并或覆盖其他关系。
3. [ ] 重复导入、矛盾引用、权限、他人隔离及旧格式兼容保持；失败不能静默降级成无父级单集。
4. [ ] 验证 H/R、导出再导入、无媒体下载及对应 personal/httpapi 回归。

### Task 5: 工单 23，保留规则与工程规范

**文件：** `catalogprune` 相关测试、`data-model.md`、`release-rollback.md`、指定测试夹具、历史证据。

1. [ ] 以现行 source-only alias 保留规则重新生成合成 dry-run；旧生产报告不得作为 apply 依据。
2. [ ] 脱敏历史证据，移除真实路径、作品编号及数据库/日志细节，保留事实和未完成状态。
3. [ ] 补齐译文模型和保留语义；回滚检查与实际 Compose 能力一致；测试编号使用保留构造函数。
4. [ ] 清理参考文档的历史叙述；验证文档、敏感信息和相关行为回归。

### Task 6: 工单 24，同候选集成与真实发布

**文件：** `execution.md`、17–24、`evidence/`、部署与回滚文档。

1. [ ] 合并所有已提交修复及另一实施会话的暂存/有界事务修复，解决归属边界，不复制未提交文件。
2. [ ] 执行 matt-code-review，修复 Standards 与 Spec 发现；重新验证变更后的候选。
3. [ ] 对同一 SHA 完成 Full CI、类型/构建、迁移恢复、备份回退、组合负载、真实索引及有限 LIVE。
4. [ ] 记录实际镜像、迁移链、生产状态与未覆盖项；只有完整 AC 与真实服务证据闭环才可关闭 17/24。

## 依赖与整合

- 20、21、22 可独立实施；23 依赖其规则裁决；24 等待全部代码票与另一会话已提交修复。
- 另一会话暂拥有 `catalog_episode_staging*`、`catalog_series_discovery.go`、`openlist_catalog.go`、096，以及 `content_translation_store.go`、`content_translation_source_store.go`、`content_translation_retention.go` 的有界事务修复。
- 本计划的 21 在其对应提交交付后整合；先提交规则和非冲突模块，禁止覆盖或回滚对方工作。
- `execution.md` 当前表与历史记录分开；当前状态只能引用同候选实际证据，旧记录保留原版本语境。
