# 24 候选账本：15b84ed2

> 当前部署候选。接在 `c7d793e4` 之后，新增四个提交。仓库只写镜像 tag；digest、主机路径与备份位置留在受限运维记录。

## 1. 候选标识

| 项 | 值 |
| --- | --- |
| 候选 SHA | `15b84ed2` |
| 分支 | `codex/kikoto-catalog-remediation` |
| 镜像 tag | `kikoto:next-15b84ed2` |
| schema 版本 | `105`（部署前后一致，未新增迁移） |
| 前端主包 | `assets/index-DNjiWLrG.js` |
| 回滚目标 | `kikoto:next-907391f2`，快照 `rollbacks/pre-15b84ed2-20261027T232854Z` |
| 配置指纹 | model `Index-Translate-35B-A3B`，推理关闭，模板 `index-user-v1`（未改动） |

## 2. 提交清单

| 提交 | 说明 |
| --- | --- |
| `38965f64` | 未进入视口的字段不再声称“正在翻译”；观察器提前 400 px 触发 |
| `907391f2` | 目录索引测试夹具（该提交对节流的改动经审查确认是空操作，见第 7 节） |
| `c2057b7c` | 可见性闩锁按请求键作用域；已定结果优先于可见性；`visible` 改为必填 |
| `15b84ed2` | 目录 pending 用例改为直接构造状态，不再驱动 101 集拆分 |

## 3. 门禁结果

| 门禁 | 命令 | 结果 |
| --- | --- | --- |
| 后端构建 / 静态检查 | `go build ./...`、`go vet ./...` | 通过 |
| 后端整包 | `go test ./internal/httpapi -count=1` | 通过，569.955 s |
| race 复现 | `go test ./internal/httpapi -run TestCatalogEpisodePendingEntryIsHiddenFromReadSearchAndReview -race -count=1` | 通过，22.70 s（CI 原为 300.71 s 失败） |
| 前端类型检查 | `npx tsc -b` | 通过 |
| 前端 lint / 格式 | `npx oxlint src --deny-warnings`、`npm run format:check` | 0 warning / 0 error；格式通过 |
| 前端单测 | `npx vitest run` | 167 文件 / 968 用例通过 |
| 前端构建 | `npm run build` | 通过，`index-DNjiWLrG.js` |
| Full CI | run `40596212856` @ `907391f2` | success（26 job） |
| Full CI | run `40602372253` @ `15b84ed2` | 见第 6 节 |
| 双轴审查 | `git diff 3336e5c0...15b84ed2` | 见第 7 节 |

## 4. 部署与部署后验收

`deploy-kikoto.sh 15b84ed2 105`：快照 `snapshot_schema=(105, 66)`、`quick_check=ok`、`foreign_key_violations=0`；启动后 `schema_state=(105, 66)`、`migration_max=105`，未回滚。`docker ps` 为 `Up (healthy) kikoto:next-15b84ed2`，`/health` 返回 `{"status":"ok","version":"v0.8.3"}`。

浏览器（Playwright，登录态 cookie）：

| 验收项 | 结果 |
| --- | --- |
| `/voices/4` 首屏 | 180 节点：`existing` 60、`translated` 7、`original` 113（未进入视口，未发请求）；`pending` 0、`failed` 0；5 个 batch 请求全部 200 |
| `/voices/4` 滚动 8 屏后 | `existing` 159、`translated` 20、`native` 1，仍 0 pending / 0 failed；新增 10 个 batch 请求，无失败 |
| 主包 | `assets/index-DNjiWLrG.js` |
| 推荐基线 | asmr.one `baseline=35`，分布 35×1 / 40×6 / 45×4 / 49×12 / 54×1；23 个徽章全部 > 35 |
| 推荐真实信号 | work 4 入列表后 `favorite=true`、`35 → 45`、`favorite.contribution=10`；验收后已还原（列表删除、声优收藏取消、work 31/34 复位，`favorite` 行数为 0） |
| 索引折叠 | 两个 `<details>` 初始 `open=false`；展开后 1 张表 / 9 行 |
| 目录卡 | 入队 6 音轨并播放（`206` 媒体）；两张无编号卡分别登记 `work 31` 与 `work 34`，标记互不影响；全程无 `/download` |
| 服务不可用提示 | OpenList 页面无“此服务暂时不可用”、无“推荐徽章暂时不可用” |

## 5. 与 `c7d793e4` 的差异

`c7d793e4` 的 Full CI run `40587302416` 失败于 race 分片：`TestCatalogEpisodePendingEntryIsHiddenFromReadSearchAndReview` 300.71 s，`/catalog/scan` 返回 503（`context deadline exceeded`）。同一用例在 `907391f2` 的 run `40596212856` 上通过，说明它处在预算边缘而非稳定失败；`15b84ed2` 把该用例的成本从 300 s 降到 22.7 s。

## 6. Full CI 终态

run `40602372253`（`15b84ed2`）：见本节更新与 [25-live-browser.md](25-live-browser.md) 第 13 节。

## 7. 双轴审查（基线 `3336e5c0`）

Standards 与 Spec 各提出三类问题，已处理：

| 审查意见 | 处理 |
| --- | --- |
| 单元测试只覆盖纯函数，删掉调用点仍全绿 | `visible` 改为必填，调用点漏传即编译失败；显示规则测试补齐“已定结果优先于可见性” |
| 一次性闩锁在语言切换后让整页重新请求 | 闩锁按请求键作用域，且观察器随键重建，只对屏幕上的字段重新请求（`c2057b7c`） |
| `visible === false` 会吞掉已定状态 | 判定顺序改为 enabled → 已定状态 → 可见性 |
| 400 px 预取未写文档 | `docs/development/translation.md` 记录窗口、按键盘闩锁与未请求字段的表现 |
| `remote_request_delay_*` 对目录索引是空操作，注释因果错误 | 该改动整体撤回；真实原因是 `checkptr` 让 101 集拆分在 race 下耗时数百秒，`15b84ed2` 直接构造被测状态 |
| 共享夹具全局清零节流 | 已撤回，夹具恢复原节流 |

保留的判断：`907391f2` 的提交信息把 CI 失败归因于节流，与事实不符。该提交的代码改动已在 `15b84ed2` 中撤回，历史提交不重写；本条记录更正。
