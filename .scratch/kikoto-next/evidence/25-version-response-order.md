# 25：浏览器旧模型响应的可控交付

Full CI `42498479864` 在 `f33e51be` 的翻译版本场景报告 `route.fulfill: Route is already handled!`；其他内容翻译8项、简繁2项和原生缓存5项通过。旧测试等待已取消的网络拦截请求，再尝试 fulfill，无法稳定证明旧响应交付后的状态。

场景改在浏览器 fetch 边界等待一条已经真实收到并完整读取的旧标题响应。其余响应正常推进，保留两个传输槽位的合同；旧标题仍在新模型版本和 IndexedDB 行确认后才交付。继续断言旧响应确实释放、持久缓存只含新版本、标题保留新译文及版本不回退。没有新增 retry、固定延时、超时或忽略异常；截图、trace、video仍关闭。

验收命令：`node scripts/run-e2e-fixtures.mjs --group=translation-version --log-dir=...`，三个版本测试全部通过，退出0。日志在仓库外 `reports/20261107-remediation/translation-version-single-response.log`。`npm run typecheck:e2e`、选定文件 oxlint 与 prettier 检查均退出0。早期试验同时滞留多条完整响应会占用全部传输槽位，因此最终只延迟目标标题响应。

该测试使用真实前端和本地真实后端配置、版本及翻译路由；模型边界为既有合成 fixture。本证据不替代最终候选的全部 F 或真实模型调用。
