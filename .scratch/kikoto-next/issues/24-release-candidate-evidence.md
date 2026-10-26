# 24：同一候选的集成审查与发布证据

**What to build:** 将原规格、整改和并行修复整合为一个可追溯候选，完成双轴审查、当前 CI、迁移恢复、真实服务及部署回退验收。

**Blocked by:** [17：组合验收与部署回退](17-integrated-acceptance-release-plan.md)、[18](18-ci-migration-completion-state.md)、[19](19-ci-browser-reliability.md)、[20](20-catalog-sync-and-admission.md)、[21](21-translation-corpus-and-search.md)、[22](22-personal-transfer-parent-identity.md)、[23](23-retention-and-repository-contracts.md)。

**Status:** ready-for-agent
**Implementation:** in-progress；候选 `2ad508e8` 已冻结，Full CI run `40341543004` success，schema 105 已部署且 `/health` 200。候选账本见 [24-candidate-2ad508e8.md](../evidence/24-candidate-2ad508e8.md)，模板为 [24-candidate-ledger.md](../evidence/24-candidate-ledger.md)；`scripts/mixed-load.mjs`、`scripts/production-smoke.mjs` 与 `scripts/rollback-kikoto.sh` 已提交，组合负载与 LIVE 待记录。

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

- [ ] Standards 与 Spec 双轴审查闭环；发现项有提交或明确未修复理由。
- [ ] 同一 SHA 的 Full CI、类型检查、构建、H/R/F/U 与必要 race 证据。Full CI 只在 `main` push 与 `workflow_dispatch` 触发，因此候选分支必须显式 dispatch 该 SHA，不能等 push 自动触发。
- [ ] 原始 AC-14 的系列证据、单集格式副本、Bonus/轨道容器和成员上限回归在当前 SHA 通过；未提交的系列修复或测试不能计入。
- [ ] 迁移链：095–105 连续、`compat/105_dev.sql` 与迁移后 schema 一致；84→105 演练已完成（21 个迁移、总耗时 20,840 ms、`quick_check` ok、`PRAGMA foreign_key_check` 0 行）。新装、旧 095 变体与重复启动仍待在同一 SHA 复验。
- [ ] 备份、恢复和可执行回滚路径演练；84 快照配旧镜像 `/health` 200，105 库配旧镜像启动失败（schema newer than binary）。仓库已含 `scripts/rollback-kikoto.sh`；快照路径、校验和与回滚触发条件仍待写入受限运维记录。
- [ ] 生产 Compose 没有 healthcheck，`docker compose ps` 只要求 `running`，以 `/health` 200 作为门禁；若补 healthcheck，需同步 `deploy/compose` 与回滚文档。
- [ ] 组合负载：播放中扫描、翻译、浏览、评价，音轨／队列／进度保持；低空间暂停与恢复。`scripts/mixed-load.mjs` 已提交，仍需在候选上运行并保存 JSON 摘要，不接受只有历史人工观测。
- [ ] 真实完整索引：分区、目录、条目、失败、受限、未确认空目录分别报告；未完成不得称 complete。预算：2 vCPU／2 GiB 或等价实例、约 3–5 小时、需要真实 OpenList 凭据；允许 partial，但必须列出失败与未确认空目录。
- [ ] 有限 LIVE：真实 Index 翻译、匿名媒体 Range／定位读取；记录版本、请求、耗时和脱敏文本。需要 `KIKOTO_TRANSLATION_LIVE_CONFIG`、`KIKOTO_OPENLIST_LIVE_URL`、`KIKOTO_OPENLIST_LIVE_FILE`；缺失时 skip 不计通过，并记录 skip 原因。
- [ ] 少量非视觉 F：文本、操作、请求、持久状态和媒体功能；截图采集关闭，既有视觉测试不改写。
- [ ] AC-25 播放中音轨／队列／进度、AC-26 进度证据、AC-27 出站边界、AC-28 截图关闭与既有视觉测试不改写全部列入矩阵。
- [ ] 部署后真实服务验收：`/health` 200 `v0.8.3` 已取得；登录、内容翻译、目录、搜索、个人记录、媒体 Range 仍待逐项记录。现有 `production-smoke.mjs` 只覆盖健康、静态资源、认证、本地扫描与媒体 Range，必须扩展到翻译、目录、搜索、个人记录，或把手工验收命令写进账本。
- [ ] 性能门禁：42k 卡浏览 p95 ≤ 300 ms、p99 ≤ 800 ms（warm、≥20 样本），已取得 p95 199.192 ms（p99 226.981 ms）；5,001 文件卡拒绝路径 p95 0.593 ms；译文 3 字以上与 1–2 字 p95 各 ≤ 100 ms（1–2 字按结果预算，卡片页与同预算总数 p95 均 ≤13 ms、零命中 48 ms）；编号等值 0.018 ms、目标语言标题整串等值 1.37–1.73 ms。

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
