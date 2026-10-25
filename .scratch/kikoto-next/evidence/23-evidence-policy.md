# 23 公开证据策略

本文件是 23 号票“文档与证据规范”子项的规则说明。它约束后续证据怎么写，
不替代 21、22 或 24 的功能验收。

## 允许进入 Git 的证据

- 合成 fixture 的名称、保留测试编号（`RJ00000000`–`RJ00000009`）、通用域名、
  回环地址、固定时间与临时目录占位符。
- 规模量级（例如“约 4.2 万张卡片”“约 11 万条目”）与对应的结论、退出状态。
- 命令形状、测试名称、迁移编号、schema 版本和可复核的 SQL／查询计划。
- 通用模板前缀 `<repo>`、`<bench-dir>`、`<logs-dir>`、`<source-root>`；必要时
  可用 `/path/to/...`，但只能出现在明确标注为通用命令模板的代码块内。

## 不进入 Git 的内容

- 生产或本地绝对路径：以根斜杠开头的服务器路径、用户主目录、挂载点、盘符
  路径，以及构建目录名。
- 真实发行编号（`RJ`／`VJ` 加四位以上数字），保留测试区间除外。
- 生产记录标识：`entryId`／`sourceId`／`entry_id`／`source_id` 后接三位以上
  整数；`0`–`3` 的合成 id 或明确标注 `fixture`／`synthetic`／`合成` 的行除外。
- 部署内部信息：数据库文件的具体字节数、`sha256` 后的 64 位摘要、`docker logs`
  重定向到具体日志文件路径。
- 真实部署计数与游标。需要表达规模时改写为量级，并在文件顶部注明
  “历史证据，非当前候选”。

真实部署数字只保留在受限运维记录。需要对外证明行为时，优先引用合成测试、
命令退出状态和量级结论，而不是回放生产行。

## 两层检查

- `make sensitive-check` 扫描本次变更行与未跟踪文件，用于阻止新提交把密钥、
  服务地址或敏感文件带进仓库。它不证明历史已跟踪内容已经脱敏。
- `make tracked-evidence-check` 用 `git ls-files` 枚举已跟踪的
  `.scratch/kikoto-next/evidence/**` 与 `.scratch/kikoto-next/*.md`，逐文件全文
  扫描。发现项输出 `文件:行号:规则名` 并以 1 退出；`--list` 只输出扫描文件数。

允许列表是 `scripts/tracked-evidence-allowlist.json`。每条必须有 `file`、
`rule`、`pattern`、`kind` 与 `reason`，只接受明确合成占位符或通用模板前缀。
扫描结束时逐条核对实际命中；未命中的条目按 `stale-allowlist-entry` 报错，
避免留下永远不生效的豁免。

## 复核顺序

1. `node --test scripts/check-tracked-evidence.test.mjs`
2. `node scripts/check-tracked-evidence.mjs`
3. `node scripts/check-doc-links.mjs`
4. `node scripts/check-doc-locales.mjs`
5. `git diff --check`
6. `make sensitive-check`

发布候选还需要 24 号票的同一 SHA 证据；上述检查通过只说明仓库内已跟踪证据
符合本策略，不替代功能、迁移或部署验收。
