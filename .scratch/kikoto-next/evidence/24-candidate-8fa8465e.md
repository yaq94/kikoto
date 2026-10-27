# 24 候选账本：8fa8465e（历史）

> 本文件记录历史候选 `8fa8465e` 的取得证据，不再代表当前部署。当前候选为 [`c7d793e4`](24-candidate-c7d793e4.md)；`fe8d3d3d`、`0aaa83fd`、`ef812140` 与 `c9449653` 的记录保留为更早历史。仓库只写镜像 tag；digest、主机路径与备份位置留在受限运维记录。

## 1. 候选标识

| 项 | 值 |
| --- | --- |
| 候选 SHA | `8fa8465e`（历史） |
| 分支 | `codex/kikoto-catalog-remediation` |
| 冻结时间（Asia/Shanghai） | 2026-10-28T04:15:00+08:00（候选冻结；此后只允许 docs 提交） |
| 集成负责人 | 主会话（Codex） |
| 镜像 tag | `kikoto:next-8fa8465e` |
| 镜像 digest（受限记录，不写仓库） | 只留在受限运维记录；仓库只写 tag |
| schema 版本 | `105` |
| 迁移链 | `095 → 096 → 097 → 098 → 099 → 100 → 101 → 102 → 103 → 104 → 105`（本候选未新增迁移） |
| 配置指纹（model / base URL / template / reasoning） | 生产沿用既有配置；`config_key` 由 model、base URL、模板版本与推理开关共同决定，本候选未改动 |
| 工作区状态 | 代码冻结时只有账本与证据文档改动；文档提交后 `git status --porcelain` 为空并推送 origin |

## 2. 提交清单

本候选接在 `fe8d3d3d` 之后新增四个提交。

| 工单 | 提交 | 说明 | 已合入候选 |
| --- | --- | --- | --- |
| 02 / 05 / 20 | `72aefe65` | 拆分“可打开”与“可确认可用”：打开与 Remote 过滤认 available + unknown，fetch/fork 只认 available；来源 chip 携带自身 code | 是 |
| 02 / 05 / 20 | `c4f43eb4` | `circleSourceBadges` 去掉未使用的 `remote` 参数，chip 只读 `sourceTags`；补后端 source code 投影测试 | 是 |
| 05 / 20 | `c8542c13` | 低空间暂停与个人记录跨应用重启的 R 测试（关闭并重开同一个数据库文件） | 是 |
| 02 / 05 / 20 | `8fa8465e` | 同源多 edition 时不再取最小 code：只有全部匹配 work 的 code 一致才投影，否则留空 | 是 |

更早的 23 个提交与 `ef812140`、`0aaa83fd`、`cf4ada56`、`fe8d3d3d` 的归属见 [24-candidate-fe8d3d3d.md](24-candidate-fe8d3d3d.md) 第 2 节与其链接。

## 3. 门禁结果

| 门禁 | 命令 | 退出码 | 证据 | 结论 |
| --- | --- | --- | --- | --- |
| 双轴审查 Standards | `git diff 0aaa83fd...8fa8465e` | — | 受限记录 | 本轮修复了可打开与可下载谓词混用、来源 chip 接收未使用参数、同源多 edition 猜测 code、E2E 固定等待掩盖过渡态、低空间暂停缺少跨重启 R 证据；无未处理硬违规 |
| 双轴审查 Spec | 同上 | — | 同上 | 修复后 D-06 的“unknown 可检查、不可冒充可播”与 AC-25 的 R 证据闭环；AC-27/28 仍按本地口径 |
| 后端构建 | `cd backend && go build ./...` | 0 | 本地执行 | 通过 |
| 后端静态检查 | `cd backend && go vet ./...` | 0 | 本地执行 | 通过 |
| 后端整包测试 | `cd backend && go test ./internal/httpapi -count=1` | 0 | 本地执行 | 通过；575.417 s（在 `c8542c13` 上执行） |
| 后端定向测试 | `go test ./internal/httpapi -run "TestLoadVoiceKnownWorksProjectsTheCodeEachSourceHolds|WithholdsAnAmbiguousSourceCode|PrefersConcreteAvailableLocation"` | 0 | 本地执行 | 通过；`8fa8465e` 之后只跑定向 |
| 前端类型检查 | `npx tsc -b` | 0 | 本地执行 | 通过 |
| 前端 E2E 类型检查 | `npm run typecheck:e2e` | 0 | 本地执行 | 通过 |
| 前端 lint | `npx oxlint src --deny-warnings` | 0 | 本地执行 | 通过 |
| 前端格式 | `npm run format:check` | 0 | 本地执行 | 通过 |
| 前端单元测试 | `npm run test:unit` | 0 | 本地执行 | 164 文件 / 950 测试 |
| 前端构建 | `npm run build` | 0 | 本地执行 | 产物主包 `index-D0M4aHWF.js` |
| 播放组合浏览器用例 | `node scripts/run-e2e-fixtures.mjs --group=source-resource-limits-live` | 0 | `frontend/test-results/e2e-fixtures` | 1 passed（9.2–9.6 s）；含队列面板恢复断言 |
| 文档链接 | `node scripts/check-doc-links.mjs` | 0 | 本地执行 | 134 份 Markdown |
| 文档 locale | `node scripts/check-doc-locales.mjs` | 0 | 本地执行 | 5 locale / 11 页 |
| tracked-evidence | `node scripts/check-tracked-evidence.mjs` | 0 | 本地执行 | 56 个文件 |
| 敏感扫描 | 变更文件扫描 | 0 | 本地执行 | 13 个文件通过 |
| Full CI | `workflow_dispatch` run `40574379570` | 0 | GitHub Actions | 27/27 job success（终态已核实） |
| 迁移：新装 / 升级 / 重复启动 | 迁移链测试四项 | — | 上一候选 `f727feee` 记录（历史） | 本候选未改迁移，沿用 |
| `PRAGMA foreign_key_check` | 部署脚本校验 | 0 | 部署记录 | 0 行 |
| 备份与恢复演练 | 部署脚本快照 + `sha256sum -c` | 0 | 部署记录 | DB、`.env`、compose 3/3 OK |
| 回滚目标 | `pre-8fa8465e` 快照指向 `next-fe8d3d3d` | 0 | 部署记录 | schema 105；`rollback.sh` 可执行 |
| 部署后验收 | 见第 6 节 | — | 部署验收记录 | 见第 6 节 |

## 4. 性能门禁

| 指标 | 环境 | 样本 | 门槛 | 结论 | 数值出处 |
| --- | --- | --- | --- | --- | --- |
| 42k 卡浏览 | 2 vCPU / 3.5 GiB, warm | 30 | ≤300 / ≤800 ms | 通过（沿用） | [20 号票](../issues/20-catalog-sync-and-admission.md) |
| 5,001 文件卡拒绝 | 同上 | 30 | ≤300 / ≤800 ms | 通过（沿用） | 同上 |
| 译文 3 字以上 | modernc SQLite, 456k 行 | 20 | ≤100 ms | 通过（沿用） | [21 号票](../issues/21-translation-corpus-and-search.md) |
| 译文 1–2 字 | 114,000 实体，全部标题命中 | 20 | ≤100 ms | 通过（沿用） | 同上 |
| 编号等值 | 同上 | 20 | <5 ms | 通过（沿用） | 同上 |
| 目标语言标题整串等值 | 同上 | 20 | <5 ms | 通过（沿用） | 同上 |

## 5. AC 覆盖

| AC | 原始主责 | 整改子项 | 当前证据 | 结论 |
| --- | --- | --- | --- | --- |
| AC-01 | 01 | — | 配置读写与掩码 H；生产试译 200 | 通过（本地 H + 生产 LIVE） |
| AC-02 | 01 | 21 错误分类 | 空/超长/不支持语言 400；截断/鉴权/限流/超时/格式错由分类测试覆盖 | 通过 |
| AC-03 | 02 | 21 显示语言搜索 | 两用户偏好 H/R；严格语言搜索 H | 通过（本地） |
| AC-04 | 02 | — | 保护名称不外发 H/F | 通过（本地） |
| AC-05 | 02 | — | 延迟、失败、取消 H/F；20 秒显示上限有浏览器用例 | 通过（本地） |
| AC-06 | 02 | 21 缓存键与保留 | 缓存键、保留与预算 H | 通过（本地） |
| AC-07 | 03 | — | 简繁复用与 LIVE 转换 | 通过（生产 LIVE） |
| AC-08 | 07 / 08 | 20 D-16、21 译文可搜 | 42k 浏览达标；无码/无编号卡片与短查询 `truncated` 生产实测 | 通过 |
| AC-09 | 06 / 07 | — | 旧引用与无码身份 H/R | 通过（本地） |
| AC-10 | 05 | — | 目录检查与多源 H/F；生产本地 1 音轨 ↔ 远程 4 音轨切换；单源失败保留本地结果 | 通过 |
| AC-11 | 05 | — | 播放隔离与进度 H/F；生产 Range 206；播放 1.58 s → 4.09 s、`paused=false` | 通过 |
| AC-12 | 04 / 07 / 12 | — | 已登记与未登记评分/备注、清评分保备注、零下载增量 | 通过 |
| AC-13 | 04 | — | 两用户隔离，生产实测 | 通过 |
| AC-14 | 11 | — | 系列证据、格式副本与成员上限 H/F | 通过（本地） |
| AC-15 | 12 | — | 系列与单集独立评价 H/F | 通过（本地） |
| AC-16 | 13 | — | 多方确认与冲突 H/F | 通过（本地） |
| AC-17 | 14 | — | 高置信唯一候选 H/F | 通过（本地） |
| AC-18 | 13 | — | 过期预览与重启 H/R | 通过（本地） |
| AC-19 | 15 | — | 拆分保留 H/R/F | 通过（本地） |
| AC-20 | 10 | — | 重定位证据 H | 通过（本地） |
| AC-21 | 08 | 20 partial 重试 | 生产索引 partial：完成 14472、待处理 0、失败 4、未确认空目录 27 | 通过（partial 符合规格） |
| AC-22 | 09 | 20 固定调度 | 每日与周日调度时间可见，无重叠任务 | 通过 |
| AC-23 | 10 | 23 清理保留 | 清理保护回归 H | 通过（本地） |
| AC-24 | 16 | 22 父级归属 | `TestTransferV2RestoresPersonalOnlyParentAndRoundTrips`、`TestTransferV2ParentCycleIsReported` | 通过（本地） |
| AC-25 | 02 / 08 / 17 | 20 低空间组合 | 本地 H 同时暂停扫描与 prefill；`TestLowDiskPauseAndPersonalRecordSurviveApplicationRestart` 关闭并重开同一个数据库文件后暂停行、暂停原因、目录与个人记录仍在；E2E 低空间暂停中队列/流/个人记录整页重载保持一致。**R 用例不覆盖播放器队列与播放进度的跨重启恢复。** | 通过（本地 H + 隔离 E2E；生产未制造磁盘压力；队列与进度跨重启未覆盖） |
| AC-26 | 17 | 23 文档/证据 | 本账本与部署验收；Full CI `40574379570` 27/27；生产试译 200（423 ms）、Range 206、索引面板计数 | 发布级证据已取得 |
| AC-27 | 01 | — | 出站边界 H | 通过（本地） |
| AC-28 | 17 | — | 只读取文本、操作、请求与持久状态 | 通过（本地 F） |

## 6. 部署与回滚

| 项 | 值 |
| --- | --- |
| 部署主机 | 既有生产主机（受限记录） |
| 部署时间（Asia/Shanghai） | 2026-10-28 04:19（部署脚本 UTC 20:19:37Z） |
| 部署前备份路径（受限记录） | `pre-8fa8465e-20261027T201929Z/`，含 DB/WAL/SHM、`.env`、compose、SHA256SUMS 与 `rollback.sh` |
| 部署前 schema / 镜像 | schema 105 / `kikoto:next-fe8d3d3d` |
| 回滚镜像 tag | `kikoto:next-fe8d3d3d` |
| 回滚 schema 兼容矩阵 | 见 [release-rollback.md](../../../docs/development/release-rollback.md) |
| 健康门禁 | `GET /health` 200 `{"status":"ok","version":"v0.8.3"}`；容器 `kikoto` 运行 `kikoto:next-8fa8465e`，`healthy` |
| 部署脚本记录 | `schema_before=105`、`schema_after=105`、`quick_check=ok`、`foreign_key_violations=0`、`status=running` |
| 快照校验 | `sha256sum -c` 3/3 OK（DB、`.env`、compose） |
| 部署后数据库 | `schema_state=105`、`migration_max=105`、`quick_check=ok`、FK 0 |
| 是否触发回滚 | 否 |

部署后逐项验收：

- `/health` 200，版本 `v0.8.3`；新前端主包 `/assets/index-D0M4aHWF.js` 在浏览器实际加载。
- 试译 `今日はいい天気です。`：页面显示 `Index-Translate-35B-A3B · 423 毫秒`，返回 `今天天气很好。`。
- 输入边界：8192 个 ASCII 字节被接受（`已输入 8192 / 8192 字节`），8193 字节被拒绝并提示超过上限。
- 短查询：来源范围单字查询显示 `第 1 / 42 页，共 1000 项作品` 与 `仅显示前 1000 个匹配结果`。
- 多来源：本地 `1 个音轨 · <1m` ↔ 远程 `4 个音轨 · 23m`；个人备注不变；页面写明“同一作品的各个来源共用这份记录”。
- 播放：`audio.currentTime` 1.58 s → 4.09 s，`paused=false`，队列 `1 / 21`；点击播放器 `aria-label="暂停"` 后停在 4.12 s，等待 2.5 s 未自行恢复。
- 媒体 Range：206，`Content-Range: bytes 0-1023/<总长>`，`audio/mpeg`，1024 字节。
- **未取得**：暂停后整页重载的恢复证据。`pause-reload-8fa8465e.mjs` 在重载后打开队列面板时超时；另一次记录遇到瞬时 `readyState=0`。该场景在 `fe8d3d3d` 上有成功记录，但不能移作本候选的证据。
- 回滚：`pre-8fa8465e` 快照 3/3 校验，`rollback.sh` 可执行，回滚目标 `next-fe8d3d3d`，schema 105。

## 7. 未覆盖项

- 暂停后整页重载的播放器恢复未在本候选取得完整证据。
- AC-25 的 R 证据只覆盖数据库重新打开，不覆盖队列与播放进度的跨重启恢复；隔离 E2E 重载后只断言队列长度、当前索引、流地址与 `paused`，不比较 `currentTime`。生产未制造磁盘压力。
- AC-27 出站边界与 AC-28 仍只有本地 H/F 证据。
- 真实完整索引未达 complete：4 个上游目录名损坏、27 个空目录未被确认为空。
- 全部卡片未逐页遍历。

## 8. 证据脱敏检查

- [x] 不含真实主机路径、数据库路径、作品编号、生产 entryId/sourceId、真实来源名、密钥或原始日志。
- [x] 镜像 digest 只出现在受限运维记录，仓库只保留 tag。
- [x] `node scripts/check-tracked-evidence.mjs` 通过。
