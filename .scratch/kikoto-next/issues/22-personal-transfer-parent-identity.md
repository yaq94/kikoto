# 22：个人备份的单集父级归属恢复

**What to build:** 备份携带单集必要的稳定父级身份，空库恢复保持评价对象及父级归属，不因父系列没有本人评价而丢失关系。

**Blocked by:** [16：个人数据导入导出](16-personal-data-transfer-compatibility.md)；迁移顺序上须等 21 的 099 落地，100 不得跳号。

**Status:** ready-for-agent
**Implementation:** in-progress；`728651a6` 已在 v2 identity 上加入可选 `parent`，但导入仍合成 `source_catalog_entry` 与 `catalog_series_member`，且系列详情会过滤 `file_source_id IS NULL`，恢复的单集不可见。审查裁决：保留 v2 格式（不升 v3），父级关系落到 `work.parent_work_id`，禁止合成来源成员。

**依据:** [正式规格](../spec.md)、[ADR-0004](../../../docs/decisions/ADR-0004-work-identity-and-personal-state.md)、[ADR-0006](../../../docs/decisions/ADR-0006-catalog-remediation-and-translation-consistency.md)。

**涉及文件:** `backend/internal/personal/transfer_format.go`、`transfer_export.go`、`transfer_identity.go`、`transfer_plan.go`、`transfer_import.go`、`backend/internal/httpapi/catalog_series.go`、work 父级迁移、对应测试、`docs/development/personal-transfer.md`、`docs/user/*/personal-data.md`。

## 验收归属

本票只负责 AC-24 的单集父级身份恢复子项；AC-24 的原始主责仍是 16。22 不替代 16 的编号、无码、全对象、旧格式和重复导入验收，也不重写共享关联流程。

## 数据模型

迁移编号固定为 100，位于 097、098、099 之后；098／099 尚未提交时不得预写 100，落地时必须同步迁移头与 `compat/100_dev.sql`：

```sql
ALTER TABLE work ADD COLUMN parent_work_id INTEGER REFERENCES work(id) ON DELETE RESTRICT;
CREATE INDEX idx_work_parent_work ON work(parent_work_id);
```

- `kind` 由 `personal_transfer_ref.object_kind`／catalog series 成员关系推断，不新增 `work.kind` 列；只允许 `kind=episode` 的 work 设置父级，父级必须是 `kind=series` 的 work。
- 导入 episode 时必须在同一事务把该 work 的主 `personal_transfer_ref.object_kind` 写成 `episode`；导出时若 `work.parent_work_id` 非空也应推断为 episode。否则空库导入后再导出会退回 `work`，往返身份失真。
- 应用层拒绝自引用、父级不是 series、父级为 episode、循环，以及一个 episode 有多个不同父级。
- 一个 work 只能有一个明确父系列。来源发现出现多个不同父系列时保留 null 并报告 `parent_conflict`，不猜测。
- 未知父级必须同时创建 `catalog_series` 行（`source_id NULL`、`source_key NULL`、`work_id` 指向父 work），不能只留下一个无法被系列入口识别的普通 work。已有 `catalog_series` 只补 `work_id`，不重复插入。
- 新建 `catalog_series` 的触发器会生成随机 `s:` ref。备份父 ref 若空闲，把该行改成备份 ref；若已指向同一 series，复用；若指向其他对象，报告 `parent_conflict` 并回滚该父级。`personal_transfer_series` 唯一索引不允许一个 series 保留两个 ref。
- 迁移回填只在 `catalog_series.work_id` 与成员 `source_catalog_entry.work_id` 都非空、不同时写入；由于 `catalog_series_member.catalog_entry_id` 是 UNIQUE，真正的多父级来自同一个 work 被多个 series 的 entry 指向，必须按 `work_id` 分组 `HAVING COUNT(DISTINCT series.work_id)=1` 才写，否则保留 null。缺失、矛盾与多父级必须写入可查询的报告载体，不得只打日志。
- `work.primary_code` 与 `logical_work.canonical_code` 在 069 后允许空值，非空值才有部分唯一索引；系列 work 可以继续使用空代码，但必须测试同一备份包含多个系列 work 时不触发唯一约束。若实现新增内部代码，它只能作为内部标识，不能进入编号解析或用户可见的发行编号。

**当前实现核对（`728651a6`）：** 该提交把 `parent` 放在 v2 的 identity 上，并在空库创建 `source_catalog_entry` 和 `catalog_series_member`。`catalog_series.go` 的详情查询仍要求 `source_catalog_entry.file_source_id IS NOT NULL`，所以导入后的系列卡不会列出该单集；这与本票的系列读取验收冲突。若最终保留 v2 格式，必须改为不伪造来源成员的父级关系并补系列详情读取；裁决已定：采用本节的 `work.parent_work_id`，删除合成 entry／member 路径；不再保留两套合同。

**已复现的导出缺口（`d77e37e2` 起，`728651a6` 后仍存在）：** 当来源 `source_catalog_entry` 尚未绑定 `work_id` 时，`exportIdentity` 的 parent 查询 `WHERE entry.work_id=?` 返回空，导出的 episode identity 没有 `parent` 且退回 `kind=work`，导入后静默创建独立 work，`issues` 为空（`relationships_not_imported` 字符串已不存在）。独立 SQLite 回放结果：导出 `parentPresent=false`、目标 `series=0 works=1 members=0`。现有 `TestPersonalTransferRestoresEpisodeParentAcrossEmptyLibraryRestart` 因先保存评价而绑定 `work_id`，掩盖了这条真实路径。修复必须同时覆盖“未绑定 work 的来源条目”导出分支，并断言目标库 `catalog_series` 存在、`GET /api/catalog-series/{id}` 列出该单集。

## 备份格式：v2 + 可选 `parent`

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
- `personal_transfer_ref` 每个 series 只允许一个 ref；父级有多个同义 `s:` 别名时，保留 `Parent.Ref` 作为主 ref，其余报告 `parent_alias_not_imported`，不能因为别名数量阻止父级恢复。
- 文件版本保持 `version: 2`；`kind=episode` 的 identity 可带 `parent`。带 `parent` 时必须通过父 ref、kind、标题校验；缺失或非法父级按 `parent_conflict`／`parent_required` 阻止该条目，不能降级成独立 work。
- 没有 `parent` 的 v2 旧文件按原行为导入，并报告该单集未恢复父级，不能标记为“父级已恢复”。v1 继续可读。不新增 `version: 3`，避免同一语义出现两套格式。

## 导入规则

1. 预览阶段先解析父 ref：已有父 work 直接复用；已知 catalog series 无 work 时登记 series work 并绑定 `catalog_series.work_id`；完全未知时按备份显式身份创建 series work 和 `catalog_series` 行（`source_id NULL`）。父级不创建 `catalog_series_member`，因为空库没有 `source_catalog_entry`；系列与单集的可见关系由 `work.parent_work_id` 和 `catalog_series.work_id` 保持一致。
2. 父级解析成功后创建／复用 episode work，并在同一事务写入 `parent_work_id`，同时把 episode 主 ref 的 `object_kind` 改为 `episode`。父级的 `s:` ref 必须保存到 `catalog_series` 行的 `personal_transfer_ref(series_id)`；父 work 只保存自己的 `w:` 主 ref。episode 只保存自己的 refs，不能把父级 `s:` ref 写到 work 行。
3. 父级缺失、冲突、多父级或循环时报告 `parent_conflict`／`parent_cycle`；该条目 blocked，不创建 episode work，不静默降级；其他条目可继续。
4. 同一备份中父系列也作为顶层 `BackupWork` 出现时，只创建一次父 work，把父系列自己的评分／备注／进度应用到同一对象；`Parent` 字段只提供最小身份，不能覆盖父系列已有个人状态。
5. 幂等：同一备份重复导入不重复创建父／子 work，多个 episode 共享同一父 ref 时只创建一个父级；中途失败整批事务回滚；不下载媒体，不读取他人记录。
6. 非空库已有不同父级时，报告冲突并保留原关系；不覆盖共享关系或他人记录。循环检测使用递归父链并设置最大深度，检测到环时整条父级写入回滚。

## 系列读取路径

- `catalog_series_member` 只表达来源目录发现的成员，仍要求非空 `catalog_entry_id`；空库恢复不能伪造成员行。
- 个人恢复的父系列通过 `work.parent_work_id` 列出子 episode；`catalog_series.work_id` 指向父 series work。`GET /api/catalog-series/{id}` 在没有成员行时必须回退到该父级关系，返回 `catalogEntryId=0`、`workId` 非空的 episode 视图。前端 `validSourceID` 目前拒绝 0，必须同时补一条按 `workId` 打开 episode 的路由，否则该条目不可点击。
- 来源系列同时有 `catalog_series_member` 和 `parent_work_id` 时按 work/entry 去重，不能重复列集；成员顺序优先使用来源 position，纯个人恢复按标题和 work id 稳定排序。
- 个人恢复的 episode 没有来源 entry，因此没有媒体可播放；系列卡仍可显示和评价，不能为它伪造路径或下载媒体。

## 验收清单

- [ ] 空库只有单集评价、父系列无评价，导出再导入后父级归属保留；父系列有独立评价时也只创建一个父对象并保留其个人状态。
- [ ] 导入后的父系列卡能列出该 episode；`catalogEntryId=0`、`workId` 非空，且不会伪造来源成员或媒体路径。
- [ ] 重复导入不重复创建；非空库已有不同父级时冲突可见且不被覆盖。
- [ ] 父级缺失、多个不同父级、循环、父 ref 指向错误对象、父 ref 已被其他 series 占用、v2 旧格式、权限和用户隔离均有 H/R 证据。
- [ ] 空库导入后再导出，episode 仍为 `kind=episode`，父系列仍为 `kind=series`；`s:` ref 属于 `catalog_series`，`w:` ref 属于 work。
- [ ] `docs/development/personal-transfer.md` 与五个 `docs/user/*/personal-data.md` 改为“v2 + 可选 parent”，说明父级、失败语义和“仅显式父级恢复”的例外。当前这些页面写的是 v3，与 `transfer_export.go` 的 `Version: 2` 不一致。
- [ ] 同一备份包含两个系列 work 时不会因空 `primary_code`／`canonical_code` 触发唯一约束；若使用内部代码，编号解析和用户界面不暴露它。
- [ ] 迁移后新装与旧库升级的 `work.parent_work_id` 和索引一致；已发布迁移不回写。
- [ ] 导入后系列与单集可独立评价，无媒体下载或共享越权变更。

**测试入口:** H、R，使用真实迁移 SQLite 与公开导入导出入口。

## Comments

`relationships_not_imported` 不能成为已确认父级静默丢失的完成状态。当前 `registerTransferWork` 对未知 episode 只创建独立 work；必须先完成父级数据模型和备份字段，再改导入路径。迁移编号在 096 落地后按连续链分配，不能抢占或跳号。
