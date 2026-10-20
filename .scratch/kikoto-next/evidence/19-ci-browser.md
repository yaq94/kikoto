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

## 3. 移动封面触摸

历史证据边界：按原 SHA 查得 3 个 workflow run，只有一个 Full、attempt 1；E2E 1/4 和 2/4 为 success，3/4 和 4/4 为 failure。仅有 3/4、4/4 浏览器工件，目录中没有移动封面失败 trace。本次诊断不冒充那次 CI 的失败记录。

原逻辑在正常环境 2 workers × 8 及 4 workers × 6 共 28 条通过。采用临时 Chromium 6 倍 CPU slowdown 后，同样的两条测试 × 3 出现 1 failed / 5 passed：失败的两个 click 均为 trusted touch、坐标 `[206, 304]`，事件时间戳相隔 476.3 ms、处理时相隔 508.8 ms、detail 为 `[1, 1]`，URL 保持 `/` 且 full player 仍打开。测试中 100 ms 等待不包含 locator 的自动等待与调度开销，实际未生成产品既有 360 ms 窗口内的双击。

实现：保留 FullPlayer。测试先完成封面的可见、启用与稳定滚动等待，再在同一坐标连续执行两次 `page.touchscreen.tap`。记录并断言两次真实 trusted touch、时间与位置满足既有阈值；保留原收起、目标 URL、目录内容、来源 Circles 恢复，并验证 history 恰好增加一次。独立单击测试确认 URL、history 和 full player 保持。没有提高超时或新增 retry。

验证：

- 相同慢 CPU 条件的 6 次双击均通过，click 间隔 57.0–132.0 ms；连同 3 次正常单击为 9 passed / 27.9 s。
- 单击单独在慢 CPU 下重复 3 次：3 passed / 6.7 s。
- 移除临时 CPU slowdown 后，3 个触摸用例 × 3：9 passed / 13.5 s，退出 0。
- 中间曾用 `tap({ trial: true })` 等待稳定；浏览器 tap 计数受该动作影响，6 条双击只观察到一个 click 就导航，因此该尝试已移除，最终使用不发送输入的等待方式。
- `make -o frontend-install frontend-format frontend-lint frontend-build` 全部通过，退出 0；现有 playingWorkNavigation 与 queueDrag 单测为 2 files / 8 passed。
- 三文件完整回归：65 passed / 4 failed。4 个额外失败均在原 work-detail 错误状态断言，不属于上述触摸、骨架或收藏用例；另项诊断处理。

## 4. 集成后的旧 fixture 契约

完整回归暴露 `catalog-directories` API 被旧 `mockApplication` 返回 404 `Not mocked`，多出一个目录错误区。原 database contention 用例因此命中两个错误区，三个 pending edition 用例原本应无错误却命中一个。临时还原到本票前的同步 `onMediaRequest` 后，4 条仍全部失败（退出 2），证明不由本票 await 引起。所有原错误数量与状态断言保留。fixture 为本地作品返回类型化的 `{ directories: [] }`；没有修改产品错误呈现或掩盖真实 503。

- 原 4 项 × 2，2 workers、retries 0：8 passed / 14.2 s，退出 0。
- `make -o frontend-install frontend-format frontend-lint sensitive-check`：格式、lint、E2E 类型及隐私检查均通过，退出 0。
- 此项为主集成在发现四项失败后明确授权的同 fixture 契约收尾，与原三项问题分开提交。

## 合并复验

本票分支合入集成 `5341b8d623d6c0363f65765f128c8c14b246b645` 后，验证产物为 `3ad187aeca508e9e8969152d1db0a815c078072c`。

- 三个完整测试文件 `work-detail.spec player.spec favorites-state.spec --workers=2 --retries=0`：69 passed / 1.5 min，退出 0。
- `make -o frontend-install frontend-format frontend-lint frontend-build sensitive-check`：全部通过，退出 0；`-o` 仅复用本工作树已单独安装的依赖，避免测试服务运行期间重新 npm ci。
- 产品源码和 FullPlayer 没有本票修改。前端项目没有真实组件测试环境，本票通过实际浏览器交互及现有导航／队列纯规则单测验证，不以伪组件测试替代。
- 所有临时事件诊断和 CPU slowdown 已从测试文件移除；最终保留的 JSON 输入事件附件用于验证真实触摸，不是视觉验收。
- 完整 Full/Gate、发布审查和部署仍归主集成流程，尚未由本票执行或宣称通过。Vite 的 native-config 兼容性及大 bundle 提示为构建警告，构建退出 0。


## 布局断言与非视觉边界裁决（2026-10-19）

裁决范围：`favorites-state.spec.ts` 的 `boundingBox()` 断言（工具栏相对位置、sticky rail、44 px 触摸目标、对话框不超出视口），以及全仓既有套件中同类的元素矩形断言。

- **保留**：这些断言留在既有文件里，作为可用性回归保护。它们读取 DOM 矩形并比较数值，不采集截图、不做像素比对、不要求人工视觉签收；本期运行设置 `E2E_NO_ARTIFACTS=1`，关闭 trace、screenshot 与 video。
- **不作为本期证据**：它们不计入 AC-26、AC-28 的验收证据。工单 19 第 2 项原文“保留布局断言”指不删除既有断言，不等于把布局纳入本期验收范围；AC-28 明确禁止把布局作为门禁。
- **不新增**：本票不新增任何几何或视觉断言，也不修改全仓既有视觉测试的定义（AC-28 末句）。

这样工单 19 与 AC-28 不再冲突：既有断言按原样保留，本期非视觉验收只引用文本、操作、请求、持久状态与媒体功能断言。
