# 25：完整门禁中的发布读与拆分保存

基点 `5f78ae45`；当前差异仅修正验收的观察边界，保留完整业务断言，不增加 retry 或超时。原始日志位于仓库外 `reports/20261107-remediation/`。

- `e9b1cd38` 的 Full run `42421535469`：coverage、static、前端、翻译、索引与大部分 F 通过，四个 race job 和 shared-split 失败。完整结果为 failure，不能作为可部署门禁。
- `TestCatalogDirectoryListingChecksUnknownAndStaleTabs` 原轮询工作表；在分页准备删除文件后、写入新文件前遇到 NULL。真实读取合同要求旧发布目录仍可用。本次改为经真实目录及 tracks HTTP 路由观察，仍要求两来源 available、检查时间非空、最终新音轨；每轮同时要求恰有一条音轨，任何短暂空目录都会失败。race 三次通过，40.985 秒，`root-published-availability-green.log`。
- shared-split 与共享关联遇到同类完成屏障缺失：提交前后状态文字都为等待其他用户。用例等待真实 decision 成功、核对 `myAllocation=left` 及已保存提示后 reload。重开后选择保留、本人分配、他人隔离与最终两边记录断言全部保留。完整 matching 三组通过，`root-split-save-green-r3.log`。前两次 runner 命令参数错误在测试启动前退出，单独保留日志，不计通过。

尚未解决的完整门禁问题：race 的保留维护十秒预算、finite 五分钟请求预算、grouping 写锁观测和整包超时。独立 CPU 探针正在记录，不能提高门槛或按已通过处理。

`e9b1cd38` 的隔离恢复实测 105→109→105 通过，作品 API 与六表逐行哈希一致，生产容器未变（`restore-e9b1cd38.log`）。实际 Index 单条试译、简繁共用模型结果及名称保护通过（`live-translation-e9b1cd38.log`）：日文样例首次生成/转换 1,266 ms，缓存切换 21 ms；原生简体只转换。第一次 LIVE 因测试 cwd 未包含迁移目录，在调用模型前失败；纠正 cwd 后通过。这些证据仅对应 e9b1cd38，新增 110 及最终候选仍须复验。
