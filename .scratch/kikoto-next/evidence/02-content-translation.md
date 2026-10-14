# 02 内容翻译与有界缓存验收

**Implementation:** resolved

本票生产代码为 `c32d551`，合入 integration `e5dc48e` 后的验收提交为
`8b6bb647b528f0757d783b7fb166edc0a0ad0573`。后续只补强个人标签/备注的
HTTP 断言及本证据、工单状态，不改变生产代码。原作者交接的检查结果只作为
背景；下列结果均由本次收尾重新执行和读取。

## 六项清单与证据

| 本票清单 | AC | 当前证据 |
| --- | --- | --- |
| 两用户独立保存显示语言与界面语言，共用配置；对象身份保持 | AC-03 | `TestContentTranslationCacheAndIndependentPreferencesSurviveRestart` 真正关闭 Server、HTTP host、SQLite 后重开同库，复核两用户语言、UI locale、个人评分备注；F 第三例覆盖分页、重载、换账户与原账户偏好。四字段 H 由公共详情确认 id/title/description 不变。 |
| 已有目标字段优先，四类和动态内容一致 | AC-03 | 标题名称保护/手写字段 H、Traditional 手写标题 H、系统标签多语言 H、远端标签 H、description/tag/directory H；F 第一例贯通真实 API 的卡片详情与动态目录，第三例翻页。 |
| 名称和私人文字不外发，原文与纯文本可读 | AC-04 | `ReadsOnlyPublicWorkFields` 先用公开 API 保存个人备注与标签，再验证伪装为公共字段的请求被拒绝，上游仅收到公共标题，最后由详情回读私人原文；本地和远端名称 H 检查模型请求无社团/声优，恢复名称；F 原文切换及字面 `<script>` 无 script 节点。 |
| 等待、失败、重试与旧结果隔离 | AC-05 | 超时/失败 H 保留原文且不自动重试；F 第二例覆盖 pending、限流、显式重试、换语言取消和离页读者释放，第三例覆盖会话与分页。 |
| 缓存身份、权限、去重与失效 | AC-06 | `CacheInvalidatesAndHonorsDiskBudget`、远端关闭后缓存不可读、删除作品后缓存不可读；共享队列 H 验证去重和最后读者取消；`TestCacheIdentitySeparatesContentLanguageModelAndTemplate` 验证原文、目标、模型、模板等分离。 |
| 可调预算、淘汰、低空间与恢复 | AC-06、AC-25 的翻译部分 | 默认值/非法配置/超时 H，共享 trial 队列 H，双并发及在途降低预算 H，4096 字节缓存淘汰 H；冷启动先低盘暂停，再恢复写入，真重启命中；个人记录经公开读取保持。 |

共同 AC-27：当前组合下重新执行配置 URL、重定向禁止转发凭据、DNS 固定、
地址策略、超大响应、取消、服务错误分类等 HTTP 测试。自动翻译使用同一
`newTranslationClient` 与 `translation.Translate`，没有另一条出站实现。
共同 AC-28：仅运行下述三个 Playwright 纯功能用例，测试体明确关闭
screenshot、trace、video，没有像素、布局或人工视觉验收。

## 红绿与定向验证

- Traditional 已有标题：`TestContentTranslationReusesAuthoredTraditionalTitleWithoutModel`
  先失败（HTTP 400，退出 1，任务 `j-vomi3s`）；允许先复用已有字段后通过
  （退出 0，`j-fvjrnu`）。缺失 Traditional 字段仍无模型调用，转换归 03。
- 系统标签原文：`TestContentTranslationReturnsOriginalTagWithEarlierTargetName`
  先失败（返回英文译名作为 original，退出 1，`j-f5gm6t`）；复用既有
  `metadatatags.LocalizedNames(..., origin)` 规则后两项一起通过
  （退出 0，`j-y2vtch`）。
- 冷缓存：加强既有 R，验证尚未建缓存目录时低空间状态、恢复存储及真重启
  命中（退出 0，`j-hs14qs`）。现有跨平台 `nearestExistingPath` 已支持此行为，
  没有为该项修改缓存实现。
- 私人字段：`TestContentTranslationReadsOnlyPublicWorkFields` 补实际个人标签
  与备注的写入、拒绝外发和读取保持；`go test -race ./internal/httpapi
  -run '^TestContentTranslationReadsOnlyPublicWorkFields$' -count=1 -v -timeout=3m`
  退出 0（`j-mueyok`）。这是加强证据，未发生生产修复。

## 当前组合检查

运行时为项目专用 Go 1.26.9、Node 24.19.0；每进程 PATH，`GOTOOLCHAIN=local`。
前端沿用已安装依赖。全部命令从本票 worktree 或其 backend 目录运行。

| 实际命令 | 结果与任务 |
| --- | --- |
| `make backend-test backend-vet` | 退出 0，`j-ae6wci`；httpapi 当前代码执行 82.209s，其他未改包允许 Go 内容寻址缓存。 |
| `make backend-format` | 退出 0，`j-pi9yfu` 首阶段。 |
| `make backend-lint backend-build GOLANGCI_LINT_BASE=15ee7e2` | 退出 0，`j-0yoiru`，0 issues；比较基线包含整票改动。首次未设代理的 lint 工具元信息请求超时，`j-pi9yfu` 最终退出 2；仅重试进程加既有代理后成功，未改全局配置。 |
| `go test -race ./internal/httpapi ./internal/translation -run '^Test(ContentTranslation\|Translation(Configuration\|Trial\|Admin)\|CacheIdentity\|ProtectedNames\|WorkReference)' -count=1 -v -timeout=5m` | 退出 0，`j-520041`；12 项内容 H/R、01 的配置与出站测试、06 的引用/升级重启测试及 2 项必要 U；无 race 报告。表内反斜线仅转义 Markdown 管道。 |
| `make -o frontend-install frontend-format frontend-lint frontend-i18n frontend-build frontend-coverage frontend-docs` | 退出 0，`j-l16wp5`；格式通过，0 lint 警告/错误，E2E 类型检查通过，构建通过，153 文件/867 测试通过，121 公开 Markdown 相对链接通过，5 locale/11 用户文档检查通过。 |
| `make -o frontend-playwright-install frontend-e2e E2E_ARGS='content-translation.spec.ts --project=desktop-chromium --workers=1'` | 退出 0，`j-zus58e`；3/3 通过，10.2s。当前源码 `go run ./tests/content-translation-fixture` 与当前 Vite 联通，仅外部模型使用桩。 |
| `make sensitive-check`、`git diff --check`、`git diff --cached --check` | 生产提交前及 integration 合并提交前均退出 0；分别扫描 51、16 个变更文件。首次扫描误报两处名为 token 的名称占位符局部变量，改名 placeholder 后通过；未添加忽略规则或扫描豁免。 |

## 缓存与后续接入边界

默认并发 1、待处理 32、超时 60 秒、缓存 256 MiB、低于 2 GiB 暂停新增缓存。
缓存独立存放于 `CacheRoot/translations`；按每个 JSON 文件长度向上取整 4 KiB
计费并限制 8192 条，不声称是所有文件系统的精确分配量。不修改业务数据库
结构，无新增迁移；069 由 07 使用。

`catalogEntryId` 仅预留并明确返回 404。本票没有复制 07 的未提交实现，
没有执行或声称通过 OpenList catalog 翻译联测。07 负责将已批准 loader 接入
`loadTranslationField`，在进入缓存前复验来源 enabled/type，以条目 Title
或真实 Path 对应文件 Title 返回公共字段，且读取不登记 work。
前端接入使用 `ContentTranslationScope` / `ContentResourceScope` 与
`TranslatedText`，接口见 [翻译服务](../../../docs/development/translation.md#public-content)。

缺失 Traditional 字段的简体生成与转换属于 03；播放并行扫描/翻译组合负载
属于 17。本票未运行新的 LIVE、真实媒体解码、Docker/Linux 部署或浏览器视觉
验收。01 的既有 LIVE 不作为本次重新执行的证据。

## 任务与清理

- 接管旧检查：`j-u1vuyx`、`j-4lasxc` 已读取终态，均退出 0；未冒充本次重跑。
- 旧服务 `j-6nor4f`、`j-aq990x` 已停止（killed）。
- 本次夹具 `j-fs9nrc` 通过 `/__fixture/stop` 正常退出 0，执行自身临时目录清理；
  Vite `j-ylwlzd` 已停止（killed）。本次全部检查任务均取得终态，无运行服务。
- 两份原作者早期合成缓存仍是未跟踪文件，位于
  `backend/internal/httpapi/translations/`，文件名分别为
  `3fac5fb5467a569b389d81653dc4174103d1ff52396439f4c79c4d90e2c873eb.json` 与
  `a2ef3576a843ba8e9385ab510bb308cb93deccd72fdea9452bf9876b6e529fbc.json`。
  已读取并确认仅含合成译文。清理调用在执行前被工具自动策略拒绝，原始理由
  `blocked by policy`。主代理明确要求保留原处、显式排除暂存，不换工具删除，
  不新增忽略规则。工作区因此不能称为完全干净；新测试都用独立临时 CacheRoot。
