# Go-AI-Gateway

> **给每一家大模型，装上"手脚、记忆和安全阀"。**

一个由 Go + Eino 打造的 **AI Agent 运行时（Harness）网关**。大多数框架只给你"模型 API"，我们给你的是**一个真正能干活的 Agent**——它会想、会调工具、记得跟你聊过什么、危险动作先问你批不批。

<!-- TOC -->
- [它是什么](#它是什么)
- [为什么是它](#为什么是它)
- [核心能力](#核心能力)
- [技术栈](#技术栈)
- [架构](#架构)
- [快速开始](#快速开始)
- [目录结构](#目录结构)
- [Roadmap：接下来要造的器官](#roadmap接下来要造的器官)

## 它是什么

大语言模型本质上是一个**无状态的纯函数**：喂它一段文本，它吐一段文本，仅此而已——没有手（不能执行工具）、没有记忆（不记得上句说过啥）、没有安全阀（分不清危险指令）。

**Go-AI-Gateway 就是包在这个"聪明大脑"外面的整个身体**：

- **眼睛**：把系统提示、工具清单、历史对话组装成上下文，喂给它看；
- **手**：它说"我要调工具"，就真的去执行；
- **记忆**：它"记得"之前聊过什么、调过哪些工具——因为每一轮都从日志里重建出来；
- **反射**：它想干高危的事，先挂起、问你批不批。

一句话：**把"会说话"升级成"能办事"。**

## 为什么是它

在"人人都在调 LLM API"的红海里，真正的分水岭不是"接哪家模型"，而是**你接回来的模型能不能稳定、安全、可恢复地干活**。我们选择自己搭 harness，而不是躺在第三方 Agent 框架上，因为：

- **可替换**：模型换个厂牌、记忆换个存储，一行核心代码不用动；
- **可追溯**：模型看到的每一句话，都能从日志里逐字节重建——审计、回放、分叉都有据可查；
- **可掌控**：审批链、沙箱边界、状态机，全部握在自己手里，不黑盒。

## 核心能力

- **ReAct Agent 循环** —— 模型「想 → 调工具 → 看结果 → 再想」，直到给出答案，全程流式推给前端。
- **人在回路高危审批** —— 模型触发防御动作不直接执行，**挂起**等管理员在终端回 `auth:approve` / `auth:reject`，物理级防呆。
- **事件溯源式记忆** —— 唯一一份只追加日志是"唯一真相"，喂给模型的历史是**从日志投影**出来的；旧记忆永久留存、可检索。
- **工具调用配对喂回** —— 每次工具调用和结果按 callId 成对落盘、成对喂回，模型下次仍"记得"自己查过什么、调用过什么。
- **全双工流式推流** —— WebSocket 毫秒级打字机体验，告别轮询延迟。
- **异步削峰 + 心跳强回收** —— RabbitMQ 隔离接入层与算力层；90s 无心跳连接自动释放，杜绝长连接 OOM。

## 技术栈

| 层次 | 选型 |
|---|---|
| 语言 / 编排 | Go / Eino `react.Agent` |
| 模型接入 | 火山引擎（OpenAI 兼容协议，可替换） |
| 记忆 / 状态 | Redis（只追加日志 + 挂起状态机） |
| 持久化 | MySQL + GORM |
| 消息队列 | RabbitMQ（接入层 ⇄ 算力层物理隔离） |
| 通信 / 鉴权 | WebSocket 长连接 + JWT |

## 架构

```
                         ┌──────────────────────────────────────┐
                         │            网关基座（Go）              │
                         │                                      │
 客户端 ──JWT──► WS 升级 ─┤  ws_handler ──► eino_agent ──► Redis │
                         │      ▲               │   ▲   ▲       │
                         │      │               │   │   │       │
                         │ ToUserID==999         │   │   │       │
                         │ (agent 对话入口)       ▼   │   │       │
                         │              react.Agent  │   │       │
                         │              循环+工具      │   │       │
                         │                    │       │   │       │
                         │            MessageModifier ▼   │       │
                         │            (钩子捕获工具调用)  │       │
                         │                          ▼   ▼       │
                         │              redis_memory ─ strategic │
                         │              只追加日志    redis_state │
                         └──────────────────────────────────────┘
                                          │              │
                                          ▼              ▼
                                      MySQL(GORM)    RabbitMQ
                                      落盘/游标      削峰/异步
```

**一条消息的一生**：
1. 落盘 `user/message` 事件（只追加，不裁剪）；
2. `GetHistory` 从日志**投影**最近 20 条（含成对的工具调用+结果）；
3. Eino react 循环：模型想 → 调工具 → 结果喂回 → 最终回答；
4. 每步经 `MessageModifier` 钩子落盘事件，`auth:approve`/`auth:reject` 走挂起状态机。

## 快速开始

```powershell
# 1. 起基础设施
docker compose up -d

# 2. 配置密钥（复制模板并填入你的 key）
Copy-Item .env.example .env   # 然后编辑 .env，填 VOLC_ACCESS_KEY / VOLC_ENDPOINT_ID

# 3. 启动网关
go run ./cmd/api

# 4. 注册登录拿 token，连终端调试客户端
Invoke-RestMethod -Method Post -Uri "http://localhost:8080/api/v1/user/register" -ContentType "application/json" -Body '{"username":"alice","password":"123456"}'
$t = (Invoke-RestMethod -Method Post -Uri "http://localhost:8080/api/v1/user/login" -ContentType "application/json" -Body '{"username":"alice","password":"123456"}').token
go run ./cmd/radar -token $t
```

> 体验人在回路：发一句"气死我了！我要投诉！"触发防御挂起，再回 `auth:approve` 看审批链路。

## 目录结构

```
cmd/api             网关入口（装配 + 优雅停机）
cmd/radar           WebSocket 终端调试客户端
internal/
  ai_service        Agent 核心：eino_agent(循环+工具) / redis_memory(记忆) / redis_state(挂起+限流)
  handler           HTTP/WS/gRPC 接入层 + JWT/限流中间件
  service           业务逻辑（消息路由、未读游标）
  dao / model       MySQL 数据访问 / 表结构
  bootstrap         依赖装配与基础设施初始化
  config            配置加载（环境变量 > .env > 默认值）
```

## Roadmap：接下来要造的器官

> 每个器官对应一个真实的 agent 工程能力；未完成前不会出现在"核心能力"里，绝不透支信用。

- **Self-built Hook Slots（自建 hook 插槽）** —— 通用 pre/post 插件点：敏感词、审计、埋点这类横切需求，插进去就行，不动业务代码。
- **Sandbox Execution（沙箱执行）** —— 高危工具调用进隔离容器，把"审批"补成"隔离"，安全从"问一句"升到"封起来"。
- **Sub-Agent Fan-out（子智能体）** —— goroutine 派发子 Agent 并行拆任务，上下文互不污染。
- **Context Compaction / Spill（上下文压缩与外存）** —— 超限时压成摘要、超大工具输出外存留定位符，告别粗暴截断。
- **MCP Integration（协议互通）** —— 对接外部 MCP 生态，工具进统一注册表，一个名字一个命名空间。
- **Web Console（可视化控制台）** —— 版本化双向协议驱动 agent，审批/进度可视化。
- **Observability（可观测性）** —— TraceID 全链路 + 指标 + 多模型路由。

---

*Go-AI-Gateway 正在持续进化——从一个 IM 网关，长成一个真正可用的 AI Agent 运行时。*
