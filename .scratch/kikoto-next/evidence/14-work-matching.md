# 14 高置信作品匹配

**Implementation:** verified-local。主责 AC-17 及共同 AC-13、AC-16、AC-18 的本地 H/R/F 证据如下；真实来源覆盖、LIVE 和部署前置仍由整体验收处理。

## 行为边界

- 沿用可靠编号、别名和既有发行关系。无码使用原始标题、声优身份集合及社团或发行日期；已有完整音频哈希仅作为已保存证据使用。
- 无码目录可缺少作品行，此时读取来源发行目录的 readme/info 侧车文本，只接受显式标注的配音与社团字段（`配音`、`声优`、`CV`、`キャスト`、`社团`、`サークル`、`Circle`）。两侧都有相同标注集合时，标题加配音构成两项独立证据；只有一侧有侧车、或仅正文提到名字，不构成匹配。侧车不提供发行编号，不登记作品，不下载音频。
- 完整音频哈希来自来源索引已取得、按 SHA-256 格式验证的逐音频哈希。匹配和作品登记不会为计算哈希下载音频、全量读取媒体或额外调用来源接口；缺少完整哈希时仍遵循多项元数据和歧义规则。
- 自动提交沿用13的共享身份与本人确认事务；系列/单集层级、不同版本、私人记录和播放引用保留。拆分后的同一对对象进入人工确认。
- 单执行者、逐项持久游标、五秒评估、候选/结果/操作预算、低空间暂停和失败退避；所有来源参与候选查询。超限只暂停，不从截断集合判唯一。详见[当前开发契约](../../../docs/development/work-matching.md)。

## 当前产物证据

- 初始集成基线 `5aac34069afb01031387d30fb34b466cd5d50aad`。独立074迁移提交 `07fb686f4bbcd8717cfeeedd5d5878346aae6e3f`，包含生成的 `compat/074_dev.sql` 与完整链版本断言；未更改旧迁移或发布 baseline。
- 红例：匹配接口404；新增候选时本人确认错误地完成；拆分后自动重新合并；刷新复用了过期个人记录快照。每项都有对应失败退出1与修复后退出0日志。
- `TestWorkMatching*`：12个H/R顶层（含7项弱证据/矛盾样例），真实路由、会话、迁移SQLite。覆盖编号、发行关系、无码元数据和哈希、多人隔离、候选漂移、双用户同库重启、102个候选跨预算、低空间、并发请求及事务失败退避恢复。
- `go generate ./migrations` 与 `go test -mod=readonly ./internal/storage ./migrations -count=1` 退出0；完整链、新装、旧库升级、幂等并发及开发基线一致。
- `make backend-test backend-format backend-vet backend-lint backend-build` 退出0；全后端通过，lint为0 issues。后续收尾仅重跑受影响定向行为与静态检查。
- 最终 `go test -mod=readonly ./internal/httpapi -run '^TestWorkMatching' -count=1 -v`：12项通过，退出0；共享关联、拆分、系列的组合定向测试及单独 race 检查均退出0。未完成来源范围的新增用例先观察到错误完成，再验证暂停。
- 前端 `make -o frontend-install frontend-lint frontend-i18n frontend-docs`、`npm run build`、选定变更文件 Prettier 检查均退出0；`npm run test:unit -- --maxWorkers=4` 为153文件、867项通过。`-o` 复用本工作区刚完成的同一锁文件依赖安装，避免运行服务期间再次移除原生模块。
- 侧车证据新增 `TestCatalogSidecarParsesLabelledCreditsOnly`、`TestWorkMatchingSidecarCreditsAssociateNumberlessCatalog`、`TestWorkMatchingSidecarCreditsRejectDifferentCast`、`TestWorkMatchingSidecarRequiresCreditsOnBothSides`、`TestSourceCatalogIndexStoresSidecarCredits`、`TestSourceCatalogSidecarFailureKeepsPlayableCard` 与 `TestSourceCatalogRelocationCarriesSidecarEvidence`，均通过；同时 `TestWorkMatching*` 既有 12 项通过。侧车读取失败不删除卡片，不把文本变成音轨；改名认回时证据随卡片迁移。
- `make frontend-e2e` 选择 `work-matching.spec.ts shared-association.spec.ts --project=desktop-chromium --workers=1`：两个真实前后端功能场景通过，退出0；截图、trace和video关闭。匹配原因、待确认候选入口、过期刷新、两位本人确认与重载均有行为断言。
- 本工作区独立 `npm ci --strict-allow-scripts` 安装192包，0 vulnerabilities。第一次F准备阶段因本工作区Vite占用本机原生模块而失败；停止本工作区Vite后，重跑规范依赖目标通过，再执行F通过。没有共享node_modules入口或其他工作区依赖被改动。

原始日志留在任务指定的仓库外 `t14-evidence` 目录；不把本机路径、服务日志、会话或真实数据写入仓库。

本任务启动的测试后端均通过专用停止入口退出0，Vite进程树已停止；独立工作区保留供集成。没有部署或真实媒体访问，本票不替代07/08/17尚需取得的真实来源、完整索引及发布验收。

## 审计后修复与补证（2026-10-21）

- `title_voice_credits` 的门槛补齐 circle：双方都有 circle 且不同时判为 `conflicting_evidence`，不再因声优相同自动关联。新增 `TestWorkMatchingSidecarCreditsRejectDifferentCircles`。
- 新增预算与终态 H：`TestWorkMatchingPausesAtEvidenceByteLimit`（2 MiB）、`TestWorkMatchingPausesAfterDailyOperationBudget`（1,000/日）、`TestWorkMatchingWorkerGivesUpAfterThirdFailure`（30s/60s 后第三次失败转 failed）。
- 既有的 worker 重启、低空间、并发复用与候选漂移用例继续通过；全后端 32 包通过。
