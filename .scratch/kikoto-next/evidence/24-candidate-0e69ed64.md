# 24 候选账本：0e69ed64

> 本文件记录部署候选 `0e69ed64` 的取得证据。仓库只写镜像 tag；digest、主机路径与备份位置留在受限运维记录。

## 1. 候选标识

| 项 | 值 |
| --- | --- |
| 候选 SHA | `0e69ed64`（完整值见 Git 分支头） |
| 分支 | `codex/kikoto-catalog-remediation` |
| 冻结时间（Asia/Shanghai） | 2026-10-27T10:28:00+08:00 |
| 集成负责人 | 主会话（Codex） |
| 镜像 tag | `kikoto:next-0e69ed64` |
| 镜像 digest（受限记录，不写仓库） | 已记入部署目录 `image.txt` |
| schema 版本 | `105` |
| 迁移链 | `095 → 096 → 097 → 098 → 099 → 100 → 101 → 102 → 103 → 104 → 105` |
| 配置指纹（model / base URL / template / reasoning） | 生产沿用既有配置；`config_key` 由 model、base URL、模板版本与推理开关共同决定，本候选未改动 |
| 工作区状态 | `git status --porcelain` 为空；代码提交与门禁见下 |

## 2. 提交清单

候选相对上一部署 `88498e94` 共 12 个提交；相对已记录候选 `f727feee` 共 16 个。

| 工单 | 提交 | 说明 |
| --- | --- | --- |
| 24 | `7b5d8f9b` | 记录 f727feee 候选账本 |
| 24 | `0692e8ed` | 说明浏览 total 的两卡差异 |
| 24 | `cb2ef7a5` | 补记回滚与磁盘 |
| 20 | `7b79a751` | 来源 verdict 在被浏览范围内可见 |
| 02 | `e670ef89` | 可见字段的待处理提示加上限 |
| 20 | `88498e94` | 仍有可用位置时不再声称没有来源 |
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

## 3. 门禁结果

| 门禁 | 命令 | 退出码 | 证据 | 结论 |
| --- | --- | --- | --- | --- |
| 双轴审查 Standards | `git diff f727feee...HEAD` | — | 本轮审查记录 | 硬违规已修，判断项已记录 |
| 双轴审查 Spec | `git diff f727feee...HEAD` | — | 本轮审查记录 | blocker/major 已修 |
| 后端构建 | `cd backend && go build ./...` | 0 | 本地执行 | 通过 |
| 后端静态检查 | `cd backend && go vet ./...` | 0 | 本地执行 | 通过 |
| 前端类型检查 | `npx tsc -b` | 0 | 本地执行 | 通过 |
| 前端 E2E 类型检查 | `npm run typecheck:e2e` | 0 | 本地执行 | 通过 |
| 前端 lint | `npx oxlint src --deny-warnings` | 0 | 本地执行 | 718 文件 0 warning |
| 前端格式 | `npm run format:check` | 0 | 本地执行 | 通过 |
| 前端单元测试 | `npm run test:unit` | 0 | 本地执行 | 164 文件 / 937 测试 |
| 文档链接 | `node scripts/check-doc-links.mjs` | 0 | 本地执行 | 134 份 Markdown |
| 文档 locale | `node scripts/check-doc-locales.mjs` | 0 | 本地执行 | 5 locale / 11 页 |
| tracked-evidence | `node scripts/check-tracked-evidence.mjs` | 0 | 本地执行 | 52 个文件 |
| 翻译浏览器用例 | `node scripts/run-e2e-fixtures.mjs --group=content-translation` | 0 | `.kikoto-work/e2e-logs/final-*` | 7 + 2 通过 |
| 试译设置浏览器用例 | `--group=translation-settings` | 0 | 同上 | 3 通过 |
| 播放失败浏览器用例 | Playwright `player.spec.ts` 定向 | 0 | 同上 | 3 通过 |
| Full CI | `workflow_dispatch` run `40438776806` | TODO | GitHub Actions | TODO |
| 迁移：新装 | `TestMigrationChain105FreshInstall` | 0 | 上一候选记录 | 本候选未改迁移 |
| 迁移：068 升级 | `TestMigrationChain105Upgrades068AndKeepsUserData` | 0 | 同上 | 本候选未改迁移 |
| 迁移：084 升级 | `TestMigrationChain105Upgrades084ToCurrentShape` | 0 | 同上 | 本候选未改迁移 |
| 迁移：重复启动 | `TestMigrationChain105RestartIsIdempotent` | 0 | 同上 | 本候选未改迁移 |
| `PRAGMA foreign_key_check` | 部署后脚本校验 | TODO | 部署记录 | TODO |
| 备份与恢复演练 | 部署脚本快照 + 校验 | TODO | 部署记录 | TODO |
| 回滚演练（隔离目录） | 105 库 + 旧镜像 | 预期失败 | 上一候选记录 | schema newer than binary，符合预期 |
| 组合负载 | `scripts/mixed-load.mjs` | TODO | 部署证据目录 | TODO |
| 真实完整索引 | 生产索引运行 | partial | 来源目录索引进度接口 | 约 1.44 万目录完成，个位数失败，数十个未确认空目录 |
| LIVE 翻译 | 生产真实模型 | TODO | 部署验收记录 | TODO |
| LIVE 媒体 Range | 生产真实音频 | TODO | 部署验收记录 | TODO |
| 非视觉 F | 前端单元与 E2E | 0 | 同上 | 通过 |
| 部署后验收 | 见第 6 节 | TODO | 部署验收记录 | TODO |

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
| AC-02 | 01 | 21 错误分类 | 空/超长/不支持语言 400 分类；试译超限提示与清除有浏览器用例 | 通过 |
| AC-03 | 02 | 21 显示语言搜索 | 两用户偏好 H/R；严格语言搜索 H | 通过（本地） |
| AC-04 | 02 | — | 保护名称不外发 H/F | 通过（本地） |
| AC-05 | 02 | — | 延迟、失败、取消 H/F；未完成提示有浏览器用例 | 通过（本地） |
| AC-06 | 02 | 21 缓存键与保留 | 缓存键、保留与预算 H | 通过（本地） |
| AC-07 | 03 | — | 简繁复用与 LIVE 转换 | 通过（生产 LIVE） |
| AC-08 | 07 / 08 | 20 D-16、21 译文可搜 | 42k 浏览达标；verdict 文字标记与配色有单测与生产浏览器核对 | 通过 |
| AC-09 | 06 / 07 | — | 旧引用与无码身份 H/R | 通过（本地） |
| AC-10 | 05 | — | 目录检查与多源 H/F | 通过（本地） |
| AC-11 | 05 | — | 播放隔离与进度 H/F；替代位置文案有浏览器用例 | 通过（本地） |
| AC-12 | 04 / 07 / 12 | — | 已登记与未登记评分/备注、清评分保备注、零下载增量，生产实测 | 通过 |
| AC-13 | 04 | — | 两用户隔离，生产实测 | 通过 |
| AC-14 | 11 | — | 系列证据、格式副本与成员上限 H/F | 通过（本地） |
| AC-15 | 12 | — | 系列与单集独立评价 H/F | 通过（本地） |
| AC-16 | 13 | — | 多方确认与冲突 H/F | 通过（本地） |
| AC-17 | 14 | — | 高置信唯一候选 H/F | 通过（本地） |
| AC-18 | 13 | — | 过期预览与重启 H/R | 通过（本地） |
| AC-19 | 15 | — | 拆分保留 H/R/F | 通过（本地） |
| AC-20 | 10 | — | 重定位证据 H | 通过（本地） |
| AC-21 | 08 | 20 partial 重试 | 生产索引运行 partial，失败与未确认空目录如实计数 | 通过（partial 符合规格） |
| AC-22 | 09 | 20 固定调度 | 每日与周日调度时间可见，无重叠任务 | 通过 |
| AC-23 | 10 | 23 清理保留 | 清理保护回归 H | 通过（本地） |
| AC-24 | 16 | 22 父级归属 | `TestTransferV2RestoresPersonalOnlyParentAndRoundTrips`、`TestTransferV2ParentCycleIsReported` | 通过（本地） |
| AC-25 | 02 / 08 / 17 | — | 播放中音轨/队列/进度 H/F | 通过（本地） |
| AC-26 | 17 | 23 文档/证据 | 本账本与部署验收 | 见第 3、6 节 |
| AC-27 | 01 | — | 出站边界 H | 通过（本地） |
| AC-28 | 17 | — | 截图关闭与既有视觉测试不改写 | 通过（本地） |

## 6. 部署与回滚

| 项 | 值 |
| --- | --- |
| 部署主机 | 既有生产主机（受限记录） |
| 部署时间（Asia/Shanghai） | TODO |
| 部署前备份路径（受限记录） | `rollbacks/pre-0e69ed64-<stamp>/`，含 DB、WAL/SHM、`.env`、compose 与 SHA256SUMS |
| 部署前 schema / 镜像 | schema 105 / `kikoto:next-88498e94` |
| 回滚镜像 tag | `kikoto:next-88498e94` |
| 回滚 schema 兼容矩阵 | 见 [release-rollback.md](../../../docs/development/release-rollback.md) |
| 健康门禁 | `GET /health` 200 `{"status":"ok","version":"v0.8.3"}` |
| 部署后验证结果 | TODO |
| 是否触发回滚 | TODO |

## 7. 未覆盖项

- TODO：逐条列出未执行或环境受限的门禁、原因、影响与后续计划。

## 8. 证据脱敏检查

- [ ] 不含真实主机路径、数据库路径、作品编号、生产 entryId/sourceId、密钥或原始日志。
- [ ] 镜像 digest 只出现在受限运维记录，仓库只保留 tag。
- [ ] `make tracked-evidence-check` 通过。
