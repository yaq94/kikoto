# 25：翻译与浏览器缓存的主代理验收

代码检查基点为 `45c92904`，加上同次提交中的会话依赖、预取在途键、CI 分组和文档收尾。测试运行时没有并行源码写入。SQLite 仍为 105；IndexedDB 采用兼容保留旧缓存的 v2 元数据账本。

## 实际验证

| 验证 | 结果 | 原始日志组 |
| --- | --- | --- |
| single/batch 在途模型切换、系列与单卡准入、恢复系列评价的真实 HTTP 组合 | exit 0，9.242 s；含合格、不合格、旧扫描代次、missing、relocation、禁用来源元数据和个人恢复对象 | `root-batch2-http.log` |
| 浏览器 batch 并发、缓存、预取规则 | 3 文件 / 38 用例通过，exit 0 | `root-batch2-units` |
| 前端 lint、E2E 类型检查、生产构建 | exit 0；lint 零警告零错误 | `root-batch2-build.log` 及对应命令输出 |
| 新版本与缓存测试通过统一 fixture 入口 | 2 组 / 5 用例通过，零 skipped，exit 0 | `root-translation-e2e` |
| 完整翻译 bundle 的组合复验 | 4 组 / 14 用例通过，零 skipped，exit 0 | `root-translation-combined` |
| CI 计划脚本回归 | 6 用例通过，exit 0 | Node test 输出 |

原始报告均在受限过程目录的 `20261107-remediation` 组中。首次命令误用了 `--groups`，CLI 明确拒绝；使用文档中的 `--group` 后才执行上述有效测试，不把参数失败记为测试通过。

## 已覆盖的行为

- 同一 SPA 从已缓存详情进入设置改模型后返回，获取当前版本并生成正确结果；另一管理员改配置时，返回页面也更新版本。
- 迟到的旧模型响应不能把已知版本改回旧配置，浏览器持久缓存保留新版本结果。
- 字段按当前用户、语言及内容独立处理；动态目录、翻页、原文保护、失败重试、简繁复用与再次访问缓存仍通过。
- 跨 flush 共用传输并发；排队取消及失败后释放由单测覆盖。
- 实际 Chromium IndexedDB 验证 LRU、用户与版本隔离、v1 缓存迁移、字节上限、并发写入、每事务有界维护及零正文全量扫描。

新增 `translation-version` 与 `translation-cache` 已加入 `run-e2e-fixtures` 的翻译 bundle，并从 mocked 范围排除；因此 Full CI 不会缺少对应 fixture 而跳过版本测试。全部本期浏览器用例关闭 screenshot、trace 和 video，不引入视觉门禁。

本证据只关闭本批已验证行为，不能替代 25 的扫描、staging、语料统计与保留、基准程序整改，也不能替代最终同一 SHA 的 Full CI、目标机测量和部署验收。
