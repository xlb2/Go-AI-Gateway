# CLI 使用与实现交接

## 当前方向

CLI 是当前主入口，复用已有 Harness 和自研 Loop。Web 代码、路由与数据保留，暂停扩展；CLI 不自动接入 Web 的资料工具或 MySQL 会话。

## 启动与操作

在仓库根目录配置 `.env` 的 Redis 与模型参数后启动：

```bash
bash scripts/run-agent.sh -user 1
```

PowerShell 使用固定路径构建再执行：

```powershell
go build -o bin/gwagent.exe ./cmd/agent
.\bin\gwagent.exe -user 1
```

不需要启动 HTTP、MySQL 或 RabbitMQ，也不需要 JWT_SECRET。MCP 未配置时不连接，配置后连接失败则明确退出。没有 MCP 不等于没有工具：历史检索、内容存取、委派和防御提案仍为内置能力；尚无通用源码读取或命令执行工具。

| 输入 | 行为 |
|---|---|
| 普通单行文本 | 提交一轮，流式显示回答 |
| `/help`、`/exit` | 查看命令、退出 |
| `/reasoning on`、`/reasoning off` | 显示或隐藏服务商返回的推理字段，默认开启，仅本进程生效 |
| `auth:approve` | 查看待审批动作及确认码 |
| `auth:approve <确认码>`、`auth:reject` | 确认或拒绝动作 |
| `auth:status <提案ID>` | 查询执行记录 |
| Ctrl+C | 运行中请求取消并等待收尾；空闲时退出 |

保留终端原生滚动、选中复制和粘贴，不接管鼠标或剪贴板快捷键。当前是单行输入，多行粘贴按行处理，不具备全屏编辑、输入历史导航或 Markdown 渲染。

## 显示语义

- 启动栏显示用户、工作目录与真实隔离状态。本机直跑不代表有文件系统或网络隔离。
- `Model processing` 表示模型请求阶段；`Tool running` 来自实际工具调用路径。
- `Tool returned` 只表示调用返回，可能是拒绝或待审批，不能证明业务成功或结果已持久化。落盘失败仍通过错误路径报告。
- 工具参数与结果正文不输出到状态栏，子循环不混入父状态栏；摘要与审批直接执行尚无独立进度显示。
- 推理只取 SDK 的 `ReasoningContent`，对应服务商公开返回的 `reasoning_content`，不靠提示词生成替代内容，不代表完整内部思考。
- 真实终端用单行窗口展示最近一段推理，缓存最多120个 rune，按当前终端宽度保守裁剪；回答、工具状态或结束时清除。非终端或 `TERM=dumb` 只留收到推理的提示。
- `NO_COLOR` 关闭颜色但保留单行更新。`/reasoning off` 不改变模型推理配置或计费，也不停止服务端生成。

## 调用链与边界

`cmd/agent` 装配 `harness.NewFromEnv`，`cli.Run` 调用已有审批入口或 `RunAgentTurn`，最终进入 `ownLoop`。CLI 不维护第二套执行循环，也不改变工具执行前的持久化屏障。

Loop 的临时进度通过每轮 context 观察器传给 CLI。Harness 把推理分片和回答分别转发；CLI 对状态和文本输出互斥写入。推理文本不混入最终 assistant 回答历史；SDK 在当前工具循环内的消息拼接与回传保持原逻辑。

默认 `user=1` 是本地存储身份，不是登录认证。同一 user 复用原 Redis 历史；运行锁仅限同一 Harness 实例，不要用同一 user 并发启动多个 CLI/API 进程。取消依赖模型与工具遵守 context，不保证强制终止不合作的工具。

旧“网关保安”人设已改为协作助手。普通抱怨不应触发防御提案，只有明确处置请求才考虑该工具，仍须审批；提示词不是授权执行边界，实际检查由 Harness/guard 承担。

## OpenCode 接入

历史环境变量 `VOLC_ACCESS_KEY`、`VOLC_ENDPOINT_ID`、`VOLC_BASE_URL` 也用于 OpenAI 兼容服务。OpenCode Go 使用 `https://opencode.ai/zen/go/v1`，模型须支持 Chat Completions。

统一模型工厂仅对官方 HTTPS `opencode.ai` 注入本项目 User-Agent 和稳定的 `x-opencode-session`；会话标识由用户与会话确定性哈希生成，同会话不同轮次、子调用与摘要保持一致。Web 共用模型工厂，因此补入其真实 conversation 身份，但不迁移其存储。缺少身份时明确失败，不为相似域名注入头。

## 验证记录

本地只执行静态检查和固定路径构建，不运行 `go test`。本批 `go vet -tags 'knowledgeintegration rageval' ./...` 与 CLI 构建已通过。

用户此前提供 CLI 6项及较早全量172项通过记录，随后真实对话验证了模型调用、测试编号回忆、工具状态和推理字段。后续新增 OpenCode、工具进度、推理测试及单行窗口改动不被旧结果覆盖。

2026-09-22 提交前，用户在 WSL 执行带 knowledgeintegration 的完整回归，178个顶层用例通过，0跳过，覆盖本批累计代码与测试：

```bash
bash scripts/test-fast.sh -tags knowledgeintegration
```

窄终端、窗口缩放、实际复制粘贴、运行中取消和重启历史仍需实际交互验证。测试清单见 [CLI 验收](../test/cli/README.md)。
