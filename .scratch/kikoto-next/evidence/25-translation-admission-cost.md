# 翻译字段读取成本

基点 `d035ce8e`；本证据随标题／简介读取修复提交。真实 HTTP、会话和迁移 SQLite 保留，外部模型使用合成网络桩。原始日志在仓库外 `reports/20261107-remediation/`。

## 触发与修复

`3f87efda` 的 Full run `42508320019` 中，`TestContentTranslationSharesBudgetDeduplicatesAndCancelsQueue` 在原 2 秒观察期限内仍得到 active=1、queued=0、readers=1；前一个候选也发生同样失败。单独运行能通过，不能据此关闭 CI 失败。

CPU 与同步采样确认，进入队列之前会为一个标题装配完整作品详情，涉及封面、来源状态、本地目录、购买信息和元数据同步。三个翻译请求的本地 CPU 采样中，字段读取 790 ms，完整详情占 730 ms；单连接争用累计等待约 2.88 秒。新增公开 HTTP 成本回归对原文标题实际计数为 52 条数据库语句，超出 24 条资源预算而失败。

标题与简介只加载文本、现成语言字段、手工修改和受保护名称；版本选择沿用 `metadatatitles`，保留所选版本文本、原文及现成目标语言匹配。标签和目录路径不改。新成本回归为 10 条语句；不存在跨请求缓存、延长时限、改变队列连接数或降低样本。

计数夹具补充将 `BeginTx` 原样转发给已有 SQLite driver，使该观察工具支持生产读取路径使用的只读事务；不模拟事务行为。

## 验证

- `go test -run '^TestContentTranslationTitleReadHasBoundedQueries$' -count=1 -v ./internal/httpapi`：修复前退出 1，52 条语句；`translation-field-budget-red.log`。
- `go test -count=1 -run '^TestContentTranslation' -v ./internal/httpapi`：退出 0，64.798 秒；`translation-field-behavior.log`，包括现有版本、权限、名称、简繁、搜索和缓存行为。
- `GOMAXPROCS=2 go test -race -count=3 -run '^TestContentTranslation(SharesBudgetDeduplicatesAndCancelsQueue|TitleReadHasBoundedQueries)$' -v ./internal/httpapi`：退出 0，29.207 秒；每次标题均 10 条语句，原队列用例三次均通过；`translation-field-budget-race.log`。首次包含真实 schema 镜像初始化，后两次队列用例 1.61/1.86 秒。
- 手工标题／简介、受保护名称、现成版本与简繁，以及计数夹具关联调用方的定向 race：退出 0；`translation-field-protection-race.log`。手工声优和社团名称未发送模型，返回文本正确恢复名称。
- format、vet、build 与固定版本 lint：退出 0，0 issues；`translation-field-static.log`。

## 发布边界

本地证据不等于原 CI 失败已关闭。修复后的同一提交仍须完成 Full CI、实际服务与部署验收；001–111 迁移未改。

## Spec 复核修正

独立 Spec 审查 `3f87efda..a1c73fbb` 发现：无发行版本但有手工标题时，公开详情仍提供带原简介的展示版本；同时设置手工简介会让新的读取遗漏原简介候选。公开 GET 先确认原简介仍在 `metadataPresentation.variants`，再翻译该文本，修复前返回 409，见 `translation-visible-description-red.log`。

仅在该展示分支存在时补入原简介，手工简介和原文语义保持。5 项现成版本／手工文字／名称保护／成本 race 回归退出 0，25.784 秒，见 `translation-visible-description-green.log`；标题仍为 10 条语句。Standards 独立代理在执行前被自动审核拒绝，未产出结论；主代理核对本批边界、语句事务生命周期、现有调用方及文档规则，未确认新增问题。该主代理核对与独立 Spec 复核分别记录，不将拒绝算作审查通过。
