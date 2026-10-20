# 索引与组合负载校准

本文件记录 AC-25 要求的请求速率、队列、缓存、内存与磁盘观察,以及
`16376c2` 有界写事务的复测口径。工单 17 与
[17 部署证据](17-deployment.md)引用本文件。

## 1. 索引吞吐校准(合成树,当前产物)

入口:`KIKOTO_SOURCE_INDEX_PERF=1` 运行
`TestSourceCatalogIndexThroughputCalibration`(`internal/httpapi`),或
`make source-index-performance`。实验每次只驱动一个批次,由生产
catalog-index worker 处理回环桩提供的合成匿名树(4 个分区 × 24 个作品
目录,每个目录含一个格式子目录);桩可以延迟应答并按比例拒绝。实验不访问
真实来源,也不对吞吐或延迟设断言,只给出选项建议。

本次运行条件与结果(开发机 32 逻辑 CPU、`GOMAXPROCS=32`、桩往返 150 ms、
单 cell 预算 900 s、连接池 4、矩阵 19 个 cell、实验总预算 7 分钟,因此只跑完
前 7 个无竞争 cell):

| 排名 | cell(并发/间隔 ms) | 目录/分钟 | 请求/分钟 | 完成 | 池等待次数 | 最长池等待 ms | 进程 CPU 秒 | 等效核数 |
| --- | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: |
| 1 | c2 / 100 | 424.8 | 424.8 | 197/197 | 0 | 0.000 | 1.422 | 0.05 |
| 2 | c2 / 150 | 361.1 | 361.1 | 197/197 | 0 | 0.000 | 1.578 | 0.05 |
| 3 | c1 / 100 | 355.7 | 355.7 | 197/197 | 0 | 0.000 | 1.859 | 0.06 |
| 4 | c1 / 150 | 355.5 | 355.5 | 197/197 | 0 | 0.000 | 1.703 | 0.05 |
| 5 | c1 / 300 | 193.7 | 193.7 | 197/197 | 0 | 0.000 | 1.859 | 0.03 |
| 6 | c2 / 300 | 193.6 | 193.6 | 197/197 | 0 | 0.000 | 2.953 | 0.05 |
| 7 | c1 / 1000 | 59.6 | 59.6 | 197/197 | 0 | 0.000 | 3.844 | 0.02 |

每 cell 均达到 `complete`、`failed=0`、`restricted=0`、`refusals=0`、
`drain_failures=0`,连接池峰值占用 1、饱和采样 0,受保护日志
(`database is locked`、推荐目录批次、元数据标签投影)在本批 cell 中均为空。

结论:请求速率由配置间隔主导(间隔翻倍约使目录/分钟减半),并发 1 与 2 在
无竞争时接近;间隔 100–150 ms 是速率上限区间,300 ms 约为其一半。并发提升
没有提高 CPU 成本(每 cell 0.02–0.06 等效核),也没有产生池等待或写锁争用。
生产选择的保守配置(并发 2、间隔 300 ms、批 32、页 500)因此不是吞吐上限,
而是给同机浏览、翻译和播放留出余量。

## 2. 生产来源索引的速率与资源观察

生产 2 vCPU 实例,来源 id=3(匿名 OpenList),选项
`pageSize=500, pagesPerBatch=32, concurrency=2, intervalMs=300,
maxResponseBytes=2097152, minFreeBytes=2147483648`:

| 运行 | 区间 | 请求 | 平均请求/分钟 | 终态 |
| --- | --- | ---: | ---: | --- |
| run 46 | 2026-10-19 13:52:28 → 16:11:32 | 14,561 | 101.6 | partial(14,460 complete、4 failed、27 empty) |
| run 47 | 2026-10-19 18:48:21 → 21:10:44 | 14,561 | 102.6 | partial(14,460 complete、4 failed、27 empty) |
| run 49 | 2026-10-20 01:19:17 → 见下节 | 见下节 | 见下节 | force 重扫,应用格式目录修复 |

运行期间的容器观测:`kikoto` 容器 CPU 约 100%(单核饱和,2 vCPU 中的一核)、
常驻内存 191.6 MiB;宿主内存 3,531 MiB 中已用约 2,128 MiB、可用约 1,402 MiB;
数据库卷可用空间 7.7 GiB,高于 `minFreeBytes` 的 2 GiB,未触发低空间暂停。

运行期间仍可见后台推荐目录批次与元数据标签投影的 `context deadline exceeded`,
以及语言标题刷新的 `database is locked (5) (SQLITE_BUSY)`;这些后台写在同一
SQLite 写者上限下与索引发布争用,索引本身没有因此丢失目录或伪报完成,
`partial` 状态如实反映 4 条源站不可读目录。

## 3. 有界写事务复测(`16376c2`)

`16376c2` 引入 `sqlutil.BeginBoundedTx`:每次尝试把驱动的 `busy_timeout`
压到 100 ms 后尝试 `BEGIN IMMEDIATE`,失败即归还连接再退避重试,拿到锁后
恢复配置的等待策略,总等待受调用方截止时间或 5 秒预算约束。A/B 复测入口为
`KIKOTO_SOURCE_INDEX_WRITE_PERF=1`,结论记录在
[SQLite 写性能](../../../docs/development/sqlite-write-performance.md):

| 信号 | raw | bounded |
| --- | ---: | ---: |
| 写者持锁时读者等待 | 29,606 ms | 55 ms |
| 读者被饿死 | 是 | 否 |
| 释放写锁后排空 | 30.1 s | 0.58 s |
| 写者观察到的 busy 错误 | 2 | 0 |

本轮的索引吞吐复测没有出现吞吐回归:第 1 节 7 个 cell 的池等待计数与最长
等待均为 0,受保护争用日志为空;生产 run 46/47/49 的请求速率与 run 42 的
保守配置区间一致(约 100 请求/分钟)。有界路径限制的是等待形状,不改变
SQLite 的单写者上限,因此生产仍以批 32、页 500 限制单次持锁时间。

## 4. 生产组合负载（当前部署）

2026-10-20 在当前部署（提交 `aa17d43c`、schema 83）上复测。后台全量索引
run 53 以并发 2、间隔 300 ms、批 32、页 500 运行；同时用 512 KiB/s
限速持续消费一条真实 M4A 音轨 60 秒，并以 12 个工作线程混合请求作品列表、
来源作品列表、目录音轨与内容翻译。

| 观察项 | 结果 |
| --- | --- |
| 媒体 | 完整读取 31,506,448 B / 58.9 s，HTTP 200，未被后台扫描中断 |
| 作品列表 | 1,956 请求，全部 200；p50 12 ms、p95 166 ms、max 1,586 ms |
| 目录音轨 | 1,370 请求，全部 200；p50 8 ms、p95 185 ms、max 974 ms |
| 内容翻译 | 1,975 请求，全部 200；p50 9 ms、p95 158 ms、max 1,316 ms；队列保持 0 |
| 来源作品列表 | 65 请求，全部 200；p50 3,691 ms、p95 4,402 ms、max 4,574 ms |
| 资源 | 宿主可用内存 1,244–1,423 MiB；容器 RSS 约 0.56–0.91 GiB；磁盘可用 11 GiB |

低空间预算单独以翻译配置 `minFreeBytes=20 GB` 触发：运行时状态为
`paused_low_disk`，一次未命中翻译返回 `cacheStatus=paused_low_disk` 且原文与
译文均可读；恢复 2 GiB 后状态回到 `ready`。同窗口的后台写仍会出现
`SQLITE_BUSY` 与 `context deadline exceeded` 告警，但用户请求全部 200，
播放未中断；索引运行没有伪报完成。

来源作品列表在扫描并发时明显慢于其他读路径（p50 约 3.7 s），属于当前
2 vCPU 与 SQLite 单写者下的已知余量问题；未出现失败，也未改变播放或
个人记录。生产仍维持并发 2、间隔 300 ms 的保守扫描配置。

## 复现

```
make source-index-performance
KIKOTO_SOURCE_INDEX_PERF_CELLS=grid/c2-i150 KIKOTO_SOURCE_INDEX_PERF_DIRS=24 \
  KIKOTO_SOURCE_INDEX_PERF_RTT_MS=150 make source-index-performance
cd backend && KIKOTO_SOURCE_INDEX_WRITE_PERF=1 \
  KIKOTO_SOURCE_INDEX_WRITE_PERF_MODE=bounded \
  go test ./internal/httpapi -run TestSourceIndexWriteContention -count=1 -v
```

实验只打印测量值,不设吞吐或延迟门禁;回归由
`TestSourceCatalogPublishTailStaysBounded` 等有界性用例承担。
