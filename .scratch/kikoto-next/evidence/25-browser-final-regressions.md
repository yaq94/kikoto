# 25：翻译浏览器最终回归

## 版本与范围

- 基点：`8dfd76269f482a88e2bcd75ba8efb3aa5047d2e3`。
- 被测代码：`ee02c1e6966b8e4f6f8d8618cc03b7e9373f651c`，分支 `codex/kikoto-cancelled-chunks`；本证据提交只新增本页。
- 依据现行规格 AC-03、AC-05、AC-06、AC-28 和 25 号整改票，修复重复访问缓存竞态及迟到旧版本浏览器测试的路由生命周期。
- 使用任务指定的 TDD 技能及完成前验证方法；已确认的边界是公开翻译 API、真实浏览器 IndexedDB、文本、用户操作和请求行为。没有截图、trace、video、像素或布局验收。
- 环境：Windows、Node 24.19.0、npm 11.17.0、Go 1.26.10、Playwright 1.63.0/Chromium 153；复用工作树现有锁定依赖和浏览器，不安装新依赖。独立夹具端口基数为 20700，数据、账户及模型响应全部为合成值。

## 根因与修复

1. `TranslatedText` 在偏好先于 `/api/content-translations/version` 返回时，因 `cacheVersion` 为空跳过持久缓存读取，却直接发送生成请求。缓存行即使已经落盘，重新打开页面仍可能产生多余请求。字段现等待权威配置版本，再检查缓存与生成；等待沿用 20 秒状态边界，版本读取失败后保留原文并允许用户显式重新读取版本。
2. 首访标题完成与整页缓存完成是不同条件：各字段分批响应，显示结果也不等待异步 IDB 写入。缓存复访用例先确认标题、简介、目录及音轨已持久化，再重新打开。额外控制版本响应迟到，检查等待期间与完整复访后均无新增生成请求，保留原请求数合同。
3. 旧版本测试的 `route.fetch()` 回调可能在标题完成后仍处理其它字段；测试退出销毁 APIRequestContext，导致 `Response has been disposed` 或 `route.fetch: Test ended`。用例先确认 B 版本标题持久化，再释放持有的 A 响应，等待全部路由及响应监听器结束，然后检查仍为 B。删除 `fulfill` 的无条件吞错，并释放每个 APIResponse；失败路径也释放门闩并等待回调结束。

## 红绿证据

原始日志位于仓库外 `reports/20261107-remediation/`，下列日志名均相对此目录。命令中的 `REPORTS` 表示该外部目录，不包含运营环境信息。

| 阶段 | 实际结果 | 日志 |
| --- | --- | --- |
| 未修改基点运行完整翻译夹具组 | exit 2；缓存复访本次通过，旧版本测试在断言结束后出现 `route.fetch: Test ended`，故整组失败 | `browser-final-baseline.log` |
| 只增强缓存前置屏障及延迟版本的回归，产品代码仍为基点 | exit 1；四字段已持久化，版本仍未返回时生成请求数从 3 增至 5；原合同断言失败 | `browser-final-version-red.log` |
| 增加版本就绪约束后运行复访回归 | exit 0；1 passed | `browser-final-cache-green.log` |
| 加入版本读取失败后的显式重试回归，尚未接通版本重读 | exit 1；预期两次版本读取，实际一次；20 秒边界与零生成断言已通过 | `browser-final-retry-red.log` |
| 最终完整翻译夹具组 | exit 0；18 passed：内容翻译 8、简繁 2、配置版本 3、原生 IDB 5；0 skipped，0 retries | `browser-final-green.log` |

基点和最终组使用相同入口，日志目录后缀分别为 `baseline`、`green`：

```sh
GOTOOLCHAIN=go1.26.10 KIKOTO_E2E_FIXTURE_PORT_BASE=20700 E2E_NO_ARTIFACTS=1 \
  make -o frontend-playwright-install frontend-e2e-fixtures \
  E2E_FIXTURE_ARGS="--group=content-translation --log-dir=$REPORTS/browser-final-green"
```

红测和单用例绿测直接导入同一个 `scripts/run-e2e-fixtures.mjs` 的 `main/groups`，通过运行时 `group.grep` 选择正在诊断的用例，未修改 runner、断言门槛、超时或重试配置。`browser-final-version-red` 保留翻译 bundle 的四组执行；`browser-final-cache-green` 和 `browser-final-retry-red` 只运行对应诊断用例。最终验收执行未过滤的完整 bundle。

## 其它验证

| 命令 | 结果 | 日志 |
| --- | --- | --- |
| `npm run test:unit -- src/components/content-translation/ContentTranslation.test.tsx src/lib/contentTranslationApi.test.ts src/lib/contentTranslationCache.test.ts src/lib/contentTranslationCacheFailure.test.ts src/lib/contentTranslationPrefetch.test.ts src/lib/contentTranslationOrder.test.ts`（frontend） | exit 0；6 files、60 tests passed | `browser-final-unit.log` |
| `make -o frontend-install frontend-build frontend-format frontend-lint frontend-i18n` | exit 0；应用与 E2E 类型检查、2486 模块构建、全前端格式、lint、UI 文案检查通过 | `browser-final-checks.log` |
| `make sensitive-check`（代码提交前） | exit 0；3 changed files | `browser-final-sensitive-code.log` |
| `make tracked-evidence-check sensitive-check`（本页提交前） | exit 0；83 evidence files、1 changed file | `browser-final-sensitive-evidence.log` |
| 对本任务 8 个夹具/前端端口逐一执行 Node TCP 连接检查 | exit 0；全部 `ECONNREFUSED`，服务已退出 | `browser-final-cleanup.log` |

`-o frontend-install` 与 `-o frontend-playwright-install` 只复用已经安装的锁定工具，不跳过任何行为检查。构建日志保留已有 Vite native config loader 和大于 500 kB chunk 提示。检查本任务日志目录，未生成 PNG、WebM 或 trace.zip。

## 交付边界

本批未修改后端、迁移、CI、Makefile、共享规格或工单状态。自启 runner、单测、构建及其服务全部终态；未访问生产、真实来源或真实模型，也未推送。上述证据只覆盖本批差异，完整候选 CI、组合性能、恢复演练与部署仍由主代理汇合验收，不能据此关闭 25 号票。
