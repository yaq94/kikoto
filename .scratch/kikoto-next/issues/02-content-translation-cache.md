# 02: 浏览内容翻译、原文与有界缓存

**What to build:** 用户切换显示语言后，当前页面与随后加载的标题、简介、系统标签、目录显示名自动翻译；可查看原文和重试，重复浏览复用缓存。

**Blocked by:** [01：管理员翻译配置与 Index 试译](01-translation-admin-config.md)

**Status:** ready-for-agent

**Implementation:** resolved；本地实现与 H/F 已验收；生产部署验收见 17。

**依据:** [正式规格](../spec.md) · [工单总览](../ticket-proposal.md)

## 验收范围

**主责:** AC-03、AC-04、AC-05、AC-06、AC-25

**共同:** AC-27、AC-28

AC-25 负责翻译队列、译文缓存和低空间行为；组合负载由 17 验证。

## 验收清单

- [x] 两用户分别保存显示语言和界面语言，偏好互不改写；共用管理员服务配置，当前版本、来源与文件身份不变。
- [x] 优先使用当前作品与版本已有的目标语言字段，缺失才生成；四类内容及翻页、详情、展开目录后新增的文字行为一致。
- [x] 社团、声优名称、个人标签和备注保持原文且不外发，标题或简介内的受保护名称也需保持；原文入口可用，译文按文本呈现。
- [x] 等待或失败时保留原文和状态；成功默认显示译文；连续换语言、离页或会话变化后，旧结果不能覆盖新状态。
- [x] 缓存区分原文、目标语言、模型与模板版本并遵守原内容权限；相同在途请求去重，变化后不误复用。
- [x] 翻译并发、队列、超时按配置生效，低空间暂停新增缓存；译文保留规则以 21 的软预算合同为准。`translationPersistentCorpusSQL` 把当前 title／description 行排除在淘汰之外，`translationBudgetReport` 超预算时只报告 `over_budget`；证据：`TestContentTranslationSharesBudgetDeduplicatesAndCancelsQueue`、`TestContentTranslationInvalidatesOnModelChangeAndReportsLowDiskPause`、`TestContentTranslationStoresInFlightResultsWhenTheSoftBudgetShrinks`、`TestContentTranslationRetentionKeepsCurrentCorpusOverBudget`、`TestContentTranslationRetentionKeepsCurrentCorpusUnderByteBudget`、`TestContentTranslationRetentionChecksCurrentWorkAndSeriesText`（2026-10-26 全部通过）。

## 测试与交付边界

**测试入口:** H、R；语言、动态内容和迟到响应复用 F；必要纯规则补 U。

H 使用真实 HTTP 路由、会话与迁移 SQLite；R 实际关闭并重启同一应用；F 仅运行少量纯功能用例，关闭截图采集，不做截图、像素、布局或人工视觉验收；U 仅补充必要的纯状态规则。

遵守工单总览的[共同完成要求](../ticket-proposal.md#共同完成要求)，复用既有测试入口；不新增测试技术栈或安装授权。

本票验收证据须对应实际产物，相关文档同步并提交、推送 Git。工单发布不代表功能实施或生产授权。

## Comments

- 六项清单与主责/共同 AC 的当前 H、R、F、U、红绿及构建证据见
  [02 验收记录](../evidence/02-content-translation.md)。production code 合入 01、04、06
  后完成组合回归，F 3/3，前端 867 项通过；没有新增依赖或数据库迁移。
- 未登记 OpenList catalog 的字段解析和翻译已在[02/07 集成复验](../evidence/02-07-integration.md)接通；共享 loader 在缓存前复验来源，标题与真实路径对应目录均可翻译。03 负责缺失繁体字段的生成转换，17 负责组合负载与部署。
- 两份合成缓存的删除被自动工具策略拒绝，按主代理决定保留为未跟踪文件并不提交；
  本票运行服务已全部结束。该清理限制及未执行项目在验收记录明确列出。
