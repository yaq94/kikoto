# 25：107 目录增量持久化设计交接（未实现）

## 范围与依据

这是针对 `35419ed7` 的代码调查和后续实现方案，不是测试通过或迁移交付证据。主代理已保留 107 给目录增量同步；106 由并行译文资源整改负责。本轮按主代理交接要求停在设计，不修改 DDL 或同步代码，待 106 汇合后继续。

- spec D-05 要求逐目录、逐页持久化检查点并释放资源；只有成功完整范围可判缺失。
- spec D-10 要求每响应最多 2 MiB、逐批落盘，低空间暂停时已存内容仍可读；AC-21 要求分页变化、取消和真实重启恢复，AC-25 要求暂停不破坏读取、播放器及个人记录。
- 工单 08 验收 30–34 行要求完整集合、重复页、每批检查点和低空间恢复；20 第 72 行要求只有完整成功父 listing 才能判子项缺失，pending/listing_complete/failed/restricted/未确认 empty 不判删除。
- 25 的未完成同步项是当前整改目标；旧 08/20 勾选不证明现行增量代码已满足这些要求。

主代理明确：新集合可完整发布前保留旧可读集合，不能用短暂隐藏已发布内容代替原子发布。

## 已查明的当前路径

- `source_catalog_sync.go:387` 先 fetch 全目录，`:397` 再 apply；`:796` 把所有页 append 到切片，`:851` 一次事务读全部旧 observations、重写 revision、比较/删除/新增全部行。删除 folder 时还在同一事务处理整个子树及其公开文件。
- `openlist/client.go:238` 的 `ListPage` 明确拒绝缺失/负 total、超 pageSize、非法名字与负 size。因此 unknown 不等于 empty；同步阶段仍需检查跨页 total 一致、重复 name 和实际完成数，不能把中间空页当完整。
- 105 schema 的 observation 主键是 `(file_source_id,directory_path,name)`，file 主键是 `(catalog_entry_id,relative_path)`，均不含 revision。直接分批覆盖不能同时保存旧/新集合。
- `source_catalog_index_store.go:866` 的 publisher 只选择 complete directory/current observation revision，但 `:1120` 直接覆盖公开 file。`source_catalog_retained.go:18` 的共享 pending gate 仅保护 episode publication，并非普通增量快照屏障。
- 公开读取分散在 `openlist_catalog.go`（全文件、分页 tracks、目录）、`catalog_playback.go:171`（音轨定位）、`catalog_admission.go:191/:228`（SQL及单卡准入）、`catalog_directory.go:128`（作品目录标签）及系列/库搜索。它们必须读相同已发布集合；只改 fetch/apply 不足以保证可见性。
- probe 的 `probeGeneration/probeAfter` 已在 `source_catalog_sync.go:637` 和结果同事务写入 job checkpoint；listing 进度不能覆盖它。现有 job `Recoverable:false`，当前测试是重新打开应用后显式继续同 syncRunId；后续需明确接入现有 scheduler/startup 的恢复路径，不以仅重新调用函数代替完整 R。

## 建议的最小一致性实现

先采用独立 stage 和已发布快照指针，保持已有稳定 entry/work/个人引用 id；不要为复用旧表而让公开文件承受未完成写入。

1. 107 增加 listing header 与 item stage。header 主键 `(source_id,directory_path)`，保存 generation、base revision、目标 revision、observed_version/options 标识、下一页、expected/received、阶段及 name 游标；item 主键再加目标 revision/name，保存规范化文件元数据。每页数据和下一页同事务提交。重复 name、total 变化、提前空页拒绝整页并保留旧公开集合；拒绝后重开新 listing revision，取消/低空间则保留当前进度。实际请求最多一页驻留内存，写入再按固定行数分片，片段完成位置与数据一起提交。
2. 完整 listing 才进入 apply。按 name keyset 比较旧 observation 与 stage，计算新增/变更/移除并逐批应用到工作集合；folder 子树删除也必须改成范围游标与固定行数批次。公开文件暂不改。目录 revision/完成标记只在工作集合全部完成后切换，不能因处理前几页推进到 complete。复用 workflow checkpoint 保存 header 标识，使用 `json_set` 保留 probe 字段。
3. 为每个受影响 catalog root 构建不可变发布快照：新 file snapshot 表以 `(publication_group,snapshot_id,entry_id,relative_path)` 定位，entry snapshot 保存本次公开元数据、成员集合及准入计数；publication group 持有单个 `active_snapshot_id`。同一 root 的格式副本和拆分单集共用一次发布边界。先复制仍有效的旧文件，再合并变化，全部按固定批次执行；不能用准入阈值裁剪要索引的文件。
4. 新集合与准入/成员统计都完整后，用一个小事务切换 group 的 active snapshot。旧快照在切换前始终可读，新 entry 在 group 发布前不可见；成功 empty、确认删除与失败状态沿既有合同区分。移除子树涉及多个 root 时逐个发布已完成的根，不能先清空整个 source。读者通过统一 published-entry/file 查询读取 active snapshot；单请求多次读取要捕获同一个 snapshot 或用短只读事务，防 admission 与 tracks 横跨切换。
5. 迁移时已有数据作为 snapshot 0 兼容基线，不在 schema migration 中复制全库文件。首个 root 更新才分批构造新快照。所有后续写入者必须共用 snapshot publisher：普通增量、full publisher、手动 OpenList refresh、episode staging、exclusion、relocation、lifecycle 都不能直接修改已选中快照。旧快照清理以固定批次、已发布指针保护和读事务可见性为前提，可恢复；每批前复查低空间/取消，返回可解释暂停。

上述表名/字段是实施起点；正式 DDL 应在 106 合入后基于实际 schema 固化。不能把 file 读路径仅换成 view 却保留 `INDEXED BY` 或针对 view 的 `ON CONFLICT` 写入：需同步改为具体 snapshot 表与明确索引，并用 query plan 回归确认。

## 预计修改边界与测试

- schema：新增 `backend/migrations/107_*.sql`、对应 `compat/107_dev.sql`、storage 迁移头和新装/升级一致性测试；不改 105/106。
- 同步核心：`source_catalog_sync.go`，新 listing/snapshot store 文件；`source_catalog_index_store.go` 的 publisher 复用接口；现有 `source_catalog_sync_probe_checkpoint_test.go` 和调度测试保留。
- 读取适配：`source_catalog_retained.go`、`openlist_catalog.go`、`catalog_playback.go`、`catalog_admission.go`、`catalog_directory.go`、系列成员查询、library 的 catalog browse/search/count SQL、`catalogprune` 的 published files 查询。必须继续保证同一准入及当前 published generation。
- 写入适配：`catalog_episode_staging.go`/maintenance、`source_catalog_exclusion.go`、`source_catalog_lifecycle.go`、`source_catalog_relocation.go`、`source_catalog_loose_works.go` 和手动 `persistOpenListSnapshotTx`。这次列举是已检索到的路径，实施前仍需逐个精读；不盲目机械替换全部 SQL。
- 106 衔接：快照发布后需按已合入的 corpus revision 合同失效搜索/译文可读性，不能在每条 staging file 写入时让当前语料变化；协调点在原子指针事务，不修改并行 worker 的未合入文件。
- 红绿 H/R：下一页请求前已完成页持久；新集合准备时旧 tracks/准入/搜索可读；任意分页/apply/切指针边界注入取消、SQLite失败、低空间；关闭 Server 和 DB 后重开从已提交检查点继续；重复页/total变化/unknown/提前empty不能发布；成功empty及确认子树移除不丢个人记录；最终完整集合无重复。
- 资源：保持相同合成业务规模，记录每批实际行写量/commit数/最长写事务、峰值驻留页、总请求/写放大；大 folder 子树与多个 split card 也纳入，不只测单音频根。计划检查不得使用阈值截断替代完整遍历。

本轮没有实施上述方案，没有 sync 新红绿或性能改善数据；107 仍为下一次追派任务。
