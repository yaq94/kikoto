# 25：发布准备期间的个人记录与共享预览

基点 `f33e51bed519e514685c6330f40fc2c07b207c64`。Spec 轴指出两条发布边界缺口，均已用真实路由和迁移 SQLite 复现。

1. 格式卡退役准备期，原卡仍可读，但身份已转移到后继卡。评价写入先对公开卡片执行准入，再在同一写事务跟随后继身份绑定记录。注册和评价共用此路径，保留用户所见标题，不给退役 alias 新建身份。
2. 共享关联预览的系列、条目、成员和来源存在性使用已发布投影。稳定身份、本人记录与 revision 保持实时，marker 后旧预览仍被拒绝。

原始日志位于仓库外 `reports/20261107-remediation/`：

| 场景 | 红测与最终结果 | 日志 |
| --- | --- | --- |
| 退役期间评价 | 未登记与已登记两种原代码均读不到刚保存的评价；修复后通过旧／新 URL、实际数据库重开及 marker 后读取同一评价，始终只有一个作品，alias 不重绑，无媒体请求 | `publication-review-red.log`、`publication-review-green.log` |
| 预览来源证据 | 原代码在准备期显示新标题与位置9；修复后显示旧标题／位置1，marker 后统一显示新标题／位置9，旧预览提交409 | `publication-preview-red.log`、`publication-preview-green.log` |
| 关联回归 | 37个顶层测试通过，包含身份冲突、不同用户记录、系列、真实重启、播放引用及退役，退出0，89.465秒 | `publication-records-related.log` |
| race | 上述两条新增回归及并发改名、已发布别名解析四项通过，退出0，40.727秒 | `publication-records-race.log` |
| 静态 | `make backend-format backend-vet backend-build backend-lint`，退出0，lint 0 issues | `publication-records-static.log` |

新增测试沿已确认 H/R 边界，无 UI 或迁移修改。以上仅为本批证据，仍需集成复核、当前完整 CI 与实际部署。
