# 21：有效译文覆盖与按语言完整搜索

**What to build:** 持久译文只对当前有效语料提供正确性；覆盖进度、失败恢复、目标语言搜索与短查询均使用一致定义。

**Blocked by:** [02：浏览内容翻译与缓存](02-content-translation-cache.md)；[03：简繁转换](03-traditional-chinese-conversion.md)。

**Status:** ready-for-agent
**Implementation:** pending

**依据:** [正式规格](../spec.md)、[整改计划](../review-remediation-plan.md)、[ADR-0005](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)。

## 验收范围

**主责:** AC-03、AC-06、AC-08；**共同:** AC-25、AC-26、AC-28。

- [ ] 软预算不删除当前有效搜索所需译文；原生行、当前译文、superseded 行与可重建缓存的保留语义明确。
- [ ] 覆盖统计排除退役和历史行，核对当前原文、模型及模板；连续失败、淘汰和配置变化不能显示为完整覆盖。
- [ ] 目标语言搜索不混入原文；`origin` 保留原行为，简繁搜索保持归一化规则。
- [ ] 1–2 字查询覆盖完整有效集合并有明确工作量上限，不能仅扫描每语言最新 50,000 行。
- [ ] H/R/F 验证远程与本地、重启及动态内容；在生产驱动下取得真实规模迁移和 p95 证据。

**测试入口:** H、R、少量 F、必要 U；真实模型与规模证据由 24 汇总。

## Comments

有界事务修复由并行实施会话交付后整合，本票不能以写锁修复替代覆盖与搜索正确性。
