# 24 候选账本：c7d793e4

> 历史候选。`c7d793e4` 曾部署并被 `907391f2` 取代；其 Full CI run `40587302416` 因 race 用例超时失败，后继候选 `15b84ed2` 已消除该失败。本文件记录 `c7d793e4` 当时的取得证据。它接在已部署的 `8fa8465e` 之后，只新增本文件第 2 节列出的两个提交。仓库只写镜像 tag；digest、主机路径与备份位置留在受限运维记录。

## 1. 候选标识

| 项 | 值 |
| --- | --- |
| 候选 SHA | `c7d793e4` |
| 分支 | `codex/kikoto-catalog-remediation` |
| 冻结时间（Asia/Shanghai） | 2026-10-28T05:20:00+08:00（代码冻结；此后只允许 docs 提交） |
| 集成负责人 | 主会话（Codex） |
| 镜像 tag | `kikoto:next-c7d793e4` |
| 镜像 digest（受限记录，不写仓库） | 只留在受限运维记录；仓库只写 tag |
| schema 版本 | `105` |
| 迁移链 | `095 → 096 → 097 → 098 → 099 → 100 → 101 → 102 → 103 → 104 → 105`（本候选未新增迁移） |
| 配置指纹（model / base URL / template / reasoning） | 生产沿用既有配置：model `Index-Translate-35B-A3B`，推理关闭，模板 `index-user-v1`；`config_key` 由 model、base URL、模板版本与推理开关共同决定，本候选未改动 |
| 工作区状态 | 代码冻结时 `git status --porcelain` 只有本文件、`24-candidate-8fa8465e.md`、`25-live-browser.md` 与相关正文的文档改动；文档提交后应为空并推送 origin |

## 2. 提交清单

本候选接在 `8fa8465e` 之后新增两个提交。`8fa8465e` 及其之前的提交构成基线，其归属见 [24-candidate-8fa8465e.md](24-candidate-8fa8465e.md) 第 2 节。

| 工单 | 提交 | 说明 | 已合入候选 |
| --- | --- | --- | --- |
| 02 / 05 / 20 / 21 | `5bf687f8` | 卡片翻译改为命名真实对象（known `workId`、catalog `catalogEntryId`、remote `sourceId + code`，无印刷编号时回退 remote id）；批量按字段排序并分成小批，标题先于标签；同一标签文本的服务端复用；runtime settings 暴露推荐基线 | 是 |
| 07 / 08 / 02 | `c7d793e4` | OpenList 索引进度收进默认折叠的 `<details>`；推荐评分端点区分“来源缺能力”和“来源不可用”，目录来源不再请求评分；目录卡新增幂等的工作登记端点与入队动作，列表/标记改为可用 | 是 |

## 3. 门禁结果

每条记录命令、退出码、证据与结论。`skip` 不计通过。

| 门禁 | 命令 | 退出码 | 证据 | 结论 |
| --- | --- | --- | --- | --- |
| 双轴审查 Standards | `git diff 8fa8465e...c7d793e4` | — | 见本节末“审查状态” | 进行中；结论回流后更新本行 |
| 双轴审查 Spec | 同上 | — | 同上 | 进行中；结论回流后更新本行 |
| 后端构建 | `cd backend && go build ./...` | 0 | 本地执行 | 通过 |
| 后端静态检查 | `cd backend && go vet ./internal/httpapi` | 0 | 本地执行 | 通过 |
| 后端整包测试 | `cd backend && go test ./internal/httpapi -count=1` | 0 | 本地执行 | 通过；586.158 s |
| 后端定向测试 | `go test ./internal/httpapi -run "TestCatalogCardRegistrationIsIdempotentAndWritesNoReview|TestSharedTagTranslationReusesOneVocabularyEntry|TestRemoteRecommendations|TestUserPreferences" -count=1` | 0 | 本地执行 | 通过；新增与变更路径均有用例 |
| 前端类型检查 | `cd frontend && npx tsc -b` | 0 | 本地执行 | 通过 |
| 前端 E2E 类型检查 | `npm run typecheck:e2e` | 0 | 本地执行 | 通过 |
| 前端 lint | `npx oxlint src --deny-warnings` | 0 | 本地执行 | 724 文件 0 warning / 0 error |
| 前端格式 | `npm run format:check` | 0 | 本地执行 | 通过 |
| 前端单元测试 | `npx vitest run` | 0 | 本地执行 | 167 文件 / 962 测试 |
| 前端构建 | `npm run build` | 0 | 本地执行 | 产物主包 `index-I_z3wgV9.js` |
| 文档链接 | `node scripts/check-doc-links.mjs` | 0 | 本地执行 | 134 份公开 Markdown，全部相对链接可解析 |
| 文档 locale | `node scripts/check-doc-locales.mjs` | 0 | 本地执行 | 5 locale / 11 页 |
| tracked-evidence | `node scripts/check-tracked-evidence.mjs` | 0 | 本地执行 | 56 个已跟踪证据文件通过 |
| 敏感扫描 | `node scripts/check-sensitive.mjs` | 0 | 本地执行 | 已跟踪内容 0 命中；扫描另报 4 项工作区未跟踪产物（`.kikoto-work/` 截图与 JSON、`.scratch/next.tar`），均不进入提交，见第 7 节 |
| LIVE 翻译（单条） | 生产 `POST /api/content-translations` | 200 | 部署验收记录 | 首次 2.08 s 返回目标文本；随后三次命中 0.83 / 1.19 / 0.83 s，`cacheStatus=hit` |
| LIVE 翻译（页面） | `/voices/4` 真实浏览器 | 200 | [25-live-browser.md](25-live-browser.md) 第 12 节 | 首个标题译文 427 ms；滚动后 0 `pending`、0 `failed`，无 `not_found` |
| LIVE 媒体 Range | 生产真实音频 | 206 | 第 6 节 | 与上一候选相同路径，本候选未改动媒体读取 |
| LIVE 浏览器 | Playwright + 管理员会话 | — | 第 6 节与 25 号文件 | 翻译、推荐、索引折叠、目录卡入队/标记/列表实测 |
| Full CI | `workflow_dispatch` run `40587302416` | — | GitHub Actions | 撰写时 in_progress；终态见本节更新 |
| 迁移：新装 / 升级 / 重复启动 | 迁移链测试四项 | — | 上一候选 `f727feee` 记录（历史） | 本候选未改迁移，沿用 |
| `PRAGMA foreign_key_check` | 部署脚本校验 | 0 | 部署记录 | 0 行 |
| 备份与恢复演练 | 部署脚本快照 + `sha256sum -c` | 0 | 部署记录 | DB、`.env`、compose 3/3 OK |
| 回滚目标 | `pre-c7d793e4` 快照指向 `next-8fa8465e` | 0 | 部署记录 | schema 105；`rollback.sh` 可执行 |
| 播放组合 E2E | `node scripts/run-e2e-fixtures.mjs --group=source-resource-limits-live` | — | 未在本候选重跑 | 本候选只改 E2E 的稳定队列读取与等待方式，未在候选上重跑；上一候选 1 passed（9.2–9.6 s） |
| 部署后验收 | 见第 6 节 | — | 部署验收记录 | 见第 6 节 |

审查状态：本轮两轴审查在 `8fa8465e...c7d793e4` 上执行，结论与逐条修复另记；本行在结论到达前不写“通过”。

## 4. 性能门禁

| 指标 | 环境 | 样本 | 门槛 | 结论 | 数值出处 |
| --- | --- | --- | --- | --- | --- |
| 42k 卡浏览 | 2 vCPU / 3.5 GiB, warm | 30 | ≤300 / ≤800 ms | 通过（沿用） | [20 号票](../issues/20-catalog-sync-and-admission.md) |
| 5,001 文件卡拒绝 | 同上 | 30 | ≤300 / ≤800 ms | 通过（沿用） | 同上 |
| 译文 3 字以上 | modernc SQLite, 456k 行 | 20 | ≤100 ms | 通过（沿用） | [21 号票](../issues/21-translation-corpus-and-search.md) |
| 译文 1–2 字 | 114,000 实体，全部标题命中 | 20 | ≤100 ms | 通过（沿用） | 同上 |
| 编号等值 | 同上 | 20 | <5 ms | 通过（沿用） | 同上 |
| 目标语言标题整串等值 | 同上 | 20 | <5 ms | 通过（沿用） | 同上 |

本候选未改查询路径，性能数值不重测，只沿用来源记录。翻译改动增加了“同文本标签复用”的进程内缓存；它只影响首次命中的模型调用次数，不改变单条 SQL 的查询计划。

## 5. AC 覆盖

本表只记录本候选实际取得的证据。未在本候选上重跑的路径沿用其来源记录，不冒充当前候选的新测量。

| AC | 原始主责 | 整改子项 | 当前证据 | 结论 |
| --- | --- | --- | --- | --- |
| AC-01 | 01 | — | 配置读写与掩码 H；生产试译使用已保存配置（本候选复测 200） | 通过（本地 H + 生产 LIVE） |
| AC-02 | 01 | 21 错误分类 | 空/超长/不支持语言 400 分类；截断/鉴权/限流/超时/格式错由 `TestTranslationTrialFailuresAreClassifiedWithoutLeakingOrRetrying` 覆盖 | 通过（沿用） |
| AC-03 | 02 | 21 显示语言搜索 | 两用户偏好 H/R；严格语言搜索 H；本候选修正卡片与预取的翻译身份，`/voices/4` 生产实测不再 `not_found` | 通过（本地 + 生产） |
| AC-04 | 02 | — | 保护名称不外发 H/F | 通过（沿用） |
| AC-05 | 02 | — | 延迟、失败、取消 H/F；未完成提示与 20 秒上限有浏览器用例；本候选把一页拆成小批并按字段排序，标题先返回 | 通过（本地 + 生产观察） |
| AC-06 | 02 | 21 缓存键与保留 | 缓存键、保留与预算 H；本候选新增同文本标签复用，仍按 `config_key` 与语言隔离，命中后仍写本卡自己的行 | 通过（本地） |
| AC-07 | 03 | — | 简繁复用与 LIVE 转换 | 通过（沿用 + 生产 LIVE） |
| AC-08 | 07 / 08 | 20 D-16、21 译文可搜 | 42k 浏览达标（数值见 20 号票）；无码/无编号卡片与短查询 `truncated` 生产实测 | 通过 |
| AC-09 | 06 / 07 | — | 旧引用与无码身份 H/R | 通过（本地） |
| AC-10 | 05 | — | 目录检查与多源 H/F；生产实测本地 1 音轨 ↔ 远程 4 音轨切换；本候选生产实测目录卡可入队 6 音轨并播放，全程无下载请求 | 通过（本地 + 生产） |
| AC-11 | 05 | — | 播放隔离与进度 H/F；生产 Range 206；本候选目录卡播放 `currentTime` 3.86 s、`paused=false`，音源为目录流地址 | 通过 |
| AC-12 | 04 / 07 / 12 | — | 已登记与未登记评分/备注、清评分保备注、零下载增量；本候选目录卡标记写入 `PATCH /api/works/31/user-state`，注册端点不写评价 | 通过 |
| AC-13 | 04 | — | 两用户隔离，生产实测 | 通过（沿用） |
| AC-14 | 11 | — | 系列证据、格式副本与成员上限 H/F | 通过（沿用） |
| AC-15 | 12 | — | 系列与单集独立评价 H/F | 通过（沿用） |
| AC-16 | 13 | — | 多方确认与冲突 H/F | 通过（沿用） |
| AC-17 | 14 | — | 高置信唯一候选 H/F | 通过（沿用） |
| AC-18 | 13 | — | 过期预览与重启 H/R | 通过（沿用） |
| AC-19 | 15 | — | 拆分保留 H/R/F | 通过（沿用） |
| AC-20 | 10 | — | 重定位证据 H | 通过（沿用） |
| AC-21 | 08 | 20 partial 重试 | 生产索引 partial：完成 14473、待处理 0、失败 4、未确认空目录 27；4 个失败目录的上游 JSON 名已损坏，不改成 complete | 通过（partial 符合规格） |
| AC-22 | 09 | 20 固定调度 | 每日与周日调度时间可见，无重叠任务 | 通过（沿用） |
| AC-23 | 10 | 23 清理保留 | 清理保护回归 H | 通过（沿用） |
| AC-24 | 16 | 22 父级归属 | `TestTransferV2RestoresPersonalOnlyParentAndRoundTrips`、`TestTransferV2ParentCycleIsReported` | 通过（沿用） |
| AC-25 | 02 / 08 / 17 | 20 低空间组合 | `TestLowDiskPausesScanAndPrefillWithoutStoppingPlayback`：播放中触发低空间，扫描与 prefill 同时 paused，媒体 Range 仍 206、目录与个人备注仍可读；`TestLowDiskPauseAndPersonalRecordSurviveApplicationRestart` 关闭并重开同一个数据库文件后暂停行、暂停原因、目录与个人记录仍在。**该 R 用例只覆盖数据库重新打开，不覆盖播放器队列与播放进度的跨重启恢复。** | 通过（本地 H/R；生产未制造磁盘压力；队列与进度跨重启未覆盖） |
| AC-26 | 17 | 23 文档/证据 | 本账本与部署验收；Full CI `40587302416` 撰写时 in_progress；生产试译、页面翻译、Range、索引面板计数各有证据 | 本轮证据已记录；CI 终态未到达前不写发布级通过 |
| AC-27 | 01 | — | 出站边界 H（非法 URL、越界重定向、超大响应、凭据剥离、取消） | 通过（本地） |
| AC-28 | 17 | — | 只读取文本、操作、请求与持久状态；未新增截图或像素门禁 | 通过（本地 F） |

## 6. 部署与回滚

| 项 | 值 |
| --- | --- |
| 部署主机 | 既有生产主机（受限记录） |
| 部署时间（Asia/Shanghai） | 2026-10-28 05:24（部署脚本 UTC 21:24:09Z） |
| 部署前备份路径（受限记录） | `pre-c7d793e4-20261027T212400Z/`，含 DB/WAL/SHM、`.env`、compose、SHA256SUMS 与 `rollback.sh` |
| 部署前 schema / 镜像 | schema 105 / `kikoto:next-8fa8465e` |
| 回滚镜像 tag | `kikoto:next-8fa8465e` |
| 回滚 schema 兼容矩阵 | 见 [release-rollback.md](../../../docs/development/release-rollback.md) |
| 健康门禁 | `GET /health` 200 `{"status":"ok","version":"v0.8.3"}`；容器 `kikoto` 运行 `kikoto:next-c7d793e4` |
| 部署脚本记录 | `schema_before=105`、`schema_after=105`、`quick_check=ok`、`foreign_key_violations=0`、`status=running` |
| 快照校验 | `sha256sum -c` 3/3 OK（DB、`.env`、compose） |
| 部署后数据库 | `schema_state=105`、`migration_max=105`、`quick_check=ok`、FK 0 |
| 是否触发回滚 | 否 |

部署后逐项验收：

- `/health` 200，版本 `v0.8.3`；新前端主包 `/assets/index-I_z3wgV9.js` 在浏览器实际加载。
- 翻译：单条标题首次 2.08 s 返回 `translated`，随后三次返回 `cacheStatus=hit`（0.83 / 1.19 / 0.83 s）。
- `/voices/4`：首个标题译文 427 ms 出现；页面共 180 个译文节点；滚动到第 3 屏后 159 `existing` + 20 `translated` + 1 `native`，0 `pending`、0 `failed`；所有批量响应 200，无 `not_found`；请求被拆成标题批次（4 / 8 条）与标签批次（11–16 条），同一次合并刷新内标题排在前；刷新后首个标题 726 ms。
- 推荐徽章：来源页只有基线分时徽章数为 0，页面无“此服务暂时不可用”。
- 索引详情：summary 为「覆盖未完成 · 已完成 14473 · 待处理 0 · 失败 4 · 待确认空目录 27」；初始 `details.open=false` 且表格不可见，点击后 `open=true` 且表格可见。
- 目录卡：队列按钮加入 6 个音轨（本机会话队列 0 → 6，提示「已加入 6 个音轨到队列」）；播放后 `audio.currentTime` 3.86 s、`paused=false`，音源为 `/api/source-catalog/<entryId>/media?...`；全程无 `/download` 请求。
- 目录卡记录：标记菜单显示「想听」等选项，选择后按钮标题变为「标记：想听」并出现「已保存并将」提示，请求为 `PATCH /api/works/<workId>/user-state`；列表按钮打开「收藏列表」弹层，账号无列表时显示「暂无列表。」且无错误。
- 回滚：`pre-c7d793e4` 快照 3/3 校验，`rollback.sh` 可执行，回滚目标 `next-8fa8465e`，schema 105。

## 7. 未覆盖项

- 双轴审查与 Full CI `40587302416` 在本文件撰写时仍未到达终态；到达前不得把本候选写为发布通过。
- AC-25 的 R 证据只覆盖「关闭并重开同一个数据库文件」，不覆盖应用重启后的播放器队列、当前轨与播放进度恢复；生产未制造磁盘压力。
- 暂停后整页重载在 `8fa8465e` 上未取得完整证据（脚本在重载后点击队列超时，另一次记录遇到瞬时 `readyState=0`）；本候选未重新构造该场景，不作为通过。
- 真实完整索引未达 complete：4 个上游目录名损坏、27 个空目录未被确认为空；逐项数字在受限运维记录。
- 目录卡的 fork/fetch 仍禁用（来源按需播放）；批量选择排除目录行，未提供目录卡批量入队。
- `source-resource-limits-live` 组未在本候选重跑；该用例重载后只断言队列长度、当前索引、流地址与 `paused`，不比较 `currentTime`。
- 敏感扫描报告的工作区产物（`.kikoto-work/` 与 `.scratch/next.tar`）是本地证据与部署包，不得提交；提交前必须确认它们保持未跟踪。
- 全部卡片未逐页遍历；抽样覆盖首页、无号、系列、失败与未确认空目录。
- 服务器根分区可用空间在部署后为 20 GiB 量级；空间核对是运维观察，不是性能门禁。

## 8. 证据脱敏检查

- [x] 不含真实主机路径、数据库路径、作品编号、生产 entryId/sourceId、真实来源名、密钥或原始日志。
- [x] 镜像 digest 只出现在受限运维记录，仓库只保留 tag。
- [x] `node scripts/check-tracked-evidence.mjs` 结果见第 3 节。
