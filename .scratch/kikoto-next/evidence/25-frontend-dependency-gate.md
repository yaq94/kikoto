# 25：前端依赖与完整门禁

## 被测产物

应用源码为 `32dc7fe5a75f635ba437bbb4e1db99eb054f9b76`，加本提交中的 `frontend/package-lock.json`。本次只更新原有 rimraf 开发依赖允许范围内的锁文件，不改变运行时代码或测试断言。

rimraf 为 6.1.4，传递依赖 glob 为 14.0.4、minimatch 为 11.0.5、path-scurry 为 3.0.0。更新前依赖审计报告三个 moderate 问题并使完整门禁失败；更新后审计为零。Node 使用仓库要求的 24.19.0，npm 为 11.17.0。

## 实际验证

命令在仓库根执行，原始输出保留在仓库外工作区 `reports/20261107-remediation/`：

| 命令 | 结果 | 日志 |
| --- | --- | --- |
| `npm update rimraf --package-lock-only --ignore-scripts`（frontend） | exit 0；仅更新该开发工具依赖链 | `root-rimraf-update.log` |
| `make ci-frontend` | exit 0；安装及审计 0 vulnerabilities；193 个包签名、68 个 attestations 验证；169 个测试文件、994 项测试通过；生产构建成功 | `root-ci-frontend-fixed.log` |

构建保留既有的大块提示，未提高阈值、添加测试 retry 或放宽断言。未新增浏览器视觉检查。本记录只证明上述工作树的前端门禁；最终固定 SHA 的 Full CI、双轴审查、恢复演练、真实调用与部署验收仍须另行完成。
