# RPC 运行控制验收

`scripts/test-fast.sh -run RPC`：真实 WebSocket 与受控 Runner，不依赖真实模型。Harness 的运行互斥另由 `test/assembly/run_control_test.go` 验证。

同一个 `/api/v1/rpc?token=...` 连接可发送：

```json
{"version":"v1","id":1,"method":"agent/run","params":{"content":"执行任务"}}
{"version":"v1","id":2,"method":"agent/status","params":{}}
{"version":"v1","id":3,"method":"agent/cancel","params":{"run_id":"从状态响应取得的 run_id"}}
```

- `agent/status` 返回 `{ "active": false }`，或 `{ "active": true, "run": { ... } }`。异步请求尚未进入 Harness 时也可能暂时没有活动运行；它不是历史结果查询。
- `agent/cancel` 返回 `cancel_requested`；true 只表示取消信号已发送，不保证任务已经停止。false 表示当前用户没有匹配的活动运行。调用者用户身份由服务端鉴权给定，参数不能覆盖。
- `agent/run` / `approval/command` 每连接最多一个正在执行的操作，多余请求返回错误码 `-32001`，不排队。运行期间状态和取消仍可接收；跨连接互斥由共享 Harness 负责。
- `agent/chunk` 保留 `chunk`，新增 `request_id` 关联原请求。响应可能交错，应按请求 ID 分发。
- 缺少取消 ID 返回 `-32602`；只实现旧 AgentRunner 的适配器仍可运行，但控制方法返回 `-32601`。
- 断线或写失败取消该连接发起的操作。写操作串行且有 10 秒期限；不强行终止忽略 context 的第三方代码。旧聊天 `/ws` 本轮未迁移。

本批静态检查通过，WSL 运行待验；不是生产 JWT 鉴权或慢客户端压力测试。
