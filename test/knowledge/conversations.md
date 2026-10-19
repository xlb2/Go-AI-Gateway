# 持久会话与轮次协议

MySQL 是知识库对话的历史来源，独立于 CLI 的 Redis 用户历史。访问层级为鉴权用户 → 资料库 → 会话 → 轮次。

## HTTP 接口

以下路径均位于 `/api/v1`：

| 方法与路径 | 行为 |
|---|---|
| POST `/libraries/:library_id/conversations` | 新建空会话 |
| GET `/libraries/:library_id/conversations?before=:id` | ID倒序，每页最多50项 |
| GET `/libraries/:library_id/conversations/:conversation_id` | 会话与ID正序轮次 |
| POST `/libraries/:library_id/conversations/:conversation_id/turns` | 接纳问题并流式回答 |
| POST `/libraries/:library_id/chat` | 旧无状态入口返回410 |

轮次请求示例：

```json
{"request_id":"00000000-0000-4000-8000-000000000001","expected_turn_id":0,"content":"请解释附件","source_ids":[1]}
```

request_id 为规范 UUID，同会话唯一；expected_turn_id 是客户端最后已知轮次 ID，空会话为0。客户端不能提交任意 messages、身份或授权字段。服务端加载已完成历史和原附件版本快照。

同请求 ID、同输入重试返回 replayed 与原轮次，不重调模型；同 ID 不同输入、旧 expected_turn_id 返回409。会话行锁覆盖查重、历史位置、附件读取与 running 记录创建，提交后才请求模型。

## 流与状态

NDJSON 顺序为 started、delta，最终为 done 或 error。只有 completed 持久化确认成功才发 done；生成失败保留已有部分回答。取消使用独立5秒收尾 context；写入结果未知时保留 running 待核对，不主动重放请求。

```text
running → completed / failed / canceled
running → interrupted（到期后在读取或接纳请求时处理）
```

模型请求超时2分钟、数据库轮次期限3分钟；过期旧 writer 不得完成轮次。刷新只读取记录，不自动恢复模型生成。期限回收是惰性处理；用户停止、断连与超时可能都表现为 canceled。

失败/取消/中断轮次展示在历史中，但不进入后续模型上下文。最多100条轮次记录；模型历史最多24消息、16000 rune，本轮最多4份附件、JSON 64KiB，带附件历史上下文合计256KiB。超限要求新建会话，不静默摘要。标题来自首轮最多60字，暂不提供重命名或删除。

## 执行日志

`knowledge_turn_events` 按轮次保存带格式版本的 MemoryDTO，执行事件与业务问答分开，不直接混入下一轮模型上下文。AppendTurnEvents 仅供持有服务端轮次 ID 的内部路径使用，事务锁检查 running 与期限，和 FinishTurn 互斥。

step 日志写失败终止执行；模型建流可能早于 step/start，因此日志失败不保证零模型调用。部分已发送文本不可撤回，但最终不能伪造 done。该路径没有直接复用旧用户级审批、子任务与恢复存储。

## 测试

```bash
bash scripts/test-fast.sh -tags knowledgeintegration -run 'KnowledgeConversation|KnowledgeChat|KnowledgeExecutionEvents|ConfiguredLoopStepWriteBarrier'
```

覆盖持久化重建、归属、附件快照、并发幂等、超期 writer、写入屏障、HTTP取消和事件顺序。页面刷新恢复依赖服务端记录；Node 预览夹具不能证明数据库持久化。
