# 25：新译文 scope 计算与写竞争

`e49c32aa` Full CI run `42505630971` 的 race 3/8 在 `TestContentTranslationWriteContention/content_save` 失败：外部锁释放后一个 writer 返回 `SQLITE_BUSY`，只存入 7/8 行。原始日志 `reports/20261107-remediation/github-e49c32aa-race3.log` 保留失败。

新译文的插入触发器已计算可搜索范围，保存路径又对全部写入行编译同一发布感知查询，延长每个写事务。当前实现先在已获得写锁的事务内按唯一 cache_key 读取既存键；新键仅由插入触发器计算，原有键仍在 upsert 后显式重算。原文、身份和 scope 规则都不改变，不跨事务保存语句或连接，不增加写入预算。

`go test -race ./internal/httpapi -run '^(TestContentTranslationWriteContention|TestContentTranslationStoreSaveIsIdempotent|TestCatalogPublicationRetry|TestCatalogPublicationPinsReaders|TestCatalogPlayback|TestCatalogPublicationRemovedTrack)' -count=1 -timeout=20m -v` 退出 0，10 个顶层用例通过，114.426 秒；日志 `translation-insert-scope-race.log`。并发保存子项 4.58 秒，所有八个 writer 存入成功；持锁超时、并发读取、upsert、准备期迟到译文与重启 marker 重试断言全部保留并通过。

format、vet、build、lint 通过，0 issues，见 `catalog-settled-scope-static.log`。最终 Full CI 及实际服务验收仍待新候选完成。
