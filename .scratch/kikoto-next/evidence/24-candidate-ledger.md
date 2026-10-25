# 24 候选账本模板

> 复制本文件为 `24-candidate-<shortsha>.md` 后填写。仓库只写镜像 tag，digest 与主机路径写入受限运维记录。
> 未填写的槽位保持 `TODO`，不得删除行；空槽位视为门禁未通过。

## 1. 候选标识

| 项 | 值 |
| --- | --- |
| 候选 SHA | TODO |
| 分支 | `codex/kikoto-catalog-remediation` |
| 冻结时间（Asia/Shanghai） | TODO |
| 集成负责人 | TODO |
| 镜像 tag | `kikoto:next-<shortsha>` |
| 镜像 digest（受限记录，不写仓库） | TODO |
| schema 版本 | TODO（当前预期 100） |
| 迁移链 | TODO（095 → 096 → 097 → 098 → 099 → 100） |
| 配置指纹（model / base URL / template / reasoning） | TODO |
| 工作区状态 | `git status --porcelain` 为空；否则阻塞 |

## 2. 提交清单

| 工单 | 提交 | 说明 | 已合入候选 |
| --- | --- | --- | --- |
| 20 | TODO | 调度状态机、D-16 边界 | TODO |
| 21 | TODO | 099、严格语言搜索、失败表 | TODO |
| 22 | TODO | 迁移 100、`work.parent_work_id` | TODO |
| 23 | TODO | 迁移与文档规范 | TODO |
| 其他 | TODO | 并行修复 | TODO |

## 3. 门禁结果

每条必须记录命令、退出码、耗时、证据路径与环境限制。`skip` 不计通过。

| 门禁 | 命令 | 退出码 | 证据 | 结论 |
| --- | --- | --- | --- | --- |
| 双轴审查 Standards | TODO | TODO | TODO | TODO |
| 双轴审查 Spec | TODO | TODO | TODO | TODO |
| 后端构建 | `cd backend && go build ./...` | TODO | TODO | TODO |
| 后端测试 | `make ci-backend` | TODO | TODO | TODO |
| 前端检查 | `make ci-frontend` | TODO | TODO | TODO |
| 样式与文档 | `make ci-style` | TODO | TODO | TODO |
| 本地集成 | `make ci-local` | TODO | TODO | TODO |
| Full CI | `workflow_dispatch` run TODO | TODO | TODO | TODO |
| 迁移：新装 | TODO | TODO | TODO | TODO |
| 迁移：既有库升级 | TODO | TODO | TODO | TODO |
| 迁移：旧 095 变体 | TODO | TODO | TODO | TODO |
| `PRAGMA foreign_key_check` | TODO | TODO | TODO | TODO |
| 备份与恢复演练 | TODO | TODO | TODO | TODO |
| 回滚演练（隔离目录） | TODO | TODO | TODO | TODO |
| 组合负载 | TODO | TODO | TODO | TODO |
| 真实完整索引 | TODO | TODO | TODO | TODO |
| LIVE 翻译 | TODO | TODO | TODO | TODO |
| LIVE 媒体 Range | TODO | TODO | TODO | TODO |
| 非视觉 F | TODO | TODO | TODO | TODO |
| 部署后验收 | TODO | TODO | TODO | TODO |

## 4. 性能门禁

| 指标 | 环境 | 样本 | p95 | p99 | 门槛 | 结论 |
| --- | --- | --- | --- | --- | --- | --- |
| 42k 卡浏览 | 2 vCPU / 2 GiB, warm | TODO | TODO | TODO | ≤300 / ≤800 ms | TODO |
| 5,001 文件卡拒绝 | 同上 | TODO | TODO | TODO | ≤300 / ≤800 ms | TODO |
| 译文 3 字以上 | modernc SQLite, 456k 行 | TODO | TODO | — | ≤100 ms | TODO |
| 译文 1–2 字 | 同上 | TODO | TODO | — | ≤100 ms | TODO |
| 编号等值 | 同上 | TODO | TODO | — | <5 ms | TODO |
| 目标语言标题整串等值 | 同上 | TODO | TODO | — | <5 ms | TODO |

## 5. AC 覆盖

| AC | 原始主责 | 整改子项 | 当前证据 | 结论 |
| --- | --- | --- | --- | --- |
| AC-01 | 01 | — | TODO | TODO |
| AC-02 | 01 | — | TODO | TODO |
| AC-03 | 02 | 21 显示语言搜索 | TODO | TODO |
| AC-04 | 02 | — | TODO | TODO |
| AC-05 | 02 | — | TODO | TODO |
| AC-06 | 02 | 21 缓存键与保留 | TODO | TODO |
| AC-07 | 03 | — | TODO | TODO |
| AC-08 | 07 / 08 | 20 D-16、21 译文可搜 | TODO | TODO |
| AC-09 | 06 / 07 | — | TODO | TODO |
| AC-10 | 05 | — | TODO | TODO |
| AC-11 | 05 | — | TODO | TODO |
| AC-12 | 04 / 07 / 12 | — | TODO | TODO |
| AC-13 | 04 | — | TODO | TODO |
| AC-14 | 11 | — | TODO | TODO |
| AC-15 | 12 | — | TODO | TODO |
| AC-16 | 13 | — | TODO | TODO |
| AC-17 | 14 | — | TODO | TODO |
| AC-18 | 13 | — | TODO | TODO |
| AC-19 | 15 | — | TODO | TODO |
| AC-20 | 10 | — | TODO | TODO |
| AC-21 | 08 | 20 partial 重试 | TODO | TODO |
| AC-22 | 09 | 20 固定调度 | TODO | TODO |
| AC-23 | 10 | 23 清理保留 | TODO | TODO |
| AC-24 | 16 | 22 父级归属 | TODO | TODO |
| AC-25 | 02 / 08 / 17 | — | TODO | TODO |
| AC-26 | 17 | 23 文档/证据 | TODO | TODO |
| AC-27 | 01 | — | TODO | TODO |
| AC-28 | 17 | — | TODO | TODO |

## 6. 部署与回滚

| 项 | 值 |
| --- | --- |
| 部署主机 | TODO |
| 部署时间 | TODO |
| 部署前备份路径（受限记录） | TODO |
| 部署前 schema / 镜像 | TODO |
| 回滚镜像 tag | TODO |
| 回滚 schema 兼容矩阵 | TODO |
| 健康门禁 | `GET /health` 200；生产 Compose 无 healthcheck |
| 部署后验证结果 | TODO |
| 是否触发回滚 | TODO |

## 7. 未覆盖项

- TODO：逐条列出未执行或环境受限的门禁、原因、影响与后续计划。

## 8. 证据脱敏检查

- [ ] 不含真实主机路径、数据库路径、作品编号、生产 entryId/sourceId、密钥或原始日志。
- [ ] 镜像 digest 只出现在受限运维记录，仓库只保留 tag。
- [ ] `make tracked-evidence-check` 通过。
