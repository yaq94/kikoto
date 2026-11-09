# 24：5f732e6f 发布候选账本

实施状态：Full CI 未通过，生产未部署。候选为 `5f732e6feb0ac6cbbc6d2d3962ec20582b22a786`，集成分支为 `codex/kikoto-catalog-remediation`，镜像为 `kikoto:next-5f732e6f`，schema 111。镜像由该 SHA 的 Git archive 构建，退出 0；digest、配置及主机路径仅保存在受限运维记录。2026-11-09 核对主仓工作区干净、提交已推送 origin。

## 提交与审查

总基点为 `53d905e1d212e85d78cb95687044b96b4fc3ebae`。既有整改清单与 AC 主责见 [738 账本](24-candidate-738f2ee0.md)。本候选包含目录优先准入计数 `72778052`、关联位置查询 `de28db0c`、投影测试连接准备 `aff8438d`、翻译磁盘统计限定 `98ae60e8`、历史状态校正 `677daae1` 和索引发布后列表刷新 `5f732e6f`。001–111 不修改。

### Standards

主代理核对实际差异及仓库规范：磁盘统计保留原表与索引分类，仅限定 dbstat 扫描范围；投影测试保留真实写锁、500ms 及时间戳断言；列表刷新由现有进度接口的 runId/finishedAt 驱动，保留取消与旧请求序号保护。格式、lint、类型、构建和隐私检查的定向证据见各专题。没有确认新增规范违反。

本批两次独立 Standards 代理在执行前被自动审核以 `possible cybersecurity risk` 拒绝，未产出结论；不能记为独立通过。只读 race 成本诊断代理另被 Codey 工具分类门禁阻止，未取得文件证据，已由主代理接手。宿主兼容约定允许代理不可用时由根查证，不隐藏该限制。

### Spec

独立 Spec 审查分别覆盖 `de28db0c...677daae1` 和 `677daae1...5f732e6f`，均未发现缺失、范围扩张或错误行为。前者确认名称集合及预算不变；后者确认计数不变的 complete/partial、首次终态及同 run 恢复结束仍刷新，旧响应不覆盖新列表。各自 diff 检查通过；没有把只读审查当成运行时验收。

## 当前门禁与真实证据

本地原始日志保存在仓库外 `reports/20261107-remediation/`；生产数据和端点只保存在受限运维目录。

| 检查 | 命令或入口 | 结果与证据 |
| --- | --- | --- |
| Full CI | workflow_dispatch run `42545558313` | completed/failure，29 个 job success；race7/8 与 Gate failure。`github-5f732e6f-jobs-current.json`、`github-5f732e6f-race7.log` |
| 后端普通行为、覆盖、静态与构建 | Full / Backend coverage、static、source-index contract | 全部 success；race 其余七片 success；第七片尚未通过，不声明完整后端门禁通过 |
| 前端、类型、构建 | Full / Frontend；本地 ci-frontend | success；本地 169 文件、994 测试与构建通过，见[发布刷新证据](25-index-publication-refresh.md) |
| 样式及文档 | Full / Style | success；本账本变更另行执行提交前检查 |
| 可移植集成和客户端构建 | Full / Smoke、Development Compose、Android、iOS、各 E2E | 全部 success；没有声称 Windows 执行了完整 make ci-local |
| 新装、历史迁移及旧 095 变体 | Full 普通迁移测试与 Smoke | success；未改编号迁移或新装基线 |
| 实际升级及恢复 | 当前镜像的隔离 105→111→105 演练 | retry 轮 exit 0，新旧应用实际启动、API 回读、六表逐行一致、quick_check=ok、FK=0，备份前故障可恢复旧服务 |
| 组合负载 | scripts/mixed-load.mjs，60 秒、8 并发、单请求 15 秒 | exit 0，924 请求、154 次 Range 206，零超时、错误、意外状态或验证错误；`target-5f732e6f-mixed-load.json` |
| 真实列表、语料与个人数据 | 现有隔离服务 HTTP | 第二次完整读回 exit 0；语料 total 42,090，runtime 与 prefill 有效集合一致；两用户 8 份记录跨 catalog/series/work 一致；`target-5f732e6f-live-readback-second.json` |
| LIVE 模型与简繁 | 实际试译及两个有限 LIVE 契约 | HTTP200，日文中性样例→简体正确；首次简繁生成 1,224ms，后续缓存切换 4ms；原生简体转换保留名称，测试 exit 0 |
| LIVE 媒体 | 编号、无码各两段 1024B 定位读取 | 四段全部206，Content-Range偏移正确；同一读回报告 |
| 非视觉浏览器 | IAB 加载新主包并恢复实际音轨 | 主包 index-BHq4Jy5U.js；重载保持353.103807秒与paused，恢复播放、导航设置/库后同stream推进到433.283481秒，无媒体错误；最终主动暂停434.009872秒。无截图、trace或video |
| 真实完整索引 | 目录扫描、发布及清理 | 8 分区、14,500 目录、14,473 完成，待处理/失败/受限均0，42,031索引条目；27空目录待确认、4编码问题，终态partial |
| 增量恢复 | 服务重启后已有检查点自动续跑 | 退避结束后继续原任务，最终partial；发布准备状态完成并清理，manual_pending清零。失败范围需保留，不能称全站complete |
| 部署后验收 | 当前生产服务 | 未执行；生产仍为 kikoto:next-53d905e1 / schema105 |

Full race7 在包计时 35 分钟结束时，完整索引测试仅运行 1分17秒；CI 没有逐测试成功耗时，终止栈不能代表整包成本。根保持原分片集合、断言和35分钟期限，另采184个httpapi测试的逐项JSON与CPU profile，定位后再修复。

实际演练第一轮因剩余空间不足10GiB在升级前退出1；清理已完成且无挂载的旧演练数据后原门槛重试通过。真实runtime第一次超过15秒，保留失败报告；第二次8,731.125ms通过，未延长超时。两份旧个人期望已由实际关联/拆分验收取代，读回脚本仅验证其余8份，不复用失效期望。

## 性能与 AC 覆盖

本批磁盘统计规模H保留42,624条目、600系列、6,000成员、172,896译文及15秒预算，9,396.139ms通过；只读Go磁盘统计8.153秒→1.226秒，字节归属不变，见[磁盘统计证据](25-translation-disk-scan.md)。42k浏览、5,001文件卡拒绝、短词/编号/标题/FTS的既有量级及阈值见[6f账本](24-candidate-6f516e31.md)，这些查询未在本批变更；它们不能替代当前Full终态。

组合负载p95：浏览890.02ms、搜索798.17ms、媒体1,212.17ms、评价写749.28ms、读853.35ms、prefill718.14ms。该负载含真实网络和并发，不能拿其延迟冒充受控42k SQL门槛。

AC-01至AC-28原始主责和整改子项保持[既有矩阵](24-candidate-738f2ee0.md#5-ac覆盖)。本候选普通H/R/U、F、LIVE与恢复结果按上表记录；完整race、最终生产调用链、部署后配置和个人数据保持仍待签收，17/24/25不关闭。

## 部署与回滚

部署前必须重新检查资源、取得一致DB/WAL/SHM与配置快照、校验并记录镜像。现有部署脚本及rollback helper已由本候选实际演练。新镜像升级105→111；旧镜像必须配105快照，不得直接读取111。健康门禁包含HTTP健康与Compose已有healthcheck，并另做真实功能验收。当前未部署，未触发生产回滚。

## 限制与证据保护

上游未确认空目录和编码问题保留原状态，不据抽样或partial声称全站完整。独立Standards审查受宿主拒绝；Full第七race分片未通过；自动匹配后台仍推进、无重试错误，终态待核实；生产部署及后续验收未执行。所有真实路径、编号、私人文字、端点、cookie、配置及原始生产日志均留在仓库外。
