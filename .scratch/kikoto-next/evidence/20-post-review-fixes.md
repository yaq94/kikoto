# 双轴审查后修复与复验（2026-10-20）

**基线：** `af59653b` → 本批 HEAD；集成分支 `codex/kikoto-next-integration`。
**范围：** 目录拆分身份、删除确认、sidecar 证据与重试、翻译变体、共享关联、个人导入 keep 语义、显示语言回退。

## 修复

1. 无编号单作品不再因音轨名不同被拆卡：`Prologue`/`Main Story`/`Epilogue`、`Opening`/`Ending`、`表妹上/中/下`、`01..03 + Bonus` 保持一卡。
2. 尾随数字只在形成 1..N 连续序列时视为音轨号；`Room 101`/`Room 102`、有标题的 `T001`/`T002` 与裸 `T001`/`T002` 保持独立。零填充编号不剥离。
3. `第1章`/`第2章` 是作品内章节，不再成为单集；`第1話`/`第2集` 仍是单集。`Episode 1 (MP3)` 折叠进相邻的 `Episode 1`。
4. 已评价的 creator 卡只保留 episode、格式、附属与分轨子目录；新的无编号作品获得自己的卡。
5. 有限扫描/刷新重写整个根后，未再出现的卡标为 `missing`，保留 id、work 关联与个人记录；再次出现恢复 `available`。
6. 全量索引最终阶段退役“根不再拆分”的旧 split 卡；每批最多 100 个根，带 work/系列/别名的根跳过。
7. 404/410 记为确定缺失（`empty` + `not_found`），不再让整源永久 `partial`；刷新路径同样标 `missing`。
8. sidecar 证据只在该卡范围完整观察后清除；partial 扫描保留证据并清空失败/重试字段。队列查询使用 partial index，写入失败不再无 tick 空转；cleanup 游标在末尾回卷以重试 uncertain 卡；旧代 `source_catalog_audio_root` 回收。
9. 目录页与 sidecar 共用同一 origin 速率令牌。
10. `keep` 导入只写备份中确实存在的字段；同一 shared identity 的任一成员冲突保护整个身份。
11. 翻译使用读者选中的 edition 文本，既有字段只在对应同一源文本时复用；catalog 翻译沿 `relocated_to_id` 读取 successor 文件。
12. 共享关联拒绝已 relocated 的 catalog 对象（409 stale），写入使用 successor。
13. 显示语言偏好读取失败时保留上次成功选择，不再固定回退 `origin`。

## 验证

| 检查 | 结果 |
| --- | --- |
| `cd backend && go test ./... -count=1` | 通过，exit 0（32 个包含测试的包） |
| `make backend-format backend-vet backend-build` | 通过；lint 首次因默认 GOPROXY 超时，改用 `GOPROXY=https://goproxy.cn,direct` 后 `0 issues` |
| `go vet ./internal/httpapi ./internal/personal` | 通过 |
| 前端 `npm run typecheck:e2e`、`npm run lint`、`npm run build` | 通过（Node 24.19.0 / npm 11.17.0） |
| `npm run test:unit -- --run --maxWorkers=4` | 155 文件、874 测试通过 |
| 新增 H 用例 | `TestSourceCatalogPublishesNamedPartsAsOneCard`、`...NumberedReleaseWithBonus...`、`TestCatalogSplitModeKeepsNumberedTitlesDistinct`、`TestCatalogSplitModeKeepsBareCodesApart`、`TestSourceCatalogEpisodeFormatCopyStaysWithEpisode`、`TestSourceCatalogDeletedDirectoryBecomesMissing`、`TestSourceCatalogPartialRescanKeepsSidecarEvidence`、`TestSourceCatalogReviewedCreatorAdmitsNewNumberlessWork`、`TestSourceCatalogRetiresOrphanSplitCards` |
| `make sensitive-check` | 通过 |

## 未覆盖

- 真实 sources 上的分页/429/改名恢复未在本批重新构造；以现有 H/R 用例与部署后实跑为准。
- 浏览器功能用例未在本批重跑；前端仅执行类型、静态、构建与 874 项纯逻辑测试。
- 生产数据清理与索引终态在部署后记录于部署证据。
