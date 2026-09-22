# 模型输入证据验收

2026-09-22 最新运行证据：用户WSL定向4项、带knowledgeintegration全量186项通过，均0跳过，覆盖下方待验记录。之后默认步数从10调整为20，属于后续改动，不包含在这次186项运行证据内。

2026-09-22：根据CLI试用反馈新增确定性回归。本地go vet（含knowledgeintegration/rageval）和CLI构建通过；运行待用户WSL，不将模型自述当作已确认缺陷。

| 用例 | 核查证据 |
|---|---|
| TestStorageSpillThresholdPerResult | 连续10条2000 rune结果不外存，2001 rune外存；日志转换不修改原消息 |
| TestModelEvidenceLargeResultAcrossTurns | 真Redis保存大内容；模型调用load后，实际SDK请求含完整正文；下一轮历史为定位符，再次load的本轮请求仍有完整正文 |
| TestModelEvidenceReasoningRoles | 推理分片进入独立回调，同轮工具续调保持assistant.reasoning_content；不混入content、user正文、事件内容或下一轮历史 |
| TestWorkspaceEmptySearchFields | 无命中明确返回lines空数组与skipped=0；隐藏文件跳过返回skipped=1 |

假模型请求快照增加role/content/reasoning_content，只供测试读取；请求摘要JSON接口不输出消息正文，不新增真实服务请求dump或生产日志。

```bash
bash scripts/test-fast.sh -run 'ModelEvidence|StorageSpillThreshold|WorkspaceEmpty'
bash scripts/test-fast.sh -tags knowledgeintegration
```

本批定向预计4项，累计全量预计186项。覆盖假模型、实际SDK、Harness与真Redis的指定场景；不代表能证明某次历史真实模型回答中的每句话，也不验证模型对来源的理解正确。

外存是历史表达与当前执行消息的区别：当前轮工具返回正文直接交回模型，持久化的超长结果记录定位符；再次取回不等于模型仍只能看到定位符。推理字段的当前轮回传同样不等于作为用户消息持久化。
