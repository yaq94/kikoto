# 25：目录排除的有界处理与恢复证据

## 版本与范围

- 基点：`933cbf2c75bfc6d8de028a2424f513ef63889834`。
- 被测代码提交：`8f7fd11a6592bcb97845e74fa87436673267a5f5`。
- 分支：`codex/kikoto-exclusion-bounds`；独立托管 worktree 名称 `kikoto-exclusion-bounds`。
- 按已授权的 `implement-spec`、`tdd` 执行；接缝为真实鉴权 HTTP、迁移 SQLite、持久任务、实际关闭并重新打开数据库。
- 范围仅为 exclusion create/delete、apply/revoke、已有 publication 的窄接点及直接测试。没有改迁移 107/108、分组、finite scan、retirement alias、中央工单或规格状态。
- 原始日志与测量脚本保存在仓库外 `.kikoto-work/reports/20261107-remediation/`。全部数据为本地合成夹具，没有生产来源、真实模型、媒体下载或部署。

依据：[D-05/D-10/D-11 与 AC-21/23/25](../spec.md)、[20](../issues/20-catalog-sync-and-admission.md)、[23](../issues/23-retention-and-repository-contracts.md)、[25](../issues/25-review-remediation.md)、[ADR-0008](../../../docs/decisions/ADR-0008-catalog-publication-snapshots.md)。

## 实现边界

规则变更、publication owner 与 workflow job 在同一个事务中创建。排除独占当前来源；full/index 与 sync 的入队入口先拒绝该来源的未发布排除操作，不能替换其 generation 或检查点。失败与取消保留 owner，由原 workflow retry 恢复同一 run 和 operation；成功 marker 才释放未发布所有权。

工作状态使用 `workflow_job.checkpoint_json.exclusion`。entry、file、observation、directory、audio root 都使用 keyset；文件及列表页最多 64 行，游标与对应变更同事务提交，不使用 OFFSET、不限制最终集合。每次准备调用最多处理 16 批，然后将同一任务放回队列；runner 的立即续排仍遵守优先级与延期时间。旧的整子树事务入口只作为测试适配器存在于 `_test.go`，生产路径不再调用它。

scan 计数使用独立的 64 行目录、观测、条目扫描。generation、累加器和游标持久化；当前 generation/revision、成功空目录、pending、audio-root membership 等口径与现有计数一致。最后仅更新一行完整 scan 投影，不在事务里重算整源 count。来源 generation 变化会拒绝过期操作，包括 marker 前的最后一个写事务。

source-owned 工作行沿用稀疏 before-image；全部批次完成前公开目录、文件及播放投影继续读取旧集合。排除不删除 entry/work、个人评价、series membership、relocation 或 `personal_transfer_ref`。whitelist 的范围、同层/内层规则优先级、父卡片下的部分文件、split `source_root` 语义保留。撤销规则不伪造文件，由后续扫描重新确认内容。

排除专用 lease callback 在取得 SQLite writer 后同事务检查任务租约。marker callback 将成功状态与 publication marker 原子提交：cancel 先提交时 marker 被拒绝；marker 先提交时 cancel 返回终态冲突。callback 失败回滚两者。提交后 cleanup 失败只留下可回收 before-image，任务保持 succeeded；后续 cleanup 可以继续。其它 publisher 没有这些 callback 时沿用原行为。媒体准备只增加明确 `excluded` 的 unavailable 映射，保留精确 track 缺失与显式 offline 状态规则。

## 公共接口与前端交接

| 接口 | 合同 |
| --- | --- |
| POST `/api/remote-sources/{id}/catalog/exclusions` | 201，原规则字段加 `runId`；同步完成时也返回 runId。201 表示规则已保存，不能单凭它认定完整目录已切换。 |
| DELETE `/api/remote-sources/{id}/catalog/exclusions/{exclusionId}` | 200，`{ok,path,mode,runId}`；同步完成时也返回 runId。 |
| GET `/api/workflow-runs/{runId}` | `queued`/`running` 为非终态；排除成功为 `succeeded`；故障为 `failed`，取消为 `cancelled`。低空间、维护或禁用来源以 `queued` 加延期保存，原因在任务状态中。 |
| POST `/api/workflow-runs/{runId}/retry` | 恢复原 run/job/operation 和 checkpoint；202，不分配新 run。通用 run 状态门禁仍拒绝 running/queued/succeeded；排除分派只恢复 recoverable failed/cancelled/partial。 |
| POST `/api/workflow-runs/{runId}/cancel` | 先于 marker 的取消保留旧可读集合；已成功提交则返回 409。 |

create/delete 使用 `sources:write`。任务读取、取消、retry 继续使用 `workflows:run` 与既有任务归属/管理员检查；排除 retry 另检查当前 `sources:write`。源 enabled 与公开访问权限保持当前配置。同步执行发生错误时沿用通用 500/503 错误体；规则和失败任务已持久化，可从 Activity 找到并 retry，错误体不携带 runId。

前端 `catalogExclusions.ts` 未声明 runId，`SourceCatalogExclusions.tsx` 丢弃写响应，只刷新规则列表；本分支未修改 UI。root 负责“已保存/处理中”、按公开任务状态轮询、失败重试和终态内容刷新。本证据不声称该前端交互已验收。

## 红绿与恢复

红测使用 Go overlay 还原指定基点的生产文件，测试文件不改断言；`exclusion-base-overlay.json` 与替换文件均留在原始报告目录。

| 回归 | 红测证据（exit 1） | 绿测证明 |
| --- | --- | --- |
| 多根 HTTP 排除 | `exclusion-bounds-red.log`：一事务 302 business / 300 file | 最大 business 64，完整删除 300 文件，路径边界卡保留 |
| 批失败 + 实际 DB 重开 + HTTP retry | `exclusion-recovery-red-v2.log`：首批不能独立提交，仍有 150 文件 | 首批剩 86 working 文件，公开仍有 150；重开后同 run/op 完成，个人评价和稳定身份保留 |
| 请求取消 | 同上：基点返回 201，未留下可恢复批次 | 503，旧 tracks 可读，retry 完成原操作 |
| cancel/marker 排序与 cleanup 故障 | 同上：基点没有 publication workflow | cancel-first 保持 cancelled/preparing；marker-first 为 succeeded、cancel 409；cleanup 故障后可再次清理 |
| marker/任务原子回滚 | 同上：基点忽略成功结算故障注入 | 注入 job succeeded 更新失败后旧媒体仍 available；重试一次切换 |
| 低空间与重启 | 同上：基点没有持久任务 | 64 文件后暂停，旧集 150 可读，禁用源不能播放；实际重开、原延期任务恢复 |
| whitelist 删除/whole 撤销 | `exclusion-scale-red.log`：未经过 marker，删除立即返回 200 | 删除失败仍读旧 whitelist 文件；retry 后排除；剩余 whole 规则及 source-only alias 保留 |
| 竞争 index/sync | 同上：新 full 返回 202 | index 返回 409，sync 被拒绝，scan/sync checkpoints 未创建 |
| 193 个未排除前缀目录的计数恢复 | `exclusion-counter-red.log`：整事务 counter 故障使 create 500 | 真重启后同任务恢复，公开 dirs/completed/observed 均 193、indexed 2、pending projection 193；generation 变化拒绝，恢复原 generation 后不重复累计 |

一次初始 race 运行 `exclusion-race.log` 暴露大规模同步 HTTP 在 60 秒首次响应预算处被取消，未提高预算。移除 16 批续排限制的 mutation overlay 在 `exclusion-invocation-red.log` 失败：公开任务直接 succeeded，未返回 queued。修复后大规模先返回 queued/runId，测试经真实 runner 和公开 GET 任务终态验收；32 根夹具在续排前实际关闭 Server/DB 并重新打开。

## 资源证据

`exclusion-scale-final.log` 在被测代码上执行，exit 0。SQLite pre-update、commit、rollback hook 统计所有真实表写，包括 before-image、admission、workflow、搜索和 FTS 物理写；单事务 business 总数包含 entry/file，因此文件写同样不会超过该总数。

| 合成规模 | 文件删除/文件事务 | business 峰值 | 全表总写/写事务 | 全表单事务峰值 | HTTP 首次返回 | 完成耗时 | 最长写事务 | 采样 heap 增量 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 32 根 / 2,080 文件 | 2,080 / 64 | 64 | 60,943 / 261 | 1,610 | 449 ms | 3.37 s | 203 ms | 4.78 MB |
| 128 根 / 8,320 文件 | 8,320 / 256 | 64 | 244,034 / 989 | 1,644 | 461 ms | 11.98 s | 83 ms | 4.83 MB |

总分配约 20.10/74.28 MB；不是同时驻留量。193 前缀计数用例覆盖跨页累加与失败恢复。128 根峰值事务包括 file 64、file-before 64、publication-entity 64、shared revision 64、search doc/before/docsize 各 64，以及两套 FTS 的 data/idx 写；详单在日志。FTS 写放大取决于索引状态，1,644 不是所有输入的硬上限。race 下延迟显著增加，不用于普通构建性能数字；其 128 根首次返回约 13.62 s，仍没有提高 HTTP 超时。

`exclusion-keyset-green.log` 与恢复 race 中的真实迁移库 EXPLAIN 显示 entry 使用 `rowid>?`，file/observation/directory/audio-root 使用复合主键范围，无 OFFSET、临时排序或从头扫描。

`measure-exclusion-keyset.py` 使用 Python SQLite、同一 schema/index 及对应 SELECT 形状记录累计查询/VM；这是隔离查询实验，不是整个 Go worker 的 VM 计数，也不包含触发器。2k/8k/32k 全集均检查返回数量：五类 64 行页分别需要 33/126/501 次查询，entry VM 约 22,300/89,200/357,000；逐根候选分别 2,001/8,001/32,001 次查询，VM 134,000/536,000/2,144,000。完整数据都被覆盖，没有按预算截断深处集合。结果为 `exclusion-keyset-vm.jsonl`，exit 0。

## 验证命令与限制

所有 Go 命令使用 `GOTOOLCHAIN=go1.26.10`，从 backend 执行测试；Make 从仓库根执行。

- `exclusion-counter-recovery-race.log`：`go test -race ./internal/httpapi -run '^TestCatalogExclusion(HTTP|Batch|Cancelled|CancelOrders|Marker|LowSpace|RuleRemoval|Blocks|Keyset|Counters)' -count=1 -timeout=10m -v`，exit 0，94.224 s；包含最终计数实现。
- `exclusion-race-final.log`：排除、既有规则/whitelist、生命周期和 32/128 根规模 race，exit 0。其后增加计数专用实现，由上一项及最终普通规模/旧规则验证覆盖；不把这份早期 race 称作最终整包 race。
- `exclusion-runner-race.log`：优先级、单 runner 排他、立即续排与延期的五项回归，exit 0，27.776 s。
- `exclusion-scale-final.log`：规模、生命周期和既有 exclusion/whitelist 回归，exit 0，24.220 s。
- `exclusion-publication-final.log`：被测提交上的 `go test ./internal/httpapi -run '^TestCatalogPublication' -count=1 -timeout=10m -v`，exit 0，9.627 s；覆盖已有 publisher 无 callback 路径、迟到译文、removed media、offline 状态、旧 reader 与清理。
- `exclusion-counter-green.log`：counter 故障、真实重开、generation 拒绝与公开进度，exit 0。
- `exclusion-static-final.log`：`make backend-format backend-vet backend-build backend-lint GOLANGCI_LINT_BASE=933cbf2c75bfc6d8de028a2424f513ef63889834`，exit 0；固定 golangci-lint 2.13.1，0 issues。所有新增文件加入 index 后另跑 `exclusion-lint-staged.log`，exit 0、0 issues。
- `exclusion-doclinks-final.log`：`node scripts/check-doc-links.mjs`，exit 0，135 公共 Markdown。
- `exclusion-commit1-sensitive.log`：实现提交前 `make sensitive-check`，exit 0。证据提交前再次运行 sensitive、tracked-evidence 与链接检查。

未执行生产/目标硬件验收、整个仓库 CI 或 GUI 渲染；本批没有前端修改。内存数字为进程 heap 采样，不是操作系统 RSS。极端跨来源稀疏 id、不同 FTS 索引历史和更大真实数据库需在最终候选资源验收中补测。退役别名、分组与 finite scan 由其它 owner 处理。

继承的 `25-publication-retry-media.md` 仅将原始日志绝对路径改成仓库外相对定位，原版本、结果和断言保持不变。本页仅关闭本次后端排除实现范围，不代表 107、工单 25 或整个特性完成。
