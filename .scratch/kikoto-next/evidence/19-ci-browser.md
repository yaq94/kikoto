# 19 浏览器可靠性证据

实施基线：`f0fd5d77be1072f260fc817245c01d1b6334ea0f`。独立分支 `codex/kikoto-ci-frontend`。既有 API 截获 Playwright 边界，保留尺寸断言；不新增视觉验收。

## 原始证据

- `9949175e53979bcd590c75a624bc0efc67684da3` 的 Full run `38620121460`，attempt 1，结论 failure。实际下载并读取 artifact `11921009551`（E2E 3）及 `11921133354`（E2E 4）的 trace。
- 骨架：可见断言在相对 5363.2–6134.2 ms 执行，随后 bounding box 在 6136.9–6514.9 ms 返回 null，原第 238 行失败。
- 收藏：布局、筛选、刷新、列表编辑和删除先后成功，最后 Circles 点击耗尽共享 30000 ms 测试预算。具体时序保留在外部诊断工件。
- 本轮未修改的四条定向测试单次均通过：4 passed / 19.0 s。偶发问题不能由单次成功判定消失。
- Node 24.19.0 / npm 11.17.0；独立工作树 `make frontend-install` 成功，192 packages，0 vulnerabilities。浏览器只访问本任务独立 Vite 服务，未复用别票依赖或服务。

## 1. 目录骨架

根因：固定 800 ms 媒体延迟与异步可见／尺寸读取没有因果同步；`onMediaRequest` 的 Promise 未被等待。

实现：hook 接受 `void | Promise<void>` 并 await。测试观察媒体请求到达，持有闸门直到原可见、非空尺寸、最小高度 352 和无冗余 loading 文字断言完成；finally 释放后确认原音轨与骨架隐藏。

验证：

- 将测试换成闸门但保留旧 fixture：1 failed，`directory-skeleton` 不存在（旧 hook 未 await，响应提前返回）；命令退出 2。
- 修复 fixture 后，骨架与同步 hook 计数用例 `--repeat-each=5 --workers=2 --retries=0`：10 passed / 15.1 s，退出 0。

- `make -o frontend-install frontend-lint sensitive-check`：lint、E2E TypeScript 与隐私扫描均通过，退出 0。
- 改动的两份测试文件 Prettier 使用仓库的 `--end-of-line auto` 通过；默认 LF 检查在 Windows CRLF 文件上曾报告格式警告，未修改行尾规范。

定向通过不代表完整 Full/Gate；最后由集成流程对最终 SHA 验收。

## 2. 收藏独立行为

原 trace 中，首次加载与布局断言耗时约 6.5 s，筛选和选择约 3.8 s，折叠／刷新恢复至 16.3 s；编辑列表至 22.8 s、新建至 26.2 s、取消删除及焦点恢复至 28.1 s、确认删除触及 30 s 边界，Circles 点击随后被已耗尽的测试预算终止。没有一个单独交互停滞 30 s 的证据。

实现：原综合测试拆成布局与 sticky rail、资源筛选与选择、标签名刷新持久化、重命名、新建、取消删除、确认删除、关闭编辑器后导航共 8 项。每项独立 mock、页面和 viewport；删除从明确初始列表开始。原尺寸、菜单内容、PATCH 正文、图标持久化、单层对话框、新建位置、取消删除焦点、删除后状态及 Circles 导航断言全部保留。

验证：

- 整个 `favorites-state`：15 passed / 29.9 s，退出 0。
- 拆分的 8 个 desktop 用例，2 workers、repeat-each 3、retries 0：24 passed / 42.7 s，退出 0；单项 2.1–4.2 s。
- `make -o frontend-install frontend-lint sensitive-check`：lint、E2E 类型与隐私扫描通过，退出 0；目标文件按仓库行尾规则格式化。
- 一次前序批次因本地 Vite 服务意外中断全部在 `page.goto` 报连接拒绝；FastCtx 记录 interrupted、无退出记录。重启该工作树自身服务后重新执行，未把此环境失败计为产品红例。
