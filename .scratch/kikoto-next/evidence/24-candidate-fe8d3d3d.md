# 24 候选账本：fe8d3d3d（历史）

> 本文件记录历史候选 `fe8d3d3d` 的取得证据，不再代表当前部署。其后的候选依次为 [`8fa8465e`](24-candidate-8fa8465e.md)、[`c7d793e4`](24-candidate-c7d793e4.md)（当前）；`0aaa83fd`、`ef812140` 与 `c9449653` 的记录保留为更早历史。仓库只写镜像 tag；digest、主机路径与备份位置留在受限运维记录。

## 1. 候选标识

| 项 | 值 |
| --- | --- |
| 候选 SHA | `fe8d3d3d`（完整值见 Git 分支头 `codex/kikoto-catalog-remediation`） |
| 分支 | `codex/kikoto-catalog-remediation` |
| 冻结时间（Asia/Shanghai） | 2026-10-27T22:40:00+08:00（候选冻结；此后只允许 docs 提交） |
| 集成负责人 | 主会话（Codex） |
| 镜像 tag | `kikoto:next-fe8d3d3d` |
| 镜像 digest（受限记录，不写仓库） | 只留在受限运维记录；仓库只写 tag |
| schema 版本 | `105` |
| 迁移链 | `095 → 096 → 097 → 098 → 099 → 100 → 101 → 102 → 103 → 104 → 105`（本候选未新增迁移） |
| 配置指纹（model / base URL / template / reasoning） | 生产沿用既有配置；`config_key` 由 model、base URL、模板版本与推理开关共同决定，本候选未改动 |
| 工作区状态 | 代码冻结时只有账本与证据文档改动；文档提交后 `git status --porcelain` 为空并推送 origin |

## 2. 提交清单

本候选接在 `cf4ada56` 之后新增两个提交；`cf4ada56` 及其之前的 9 个提交是该候选的基线，均在同一分支上。

| 工单 | 提交 | 说明 | 已合入候选 |
| --- | --- | --- | --- |
| 24 | `df3296f2` | 记录 `0aaa83fd` 历史候选与部署 | 是（历史） |
| 24 | `e9c1f05f` | 保留 `ef812140` 候选账本 | 是（历史） |
| 24 | `52055d17` | 记录索引运行重启后的 settled 结果 | 是 |
| 02 / 05 / 20 | `61ead698` | voice `unknown + last_seen` 不再提升为可播；补 handler 用例 | 是 |
| 02 / 05 / 20 | `9fc969b8` | unknown 行保留为可检查的来源引用；后端只认 `available` 可播 | 是 |
| 05 / 20 | `1634ec2d` | 低空间 E2E 记录两轨队列基线 | 是 |
| 05 / 20 | `33cf4932` | 低空间 E2E 断言流身份、队列与暂停/浏览/恢复进度 | 是 |
| 05 / 20 | `3248c0a9` | 前端“可打开”收成 `voiceWorkRemoteTarget` 单一谓词 | 是 |
| 05 / 20 | `cf4ada56` | E2E 使用共享队列存储 key；增加个人评分/备注重载恢复 | 是 |
| 02 / 05 / 20 | `2576e745` | Available 只认当前可用答案；已知作品的 unknown observation 可打开；E2E 恢复本地队列整页重载断言 | 是 |
| 02 / 05 / 20 | `fe8d3d3d` | 可用来源优先于 unknown observation，避免把读者送到待检查来源 | 是 |

`0aaa83fd` 及其之前的 23 个提交见 [24-candidate-c9449653.md](24-candidate-c9449653.md) 第 2 节与其后续账本。

## 3. 门禁结果

| 门禁 | 命令 | 退出码 | 证据 | 结论 |
| --- | --- | --- | --- | --- |
| 双轴审查 Standards | `git diff 0aaa83fd...fe8d3d3d` | — | 受限记录 | 本轮修复了 Available/presence 混用、已知作品 unknown 不可打开、E2E 队列重载断言缺失、真实来源名入仓与 healthcheck 矛盾；无未处理硬违规 |
| 双轴审查 Spec | 同上 | — | 同上 | 本轮修复后，D-06 的“unknown 可检查、不可冒充可播”与 AC-25 的队列重载证据闭环；AC-27/28 仍按本地口径 |
| 后端构建 | `cd backend && go build ./...` | 0 | 本地执行 | 通过 |
| 后端静态检查 | `cd backend && go vet ./...` | 0 | 本地执行 | 通过 |
| 后端定向测试 | `cd backend && go test ./internal/httpapi` | 0 | 本地执行 | 通过；voice catalog unknown/available 用例在包内 |
| 前端类型检查 | `npx tsc -b` | 0 | 本地执行 | 通过 |
| 前端 E2E 类型检查 | `npm run typecheck:e2e` | 0 | 本地执行 | 通过 |
| 前端 lint | `npx oxlint src --deny-warnings` | 0 | 本地执行 | 通过 |
| 前端格式 | `npm run format:check` | 0 | 本地执行 | 通过 |
| 前端单元测试 | `npm run test:unit` | 0 | 本地执行 | 164 文件 / 947 测试 |
| 文档链接 | `node scripts/check-doc-links.mjs` | 0 | 本地执行 | 134 份 Markdown |
| 文档 locale | `node scripts/check-doc-locales.mjs` | 0 | 本地执行 | 5 locale / 11 页 |
| tracked-evidence | `node scripts/check-tracked-evidence.mjs` | 0 | 本地执行 | 56 个文件 |
| 敏感扫描 | `node scripts/check-sensitive.mjs` | 0 | 本地执行 | 变更文件通过 |
| 播放组合浏览器用例 | `node scripts/run-e2e-fixtures.mjs --group=source-resource-limits-live` | 0 | `frontend/test-results/e2e-fixtures` | 1 passed（8.9 s）；含队列/流/个人记录整页重载 |
| Full CI | `workflow_dispatch` run `40523343369` | 0 | GitHub Actions | 27/27 job success |
| 迁移：新装 | `TestMigrationChain105FreshInstall` | 0 | 上一候选 `f727feee` 记录（历史） | 本候选未改迁移，沿用 |
| 迁移：068 升级 | `TestMigrationChain105Upgrades068AndKeepsUserData` | 0 | 同上（历史） | 同上 |
| 迁移：084 升级 | `TestMigrationChain105Upgrades084ToCurrentShape` | 0 | 同上（历史） | 同上 |
| 迁移：重复启动 | `TestMigrationChain105RestartIsIdempotent` | 0 | 同上（历史） | 同上 |
| `PRAGMA foreign_key_check` | 部署后脚本校验 | 0 | 部署记录 | 0 行 |
| 备份与恢复演练 | 部署脚本快照 + `sha256sum -c` | — | 部署记录 | DB、`.env`、compose 3/3 OK |
| 回滚目标 | `pre-fe8d3d3d` 快照指向 `next-0aaa83fd` | 0 | 部署记录 | schema 105；脚本可执行 |
| 组合负载 | `scripts/mixed-load.mjs` 60 秒、并发 8 | 见 20 号票 | [20 号票](../issues/20-catalog-sync-and-admission.md) | 通过；数值只在 20 与 21 号票 |
| 真实完整索引 | 生产索引运行 | partial | 来源目录索引进度接口 | 见第 6 节；按量级记录 |
| LIVE 翻译 | 生产真实模型 | 200 | 部署验收记录 | 见第 6 节 |
| LIVE 媒体 Range | 生产真实音频 | 206 | 部署验收记录 | 见第 6 节 |
| LIVE 浏览器 | 应用内浏览器 + 管理员会话 | — | [25-live-browser.md](25-live-browser.md) | 语言、短查询、多来源、播放连续性、队列重载、输入边界均实测 |
| 非视觉 F | 前端单元与 E2E | 0 | 同上 | 通过 |
| 部署后验收 | 见第 6 节 | — | 部署验收记录 | 见第 6 节 |

## 4. 性能门禁

| 指标 | 环境 | 样本 | 门槛 | 结论 | 数值出处 |
| --- | --- | --- | --- | --- | --- |
| 42k 卡浏览 | 2 vCPU / 3.5 GiB, warm | 30 | ≤300 / ≤800 ms | 通过 | [20 号票](../issues/20-catalog-sync-and-admission.md) |
| 5,001 文件卡拒绝 | 同上 | 30 | ≤300 / ≤800 ms | 通过 | 同上 |
| 译文 3 字以上 | modernc SQLite, 456k 行 | 20 | ≤100 ms | 通过 | [21 号票](../issues/21-translation-corpus-and-search.md) |
| 译文 1–2 字 | 114,000 实体，全部标题命中 | 20 | ≤100 ms | 通过 | 同上 |
| 编号等值 | 同上 | 20 | <5 ms | 通过 | 同上 |
| 目标语言标题整串等值 | 同上 | 20 | <5 ms | 通过 | 同上 |

数值只在上述来源写一次；本账本记录候选上是否达标，不重复抄写测量值。

## 5. AC 覆盖

本表只记录本候选实际取得的证据；未在候选上重跑的原始主责测试沿用其记录 SHA 的历史结论，不冒充当前候选。

| AC | 原始主责 | 整改子项 | 当前证据 | 结论 |
| --- | --- | --- | --- | --- |
| AC-01 | 01 | — | 配置读写与掩码 H 测试；生产试译使用已保存配置 | 通过（本地 H + 生产 LIVE） |
| AC-02 | 01 | 21 错误分类 | 空/超长/不支持语言 400 分类，生产实测；成功试译 200；8192 字节被接受、8193 字节被拒绝；8192 字节有效日文请求返回 `translation_timeout` 且不重试；截断/鉴权/限流/超时/格式错由 `TestTranslationTrialFailuresAreClassifiedWithoutLeakingOrRetrying` 覆盖 14 种分类且不重试、不回显上游细节 | 通过（生产 LIVE + 本地分类） |
| AC-03 | 02 | 21 显示语言搜索 | 两用户偏好 H/R；严格语言搜索 H | 通过（本地） |
| AC-04 | 02 | — | 保护名称不外发 H/F | 通过（本地） |
| AC-05 | 02 | — | 延迟、失败、取消 H/F；未完成提示与 20 秒上限有浏览器用例 | 通过（本地） |
| AC-06 | 02 | 21 缓存键与保留 | 缓存键、保留与预算 H | 通过（本地） |
| AC-07 | 03 | — | 简繁复用与 LIVE 转换 | 通过（生产 LIVE） |
| AC-08 | 07 / 08 | 20 D-16、21 译文可搜 | 42k 浏览达标（数值见 20 号票）；无码/无编号卡片与短查询 `truncated` 生产实测；分支被扫描上界截断时也报 `truncated`（`TestContentTranslationSearchRemoteShortBranchSaturationIsReported`） | 通过 |
| AC-09 | 06 / 07 | — | 旧引用与无码身份 H/R | 通过（本地） |
| AC-10 | 05 | — | 目录检查与多源 H/F；当前构建生产实测本地 1 音轨 ↔ 远程 4 音轨切换，单源失败保留本地结果；unknown 行的可打开/不可播由 `voiceWorkRemoteTarget` 与 handler 用例覆盖 | 通过（本地 + 生产） |
| AC-11 | 05 | — | 播放隔离与进度 H/F；生产 Range 206；当前构建播放连续性 0.93 s → 3.49 s，`paused=false`；整页重载后队列 `1 / 21`、暂停 | 通过 |
| AC-12 | 04 / 07 / 12 | — | 已登记与未登记评分/备注、清评分保备注、零下载增量，生产实测；E2E 整页重载恢复个人评分/备注 | 通过 |
| AC-13 | 04 | — | 两用户隔离，生产实测 | 通过 |
| AC-14 | 11 | — | 系列证据、格式副本与成员上限 H/F | 通过（本地） |
| AC-15 | 12 | — | 系列与单集独立评价 H/F | 通过（本地） |
| AC-16 | 13 | — | 多方确认与冲突 H/F | 通过（本地） |
| AC-17 | 14 | — | 高置信唯一候选 H/F | 通过（本地） |
| AC-18 | 13 | — | 过期预览与重启 H/R | 通过（本地） |
| AC-19 | 15 | — | 拆分保留 H/R/F | 通过（本地） |
| AC-20 | 10 | — | 重定位证据 H | 通过（本地） |
| AC-21 | 08 | 20 partial 重试 | 生产索引 partial，计数与原因见第 6 节；未称 complete | 通过（partial 符合规格） |
| AC-22 | 09 | 20 固定调度 | 每日与周日调度时间可见，无重叠任务 | 通过 |
| AC-23 | 10 | 23 清理保留 | 清理保护回归 H | 通过（本地） |
| AC-24 | 16 | 22 父级归属 | `TestTransferV2RestoresPersonalOnlyParentAndRoundTrips`、`TestTransferV2ParentCycleIsReported` | 通过（本地） |
| AC-25 | 02 / 08 / 17 | 20 低空间组合 | 本地 H 同时暂停扫描与 prefill；E2E 低空间暂停中队列/流/进度保持，整页重载后本地双音轨队列长度、当前索引、流地址、暂停状态与个人评分/备注恢复（未比较重载后的 `currentTime`）；生产当前构建播放连续性 0.93 s → 3.49 s，暂停后重载队列 `1 / 21` | 通过（本地 H + 隔离 E2E；生产未制造磁盘压力；重载后播放位置未比对） |
| AC-26 | 17 | 23 文档/证据 | 本账本与部署验收；Full CI `40523343369` @ `fe8d3d3d`；生产试译 200（419 ms）、Range 206、进度接口 200；索引面板显示完成/待处理/失败/空目录计数 | 发布级证据已取得；进度面板公网边缘延迟作为运维观察 |
| AC-27 | 01 | — | 出站边界 H（非法 URL、越界重定向、超大响应、凭据剥离、取消） | 通过（本地） |
| AC-28 | 17 | — | 截图关闭与既有视觉测试不改写 | 本地 F 证据 |

## 6. 部署与回滚

| 项 | 值 |
| --- | --- |
| 部署主机 | 既有生产主机（受限记录） |
| 部署时间（Asia/Shanghai） | 2026-10-27 22:44（部署脚本 UTC 14:44:03Z） |
| 部署前备份路径（受限记录） | `pre-fe8d3d3d-20261027T144403Z/`，含 DB/WAL/SHM、`.env`、compose、SHA256SUMS 与 `rollback.sh` |
| 部署前 schema / 镜像 | schema 105 / `kikoto:next-cf4ada56` |
| 回滚镜像 tag | `kikoto:next-0aaa83fd` |
| 回滚 schema 兼容矩阵 | 见 [release-rollback.md](../../../docs/development/release-rollback.md) |
| 健康门禁 | `GET /health` 200 `{"status":"ok","version":"v0.8.3"}`；compose healthcheck 生效，容器 `healthy` |
| 部署脚本记录 | `schema_before=105`、`schema_after=105`、`git_sha=fe8d3d3d`、`quick_check=ok`、`foreign_key_violations=0`、`status=running` |
| 快照校验 | `sha256sum -c` 3/3 OK（DB、`.env`、compose） |
| 部署后数据库 | `schema_state=105`、`migration_max=105`、`quick_check=ok`、FK 0 |
| 是否触发回滚 | 否 |
| 部署代码等价说明 | 部署镜像代码为 `fe8d3d3d`；其后 docs 提交不改变镜像，账本注明“代码等价于 `fe8d3d3d`” |

部署后逐项验收：

- `/health` 200，版本 `v0.8.3`；容器 `kikoto` 运行 `kikoto:next-fe8d3d3d`，`docker inspect` 健康状态 `healthy`；新前端主包 `index-CVkyIuio.js` 在浏览器实际加载。
- 试译：HTTP 200，返回 `今天天气很好。`，`model=Index-Translate-35B-A3B`，页面显示 419 ms。
- 试译错误分类：空白 400 `translation_empty_text`；不支持语言 400 `translation_unsupported_language`；8193 字节 400 `translation_text_too_long`。
- 8192 字节有效日文请求：通过前端校验并到达模型，HTTP 422 `translation_timeout`，页面显示超时且不自动重试；不伪报成功。
- 短查询：浏览器显示 `1,000 个作品 第 1/42 页` 与截断说明；API `total=1000`、`truncated=true`、24 条。
- 多来源：本地 1 音轨 ↔ 远程 4 音轨 23m；个人备注跨来源不变；单源失败时本地结果保留，页面提供重试。
- 播放连续性：当前构建 `audio.currentTime` 0.93 s → 3.49 s，`paused=false`；暂停后整页重载队列 `1 / 21`、`paused=true`。
- 媒体 Range：HTTP 206，`Content-Range: bytes 0-1023/<总长>`，读取 1024 字节，`audio/mpeg`；匿名请求同一地址 401。
- 索引运行 53：`partial`（完成 14472、待处理 0、失败 4、未确认空目录 27、受限 0；可播放卡片 41572）。4 个失败目录名含 U+FFFD，上游原始 JSON 已损坏；27 个空目录是上游空列表。编码守卫上线前开始，所以 `encodingUnresolvedDirectories=0`；不改写为 complete。
- 增量同步运行 62：`partial`（3034 探测、6 变化、31 列出；4 个变化目录未列出、31 个范围未解决）。
- 进度接口：公网边缘 1.9–5.5 s；直接 SQL 29 ms 以内。慢在公网/边缘或请求排队，不在查询本身。
- 回滚：`pre-fe8d3d3d` 快照 3/3 校验，`rollback.sh` 可执行；回滚目标 `next-0aaa83fd`，schema 105。

浏览器逐项证据见 [25-live-browser.md](25-live-browser.md)；该文件的 22:00–22:50 段对应 `cf4ada56`，22:44 后的复验对应 `fe8d3d3d`，两者用页面实际加载的主包区分。

## 7. 未覆盖项

- AC-25 的“播放中同时低空间”组合只在本地 H 与隔离 E2E 构造；生产浏览器未制造磁盘压力。
- AC-27 出站边界仍只有本地 H 证据；未在生产构造恶意出站请求。
- AC-28 仍只有本地 F 证据；生产检查只读取文本、DOM 计算样式与网络结果，没有新增截图、像素或布局门禁。
- 8192 字节有效文本的模型调用超时，未取得成功译文；这是已分类超时，不作为成功证据。
- 真实完整索引未达 complete：4 个上游目录名损坏、27 个空目录未被确认为空；逐项数字在受限运维记录。
- 全部卡片未逐页遍历；抽样覆盖首页、无码、系列、unavailable 与 unconfirmed 卡片。
- 前端渲染已对来源页、媒体库、作品详情、翻译设置和语言菜单做生产浏览器检查；未逐页遍历全部卡片。

## 7.1 运维说明

- 回滚：`bash <部署回滚目录>/rollback.sh`。该脚本恢复切换前的数据库、`.env` 与 compose，并重新启动上一镜像 `kikoto:next-0aaa83fd`；快照已含 `SHA256SUMS` 校验。
- 部署脚本在容器停止后才复制 DB/WAL/SHM，因此快照与运行态一致；健康门禁失败会自动回滚。
- 当前服务器保留 `next-fe8d3d3d`、`next-0aaa83fd`、`next-cf4ada56` 及更深回滚所需镜像；旧构建目录、临时 DB 副本和明显无用的 tar 已按存储清理记录删除，不执行 blanket `docker image prune`。
- 下一次每日索引与周日全量索引按既有调度执行，未因本次部署改动时间。

## 8. 证据脱敏检查

- [x] 不含真实主机路径、数据库路径、作品编号、生产 entryId/sourceId、真实来源名、密钥或原始日志。
- [x] 镜像 digest 只出现在受限运维记录，仓库只保留 tag。
- [x] `node scripts/check-tracked-evidence.mjs` 通过。
