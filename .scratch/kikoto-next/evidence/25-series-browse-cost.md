# 25：系列卡片发布期间的读取成本

实现提交`fded295066ad620cab85cfeb47c4af2a5945393a`，基点`738f2ee0`。本批仅修改卡片查询和新增合成HTTP规模回归，schema保持111。

## 触发与修复

最新隔离服务实际完整索引发布后，增量同步建立了待发布快照。包含系列的来源列表连续两次在原15秒预算内超时；第二次没有活跃后台任务。现有平面42k基准缺少这一成员与发布快照组合，不能证明真实场景通过。

合成42,624条目、600系列、6,000成员的公开HTTP回归复现旧代码 preparing 请求超过15秒。查询计划与分项计时将成本定位到系列资格相关子查询重复展开published UNION视图，单独资格查询也超过15秒。

卡片查询在每条SQL内物化一次合格成员集合，复用于来源资格、音频存在和可用性；系列成员数以集合方式判断。已有准入要求当前generation音频，移除重复音频join。短查询仍沿用物化browsable的既有契约。来源范围、身份去重、单集折叠、不可播放状态、原文短词、翻译搜索预算和准备期可见快照保持。

## 验证

全部日志位于仓库外`reports/20261107-remediation/`。本机使用Go1.26.10；静态检查使用固定golangci-lint v2.13.1。

| 检查 | 命令或入口 | 结果 | 日志 |
| --- | --- | --- | --- |
| 旧实现红测 | `KIKOTO_CATALOG_RESOURCE_SCALE=1 go test ./internal/httpapi -run '^TestCatalogSeriesBrowseRequestBudget$' -count=1 -v -timeout=30m` | exit1，preparing请求超过15秒 | `series-browse-red.log` |
| 根因 | 相同合成数据的分项查询与EXPLAIN | 系列资格查询单独超过15秒 | `series-browse-diagnosis.log` |
| 修复后规模H | 同一规模、预算和断言 | exit0；live231.506ms、preparing734.710ms、committed247.415ms；总数37228/37228/37237及页面顺序、准入、来源隔离、旧快照正确 | `series-browse-green.log` |
| 相关公开行为 | 系列、准入、发布、目录与搜索定向回归 | 61顶层测试通过，exit0，41.134秒 | `series-browse-regression.log` |
| race | 对应行为、发布和准入定向race | 21顶层测试通过，exit0，248.779秒 | `series-browse-race-regression.log` |
| 短查询 | 114,000实体、456,000译文、20样本、3预热，现有测试入口 | 五组页面与预算总数p95均≤100ms，最大80.509ms，严格门槛检查通过 | `series-browse-short-query-strict.log` |
| 静态与隐私 | format、vet、lint、sensitive-check、tracked-evidence-check | exit0，lint0issues | `series-browse-static.log`、`series-browse-lint-staged.log`、`series-browse-privacy.log` |

规模H沿用现有资源回归的opt-in入口，15秒客户端边界与完整样本保持，实际执行不带race插桩。Windows modernc SQLite插桩的中间版本规模超时保留于`series-browse-race.log`，不计为通过；race行为覆盖由上表独立命令提供。主代理已核对实际差异、红绿日志、测试终态和样本参数。目标机正式p95/p99、当前镜像真实请求、完整CI与部署验收继续由最终候选完成。
