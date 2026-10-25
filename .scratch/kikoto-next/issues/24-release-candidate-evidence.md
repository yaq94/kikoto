# 24：同一候选的集成审查与发布证据

**What to build:** 将原规格、整改和并行修复整合为一个可追溯候选，完成双轴审查、当前 CI、迁移恢复、真实服务及部署回退验收。

**Blocked by:** [17：组合验收与部署回退](17-integrated-acceptance-release-plan.md)、[18](18-ci-migration-completion-state.md)、[19](19-ci-browser-reliability.md)、[20](20-catalog-sync-and-admission.md)、[21](21-translation-corpus-and-search.md)、[22](22-personal-transfer-parent-identity.md)、[23](23-retention-and-repository-contracts.md)。

**Status:** ready-for-agent
**Implementation:** pending；当前没有同一最终 SHA 的 CI、迁移、真实索引和部署证据。

**依据:** [正式规格](../spec.md)、[整改计划](../review-remediation-plan.md)、[执行记录](../execution.md)。

## 验收归属

本票只负责**发布候选证据**，不冒充任何行为票的主责。AC-01 至 AC-28 的原始主责仍按 `ticket-proposal.md` 的验收矩阵；20–23 只对其声明的整改子项负责。24 汇总原始主责、整改子项和发布证据，缺少任何一层都不能标记 `resolved`。

## 候选账本

- 记录最终 SHA、分支、镜像 digest（只留在受限运维记录，仓库只写 tag）、schema、配置指纹、提交清单；工作区以 `git status --porcelain` 为空判定（Windows 上先统一 `core.autocrlf`，不得用 `--ignore-cr-at-eol` 掩盖真实改动）。
- 明确列出 20–23 的提交；`6b4a02ec`（含 096）已在候选基线内，只记 SHA，不重复计为外部交付；不包含未审阅工作区文件。
- 每个提交记录：工单、AC 子项、命令、退出状态、证据路径、是否受环境限制。
- 历史 run 53、旧 schema（83/84/95）、旧镜像和旧 CI 只作为历史记录，不能关闭当前候选。

## 发布门禁

- [ ] Standards 与 Spec 双轴审查闭环；发现项有提交或明确未修复理由。
- [ ] 同一 SHA 的 Full CI、类型检查、构建、H/R/F/U 与必要 race 证据。Full CI 只在 `main` push 与 `workflow_dispatch` 触发，因此候选分支必须显式 dispatch 该 SHA，不能等 push 自动触发。
- [ ] 原始 AC-14 的系列证据、单集格式副本、Bonus/轨道容器和成员上限回归在当前 SHA 通过；未提交的系列修复或测试不能计入。
- [ ] 迁移链：新装、既有库升级、旧 095 变体（若存在）和恢复；`PRAGMA foreign_key_check` 0 行。
- [ ] 备份、恢复和可执行回滚路径演练；记录镜像、schema、数据库快照和回滚触发条件。仓库不含 `rollback-kikoto.sh`，演练按 `docs/development/release-rollback.md` 的手工步骤执行，并至少在隔离目录完成一次快照恢复与 `foreign_key_check`。
- [ ] 生产 Compose 没有 healthcheck，`docker compose ps` 只要求 `running`，以 `/health` 200 作为门禁；若补 healthcheck，需同步 `deploy/compose` 与回滚文档。
- [ ] 组合负载：播放中扫描、翻译、浏览、评价，音轨／队列／进度保持；低空间暂停与恢复。
- [ ] 真实完整索引：分区、目录、条目、失败、受限、未确认空目录分别报告；未完成不得称 complete。
- [ ] 有限 LIVE：真实 Index 翻译、匿名媒体 Range／定位读取；记录版本、请求、耗时和脱敏文本。
- [ ] 少量非视觉 F：文本、操作、请求、持久状态和媒体功能；截图采集关闭，既有视觉测试不改写。
- [ ] AC-25 播放中音轨／队列／进度、AC-26 进度证据、AC-27 出站边界、AC-28 截图关闭与既有视觉测试不改写全部列入矩阵。
- [ ] 部署后真实服务验收：健康、登录、内容翻译、目录、搜索、个人记录、媒体 Range；失败时按已验证方案回滚并继续。

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

本票的可执行入口是：`make ci-style`（已接入 tracked-evidence 检查）、`make ci-backend`、`make ci-frontend`、`make ci-local`、`make production-smoke`、`workflow_dispatch` 的 Full CI、以及手工部署与隔离回滚。仓库没有部署 workflow、候选账本模板和组合负载脚本；这些必须在执行本票时补齐或明确写出替代步骤，不能按原文假装存在。

## Comments

17 与 24 是单向依赖：17 完成原始功能组合，24 负责最终同候选证据；17 不再反向依赖 20–24。24 只有全部 AC 原始主责与整改子项都有当前证据后才能标记 `resolved`。20–23 的任一 `pending`、未提交改动或不干净工作区都会阻止本票开始。
