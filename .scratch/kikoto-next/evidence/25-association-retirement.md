# 25：共享关联与准备期退役身份

本证据对应同提交的 `shared_association.go` 与 `shared_association_publication_test.go`。依据 Spec AC-16、AC-18 和 ADR-0008 的实时身份边界。

`76c4a4c7` 的 Spec 复审继续发现：未登记的旧卡在准备期退役到未登记 successor 后，发布投影仍展示旧卡；关联登记只检查该投影，可能把新 work 绑回旧 alias。marker 后旧 URL 转到 successor，关联与评价无法随来源读回。

提交共享关联时在同一写事务读取该卡实时 `relocated_to_id`，准备退役的对象返回既有 `association_stale` 409，要求在发布后重新预览 successor。不修改公开目录快照，不把用户确认自动转到未预览的对象，不增加身份或丢弃记录。

- 红测 `TestSharedAssociationRetirementRequiresPublishedSuccessor`：旧实现返回 200/completed，而应拒绝过期主体；退出 1，日志 `reports/20261107-remediation/association-retirement-red.log`。
- 修复后定向测试退出 0。实际关闭数据库与 Server，在 marker 后重新预览并关联成功，再次实际重启，旧入口、新入口与另一关联对象均回读同一评分备注；拒绝阶段未新增 work 或重绑 alias，未请求媒体。日志 `association-retirement-green.log`。
- `go test -race ./internal/httpapi -run '^(TestSharedAssociation|TestCatalogReviewDuringRetirementKeepsSuccessorIdentity)' -count=1 -timeout=20m -v` 退出 0，12 个顶层用例通过，215.039 秒；包括并发预览、个人确认、系列层级、故障提交、已发布改名、恢复拆分及准备期评价。日志 `association-retirement-related-race.log`。

原始日志在仓库外；最终同候选审查、Full CI 和部署验收另行记录。
