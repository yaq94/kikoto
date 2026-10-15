# 10 来源生命周期迁移准备

Implementation: in-progress

本独立迁移提交基于 integration `f0fd5d77be1072f260fc817245c01d1b6334ea0f`。
072 已由集成明确分配给 10，073 留给 13。此记录证明存储前置，不能代替 10 的 H/R/F。

`072_source_catalog_lifecycle.sql` 以加列和新增候选关系方式扩展 069/070：

- entry 保存来源可用状态、观察代次、内容/布局证据摘要及可选路径认回后的原引用。
- file 与 observation 保存经过验证的来源内容哈希；file 另有观察代次。
- scan 保存有界生命周期结算的阶段与游标。
- relocation candidate 只保存两个 catalog id、固定原因与代次，不包含个人字段。

所有旧行保留，默认 available/空证据/代次 0；不会在迁移时登记作品、判缺失、认回目录、
移动评价或发起网络。071、001–070 和发布 baseline 不修改。暂时观察到的替代目录可以保留
旧 catalog 引用；是否认回仍由后续完整且唯一证据的行为实现决定。

Go 1.26.9、`GOTOOLCHAIN=local`，在 `backend/` 执行：

- RED：`go test -mod=readonly ./internal/storage -run '^TestSourceCatalogLifecycle' -count=1 -v -timeout=1m`，退出 1，旧 schema 缺少 `availability_status`。
- `go generate ./migrations`，退出 0，生成 `compat/072_dev.sql`。
- `go test -mod=readonly ./internal/storage -count=1 -v -timeout=3m`，退出 0，12.393 秒；新装/完整增量链/开发基线、旧库升级、重复/并发启动和外键均通过。新增 071 升级用例保留 catalog/work id、文件、系列成员与评分备注，候选外键拒绝不存在目标。

原始日志为仓库外 jobs `j-okubrx`（RED）、`j-iobqsb`（GREEN）。
来源状态、可靠改名/待确认候选、失联恢复、个人引用清理保护及功能验收仍在实施。
