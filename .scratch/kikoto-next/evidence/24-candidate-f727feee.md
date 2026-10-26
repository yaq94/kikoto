# 24 候选账本：f727feee

> 本文件记录部署候选 `f727feee` 的取得证据。仓库只写镜像 tag；digest、主机路径与备份位置留在受限运维记录。

## 1. 候选标识

| 项 | 值 |
| --- | --- |
| 候选 SHA | `f727feee267c09ca3fe49526d3677b6b47f286ce` |
| 分支 | `codex/kikoto-catalog-remediation` |
| 冻结时间（Asia/Shanghai） | 2026-10-27T06:42:47+08:00 |
| 集成负责人 | 主会话（Codex） |
| 镜像 tag | `kikoto:next-f727feee` |
| 镜像 digest（受限记录，不写仓库） | 已记入部署目录 `image.txt` |
| schema 版本 | `105` |
| 迁移链 | `095 → 096 → 097 → 098 → 099 → 100 → 101 → 102 → 103 → 104 → 105` |
| 配置指纹（model / base URL / template / reasoning） | 生产沿用既有配置；`config_key` 由 model、base URL、模板版本与推理开关共同决定，未在本候选变更 |
| 工作区状态 | 候选冻结后仅剩本账本；代码提交与门禁见下 |

## 2. 提交清单

候选相对上一部署 `2ad508e8` 共 13 个提交。

| 工单 | 提交 | 说明 |
| --- | --- | --- |
| 21 | `91581a66` | 短查询 page 与 total 共用 1000 张预算 |
| 21 | `118efaf8` | 试译输入按 UTF-8 字节计量 |
| 21 | `dc8cd565` | 试译输入错误分类 |
| 02 | `38024767` | prefill 运营视图 15s 快照缓存 |
| 07 | `be6c0785` | 前端 truncated 提示 |
| 20 | `ce3fee22` | 卡片可播放性与来源 verdict 分离 |
| 20 | `462d6aa2` | `catalog/index` 返回 `finishedAt` |
| 23 | `47e8a121` | OpenList 文档同步 |
| 23 | `0cc80534` | 规格与工单对齐有界 total |
| 20 | `b54b49de` | AC-08 搜索范围口径与 verdict 规则 |
| 02 | `c8f53f18` | 受保护名称占位符提示 |
| 20 | `74c21a8d` | 系列与成员同一 verdict 规则 |
| 20 | `f727feee` | 来源 verdict 进入可见标签；移除不可达 `disabled` 判定；快照缓存去重 |

## 3. 门禁结果

| 门禁 | 命令 | 退出码 | 证据 | 结论 |
| --- | --- | --- | --- | --- |
| 双轴审查 Standards | `git diff 2ad508e8...HEAD` | — | `.kikoto-work/reports/review-standards-delta.md` | 发现项已修复或记录理由 |
| 双轴审查 Spec | `git diff 2ad508e8...HEAD` | — | `.kikoto-work/reports/review-spec-delta.md` | 见该报告 |
| 后端构建 | `cd backend && go build ./...` | 0 | 本地执行 | 通过 |
| 后端静态检查 | `cd backend && go vet ./...` | 0 | 本地执行 | 通过 |
| 后端全量测试 | `go test ./... -count=1 -timeout 5400s` | 0 | `.kikoto-work/logs/go-test-all-20261027e.log` | 通过；httpapi 549.054s |
| 前端检查 | `lint`、`format:check`、`typecheck:e2e` | 0 | `.kikoto-work/logs/frontend-full-20261027d.log` | 通过 |
| 前端单元测试 | `npm run test:unit` | 0 | 同上 | 164 文件 / 928 测试 |
| 前端构建 | `npm run build` | 0 | `.kikoto-work/logs/frontend-full-20261027c.log` | 通过 |
| 样式与文档 | `make ci-style`（actionlint 单独以 GOPROXY 重跑） | 0 | `.kikoto-work/logs/ci-style-20261027d.log` | 通过 |
| Full CI | `workflow_dispatch` run `40409566661` | success | GitHub Actions | 26 个 job 全部 success |
| 迁移：新装 | `TestMigrationChain105FreshInstall` | 0 | `.kikoto-work/logs/migration-105-20261027.log` | 通过（0.67s） |
| 迁移：068 升级 | `TestMigrationChain105Upgrades068AndKeepsUserData` | 0 | 同上 | 通过（0.90s） |
| 迁移：084 升级 | `TestMigrationChain105Upgrades084ToCurrentShape` | 0 | 同上 | 通过（0.96s） |
| 迁移：重复启动 | `TestMigrationChain105RestartIsIdempotent` | 0 | 同上 | 通过（0.74s） |
| `PRAGMA foreign_key_check` | 部署后脚本校验 | 0 | 部署目录 `deploy.log` | 0 行 |
| 备份与恢复演练 | 84 快照 + 旧镜像 | 0 | 历史演练记录 | 旧镜像 `/health` 200 |
| 回滚演练（隔离目录） | 105 库 + 旧镜像 | 预期失败 | 历史演练记录 | schema newer than binary，符合预期 |
| 组合负载 | `scripts/mixed-load.mjs` 60s / 并发 8 | 0 | 部署证据目录 `load-f727feee.json` | 16,162 请求全部匹配 |
| 真实完整索引 | 生产 run 53 | partial | `/api/remote-sources/3/catalog/index` | 14,463 complete，4 failed，27 unconfirmed empty |
| LIVE 翻译 | 生产真实模型 | 200 | 部署验收记录 | 原 3 条失败条目已翻译成功 |
| LIVE 媒体 Range | 生产真实音频 | 206 | 部署验收记录 | 无码与 unavailable 卡片均 1 KiB Range |
| 非视觉 F | 前端单元与 E2E | 0 | 同上 | 通过 |
| 部署后验收 | 见第 6 节 | — | 部署验收记录 | 见第 6 节 |

## 4. 性能门禁

| 指标 | 环境 | 样本 | p95 | p99 | 门槛 | 结论 |
| --- | --- | --- | --- | --- | --- | --- |
| 42k 卡浏览 | 2 vCPU / 3.5 GiB, warm | 30 | 199.192 ms | 226.981 ms | ≤300 / ≤800 ms | 通过 |
| 5,001 文件卡拒绝 | 同上 | 30 | 0.593 ms | 0.596 ms | ≤300 / ≤800 ms | 通过 |
| 译文 3 字以上 | modernc SQLite, 456k 行 | 20 | 1.367–1.725 ms（标题整串等值） | — | ≤100 ms | 通过 |
| 译文 1–2 字 | 114,000 实体，全部标题命中 | 20 | 卡片页 8.91–12.59 ms；同预算总数 8.9–12.6 ms | — | ≤100 ms | 通过 |
| 编号等值 | 同上 | 20 | 0.018 ms | — | <5 ms | 通过 |
| 目标语言标题整串等值 | 同上 | 20 | 1.367–1.725 ms | — | <5 ms | 通过 |

补充：无预算全量计数 p95 667–693 ms，仅作诊断；它正是结果预算与 `truncated` 的取舍依据。

## 5. AC 覆盖

本表只记录本候选实际取得的证据；未在候选上重跑的原始主责测试沿用其记录 SHA 的历史结论，不冒充当前候选。

| AC | 原始主责 | 整改子项 | 当前证据 | 结论 |
| --- | --- | --- | --- | --- |
| AC-01 | 01 | — | 配置读写与掩码 H 测试；生产试译使用已保存配置 | 通过（本地 H + 生产 LIVE） |
| AC-02 | 01 | 21 错误分类 | 空/超长/不支持语言 400 分类，生产实测；成功试译 200 | 通过 |
| AC-03 | 02 | 21 显示语言搜索 | 两用户偏好 H/R；严格语言搜索 H | 通过（本地） |
| AC-04 | 02 | — | 保护名称不外发 H/F | 通过（本地） |
| AC-05 | 02 | — | 延迟、失败、取消 H/F | 通过（本地） |
| AC-06 | 02 | 21 缓存键与保留 | 缓存键、保留与预算 H | 通过（本地） |
| AC-07 | 03 | — | 简繁复用与 LIVE 转换 | 通过（生产 LIVE） |
| AC-08 | 07 / 08 | 20 D-16、21 译文可搜 | 42k 浏览 p95 199 ms；无码/无编号卡片与短查询 truncated 生产实测 | 通过 |
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
| AC-21 | 08 | 20 partial 重试 | 生产 run 53 partial，失败与未确认空目录如实计数 | 通过（partial 符合规格） |
| AC-22 | 09 | 20 固定调度 | 每日与周日调度时间可见，无重叠任务 | 通过 |
| AC-23 | 10 | 23 清理保留 | 清理保护回归 H | 通过（本地） |
| AC-24 | 16 | 22 父级归属 | `TestTransferV2RestoresPersonalOnlyParentAndRoundTrips`、`TestTransferV2ParentCycleIsReported` | 通过（本地） |
| AC-25 | 02 / 08 / 17 | — | 播放中音轨/队列/进度 H/F | 通过（本地） |
| AC-26 | 17 | 23 文档/证据 | 本账本与部署验收 | 通过 |
| AC-27 | 01 | — | 出站边界 H | 通过（本地） |
| AC-28 | 17 | — | 截图关闭与既有视觉测试不改写 | 通过（本地） |

## 6. 部署与回滚

| 项 | 值 |
| --- | --- |
| 部署主机 | 既有生产主机（受限记录） |
| 部署时间（Asia/Shanghai） | 2026-10-27 06:30 |
| 部署前备份路径（受限记录） | `rollbacks/pre-f727feee-<stamp>/`，含 DB、WAL/SHM、`.env`、compose 与 SHA256SUMS |
| 部署前 schema / 镜像 | schema 105 / `kikoto:next-2ad508e8` |
| 回滚镜像 tag | `kikoto:next-2ad508e8` |
| 回滚 schema 兼容矩阵 | 见 [release-rollback.md](../../../docs/development/release-rollback.md) |
| 健康门禁 | `GET /health` 200 `{"status":"ok","version":"v0.8.3"}`，2 秒就绪 |
| 部署后数据库 | `schema_state=105`，`migration_max=105`，`quick_check=ok`，FK 0 |
| 是否触发回滚 | 否 |

部署后逐项验收：

- `GET /health` 200；`catalog/index` 返回 `finishedAt`。
- 试译：空文本 `translation_empty_text`、超长 `translation_text_too_long`、不支持语言 `translation_unsupported_language`，均 400。
- prefill 状态：冷读 3.94 s，缓存命中 0.5 ms；8 并发全部 4 ms 内，同期 `/api/works` 10 ms（修复前 13.7 s）。
- 组合负载 16,162 请求：browse/search p95 70 ms，media-range p95 10 ms，prefill p95 12.97 ms，无 5xx、无超时。
- 短查询：1 字命中 1000 时 `total=1000`、`truncated=true`；其余返回真实总数。
- `unavailable` / `unconfirmed_empty` 卡片 `remotePlayable=true` 且 `availability` 正确；无码卡片媒体 Range 206。
- 个人记录：评分/备注独立更新、清评分保备注、跨源共享、用户隔离，`source_catalog_file`/`media_file_location`/`workflow_run` 零增量。
- prefill 提示规则：原 3 条 `Protected names could not be preserved.` 条目已全部翻译成功，失败表清零。
- 索引 run 53 仍为 `partial`（14,463 complete = 14,494 − 4 failed − 27 unconfirmed empty），未称 complete。

## 7. 未覆盖项

- 真实完整索引尚未 complete：run 53 为 partial，4 条失败与 27 个未确认空目录已如实计数。
- 41,179 张卡片未逐页遍历；抽样覆盖首页、无码、系列、unavailable 与 unconfirmed 卡片。
- 前端渲染以单元与既有 E2E 覆盖，未在生产浏览器逐页人工核对。

## 8. 证据脱敏检查

- [x] 不含真实主机地址、数据库路径、密钥或原始日志。
- [x] 不含生产 entryId/sourceId 与作品编号；卡片行为以字段名与状态描述。
- [x] 镜像 digest 只留在受限运维记录，仓库只保留 tag。
- [x] `make tracked-evidence-check` 通过。
