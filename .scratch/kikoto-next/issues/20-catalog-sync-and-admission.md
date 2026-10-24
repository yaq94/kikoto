# 20：持续目录增量与统一准入

**What to build:** 首次全量与后续增量分工明确；partial 的失败范围可重试，已成功范围继续发现变化。D-16 的三条准入规则在入队、发布和 API 一致生效。

**Blocked by:** [08：完整来源索引与恢复进度](08-recoverable-full-source-index.md)；[09：每日更新与已有扫描复用](09-daily-index-update.md)。

**Status:** ready-for-agent
**Implementation:** pending

**依据:** [正式规格](../spec.md)、[整改计划](../review-remediation-plan.md)、[ADR-0005](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)。

## 验收范围

**主责:** AC-08、AC-21、AC-22；**共同:** AC-25、AC-26。

- [ ] 持续 partial 时，失败、受限及未确认空目录分别重试，已完成分支仍通过目录优先增量发现新文件。
- [ ] 未知范围不判缺失；同一来源的全量、增量和手动更新不重叠，重启保留检查点。
- [ ] 每日与周日 03:00（Asia/Shanghai）分别触发增量与全量对账；触发不等于扫描完成。
- [ ] 无音频、文件数大于 5,000、非音频大于 2,000 且音频少于 5 均不发布；三层一致拦截，合格多轨作品仍可用。
- [ ] H/R 覆盖真实持久任务与受控时间，保留速率、取消、低空间及个人记录回归。

**测试入口:** H、R，必要的纯规则 U；真实覆盖由 24 汇总。

## Comments

本票保留既有目录优先算法，不用祖先 mtime 跳过整棵子树，不用搜索结果代替完整遍历。不能将 resume 同一 generation 当作跨 generation 增量。
