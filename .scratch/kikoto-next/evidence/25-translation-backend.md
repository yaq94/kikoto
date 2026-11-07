# 25：第二批服务端交接

## 版本和范围

本批基点为 `b4832bb783506aa4b8e01673e9a5618429cb0173`，从第一批工作树正常快进合并。服务端可独立审阅的提交为 `a25eca8c`、`48ffeb36`；后者包含本批全部服务端行为。SQLite 仍为 105，无迁移改动。真实 HTTP、会话、迁移 SQLite 与 loopback 合成模型用于验证，不调用生产、真实来源或真实模型。

主代理调整交接顺序后，本次只交付服务端组供验收；前端已有提交和两处收尾改动全部保留，不回滚。25 保持 `in-progress`，前端、源同步、staging、语料覆盖/保留和基准程序的完整交付仍待续。

## 服务端修复和红绿证据

原始日志保存在仓库外 `reports/20261107-remediation/`。以下日志名均相对此目录。

| 行为 | 缺陷代码证据 | 修复后验证 |
| --- | --- | --- |
| single/batch 在途 Model A 被配置 B 错标 | `translation-version-red.log`：两例均返回 A 文本和 B version，exit 1 | `translation-version-green.log`：所有 `TestContentTranslation` 通过，57.932 s；包括 A 在途切 B、随后 B 生成及缓存命中 |
| 系列 GET 404，但评价和翻译成功 | `series-admission-red-valid.log`：empty、旧音频两组 GET 404、PATCH/翻译 200，exit 1 | `series-admission-final.log`：9 种场景、单条与 batch、实际个人导入恢复父系列通过，2.878 s |
| 单卡翻译遗漏 published admission | `card-admission-red.log`：empty、旧音频 GET 404，single/batch 成功，exit 1 | `card-admission-fixture-final.log`：相同场景拒绝，合格及 missing 保持；3 个原有名字保护/字段优先级测试通过，7.833 s |

配置在 `translateContentField` 开始时捕获，并传入持久/来源缓存及生成路径；响应版本来自同一快照，batch 不在所有条目结束后统一覆盖版本。系列共享 detail 的成员查询，保留 relocation、missing、disabled 来源已保存元数据、恢复父子 work 的原合同；评价在写事务内先验证可读性再注册 series work。单卡翻译在读取公有字段前调用现有 `catalogEntryAdmissionRequired` 和 `catalogPublishedEntryAdmitted`。

准入新增后，4 个旧测试夹具缺少任何可播放音频。仅为其添加保留范围的合成音频行，原断言全部保留。最初 `series-admission-red.log` 中有 size_bytes 缺省导致的夹具错误；正式红证据使用修正后的 `series-admission-red-valid.log`。

## 组合验证

- `go test -race ./internal/httpapi -run '^(TestContentTranslationVersionFollowsInFlightConfiguration|TestCatalogSeriesAdmissionMatchesReviewAndTranslation|TestContentTranslationCatalogAdmissionMatchesReadableFiles|TestPersonalTransferRestoredSeriesAndEpisodeReviewIndependently)$' -count=1 -v -timeout=5m`：exit 0，44.173 s，日志 `batch2-race.log`。
- `make backend-format backend-build` 成功，见 `batch2-build-static.log` 前两项；同次后续 frontend lint 因新增 effect 缺少 session 依赖失败，随后已补齐，不影响后端结果。
- `go vet ./internal/httpapi`：exit 0，`batch2-vet.log` 无输出。
- `series-admission-green.log` 中其余 catalog series、missing、relocation、共享身份和恢复测试通过；唯一失败为上述无音频旧夹具，其修正后的定向日志已列出。
- 每个提交前运行 `make sensitive-check`：`version-sensitive.log`、`admission-sensitive.log` 等均 exit 0。普通签名提交，无 push 或部署。

## 保留的前端工作

- `b565f317`：共享 batch transport 并发上限 2，跨 16 ms flush 和混合语言都共享；取消排队、失败释放有回归。`chunk-concurrency-red.log` 实测旧代码同时 4 请求，`chunk-concurrency-final.log` 15 tests 通过。
- `4fe94ade` 是测试编写中间提交，含返回详情的 reload；它不是同 SPA 通过证据。`be905c3b` 已将正式测试改为单次 goBack，原文没有放宽超时/重试。`spa-version-red-history.log` 在旧实现返回同一详情但仍没有 B 版本，exit 1；`spa-version-green.log` 修复后通过。
- `be905c3b`：SPA 导航、设置保存、focus/可见性及有界轮询刷新权威 version；响应仅请求刷新，不直接倒退版本。账户名/权限变化进入独立 scope，已知版本拒绝迟到旧写入。
- `0a54aa8d`：IDB v2 保留旧 entries，128 条分批补元数据和持久预算账本；LRU 读取元数据索引并分批删除，不 getAll 全文；prune 调用共享运行态，写入期间标记后续维护。`cache-prune-red.log` 原并行 prune 三次 getAll；`cache-prune-final.log` 真实 Chromium 的 LRU、账户/版本隔离、v1 迁移、字节预算、并发写和事务上限通过。
- 49 个针对性前端 U 通过（`translation-units-final.log`）。真实前后端 11 个非视觉 F 通过（`translation-browser-final.log`），另新增迟到 A 不覆盖 B 的三个 F 通过（`version-late-browser.log`）。所有截图、trace、video 关闭。
- `batch2-frontend-static-final.log`：frontend lint、e2e 类型、生产构建 exit 0。最后的 effect session 依赖补齐和 prefetch scope 对齐还需单独提交/验收，当前不关闭前端条目。

缓存规模复测 `cache-prune-after.jsonl` 以同样 100,000 条/48.04 MiB 合成数据、已建立的账本替代边界实现：预算内 2 次元数据读取、0 次正文读取，0.130 ms、堆增 0.015 MiB；删除一条 4 次元数据读取、0 次正文读取，0.238 ms。它测的是 Node24 中真实维护代码的稳态逻辑，不含浏览器 I/O、索引构建或首次迁移成本，不能直接称为浏览器性能；真实浏览器只验证有界迁移与语义。原始基线保留在 [资源基线](25-resource-baseline.md)。

前端仍需补全收尾文档、账号/权限变化及缓存重叠的最终独立验收，并复核延迟响应/维护完整性；本次分批不把这些工作标为已完成。完整 CI、最终发布账本和生产验收由主代理处理。
