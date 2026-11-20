# 26 号工单复审基线

对应 [26 号工单](../issues/26-live-review-remediation.md)。集成分支起点为 `55458f0e8137abf4581d350673e21b631d3e8f6f`。生产应用为 `2dbfa228`，`fded2950` 已在两者的祖先链中。

## 2026-11-18 生产采样

- 容器报告 `healthy`，运行镜像为 `kikoto:next-2dbfa228`。只读读取的 schema 为 111，未标记为 dirty。
- 北京时间约 19:34，目录首次扫描仍为 `running`。扫描表显示 `done`、零待处理发布批次、一条失败目录和零新增索引，发布仍为 `preparing`。42,137 条作品目录和 584 条系列实体均未记录翻译或媒体准备完成。单次数据库快照无法判定任务当前在哪条语句执行。
- 最后一轮每日增量在 11 月 1 日 03:00 启动，结果为 `partial`，列出 28 个目录，零发布条目。预填游标两次采样间从 753231 推进到 753269。
- 数据库与 Docker 位于同一卷。精确可用容量为 9.326 GiB，低于既有部署脚本的 10 GiB 门槛。当前镜像及回滚脚本在主机上。此采样只读。

## 当前工作区基线验证

| 验证 | 结果 |
| --- | --- |
| 项目指定 Node 24.19.0 和 npm 11.17.0 下 `make frontend-install` | 退出 0，锁定依赖安装完成。系统 Node 22 尝试曾因项目引擎约束失败。 |
| `make -o frontend-install frontend-build` | 退出 0，类型检查和前端构建通过。 |
| `make -o frontend-install frontend-docs` | 退出 0，136 份公开文档链接通过，覆盖 5 个文档语言和 11 个用户页面。 |
| `make frontend-e2e` 定向详情操作 | 8 通过。目录系列和翻译的 10 个夹具用例在该入口跳过。 |
| `make frontend-e2e-fixtures` 定向目录系列和翻译组 | 20 通过，涵盖系列、内容翻译、繁体、版本及浏览器译文缓存。已有用例为基线，26 号新增布局与状态回归待运行。 |
| `make backend-verify` | 退出 0。 |
| `make backend-vuln` | 退出 2，连接默认 Go 模块代理超时，扫描未运行。 |
| `make frontend-audit` | 退出 2。开发工具依赖 `picomatch@4.0.7` 在 GHSA-fc5q-39xc-vjpf 受影响范围内，GitHub 公告未列修复版本。`npm audit --omit=dev --audit-level=moderate` 退出 0。完整门槛仍待通过。 |

私有远端通过配置凭据读取，基线与本地一致。工作区未发现仓库签名配置或可用的 1Password SSH 签名代理，当前未生成候选提交。
