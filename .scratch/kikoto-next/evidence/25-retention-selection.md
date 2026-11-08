# 25：有界淘汰候选与写竞争

本证据对应同提交的 `content_translation_retention.go`；原始日志在仓库外 `reports/20261107-remediation/`。不代表完整候选已验收或已部署。

`f33e51be` 的 Full CI run `42498479864` 中，`TestContentTranslationWriteContention/cache_eviction` 在外部写锁释放后仍有两个 writer 返回 `SQLITE_BUSY`，实际淘汰数量也不正确。每个 writer 重复编译完整发布语料保护查询，耗尽已有写入预算。

淘汰先读取最多一个批次的最旧 model/source 候选；只有候选包含当前配置的标题或简介时才在同一写事务中重新执行完整语料保护选择。当前候选之外的行不可能被第一条路径删除；完整保护路径继续寻找后续可淘汰行。没有按配置键预扫整张译文表，没有跨事务占用连接，没有修改写入、读取、维护超时或新增迁移。

- `go test -race ./internal/httpapi -run '^(TestContentTranslation(Retention|Store)|TestContentTranslationWriteContention|TestContentTranslationShortQueryCoversFullCorpus|TestMigratedTestDatabasesRemainIsolated)' -count=1 -timeout=20m -v`：退出 0，16 个顶层用例通过，399.402 秒；日志 `retention-bounded-candidates-race.log`。
- 原 12,000 行、1 MiB 页面预算、非空剩余缓存断言通过；维护耗时 5.766 秒，原预算 10 秒。当前 work/series 语料、native 行、软预算保护及多批清理通过。
- 原全部 writer、400 ms 并发读取、5 秒写入预算及持锁超时用例通过，未减少 writer、放宽断言或增加 retry。
- 50,001 行短查询仍找到最旧与最新目标，race 耗时 238.60 秒；样本未缩减。
- `make backend-format backend-vet backend-build` 通过；代理未配置导致的依赖下载失败单列在 `nov9-candidate-static.log`。配置既有代理后完成 lint；首次 lint 指出错误分支未显式处理 Close 返回值，修正后 `nov9-candidate-static-final.log` 为 0 issues，actionlint、敏感信息与证据扫描退出 0。

最终候选的 Full CI、目标机负载和部署验收继续由 24/25 记录。
