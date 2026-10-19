# 输入与模型用量观测

观测由每轮 context 携带，记录数值和元数据，不改模型输入或事件日志策略。

## 三种统计视图

| 视图 | 口径 |
|---|---|
| `Input estimate (main loop)` | 主循环每次逻辑请求的启发式输入估算 |
| `Usage (reported steps x/y)` | 完整读取的逻辑步骤中，带 SDK usage 的步骤数及已知用量 |
| `Model attempts (main/child/summary)` | 主调用、子调用、摘要的 SDK 尝试次数与已读用量，包含 Harness 重试 |

输入估算按 system、user、assistant、工具结果、reasoning、结构和工具定义分桶；重复发送的历史与定义重复计入。参数、ID和消息结构进入结构桶。工具定义估算失败标 unknown，文本估算不代表服务商线级序列化或多模态成本。

步骤 usage 通过消息拼接汇总，重复累计快照不重复计数；缺少 usage 不造零。该视图不包含完整的子调用、摘要和内部重试，应与尝试视图分开解释。

## SDK 尝试统计

包装顺序为 Loop → retry → accounting → SDK；摘要使用独立类别。每次调用 SDK 前计一次尝试，重试单独计数。收集器线程安全，仅保留数值，按 CLI 操作重置；子任务继承收集器但不刷新父工具进度。

- Main、Child、Summary 互斥，可以相加；不能再叠加步骤 usage。
- `WithUsage/Attempts` 表示已知覆盖，Prompt/Completion 按同次调用累计快照的最大值增量计数。
- 中断前已读 usage 保留，未返回或未消费的部分未知；覆盖完整也不等于最终账单已取得。
- 统计边界是 SDK 方法，不能保证看到 SDK、代理或服务端内部重试。
- observed-errors 只记录实际观测的错误，零错误不证明业务完成。
- 绕开 Runtime 的自定义模型需要显式接入收集器。

缓存字段存在性与最终费用无法确认时保持 unknown/null，不根据耗时推断缓存命中。

## 回归与评测

```bash
bash scripts/test-fast.sh -run 'ModelAccounting|ContextObservation|CLIToolProgress|ConfiguredLoop'
```

用例覆盖主/子/摘要分类、429重试、断流部分 usage、并发快照、跨轮隔离以及观测不修改实际输入。需要对比真实调用时，使用 [上下文基线](contextbaseline/README.md)。
