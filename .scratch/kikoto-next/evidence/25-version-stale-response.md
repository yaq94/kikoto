# 25：仍有订阅的旧版本响应

本证据对应同提交的 `translation-version.spec.ts`。真实隔离前后端生成 A 结果并观察 B 配置，随后在 HTTP 响应边界把已录下的 A 标题结果投递给仍在等待的 B 请求，仅替换该请求的关联 key。原文、译文和版本均来自实际 A 响应，业务逻辑不使用桩。

测试等待旧结果触发的新版本读取，证明它已被组件消费；检查原文仍可读、A 不进入 IndexedDB、后续请求不使用 A scope。随后重新进入详情，B 标题正常显示并写入缓存。旧测试在导航后释放已取消订阅的响应，无法保证到达版本判断；本测试覆盖该遗漏。

原始日志在仓库外 `reports/20261107-remediation/`：

- `node scripts/run-e2e-fixtures.mjs --group=translation-version`：`version-active-response-green.log`，退出 0，三项通过，9.1 秒。
- 在独立工作树临时将版本通知改为直接采用返回版本后，同命令退出 1，前两项通过，第三项在消费旧结果后的版本刷新断言失败，`version-active-response-mutant.log`。没有增加超时、重试或弱化断言。
- 临时改动完整还原，`git diff --exit-code -- frontend/src/components/content-translation/ContentTranslation.tsx` 为 0；再次运行上述命令退出 0，三项通过，7.0 秒，`version-active-response-restored.log`。
- `npm run typecheck:e2e`、该文件的 oxlint 与 prettier 检查通过。项目已有依赖在独立工作树通过 `npm ci` 安装，审计为零漏洞。

截图、trace、video 均关闭；本证据不包含视觉门禁或生产调用。最终提交仅改变测试及本文，产品版本防护保持。
