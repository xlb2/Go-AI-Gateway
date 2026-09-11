# Go-AI-Gateway

> **给每一家大模型，装上"手脚、记忆和安全阀"。**

一个由 Go + Eino 打造的 **AI Agent 运行时（Harness）网关**。大多数框架只给你"模型 API"，我们给你的是**一个真正能干活的 Agent**——它会想、会调工具、记得跟你聊过什么、危险动作先问你批不批。

<!-- TOC -->
- [它是什么](#它是什么)
- [为什么是它](#为什么是它)
- [核心能力](#核心能力)
- [架构](#架构)
- [技术栈](#技术栈)
- [快速开始](#快速开始)
- [验证与测试](#验证与测试)
- [目录结构](#目录结构)
- [两个关键设计](#两个关键设计)
- [Roadmap](#roadmap)

## 它是什么

大语言模型本质上是一个**无状态的纯函数**：喂它一段文本，它吐一段文本，仅此而已——没有手（不能执行工具）、没有记忆（不记得上句说过啥）、没有安全阀（分不清危险指令）。

**Go-AI-Gateway 就是包在这个"聪明大脑"外面的整个身体**：

- **眼睛**：把系统提示、工具清单、历史对话组装成上下文，喂给它看；
- **手**：它说"我要调工具"，就真的去执行；
- **记忆**：它"记得"之前聊过什么、调过哪些工具——因为每一轮都从日志里重建出来；
- **安全阀**：它想干高危的事，先挂起、问你批不批，批了才真的执行。

一句话：**把"会说话"升级成"能办事"。**

## 为什么是它

在"人人都在调 LLM API"的红海里，真正的分水岭不是"接哪家模型"，而是**你接回来的模型能不能稳定、安全、可恢复、可回归地干活**。我们选择自己搭 harness，而不是躺在第三方 Agent 框架上，因为：

- **可替换**：模型换个厂牌、记忆换个存储，一行核心代码不用动；
- **可追溯**：模型看到的每一句话，都能从日志里逐字节重建——审计、回放、分叉都有据可查；
- **可掌控**：审批链、沙箱边界、状态机，全部握在自己手里，不黑盒；
- **可回归**：本地假模型让端到端验证从 **10 分钟压到秒级**——能随手验，才敢随手改。

## 核心能力

- **ReAct Agent 循环** —— 模型「想 → 调工具 → 看结果 → 再想」，直到给出答案，全程流式推给前端。
- **Prompt 组装器官** —— 系统提示不是一坨写死的字符串，而是「身份 / 人设 / 工具指引 / 动态上下文」按 `order` 排序拼接；**工具指引里的工具名从注册表实时取**，加了工具提示词自动跟上。
- **人在回路高危审批** —— 模型触发防御动作不直接执行，**挂起**等管理员回 `auth:approve` / `auth:reject`；**提案里带着批准后要跑的命令**，批准即经沙箱真实执行，并落一条审计事件。
- **工具执行流水线** —— 一次工具调用要穿四道关（策略 → 单调守卫 → 超时执行 → 结果脱敏），不是直接落到工具体上。
- **事件溯源式记忆** —— 唯一一份只追加日志是"唯一真相"，喂给模型的历史是**从日志投影**出来的；滚动摘要压缩 + **折叠快照**让读取始终只扫尾部，不随对话变长而变慢。
- **工具调用配对喂回** —— 每次工具调用和结果按 callId 成对落盘、成对喂回；悬空的一律丢弃（pair-or-drop）。
- **子智能体 / 溢出存储 / MCP** —— 并行 fan-out 且上下文隔离；大内容外存只留定位符；外部 MCP 工具桥进统一注册表。
- **全双工流式推流 + 异步削峰** —— WebSocket 打字机体验；RabbitMQ 隔离接入层与算力层；90s 无心跳连接自动释放。

## 架构

```
请求 ──JWT──► handler（HTTP/WS/gRPC 接入层）──► appserver（JSON-RPC v1 双向契约）
                                                      │
                                                      ▼
     ┌───────────── harness 编排层（RunAgentTurn 一轮对话的唯一入口）─────────────┐
     │                                                                          │
     │  ① prompt    组装系统提示：身份 / 人设 / 工具指引，按 order 拼接            │
     │  ② session   压缩 → 折叠 → 投影历史（只追加日志是唯一真相）                │
     │  ③ agent     Eino react 固定内核：模型 ⇄ 工具，流式输出                    │
     │  ④ guard     工具流水线四道关：pre 策略 / guard 单调 / execute / post 脱敏  │
     │  ⑤ approval  高危动作挂起 → 人批准 → sandbox 真实执行 → 落审计事件         │
     │  ⑥ hooks     横切插槽：pre 拦截（waterfall）/ post 观察                    │
     └──────────────────────────────────────────────────────────────────────────┘
                │                          │                        │
             Redis                     MySQL(GORM)             RabbitMQ
     日志 / 状态 / 折叠指针 / 溢出     业务落盘 / 游标          削峰 / 异步
```

**一条消息的一生**（就是 `RunAgentTurn` 的真实顺序）：

1. **pre 钩子** —— 敏感词 / 超长输入等横切拦截，拦下就直接返回，不进 agent；
2. **系统提示** —— 由 prompt 器官组装；它是 log-only 事件，且**每会话只写一次**；
3. **压缩** —— 把窗口外的早期对话压成一条滚动摘要（best-effort，失败跳过本次）；
4. **投影历史 + 落盘用户消息** —— 从日志折叠水位之后的尾部投影；悬空工具结果丢弃；
5. **Eino react 循环** —— 模型 → 工具（穿四道关）→ 结果喂回 → 直到给出答案，边生成边推流；
6. **落盘回复 + post 钩子** —— 完整回复进日志，post 钩子观察。

## 技术栈

| 层次 | 选型 |
|---|---|
| 语言 / 编排 | Go / Eino `react.Agent`（固定内核，不拆） |
| 模型接入 | 火山引擎（OpenAI 兼容协议，可换成任何兼容端点） |
| 记忆 / 状态 | Redis（只追加事件日志 + 折叠指针 + 挂起状态机） |
| 持久化 | MySQL + GORM |
| 消息队列 | RabbitMQ（接入层 ⇄ 算力层物理隔离） |
| 通信 / 鉴权 | WebSocket 长连接 + JSON-RPC v1 + JWT |

## 快速开始

### 方式 A：不花一分钱，先跑起来（推荐首次体验）

```bash
# 1. 起基础设施（Redis / MySQL / RabbitMQ）
docker compose up -d

# 2. 起本地假模型（OpenAI 兼容替身，不需要真实 key）
go run ./cmd/fakemodel

# 3. 另开终端，把模型指向假模型后启动网关
#    .env 里改成：VOLC_BASE_URL=http://127.0.0.1:9099/api/v3
go run ./cmd/api
```

业务代码**一行不用改** —— 这正是把模型适配做成一条"缝"换来的好处。

### 方式 B：接真实模型

```powershell
Copy-Item .env.example .env   # 填 VOLC_ACCESS_KEY / VOLC_ENDPOINT_ID
go run ./cmd/api

# 注册登录拿 token，连终端调试客户端
Invoke-RestMethod -Method Post -Uri "http://localhost:8080/api/v1/user/register" -ContentType "application/json" -Body '{"username":"alice","password":"123456"}'
$t = (Invoke-RestMethod -Method Post -Uri "http://localhost:8080/api/v1/user/login" -ContentType "application/json" -Body '{"username":"alice","password":"123456"}').token
go run ./cmd/radar -token $t
```

> 体验人在回路：发一句"气死我了！我要投诉！"触发防御挂起，再回 `auth:approve`——
> 你会看到真实的退出码和执行输出，而不是一句"已激活"。

## 验证与测试

> 这是"玩具"和"能用"的分界线：**不敢回归就没法改，改不动就永远是玩具。**

| 命令 | 用途 | 耗时 |
|---|---|---|
| `scripts/test-fast.sh` | 秒级回归：假模型 + 真 Redis，**不需要真 key、不烧 token** | ~5 秒 |
| `scripts/fake-model.sh` | 起假模型（让整个服务跑在确定性场景上） | — |
| `scripts/test-real.sh [pump]` | 真模型端到端 6 段冒烟（`pump` 只跑"灌对话逼压缩"） | 几分钟 |
| `scripts/reset-state.sh [uid]` | 重置 `agent:*` 键与审批审计产物，从干净状态开始 | — |

测试覆盖的不变量（`test/e2e`，7 个用例）：

- 悬空的 `tool/result` 不进投影（pair-or-drop）；
- 折叠后数组下标 ≠ seq，**遮蔽区间必须跟着偏移**（否则区间静默错位）；
- `system/prompt` 每会话只写一次（3 轮之后仍是 1 条）；
- `tool/call` 与 `tool/result` 数量恒等，且每条结果都能找到配对的调用；
- 审批挂起**必须带可执行命令**；批准前不许执行；批准后恰好执行一次；重复批准不重复执行；拒绝不执行但仍留审计。

设计原则（借自 dsh 的 testing 文档）：**只 mock LLM**，Redis 用真的；**断言落到 Redis 的事件序列**，不断言模型回复的文案；外部依赖不可用就 **skip 而不是 fail**。详见 [`test/README.md`](test/README.md)。

## 目录结构

```
cmd/api             网关入口（装配 + 优雅停机）
cmd/radar           WebSocket 终端调试客户端
cmd/fakemodel       本地假模型（OpenAI 兼容，开发/测试用）
cmd/verify          6 段端到端冒烟（本地验证工具）
cmd/probe           只读扫描 Redis 记忆日志，统计压缩/溢出/事件分布
cmd/bench           压测工具
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
test/
  fakemodel         假模型库（场景驱动，可被服务或测试复用）
  e2e               端到端测试（假模型 + 真 Redis）
scripts/            本地开发与验证脚本（见上表）
```

## 两个关键设计

### 工具执行流水线

模型的一次工具调用不会直接落到工具体上，要穿四道关：

```
工具调用 → pre-execute（业务策略，可 allow/deny/ask）
        → guard（安全不变量，单调收紧：Deny > Ask > Allow）
        → execute（超时 + 重试 + 指标包住）
        → post-execute（结果脱敏/改写后交给模型）
```

两条纪律写在 `internal/harness/guard` 里：

- **单调**：后注册的规则只能收紧，永远放不开别人收紧的结果；
- **默认值按代价选**：限流这种"人为、可恢复"的过载 → fail-open 放行；
  审批这种"没人介入就不该做"的动作 → fail-closed 拒绝。

### 审批不是打印一行日志

`auth:approve` 之后是真的走沙箱执行器把命令跑起来，并把**退出码和输出回执**给你；同时落一条
`audit/action` 审计事件（log-only，不喂模型），可追溯谁批的、批了什么、结果如何。

一个刻意的选择：**流水线不拦 `execute_system_defense` 这个工具调用**。它只是"起草提案"、本身不高危；
真正高危的是批准后执行的命令，所以不变量落在**执行点**而不是工具调用点——拦错地方会直接破坏审批链。

## Roadmap

> 未完成前不会出现在"核心能力"里，绝不透支信用。

- **Token 预算替换条数窗口** —— 现在上下文窗口按"消息条数"算（`MaxHistory=20`），要改成按 token 占窗口比例触发压缩。
- **崩溃恢复与中断语义** —— 开放轮次合成收尾（repair）、流中断打 `interrupted` 标记、LLM 重试并把重试记进日志。
- **真沙箱** —— seam 已就位（`Executor`），下一步把 `LocalExecutor` 换成容器隔离（`--network=none --read-only`），并如实上报隔离等级。
- **Step-level Checkpoint** —— 现在是轮次级日志，还缺"每跑完一个 step 存快照 + resume"。
- **策略配置化** —— guard 规则与 hooks 现在都是编译期注册，下一步做成配置驱动。
- **Web Console** —— 版本化双向协议驱动 agent，审批/进度可视化（app-server 协议已具备）。
- **Observability 深化** —— TraceID 全链路 + 直方图指标 + 多模型路由。

---

*Go-AI-Gateway 正在持续进化——从一个 IM 网关，长成一个真正可用的 AI Agent 运行时。*
