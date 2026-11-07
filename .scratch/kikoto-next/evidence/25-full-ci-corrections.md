# 25：完整门禁的兼容性回归修复

基点 `2747e05e5ab6e3824c7e618195eb4781b4cd33d1`；本页描述其后的当前差异。生产仍是旧版本，当前未部署。本轮维持原工单合同和现有断言，不增加 retry、不放宽超时。

## 同源刷新兼容性

完整 `make ci-backend` 在 8dfd7626 对应代码失败于 `TestOpenListMetadataRequestsAreSerializedPacedAndCancellable`：三个同源请求中两个返回 409，原合同要求按序成功。日志 `root-ci-backend-109.log`，httpapi 1327.935s、退出 2；50k 插入超时已经消除，不能把该局部修复算作整包通过。

刷新保留三个活动来源，每来源一个请求，等待总量限制为 64。等待使用请求 context 和端点五分钟预算；队列满才返回可重试 409。等待结束重新读取来源配置，取消释放登记。网络请求期间不持 Mutex。

原始三请求串行、请求间隔、在途取消断言不改。新增回归检查其他来源独立完成、同源等候可取消、64 个等待者之外拒绝、取消后可继续刷新。`root-bounded-refresh-final.log` 为 race exit 0、30.297s；配置重读后 `root-bounded-refresh-current.log` exit 0、4.834s。`root-final-compat-static.log` format/lint 0 issues；同一 shell 后续单文件前端格式命令因 cwd 错误失败，已单独纠正，不能把整个 shell 称为成功。

此实现取代 `25-final-review-fixes.md` 的立即拒绝重复来源阶段方案，文档同步为有界等待。

## 关联确认的保存屏障

GitHub Full `42419942862` 的 matching-shared 组及本机 `root-shared-before` 均在重开后读到初始备注，未读到已选备注。界面提交前后都可显示“Waiting for record owners”；测试用该文字作为提交完成条件，会在实际 decision 请求结束前 reload。

用例等待真实 decision 响应成功，并断言响应中的本人评分与备注，再检查界面“本人选择已保存”。原两用户隔离、未完成不合并、刷新后保存选择、最终统一提交和备注归属断言全部保留。`root-shared-green.log` 的完整 matching 组通过，覆盖匹配、共享关联及拆分；非视觉、无重试。本轮没有改变关联业务实现。

`make -o frontend-install frontend-format frontend-lint` 在修复后的工作区退出 0，`root-shared-checks.log` 记录格式通过、lint 0 warnings / 0 errors、E2E TypeScript 检查通过。

## 实际恢复演练

候选 2747e05e 已构建为独立镜像。在目标机以生产 SQLite backup 副本执行 105→109→105，实际启动新旧应用并读取作品 API；作品、来源条目、个人状态、收藏、个人标签和 portable progress 六表逐行哈希一致。隔离副本关闭来源和翻译，使用内部网络，不挂载生产数据目录；没有把该演练称作真实外部服务验收。

运维脚本的备份前故障分支原会继承新镜像环境变量。本轮显式固定旧镜像，并在准备好恢复脚本后、修改环境配置之前进入回滚保护。隔离演练故意让数据库副本复制返回 77，确认自动恢复旧镜像；正常升级及恢复也实际回读通过。结果 `restore-2747e05e.log`、`restore-prebackup-failure.log`、`restore-upgrade-2747e05e.log`、`restore-previous-image-2747e05e.log`，演练 exit 0，生产容器身份与启动时间未变。首轮 compose 临时目录参数写法错误已记录，未启动应用，第二轮修正后通过。

原始日志均位于仓库外 `reports/20261107-remediation/`。受限运维目录保留镜像 digest、原始快照与脚本指纹。最终新候选仍须完成同 SHA Full CI、恢复复核、目标机资源及真实服务验收后部署，17/24/25 不据本页关闭。
