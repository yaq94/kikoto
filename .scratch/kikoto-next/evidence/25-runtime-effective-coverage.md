# 25：运行状态的语言汇总使用有效语料

基点 `2dc873a52424973e08f15c01fba6674f4d6e033f`。运行状态接口的顶层 `languages` 原本只检查 origin/config_key，仍会统计旧原文、退役对象和仅用于显示的标签；同一响应内的 prefill 统计却采用有效语料。该差异违反 ADR-0006 第 3 条。

接口直接从本次已验证的 prefill 覆盖结果构造非零语言汇总，去掉额外全表查询。没有新增缓存、预算、返回字段或迁移；同实体同字段的多行由覆盖定义统一处理。

`TestTranslationRuntimeLanguagesUseEffectiveCorpus` 经真实 HTTP 和迁移 SQLite 写入一个当前英文标题、一条旧原文日文行和一个韩文显示标签。旧实现返回三个语言，退出 1；预期仅返回当前英文语料。日志 `translation-runtime-corpus-red.log`。修复后同 HTTP 用例通过；相关 race 退出 0、200.250 秒，覆盖队列、简繁、预算、短查询及实际重启，`translation-runtime-corpus-race.log`。本地 LIVE 用例未配置而 skip，不计通过，真实调用单独见下文。format／lint（0 issues）／vet／build／隐私检查退出 0，`translation-runtime-checks.log`。

## 前一候选的真实证据

以下仅适用于 `2dc873a5`，完整目标仍在进行：

- 实际 Index 调用与简繁缓存切换通过：日文样例生成及转换 1,992 ms，后续简繁缓存切换 7 ms；中文原生样例直接转换，保护名称保持。`target-2dc873a5-live-translation.log`，退出 0。仅调用两个合成中性样例及一条短试译。
- 隔离 105→111→105 升级与恢复通过：新旧应用实际启动、作品 API 回读、六张身份／个人数据表逐行一致，完整性和外键检查通过；备份前失败能恢复旧镜像。`target-2dc873a5-rehearsal.log`，退出 0，生产保持未更新。
- 1 vCPU／768 MiB 隔离实例，原 60 秒／8 并发组合负载 1,058 请求，0 超时／错误／校验失败；176 次 Range 为 206。冷统计 14,896.57 ms；浏览 p95 611.95 ms、搜索 661.92 ms、Range 924.19 ms、评分写 799.87 ms、读 655.06 ms。`target-2dc873a5-mixed-load.json`，退出 0。此为真实服务组合测量，不替代单独规定的 2 vCPU／2 GiB 浏览规模门槛。
- 对 27 个待确认空目录分别取得匿名 list 与 get 响应，54 个 HTTP 200；全部列表 total=0，目录对象均存在。未改变扫描的 unconfirmed-empty 状态，未声明完整覆盖。路径及原始响应仅保存在受限运维目录。

## 审查限制

按 matt-code-review 派发的本批 Spec 和 Standards 两个独立审查均在执行前被自动审核拒绝，理由为 possible cybersecurity risk，无独立审查结果。主代理核对集合资格的等价性、预算页分摊及已发布快照规则，并补 HTTP 红绿回归；这不记为独立双轴审查通过。此前已有独立审查仍仅覆盖其记录的 SHA。

原始日志位于仓库外 `reports/20261107-remediation/`。最终候选完整门禁、索引最终覆盖与部署后验收仍待完成。
