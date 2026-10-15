# 08 本地集成验收

本记录对应包含此文件的集成提交，分支为 `codex/kikoto-next-integration`。
第一父提交为 `dd38bf1e70ca09fcd796621e9dc5f4a741f1b5ae`，08 合入提交为
`557a444af505b72c47363d93fc268ff0f0d02c35`。使用普通 `git merge --no-ff
--no-commit`，无冲突；合并完成时暂存树与 08 交付树完全一致。本批另外同步执行
状态、规格进度行和本证据，没有修改产品代码、验收条件或用户故事。

01–06 为 resolved；07、08 为 verified-local。11 的预备提交 `2cc1ef5` 未合入，
仍缺 071 迁移及 H/F；071 保留给 11。完整发布前置、07 真实媒体 LIVE 和 08
真实全量运行报告继续保留，合成数据不证明实际站点覆盖。

## 实际集成复验

使用 Go 1.26.9、Node 24.19.0、npm 11.17.0，设置进程级 PATH、
`GOTOOLCHAIN=local` 和既定 HTTP(S) 代理，回环地址使用 NO_PROXY。
复用现有依赖和浏览器，没有安装新工具或变更全局配置。H/R/race 使用
`-count=1`，不是空匹配或测试缓存。

| 命令 / 范围 | 当前产物结果 |
| --- | --- |
| `go test -mod=readonly ./internal/httpapi -run '^Test(SourceCatalogIndex\|OpenList\|CatalogDirector\|CatalogLocation\|CatalogRefresh\|ContentTranslationTraditional\|SourcePlayback\|SourceAvailability\|DatabaseCleanup)' -count=1 -v -timeout=5m` | `j-xeh1zs` 退出 0；40 个顶层 H/R 通过，23.669s；2 个未配置 LIVE 跳过，不计成功。 |
| `go test -mod=readonly ./internal/storage -count=1 -v -timeout=5m` | `j-oeegcd` 退出 0；66 个顶层通过，17.954s。 |
| `go test -race -mod=readonly ./internal/httpapi -run '^TestSourceCatalogIndex(ConfiguredConcurrency\|ConcurrentStarts\|Cancel)' -count=1 -v -timeout=3m` | `j-o1md1n` 退出 0；3 个顶层通过，9.434s，无 race。 |
| `make backend-format backend-vet backend-build backend-lint GOLANGCI_LINT_BASE=dd38bf1e70ca09fcd796621e9dc5f4a741f1b5ae` | `j-q1dohu` 退出 0；lint 0 issues。共享 golangci 锁已释放。 |
| `make -o frontend-install frontend-format frontend-lint frontend-i18n frontend-build frontend-docs` | `j-4xhkzp` 退出 0；格式、0 lint 警告/错误、E2E 类型与生产构建通过；122 公开 Markdown、5 locale/11 用户页通过。保留既有 Vite 配置加载与大 chunk 提示。 |
| `make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='source-catalog-index.spec.ts --project=desktop-chromium --workers=1 --output=test-results/merge-08-index'` | `j-9bephf` 退出 0；1/1 通过，7.4s。 |
| `make -o frontend-install -o frontend-playwright-install frontend-e2e E2E_ARGS='openlist-catalog.spec.ts --project=desktop-chromium --workers=1 --output=test-results/merge-08-catalog'` | `j-ivah69` 退出 0；2/2 通过，8.7s。 |

合并产品树没有另改，原 08 的 867 项前端测试等证据见[08 实施证据](08-source-index.md)。
本批未重复全套单元测试、全后端、真实服务请求或外部 LIVE。

## 行为与迁移边界

- 预先列明的完整合成树验证分页、变化及重复页、编号/无码/格式目录与排除项，
  未登记作品可搜索。取消、部分失败、限额调整和同库实际关闭重开保留检查点。
- 并发 1/2、共享速率、响应/目录页/入队上限和落盘前低空间暂停通过；已有卡片和
  个人记录可读，恢复不重复对象，不把暂停或受限范围计作完成。
- 历史清理保留每源当前扫描所引用的 workflow；下一完整轮替换引用后旧轮可清理。
  完整成功后每事务最多回收 100 个过时 staging 对象，不清除卡片、文件与评价。
- 03/05/07 交界复验通过：简繁共享缓存及在途请求、保护名称、catalog 真实 path、
  权限先于缓存；已知/未知/失败来源隔离、只读目录与刷新、真实路径 Range 无缓存、
  精确音轨/用户游标和过期 URL 重新解析；旧库关系和个人数据升级后可读。
- 最高编号迁移为 `070_source_catalog_scan.sql`，最新开发基线为 `compat/070_dev.sql`。
  001–069 与发布 baseline 相对第一父提交无差异；生产新装、增量升级、开发基线
  与完整链结构/种子一致性、重复和并发启动、未来版本拒绝均通过。
- F 运行真实 production Routes、会话和迁移 SQLite，外部目录/翻译/合成 WAV
  仅在网络边界替换。索引配置、取消后重开、同 run 恢复、3 张卡片和作品总数 0
  通过；独立新夹具验证无码播放进度、评价保存/清除和未登记 catalog 翻译。
  screenshot、trace、video 全部关闭，无布局、像素或人工视觉签收。

## 状态、进程与缺口

F 使用 3118/17618，先验证端口空闲，夹具采用 `OPENLIST_HTTP_ADDR` 和
`OPENLIST_FRONTEND_ORIGIN`，前端采用 `VITE_API_BASE_URL`。首次 Vite 的无效
测试环境变量在运行 F 前纠正，没有发生产品测试失败。两个独立夹具经自身 stop
入口返回 204，并分别正常退出 0；本次两个 Vite 任务均已停止。最终独立绑定检查
确认两个端口空闲；所有测试任务均读取终态。

本次不处理其他 worktree 或已拒删残留，保留现有依赖、构建和非视觉测试结果。
原始运行日志留在仓库外，只提交脱敏摘要。没有生产变更、真实站点重探测或凭据
操作；现有 IAB 网页播放观察与程序 fs/get 的连接重置/正式代理挑战页失败仍按
[07 证据](07-openlist.md#live-缺口与可复验入口)区分记录。尚无实际 Range 或全量覆盖
证据，实际组合负载、部署与回退继续由后续完整验收负责。

## 中断后的交接核对

接续合并时重新核实 `HEAD=dd38bf1`、`MERGE_HEAD=557a444`，无未解决冲突；
`backend`、`frontend` 和 `docs` 与 08 交付提交无差异。上述七项测试任务的终态及
仓库外完整日志已读回，包含索引 F 的 1/1 和 catalog F 的 2/2，均退出 0。
此次接续未重跑这些已完成的测试，没有修改产品代码。

重新检查两个夹具任务均退出 0、两个 Vite 任务均为已停止，且独立绑定
3118/17618 成功后立即关闭，确认端口空闲。本次没有启动或停止新测试服务，
没有清理其他工作区或既有拒删残留。提交前重新执行文档链接、隐私扫描与暂存/
未暂存两侧空白检查；只有本合并与文档收尾进入提交。
