# 18：迁移完成状态与维护状态一致性

## 范围与基线

- 本票分支：`codex/kikoto-ci-migration`，集成基线 `f0fd5d77be1072f260fc817245c01d1b6334ea0f`。
- 原生 worktree 创建返回 `Not a git repository`；确认目标仓库干净、`.worktrees` 已忽略后，按授权在独立 worktree 使用 Git fallback。
- 仅修改迁移完成发布、详情读取、同模块回归测试与对应文档；无数据库 schema 变更，无部署、全局配置或签名设置变更。

## 根因与行为

`runLibraryMigration` 的最终 UPDATE 可以先被另一数据库连接读取，随后才清除维护标记。详情读取未参与发布锁，存在 HTTP 返回 `completed` 而普通请求仍为 503 的窗口。

最终 UPDATE、扫描标记清除与维护标记清除共用 `layoutMigrationMu`，详情读取使用同一把锁。锁仅在迁移扫描完成后取得；扫描任务的领取需要该锁，不能把扫描等待纳入完成临界区。UPDATE 返回错误时仍保留维护状态，由已有失败记录与恢复流程处理。

## 当前产物的验证

使用现有 Go 1.26.9、Node 24.19.0 和 Make 工具链；命令均在本票 worktree 执行。日志由执行宿主保留为下列 job 的完整输出；受控交接目录另外保存副本及退出码索引。

| 检查 | 命令或控制条件 | 结果 | 日志 job |
| --- | --- | --- | --- |
| 原有定向基线 | `go test -mod=readonly ./internal/httpapi -run 'TestLibraryMigration\|TestV061DatabaseBoots' -count=1 -timeout=3m` | 退出 0，10.602 秒 | `j-3x0v45` |
| 首次失败复现 | 最终 UPDATE 已提交，测试 driver 闸门暂停返回 | 退出 1，HTTP 200 / `completed` 且维护未解除 | `j-nsuvct` |
| 最终夹具验证旧实现 | 暂时撤去本票 7 行修复，发布用例 `-count=10` | 退出 1，10/10 均因同一不一致失败；随后恢复修复 | `j-se499b` |
| 初始修复与恢复回归 | `go test -mod=readonly ./internal/httpapi -run '^TestLibraryMigrationCompletion' -count=1 -timeout=45s -v` | 退出 0，1.835 秒 | `j-uv5jly` |
| 迁移全范围有界 race | `go test -race -mod=readonly ./internal/httpapi -run 'TestLibraryMigration\|TestV061DatabaseBoots' -count=3 -timeout=3m -v` | 退出 0，80.296 秒，无 race 报告 | `j-xwxg1x` |
| 恢复修复后的完成场景重复 | `go test -race -mod=readonly ./internal/httpapi -run '^TestLibraryMigrationCompletion' -count=10 -timeout=2m -v` | 退出 0，38.510 秒，全部通过，无 race 报告 | `j-frbyrd` |
| 格式检查 | `make backend-format` | 退出 0；同次后续 lint 在依赖解析阶段网络失败 | `j-a3ahs3` |
| 依赖、vet 与构建 | `make backend-verify backend-vet backend-build` | 退出 0，模块校验通过 | `j-bluuoi` |
| lint 与文档 | `make backend-lint frontend-docs` | 退出 0；lint 0 issues，124 个公共 Markdown 链接、5 个 locale / 11 个用户页面通过 | `j-bbzzba` |

首次 lint 请求官方 Go 模块代理时连接超时；沿用已有工具链文档的进程级代理后重跑通过。没有修改 Go 校验、注册表、代理或其他持久配置。

## 回归所保护的契约

- 发布并发：真实登录会话请求详情；数据库已提交且维护尚未解除时详情不提前返回。释放发布后，详情为 `completed`，公共维护接口为 false，普通布局请求为 200。
- 数据库写失败：真实 SQLite trigger 拒绝最终 UPDATE，状态为 `failed/scan`，公共维护接口为 true，普通布局请求为 503；关闭并重开同库后保持检查点，显式 retry 执行扫描后完成。
- 取消：在最终 UPDATE 前取消真实上下文，状态保持 `running/scan`，维护继续；关闭并重开同库后 resume 完成。
- 原行为：有界 race 重复覆盖旧版本数据库启动、空库与有作品往返迁移、copy/cleanup/scan 三阶段重启恢复、作品与 Fetch 事务路径一起迁移、部分源清理恢复及扫描完成条件。

没有生产测试钩子；测试 driver 仅控制 SQLite 系统边界。没有提高既有超时、增加重试、跳过测试或放宽断言。

## 交接边界

提交前按 `make sensitive-check` 验证当前完整差异，提交与推送沿用仓库既有配置。合入集成分支及最终 Full CI 由主流程完成；本票不宣称整个 CI 或生产验收通过。

## 集成交接（2026-10-21）

- 本票实现提交：`924f8079a60d7583058f79409cd1afd1826c6474`（Publish library migration completion with maintenance release）。
- 合入集成分支：`2c2e87c4`；CI 迁移相关提交另见 `298ebddc`、`a46b9a70`、`be14c79f`、`e1cadf0f`。
- `codex/kikoto-ci-migration` 是临时实施分支，合入后未保留独立远端分支；集成历史与上述 SHA 即交接点。最终 Full CI 对集成分支执行。
