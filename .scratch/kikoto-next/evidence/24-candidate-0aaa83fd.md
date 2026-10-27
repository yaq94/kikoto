# 24 候选账本：0aaa83fd

> 本文件记录本轮最终候选 `0aaa83fd` 的取得证据；`ef812140` 与 `c9449653` 的记录保留为上一候选历史。仓库只写镜像 tag；digest、主机路径与备份位置留在受限运维记录。

## 1. 候选标识

| 项 | 值 |
| --- | --- |
| 候选 SHA | `0aaa83fd`（完整值见 Git 分支头 `codex/kikoto-catalog-remediation`） |
| 分支 | `codex/kikoto-catalog-remediation` |
| 冻结时间（Asia/Shanghai） | 2026-10-27T19:26:00+08:00（候选冻结；此后只允许 docs 提交） |
| 集成负责人 | 主会话（Codex） |
| 镜像 tag | `kikoto:next-0aaa83fd` |
| 候选后 docs 提交 | 允许；账本记录时必须注明“代码等价于 `0aaa83fd`” |
| 镜像 digest（受限记录，不写仓库） | 只留在受限运维记录；仓库只写 tag |
| schema 版本 | `105` |
| 迁移链 | `095 → 096 → 097 → 098 → 099 → 100 → 101 → 102 → 103 → 104 → 105`（本候选未新增迁移） |
| 配置指纹（model / base URL / template / reasoning） | 生产沿用既有配置；`config_key` 由 model、base URL、模板版本与推理开关共同决定，本候选未改动 |
| 工作区状态 | 代码冻结时只有账本与证据文档改动；文档提交后 `git status --porcelain` 为空并推送 origin |

## 2. 提交清单

本轮接在 `c9449653` 之后共 9 个提交；其中 `ef812140` 已部署过一次，`0aaa83fd` 修复审查发现的短查询与标记规则后重新构建部署：

| 工单 | 提交 | 说明 |
| --- | --- | --- |
| 21 | `3becd805` | 一次短查询只用一个卡片预算：各分支扫描上界不动，合并去重后按展示顺序裁到 1000；总数读同一语句多一行 |
| 20 / 02 | `4facf5d9` | voice 行按自身证据判断可播；标记字段改为 `needsLabel`；Library 真实链路统一排序；短查询规则只写在数据模型 |
| 24 | `ce0586cc` | 账本记录 CI 终态，部署计数改量级，回滚行改为实测结果 |
| 21 / 20 | `eab127f6` | 卡片预算与分支扫描上界各自命名；展示顺序收成一个常量；删除无调用的 overflow 探测；Makefile 测试超时可配置并说明理由 |
| 24 | `de8b9d9d` | 提交计数与审查区间对齐 |
| 24 | `ef812140` | 删除会随每提交变化的计数复述 |

| 24 / 部署 | `6927083a` | 生产 compose 增加 `/health` healthcheck，回滚文档同步；镜像二进制不变 |
| 20 | `f2bdce8a` | 卡片“必须读文本”的判定收成一个 `badgeNeedsText`，warning 与 verdict 不再分叉；文档去掉短查询预算的重复陈述 |
| 21 | `0aaa83fd` | 短查询分支被扫描上界截断时也标 `truncated`，不再把折叠后的少量卡片当成完整答案；新增折叠分支用例 |

`ef812140` 及其之前的提交见 [上一候选账本](24-candidate-ef812140.md) 第 2 节，`c9449653` 更早的 23 个提交见 [24-candidate-c9449653.md](24-candidate-c9449653.md) 第 2 节。

## 3. 门禁结果

| 门禁 | 命令 | 退出码 | 证据 | 结论 |
| --- | --- | --- | --- | --- |
| 双轴审查 Standards | `git diff 88498e94...ce0586cc` 与其后修复 | — | 受限记录 | 首轮 4 条硬违规、次轮 4 条硬违规与 7 条判断项全部处理；末轮复核见本节末 |
| 双轴审查 Spec | `git diff 88498e94...ce0586cc` 与其后修复 | — | 同上 | 首轮 1 blocker + 5 major、次轮全部处理；部署前未留未修项 |
| 后端构建 | `cd backend && go build ./...` | 0 | 本地执行 | 通过 |
| 后端静态检查 | `cd backend && go vet ./...` | 0 | 本地执行 | 通过 |
| 后端全量测试 | `cd backend && go test -timeout=45m ./... -count=1` | 0 | `.kikoto-work/logs/go-test-all-0aaa83fd.log` | 全部包通过；`internal/httpapi` 573 s |
| 后端竞态测试 | CI `Backend (race 1/4…4/4)` | 0 | 本候选 Full CI | 4 片全部 success |
| 前端类型检查 | `npx tsc -b` | 0 | 本地执行 | 通过 |
| 前端 E2E 类型检查 | `npm run typecheck:e2e` | 0 | 本地执行 | 通过 |
| 前端 lint | `npx oxlint src --deny-warnings` | 0 | 本地执行 | 718 文件 0 warning |
| 前端格式 | `npm run format:check` | 0 | 本地执行 | 通过 |
| 前端单元测试 | `npm run test:unit` | 0 | 本地执行 | 164 文件 / 943 测试 |
| 文档链接 | `node scripts/check-doc-links.mjs` | 0 | 本地执行 | 134 份 Markdown |
| 文档 locale | `node scripts/check-doc-locales.mjs` | 0 | 本地执行 | 5 locale / 11 页 |
| tracked-evidence | `node scripts/check-tracked-evidence.mjs` | 0 | 本地执行 | 53 个文件 |
| 敏感扫描 | `make sensitive-check` | 0 | 本地执行 | 0 个变更文件命中 |
| 翻译浏览器用例 | `node scripts/run-e2e-fixtures.mjs --group=content-translation` | 0 | `frontend/test-results/e2e-fixtures` | 2 passed（简繁复用、旧请求不覆盖新语言） |
| 试译设置浏览器用例 | `--group=translation-settings` | 0 | 同上 | 3 passed（含超限按键被拒绝） |
| 播放组合浏览器用例 | `--group=source-playback` | 0 | 同上 | 3 组通过；含“低空间暂停时播放与个人记录存活” |
| Full CI | `workflow_dispatch` run `40496141828` | 0 | GitHub Actions | 27/27 job success |
| 迁移：新装 | `TestMigrationChain105FreshInstall` | 0 | 上一候选 `f727feee` 记录（历史） | 本候选未改迁移，沿用 |
| 迁移：068 升级 | `TestMigrationChain105Upgrades068AndKeepsUserData` | 0 | 同上（历史） | 同上 |
| 迁移：084 升级 | `TestMigrationChain105Upgrades084ToCurrentShape` | 0 | 同上（历史） | 同上 |
| 迁移：重复启动 | `TestMigrationChain105RestartIsIdempotent` | 0 | 同上（历史） | 同上 |
| `PRAGMA foreign_key_check` | 部署后脚本校验 | 0 | 部署记录 | 0 行 |
| 备份与恢复演练 | 部署脚本快照 + `sha256sum -c` | — | 部署记录 | 快照含 DB/WAL/SHM、`.env`、compose、SHA256SUMS 与 `rollback.sh`，校验全部 OK |
| 回滚演练（隔离容器） | 快照库 + 该快照指向的镜像 | 0 | 部署记录 | `/health` 200；两套保留快照的镜像都在主机上 |
| 组合负载 | `scripts/mixed-load.mjs` 60 秒、并发 8 | 见 20 号票 | [20 号票](../issues/20-catalog-sync-and-admission.md) | 通过；数值只在 20 与 21 号票 |
| 真实完整索引 | 生产索引运行 | partial | 来源目录索引进度接口 | 见第 6 节；按量级记录 |
| LIVE 翻译 | 生产真实模型 | 200 | 部署验收记录 | 见第 6 节 |
| LIVE 媒体 Range | 生产真实音频 | 206 | 部署验收记录 | 见第 6 节 |
| LIVE 浏览器 | 应用内浏览器 + 管理员会话 | — | [25-live-browser-ef812140.md](25-live-browser-ef812140.md) | 语言、短查询、多来源、播放连续性、输入边界均实测 |
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
| AC-02 | 01 | 21 错误分类 | 空/超长/不支持语言 400 分类，生产实测；成功试译 200；截断/鉴权/限流/超时/格式错由 `TestTranslationTrialFailuresAreClassifiedWithoutLeakingOrRetrying` 覆盖 14 种分类且不重试、不回显上游细节 | 通过（生产 LIVE + 本地分类） |
| AC-03 | 02 | 21 显示语言搜索 | 两用户偏好 H/R；严格语言搜索 H | 通过（本地） |
| AC-04 | 02 | — | 保护名称不外发 H/F | 通过（本地） |
| AC-05 | 02 | — | 延迟、失败、取消 H/F；未完成提示与 20 秒上限有浏览器用例 | 通过（本地） |
| AC-06 | 02 | 21 缓存键与保留 | 缓存键、保留与预算 H | 通过（本地） |
| AC-07 | 03 | — | 简繁复用与 LIVE 转换 | 通过（生产 LIVE） |
| AC-08 | 07 / 08 | 20 D-16、21 译文可搜 | 42k 浏览达标（数值见 20 号票）；无码/无编号卡片与短查询 `truncated` 生产实测；分支被扫描上界截断时也报 `truncated`（`TestContentTranslationSearchRemoteShortBranchSaturationIsReported`） | 通过 |
| AC-09 | 06 / 07 | — | 旧引用与无码身份 H/R | 通过（本地） |
| AC-10 | 05 | — | 目录检查与多源 H/F | 通过（本地） |
| AC-11 | 05 | — | 播放隔离与进度 H/F；生产 Range 206 | 通过 |
| AC-12 | 04 / 07 / 12 | — | 已登记与未登记评分/备注、清评分保备注、零下载增量，生产实测 | 通过 |
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
| AC-25 | 02 / 08 / 17 | 20 低空间组合 | `TestLowDiskPausesScanAndPrefillWithoutStoppingPlayback`：播放中触发低空间，扫描与 prefill 同时 paused，媒体 Range 仍 206、目录与个人备注仍可读；恢复空间后两者自动续跑 | 通过（本地 H；生产组合未构造） |
| AC-26 | 17 | 23 文档/证据 | 本账本与部署验收；进度证据仅本地 | 本地证据；发布级进度证据未取得 |
| AC-27 | 01 | — | 出站边界 H（非法 URL、越界重定向、超大响应、凭据剥离、取消） | 通过（本地） |
| AC-28 | 17 | — | 截图关闭与既有视觉测试不改写 | 本地 F 证据 |

## 6. 部署与回滚

| 项 | 值 |
| --- | --- |
| 部署主机 | 既有生产主机（受限记录） |
| 部署时间（Asia/Shanghai） | 2026-10-27 19:31（部署脚本 UTC 11:31:04Z） |
| 部署前备份路径（受限记录） | `pre-0aaa83fd-20261027T113104Z/`，含 DB/WAL/SHM、`.env`、compose、SHA256SUMS 与 `rollback.sh` |
| 部署前 schema / 镜像 | schema 105 / `kikoto:next-ef812140` |
| 回滚镜像 tag | `kikoto:next-ef812140` |
| 回滚 schema 兼容矩阵 | 见 [release-rollback.md](../../../docs/development/release-rollback.md) |
| 健康门禁 | `GET /health` 200 `{"status":"ok","version":"v0.8.3"}`；compose healthcheck 生效，容器 `healthy` |
| 部署脚本记录 | `schema_before=105`、`schema_after=105`、`git_sha=0aaa83fd`、`quick_check=ok`、`foreign_key_violations=0`、`status=running` |
| 快照校验 | `sha256sum -c` 3/3 OK（DB、`.env`、compose） |
| 部署后数据库 | `schema_state=105`、`migration_max=105`、`quick_check=ok`、FK 0 |
| 是否触发回滚 | 否 |
| 部署代码等价说明 | 部署镜像代码为 `0aaa83fd`；其后仅 docs 提交时注明“代码等价于 `0aaa83fd`”，不得写成部署了更晚代码 |

部署后逐项验收：

- `/health` 200，版本 `v0.8.3`；容器 `kikoto` 运行 `kikoto:next-0aaa83fd`，`docker inspect` 的健康状态为 `healthy`。
- 试译：HTTP 200，`今天天气很好。`，`model=Index-Translate-35B-A3B`，本候选部署后复测为 200 并返回目标文本。
- 试译错误分类：空白 400 `translation_empty_text`；不支持语言 400 `translation_unsupported_language`；超长 400 `translation_text_too_long`。
- 媒体 Range：HTTP 206，`Content-Range: bytes 0-1023/26015852`，读取 1024 字节；匿名请求同一地址 401。
- 短查询：浏览器显示 `第 1 / 42 页，共 1000 项作品` 与截断说明；API `total=1000`、`truncated=true`、24 条。
- 输入边界：8192 字节被接受；再加 1 个字符被拒绝，值保持 8192 字节，并显示内联提示。
- 浏览器语言：界面切为 English 后导航与设置均为英文，内容仍为简体中文译文；内容切为日本語后界面仍为英文、标题变为日文；随后恢复简体中文。
- 多来源：同一作品在 Local 与 asmr.one 之间切换，目录从 1 个音轨变为 4 个音轨再切回；个人备注跨来源保持不变，页面明确写明同一作品的来源共用记录。
- 播放连续性：SPA 导航到设置与翻译面板期间 `audio.currentTime` 从 15.3 s 推进到 99.9 s，`paused=false`。
- 来源卡：约 4.1 万张可播放卡片（量级，精确计数在受限运维记录）。
- 索引运行 53：部署时为 `partial`（4 个上游名损坏目录失败、27 个未确认空目录、0 个待处理、0 个受限）；部署后一次增量同步被重启中断，留下 3 个新目录待处理。随后从来源页执行“继续索引”，运行 53 重新进入 `running`，把失败、空目录与新目录一起放回待处理并可恢复。`partial` 不改成 `complete`，因为空目录未确认、损坏名未修复。
- 回滚：`rollback.sh` 可执行，快照 `sha256sum -c` 3/3 通过。本轮新增 `pre-0aaa83fd` → `next-ef812140`；上一轮 `pre-ef812140` → `next-c9449653`、更深 `pre-c9449653` → `next-fd37dc4c`、`pre-88498e94` → `next-f727feee` 均已实测 `/health` 200。

浏览器逐项证据见 [25-live-browser-ef812140.md](25-live-browser-ef812140.md)；0aaa83fd 部署后的短查询与 healthcheck 复验记在该文件末尾。

## 7. 未覆盖项

- AC-25 的“播放中同时低空间”组合只在本地 H 测试构造；生产浏览器已证明 SPA 导航、翻译面板加载与语言切换期间播放不中断，但没有制造磁盘压力。AC-27 出站边界与 AC-28 截图关闭仍是本地 H/F 证据。AC-26 的进度证据仍是本地口径。
- `/health` 是唯一健康门禁；生产 Compose 没有 healthcheck，`docker compose ps` 只要求 `running`。
- 20 秒显示上限短于 60 秒请求上限；点重试会中止当前请求，属显示层取舍。该上限只在字段进入视口后计时，因此隐藏标签页里不会自然触发；证据是带假时钟的浏览器用例，不是生产观察。
- 真实完整索引未达 complete：少量上游目录名损坏、数十个空目录未被确认为空，均如实计数（逐项数字在受限运维记录）。
- 全部卡片未逐页遍历；抽样覆盖首页、无码、系列、unavailable 与 unconfirmed 卡片。
- 前端渲染已对来源页、媒体库、作品详情、翻译设置和语言菜单做生产浏览器检查；未逐页遍历全部卡片。

## 7.1 运维说明

- 回滚：`bash <部署回滚目录>/rollback.sh`。该脚本恢复切换前的数据库、`.env` 与 compose，并重新启动上一镜像；快照已含 `SHA256SUMS` 校验。
- 上一候选镜像仍保留在本机，回滚不需要重新构建。
- 部署脚本在容器停止后才复制 DB/WAL/SHM，因此快照与运行态一致；健康门禁失败会自动回滚。
- 下一次每日索引与周日全量索引按既有调度执行，未因本次部署改动时间。

## 8. 证据脱敏检查

- [x] 不含真实主机路径、数据库路径、作品编号、生产 entryId/sourceId、密钥或原始日志。
- [x] 镜像 digest 只出现在受限运维记录，仓库只保留 tag。
- [x] `node scripts/check-tracked-evidence.mjs` 通过。
