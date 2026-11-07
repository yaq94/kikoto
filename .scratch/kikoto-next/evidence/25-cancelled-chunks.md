# 25：取消排队翻译分块

## 版本与范围

- 基点：`codex/kikoto-catalog-remediation`，`72d52434075c1724c5f3fd97f14b5b098826d546`。
- 被测代码提交：`d7fc68cf19a1dbaf6af6da545e294621cb6c8b91`；工作分支：`codex/kikoto-cancelled-chunks`。
- 仅覆盖独立复核 F2：可见字段的排队分块失去全部读者后仍发送请求。依据规格 D-10、AC-06、AC-25 及 25 号票的取消、并发和资源释放要求。
- 按 implement-spec 与 TDD 方法，在既有 U 入口调用 `translateCoalesced`、`contentTranslationApi.translateBatch`，使用真实调度和取消逻辑，仅替换网络传输边界；数据全部为合成标题和来源索引条目 ID。

## 行为与回归证据

分块取得执行机会时先清除无读者项，空分块直接结算。等待会话共享传输槽后，再逐项读取当前读者集合，空请求不发送并释放槽。响应只校验实际发送的键，取消项不会令仍有效的同语言或其他语言请求误报缺项。成功、失败、取消及旧 entry 身份保护沿用现有结算路径；公开 batch API 的参数保持不变，全局并发上限仍为 2。

原始日志位于仓库外 `reports/20261107-remediation/`，本页日志名均相对此目录。

1. `cancelled-chunks-red-dispatch.log`：先新增 33 项、16/16/1 分块的公共 API 回归，再运行旧实现，exit 1。前两请求挂起时取消第 33 项，释放首请求后仍发送 `title 232`；所有模拟请求释放后才断言失败。
2. `cancelled-chunks-green-dispatch.log`：只补分块派发前筛选后，该回归通过，exit 0。
3. `cancelled-chunks-red-slot.log`：继续新增等待共享槽期间取消的回归，exit 1。两条直接 batch 占满槽后排入 17 个合并字段；取消前 16 项再释放一槽，错误发送 `title 300` 至 `title 315`，有效的 `title 316` 仍等待。
4. `cancelled-chunks-green-slot.log`：加入取得共享槽后的读者复核，API 与排序共 20 个用例通过，exit 0。
5. `cancelled-chunks-unit-final.log`：最终 30 个用例通过，exit 0（API 21、排序 2、预取 7）。新增 5 个场景覆盖两处取消时点、空槽向后传递、取消项重试、混合语言部分取消，以及 flush 后在取消之前或之后加入的重复读者。既有迟到读者、成功/失败后 waiter 清理、错误传播与跨 flush 并发用例全部保留。

补充读者场景的首次组合运行在 `cancelled-chunks-unit.log` 记录一次测试夹具的 fake timer 清理遗漏；最终测试在恢复真实时钟前耗尽自身定时器，并取得上述 30/30 结果。该中间日志不作为通过证据。

## 验证命令

Node 使用工作区指定的 24.19.0，FastCtx 命令采用绝对工作目录与 `login_shell:false`。已先通过 `make frontend-install` 安装锁定依赖，后续 Make 目标使用 `-o frontend-install` 复用当前安装。

| 命令（仓库根目录；单测和格式检查在 frontend） | 结果 | 日志 |
| --- | --- | --- |
| `node node_modules/vitest/vitest.mjs run src/lib/contentTranslationApi.test.ts src/lib/contentTranslationOrder.test.ts src/lib/contentTranslationPrefetch.test.ts` | exit 0；30 passed | `cancelled-chunks-unit-final.log` |
| `make -o frontend-install frontend-lint` | exit 0；oxlint 0 warnings / 0 errors，E2E TypeScript 通过 | `cancelled-chunks-lint.log` |
| `make -o frontend-install frontend-build` | exit 0；应用 `tsc -b` 与 Vite 构建通过，2486 模块 | `cancelled-chunks-build.log` |
| `node node_modules/prettier/bin/prettier.cjs --check src/lib/contentTranslationApi.ts src/lib/contentTranslationApi.test.ts` | exit 0；两文件格式通过 | `cancelled-chunks-format.log` |
| `make sensitive-check`（代码提交前） | exit 0；2 changed files | `cancelled-chunks-sensitive-code.log` |
| `make tracked-evidence-check sensitive-check`（证据提交前） | exit 0；证据与敏感信息扫描通过 | `cancelled-chunks-sensitive-evidence.log` |

## 交付限制

没有 UI 结构变化，未执行浏览器功能、截图或视觉验收，没有生产、真实来源、真实模型或部署调用。本页不关闭 25 号票剩余资源整改，也不代替同候选集成 CI。安装日志提示 3 个 moderate 依赖问题，锁文件未修改，未在本项中做依赖升级；Vite 的 native config loader 和大于 500 kB chunk 提示保留于构建日志。

唯一后台任务是依赖安装，已确认 exit 0。未启动服务；保留托管工作树供主代理集成和后续归档，不清理其他工作者产物。
