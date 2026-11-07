# 25：107 目录同步与发布证据（实施中）

基点为 `72d52434075c1724c5f3fd97f14b5b098826d546`，分支为
`codex/kikoto-sync107-nov7`。本页记录当前工作树的分步验证，不代表完整107验收。
原始日志统一在仓库外 `.kikoto-work/reports/20261107-remediation/`。
所有来源与内容均为本地合成夹具；未调用生产、真实来源或真实模型，未下载媒体。

持久化决定见 [ADR-0008](../../../../docs/decisions/ADR-0008-catalog-publication-snapshots.md)。
迁移仅新增 `107_catalog_publication.sql`；`compat/107_dev.sql` 使用
`GOTOOLCHAIN=local GOPROXY=off go generate ./migrations` 生成，105与106保持原样。

## 条款与证据

| 条款 | 当前实现与验证 | 状态 |
| --- | --- | --- |
| F1叶目录尾部不重复扫描 | 显式 `probeExhausted`、成功游标、失败run标记；末尾wrap发现较小的新路径；跨lease保留其它checkpoint字段 | 定向绿 |
| 页与cursor同事务 | listing header/item，下一页请求前检查前页持久化；page limit500，apply64 | 定向绿 |
| 不完整分页不误删除 | duplicate、total变化、提前empty、unknown total、HTTP partial均保留旧文件；成功empty保留entry与个人引用并标unconfirmed_empty | 定向绿 |
| 真重启与scheduler恢复 | 取消第2页后关闭Server与DB；重开，daily/weekly同时到期优先恢复原syncRun；仅请求第2页 | 红→绿 |
| 旧集合持续可读 | before-image、新insert缺席标记、删除旧值；entry/file/admission/series/member/presence/playback/search共用operation marker | 定向绿，路径审计未结束 |
| 同一读取快照 | list/count、准入、tracks、目录、series短SQLite只读事务；旧reader跨commit及cleanup仍读旧文件，新reader读新文件 | 定向绿 |
| 播放/个人记录 | missing在commit后生效；prepare中的首次播放和首次登记保留旧可读投影；退役旧引用仍读取当前稳定身份上的评价 | 红→绿 |
| 搜索一致性 | 原文trigram的live/before索引两臂；文档id不得复用pending tombstone；短/长native与model scope同时切换 | 红→绿 |
| source.enabled即时生效 | 外部scope刷新同步before-image，既有快照不恢复禁用来源 | 红→绿 |
| 冲突顺序 | 每source单pending；按实体key拒绝其它operation，包括source为空的共享series；重复owner恢复同operation | 定向绿 |
| 历史不线性退化 | 10000 committed operations，file查询两臂均按entry/path键及operation主键查；cleanup64行 | 定向绿 |
| 106 retention衔接 | staging不逐行递增revision，marker递增；普通目录title/path改名使revision 5→6，维护删除旧目录译文 | 外部评审探针绿 |
| 低空间 | listing请求前后及publication写事务前检查；工作文件已准备后资源不足仍保留旧发布快照，原run恢复后切换 | 红→绿 |
| 固定行数写 | listing/apply/cleanup64；手动普通快照stage/clear/apply64；媒体投影64；lifecycle旧文件64；cleanup两个32文件臂 | 定向绿，遗留全范围路径仍未完成 |
| 无编号/系列/多来源/格式副本 | 原有目录/episode/relocation/shared测试；单集101完整成员、取消恢复 | 已有定向绿，最终广回归待终态 |
| 迁移与回滚 | 105/106升级、重启、备份恢复、snapshot0不复制、107注入失败回滚106、重试；当前DDL重新生成compat107并复测 | 当前定向绿；整合后的全迁移门禁待跑 |

## 红绿记录

测试均从 `backend` 目录使用 `GOTOOLCHAIN=local GOPROXY=off go test ./internal/httpapi -run '<测试名>' -count=1`。

- `TestCatalogSyncProbeExhaustionSurvivesListingBatches`：`sync107-probe-red.log` exit1，71次listing均没有durable exhausted；`sync107-probe-green.log` exit0。
- `TestCatalogSyncListingCommitsPageBeforeNextRequest`：`sync107-listing-red.log` exit1，前页尚未持久化；`sync107-listing-green.log` exit0。
- `TestCatalogPublicationKeepsOldTracksUntilCommit`：`sync107-publication-red.log` exit1，新文件提前可见；`sync107-publication-green.log` exit0。
- `TestCatalogSyncListingRestartsFromCommittedPage`：`sync107-dispatch-recovery-red.log` exit1，新建run丢恢复身份；`sync107-dispatch-recovery-green.log` exit0。随后真实scheduler优先级反例为 `sync107-scheduler-recovery-red.log` exit1；修后 `sync107-scheduler-recovery-green.log` exit0。
- `TestCatalogPublicationHidesNewCardsAndKeepsPersonalWrites`：`sync107-personal-concurrent-red.log` exit1，review写presence遇冲突；修后个人记录断言通过。扩展首次播放后 `sync107-first-playback-red.log` exit1，修后green日志exit0。
- `TestCatalogPublicationSearchKeepsOldTextUntilCommit`：`sync107-search-red.log` exit1，旧标题搜索丢失；`sync107-search-green.log` exit0。跨source文档id复用见 `sync107-search-key-red.log` exit1与green exit0。
- `TestCatalogPublicationSwitchesNativeAndModelSearchScopeTogether`：`sync107-language-scope.log` exit0，覆盖短query与长trigram、native与model、未来文本缓存。
- `TestCatalogPublicationDoesNotFreezeSourceEnabledConfiguration`：`sync107-enabled-red.log` exit1，禁用后旧scope仍为1；`sync107-enabled-green.log` exit0。
- `TestCatalogSyncRemovedFolderCommitsDeletionCursorWithFiles`：`sync107-delete-cursor-red.log` exit1，已提交64删除没有cursor；green exit0，故障后继续删除且marker前仍读70旧文件。
- `TestCatalogSyncLowSpaceAfterWorkingBatchKeepsPublishedSnapshot`：`sync107-working-low-space-red.log` exit1，低空间仍succeeded；green exit0。
- `TestCatalogManualSingleSnapshotUsesBoundedFileTransactions`：`sync107-manual-batch-red.log` exit1，一事务2entry+205files；green exit0，205完整文件保留。
- `TestCatalogPublicationBoundsPlaybackProjectionUpdates`：`sync107-media-bound-red.log` exit1，150播放位置同事务；green exit0，每批最多64。
- `TestCatalogPublicationBoundsStaleFileDeletion`：`sync107-cleanup-bound-red.log` exit1，lifecycle/cleanup各150文件同事务；green exit0，最终仅当前文件。
- `TestCatalogPublicationRetirementKeepsOldPersonalReferenceReadable`：`sync107-retirement-reference-red.log` exit1，旧快照个人评价短暂不可见；green exit0。读取使用live退役目标的稳定work引用，不复制个人记录。

## 资源测量

`measure-sync107-probe.py` 抽取当前候选的实际probe SQL，以SQLite progress handler
计SELECT VM；它是SQL/控制流合成实验，不是Go驱动VM探针。最终
`sync107-probe-scale-latest.jsonl` exit0覆盖2k、8k、16k、32k叶尾与混合分布。
32k叶尾：1002次查询/约528834000 VM降至2次/1120300；32k混合：1102次/325060000
降至102次/1170500，全部3200父目录与32000listing集合均被断言覆盖。

`TestSourceCatalogEpisodeDiscoveryPublishes101WithinWriteBound` 的SQLite pre-update、
commit、rollback hook记录全部真实表写入。`sync107-all-row-audit-latest.log` exit0：
101文件、3个文件stage事务；entry/file峰值50+50。加入搜索before-image后，全表总写6851、
138个写事务、峰值1881行，最长已测写事务221.1922ms（开始持有writer至commit/release）。
当前交接树重测 `sync107-reviewable-write-audit.log` exit0：全表7156行、240个写事务、
峰值仍1881行，最长159.3198ms。新增媒体/退役准备与清理检查增加事务次数；该测量仍只
代表101集夹具，不能推断未覆盖的大根目录、删除和退役别名路径已经满足预算。
500ms是现有catalog慢事务观察阈值，100是entry/file业务行合同，二者均不是全表写行上限。
峰值分表：entry50、entry_before50、file50、file_before50、member50、member_before50、
series1、series_before1、episode_pending50、pending_before50、publication_entity51、
writer2、personal_transfer_ref51、shared_identity_revision151、work_matching_control101、
search_doc101、search_doc_before101、search_docsize101、search_data612、search_idx208。
FTS物理写量会随索引状态变化；本数字只覆盖101集夹具，不推广成所有输入的最大值。

5000文件既有边界回归：早期published scope触发器展开48.60s，live写侧scope后9.76s；
增加搜索/冲突边界后20.77s；批内复用entry/file prepared statements后
`sync107-5000-prepared.log` exit0、4.82s，未改阈值或超时。
5000卡片真实HTTP浏览 `sync107-browse.log` exit0，3样本p95 71.48ms，5001文件拒绝p95 2.71ms。
该测试自带EXPLAIN仍展示原SQL，不能用它证明published视图的执行计划；真实HTTP时延与plan证据须区分。

## 验证边界与未完成项

- `sync107-race-publication-v2.log` exit0，244.326s；其后新增的退役/最后批量改动尚需对应race。
- `sync107-static-publication-v2.log` exit0：backend-format、vet、build；其后改动须再核验。
- `sync107-privacy.log` exit0：sensitive-check、tracked-evidence-check；新增本证据后须再核验。
- `sync107-doclinks.log` exit0：135个公共Markdown相对链接。
- `sync107-lint.log` exit2：离线环境缺golangci-lint模块，GOPROXY=off拒绝查找；不是lint通过。
- 两个个人导出测试 `TestPersonalTransferExportsEveryEvaluationLevel` 与
  `TestPersonalTransferEmptyLibraryRoundTripAndRestart` 在series user-state返回404。
  `sync107-series-legacy-control.log` 用Go overlay恢复基点的series admission函数仍失败。
  这不替代完整基点验证；主代理已安排独立worker处理，本分支未修个人导出模块。
- `decideCatalogWorkSplitTx`、`catalogSplitPlanForRoot`仍物化整根名称/分组map；
  finite `ScanRoot`仍返回整根slice。普通文件写入已分批，但未证明这些算法符合一页内存目标。
- exclusion与部分retire/relocation依赖仍有全范围写事务；来源配置、共享稳定身份与
  来源拥有投影的完整调用链尚未收口，不能声称所有writer均已完成有界转换。
- 衍生来源presence的其它界面读路径仍需审计；主catalog读取已适配，不等同全应用覆盖。
- 未做最终RSS/heap峰值、大folder与多根split完整规模曲线；本阶段提交尚未整合其它独立分支，不等同最终候选验收。

工单25的107复选框保持未勾选。最终完成必须消除上述107实现缺口，并对最终SHA取得门禁证据。

## 供主代理审阅的阶段交接

主代理要求先审阅、集成当前纵向实现，再继续107尾部。本次提交是可构建的阶段结果，
并非107完成；没有改工单复选框，也没有删掉未完成验收。独立前端、个人系列夹具和
翻译维护锁分支尚未合入本分支，合并后的真实候选须再验收。

已启动的广回归 `sync107-regressions-publication-v2.log` 最终exit1（681.094s），唯一
失败是 `TestCatalogMaintenanceHoldPausesAndDefersResume`：新增资源guard把maintenance
结算成paused而非queued。worker已恢复queued及hold截止时间；当前定向复测通过，未把
原广回归改记成功，也没有在交接阶段重新启动长回归。

本次另外补齐两处反例：

- `sync107-derived-reads-red.log`：媒体准备后，来源标签提前not_found，译作HasMedia提前false。
  `voice_source_projection.go`、`work_translations.go`使用published读取；相关来源投影查询共用短读事务。
- `sync107-cleanup-checkpoint-red.log`：另一来源发布清理时，130条未发布listing的items全被清掉，
  headers仅剩66。清理保留仍有preparing operation来源的检查点，已发布headers按64行循环清完。

当前树收敛验证（均已读终态）：

- `sync107-reviewable-static.log` exit0：`make backend-format backend-vet backend-build`。
- `sync107-reviewable-migration.log` exit0：107从105/106升级、重启/恢复、失败回滚；106历史迁移回归。
- `sync107-reviewable-targeted.log` exit0：maintenance、检查点清理、播放/来源投影、退役个人引用、work detail。
- `sync107-retirement-batch.log` exit0（69.726s）：现有格式副本/个人冲突/播放游标及退役回归。
- `sync107-derived-cleanup-green.log` exit0（3.812s）：上述两个新增反例修后验证。

短读事务不能包住仍直接调用数据库连接池的整段work detail：单连接测试会等待自身占用的连接。
本次试验已撤回该整段包装，保留`TestWorkDetailSeparatesMediaAndLoadsLyricsPreference`验证；
译作来源投影读取完成即释放事务，再读live标题输入。未来扩展HTTP整段一致性必须先把相关
reader的queryer贯通，不能仅在入口开事务。主catalog列表/准入/tracks的已接路径不以此替代验收。

下一批明确入口：

| 文件/入口 | 未闭合条款与工作 |
| --- | --- |
| `source_catalog_loose_works.go`: `decideCatalogWorkSplitTx`、`catalogSplitPlanForRoot` | 整根names及group maps仍物化，每publish批重复构建；需持久分组或流式聚合，保留当前无编号/episode/格式语义 |
| `source_catalog_duplicate.go`: `catalogDuplicateDirectoryDecisionTx`；`catalog_series_discovery.go` | 子/父完整音频集合、一对一匹配、episode位置集合仍整体装入内存；需用真实多根夹具证明资源上限 |
| `openlist_catalog.go`、`catalog_directory.go`、`catalog_manual_publication.go` | finite ScanRoot整根slice及split maps；manual写已64行，但输入内存、资源guard及输入改变后的恢复语义未闭合 |
| `source_catalog_exclusion.go`: apply/revoke/create/delete | 全量id、文件/observation/directory/audio-root事务，尚未统一publisher；须保留规则、白名单、个人引用及失败恢复合同 |
| `source_catalog_loose_works.go`: `retireCatalogCardTx` | 文件删除已defer至64；入站alias UPDATE仍全范围；shared/personal迁移仍原savepoint，不可擅改个人合同 |
| `catalog_sync_listing_apply.go`、`catalog_publication_media.go` | 删除真实路径CASE及media fingerprint/id keyset需要大规模实际EXPLAIN/VM，排除每批重复扫描 |
| HTTP其它衍生readers、library enrichment | 主catalog已接；仍需跨来源/准入/搜索/播放与live identity组合读取审计及单连接测试，不能将局部published替换视为全应用完成 |
| 107迁移、测试与证据 | 整合后最终race、全相关H/R、真实published EXPLAIN、RSS/heap、多根/长历史曲线；lint缺模块仍未通过 |

外部生成辅助脚本`generate-sync107-publication.py`保存于同一报告目录，用于生成107的publication后半；
数据库开发baseline的正式生成入口始终是`go generate ./migrations`。继续修改DDL时应同步该辅助脚本，
或明确停止使用它，避免覆盖新的DDL。105/106文件保持原样。
