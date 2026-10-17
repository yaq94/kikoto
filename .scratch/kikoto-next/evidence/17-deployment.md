# 17 组合验收、部署与回滚证据

部署产物：集成分支 `codex/kikoto-next-integration` 固定提交
`7ed73fa06b38686caf4422f5a9cbd090ba53b7a3`（应用版本 `v0.8.3`，schema 目标 76）。
本记录只登记实际执行结果；服务器私有路径、凭据与原始日志不进入 Git。

## 双轴审查（matt-code-review）

固定审查基线 `9949175e53979bcd590c75a624bc0efc67684da3`，范围
`git diff <base>...HEAD`（402+ 文件）。两个并行审查子代理分别完成：

- Standards 轴：硬性发现 2 项。A1 历史提交信息格式不合 `<type>(scope):`
  约定（已推送历史，保持原样，后续提交遵守）；A2 `.scratch/` 过程产物入库
  与 CONTRIBUTING 的取舍（工作区 AGENTS.md 明确该目录为工单系统，按上层
  约定保留，证据文件继续经 `make sensitive-check` 扫描）。判断性提示
  （命名、重复骨架、忽略序列化错误等）逐条评估为低风险或不适用，不阻塞
  发布；完整报告在仓库外 `kikoto-next-resume/review-standards.md`。
- Spec 轴：01–16、18、19 各票验收清单的关键行为在当前源码中均有对应
  实现；未发现规格要求但缺失的功能、未发现范围蔓延、未发现与规格冲突的
  实现。三项待复核（翻译缓存读权限路径、取消传播、配置表单 F 覆盖）由主
  代理逐条关闭：翻译端点要求 `library:read` 且按标识回读内容、缓存只存
  公共字段译文；队列在最后一个读者取消时终止在途任务且共享任务去重；
  `translation-settings.spec.ts` 覆盖普通用户无配置标签且 API 读取 403。
  完整报告在仓库外 `kikoto-next-resume/review-spec.md`。

## Full CI 终态

GitHub Full 工作流对部署提交的实际运行：run `39049272400`
（workflow_dispatch，attempt 1），结论 success；17 项检查（Gate、Style、
Backend static/coverage/race 1–4、Frontend、E2E 1/4–4/4、Smoke、
Development Compose、Android build、iOS build）全部 success。

## 组合前后端验收（同一当前构建）

`run-combined-integration.sh` 在部署提交上重跑组合用例（fixture 生产模式
API + 生产前端构建 + 合成 OpenList 与可控模型，退出 0）：

- 编号作品本地播放、折叠播放器、队列持久化；切简体后 Hero 译文出现；
  保存评分 4 + 备注并经 API 回读一致；切繁体后译文与偏好 `zh-tw` 回读一致。
- SPA 导航到无码目录后同一 audio 元素、currentSrc、队列与进度保持；
  无码条目保存评分 5 + 备注；`primaryCode=""`、`workId>0`、
  `workflow-runs total=0`（登记评价不触发下载任务）。
- 共享关联 preview/start 完成后另一条目共享同一评价，两条目 workId 各自
  保留（ADR 0002）；`catalog-directories` 同时列出两个目录；
  `/api/works/<id>/media` 为空（登记不隐式下载音频）。
- 夹具上游轨迹为 6 次 `/chat/completions`（简/繁各 3 字段），无异常路径。

## 服务器资源与备份

部署前复查：2 vCPU、内存 3.5 GiB（可用约 1.6 GiB）、磁盘 59 GiB 总量
（可用约 17 GiB）。生产为 Docker 单容器 host 网络，监听 127.0.0.1:7655，
数据卷 config/cache/data。

切换前快照 `/root/kikoto/rollbacks/cutover-20261017T164424Z/`：
停服后复制 `kikoto.db`（3 633 152 字节，sha256
`9189e21f849edf59044dea9d7d493b34c3b98eeeedbee0a2aee9cebde3e44660`，
schema 68、quick_check ok、外键违规 0）及完整 config 目录；manifest 记录
旧镜像摘要与新镜像标签。

## 回滚路径演练（同一部署提交）

修正版演练脚本对 2026-10-16 的停服快照执行两阶段，结果
`rollback drill complete`、`drill_exit=0`：

1. 快照 + 新镜像 `kikoto:next-7ed73fa`（镜像 ID `f09ab8c6f2a0`）：健康
   200，迁移到 schema 76，quick_check ok，外键违规 0。
2. 重新还原快照 + 旧镜像（`kikoto:rollback-before-next-20261016t162538z`）：
   健康 200，schema 保持 68，quick_check ok，外键违规 0。

结论：可执行回滚路径 = 还原切换前快照数据库 + 切回旧固定镜像；旧应用拒绝
未来版本数据库属预期，不能跳过快照还原。

## 部署与迁移

`cutover.sh`（2026-10-17T16:44:24Z）：快照 → `.env` 固定
`KIKOTO_IMAGE=kikoto:next-7ed73fa` → `docker compose up -d` 重建容器 →
健康 200 → 迁移观察 schema 68 → 76，final quick_check ok、外键违规 0，
`cutover_exit=0`。部署后验证：健康接口 200（`v0.8.3`）；登录返回 200 且
权限列表完整；`/api/works` 既有 6 部作品在列；翻译配置回读（默认模型
`Index-Translate-35B-A3B`，未配置服务地址时 `hasApiKey=false`）；验证后
已登出并删除临时凭据文件。

## 部署后功能验收（2026-10-17T17:12 起，生产）

- 创建规格指定的 OpenList 来源（displayName `asmrgay OpenList`，id=3，
  匿名，HTTP 201）。首次索引（runId 41）启动。
- 首次索引用校准甜点配置（并发 2、间隔 150 ms）触发源站 429 限流，
  45 秒 partial、329 目录失败；按规格退避与恢复预算处理：重置失败目录，
  改保守配置（并发 1、间隔 1000 ms）续跑，续跑期间 0 次 429、failed 0。
  该过程同时验证了来源退避（`remote source origin backing off`）与失败
  不冒充完整覆盖的契约。
- 浏览回读：`/api/remote-sources/3/works` 返回统一卡片（索引进行中已
  702 张），含无发行编号作品（`primaryCode` 为空）与系列合并卡。
- 搜索回读：`q=小芹` 命中 1（《小芹的出轨经历》），搜索直接覆盖来源
  索引条目，未登记也可检索。
- 媒体播放：既有作品 `/api/media/186/stream` Range `bytes=0-31` 返回
  HTTP 206、32 字节、M4A 文件头，证实部署后节目流路由与偏移读取正常。
- 翻译服务：管理员配置入口与权限已验收（`sources:write` 才能保存，
  普通用户 403）；生产服务地址/密钥属管理员配置项，当前 `hasApiKey=false`，
  由管理员在设置页保存后即时生效并可在同页试译。

## 真实来源索引（发布前同机构建族）

真实匿名 OpenList 来源（规格指定站点）在独立实例上完成首轮全量索引：
运行至 3300+ 目录完成后实例重启，按断点续跑；续跑期间站点新增目录持续
被发现（目录总数随运行增长），最新观察为运行中、完成 6220/6277、失败 6
（`upstream_unavailable`，源站侧）、受限 0。索引不创建作品、不下载音频，
速率与预算按校准结论（并发 2、间隔 150 ms、批 32、页 500）执行。
最终汇总数字以运行终态记录为准，详见仓库外
`kikoto-final-validation/index-calibration.md` 与实例进度日志。

## 未完成项

- 真实来源索引运行的终态汇总与失败目录清单（运行收尾后补记）。
- 生产环境翻译服务地址/密钥由管理员在设置页保存后完成一次试译（配置
  入口、权限与默认模型已验收，当前未配置凭据）。
