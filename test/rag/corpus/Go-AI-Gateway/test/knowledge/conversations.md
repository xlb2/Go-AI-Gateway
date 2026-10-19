# 持久会话：契约与验收

2026-09-21 持久会话已由用户 WSL 定向8/全量147验收，0跳过。后续执行日志增量仍待验；不是完整 Harness 工具/审批接入。

## 执行日志增量

v3 迁移新增 `knowledge_turn_events`，外键指向业务 turn，按 `(turn_id,id)` 读取顺序；payload 保存带格式版本的 MemoryDTO。业务问答与执行证据分开：历史 API 仍只返回问答，事件不混入模型上下文，也未开放日志读取 API。数据库的轮次外键提供归属，不伪造 legacy runstate.Ref。

调用链：鉴权用户 → BeginTurn 接纳并落库 → writer 捕获该 turn_id → ownLoop step 事件 → MySQL → FinishTurn → done。AppendTurnEvents 仅供持有服务端轮次 ID 的内部调用；不是客户端授权接口。事件批次事务锁住 running 且未过期的轮次，与 FinishTurn 互斥。结束后的 writer 被拒绝。

step/start、completed、handoff 写失败均停止；已经输出的片段不能撤回，但最终不得发 done。首次模型建流早于 step/start，所以开始日志失败不保证模型零调用。取消时写入 context 也已取消，可能留下开放 step，业务 canceled/interrupted 状态仍是轮次结果；本批不增加自动恢复。没有将旧 Harness 的用户级 Store/审批/子任务直接接进来，仍单步无工具。

新增两项集成测试 `KnowledgeExecutionEventsHTTP`、`KnowledgeExecutionEventsFencing` 和一项核心 `ConfiguredLoopStepWriteBarrier`。用户 WSL 验证：

```bash
scripts/test-fast.sh -tags knowledgeintegration -run 'KnowledgeExecutionEvents|ConfiguredLoopStepWriteBarrier'
scripts/test-fast.sh -tags knowledgeintegration
```

预期3/150，实际结果待回传；下文8/147是上一批记录。

## 数据与接口

MySQL v2 迁移新增 knowledge_conversations、knowledge_chat_turns，沿用原迁移锁，v1 已有数据不修改。DDL 可重入，成功后记版本；不宣称 DDL 可事务回滚。

鉴权用户 → 资料库 → 会话 → 每轮请求。MySQL 是本路径历史的权威来源；Redis 旧 user:<uid> 历史不参与。

| 方法及路径（均在 /api/v1 下） | 行为 |
|---|---|
| POST /libraries/:library_id/conversations | 新建空会话 |
| GET /libraries/:library_id/conversations?before=:id | 按 ID 倒序，每页最多 50 |
| GET /libraries/:library_id/conversations/:conversation_id | 会话及按 ID 正序的轮次记录 |
| POST /libraries/:library_id/conversations/:conversation_id/turns | 接纳本轮问题并流式回答 |
| POST /libraries/:library_id/chat | 旧无状态入口返回 410，提示刷新页面 |

本轮输入：

```json
{"request_id":"00000000-0000-4000-8000-000000000001","expected_turn_id":0,"content":"请解释附件","source_ids":[1]}
```

request_id 使用规范 UUID，同会话唯一；expected_turn_id 为页面最后已知轮次 ID，空会话为 0。拒绝客户端 messages 等未知字段，客户端不决定历史、身份或授权。服务端加载已完成历史和当时附件快照；附件更新后不会悄悄替换旧轮次的依据。来源标题、版本号、版本 ID 在历史中保留。原始 prompt 仅内部使用，不通过历史 API 返回。

同一请求 ID、同一输入的重试返回 JSON replayed + 原 turn，不重调模型；同 ID 不同输入或旧 expected_turn_id 返回 409。同会话行锁覆盖请求查重、历史位置校验、附件读取和 running 记录插入，事务提交后才调用模型。并发控制为每会话，不承诺用户级跨所有入口并发配额。

新执行响应为 NDJSON：started（turn_id）、delta、done 或 error。只有 completed 写入确认成功才发送 done。模型初始化失败返回脱敏 HTTP 错误并收尾 failed；生成失败保存已有部分答案。取消收尾使用独立 5 秒写入 context。写失败不伪造结束，保留 running 待核对；提交结果不确定时同请求 ID 也不会主动重放。

## 状态与边界

```text
running → completed / failed / canceled
running → interrupted（期限到达后，在读取/接纳请求时标记）
```

模型调用超时 2 分钟，数据库期限 3 分钟。超期后的旧 writer 无法写 completed。不是后台常驻调度：断开请求会取消，刷新只读取记录，不重新运行模型。进程突然终止可能丢失尚未收尾的部分文本；恢复时标 interrupted，不自动生成答案。期限回收为惰性处理，不是定时任务。用户停止/网络断开/超时均可能体现为 canceled，目前不细分取消来源。

失败/取消/中断记录展示在历史里，但不进入后续模型上下文；只有 completed 的问答成对进入。最多 100 条轮次记录；模型历史仍受 24 消息、16000 rune 限制，本轮附件最多 4 份、JSON 64 KiB，带附件历史上下文总计 256 KiB。超限明确要求新建会话，不静默删除或摘要。自动标题来自首轮问题最多 60 字；暂不支持重命名、删除、跨设备选择同步。

页面 sessionStorage 仅保留令牌和最近选中的库/会话 ID，实际记录从服务端加载；退出会清除选择。浏览器旧版内存对话没有可信持久副本，不导入为服务端历史。部署需重建 API 并刷新页面。

## 验证

新增五项顶层测试全部位于 test/knowledge，需 knowledgeintegration：

| 测试 | 守护行为 |
|---|---|
| TestKnowledgeConversationPersistenceAndOwnership | 重建 Store 后读取、跨用户/库拒绝、旧附件快照、失败历史不进入下一轮 |
| TestKnowledgeConversationConcurrentAndDuplicate | 并发同请求仅一次接纳、参数冲突、运行互斥、旧页面追加拒绝 |
| TestKnowledgeConversationExpiredAndWriteBarriers | 超期中断、拒绝旧 writer、无效输入不落盘 |
| TestKnowledgeConversationHTTPWriteBarriers | 问题写失败不调用模型、答案写失败无 done、重复请求不重跑 |
| TestKnowledgeConversationHTTPCancellation | 请求取消后有界返回并在新 context 下保存取消状态 |

原 TestKnowledgeChatHTTPIsolationAndStream 已迁移新协议；原角色/附件契约测试保留。数据库使用随机独立测试库，沿用已有建库/删库测试账号要求。

```bash
scripts/test-fast.sh -tags knowledgeintegration -run 'KnowledgeConversation|KnowledgeChat'
scripts/test-fast.sh -tags knowledgeintegration
```

预期定向 8、全量 147，以实际结果为准。本地 go vet 带 tag 与 JS 语法检查不替代这些运行结果。

模拟浏览器已检查发送后刷新恢复、来源版本显示、1440/390 布局和零控制台错误。独立 Node fixture 的状态只是内存模拟，不证明 MySQL 持久化；新建/切换按钮操作被自动审批拒绝，未标浏览器通过。真实验收：新建两个会话分别提问，切换不串历史；刷新/重启 API 后仍可读取；停止后刷新显示取消或已完成的真实状态，不能永远显示生成成功。
