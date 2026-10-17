# 17 组合验收、部署与回滚证据

部署产物：集成分支 `codex/kikoto-next-integration` 固定提交
`7ed73fa06b38686caf4422f5a9cbd090ba53b7a3`（应用版本 `v0.8.3`，schema 目标 76）。
本记录只登记实际执行结果；服务器私有路径、凭据与原始日志不进入 Git。

## 双轴审查（matt-code-review）

固定审查基线 `9949175e53979bcd590c75a624bc0efc67684da3`，范围
`git diff <base>...HEAD`（402+ 文件）。两个并行审查子代理分别完成：

- Standards 轴：硬性发现 2 项。A1 历史提交信息格式不合 `<type>(scope):`
  约定（已推送历史，保持原样，后续提交遵守）；A2 `.scratch/` 过程产物入库
  与 CONTRIBUTING 的取舍（工作区 AGENTS.md 明确该目录为工单系统，按上层
  约定保留，证据文件继续经 `make sensitive-check` 扫描）。判断性提示
  （命名、重复骨架、忽略序列化错误等）逐条评估为低风险或不适用，不阻塞
  发布；完整报告在仓库外 `kikoto-next-resume/review-standards.md`。
- Spec 轴：01–16、18、19 各票验收清单的关键行为在当前源码中均有对应
  实现；未发现规格要求但缺失的功能、未发现范围蔓延、未发现与规格冲突的
  实现。三项待复核（翻译缓存读权限路径、取消传播、配置表单 F 覆盖）由主
  代理逐条关闭：翻译端点要求 `library:read` 且按标识回读内容、缓存只存
  公共字段译文；队列在最后一个读者取消时终止在途任务且共享任务去重；
  `translation-settings.spec.ts` 覆盖普通用户无配置标签且 API 读取 403。
  完整报告在仓库外 `kikoto-next-resume/review-spec.md`。

## Full CI 终态

GitHub Full 工作流对部署提交的实际运行：run `39049272400`
（workflow_dispatch，attempt 1），结论 success；17 项检查（Gate、Style、
Backend static/coverage/race 1–4、Frontend、E2E 1/4–4/4、Smoke、
Development Compose、Android build、iOS build）全部 success。

## 组合前后端验收（同一当前构建）

`run-combined-integration.sh` 在部署提交上重跑组合用例（fixture 生产模式
API + 生产前端构建 + 合成 OpenList 与可控模型，退出 0）：

- 编号作品本地播放、折叠播放器、队列持久化；切简体后 Hero 译文出现；
  保存评分 4 + 备注并经 API 回读一致；切繁体后译文与偏好 `zh-tw` 回读一致。
- SPA 导航到无码目录后同一 audio 元素、currentSrc、队列与进度保持；
  无码条目保存评分 5 + 备注；`primaryCode=""`、`workId>0`、
  `workflow-runs total=0`（登记评价不触发下载任务）。
- 共享关联 preview/start 完成后另一条目共享同一评价，两条目 workId 各自
  保留（ADR 0002）；`catalog-directories` 同时列出两个目录；
  `/api/works/<id>/media` 为空（登记不隐式下载音频）。
- 夹具上游轨迹为 6 次 `/chat/completions`（简/繁各 3 字段），无异常路径。

## 服务器资源与备份

部署前复查：2 vCPU、内存 3.5 GiB（可用约 1.6 GiB）、磁盘 59 GiB 总量
（可用约 17 GiB）。生产为 Docker 单容器 host 网络，监听 127.0.0.1:7655，
数据卷 config/cache/data。

切换前快照（服务器回滚目录 `rollbacks/cutover-20261017T164424Z/`）：
停服后复制 `kikoto.db`（3 633 152 字节，sha256
`9189e21f849edf59044dea9d7d493b34c3b98eeeedbee0a2aee9cebde3e44660`，
schema 68、quick_check ok、外键违规 0）及完整 config 目录；manifest 记录
旧镜像摘要与新镜像标签。

## 回滚路径演练（同一部署提交）

修正版演练脚本对 2026-10-16 的停服快照执行两阶段，结果
`rollback drill complete`、`drill_exit=0`：

1. 快照 + 新镜像 `kikoto:next-7ed73fa`（镜像 ID `f09ab8c6f2a0`）：健康
   200，迁移到 schema 76，quick_check ok，外键违规 0。
2. 重新还原快照 + 旧镜像（`kikoto:rollback-before-next-20261016t162538z`）：
   健康 200，schema 保持 68，quick_check ok，外键违规 0。

结论：可执行回滚路径 = 还原切换前快照数据库 + 切回旧固定镜像；旧应用拒绝
未来版本数据库属预期，不能跳过快照还原。

## 部署与迁移

`cutover.sh`（2026-10-17T16:44:24Z）：快照 → `.env` 固定
`KIKOTO_IMAGE=kikoto:next-7ed73fa` → `docker compose up -d` 重建容器 →
健康 200 → 迁移观察 schema 68 → 76，final quick_check ok、外键违规 0，
`cutover_exit=0`。部署后验证：健康接口 200（`v0.8.3`）；登录返回 200 且
权限列表完整；`/api/works` 既有 6 部作品在列；翻译配置回读（默认模型
`Index-Translate-35B-A3B`，未配置服务地址时 `hasApiKey=false`）；验证后
已登出并删除临时凭据文件。

## 部署后功能验收（2026-10-17T17:12 起，生产）

- 创建规格指定的 OpenList 来源（displayName `asmrgay OpenList`，id=3，
  匿名，HTTP 201）。首次索引（runId 41）启动。
- 首次索引用校准甜点配置（并发 2、间隔 150 ms）触发源站 429 限流，
  45 秒 partial、329 目录失败；按规格退避与恢复预算处理：重置失败目录，
  改保守配置（并发 1、间隔 1000 ms）续跑，续跑期间 0 次 429、failed 0。
  该过程同时验证了来源退避（`remote source origin backing off`）与失败
  不冒充完整覆盖的契约。
- 浏览回读：`/api/remote-sources/3/works` 返回统一卡片（索引进行中已
  702 张），含无发行编号作品（`primaryCode` 为空）与系列合并卡。
- 搜索回读：`q=小芹` 命中 1（《小芹的出轨经历》），搜索直接覆盖来源
  索引条目，未登记也可检索。
- 媒体播放：既有作品 `/api/media/186/stream` Range `bytes=0-31` 返回
  HTTP 206、32 字节、M4A 文件头，证实部署后节目流路由与偏移读取正常。
- 翻译服务：管理员配置入口与权限已验收（`sources:write` 才能保存，
  普通用户 403）。2026-10-17 本会话完成生产配置：管理员保存
  `baseUrl=http://127.0.0.1:8081/v1`（本机 sub2api 网关）、模型
  `Index-Translate-35B-A3B` 与服务密钥（网关 80+ 把凭据中仅 group 18 一把
  对目标模型返回 HTTP 200，已选该把；密钥值不写入本证据）。
  保存后同页试译真实调用成功（HTTP 200、176 ms，日→简中
  「今天天气真好啊。我要和姐姐一起听音乐。」，`model=Index-Translate-35B-A3B`、
  `templateVersion=index-user-v1`）；内容翻译对真实远程作品
  catalog entry 12949 的 title 生效（zh-cn `translated/stored`，zh-tw
  复用简体缓存转换 `hit`），内容语言与 UI 语言独立。

## 真实来源索引（发布前同机构建族）

真实匿名 OpenList 来源（规格指定站点）在独立实例（schema 76）上的首轮全量
索引已收于终态 `partial`：14,460 个 complete 目录、0 pending、4 条源站不可读
目录（`upstream_unavailable`，橙澄子汉化组 4 个目录）、27 条未确认空目录、
4,254 张卡片、124,693 条文件快照，发布队列余量 0。索引不创建作品、不下载
音频，速率与预算按校准结论执行。失败目录和空目录不计入完整覆盖，报告也不把
搜索命中数当作作品总量。完整终态与失败清单见
[08 真实增量复测](08-source-index.md#真实增量复测2026-10-17当前构建)。

## 生产来源索引（部署后，runId 41）

部署后新建来源 id=3（`asmrgay OpenList`，匿名）的首次全量索引：激进配置
（并发 2/150 ms）触发源站 429，45 秒 partial、329 目录失败；按规格退避——
重置失败目录 + 保守配置（并发 1/1000 ms）续跑。续跑 0 次 429。截至
2026-10-17T19:05Z 仍在运行（约 1 小时 40 分钟）：完成 5,132 / 5,204 目录、
pending 51、失败 5（`upstream_unavailable`，源站侧不可读目录）、受限 0、
未确认空目录 16、已索引 2,473 张卡片。目录总数随递归展开持续增长，新增目录
多不含可播放音频（indexed 已稳定）。终态与失败目录清单以运行到
TERMINAL 后的记录为准；后台监控持续记录进度。

该轮于 2026-10-17T22:52:22Z 收于终态 `partial`：14,491 个目录中 14,458
complete、27 未确认空、6 failed、0 pending；14,890 次请求；3,897 个已索引
条目；发布队列余量 0。失败 6 条均为 `upstream_unavailable`（5 条在
`/asmr/橙澄子汉化组/...` 的乱码子目录，1 条在 `/asmr6/aftynrose/...`），
不计入完整覆盖。

## 索引准入缺陷与修复（2026-10-18）

runId 41 的终态暴露了三类准入缺陷，均在代码中修复并补了真实 HTTP +
迁移 SQLite 的行为测试：

1. **旧父卡不释放子作品**。父目录已有 exact entry 时，重扫直接沿用父根，
   分类容器与上传者目录继续把子目录列为自己的音轨。生产表现为
   `/asmr/未分类` 一张卡 1,762 条 tracks，`/asmr6/婉儿` 一张卡 115 个子目录、
   9,755 个文件。修复后：容器仍按容器规则；已发布的分组目录在发现编码子目录
   后不再继承父根；发布时逐条清理旧 generation 留下的文件行，并删除已独立成
   卡片的子目录行。
2. **失败/部分运行无法重新观察已完成目录**。`enqueueSourceCatalogIndexTx` 只
   重置 failed/empty/pending，已完成目录保持旧准入决定，重扫请求返回
   `{"existing":true}`。新增 `force` 选项启动新 generation，同时保留“活动
   运行优先、单源不并行全站任务”的约束；手动重试也走 force。
3. **创作者目录折叠**。父根一旦发布，任何有音频的子目录都继承父卡，导致
   `/asmr/中文音声/岁岁`、`/南征`、`/小野猫-N`、`/Tags/Harem` 等被当成单部
   作品。修复后，无编号父根下「有直接音频且不是格式/附属/分轨目录」的子目录
   自成一卡；编号父目录仍把 Disc、特典、格式副本留在同一张卡内。

另修 `primary_code` 在重扫时未更新的问题（`RJ01075962妹妹…` 这类无分隔符
前缀此前搜索不到），并给 `source_catalog_file.title` 加了覆盖索引，避免文件
标题搜索逐候选全表扫描。

## 生产重扫（runId 42，新 generation）

以 `force:true`、并发 1、间隔 1000 ms、批 32、页 500 启动
`runId=42`，响应 `{"runId":42,"existing":false}`，确认没有复用 run 41 的
generation。运行中观察：目录数随递归展开增长，请求速率约 1 次/秒，符合
保守配置；`publish_cleanup_cursor` 只在「无待观察目录且发布队列为空」后推进，
避免在条目尚未发布时跳过清理。

重扫期间已可观察到修复效果：搜索 `RJ01075962` 返回独立作品卡
（`RJ01075962妹妹的夜间侍奉…`）与父卡 `夜汐音声社` 两条；`RJ323783` 同样
返回独立卡与 `呢喃音声`；`RJ330196` 从 0 命中变为在 `/asmr/未分类` 卡内可
检索。终态数字与完整验收以该轮收尾后的记录为准。

## AC-25 组合资源与低空间（当前构建）

`run-20261017T163950Z` 在同提交 `7ed73fa0` 的隔离实例上运行 143 个采样点、
70.02 秒，实际动作不是浏览器视觉检查：一边以 8 KiB/0.6 s 消费 960,044
字节的 `audio/wav`，一边发起目录索引、内容翻译和管理员试译，并以
`LD_PRELOAD` 的 `statvfs` 驱动器把可用空间压到测试预算附近。低空间注入
只替换磁盘余量这一个系统边界；项目读取、工作流、SQLite 和归属逻辑都是
真实实现。

| 观察 | 实测 |
| --- | --- |
| 媒体读取 | 118 个进度点，从 8,192 到 960,044 字节；HTTP 206、`audio/wav`、sha256 `5fa893ae…` |
| 低空间暂停 | 扫描 `paused` / `low_disk_space`；12 个采样、5.04 秒 |
| 暂停时播放 | 暂停窗口内媒体仍前进 73,728 字节，播放器没有被后台暂停终止 |
| 扫描恢复 | 恢复后 42/42 complete、0 pending、40 个索引条目；选项为页 100、批 8、并发 2、间隔 150 ms |
| 翻译队列 | `queued` 与 `active` 峰值均为 1；并发试译返回 HTTP 429 `translation_busy` |
| 队列上限 | 超过预算的内容翻译返回 `translation_queue_full`，未假装成功 |
| 缓存预算 | 低空间时 `cacheStatus=paused_low_disk`，占用仅 4–8 KiB；恢复后写入并命中（`stored` → `hit`） |
| 进程 | RSS 57.5–81.0 MiB |
| 磁盘 | 注入窗口 45,293,568–129,699,840 字节可用；结束后恢复到约 125.6 MB |
| 个人记录 | 重启后同一目录评价 4 与备注 `T17 low-space retained note` 仍在，扫描 run 仍为 complete |

同一产物上的真实模型输出为 `今天天气很好。`（模型
`Index-Translate-35B-A3B`，10 个输入字符、7 个输出字符、306 ms，一次请求），
媒体 Range 三组均为 206/32 B：`0-31`、`1024-1055`、`4096-4127`，对应
sha256 `3ea3c090…`、`7fc7ec19…`、`0231b88c…`。目录覆盖当时为
9,061 complete、303 pending、6 failed、18 empty，另有 1 个 listing_complete；
该快照只说明组合运行时的中间覆盖，不代表全量完成。源站请求轨迹显示
42 次列表请求、中位间隔 151 ms、最长 10,001 ms（429 退避），与实际节流一致。

## 未完成项

- 真实来源索引的 production 部署后终态。隔离实例的 schema 76 增量复测已收于
  `partial`（14,460 complete、0 pending、4 条源站不可读目录、27 条未确认
  空目录、4,254 张卡片），见
  [08 真实增量复测](08-source-index.md#真实增量复测2026-10-17当前构建)；
  production 当前实例是部署后新建的来源，不能把隔离实例的数字写成
  production 全量结果。
- ~~生产环境翻译服务地址/密钥由管理员保存后试译~~ 已于 2026-10-17 本会话
  完成（见上节），`hasApiKey=true`，真实模型调用与内容翻译缓存均验证通过。
