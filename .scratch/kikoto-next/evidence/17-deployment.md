# 17 组合验收、部署与回滚证据

本文件记录当前部署的产物、迁移、回滚路径与组合验收入口。分项功能证据见
[21 最终验收记录](21-final-acceptance.md)；历史部署只保留在 Git 历史与
`docs/history/`，本文件不重复旧构建或旧模型配置。

## 当前产物

- 集成 HEAD：`2bae5db98acceff6b45fdb4d168908c7b0da9177`。
- 固定审查基线：`9949175e53979bcd590c75a624bc0efc67684da3`。
- 部署镜像：`kikoto:next-2bae5db9`，镜像 ID
  `sha256:25e5467ede40342fad8ca772b95d931343a87e3c5239bd535bbc0330777b1a99`。
- Full CI：run `39496305624`，27 个 job 全部 `success`（含 Gate）。
- 双轴审查：Standards 与 Spec 在当前差异上执行，结论见
  [22 最终审查记录](22-final-review.md)。

## 部署与迁移

- 容器 `kikoto` 处于 `running`，host 网络监听本机端口；`GET /health` 返回 200
  与 `status=ok`。
- 数据库 schema `083`；`schema_state.current_version=83`、
  `baseline_version=66`、`dirty_version=NULL`；`schema_migration` 已记录 83。
- `PRAGMA quick_check=ok`；`PRAGMA foreign_key_check` 返回 0 行。
- 部署前快照包含数据库、WAL/SHM、环境文件、Compose 文件、容器状态与回滚脚本。
- 应用在首次编号迁移前写入 `v082-to-v083` 数据库备份；该备份与部署前快照
  分开保留。

## 回滚路径

兼容矩阵与命令模板见
[发布回退](../../docs/development/release-rollback.md)。当前部署的关键前提：

- 回滚镜像 `kikoto:next-aa17d43c` 保留，包含 schema `083` 的打包迁移目录。
- 部署前快照 `pre-2bae5db9-20261020T211725Z` 中的数据库为 schema `083`，
  WAL/SHM 与环境文件与快照同一时刻；`quick_check=ok`、外键 0 行。
- 上一版部署的 schema `082` 快照 `pre-aa17d43c-20261020T200351Z` 与镜像
  `kikoto:next-2d78344a` 仍保留，可用于回退到 schema 82。
- 回滚脚本执行：停止容器、替换数据库与 WAL/SHM、恢复环境中的镜像引用、
  启动旧镜像、轮询 `/health`。脚本已通过 `bash -n` 语法检查；快照文件与镜像
  均存在。未在生产上执行破坏性回滚切换。
- `schema_migration` 与 `schema_state` 不手工修改；schema 83 数据库只能由
  打包 083 的二进制读取。

## 备份恢复演练的适用范围

快照迁移与还原已在早期构建上做过一次两阶段演练：停服取快照、启动新镜像并迁移，
健康检查通过；再还原快照、启动旧镜像回到旧 schema，健康检查同样通过。那次演练
证明的是流程本身，不能当作当前构建的证据。

当前构建上已完成的部分：部署前快照与迁移备份存在、快照数据库 `quick_check=ok`、
回滚镜像存在、回滚脚本通过 `bash -n`。未在当前构建上重复执行还原切换。

## 组合验收入口

- 翻译、来源目录、多来源个人记录、媒体 Range 与 AC-25 组合负载的当前证据见
  [21 最终验收记录](21-final-acceptance.md)。
- 索引速率、缓存、内存与磁盘校准见
  [索引与组合负载校准](index-calibration.md)。
- 浏览器用例只断言文本、操作、请求、持久状态与媒体功能；截图、像素、布局与
  人工视觉签收不作为门禁。

## 未完成项

- run 53 的全量索引终态与 generation 清理结果。
- `work_matching_control` 的最终状态、游标与结果数。

在上述两项取得终态前，工单 17 不标记为 resolved。
