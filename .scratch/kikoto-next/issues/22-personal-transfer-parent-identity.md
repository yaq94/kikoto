# 22：个人备份的单集父级归属恢复

**What to build:** 备份携带单集必要的稳定父级身份，空库恢复保持评价对象及父级归属，不因父系列没有本人评价而丢失关系。

**Blocked by:** [16：个人数据导入导出](16-personal-data-transfer-compatibility.md)。

**Status:** ready-for-agent
**Implementation:** pending；现有 v2 格式只有 episode 的 `Ref`／`Kind`／`Title`，没有父级字段，不能仅靠导入代码补齐。

**依据:** [正式规格](../spec.md)、[ADR-0004](../../../docs/decisions/ADR-0004-work-identity-and-personal-state.md)、[ADR-0006](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)。

## 验收归属

本票只负责 AC-24 的单集父级身份恢复子项；AC-24 的原始主责仍是 16。22 不替代 16 的编号、无码、全对象、旧格式和重复导入验收，也不重写共享关联流程。

## 数据模型

计划使用迁移 100（在 096、097、098、099 之后；按实际连续链调整）：

```sql
ALTER TABLE work ADD COLUMN parent_work_id INTEGER REFERENCES work(id) ON DELETE RESTRICT;
CREATE INDEX idx_work_parent_work ON work(parent_work_id);
```

- `kind` 由 `personal_transfer_ref.object_kind`／catalog series 成员关系推断，不新增 `work.kind` 列；只允许 `kind=episode` 的 work 设置父级，父级必须是 `kind=series` 的 work。
- 应用层拒绝自引用、父级不是 series、父级为 episode、循环，以及一个 episode 有多个不同父级。
- 一个 work 只能有一个明确父系列。来源发现出现多个不同父系列时保留 null 并报告 `parent_conflict`，不猜测。
- 未知父级必须同时创建 `catalog_series` 行（`source_id NULL`、`source_key NULL`、`work_id` 指向父 work），不能只留下一个无法被系列入口识别的普通 work。已有 `catalog_series` 只补 `work_id`，不重复插入。
- 新建 `catalog_series` 的触发器会生成随机 `s:` ref。备份父 ref 若空闲，把该行改成备份 ref；若已指向同一 series，复用；若指向其他对象，报告 `parent_conflict` 并回滚该父级。`personal_transfer_series` 唯一索引不允许一个 series 保留两个 ref。
- 迁移回填只在 `catalog_series.work_id` 与成员 `source_catalog_entry.work_id` 都非空、不同，且该 entry 只属于一个 series 时写入；多父级、缺失或矛盾记录只进入报告。
- `work.primary_code` 与 `logical_work.canonical_code` 在 069 后允许空值，非空值才有部分唯一索引；系列 work 可以继续使用空代码，但必须测试同一备份包含多个系列 work 时不触发唯一约束。若实现新增内部代码，它只能作为内部标识，不能进入编号解析或用户可见的发行编号。

## 备份格式 v3

```json
{
  "identity": {
    "ref": "c:...",
    "kind": "episode",
    "title": "Episode One",
    "parent": {
      "ref": "s:...",
      "kind": "series",
      "title": "Example Series",
      "references": []
    }
  }
}
```

- `BackupIdentity.Parent *BackupIdentity` 只允许 `kind=series`；父级不再嵌套 Parent；父级评分、备注、进度不进入该字段。
- 导出 episode 时，即使父系列没有本人评价，也必须导出父级的稳定 `s:` ref 和最小标题；父级 references 只保存为父级别名，不写入 episode 的 `References`。
- 父级的 `references` 必须都能解析为同一个 series；指向 episode、其他 work 或未知对象的引用报告 `parent_conflict`，不能保存到 episode。
- v3 的 `kind=episode` 必须有 `Parent`，且父 ref、kind、标题通过校验；缺失或非法父级按 `parent_conflict`／`parent_required` 阻止该条目，不能降级成独立 work。
- v2 旧格式继续可读；没有父级字段时按原行为导入并报告 `relationships_not_imported`，不能标记为“父级已恢复”。v2 没有父级信息不等于 v3 可以静默丢失父级。

## 导入规则

1. 预览阶段先解析父 ref：已有父 work 直接复用；已知 catalog series 无 work 时登记 series work 并绑定 `catalog_series.work_id`；完全未知时按备份显式身份创建 series work 和 `catalog_series` 行（`source_id NULL`）。父级不创建 `catalog_series_member`，因为空库没有 `source_catalog_entry`；系列与单集的可见关系由 `work.parent_work_id` 和 `catalog_series.work_id` 保持一致。
2. 父级解析成功后创建／复用 episode work，并在同一事务写入 `parent_work_id`。父级 refs 保存到父 work 的 `personal_transfer_ref`；episode 只保存自己的 refs。
3. 父级缺失、冲突、多父级或循环时报告 `parent_conflict`／`parent_cycle`；该条目 blocked，不创建 episode work，不静默降级；其他条目可继续。
4. 同一备份中父系列也作为顶层 `BackupWork` 出现时，只创建一次父 work，把父系列自己的评分／备注／进度应用到同一对象；`Parent` 字段只提供最小身份，不能覆盖父系列已有个人状态。
5. 幂等：同一备份重复导入不重复创建父／子 work，多个 episode 共享同一父 ref 时只创建一个父级；中途失败整批事务回滚；不下载媒体，不读取他人记录。
6. 非空库已有不同父级时，报告冲突并保留原关系；不覆盖共享关系或他人记录。循环检测使用递归父链并设置最大深度，检测到环时整条父级写入回滚。

## 验收清单

- [ ] 空库只有单集评价、父系列无评价，导出再导入后父级归属保留；父系列有独立评价时也只创建一个父对象并保留其个人状态。
- [ ] 重复导入不重复创建；非空库已有不同父级时冲突可见且不被覆盖。
- [ ] 父级缺失、多个不同父级、循环、父 ref 指向错误对象、父 ref 已被其他 series 占用、v2 旧格式、权限和用户隔离均有 H/R 证据。
- [ ] 同一备份包含两个系列 work 时不会因空 `primary_code`／`canonical_code` 触发唯一约束；若使用内部代码，编号解析和用户界面不暴露它。
- [ ] 迁移后新装与旧库升级的 `work.parent_work_id` 和索引一致；已发布迁移不回写。
- [ ] 导入后系列与单集可独立评价，无媒体下载或共享越权变更。

**测试入口:** H、R，使用真实迁移 SQLite 与公开导入导出入口。

## Comments

`relationships_not_imported` 不能成为已确认父级静默丢失的完成状态。当前 `registerTransferWork` 对未知 episode 只创建独立 work；必须先完成父级数据模型和备份字段，再改导入路径。迁移编号在 096 落地后按连续链分配，不能抢占或跳号。
