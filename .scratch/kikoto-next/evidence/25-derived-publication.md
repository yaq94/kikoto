# 25：来源派生读取的 publication 边界

## 版本与范围

- 基点：`ccf229afafeb39c5b93afaebd6c4577452e52036`，原工作树正常 fast-forward 合入集成分支。
- 被测代码：`63845b8be9e170acfef8d9c679ae974d4e7154a0`；后续证据提交只新增本文。
- 分支：`codex/kikoto-publication-retry-media`。
- 工作树：Codex 托管的 `kikoto-publication-retry-media/kikoto-source`，完整路径仅在本地交接中提供。
- 按已确认的 `implement-spec` / TDD 边界先复现，再修改；数据库使用真实迁移 SQLite，账号、作品、来源、媒体均为合成夹具。
- 本轮没有修改 105–108 DDL、compat、分组、排除、手动扫描、退役 alias 批次或中央工单规格。保留基点已有的 `workHasSourcePresence` / `workHasLocationType` publication reader。

## 修改与可观察行为

`voice_read_model.go` 的声优列表与详情汇总通过 `catalogRead` 读取提交后的 presence。`circle_query.go` 的来源数量、可用作品、分页过滤、系列和作品来源标签也使用同一 publication reader；catalog_stream 的统计额外服从当前 source.enabled。

`work_card_summary.go` 的歌词判断同时读取已发布音轨位置与当前 local/cache 歌词位置，保留原歌词匹配规则。新增批次 queryer helper 保持现有 IN-list 批次上限，并确保同一聚合的各批使用同一个读事务。

`source_availability.go` 的 checkedAt 和缓存 presence 改用 publication reader；作品家族 flags 的相关查询使用短读事务。身份解析先完成，避免单连接事务内再次从数据库池取连接。

`unlinked_works.go` 以 published presence/location 判断删除资格。在同一个写事务中继续检查当前稳定身份与个人引用；已发布也缺失但被 preparing operation 持有的作品返回 `publication_pending`，避免级联删除恢复状态。后台来源检查在网络与写入之前释放读事务。

短事务覆盖各聚合单元：circle 统计组、count+page、单作品来源标签、歌词各批、source flags。没有声称整个包含元数据和个人信息的 HTTP 详情响应共享一个事务；响应中各聚合均读取已提交 projection。身份、个人数据、enabled、local/cache 没有被保存为 publication before-image，仍按当前数据库状态参与查询。

## 红绿证据

原始日志均在仓库外：`E:/项目/服务器/.kikoto-work/reports/20261107-remediation/`。

| 日志 | 结果与失败/成功断言 |
| --- | --- |
| `derived-publication-readers-red.log` | exit 1；preparing 时真实 `/api/voices` 列表/详情 remoteWorks 从 1 提前变 0；circles remoteWorks、remote 过滤 total、series remoteWorks 也变 0；歌词判断提前 false |
| `derived-publication-readers-green.log` | exit 0，1.433s；上述 preparing 仍为 1/true，marker 后为 0/false |
| `derived-publication-unlinked-red.log` | exit 1；真实删除路由在 preparing 时返回 500，SQLite ownership trigger 拒绝。没有观察到作品已被删除 |
| `derived-publication-unlinked-green.log` | exit 0，1.573s；初始 available 与 missing 两种情况 preparing 都返回 200/deleted=0；marker 后缺失作品 deleted=1 |
| `derived-publication-enabled-red.log` | exit 1；禁用来源后 circle remoteWorks、remote 过滤 total、series remoteWorks 错误保留为 1 |
| `derived-publication-checked-red.log` | exit 1；真实 source-availability 路由在 preparing 时 checkedAt 从固定旧值提前变为当前值 |
| `derived-publication-boundaries.log` | exit 0，11.598s；6 个顶层测试全部通过，覆盖上述路由、local 歌词即时变化、禁用来源及跨 marker 的 pinned snapshot |
| `derived-publication-race.log` | exit 0，83.706s；新增边界及既有 circle/voice/lyrics/unlinked/playback-library 定向回归全部通过，无 race 报告 |
| `derived-publication-static.log` | exit 0；format、vet、build、golangci-lint 通过，0 issues |
| `derived-publication-precommit.log` | exit 0；最终格式检查和代码提交前 sensitive-check，6 个变更文件通过 |
| `derived-publication-doclinks.log` | exit 0；136 个公共 Markdown 文件相对链接有效 |

初次夹具缺少 party_series.provider_id 的约束错误已修复，再取得有效行为红灯；该夹具错误不计为缺陷复现。

```text
GOTOOLCHAIN=go1.26.10 go test ./internal/httpapi -run '^TestDerivedPublication' -count=1 -v -timeout=90s
GOTOOLCHAIN=go1.26.10 go test -race ./internal/httpapi -run '^(TestDerivedPublication|TestWorksWithLyrics|TestLoadVoiceSummaries|TestCircleWorksPage|TestCircleProjection|TestLoadCircleWorksLimits|TestLoadCircleAvailableWorks|TestDeleteUnlinkedWorks|TestUnlinkedWorkSourceCheck|TestCheckUnlinkedWorkSources|TestCatalogPublicationKeepsPlaybackAndLibraryPresenceUntilCommit)' -count=1 -v -timeout=8m
GOTOOLCHAIN=go1.26.10 make backend-format backend-vet backend-build backend-lint
make sensitive-check NODE='/e/项目/服务器/.kikoto-work/tools/node-v24.19.0-win-x64/node.exe'
```

HTTP 用例使用单连接数据库，覆盖事务内部误用 s.db 导致的等待风险。pinned snapshot 用例使用 WAL 多连接，在开始读快照后提交 marker 并完成 cleanup，旧快照的来源标签、分页、歌词仍保持旧状态，新读得到提交后的状态。歌词以直接 helper 验证其复杂匹配边界，未作前端渲染。

## 刻意保留的 raw SQL 与限制

- `circle_query.go` 的身份、metadata、个人标签读取继续使用 live 表及原 queryer；不涉及 publication-owned projection，不把整个详情 enrichment 包在长事务里。
- `unlinked_works.go` 的身份、个人引用、删除和 pending ownership/before-image 查询继续使用调用方写事务；这些是当前删除安全条件，不能替换为 published identity。
- `source_availability.go` 的来源配置/endpoint 与后台 `localWorkCodesNeedingRemoteAvailability` 保留 live 查询。后者用于 Kikoeru 后台检查候选和 local/tracked 状态，并非 OpenList 发布读取。
- `work_card_summary.go` 的 tracked fork 查询限定 `remote_stream`，保留原 live 行为。
- `remote_work_detail.go:remoteTrackLocationState`、`media_stream.go:storedMediaPath` 限定 local/cache；`remote_fetch_selection.go:findRemoteCacheFile` 限定 cache，未改。
- `internal/library/query.go` 与 `enrichment.go` 已显式读取 published 表，count/page 已共用只读事务，本轮无需修改。

没有新增全语料复制或 marker 写入。聚合仍按原有索引/IN-list 批次执行；published 视图会增加 pending before-image 查询成本，本轮只验证正确性和已有定向性能用例，没有生产规模基准。pending 删除保护的 series 分支沿同一 ownership 模型实现，未增加独立 series-only 删除用例。

未重复根代理正在运行的整套 backend CI。没有访问生产、真实来源、模型、外部媒体或凭据；无公共 UI 修改。所有本轮后台命令已结束；代码提交后仅补充证据文档和其链接/敏感信息检查。
