# 25：现行实现的功能与资源审查整改

**What to build:** 按现行规格修复重新审查复现的目录清理、恢复单集身份、译文配置版本、系列准入及有界资源处理缺陷，并用能在缺陷代码上失败的合成回归验证。原始工单要求与历史证据全部保留。

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent

**Implementation:** in-progress；功能、浏览器生命周期、106 翻译资源、107 目录发布、108 持久分组与有限扫描、109 查询亲和性、排除任务、退役别名及派生读取整改均已集成并取得定向证据。刷新有界等待和共享关联保存屏障已修复完整门禁发现的回归。最终同候选双轴审查、完整门禁、目标机组合性能、恢复复核及部署验收仍未完成。

**依据:** [正式规格](../spec.md)、[20 准入合同](20-catalog-sync-and-admission.md)、[22 父级恢复](22-personal-transfer-parent-identity.md)、[23 清理保留](23-retention-and-repository-contracts.md)、[13 共享关联](13-shared-link-conflict-review.md)、[14 自动匹配](14-high-confidence-work-matching.md)、[ADR-0004](../../../docs/decisions/ADR-0004-work-identity-and-personal-state.md)、[ADR-0006](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)。

## 验收范围

当前审查基点是 `53d905e1d212e85d78cb95687044b96b4fc3ebae`。旧 `resolved` 和旧绿色日志仅证明对应历史产物，当前验收以本票证据中的提交及命令为准。生产迁移头仍为 105；集成分支包含 106 译文 revision、107 目录 publication、108 分组及 finite 检查点、109 发布投影查询亲和性及尺寸索引，已发布迁移保持不变。107 的初始阶段记录见[阶段证据](../evidence/25-sync107-publication.md)，后续集成见下列证据及 Comments。

## 验收清单

- [x] 第一批：清理准入只计当前发布 generation 的非 folder 文件；阈值等值允许；每个删除事务复核准入，批次间变为合格的卡片保留；个人评价与稳定引用不丢失。
- [x] 第一批：真实 v2 导入的单集保持评价层级；显式父级与旧格式 episode kind 均不能和独立作品跨层关联；人工与自动匹配共用该规则；同层关联、重启后拆分与导出保留父级及记录。
- [x] 单条及 batch 翻译结果绑定实际生成或命中时的配置快照；在途切换不把旧模型结果标为新版本。
- [x] 系列评价及翻译统一检查合格成员，并保留个人恢复的无媒体父系列及既有 missing 边界。
- [x] 同一 SPA 缓存页进入设置改模型后返回，复现并处理版本刷新；他人配置变化和迟到响应不能回写旧版本。
- [x] 单集 staging 每批消费后正确推进；101 集总 staging 量及事务数有回归，不只检查单事务上限。
- [ ] 增量同步按页及有界批次持久化检查点，取消、低空间与恢复有效，完整集合与最终可见性保持；107/108、分组、finite scan、exclusion、retire 与派生读路径均已集成，最终生产调用链及同候选组合复核仍待完成。设计依据与阶段结果分别见[107 设计](../evidence/25-sync107-design.md)、[107 实施证据](../evidence/25-sync107-publication.md)。
- [x] 前端翻译跨 flush 保持共享并发上限，取消和失败后资源释放可验证。
- [x] 翻译基准查询失败明确失败并非零退出，不以 0 ms 成功输出；保留每项错误；相关夹具使用保留编号构造器。
- [x] 增量 probe 不重复扫描已完成前缀，持久游标与失败恢复仍覆盖完整集合；按资源基线复测规模曲线。
- [x] 已完成译文语料的覆盖检查避免反复完整物化，原文、配置、准入或实体变化仍可靠恢复预填；统计仍依据当前有效语料。定向 H/R、race 与规模结果见[翻译资源证据](../evidence/25-translation-resource.md)，107 发布失效和最终组合验收另列。
- [x] 写后保留维护不在交互返回路径同步扫描全表；预算与历史淘汰有界、可恢复，当前搜索语料与个人记录保持。上述资源证据及[维护取消回归](../evidence/25-translation-maintenance-cancel.md)记录实际测试。
- [x] 浏览器缓存清理有界且不重叠，避免每次写入后重新复制、编码全部缓存；上限、LRU 和账号／内容／配置隔离保持正确。
- [x] 后续独立复审：flush 后加入的重复读者计入取消所有权，已完成读者不阻止剩余请求取消；IDB 失败不留下未处理 transaction rejection，禁用或 blocked 后迟到成功关闭连接。39 个公共边界 U 与 5 个原生 IDB F 通过，含多 tab 精确预算账本，见[浏览器生命周期证据](../evidence/25-browser-lifecycle.md)。
- [x] 独立复审 F2：排队分块失去全部读者后不再发送；取得共享槽后重新检查，保留仍有效或重新加入的读者。见[取消分块证据](../evidence/25-cancelled-chunks.md)，主集成工作区 30 项 U 复跑通过。
- [x] 独立复审 TR2：维护等待配置锁不阻止生命周期取消，跳过的维护下个 tick 可继续；见[配置锁取消证据](../evidence/25-translation-maintenance-cancel.md)。
- [ ] 最终集成重新复验 106–110 的语料、目录改名、发布指针与 maintenance 失效依赖，完成最终 race、规模资源、完整 CI、恢复演练及部署验收。

## 测试与交付边界

第一批采用 H（真实 HTTP、鉴权、迁移 SQLite）及 R（关闭应用与数据库后重新打开）和 catalogprune 的公开 BuildReport/Apply 边界。不调用生产、真实来源或真实模型，不读取凭据，不隐式下载媒体。当前批次不修改 UI，不执行截图、像素或布局验收。后续浏览器回归沿用已授权的纯功能 F 边界。

第一批证据见 [25 审查整改证据](../evidence/25-review-remediation.md)。第二批服务端版本绑定、系列及单卡翻译准入见 [第二批交接证据](../evidence/25-translation-backend.md)；前端实现已保留在同分支，尚不据此关闭本票前端清单。原始日志保留在仓库外的工作区 reports/20261107-remediation 目录。发布账本、完整候选 CI、生产验收由主代理另行记录，本票不替代它们。

## Comments

2026-11-08：`5f78ae45` 修复完整索引及增量恢复中的整根旧拆分卡退役，新增 110 持久游标；1,103 张旧卡的单事务 entry 写入由 1,104 降至 64，故障、低空间和取消后实际重启恢复保留个人评分，见[退役资源证据](../evidence/25-split-retirement-bounds.md)。完整 CI 的工作表观察和拆分保存屏障修正及未解决 race 问题见[完整门禁续验](../evidence/25-ci-publication-and-split.md)。本票继续 in-progress。

2026-11-08：109 将已发布投影的 work_id 固定为整数亲和性，使译文插入使用索引查询；50,001 条原回归由超时恢复为 8.31 秒通过。排除恢复拥有者、series-only 清理与尺寸查询修复见[复审修复](../evidence/25-final-review-fixes.md)。后续完整门禁暴露的同来源刷新兼容性和共享关联保存竞态、2747e05e 的真实隔离恢复演练见[完整门禁修复](../evidence/25-full-ci-corrections.md)。这些定向证据不关闭最终同候选门禁。

2026-11-08 集成 `32dc7fe5`：声优、社团、歌词、来源检查时间与清理资格使用已发布投影，定向 H/R/race 证据见[派生读取](../evidence/25-derived-publication.md)。分组与有限扫描见[108 证据](../evidence/25-grouping-finite.md)，排除见[后端](../evidence/25-exclusion-resource.md)及[任务界面](../evidence/25-exclusion-task-ui.md)。前端完整门禁及开发依赖审计见[前端门禁](../evidence/25-frontend-dependency-gate.md)。下列记录保留各阶段当时的未完成项，不表示这些项尚未实现；本票最终签收条件以当前清单为准。

2026-11-08 后续集成：`ac66e07d` 合入 publication 两项复审修复、108 持久分组/finite、exclusion 有界后台及任务界面；交叉 H/R 和格式/lint 通过。退役别名 150 行红测、分批故障后真实重启及来源可用性标志回归见[主代理尾部核验](../evidence/25-root-tail.md)。其他衍生读取、完整候选门禁及实际部署继续待办，不能据此关闭本票。

分组与 finite 输入的独立批次采用108，设计和定向证据见[持久分组与 finite 输入](../evidence/25-grouping-finite.md)。本批不关闭上述107整体检查项；exclusion、retire、派生readers与最终同候选验收仍由集成批次处理。

2026-11-08：集成 `30cd671d` 包含第三批算法、取消/IDB 修复、106 翻译资源、维护取消、前端空分块取消及 107 阶段实现。两项个人导出失败已在原基点证实为无来源/音频夹具违背准入合同；仅补有效导出端夹具，全部原断言与空库恢复保留，见[个人系列回归](../evidence/25-personal-series-regression.md)。根代理当前验收与剩余范围见[集成记录](../evidence/25-integration-20261108.md)；本票不据阶段绿灯关闭。

第三批算法证据见 [25 算法整改](../evidence/25-algorithms.md)。本批完成 staging、probe 路径遍历和基准错误传播；整目录分页事务、覆盖统计与写后保留仍待实施，后续前端复审问题另行修复。上述前端已勾选项记录此前批次的验证，不代表后续新发现已关闭。

2026-11-07：主代理依据 Spec/Standards 两轴报告授权整改。首次创建时 issues 最大号为 24；既有 `evidence/25-live-browser.md` 是历史证据文件，原样保留。主代理将首批交接限定为两项 P1，其余明确要求留在本票待续。
