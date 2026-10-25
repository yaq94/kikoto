# 22 合成目录清理 dry-run

> 本报告由合成 fixture 生成，不含部署数据、生产路径或真实作品编号。
> 历史报告中基于旧 alias 删除策略的生产数字不可用于 apply，也不在本文件中
> 复述。

## Fixture

- 来源：`TestCatalogPruneDryRunAndApply` 的迁移 SQLite 合成库。
- 保留测试编号：`RJ00000001`、`RJ00000002`（保留区间 `RJ00000000`–`RJ00000009`）。
- 目录树：合成根目录、一个白名单子目录、一个系列成员、一个 relocation
  目标与一个 source-only alias。
- 生成时间：测试运行时生成，不固定为生产时间。
- 批次指纹：`bce1eb7e217228b3`（对 fixture 名称、保留编号、候选/计划计数与
  原因分布做规范化 SHA-256 后取前 16 位）。

## 合成计数

| 项 | 值 |
| --- | ---: |
| 候选卡片（未通过准入） | 5 |
| 候选文件 | 6,013 |
| 候选 transfer refs | 5 |
| 计划删除卡片 | 2 |
| 计划删除文件 | 6,001 |
| 观察 | 4 |
| 目录 | 2 |
| audio roots | 1 |
| transfer refs | 2 |
| 保留的路径行（白名单或保留条目） | 4 |
| 白名单 | `/bulk/audio`（合成） |

## 原因分布

| 原因 | 卡片 |
| --- | ---: |
| `too_many_files` | 1 |
| `empty_audio_card` | 1 |
| `series_member`（跳过） | 1 |
| `relocation_target`（跳过） | 1 |
| `transfer_alias`（跳过） | 1 |

## Stale 检查

- 以同一份 fixture 重建报告后逐卡比较 files、audio、non-audio、observations、
  directories、audio roots 与 transfer refs；任一变化都返回
  `catalog changed since the dry-run report; regenerate it`。
- 回归覆盖：删除一个文件后 apply 被拒绝，卡片与个人记录保持不变。
- 旧报告即使列出 source-only alias，apply 也必须在删除前拒绝；测试用历史
  计划形状验证该路径。

## 历史记录

旧策略的 dry-run 曾基于停机的部署副本生成，并计划删除带
`personal_transfer_ref` 的 source-only 卡片。该策略已被 ADR-0006 取代，
历史报告不可 apply；本文件不保留其生产计数或路径。
