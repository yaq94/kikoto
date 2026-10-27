# 24 候选账本：c9449653

> 本文件记录部署候选 `c9449653` 的取得证据。仓库只写镜像 tag；digest、主机路径与备份位置留在受限运维记录。

## 1. 候选标识

| 项 | 值 |
| --- | --- |
| 候选 SHA | `c9449653`（完整值见 Git 分支头 `codex/kikoto-catalog-remediation`） |
| 分支 | `codex/kikoto-catalog-remediation` |
| 冻结时间（Asia/Shanghai） | 2026-10-27T14:20:00+08:00（候选冻结，部署前后均未再改代码） |
| 集成负责人 | 主会话（Codex） |
| 镜像 tag | `kikoto:next-c9449653` |
| 镜像 digest（受限记录，不写仓库） | 只留在受限运维记录；仓库只写 tag |
| schema 版本 | `105` |
| 迁移链 | `095 → 096 → 097 → 098 → 099 → 100 → 101 → 102 → 103 → 104 → 105`（本候选未新增迁移） |
| 配置指纹（model / base URL / template / reasoning） | 生产沿用既有配置；`config_key` 由 model、base URL、模板版本与推理开关共同决定，本候选未改动 |
| 工作区状态 | 候选时 `git status --porcelain` 为空；已推送 origin |

## 2. 提交清单

候选相对上一部署 `88498e94` 共 22 个提交；相对上一候选 `f727feee` 共 28 个提交。下表按 `git log --oneline 88498e94..c9449653` 逐条对齐。

| 工单 | 提交 | 说明 |
| --- | --- | --- |
| 02 | `333390b3` | 试译拒绝的按键有可见反馈 |
| 20 | `f8bfca97` | 播放替代位置判定共用同一谓词 |
| 20 | `f5eb00cf` | verdict 标记不再用健康绿点 |
| 02 | `9abcc6fc` | 未完成的翻译不再称“排队” |
| 02 | `8075bbdf` | 修正批量体积上限的测试注释 |
| 24 | `0c822ef3` | 格式化播放用例并改正测试名 |
| 02 | `466bac53` | 试译提示描述被拒绝的这次修改并会清除 |
| 20 | `8a7af93a` | 缓存副本按位置而非来源描述 |
| 23 | `2f329657` | 用户页与开发页说明 verdict 文字标记的适用范围 |
| 23 | `46d30785` | 测量值只保留一处，账本改为引用 |
| 02 | `0e69ed64` | 移除待处理注释里过时的“排队”措辞 |
| 24 | `510cf391` | 打开本候选账本 |
| 02 | `4a85d7ee` | 试译等待 prefill 让出槽位，不再恒报忙 |
| 02 | `ac6632a2` | 状态判定只读一处；行内操作达到触控目标；缓存读取不外泄拒绝 |
| 23 | `a6031fd3` | 注释回到所描述的符号上，基准不再重复构建同一语句 |
| 24 | `bfd8dfdf` | 账本移到当前候选 |
| 24 | `f9ece237` | 删除被取代的账本文件 |
| 02 | `989d13bb` | gofmt 修正 queue 缩进 |
| 20 | `1cb0176b` | voice 目录卡按统一的 verdict 规则判断可播放性 |
| 20 | `a93cdc66` | 标记用 `readable` 声明需阅读，同类内排到健康标记之前 |
| 21 | `fd37dc4c` | 短查询基准的 Go 名称与它实际测量的有界总数一致 |
| 02 | `ce642084` | 状态页与账本指向已部署候选，参考文档删去带日期的测量表 |
| 02 | `c9449653` | 翻译显示状态改用真实联合类型，不再字符串强转 |

## 3. 门禁结果

| 门禁 | 命令 | 退出码 | 证据 | 结论 |
| --- | --- | --- | --- | --- |
| 双轴审查 Standards | `git diff f727feee...0c822ef3` | — | `.kikoto-work/reports/final-standards-review.md` | 硬违规已修；报告覆盖到 `0c822ef3`，其后 12 个提交（含试译、播放与 queue 修复）未另出双轴审查报告 |
| 双轴审查 Spec | `git diff 2ad508e8...f727feee` | — | `.kikoto-work/reports/review-spec-delta.md` | 上一候选的 Spec 轴报告；本候选未重开该轴 |
| 后端构建 | `cd backend && go build ./...` | 0 | 本地执行 | 通过 |
| 后端静态检查 | `cd backend && go vet ./...` | 0 | 本地执行 | 通过 |
| 后端全量测试 | `cd backend && go test ./...` | 0 | 本地执行 | 全部包通过；`internal/httpapi` 656s |
| 后端竞态测试 | `cd backend && go test ./internal/translation -race` | 0 | 本地执行 | 通过 |
| 前端类型检查 | `npx tsc -b` | 0 | 本地执行 | 通过 |
| 前端 E2E 类型检查 | `npm run typecheck:e2e` | 0 | 本地执行 | 通过 |
| 前端 lint | `npx oxlint src --deny-warnings` | 0 | 本地执行 | 718 文件 0 warning |
| 前端格式 | `npm run format:check` | 0 | 本地执行 | 通过 |
| 前端单元测试 | `npm run test:unit` | 0 | 本地执行 | 164 文件 / 942 测试 |
| 文档链接 | `node scripts/check-doc-links.mjs` | 0 | 本地执行 | 134 份 Markdown |
| 文档 locale | `node scripts/check-doc-locales.mjs` | 0 | 本地执行 | 5 locale / 11 页 |
| tracked-evidence | `node scripts/check-tracked-evidence.mjs` | 0 | 本地执行 | 53 个文件 |
| 翻译浏览器用例 | `node scripts/run-e2e-fixtures.mjs --group=content-translation` | 0 | `.kikoto-work/e2e-logs/final-*` | 7 + 2 通过 |
| 试译设置浏览器用例 | `--group=translation-settings` | 0 | 同上 | 3 通过 |
| 播放失败浏览器用例 | Playwright `player.spec.ts` 定向 | 0 | 同上 | 3 通过 |
| Full CI | `workflow_dispatch` run `40463449636` | TODO | GitHub Actions | 记录状态为进行中，终态 TODO |
| 迁移：新装 | `TestMigrationChain105FreshInstall` | 0 | 上一候选 `f727feee` 记录（历史） | 本候选未改迁移，沿用上一候选证据 |
| 迁移：068 升级 | `TestMigrationChain105Upgrades068AndKeepsUserData` | 0 | 同上（历史） | 本候选未改迁移，沿用上一候选证据 |
| 迁移：084 升级 | `TestMigrationChain105Upgrades084ToCurrentShape` | 0 | 同上（历史） | 本候选未改迁移，沿用上一候选证据 |
| 迁移：重复启动 | `TestMigrationChain105RestartIsIdempotent` | 0 | 同上（历史） | 本候选未改迁移，沿用上一候选证据 |
| `PRAGMA foreign_key_check` | 部署后脚本校验 | 0 | 部署记录 | 0 行 |
| 备份与恢复演练 | 部署脚本快照 + `sha256sum -c` | — | 部署记录 | 快照含 DB/WAL/SHM、`.env`、compose、SHA256SUMS 与 `rollback.sh`，校验全部 OK |
| 回滚演练（隔离目录） | 105 库 + 旧镜像 | 预期失败 | 部署记录 | schema newer than binary，符合回滚矩阵 |
| 回滚脚本 | `rollback.sh` 可执行 + 快照校验 | 0 | 部署记录 | 可执行，`sha256sum -c` 全部 OK |
| 组合负载 | `scripts/mixed-load.mjs` 60 秒、并发 8 | 见 20 号票 | [20 号票](../issues/20-catalog-sync-and-admission.md) | 通过；数值与逐项结果只在 20 号票与 [translation.md](../../../docs/development/translation.md) 记录 |
| 真实完整索引 | 生产索引运行 | partial | 来源目录索引进度接口 | 见第 6 节 |
| LIVE 翻译 | 生产真实模型 | 200 | 部署验收记录 | 见第 6 节 |
| LIVE 媒体 Range | 生产真实音频 | 206 | 部署验收记录 | 见第 6 节 |
| 非视觉 F | 前端单元与 E2E | 0 | 同上 | 通过 |
| 部署后验收 | 见第 6 节 | — | 部署验收记录 | 见第 6 节 |

## 4. 性能门禁

| 指标 | 环境 | 样本 | 门槛 | 结论 | 数值出处 |
| --- | --- | --- | --- | --- | --- |
| 42k 卡浏览 | 2 vCPU / 3.5 GiB, warm | 30 | ≤300 / ≤800 ms | 通过 | [20 号票](../issues/20-catalog-sync-and-admission.md) |
| 5,001 文件卡拒绝 | 同上 | 30 | ≤300 / ≤800 ms | 通过 | 同上 |
| 译文 3 字以上 | modernc SQLite, 456k 行 | 20 | ≤100 ms | 通过 | [translation.md](../../../docs/development/translation.md) |
| 译文 1–2 字 | 114,000 实体，全部标题命中 | 20 | ≤100 ms | 通过 | 同上 |
| 编号等值 | 同上 | 20 | <5 ms | 通过 | 同上 |
| 目标语言标题整串等值 | 同上 | 20 | <5 ms | 通过 | 同上 |

数值只在上述来源写一次；本账本记录候选上是否达标，不重复抄写测量值。

## 5. AC 覆盖

本表只记录本候选实际取得的证据；未在候选上重跑的原始主责测试沿用其记录 SHA 的历史结论，不冒充当前候选。

| AC | 原始主责 | 整改子项 | 当前证据 | 结论 |
| --- | --- | --- | --- | --- |
| AC-01 | 01 | — | 配置读写与掩码 H 测试；生产试译使用已保存配置 | 通过（本地 H + 生产 LIVE） |
| AC-02 | 01 | 21 错误分类 | 空/超长/不支持语言 400 分类，生产实测；成功试译 200 | 通过 |
| AC-03 | 02 | 21 显示语言搜索 | 两用户偏好 H/R；严格语言搜索 H | 通过（本地） |
| AC-04 | 02 | — | 保护名称不外发 H/F | 通过（本地） |
| AC-05 | 02 | — | 延迟、失败、取消 H/F；未完成提示与 20 秒上限有浏览器用例 | 通过（本地） |
| AC-06 | 02 | 21 缓存键与保留 | 缓存键、保留与预算 H | 通过（本地） |
| AC-07 | 03 | — | 简繁复用与 LIVE 转换 | 通过（生产 LIVE） |
| AC-08 | 07 / 08 | 20 D-16、21 译文可搜 | 42k 浏览达标（数值见 20 号票）；无码/无编号卡片与短查询 `truncated` 生产实测；来源卡 total 见第 6 节 | 通过 |
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
| AC-25 | 02 / 08 / 17 | — | 播放中音轨/队列/进度 H/F | 本地证据；未在本候选取得组合 LIVE |
| AC-26 | 17 | 23 文档/证据 | 本账本与部署验收；进度证据仅本地 | 本地证据；发布级进度证据未取得 |
| AC-27 | 01 | — | 出站边界 H | 本地证据；未在本候选重跑 |
| AC-28 | 17 | — | 截图关闭与既有视觉测试不改写 | 本地 F 证据 |

## 6. 部署与回滚

| 项 | 值 |
| --- | --- |
| 部署主机 | 既有生产主机（受限记录） |
| 部署时间（Asia/Shanghai） | 2026-10-27 14:50（部署脚本 UTC 06:50） |
| 部署前备份路径（受限记录） | `rollbacks/pre-c9449653-<stamp>/`，含 DB、WAL/SHM、`.env`、compose、SHA256SUMS 与 `rollback.sh` |
| 部署前 schema / 镜像 | schema 105 / 上一候选（已由 `c9449653` 取代） |
| 回滚镜像 tag | 上一候选（已由 `c9449653` 取代） |
| 回滚 schema 兼容矩阵 | 见 [release-rollback.md](../../../docs/development/release-rollback.md) |
| 健康门禁 | `GET /health` 200 `{"status":"ok","version":"v0.8.3"}`，2 秒就绪 |
| 部署脚本记录 | `schema_before=105`、`schema_after=105`、`health_seconds=2`、`git_sha=c9449653` |
| 快照校验 | `sha256sum -c` 全部 OK |
| 部署后数据库 | `schema_state=105`、`migration_max=105`、`quick_check=ok`、FK 0 |
| 部署后验证结果 | 见本节下方逐项验收 |
| 是否触发回滚 | 否 |

部署后逐项验收：

- `GET /health` 200 `{"status":"ok","version":"v0.8.3"}`。
- 试译：HTTP 200，返回 `"The weather is so nice today."`，`elapsedMs=616`，model `Index-Translate-35B-A3B`。
- 试译错误分类：空文本 400 `translation_empty_text`；不支持语言 400 `translation_unsupported_language`；超长 400 `translation_text_too_long`。
- 媒体 Range：206，`Content-Range: bytes 0-1023/19105977`。
- 短查询：`q=的&lang=zh-cn` 返回 `total=1000`、`truncated=true`，24 条。
- 来源卡：`total=41179`。
- 真实完整索引仍为 `partial`：`directories=14494`、`completed=14463`、`failed=4`、`restricted=0`、`empty=27`（未确认为空）、`pending=0`。4 个失败目录名本身含 U+FFFD 与异常码位，上游父目录 200、子目录名已损坏，属上游数据问题，不是 Kikoto 故障；27 个空目录未被确认为空，因此不标 complete。
- 回滚：`rollback.sh` 可执行，快照校验通过；105 库 + 旧镜像的回滚预期失败（schema newer than binary），符合 [release-rollback.md](../../../docs/development/release-rollback.md) 的矩阵。

## 7. 未覆盖项

- AC-25 播放中音轨/队列/进度、AC-26 进度证据、AC-27 出站边界、AC-28 截图关闭仍只按本地 H/F 证据记录，未在本候选上取得生产 LIVE 组合证据；`issues/24` 的对应两项保持未勾选。
- `/health` 是唯一健康门禁；生产 Compose 没有 healthcheck，`docker compose ps` 只要求 `running`。
- 20 秒显示上限短于 60 秒请求上限；点重试会中止当前请求，属显示层取舍。
- 真实完整索引未达 complete：4 个上游目录名损坏、27 个空目录未确认为空，均如实计数。
- 全部卡片未逐页遍历；抽样覆盖首页、无码、系列、unavailable 与 unconfirmed 卡片。
- 前端渲染以单元与既有 E2E 覆盖，未在生产浏览器逐页人工核对。

## 7.1 运维说明

- 回滚：`bash <部署回滚目录>/rollback.sh`。该脚本恢复切换前的数据库、`.env` 与 compose，并重新启动上一镜像；快照已含 `SHA256SUMS` 校验。
- 上一候选镜像仍保留在本机，回滚不需要重新构建。
- 部署脚本在容器停止后才复制 DB/WAL/SHM，因此快照与运行态一致；健康门禁失败会自动回滚。
- 下一次每日索引与周日全量索引按既有调度执行，未因本次部署改动时间。

## 8. 证据脱敏检查

- [x] 不含真实主机路径、数据库路径、作品编号、生产 entryId/sourceId、密钥或原始日志。
- [x] 镜像 digest 只出现在受限运维记录，仓库只保留 tag。
- [x] `node scripts/check-tracked-evidence.mjs` 通过。
