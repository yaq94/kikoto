# 02/07 本地集成验收

本记录对应包含此文件的集成提交。集成分支为 `codex/kikoto-next-integration`，
02 已快进至 `3bde7d510587f2f0d25c4d5156e8244898dcacef`，再合入 07
`71514b192a140a91caf2590af632e3ead86a8d81` 并补齐下述接口。固定最终审查基线
仍为 `9949175e53979bcd590c75a624bc0efc67684da3`。

## 实际集成差异

- 唯一文本冲突在 `RemoteWorkCard.tsx`，同时保留翻译资源与无码卡片导航。
- `content_translation.go` / `content_translation_fields.go` 将 catalog selector
  接入 07 的 `loadSourceCatalogEntry` / `loadSourceCatalogFiles`；缓存之前重验
  source enabled/type 和 Demo 边界。title 核对条目标题，directory 必须精确匹配
  真实 Path，再核对 Title。不回退其他资源，不登记作品，不取媒体 URL。
- catalog 卡片和详情使用已有 `ContentTranslationScope` 与 `TranslatedText`。
  四个目录组件补传真实路径；译文和原文切换不改变文件、导航和播放器对象。
- OpenList 夹具加入边界模型桩与独立翻译目录，端口和允许的前端 origin 可配置；
  新 F 与原有 F 共用真实 API、会话和迁移 SQLite。没有新增依赖或另一套目录解析。
- [执行记录](../execution.md#开发依赖与发布依赖)、总览和 08/11/17 明确开发与
  发布两类依赖。02 resolved，07 verified-local；07 LIVE 与 17 全部门槛保留。

## 红绿与行为证据

- catalog title H 先返回 404（`j-jl0gga`，退出 1），最小 loader 接入后通过
  （`j-gnpnv5`，退出 0）。验证译文/原文、缓存 stored/hit、错误文本、匿名、
  不存在 selector、来源停用与类型变更；公开库/目录/任务回读证明没有登记或下载。
- catalog directory H 先返回 404（`j-thton6`，退出 1），接入真实 Path→Title
  后与 title 一起通过（`j-3i8x52`，退出 0）。显示名冒充路径、缺路径返回 404，
  同路径错误文本返回 409，原文和目录公开回读保持身份。
- 新 F 初次缺少 translated card（`j-5fmkvo`，退出 2）；接通资源后页面已有
  译文，但 exact heading 名还包含原文/重试按钮，修正为标题内的文本定位后通过
  （`j-5sknb3`，退出 0）。随后旧 F 遇到前次运行遗留的语言偏好（`j-47xeqk`，
  退出 2）；通过公开 API 在旧用例开始明确恢复原文偏好，完整 2/2 通过。
- 首轮组合 race 的新增 H 统计了管理员更换来源类型后自动触发的可用性探测，
  因而失败（`j-yjmjn7`，退出 1）；将“无来源请求”断言限定在翻译读取阶段，
  仍验证拒绝请求不再调用模型，重跑完整组合通过。未改生产探测行为。

## 当前产物验证

Go 1.26.9、Node 24.19.0；进程级 PATH 与 `GOTOOLCHAIN=local`。复用已安装依赖。
下列命令均读取终态，普通构建/单元测试允许未改包的内容寻址缓存；定向 H/R 强制
`-count=1`。所有浏览器用例关闭 screenshot、trace、video，没有视觉验收。

| 命令/入口 | 确定性结果 |
| --- | --- |
| `go test -race -mod=readonly ./internal/httpapi ./internal/translation -run '^Test(ContentTranslation\|Translation(Configuration\|Trial\|Admin)\|CacheIdentity\|ProtectedNames\|WorkReference\|ResolveIsReadOnly\|PersonalReview\|OpenList(Finite\|Playback\|Review\|Upgrade\|Metadata\|Media\|Catalog))' -count=1 -v -timeout=5m` | 退出 0，`j-dt1tso`；38 个 H/R 顶层与 2 个 U 顶层通过，无 race。httpapi 68.811s。表中反斜线仅转义 Markdown 管道。 |
| `go test -mod=readonly ./internal/storage -count=1 -timeout=3m` | 退出 0，同任务最后阶段，12.095s；迁移链、基线与恢复套件。 |
| `make backend-format backend-test backend-vet backend-build` | 退出 0，`j-efp0g3`；全包通过，httpapi 96.356s。 |
| `make backend-lint GOLANGCI_LINT_BASE=9949175e53979bcd590c75a624bc0efc67684da3` | 退出 0，`j-y1avgb`；0 issues，使用现有进程代理；共享锁已释放。 |
| `make -o frontend-install frontend-format frontend-lint frontend-i18n frontend-build frontend-coverage frontend-docs` | 退出 0，`j-1334y0`；0 lint 警告/错误，E2E 类型检查、构建、153 文件/867 测试、122 公开 Markdown 链接、5 locale/11 用户页通过。Vite 有既存 configLoader 未来兼容提示。 |
| `make -o frontend-playwright-install frontend-e2e E2E_ARGS='openlist-catalog.spec.ts --project=desktop-chromium --workers=1'` | 退出 0，`j-ea6bd4`；2/2，8.8s；原播放/评价链路与新增翻译/原文/路径/只读链路。 |
| `make -o frontend-playwright-install frontend-e2e E2E_ARGS='content-translation.spec.ts --project=desktop-chromium --workers=1'` | 退出 0，`j-b83ir1`；3/3，9.4s；两用户语言、动态内容、迟到/失败与原文回归。 |
| `make -o frontend-install frontend-format frontend-lint frontend-docs sensitive-check`、`git diff --check` | 文档与 F 隔离调整后退出 0，`j-b5j0ws`；71 个变更文件隐私扫描通过。 |
| 复用 `checkFile` 检查计划链接并比较原票清单 | 退出 0；28 份计划/证据相对链接有效，17 票验收条文保持，07 LIVE 未勾选，固定审查基线保留。 |

F 前端为 `127.0.0.1:3119`，API 为 `127.0.0.1:17619`。先运行 OpenList fixture，
停止后用同一端口运行 content-translation fixture；Vite 连接同一真实 API 地址。

## 069 升级与源码出站审查

实际 Git 差异只增加 `069_openlist_catalog.sql` 和 `compat/069_dev.sql`，
001–068 与已发布 `068_v0.8.3` 未改。真实 068 同库升级 R 保持 canonical、alias、
numeric edition、收藏、播放游标和两用户评分备注，再登记无码对象并重开验证。

另用 Node 自带 SQLite 在内存加载发布 baseline 后执行当前 069（退出 0）：
164 个 view/trigger 的名称、类型与 SQL 定义逐项完全一致；旧 work/logical/edition
主键和外键关系保持；非空 work/logical 编号仍拒绝重复，两个空编号可并存；
`foreign_key_check` 无行，`integrity_check` 为 `ok`。未修改迁移或生成额外基线。

源码复核：OpenList metadata 复用 `sourceClient` / shared outbound policy，
POST 不跟随重定向，串行节流、30 秒/2 MiB 上限及取消保留；media 先核验 catalog
真实路径，再经 `remotePlaybackURLAllowed`、`openRemotePlaybackResponse` 与
有界 streaming；过期链接最多重取一次。翻译复用既有配置客户端与共享缓存队列。
上述边界在组合 H 中验证；源码存在本身不计作 LIVE。

## 外部真实来源：分层证据与未完成项

1. 既有裸程序 fs/list 诊断：本机代理与部署网络均记录 HTTP 403、Cloudflare 1010、
   `browser_signature_banned`、`owner_action_required=true`，见[07 原始证据](07-openlist.md#live-缺口与可复验入口)。本次没有重复列表请求。
2. 本次 IAB 正常匿名导航：首页六个分区、一个子目录及一条音轨页面均加载。
   未登录或处理验证码。文件页自行启动站点播放器，后续语义状态显示时间推进。
   这是网页功能观察，没有受控的 Range 响应头、偏移或字节证据；已关闭诊断 tab。
3. 根据实际页面取得一个文件定位后，只运行一次既有 `TestOpenListLiveRangeSample`
   （`j-9hs506`，退出 1，0.712s）。失败发生在 `openlist_live_test.go:44` 的首次
   fs/get 元数据阶段；两个 32 字节 Range 均未开始。该入口隐藏上游细节，不能把
   本次失败直接分类为 403/1010，也不能声明协议、解码或 Kikoto 真实播放通过。

真实定位仅存于仓库外的私有研究记录，不进入代码、夹具或提交。没有更换 UA、
伪装 TLS、使用 CDP、读取 cookies 或注入网络拦截。IAB 网页可用与程序 API 失败
需要后续有界兼容性诊断；不是整个匿名网页不可用，也不是已取得全部 LIVE。
07 保持 verified-local，LIVE 未勾选；17 的真实媒体、组合负载与部署验收均待执行。

## 进程、保留项与后续接口

OpenList fixture `j-hnlh8b`、内容 fixture `j-kn1fzn` 都经自身 stop 端点退出 0，
并执行自身临时库清理；Vite `j-81myby` killed。全部检查任务有终态；没有部署。
作者 worktree 只读，未触及 02 两份拒删合成缓存或 07 被拒删系统临时库，未增加
忽略规则、移动目标或清理 worktree。它们的保留限制不等于主集成目录有未跟踪文件。

- 03：`backend/internal/httpapi/content_translation.go`、`translation_runtime.go`、
  `backend/internal/translation/{cache,protection,queue}.go`；已有繁体字段优先，缺失
  繁体生成/转换仍由 03 实现。前端公共入口 `components/content-translation/ContentTranslation.tsx`。
- 08/11：`backend/internal/httpapi/openlist_catalog.go` 的两个只读 loader、
  `persistOpenListRoot` 与 `internal/openlist/client.go` 的有界读取；持久 schema
  `source_catalog_entry` / `source_catalog_file`，`catalogEntryId` 与可空 `workId` 分离。
  前端 `lib/sourceCatalogApi.ts`、`CatalogWorkDetail.tsx`、`catalogTreeModel.ts` 复用
  现有目录和播放器模型；不得按译名或路径合并作品。下一迁移号由主流程统一分配。
