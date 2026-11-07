# 25：翻译维护等待配置锁时的生命周期取消

被测实现：`b6f8539ca7909981cab5deab62e32a8d6383e949`。本分支先从 `b521cc47` 正常 fast-forward 合入集成点 `72d52434075c1724c5f3fd97f14b5b098826d546`，本轮差异仅为 `translation_prefill.go`、直接相关的 `translation_maintenance_cancel_test.go` 和本证据。

本次处理独立复审 TR2。TR1 的目录显示名/路径失效与后续 107 不在本轮范围；不改已合入的 106、catalog、server.go 或工单状态。

## 修复

低频维护到期后使用 `translationConfigMu.TryLock()`。设置请求仍持锁时，后台不等待，不新增任务或等待队列，并保留到期状态，下一次 15 秒 tick 再试。取得锁后仍在同一把锁内加载配置并执行维护，配置一致性保护保持；普通翻译保存路径不获取该锁。锁等待为零，原维护十秒 context 与数据库有界写入规则保持。

## 红绿证据

测试经过真实鉴权路由 `PATCH /api/settings/translation`，在请求体读取边界延迟客户端输入；未直接操作 mutex。随后启动公开 `Server.StartJobRunner(ctx)`，等待真实首个五秒维护 tick，再取消其生命周期 context。测试无论红绿都会释放客户端并等待 runner 退出。

- 修改前：300.121 ms 内 runner 未退出，释放设置请求体后才退出。`translation-maintenance-cancel-red.log`，退出 1。
- 修改后：设置请求体仍未释放时 runner 已退出，测量输出为 0.000 ms（微秒采样，不表示严格零耗时），满足 300 ms 测试界限。`translation-maintenance-cancel-green.log`，退出 0。
- 恢复用例保持请求体未释放至首个 tick 后，再释放；历史译文于 14501.265 ms 后被下一 tick 清理，当前标题与 native 保留。race 下为 14545.256 ms。17 秒测试界限覆盖下一 15 秒 tick。

同批复跑已有维护取消/写锁超时后重试、在途配置切换与交互保存不扫描 retention 的回归。恢复测试清理阶段取消 runner 时，日志可出现 `context canceled`，这是主动结束测试生命周期的结果，退出状态与断言均通过。

## 命令与结果

Go 命令 cwd 为仓库 `backend/`，Make 命令 cwd 为仓库根；均使用非 login Git Bash。日志位于仓库外工作区 `.kikoto-work/reports/20261107-remediation/`。

| 命令 | 结果 | 日志 |
| --- | --- | --- |
| `go test ./internal/httpapi -run '^TestTranslationMaintenanceRunnerCancelsDuringSlowSettingsRequest$' -count=1 -v -timeout=30s` | 红退出 1，绿退出 0 | translation-maintenance-cancel-red.log / translation-maintenance-cancel-green.log |
| `go test ./internal/httpapi -run 'TestTranslation(MaintenanceRunner\|RetentionRetryAfterWriterTimeout\|RetentionCoalesces\|PrefillConfigurationSwitch\|WritesDoNotRunRetentionScans)' -count=1 -v -timeout=90s` | 退出 0，6 项，29.108 s | translation-maintenance-cancel-regression.log |
| `go test -race ./internal/httpapi -run 'TestTranslation(MaintenanceRunner\|RetentionRetryAfterWriterTimeout\|RetentionCoalesces\|PrefillConfigurationSwitch\|WritesDoNotRunRetentionScans)' -count=1 -v -timeout=2m` | 退出 0，6 项，47.729 s | translation-maintenance-cancel-race.log |
| `make backend-format backend-vet backend-build` | 退出 0 | translation-maintenance-cancel-build.log |
| `make sensitive-check NODE=.../node.exe` | 提交实现前退出 0，2 个文件 | translation-maintenance-cancel-sensitive.log |
| `git diff --check` | 退出 0 | 命令终态 |

本机 Windows amd64、Go 1.27.3、迁移 SQLite，外部模型仅本地 HTTP 合成桩。无真实来源、模型、生产、凭据、UI 或视觉验收。未重跑全仓测试、规模性能或生产 SLO；本轮只验证维护配置锁等待及其直接相关边界，不代表整个工单或项目完成。提交沿用已有正常 Git 配置，无禁签参数，无密钥或全局配置操作。
