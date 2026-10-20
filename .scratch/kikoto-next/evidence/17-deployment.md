注：本文件以占位符记录来源名、作品名、生产端口与快照哈希；实际值保存在访问受控的运维记录中。

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
（可用约 17 GiB）。生产为 Docker 单容器 host 网络，监听 127.0.0.1:<生产端口>，
数据卷 config/cache/data。

切换前快照（服务器回滚目录 `rollbacks/cutover-20261017T164424Z/`）：
停服后复制 `kikoto.db`（3 633 152 字节，sha256
`<快照哈希>`，
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

- 创建规格指定的 OpenList 来源（displayName `OpenList Source A`，id=3，
  匿名，HTTP 201）。首次索引（runId 41）启动。
- 首次索引用校准甜点配置（并发 2、间隔 150 ms）触发源站 429 限流，
  45 秒 partial、329 目录失败；按规格退避与恢复预算处理：重置失败目录，
  改保守配置（并发 1、间隔 1000 ms）续跑，续跑期间 0 次 429、failed 0。
  该过程同时验证了来源退避（`remote source origin backing off`）与失败
  不冒充完整覆盖的契约。
- 浏览回读：`/api/remote-sources/3/works` 返回统一卡片（索引进行中已
  702 张），含无发行编号作品（`primaryCode` 为空）与系列合并卡。
- 搜索回读：`q=<示例关键词>` 命中 1（示例作品标题），搜索直接覆盖来源
  索引条目，未登记也可检索。
- 媒体播放：既有作品 `/api/media/186/stream` Range `bytes=0-31` 返回
  HTTP 206、32 字节、M4A 文件头，证实部署后节目流路由与偏移读取正常。
- 翻译服务：管理员配置入口与权限已验收（`sources:write` 才能保存，
  普通用户 403）。2026-10-17 本会话完成生产配置：管理员保存
  `baseUrl=http://127.0.0.1:<网关端口>/v1`（本机 sub2api 网关）、模型
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
目录（`upstream_unavailable`，来源内 4 个目录）、27 条未确认空目录、
4,254 张卡片、124,693 条文件快照，发布队列余量 0。索引不创建作品、不下载
音频，速率与预算按校准结论执行。失败目录和空目录不计入完整覆盖，报告也不把
搜索命中数当作作品总量。完整终态与失败清单见
[08 真实增量复测](08-source-index.md#真实增量复测2026-10-17当前构建)。

## 生产来源索引（部署后，runId 41）

部署后新建来源 id=3（`OpenList Source A`，匿名）的首次全量索引：激进配置
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
`/<分区>/<示例分组>/...` 的乱码子目录，1 条在 `/<分区2>/<示例分组>/...`），
不计入完整覆盖。

## 索引准入缺陷与修复（2026-10-18）

runId 41 的终态暴露了三类准入缺陷，均在代码中修复并补了真实 HTTP +
迁移 SQLite 的行为测试：

1. **旧父卡不释放子作品**。父目录已有 exact entry 时，重扫直接沿用父根，
   分类容器与上传者目录继续把子目录列为自己的音轨。生产表现为
   `/<分区>/未分类` 一张卡 1,762 条 tracks，`/asmr6/婉儿` 一张卡 115 个子目录、
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

另修 `primary_code` 在重扫时未更新的问题（`RJ00000000作品名…` 这类无分隔符
前缀此前搜索不到），并给 `source_catalog_file.title` 加了覆盖索引，避免文件
标题搜索逐候选全表扫描。

## 生产重扫（runId 42，新 generation）

以 `force:true`、并发 1、间隔 1000 ms、批 32、页 500 启动
`runId=42`，响应 `{"runId":42,"existing":false}`，确认没有复用 run 41 的
generation。运行中观察：目录数随递归展开增长，请求速率约 1 次/秒，符合
保守配置；`publish_cleanup_cursor` 只在「无待观察目录且发布队列为空」后推进，
避免在条目尚未发布时跳过清理。

重扫期间已可观察到修复效果：搜索 `RJ00000000` 返回独立作品卡
（`RJ00000000作品名…`）与父卡 `示例分组` 两条；`RJ00000000` 同样
返回独立卡与 `示例分组2`；`RJ00000000` 从 0 命中变为在 `/<分区>/未分类` 卡内可
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

- 隔离实例 schema 76 的增量复测与 production 数字不能混写；production 的
  当前状态见下方 run 47/run 49 记录。
- ~~生产环境翻译服务地址/密钥由管理员保存后试译~~ 已于 2026-10-17 本会话
  完成（见上节），`hasApiKey=true`，真实模型调用与内容翻译缓存均验证通过。

## 回滚演练（schema 77）

在 `kikoto:next-41f994e0` 上执行受控回滚演练：

1. 停服，用 `cutover-20261017T232517Z` 的 schema 77 快照替换数据库，保留演练前
   config 目录副本。
2. 切回上一镜像 `kikoto:next-7ec4fbec`（它只声明 schema 77 以内的迁移，实际
   读取 schema 77 数据库）。
3. 结果：健康 `{"status":"ok","version":"v0.8.3"}`，`quick_check=ok`，外键违规 0，
   条目数与快照一致（4,517）。
4. 恢复演练前状态：换回演练前 config 目录与 `kikoto:next-41f994e0` 镜像；健康
   200，schema 77，`quick_check=ok`，外键违规 0，run 42 继续推进（恢复后 20 秒
   内请求数由 2,441 增至 2,467）。

结论：schema 77 快照可被上一镜像读取，回滚路径可执行；演练期间丢失的是快照
之后的增量观察，生产库已用演练前副本恢复，未丢失 run 42 的既有进度。


## 生产 schema 78 只读核查（2026-10-19）

在生产数据库快照 `/root/kikoto/rollbacks/v080-20261019T054323Z/kikoto.db`（schema 78，`quick_check=ok`，外键 0）上核查改名链：

- `SELECT count(*) FROM source_catalog_entry WHERE relocated_to_id=id` → `0`。
- `relocated_to_id IS NOT NULL` 共 1,907 行；没有自指，也没有发现二阶环或深度超过 1 的链。
- 代码侧：改名入口排除 old 自身，候选只取 active 行；退休路径有 `id==targetID` 守卫；解析限制 8 跳。

结论：当前生产库不存在自指。数据库没有为 `relocated_to_id` 加 CHECK，但写入路径已有守卫；后续迁移若引入新的写入方，应补同类检查，而不是依赖历史数据。

## 生产 schema 81 部署与全量重扫终态（2026-10-19/20）

部署镜像 `kikoto:next-31db58af`（镜像 ID `4147e4585631`），schema 81，
`GET /health` 返回 `{"status":"ok","version":"v0.8.3"}`，`quick_check=ok`、
外键违规 0、`relocated_to_id=id` 为 0。远端构建目录与本地 HEAD 的
1,557 个 `.go/.ts/.tsx/.sql` 文件按行尾归一化后 SHA256 全部一致。

`runId=46` 以 `force:true`、页 500、批 32、并发 2、间隔 300 ms 启动，
于 2026-10-19 16:11:32 结束，状态 `partial`：

| 观察 | 实际 |
| --- | --- |
| 目录 | 14,491 总数：14,460 complete、0 pending、4 failed、27 empty |
| 请求 | 14,561 次元数据请求 |
| 失败目录 | 4 条，全部为 `/<分区>/<示例分组>/...` 下 `upstream_unavailable`，匿名来源未返回页面 |
| 空目录 | 27 条，保留为未确认空，不计入完整覆盖 |

4 条失败不是权限拒绝或挑战页，也没有被当成完整覆盖；重新扫描时只重试
未完成目录。索引期间的 `database is locked` 与 `context deadline exceeded`
出现在后台推荐/标签刷新，扫描本身以 `partial` 结束而不是伪报成功。

## 卡片可见性缺口与修复（同一轮审查）

生产有 16 张 live 卡片没有任何音频行（例如 `/asmr/中文音声/七重极乐`），
它们仍出现在 `/works` 且标记为可播放。修复后统一卡片列表与搜索只返回存在
`source_catalog_file.kind='audio'` 的卡片；没有音频的父目录保留在数据库中，
但不再伪装成可播放作品。回归 `TestOpenListCatalogOmitsCardsWithoutAudio`
从真实 HTTP 入口断言列表与搜索都不含无音频卡片，同时有音频的卡片仍在。

## 信用文件的取舍与漏检

`.md/.nfo/.html` 与 `.txt` 使用同一套名称词表，不因扩展名无条件读取：
歌词、笔记与日志也使用这些格式，无条件读取会把请求花在不可能包含信用的
文件上。名称词表现包含英文变体、中文与日文写法（见
[OpenList 文档](../../../docs/development/openlist.md#release-sidecars)）。
ASCII 提示按整词匹配，`trackinfo.txt` 不进入候选；CJK 提示按包含匹配。
尚未列入的词属于已知漏检，需要加入共享词表，而不是在 SQL 中另写一份。
每张卡最多读取 6 个信用文件，确保根 readme 加常见语言版本不会被截断。

## 收藏布局断言与 AC-28 的边界

裁决与 [19 浏览器证据](19-ci-browser.md#布局断言与非视觉边界裁决2026-10-19) 一致：
`favorites-state.spec.ts` 的 `boundingBox()` 及全仓既有套件中的同类元素矩形
断言按原样保留，作为可用性回归保护；它们不计入 AC-26、AC-28 的验收证据。
本期非视觉验收只引用文本、操作、请求、持久状态与媒体功能断言，运行设置
`E2E_NO_ARTIFACTS=1` 关闭 trace、screenshot 与 video，也不新增几何或视觉断言。

## 独立审查修复与原状态

同一轮独立审查确认并已修复：

- 失败首读的 `sidecar_revision` 为空，导致每轮重扫把尝试预算清零；现在
  失败路径也记录观察到的 revision，重扫只在 revision 真正变化时重开预算。
- 预算耗尽的 pass 丢弃已读文件；现在先保存部分证据，再不消耗尝试地保持待处理。
- 日文信用名称（`りどみ`、`クレジット`、`説明書` 等）与 `read me` 变体未进入候选；
  现在由一套共享词表驱动候选、排序与发布查询。
- `Readme_TC.txt` 被 4 文件上限挤出；上限提高到 6，并补繁体 readme 回归。
- `RJ… 上/下`、`Disc1/Disc2`、`CD1/CD2`、`Part1/Part2`、`00 Intro + 01..N`
  的拆分错误；分别补真实索引的卡片数与音轨数断言。
- `2021 Live`/`2022 Live` 被合并；年份现在保留为标题的一部分，两个年份为两张卡。
- 旧拆分卡存在别名子节点时永不退休；现在把子节点重指向唯一继承文件的当前卡片，
  再在同一事务退休旧卡。没有唯一继承者时保留旧卡交人工复核，不猜目标。
- 旧卡有多个候选替换者时不迁移，避免把个人记录挂到无关作品。

## 信用文件重试策略与 schema 082（已部署）

081 把暂时故障和永久缺失都记在同一个 `sidecar_attempts` 上：一次上游 503
或凭据失效会消耗五次预算，恢复后卡片仍停在被耗尽的状态。082 将两类结果
分开：

- 永久失败（`404`/`410`、超过读取上限、对象无效）继续消耗五次
  `sidecar_attempts`，耗尽后存 `sidecar_failure='unreadable'`。
- 暂时失败（连接错误、`5xx`、认证拒绝、限流、超时、本地数据库读取失败）
  只递增 `sidecar_transient_attempts`，写入 `sidecar_retry_at`，不消耗永久预算。
  退避从 15 分钟开始、逐次翻倍、上限 24 小时；第 8 次后停止自动探测，
  由管理员重开。
- 同一张卡既有已删除文件又有暂时故障时按暂时失败处理，避免一个删除的
  readme 把仍可恢复的读取永久 settle。
- 重扫不是读取：revision 未变时保留 attempts、退避和停止状态；只有真正
  变化的 revision 自动重开。
- `sidecar_retry_at` 由 SQLite `datetime('now','+N seconds')` 生成，与队列
  比较使用的 `CURRENT_TIMESTAMP` 同一时钟，避免进程与数据库时钟漂移。

管理员入口为 `POST /api/remote-sources/{id}/catalog/sidecars/retry`
（`sources:write`），只重置排队与计数，不改证据、作品关联和个人记录。
UI 入口与停止计数见 [索引与组合负载校准](index-calibration.md) 与
[OpenList 文档](../../../docs/development/openlist.md#reopening-the-credit-queue)。

本地验证：`go test ./...` 全部通过；`go test -race ./internal/httpapi -run
'TestCatalogSidecar|TestCatalog|TestSourceCatalogLooseWorks|TestRetire'`
通过；`go vet ./...`、`gofmt` 检查、前端 `tsc -b`/`lint` 通过；文档链接与
敏感扫描通过。新增回归覆盖暂时失败不消耗预算、SQLite 时钟写入、
永久失败耗尽预算、混合失败保留退避、重扫不重启已停止的暂时卡、退避上限
和管理员重开路由。

## 生产部署 5971912c（schema 82 首次上线，2026-10-20）

- 部署镜像 `kikoto:next-5971912c`（镜像 ID `2c1dcb8e109d`），生产目录
  `/root/kikoto`，DB `/root/kikoto/config/kikoto.db`，Docker 单容器 host
  网络监听 127.0.0.1:<生产端口>。
- 切换前备份 `rollbacks/pre-082-20261019T184014Z/`：schema 81、
  `quick_check=ok`、外键违规 0、entries 71,315、files 107,822、
  sha256 `<快照哈希>`、
  307,957,760 字节。
- 部署后：`GET /health` 返回 `{"status":"ok","version":"v0.8.3"}`，schema 82，
  `quick_check=ok`，外键违规 0，`relocated_to_id=id` 为 0。
- `source_catalog_entry` 已存在 `sidecar_transient_attempts`、`sidecar_retry_at`、
  `sidecar_failure`；`source_catalog_entry_sidecar_due` 索引存在。
- 回滚镜像：`kikoto:next-31db58af`（schema 81）、`kikoto:next-30762a6b`（080）、
  `kikoto:next-0f3cb4c4`（079）。`kikoto:next-be14c79f`（078）已删除，不能
  再作为回滚指针。

## 生产部署 c8c24605（格式目录修复，2026-10-20）

- 部署镜像 `kikoto:next-c8c24605`（镜像 ID `793b23e9dcb5`），schema 保持 82。
- 构建时通过 `--build-arg GOPROXY=https://goproxy.cn,direct` 覆盖模块代理
  （默认 `proxy.golang.org` 在当前网络下 `go mod download` EOF 失败）。
- 部署后 `GET /health` 200、`quick_check=ok`、外键违规 0、
  `relocated_to_id=id` 为 0。
- Full CI run `39314267229`（HEAD `c8c24605`）全绿。

## run 47 与 run 49 的强制重扫

run 47（镜像 `5971912c`，不含格式目录修复）与 run 49（镜像 `c8c24605`，
含修复）都以 `force:true`、页 500、批 32、并发 2、间隔 300 ms 启动，
`minFreeBytes` 2 GiB。两者都以 `partial` 收尾，失败与空目录集合相同：

| 观察 | run 47 | run 49 |
| --- | --- | --- |
| 目录 | 14,491（14,460 complete、4 failed、27 empty） | 见下文收尾记录 |
| 请求 | 14,561 | 见下文收尾记录 |
| 失败目录 | 4 条，全部 `/<分区>/<示例分组>/...` 下 `upstream_unavailable` | 同 4 条，路径一致 |
| 空目录 | 27 条未确认空 | 见下文收尾记录 |

失败目录本轮逐条核对为 `/<分区>/<示例分组>` 下四个乱码子目录，上游未返回
页面；不是权限拒绝或挑战页，也没有被当成完整覆盖。索引期间的
`database is locked` 与 `context deadline exceeded` 出现在后台推荐/标签
刷新，扫描本身以 `partial` 结束而不是伪报成功。

## 未完成项与后续

- run 49 终态（目录、请求、空目录、卡片数、格式目录卡是否清零）随本轮收尾补记。
- 生产验收缺口：两用户冲突关联、关联/拆分重启恢复、时间过期预览、跨源共享、
  登记不写受管理媒体的文件级核查、系列+两单集三对象完整回读、Favorites 跨源展示、
  旧卡 `work_id`/series 重复查询。
- 082 管理员 retry 路由与 UI 入口的生产调用证据。
