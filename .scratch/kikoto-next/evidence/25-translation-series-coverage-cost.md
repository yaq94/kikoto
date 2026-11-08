# 25：发布准备期间的系列覆盖统计成本

审查基点 `440071931d5f36b333906c1bae47fb38da2d9a12`。本批保持 schema 111、原有准入条件、覆盖定义、缓存保护及 15 秒请求预算。实际端点、目录和原始响应仅保留在仓库外受限验收目录。

## 触发与修复

隔离服务的组合负载在初始化读取翻译预填状态时超过原 15 秒预算，负载未开始。分段诊断确认系列成员的相关子查询反复展开已发布视图；仅平面条目的历史规模测试没有覆盖该成本。

系列资格改为查询内一次计算的成员数及合格成员集合，覆盖和保留规则共用同一表达式。标题、成员和准入仍来自已发布投影。预算统计在一个 SQL 快照中对每行分类一次，缓存数量、持久文本和页占用分摊复用该结果，不再三次执行保护谓词。没有提高超时或重试、降低样本规模、修改迁移或改变淘汰资格。

## 验证

- 合成 H 规模回归 `TestTranslationSeriesCoverageRequestBudget`：42,624 条目、600 系列、6,000 成员、172,896 译文，存在 preparing 发布操作。原实现冷 HTTP 请求超过 15 秒，退出 1；`translation-series-coverage-red.log`。
- 只修系列集合后仍超过预算。CPU 记录中，覆盖占 3.95 秒，预算报告占 10.44 秒，其中可淘汰计数 4.92 秒、页归属统计 5.34 秒；持久文本统计尚未完成就被取消。记录 `translation-series-coverage-profile.log` 与同名前缀 `.cpu`。
- 完整修复后，同样 HTTP 请求在 9,906.748 毫秒返回 200，总语料 43,224，四种模型语言均无 missing/stale/failed；退出 0，`translation-series-coverage-green.log`。这是一项本机测量，不代替目标硬件组合验收。
- 既有系列覆盖用例增加 preparing 期间保留旧成员、提交后再改变统计的 HTTP 断言。受影响 race 回归退出 0，总 267.623 秒，覆盖低空间、配置失效、保留预算、取消、完整覆盖与实际重启；`translation-series-behavior-race.log`。
- `make backend-format backend-lint backend-vet backend-build sensitive-check tracked-evidence-check`：退出 0，lint 0 issues；`translation-series-checks.log`。后续工单／证据变更须再次运行隐私扫描。

原始日志在仓库外 `reports/20261107-remediation/`。最终候选的独立审查、Full CI、目标机组合负载和部署后验收尚未完成，本页不关闭 17、24 或 25。
