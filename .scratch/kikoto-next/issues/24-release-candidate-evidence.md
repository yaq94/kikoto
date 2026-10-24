# 24：同一候选的集成审查与发布证据

**What to build:** 将原规格、整改和并行修复整合为一个可追溯候选，完成双轴审查、当前 CI、迁移恢复、真实服务及部署回退验收。

**Blocked by:** [17：组合验收与部署回退](17-integrated-acceptance-release-plan.md)；[18](18-ci-migration-completion-state.md)；[19](19-ci-browser-reliability.md)；[20](20-catalog-sync-and-admission.md)；[21](21-translation-corpus-and-search.md)；[22](22-personal-transfer-parent-identity.md)；[23](23-retention-and-repository-contracts.md)。

**Status:** ready-for-agent
**Implementation:** pending

**依据:** [正式规格](../spec.md)、[整改计划](../review-remediation-plan.md)、[执行记录](../execution.md)。

## 验收范围

**主责:** AC-21、AC-22、AC-25、AC-26、AC-28；汇总 AC-01 至 AC-28 的实际证据。

- [ ] 所有修复以提交整合，最终候选包含另一会话的系列暂存及有界事务修复，不包含未审阅的工作区文件。
- [ ] Standards 与 Spec 双轴审查闭环；当前 SHA 的 Full CI、类型、构建、H/R/F 与必要 race 证据对应。
- [ ] 同一 SHA 完成迁移恢复、备份回退、组合负载、真实完整索引、有限真实模型与媒体 Range/定位读取。
- [ ] 部署镜像、Git 提交、schema、回滚前提和生产结果一致；真实 prune 与保留规则经验证后才可执行。
- [ ] 逐项记录未覆盖、失败与资源校准，不以历史成功、健康检查或代理报告代替当前产物验收。

**测试入口:** H、R、少量 F、有限 LIVE、完整 CI 与真实部署验证。

## Comments

本票不取代 01–23 的主责验证。未完成项必须保持 open，不得因新计划或阶段报告改成无需实现。
