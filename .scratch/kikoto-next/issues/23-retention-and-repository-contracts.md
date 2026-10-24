# 23：个人引用保留与仓库工程规范

**What to build:** 统一清理计划与个人稳定引用保留规则；使数据模型、回滚说明、测试夹具和证据符合仓库公开边界。

**Blocked by:** [10：来源生命周期与记录保留](10-source-lifecycle-retention.md)；[ADR-0006](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)。

**Status:** ready-for-agent
**Implementation:** pending

**依据:** [正式规格](../spec.md)、[AGENTS.md](../../../AGENTS.md)、[ADR-0006](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)。

## 验收范围

**主责:** AC-23、AC-26；**共同:** AC-13、AC-19、AC-24。

- [ ] 当前 dry-run 保留仍带 `personal_transfer_ref` 的 source-only 卡片，旧 D-17 删除计数不得用于 apply。
- [ ] 个人记录、来源身份与恢复路径不因清理、翻译淘汰或退休而丢失。
- [ ] 译文模型、保留语义、回滚检查与真实 Compose 能力一致；095 的既有环境执行状态有记录依据。
- [ ] 公开文档不含真实路径、作品记录或日志；测试使用规定的合成身份构造函数，文档只描述当前行为。

**测试入口:** H/R 的保留回归、文档与敏感信息检查、合成 dry-run；实际生产清理由 24 另行验收。

## Comments

历史证据保留当时版本、命令、退出状态和缺口，不能因脱敏改写为当前候选已通过。
