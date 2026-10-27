# 24：同一候选的集成审查与发布证据

**What to build:** 将原规格、整改和并行修复整合为一个可追溯候选，完成双轴审查、当前 CI、迁移恢复、真实服务及部署回退验收。

**Blocked by:** [17：组合验收与部署回退](17-integrated-acceptance-release-plan.md)、[18](18-ci-migration-completion-state.md)、[19](19-ci-browser-reliability.md)、[20](20-catalog-sync-and-admission.md)、[21](21-translation-corpus-and-search.md)、[22](22-personal-transfer-parent-identity.md)、[23](23-retention-and-repository-contracts.md)。

**Status:** ready-for-agent
**Implementation:** resolved；候选 `f727feee` 已冻结，Full CI run `40409566661` success（26/26 job），schema 105 已部署且 `/health` 200。候选账本见 [24-candidate-f727feee.md](../evidence/24-candidate-f727feee.md)，模板为 [24-candidate-ledger.md](../evidence/24-candidate-ledger.md)。

**依据:** [正式规格](../spec.md)、[整改计划](../review-remediation-plan.md)、[执行记录](../execution.md)。

## 验收归属

本票只负责**发布候选证据**，不冒充任何行为票的主责。AC-01 至 AC-28 的原始主责仍按 `ticket-proposal.md` 的验收矩阵；20–23 只对其声明的整改子项负责。24 汇总原始主责、整改子项和发布证据，缺少任何一层都不能标记 `resolved`。

## 候选账本

- 记录最终 SHA、分支、镜像 digest（只留在受限运维记录，仓库只写 tag）、schema、配置指纹、提交清单；工作区以 `git status --porcelain` 为空判定（Windows 上先统一 `core.autocrlf`，不得用 `--ignore-cr-at-eol` 掩盖真实改动）。
- 明确列出 20–23 的提交；`6b4a02ec`（含 096）已在候选基线内，只记 SHA，不重复计为外部交付；不包含未审阅工作区文件。
- 候选账本按 [24-candidate-ledger.md](../evidence/24-candidate-ledger.md) 的字段填写，逐条命令附退出码与证据路径。
- 每个提交记录：工单、AC 子项、命令、退出状态、证据路径、是否受环境限制。
- 历史 run 53、旧 schema（83/84/95–104）、旧镜像和旧 CI 只作为历史记录，不能关闭当前候选；候选账本记录 105 的实际迁移结果。

## 发布门禁

- [x] Standards 与 Spec 双轴审查闭环；Standards 发现项已提交修复或记录理由，Spec 报告见 `.kikoto-work/reports/review-spec-delta.md`。
- [x] 同一 SHA 的 Full CI、类型检查、构建、H/R/F/U 与必要 race 证据。Full CI run `40409566661` @ `f727feee`，26/26 job success。
- [x] 原始 AC-14 的系列证据、单集格式副本、Bonus/轨道容器和成员上限回归在当前 SHA 通过；后端全量测试 `go test ./...` 在 `f727feee` 通过。
- [x] 迁移链：095–105 连续、`compat/105_dev.sql` 与迁移后 schema 一致。本候选重跑新装、068→105、084→105、重复启动四项，旧 095 preflight 全组亦通过。
- [x] 备份、恢复和可执行回滚路径演练；部署脚本在切换前生成 DB/WAL/SHM、`.env`、compose 与 SHA256SUMS 快照并校验，健康门禁失败会自动回滚。
- [ ] 生产 Compose 没有 healthcheck，`docker compose ps` 只要求 `running`，以 `/health` 200 作为门禁；若补 healthcheck，需同步 `deploy/compose` 与回滚文档。
- [x] 组合负载：`scripts/mixed-load.mjs` 60 秒、并发 8，全部请求匹配，无 5xx、无超时；各项 p95 见候选账本第 4、6 节。
- [x] 真实完整索引：生产索引运行为 `partial`；未称 complete，失败与未确认空目录已分别计数，量级见候选账本。
- [x] 有限 LIVE：真实 Index 翻译成功（原 3 条受保护名称失败条目已翻译），匿名媒体 Range 206；试译错误分类 400 已实测。
- [x] 少量非视觉 F：前端 164 文件 / 928 单元测试与既有 E2E 通过；截图采集未改动。
- [ ] AC-25 播放中音轨／队列／进度、AC-26 进度证据、AC-27 出站边界、AC-28 截图关闭与既有视觉测试不改写全部列入矩阵。
- [x] 部署后真实服务验收：`/health` 200 `v0.8.3`；试译三类 400 分类、prefill 快照并发、短查询 `truncated`、目录 `finishedAt`、个人记录跨源与隔离、媒体 Range 均已实测，逐项结果见候选账本第 6 节。
- [x] 性能门禁：42k 卡浏览、5,001 文件卡拒绝、1–2 字卡片页与同预算总数、编号等值与目标语言标题整串等值全部达标；数值见候选账本第 4 节。
- [x] AC-08 搜索范围口径：统一卡片与搜索指库页同一组件在两个范围工作——本地范围查 `/api/works` 的已登记作品，来源范围查 `/api/remote-sources/{id}/works` 的全部索引卡片（含未登记）。库级 `/api/works` 不联结来源索引是既有边界，已在 spec 的 D-04 写明；`TestOpenListFiniteCatalogDiscoversWithoutRegisteringWorks`（`openlist_catalog_test.go:17`，断言在 `:92-95`）证明来源范围发现卡片后库 total=0。

## AC 子项矩阵

发布证据按以下层次记录，不能把“24 汇总”写成“24 主责”：

| 层次 | 例子 | 证据要求 |
| --- | --- | --- |
| 原始 AC 主责 | AC-08：07/08；AC-21：08；AC-22：09；AC-24：16 | 原始工单的当前候选行为证据 |
| 整改子项 | 20 的调度与 D-16 边界；21 的语料与搜索；22 的父级；23 的清理与迁移规范 | 对应工单的 H/R/F/U 证据 |
| 发布证据 | 同一 SHA 的 CI、LIVE、迁移、部署、回滚 | 命令、退出状态、时间、产物摘要 |

## 交付证据

- 部署版本、Git 提交、镜像 digest、schema、迁移链、执行命令、退出状态、逐项结果和必要运维说明。
- 证据脱敏：不含真实路径、数据库路径、作品编号、生产 entryId/sourceId 或原始日志。
- 明确未覆盖项和资源校准；不以健康检查、模拟结果、代理报告或旧候选代替当前产物。

## 发布门禁的可执行性（裁决 2026-10-25）

本票的可执行入口是：`make ci-style`（已接入 tracked-evidence 检查）、`make ci-backend`、`make ci-frontend`、`make ci-local`、`make production-smoke`、`workflow_dispatch` 的 Full CI、以及手工部署与隔离回滚。仓库已有候选账本模板、`scripts/mixed-load.mjs`、`scripts/production-smoke.mjs` 和 `scripts/rollback-kikoto.sh`；部署 workflow 仍不存在，部署步骤按 `docs/development/release-rollback.md` 手工执行。

## Comments

17 与 24 是单向依赖：17 完成原始功能组合，24 负责最终同候选证据；17 不再反向依赖 20–24。24 只有全部 AC 原始主责与整改子项都有当前证据后才能标记 `resolved`。20–23 的任一 `pending`、未提交改动或不干净工作区都会阻止本票开始。
