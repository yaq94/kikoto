# 个人记录保留、冲突与 catalog alias

## 保留规则

- 格式卡退休时，同一用户在同一收藏列表的旧卡与目标卡都有已填写备注或非零排序且值不同，保留两张卡及两条记录，自动退休保持待处理。
- 目标收藏项为空备注或默认排序时可从旧行补齐；任何用户存在冲突时整卡不退休。不同用户的收藏列表和 transfer progress 不互相冲突。
- 同一用户的目标工作已有 `personal_transfer_progress` 时视为待审阅冲突，保留两张卡及两份进度。
- catalog prune 不物理删除仍带 `personal_transfer_ref` 的 source-only 卡片，报告中记为 `transfer_alias`；work-backed alias 仅在可重指向工作后删除卡片。过期报告执行时再次拒绝 source-only alias 批次。
- 已知但未登记的 catalog entry、catalog series 和 episode 在具备 `remote:track` 时各重建一个最小工作并回写原目录/系列；无权限时整体拒绝导入。不下载媒体、不创建共享关联或系列成员、不移动既有工作。
- 导入按原评价层级校验；catalog 单集不能冒充独立作品或系列，系列引用不能冒充作品，矛盾进入 `identity_conflict`。

## 证据

- `go test ./internal/httpapi -run 'TestSourceCatalog.*FormatCard|TestCatalogPrune|TestPersonalTransfer' -count=1 -timeout=15m`：退出 0，53.299s。
- `go test ./internal/personal ./internal/catalogprune -count=1 -timeout=10m`：退出 0，personal 19.974s；catalogprune 无测试文件。
- `go test ./internal/httpapi -count=1 -timeout=20m`：退出 0，418.824s。
- H/R 场景 `TestPersonalTransferRestoresKnownUnregisteredCatalogSeriesAndEpisodeAcrossRestart`：无码 catalog、episode、series 三类对象预览均需新建工作，普通用户无登记权限时 403 且零工作；管理员导入 3 条、重复导入仍 3 条、导出层级和备注一致、重启后仍 3 个工作且目录/系列关联保留；其他用户导出为空；无媒体、工作流或共享成员写入。
- 冲突场景覆盖收藏备注、收藏排序、空目标迁移、transfer progress 已有值、不同用户不冲突、任一用户冲突阻塞以及该用户解决后恢复退休。

## 未覆盖

- 未新增收藏项备注编辑 UI；冲突解决依赖本人现有数据操作或后续接口，本次只保证自动退休不丢弃记录。
- 未运行真实生产 prune、部署验收、浏览器功能测试或真实来源扫描。
