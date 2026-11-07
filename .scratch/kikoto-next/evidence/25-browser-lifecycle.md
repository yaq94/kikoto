# 25：浏览器请求和持久缓存生命周期整改

## 版本与边界

本批从 `35419ed761e7a95f7c41d7dcdbc1c144f0c6ea62` 继续，针对 `edab56b4` 独立复审的 R1–R4。使用 Node 24.19.0、Vitest 与 Chromium 原生 IndexedDB；浏览器只执行功能验证，截图、trace、video 关闭。没有访问生产、真实来源或模型，也没有启动后端 fixture。本页不把缓存模块的浏览器测试当作完整前后端翻译验收。

原始输出保留在仓库外 `reports/20261107-remediation/`，下列日志名均相对此目录。

## 请求取消所有权

flush 前后加入的读者都注册到同一个 coalesced entry。成功或失败结算后清空 waiter；最后一个活跃读者离开时，先移除本组 inflight entry，再取消 transport，避免新读者加入已取消请求。删除 map 时比对 entry 身份，迟到旧结果不能删除后来创建的同键请求。跨 flush 共享并发上限仍为 2。

- `readers-red.log`：公共 API 三个新用例在旧实现失败，exit 1。flush 后的第二读者被第一读者误取消；首 chunk 成功或失败结算后，取消其余读者仍未 abort 两个剩余 transport。
- `readers-idb-green.log`：API 16、排序 2、缓存 18、异常生命周期 3，共 39 个 U 用例通过，exit 0。恢复后的新请求可以取得并发 slot。

## IndexedDB 异常释放

transaction completion Promise 创建后立即观察拒绝，仍返回原 Promise 供正常路径 await；早期 request 失败不会留下未处理的 transaction rejection。禁用持久化时关闭已打开连接并停止待调度维护；blocked 后迟到的 onsuccess 也立即关闭连接。内存缓存仍可用。

- `idb-errors-red.log`：旧实现 3 个异常用例失败，close 计数均为 0，且出现一个未处理 AbortError，exit 1。
- 上述 39 个 U 组合覆盖早期 read/transaction abort、quota abort 和 blocked 后迟到连接。
- `idb-native-review.log`：5 个 Chromium F 用例通过，exit 0，4.4 s。新增原生 transaction abort、真实 blocked 升级后迟到 success、同 origin 两个独立 tab 的并发迁移/写入/触碰/淘汰。pageerror 无未处理拒绝，禁用和迟到连接各关闭一次。
- 多 tab 用例从 v1 的 300 行迁移，两 tab 各执行 70 次 put/touch，包含同 key 更新和同时 prune。用同一只读事务比对正文、metadata 和 budget：count 与编码字节数精确相等，最终不超过 50 行/12,000 字节。原先“不 getAll 正文”、128 行迁移批次与 LRU/隔离断言保留。

## 静态检查与未完成范围

修改文件 Prettier、应用与 E2E TypeScript 检查、oxlint 均通过。生产前端构建 exit 0，见 `review-frontend-build.log`；Vite 的既有 native config loader 提示仍存在。没有视觉验收，也没有用 root 对旧基点的 14F 结果替代本批结果。

25 保持 `in-progress`：目录同步逐页/有界可恢复应用、覆盖检查、写后保留资源整改以及同候选集成、发布和生产验证仍未由本页关闭。
