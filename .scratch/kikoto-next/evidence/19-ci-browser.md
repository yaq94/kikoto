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
