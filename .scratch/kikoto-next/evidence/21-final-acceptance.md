# 21 最终验收记录（当前部署）

本文件汇总工单 17 在当前部署上的实际验收证据。公开记录只保留脱敏摘要；
真实数据库、私有路径、密钥、个人记录与原始日志不进入 Git。

## 产物与审查

- 集成 HEAD：`2bae5db98acceff6b45fdb4d168908c7b0da9177`。
- 固定审查基线：`9949175e53979bcd590c75a624bc0efc67684da3`。
- 部署镜像：`kikoto:next-2bae5db9`，镜像 ID
  `sha256:25e5467ede40342fad8ca772b95d931343a87e3c5239bd535bbc0330777b1a99`。
- Full CI：run `39496305624`，27 个 job 全部 `success`（含 Gate）。
- 双轴审查：Standards 与 Spec 两道审查在当前差异上执行，结论与修复见
  [最终审查记录](22-final-review.md)。
- 本地证据：后端全量测试、race shard 1/4、前端类型检查/静态检查/构建/单元测试、
  文档链接与 locale 检查、敏感变更扫描均以当前 HEAD 重新执行，结果见
  [执行记录](../execution.md#验证证据登记)。

## 部署与数据

- 服务状态：`running`，健康检查 `GET /health` 返回 200 与 `status=ok`。
- 数据库 schema `083`；`schema_state.current_version=83`、`baseline_version=66`、
  `dirty_version=NULL`；`schema_migration` 已记录 83。
- `PRAGMA quick_check=ok`；`PRAGMA foreign_key_check` 返回 0 行。
- 部署前快照包含数据库、WAL/SHM、环境文件、Compose 文件与回滚脚本；回滚镜像
  `kikoto:next-2d78344a` 与 schema 82 快照保留。回滚路径与兼容矩阵见
  [发布回退](../../../docs/development/release-rollback.md)。

## 内容翻译

- 管理员可读取与修改翻译配置；密钥只以掩码返回。普通用户读取或修改配置返回
  403。当前运行配置使用一个 OpenAI 兼容端点、一个模型、并发 1、队列 32、
  缓存 256 MiB、低空间阈值 2 GiB。
- 真实日文标题卡片：
  - `origin` 返回原文，状态 `original`。
  - `zh-cn` 返回简体译文，状态 `translated`、`cacheStatus=stored`。
  - 再次请求 `zh-cn` 返回同一译文，`cacheStatus=hit`。
  - `zh-tw` 返回繁体译文，`cacheStatus=hit`，与简体文本不同。
- 保护名称：catalog 卡片与系列的 circle、voice actor 名称在翻译前替换为占位符，
  译文恢复后保持逐字不变；个人评分与备注不进入翻译输入。
- 低空间：把 `minFreeBytes` 临时提高到高于可用空间后，运行时状态为
  `paused_low_disk`，一次未命中翻译返回 `cacheStatus=paused_low_disk`，原文与
  译文均可读；恢复 2 GiB 后状态回到 `ready`。

## 来源目录、卡片与搜索

- 生产来源当前有 42,624 张可播放音频卡片，与来源作品 API 的 `total` 一致。
- 其中带发行编号 2,780 张，无发行编号 39,844 张；未登记作品 42,610 张，
  已登记 14 张。未登记卡片不会被列表或搜索过滤。
- 全部 14,496 个来源目录已进入目录表；可用条目 111,690，live 条目 111,790。
- `source_catalog_observation` 的待发布队列从 171,604 条降到 2,439 条；
  其中 1,761 条位于 `catalog_root=''` 的 staging 目录，678 条等待当前 generation
  发布。该队列由 generation 清理与发布步骤回收，不把 staging 观察当作卡片。
- 首次全量索引 run 52 收于 `partial`：14,461/14,494 目录完成、6 个源站不可读、
  27 个未确认空目录；当前代码的 run 53 正在同一来源上强制重扫，终态与分区覆盖
  见下节。

## 多来源、关联与个人记录

- 同一作品的两个来源目录可以展示、切换与精确播放；切换来源不替换当前音轨、
  不重置队列，继续播放使用已保存进度。
- 两个来源目录关联为同一共享身份后，同一用户的评分与备注跨源读取一致；不同
  用户各自只看到自己的记录。测试用户与测试记录已从生产删除，删除后相关个人
  表无残留。
- 关联操作 7 的两条目录分别是 M4A 与 MP3 版本：时长 652.6 s 与 660.1 s，
  音频能量包络互相关 0.83，标题明确标注“发重复了”。保留为同一共享身份，
  不拆分为两个作品。
- 评分与备注保存不创建下载任务，也不写入受管理媒体位置；生产 `workflow_run`
  数量在操作前后保持不变。

## 媒体与组合负载

- 真实 M4A 音轨的 Range 请求返回 206、32 字节与正确的
  `Content-Range: bytes 0-31/31506448`；另一偏移同样返回 206。
- 当前部署的 AC-25 组合负载：后台全量索引运行时，以 512 KiB/s 消费一条真实
  音轨 60 秒，同时以 12 个工作线程混合请求作品列表、来源列表、目录音轨与内容
  翻译。媒体完整读取 31,506,448 B；作品列表 1,956 次、目录音轨 1,370 次、
  翻译 1,975 次均返回 200，翻译队列保持 0；来源列表 65 次返回 200，p50 约
  3.7 s。宿主可用内存保持 1.2 GiB 以上，磁盘可用 11 GiB。
- 同窗口的后台写仍出现 `SQLITE_BUSY` 与 `context deadline exceeded` 告警；
  用户请求未失败，播放未中断，索引未伪报完成。该余量已记录在
  [索引与组合负载校准](index-calibration.md#4-生产组合负载当前部署)。

## 浏览器与真实样例

- Full CI 的 E2E 分组全部通过：mocked 1/4–4/4、translation-settings、
  content-translation、personal、series、matching-shared、source-playback、
  openlist、mobile-shell、integrated-acceptance。
- 本期浏览器用例只断言文本、操作、请求、持久状态与媒体功能；截图、像素、
  布局与人工视觉签收不作为门禁。

## 部署期间重启与续跑

最终部署把镜像从 `aa17d43c` 切换到 `2bae5db9`，容器重启时全量索引 run 53 正在
扫描中间分区。重启后仍是同一个 run，扫描从其检查点继续：

| 时点 | 完成目录 | 发现目录 | 已索引条目 |
| --- | ---: | ---: | ---: |
| 重启前（21:17 之前） | 7,112 | 7,196 | 39,588 |
| 重启后（21:18） | 7,558 | 7,818 | 40,196 |

没有新建第二个索引 run，也没有把尚未观察的目录计为完成。这直接验证了
「取消、重启、重试后从检查点续跑」的行为。

调度同时创建了一个 `database_backup_schedule` 的 run 54（`queued`），属于数据库
备份调度，与来源索引无关，不构成重叠的全站扫描。

## 上游数据缺陷（不可修复项）

来源上有 4 个目录名在 OpenList 的 JSON 响应里已经含 U+FFFD 替换字符（源站自身
返回 `\ufffd`，不是本地解码造成）。对其中一个目录执行 `fs/get` 与 `fs/list`
都返回 `object not found`，说明源站保存的名字已不可寻址，无法恢复原始字节。

其中 2 个目录与已有可播放卡片重复（同一作品另有可访问副本）；另外 2 个
另外 2 个作品各只有这一份副本，且目录内只有压缩包与一个 MP4，音频在无法
寻址的子目录里。扫描把它们记为 `failed` / `upstream_unavailable`，不计入完整
覆盖，也不伪造卡片。这是源站数据缺陷；若上游修复目录名，下一次扫描会自动收录。

## 未完成项

- run 53 尚未到达终态；终态、分区完成数、失败/未确认空目录与 generation 清理
  结果必须补齐后才能关闭 AC-21。
- `work_matching_control` 仍为 `running`；必须确认其到达 `complete` 或
  `partial`，并记录最终游标与结果数。
