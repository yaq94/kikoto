# 01 翻译配置与试译验收

验收日期：2026-10-15（Asia/Shanghai）。实现基于集成提交 `71a9315`，总审查基线保持 `9949175`。证据对应本票提交内的源码；只记录本票实际运行结果。

## AC 与入口

| 验收 | 证据 | 结果 |
| --- | --- | --- |
| AC-01 默认模型、保存、密钥和权限 | `TestTranslationAdminConfigurationIsPrivateAndPreservesModel` 通过真实 HTTP、会话和迁移 SQLite 验证默认 `Index-Translate-35B-A3B`、自定义调用名原样保存、掩码回读、省略保留与空串清除；普通用户 GET/PATCH 为 403，匿名读取为 401。 | 通过 |
| AC-01 重启与语言隔离 | `TestTranslationConfigurationAndTrialSurviveApplicationRestart` 关闭 HTTP 服务、应用与数据库，重新迁移并打开同一数据库；复用真实持久会话，经 GET 回读相同配置，再向边界上游完成试译。已保存界面语言保持日语，试译目标为简体中文。 | R 通过 |
| AC-01/02 表单入口 | `translation-settings.spec.ts` 启动真实 Kikoto 生产模式进程及迁移 SQLite，仅外部模型使用本地服务桩。管理员从设置标签进入，保存、重载、试译、查看失败并清除密钥；第二场景取消请求并确认上游连接结束，再成功试译。普通用户没有翻译配置标签，API 读取为 403。 | F 2/2 通过 |
| AC-02 模板与参数 | `TestTranslationTrialUsesSavedModelKeyAndOfficialSingleUserTemplate` 在外部 HTTP 边界读取真实请求，确认单条 user 模板、原样模型名、保存的 bearer key、`temperature=0`、按输入长度计算的输出预算（短文本 256，上限 4096）、非流式及可选关闭推理扩展。省略密钥与明确清除后的真实请求头也被验证。 | H 通过 |
| AC-02 失败分类 | `TestTranslationTrialFailuresAreClassifiedWithoutLeakingOrRetrying` 覆盖空输出、截断、缺失完成标记、格式错误、无效 UTF-8、推理块、401/403、429、400、503、重定向、超大响应与超时。每例仅一次上游请求，失败无译文且不泄露上游内容或配置。 | H 14 个子场景通过 |
| AC-02 有限真实生成 | 专用 HTTPS 配置在进程内存读取，以进程环境注入 `TestTranslationLiveConfiguredService`；通过本票 HTTP 配置和试译接口发出一次真实请求，使用选定 Index 调用名、正式模板和默认关闭推理扩展。 | LIVE 通过 |
| AC-27 输入与请求边界 | `TestTranslationConfigurationRejectsInvalidBodiesAndURLsAtomically` 拒绝非法协议、嵌入凭据、查询参数、fragment、非法字段、超大 JSON 和追加 JSON，并 GET 验证无部分更新。 | H 通过 |
| AC-27 redirect、私网和 DNS | `TestTranslationTrialRejectsRedirectsWithoutForwardingCredentials` 确认同源、异源、私网、保留地址和带凭据重定向均不继续请求；`TestTranslationTrialPinsConfiguredDNSAndRejectsUnusableAddresses` 确认显式配置的私网/保留 origin 例外、一次解析、数值地址拨号和不可用地址拒绝。 | H 通过 |
| AC-27 资源与取消 | `TestTranslationTrialBoundsConcurrencyAndCancelsOnShutdown` 验证同时仅一个试译，第二请求立即得到固定 busy 分类，应用关闭取消上游。F 补充浏览器主动取消与释放后再次试译。 | H/F 通过 |
| AC-28 非视觉边界 | 两个 F 用例顶部显式关闭 screenshot、trace、video；只检查语义控件、文本、HTTP 请求、会话状态和取消。未调用截图、像素或布局 API。 | 通过 |

## 红绿记录

各轮按一个完整行为推进，没有用私有实现函数镜像作验收：

1. 管理员配置测试初次返回 404，退出 1；实现独立配置模块与路由后退出 0。
2. 保存后试译测试初次 POST 返回 404，退出 1；实现策略客户端、正式模板和试译入口后退出 0。
3. 失败分类初次把空、截断等结果返回成功，401/429/超时分类不足，退出 1；修正后 14 个失败子场景全部通过。
4. 原子输入和并发测试初次接受追加 JSON、并行进入模型而超时，退出 1；严格解码、单试译限制及关闭取消后通过。既有策略覆盖的 redirect/DNS 回归从首次运行即通过。
5. 表单 F 初次找不到 Translation 标签，失败退出；接入表单后通过。取消和普通用户第二场景首次即通过。

环境或检查失败单独处理：Windows 原生依赖被 Vite 占用导致重复安装失败，停止本票 Vite 后按现有入口安装成功；无扩展名 Go 产物无法由 Node 启动，测试把它复制为自己的临时 `.exe`；一次 Make 调用目录错误已纠正。后端 lint 的响应关闭返回值提示已修正；下载 lint 元数据的连接失败使用既有进程代理完成。隐私扫描对合成安全测试和界面标签的误报通过明确的夹具构造和标签命名消除；官方资料仅为对应开发文档扩展已有允许清单，扫描规则未修改。

## 已运行的检查

工具链：Go 1.26.9，Node 24.19.0，npm 11.17.0；依赖沿用锁文件，无新增项目依赖。

| 命令（仓库根目录，另注明者除外） | 实际结果 |
| --- | --- |
| `make frontend-install` | 退出 0，192 个包，审计无已知漏洞。 |
| `make backend-format backend-vet backend-test` | 退出 0，全后端测试通过，包含真实迁移与既有集成测试。 |
| `go test -mod=readonly ./internal/httpapi -run '^TestTranslation(AdminConfiguration\|Configuration\|Trial)' -count=1 -v -timeout=2m`（backend） | 退出 0，8 个顶层 H/R 用例通过。 |
| 上述 H/R 选择加 `-race`、`CGO_ENABLED=1` | 退出 0，未报告竞态。 |
| `make backend-lint backend-build` | 退出 0，lint 零问题，构建成功；共享运行锁释放后完成。 |
| `make -o frontend-install frontend-lint frontend-coverage frontend-i18n` | 退出 0；lint 与 E2E 类型检查通过；Vitest 153 个文件、867 项通过；UI 文案检查通过。 |
| `make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='translation-settings.spec.ts --project=desktop-chromium --workers=1'` | 已设置专属 Vite 和后端产物环境，退出 0，2/2 通过；最后一次 6.5 秒。 |
| `make -o frontend-install frontend-format frontend-docs frontend-build` | 各目标实际通过，格式、121 份公开 Markdown 链接、5 种语言的 11 类用户页及 TypeScript/Vite 构建通过。 |
| `make sensitive-check`、`git diff --check` | 退出 0。公开资料允许清单的两个已有条目只增加开发文档归属。 |

F 的可复验准备和环境名见 [开发文档](../../../docs/development/translation.md#verification)。所选 Chromium 为 Playwright 1.63.0 对应的 Chromium 153。既有 Vite 配置兼容性与大分块警告仍存在，构建退出 0；本票没有变更打包策略。

## LIVE 结果

这是经本票新客户端发出的独立生成请求，不采用先前网关健康或 curl 结果作为替代。

- 请求调用名：`Index-Translate-35B-A3B`；模板版本：`index-user-v1`。
- 输入：`今日はいい天気です。`；目标：简体中文。
- 本票 API HTTP 200，完整译文：`今天天气很好。`；客户端记录 1753 毫秒。
- Go LIVE 测试退出 0，一次生成、无重试。TLS 验证开启；凭据和 URL 未进入源码、参数、输出或此证据。隔离测试库随测试关闭清理。
- 该样例只证明短文本链路与相应语义；不宣称长文、所有语言或长期服务可用性。

## 接续与清理

01 验收全部完成。`internal/translation` 的 Store、Config、Input、Result 和 Translate 可供 02 复用；`httpapi.newTranslationClient` 负责精确 origin 与 Other 代理路由。试译的单并发限制在 HTTP 入口，Translate 本身不拥有全局队列；02 必须把自动内容调用与试译协调到自己的已验收资源预算，不能另造绕过策略的客户端。

本票复用 `app_setting`，没有增加迁移或占用 069；未实现 02 缓存、04 个人评价或生产部署。完整规格的后续验收保持在所属工单。

F 自有后端和模型服务由测试收尾关闭；本票 Vite 后台进程已停止，专属前后端监听端口均实际确认关闭。保留可复用依赖、构建产物和源码，没有遗留本票运行服务或临时凭据文件。

## HEAD LIVE 复验（2026-10-21）

在服务器用 Go 1.26.9 与生产模型端点（`Index-Translate-35B-A3B`）运行 `TestTranslationLiveConfiguredService`：

- 模板 `index-user-v1`，耗时 526 ms，输出“今天天气很好。”，响应不含密钥或端点。
- 测试新增 `KIKOTO_TRANSLATION_LIVE_ALLOW_LOOPBACK=1` 显式开关，仅允许 127.0.0.1/localhost 的 HTTP；默认仍要求 HTTPS。
- 该用例对应当前 HEAD 的客户端与输出预算，不再是旧提交的沿用证据。日志保存在仓库外 `.kikoto-work/live-test-head.log`。
