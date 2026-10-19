# 资料只读工具

资料工具由 Web 对话的 ownLoop 调用，用户与资料库通过服务端闭包绑定。模型参数不接受 owner/library，未知字段、非对象和尾随 JSON 拒绝；每次调用重新校验资源归属。

## 工具接口

| 工具 | 输入 | 输出 |
|---|---|---|
| `list_sources` | after，默认0 | 最多20项来源与当前版本ID，next_after为0表示结束 |
| `read_source` | source_id、version_id；offset默认0 | 固定版本原文、标题、范围及eof |
| `search_sources` | query、after | 当前版本关键词候选与位置，不含正文 |

读取按 Unicode 码点分页，每页最多4000字符。version_id 来自列表或搜索结果，后续分页保持相同版本，不随资料当前版本变化。

工具集共享每轮64KiB序列化成功结果预算，含 JSON 开销；预算不足返回明确错误。该预算与附件及会话历史预算独立。Web 路径最多8步模型调用，另受2分钟请求时限约束。

## 执行与证据

```text
鉴权 → 校验库/会话 → BeginTurn → 装配读取工具
模型工具请求 → tool/call 写入 → 业务读取 → tool/result 写入
模型回答 → completed 写入 → done
```

事件写入失败终止流程，不能仅凭回答文字判断工具执行或持久化成功。业务错误作为工具结果返回，SQL 原始错误不直接提供给模型。成功读取结果可投影为 [来源片段](evidence.md)，附件快照与主动读取证据分别保存。

下一轮加载已完成问答与原附件快照，不重放工具结果，需要原文时重新读取。数据库当前仍先取完整版本再分页，单文档受2MiB导入限制。

`NewReadingTools` 提供上述3个工具；`NewReadingToolsWithEmbedder` 可额外提供 [混合检索工具](hybrid.md)，当前未自动装入 Web 对话或 CLI。资料工具不提供通用命令、写入、审批、子任务或自动任务恢复。

## 测试

```bash
bash scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeReadingTools
```

覆盖归属、版本、分页、预算、假模型真实 HTTP/MySQL 工具循环以及事件写入故障。
