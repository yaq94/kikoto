# 03 简繁转换验收

**Implementation:** resolved

本记录对应包含此文件的 03 提交，基线为
`08b14696c76c63d3a75e5fb0c984dbb2e568ad12`。完成前已 fetch 远端 integration，
在 `codex/kikoto-next-t03` 执行合并，返回 `Already up to date`；没有操作主工作区。

## 验收清单

| 清单 / AC | 当前证据 |
| --- | --- |
| 简繁复用，缓存分离 / AC-07、AC-06 | `TraditionalSharesSimplifiedAndPreservesNames` 从繁体开始生成，再读简体、繁体，模型恰好一次；更换模型后生成一次。`TraditionalAndSimplifiedShareInflightGeneration` 经真实 HTTP 同时请求两种文字，runtime 回读两个读者、一个活动任务、零排队，模型一次、两份正确文字。只缓存带名称占位符的简体，繁体按请求转换，不保存可能过期的派生缓存。既有原文/模型/模板、队列、淘汰和低盘测试一起通过。 |
| 现成字段优先 / AC-07 | `TraditionalReusesAuthoredSimplifiedWithoutModel` 在未配置模型时转换手写简体标题，更新字段后立即变化；现成繁体标题仍直接返回 existing。`TraditionalAuthoredTagUsesSimplified` 证明系统标签沿用简体字段且原文可读。既有繁体标题/标签回归通过。 |
| 名称、私人文字、路径与身份 / AC-04、AC-07 | H 验证中文社团、声优不进入模型且转换后逐字保留；个人备注/标签由真实 API 写入、拒绝冒充公共字段外发、公开详情回读保持。catalog H 验证 `音乐/风声.wav` 原路径保持、转换后的路径不能冒充原路径、来源停用后缓存不可读、不登记 work；F 回读媒体 ID 与路径保持。 |
| 原文、失败、重试与迟到 / AC-07 | 超限手写简体字段触发 conversion_failed，响应保留原文；修正字段后再次请求成功。F 保留等待/失败原文、显示限流状态、显式重试成功，繁体迟到请求被取消且不能覆盖后选的英语。H/原 F 的取消、超时、原文和会话回归一起通过。 |
| 成熟转换实现 / AC-07 | 使用 OpenCC `s2t` 与内嵌官方词典；版本、依赖和实际资源观察见下。没有自制转换字典。 |
| 真实文本 / AC-07 | 下述两条中性文本实际通过 Index 内容翻译链路，繁体、简体和再次繁体分别得到 stored/hit/hit；语义正文直接检查。 |
| 少量非视觉 F / AC-28 | 现有 3 条内容 F 与新增 2 条简繁 F，共 5/5；每个文件显式关闭 screenshot、trace、video，仅断言文本、语义操作、请求和公开 API。最终结果目录未发现 png/webm/zip。不作像素、布局或人工视觉签收。 |

## 红绿与修正记录

- `TraditionalSharesSimplifiedAndPreservesNames` 初始返回
  `translation_invalid_input`，`j-tdrh54` 退出 1；接入 OpenCC 后
  `j-us87m5` 退出 0。
- `TraditionalReusesAuthoredSimplifiedWithoutModel` 初始返回
  `translation_not_configured`，`j-no74nr` 退出 1；复用原字段选择规则后
  `j-hvaovc` 两项通过，退出 0。
- F 初次失败于隐藏标题仍处于按可见区域翻译的 pending 状态；用例先滚动到
  待验证标题。下一次失败于错误断言字段名 `media`，改为既有 `mediaItems`。
  `j-tv1x5j` 两条通过；增加按测试分离模型与请求计数后，最终 5 条组合通过。
- 新 catalog H 的夹具补齐父目录，并将错误断言 `id` 修正为公开
  `catalogEntryId`；未修改 catalog 生产代码。最终 H/race 全部通过。
- 首次 LIVE 在第一条成功后，第二条夹具重复创建唯一声优名而中止；复用同一
  声优与社团后整组通过。本次真实调用合计 3 次生成（含此前成功的第一条），
  没有掩盖早期失败或将该次算作完整组通过。

## 实际命令与退出状态

Go 1.26.9、Node 24.19.0、npm 11.17.0；每进程 PATH 与 `GOTOOLCHAIN=local`。
检查均在本票 worktree 执行。普通依赖下载沿用已授权的进程代理与现有模块缓存。
前端 `make frontend-install` 安装现有锁文件 192 个包，0 vulnerabilities，退出 0；
没有新增前端包。

| 命令 | 结果 |
| --- | --- |
| `go test -race -mod=readonly ./internal/httpapi ./internal/translation -run '^Test(ContentTranslation\|CacheIdentity\|ProtectedNames)' -count=1 -v -timeout=5m` | `j-ema74v` 退出 0；20 个 H/R 顶层、2 个 U 顶层通过，无 race；未配置的 LIVE 正常跳过，另行实测见下。Markdown 管道有转义。 |
| `make backend-format backend-test backend-vet backend-verify backend-build` | `j-xvebou` 退出 0；全后端通过，httpapi 93.712s；所有模块校验通过，当前可执行文件已构建。 |
| `make backend-lint GOLANGCI_LINT_BASE=08b14696c76c63d3a75e5fb0c984dbb2e568ad12` | `j-x83nix` 退出 0，0 issues；共享锁正常释放。 |
| `make -o frontend-install frontend-format frontend-lint frontend-i18n frontend-build` | `j-jxgy3z` 退出 0；0 lint 警告/错误，E2E 类型检查与构建通过。Vite 保留既有 configLoader 和大 chunk 提示。 |
| `make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='content-translation.spec.ts traditional-translation.spec.ts --project=desktop-chromium --workers=1'` | `j-3uj1pp` 退出 0，5/5，11.1s。F 当前 Vite 3113 → production API 17613 → 迁移 SQLite，只有外部模型用桩。 |
| `make -o frontend-install frontend-docs sensitive-check`、`git diff --check` | `j-tbi1hb` 退出 0；122 公开 Markdown 链接、5 locale/11 用户页通过，19 个当时变更文件隐私扫描通过。工单与本证据加入后提交前再次扫描。 |

## LIVE 正文与次数

`go test -mod=readonly ./internal/httpapi -run
'^TestContentTranslationTraditionalLiveConfiguredService$' -count=1 -v -timeout=3m`
实际经环境注入专用配置，`j-zqj80g` **退出 0**，3.368s。
使用真实会话、生产路由、迁移临时 SQLite、既有出站策略和实际模型。
配置从已授权归档的精确成员仅读入进程内存；未解包或提交端点、密钥、请求头。
模型为 `Index-Translate-35B-A3B`，沿用单 user `index-user-v1`，没有改写调用名。

| 样例 | 原文 | 实际简体 | 实际繁体 | 生成含转换 / 两次缓存切换 |
| --- | --- | --- | --- | --- |
| 日文 | 今日はいい天気です。示例声优と示例社团の音楽を聞きます。 | 今天天气真好。我听示例声优和示例社团的音乐。 | 今天天氣真好。我聽示例声优和示例社团的音樂。 | 1494 ms / 16 ms |
| 中文 | 风吹过森林，示例社团和示例声优一起听音乐。 | 风吹过森林，示例社团和示例声优一起听音乐。 | 風吹過森林，示例社团和示例声优一起聽音樂。 | 1351 ms / 14 ms |

两条均保留天气/听音乐或风吹森林的中性含义，正文中的保护名称保持原字形。
每条先请求繁体，产生 1 次简体生成；随后简体与繁体各命中一次同一缓存，
共 2 次生成、4 次缓存读取。runtime 回读两条缓存、零读者。上次夹具失败前
另外 1 次日文生成耗时 1852 ms、两次缓存读取 13 ms；因此本次总计 3 次真实生成。
这些是有限短文本样例和单次耗时，不证明任意文本质量或生产吞吐。

## 依赖与资源

- 新直接依赖 `github.com/longbridgeapp/opencc v0.3.13`；新增运行时间接依赖
  `github.com/liuzl/da v0.0.0-20180704015230-14771aad5b1d`、
  `github.com/liuzl/cedar-go v0.0.0-20170805034717-80a9c64b256d`。
  仅更新项目 `go.mod/go.sum`，没有系统安装或 C 库；额外 go.sum 项来自模块图校验。
  官方 main 文档显示迁移后的 `longbridge` 名称，但该发行版 go.mod 声明
  `longbridgeapp`，因此使用发行版自身的模块路径。
- `sync.OnceValues` 惰性初始化一个 `s2t` 转换器；后续请求只读共享词典，
  在名称恢复前转换。不按字段或请求重复加载，也不落盘繁体派生缓存。
- 本机独立测量：嵌入词典原始总计 **1,125,287 bytes**；首次初始化
  **22.545 ms**；GC 后堆增量 **8,518,800 bytes**，初始化累计分配
  **20,308,960 bytes**；固定短句 1000 次转换平均 **5.911 µs**。
  测量程序仅保存在仓库外。它测量库初始化与短句转换，不是完整服务峰值内存。
- 当前 Windows Go 构建文件 **31,170,560 bytes**；未重建无依赖基线作二进制
  体积差分，因此不将上述词典大小当作准确可执行文件增量。
- 没有数据库迁移；现有最高 069 保持。服务器内存、Linux 镜像和组合负载由
  整体验收在部署产物上实测，不用本机数字代替。

## 进程与保留项

本次 fixture `j-z1yidq` 收到自身 `/__fixture/stop` 的 204，正常退出 0，
执行了临时目录清理；Vite `j-z17bd7` 已 killed。所有测试、构建与 LIVE 命令
已读取终态。没有停止其他票的服务，没有本票未跟踪合成缓存或删除拒绝。
已忽略的项目依赖、构建输出和 Playwright 结果可供复用，原始运行日志与资源
测量程序保留在仓库外。本票所有验收已完成；未执行部署、媒体 LIVE 或视觉验收。
