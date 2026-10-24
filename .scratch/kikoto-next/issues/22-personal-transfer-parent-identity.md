# 22：个人备份的单集父级归属恢复

**What to build:** 备份携带单集必要的稳定父级身份，空库恢复保持评价对象及父级归属，不因父系列没有本人评价而丢失关系。

**Blocked by:** [16：个人数据导入导出](16-personal-data-transfer-compatibility.md)。

**Status:** ready-for-agent
**Implementation:** pending

**依据:** [正式规格](../spec.md)、[ADR-0004](../../../docs/decisions/ADR-0004-work-identity-and-personal-state.md)、[ADR-0005](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)。

## 验收范围

**主责:** AC-24；**共同:** AC-09、AC-13、AC-14、AC-15。

- [ ] 仅有单集评价的空库往返保留稳定父级身份与归属；重复导入不重复创建。
- [ ] 备份只重建明确给出的必要身份与关系；矛盾、缺失或越权引用不被静默降级或猜测关联。
- [ ] 既有共享关系、他人记录、个人字段冲突及旧编号格式保持兼容。
- [ ] H/R 证明导入后系列与单集可独立评价、无媒体下载或共享越权变更。

**测试入口:** H、R，使用真实迁移 SQLite 与公开导入导出入口。

## Comments

本票不改变作品关联或拆分的本人确认规则。`relationships_not_imported` 不能成为已确认父级静默丢失的完成状态。
