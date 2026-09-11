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
  harness/          自建 Agent Harness —— 固定内核 + 外包器官（每个器官一个子包，可单独替换）
    agent           Eino 固定内核：模型适配器缝 + ReAct 循环 + 工具注册表
    prompt          Prompt 组装：带 order 的片段排序拼接（身份/人设/工具指引/动态上下文）
    session         记忆：只追加事件日志 + pair-or-drop 投影 + 滚动摘要压缩 + 折叠快照
    approval        审批：人在回路挂起状态机（提案带可执行命令，批准后交给沙箱）
    guard           把关：滑动窗口限流 + 工具执行流水线四道关（pre/guard/execute/post）
    sandbox         沙箱 seam：Executor 接口 + 本机占位实现（生产换容器隔离）
    hooks           钩子插槽：pre 拦截（waterfall）/ post 观察
    subagent        子智能体：delegate_task / delegate_tasks 并行 fan-out + 深度上限
    spill           溢出存储：大内容外存留定位符（store/load_large_content）
    mcp             MCP 集成：外部 server 工具桥进统一注册表（mcp__server__tool）
    appserver       app-server 协议：JSON-RPC v1 双向契约（流式推送 + 审批回调）
    metrics         可观测性：计数/求和/仪表 + Prometheus 文本导出
  handler           HTTP/WS/gRPC 接入层 + JWT/限流中间件
  service           业务逻辑（消息路由、未读游标）
  dao / model       MySQL 数据访问 / 表结构
  bootstrap         依赖装配与基础设施初始化
  config            配置加载（环境变量 > .env > 默认值）
```

## 工具执行流水线（M5）

模型的一次工具调用不会直接落到工具体上，要穿四道关：

```
工具调用 → pre-execute（业务策略，可 allow/deny/ask）
        → guard（安全不变量，单调收紧：Deny > Ask > Allow）
        → execute（超时 + 重试 + 指标包住）
        → post-execute（结果脱敏/改写后交给模型）
```

两条纪律写在 `internal/harness/guard` 里：

- **单调**：后注册的规则只能收紧，永远放不开别人收紧的结果。
- **默认值按代价选**：限流这种"人为、可恢复"的过载 → fail-open 放行；
  审批这种"没人介入就不该做"的动作 → fail-closed 拒绝。

## Roadmap：接下来要造的器官

> 每个器官对应一个真实的 agent 工程能力；未完成前不会出现在"核心能力"里，绝不透支信用。

- **Sandbox Execution（真沙箱）** —— seam 已就位（`Executor`），下一步把 `LocalExecutor` 换成容器隔离（`--network=none --read-only`）。
- **Step-level Checkpoint（逐节点 checkpoint）** —— 现在是轮次级日志，还缺"每跑完一个 step 存快照 + resume"。
- **策略配置化** —— guard 的 pre/guard 规则与 hooks 现在都是编译期注册，下一步做成配置驱动。
- **Web Console（可视化控制台）** —— 版本化双向协议驱动 agent，审批/进度可视化（app-server 协议已具备）。
- **Observability 深化** —— TraceID 全链路 + 直方图指标 + 多模型路由。

---

*Go-AI-Gateway 正在持续进化——从一个 IM 网关，长成一个真正可用的 AI Agent 运行时。*
