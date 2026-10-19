# 模型输入与持久历史的证据测试

这些回归通过假模型的实际 SDK 请求快照与 Redis 事件，检查工具正文、推理字段和历史表达是否符合契约，不依赖模型自述判断执行。

| 测试 | 检查内容 |
|---|---|
| `TestStorageSpillThresholdPerResult` | 单条2000 rune不外存，2001 rune外存；日志转换不修改原消息 |
| `TestModelEvidenceLargeResultAcrossTurns` | 当前执行可见完整工具正文，历史保存外存引用，显式回读后本轮 SDK 输入获得正文 |
| `TestModelEvidenceReasoningRoles` | reasoning 独立回调，同轮工具续调保留服务商字段，不混入用户正文或下一轮持久历史 |
| `TestWorkspaceEmptySearchFields` | 无命中与 skipped 字段明确区分 |

```bash
bash scripts/test-fast.sh -run 'ModelEvidence|StorageSpillThreshold|WorkspaceEmpty'
```

请求快照仅用于假模型测试；摘要接口不输出消息正文，不新增真实服务请求转储。外存改变的是持久历史表达，不代表当前工具返回也只能看见定位符。

这些测试证明指定链路中的输入与事件行为，不证明模型理解来源或回答正确。
