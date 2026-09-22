# Go-AI-Gateway

## 当前主入口：CLI（2026-09-22）

完整操作、调用链和验证边界见 [CLI 使用与实现交接](docs/cli.md)。下方早期验收数字为历史记录，最新状态以该文档及 test/README.md 顶部为准。

提交前最新验收（2026-09-22）：用户WSL带knowledgeintegration全量178项通过、0跳过；本地静态检查和CLI构建通过。真实模型对话、工具进度及推理字段已由用户验证，单行推理窗口和复制粘贴等终端交互仍需实际验收。

推理显示默认开启：服务商返回独立 `reasoning_content` 时，用单行滑动窗口显示最近一段，按终端宽度截尾，切换到回答或工具状态时清除。非终端或 `TERM=dumb` 仅显示收到推理的提示，不输出全文。`/reasoning off` 隐藏，`/reasoning on` 恢复，仅本进程生效；不改变模型推理配置，也不代表获得完整内部思考。没有字段时不显示推理区，推理文本不写入最终回答历史。复制粘贴沿用终端原生操作；当前仍是单行输入，多行粘贴会被按行处理。

终端显示：启动栏展示用户、工作目录和实际隔离状态；You/Agent 分区，等待提示与结束耗时，取消和错误单独标记。真实终端启用颜色，`NO_COLOR=1` 或 `TERM=dumb` 关闭；重定向输出保持纯文本。保留滚动历史，当前仍为单行输入、原样流式文本，不包含 Markdown 渲染或全屏编辑器。

OpenCode接入修复：针对官方HTTPS地址自动发送稳定的`x-opencode-session`及`User-Agent: go-ai-gateway/0.1`，覆盖统一模型工厂的流式/非流式请求。同会话跨轮和子任务共享路由标识，不使用每次变化的run_id。Go的Base URL为`https://opencode.ai/zen/go/v1`，模型ID填入历史变量`VOLC_ENDPOINT_ID`，仅适用于Chat Completions模型。依据[官方客户端要求](https://opencode.ai/docs/go/#where-can-i-use-it)。静态检查/编译通过，新增3项回归及真实OpenCode调用待验；此前172项不覆盖本修复。

最新验收：用户WSL的CLI定向5项、带knowledgeintegration全量172项通过，均0跳过；已重建启动到输入提示符。覆盖下文回归待验描述；真实模型问答、终端取消及重启后的历史仍待验证。

直接通过终端使用已有Harness；Web代码和路由保留，暂停扩展。CLI只需Redis和模型配置（仓库根目录.env中的VOLC_ACCESS_KEY / VOLC_ENDPOINT_ID，可选VOLC_BASE_URL、REDIS_ADDR），不启动HTTP、MySQL或RabbitMQ，也不要求JWT_SECRET。

```bash
# WSL / Git Bash，在仓库根目录执行；脚本先编译再启动
bash scripts/run-agent.sh -user 1
```

```powershell
# PowerShell，在仓库根目录执行
go build -o bin/gwagent.exe ./cmd/agent
.\bin\gwagent.exe -user 1
```

输入文本后回车，回复流式输出。`/exit`退出、`/help`查看命令；运行中Ctrl+C取消当前调用并等待返回，空闲Ctrl+C退出。审批继续使用`auth:approve`、`auth:approve <确认码>`、`auth:reject`和`auth:status <提案ID>`，不自动批准。

默认user=1，复用对应的旧Redis历史；这是本地身份选择，不是用户认证。不要用同一user并发运行多个CLI/API进程，当前运行锁仅限实例内。Web的MySQL会话、资料工具未自动接入CLI。本批没有重写Loop或改造多会话存储。静态检查与编译已通过，5项新增回归及真实终端交互待WSL验收，见 [CLI验收](test/cli/README.md)。下方Web入口与早期能力说明保留为历史背景，最新验证状态见test/README.md。

> 资料工作台第一批：API 重建启动后打开 `/knowledge`，可创建个人资料库、导入 UTF-8 Markdown/TXT 并读取原文（每份最多 2 MiB）。持久化与页面运行验收待 WSL；范围及命令见 [资料模块验收](test/knowledge/README.md)。专题、笔记和 RAG 尚未实现。

> 登录配置：启动前必须设置 `JWT_SECRET`（至少 32 字节，建议使用独立随机值，不能有首尾空白），缺失或过短时 API 拒绝启动。可在 WSL 用 `openssl rand -hex 32` 生成并填入不受版本控制的 `.env`。更换密钥后旧令牌失效，需要重新登录；多实例必须配置相同密钥。本项目不提供默认密钥，也不会自动生成临时密钥。JWT 修复新增回归待用户 WSL 验证。

> **能力边界（2026-09-17）**：已有自研循环、事件日志、审批与恢复基础。压缩保真、主会话执行前写入确认、失败与取消传播、共享重试预算已改进；用户 WSL 快速回归 81 个顶层用例通过、0 跳过，压缩事实保留单样例通过。日志续答不保证副作用恰好一次；并发审批、子任务日志归属与 MCP 审批后的原工具执行链仍待补齐，验证范围见 `test/README.md`。

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
- [在公司电脑上开发（端点防护 / EDR）](#在公司电脑上开发端点防护--edr)
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

- **自研 Agent 循环** —— 模型「想 → 调工具 → 看结果 → 再想」，直到给出答案，全程流式推给前端。循环是**自己写的代码，不是框架黑盒**：Eino 只当模型适配缝 + 工具零件；`step` 是一等公民（可观测 / 可中断 / 为断点续跑铺路）。
- **Prompt 组装器官** —— 系统提示不是一坨写死的字符串，而是「身份 / 人设 / 工具指引 / 动态上下文」按 `order` 排序拼接；**工具指引里的工具名从注册表实时取**，加了工具提示词自动跟上。
- **人在回路高危审批** —— 模型触发防御动作不直接执行，**挂起**等管理员回 `auth:approve` / `auth:reject`；**提案里带着批准后要跑的命令**，批准即经沙箱真实执行，并落一条审计事件。
- **工具执行流水线** —— 一次工具调用要穿四道关（策略 → 单调守卫 → 超时执行 → 结果脱敏），不是直接落到工具体上。
- **模型调用重试** —— 429 / 上游 5xx / 连接抖动按指数退避重试（带抖动，防多会话同时重试的惊群）；**只重试"再试可能好"的错误**，参数错与鉴权错直接失败不白等。每次重试都落一条 `llm/retry` 事件，事后能回答"这一轮为什么这么慢"。
- **事件溯源式记忆** —— 唯一一份只追加日志是"唯一真相"，喂给模型的历史是**从日志投影**出来的；滚动摘要压缩 + **折叠快照**让读取始终只扫尾部，不随对话变长而变慢。写入侧带**不变量校验**（结构非法的事件当场拒绝）与**格式版本号**（读到更高版本宁可拒绝加载，也不用零值猜）。
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
     │  ③ agent     自研循环：模型 ⇄ 工具，流式输出（step 一等公民）             │
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
5. **自研 agent 循环** —— 模型 → 工具（穿四道关）→ 结果喂回 → 直到给出答案，边生成边推流；
6. **落盘回复 + post 钩子** —— 完整回复进日志，post 钩子观察。

## 技术栈

| 层次 | 选型 |
|---|---|
| 语言 / 编排 | Go + 自研 agent 循环（Eino 只当模型适配缝与工具零件） |
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
scripts/fake-model.sh

# 3. 另开终端，把模型指向假模型后启动网关
#    .env 里改成：VOLC_BASE_URL=http://127.0.0.1:9099/api/v3
scripts/run-api.sh
```

业务代码**一行不用改** —— 这正是把模型适配做成一条"缝"换来的好处。

### 方式 B：接真实模型

```powershell
Copy-Item .env.example .env   # 填 VOLC_ACCESS_KEY / VOLC_ENDPOINT_ID
scripts/build.sh
scripts/run-api.sh

# 注册登录拿 token，连终端调试客户端
Invoke-RestMethod -Method Post -Uri "http://localhost:8080/api/v1/user/register" -ContentType "application/json" -Body '{"username":"alice","password":"123456"}'
$t = (Invoke-RestMethod -Method Post -Uri "http://localhost:8080/api/v1/user/login" -ContentType "application/json" -Body '{"username":"alice","password":"123456"}').token
./bin/gwradar -token $t
```

> 体验人在回路：发一句"气死我了！我要投诉！"触发防御挂起，再回 `auth:approve`——
> 你会看到真实的退出码和执行输出，而不是一句"已激活"。

## 验证与测试

> 这是"玩具"和"能用"的分界线：**不敢回归就没法改，改不动就永远是玩具。**

| 命令 | 用途 | 耗时 |
|---|---|---|
| `scripts/build.sh` | 一次编译出全部命令行工具到 `bin/` | 几秒 |
| `scripts/test-fast.sh` | 秒级回归：假模型 + 真 Redis，**不需要真 key、不烧 token** | ~5 秒 |
| `scripts/run-api.sh` | 起网关服务（编译到 `bin/` 再执行） | — |
| `scripts/fake-model.sh` | 起假模型（让整个服务跑在确定性场景上） | — |
| `scripts/test-real.sh [pump]` | 真模型端到端 6 段冒烟（`pump` 只跑"灌对话逼压缩"） | 几分钟 |
| `scripts/reset-state.sh [uid]` | 重置 `agent:*` 键与审批审计产物，从干净状态开始 | — |
| `scripts/build.sh` 后执行 `./bin/gwprobe validate`（Windows 产物带 `.exe`，以脚本输出为准） | 体检存量日志：不变量违规 + 格式版本（只读） | 秒级 |

测试覆盖的不变量（`test/e2e` 26 个 + `sandbox` 14 个、`tokenmeter` / `retry` 各 6 个、`metrics` 4 个纯函数用例）：

- 悬空的 `tool/result` 不进投影（pair-or-drop）；
- 折叠后数组下标 ≠ seq，**遮蔽区间必须跟着偏移**（否则区间静默错位）；
- `system/prompt` 每会话只写一次（3 轮之后仍是 1 条）；
- `tool/call` 与 `tool/result` 数量恒等，且每条结果都能找到配对的调用；
- 审批挂起**必须带可执行命令**；批准前不许执行；批准后恰好执行一次；重复批准不重复执行；拒绝不执行但仍留审计；
- 审批回执**必须写明隔离等级**；要求隔离却拿不到时 **fail-closed 一次都不执行**；没有隔离时执行必须带警告；
- **20 条短消息既不压缩也不被截断**（它们很便宜）；几条长消息则必须触发压缩，且压完落在触发线以内；
- 单条消息就撑爆窗口时老实交给硬裁兜底，不硬造空摘要；
- 真实 usage 会落盘，并用来校准估算系数；
- 崩溃留下的**开放轮次**能被认出并补合成收尾（幂等、不改历史）；流被切断的回复标 `interrupted` 且不冒充完整回答；
- 429 两次能重试成功并留下 2 条 `llm/retry`；**400 参数错立刻失败**、一条重试记录都不留；
- 结构非法的事件（缺 `ToolCallID` 的 `tool/result`）**在写入时就被拒绝**；读到更高版本的日志明确报错，而**没有版本号的老日志照常读**；
- **执行计划只有 argv**：想交给 shell 解释器的计划会被拦下；防御动作走内置文件动作，带空格的路径与中文都不用转义也能落盘；
- **没有执行计划的挂起**（被流水线拦下的工具调用）批准后被**如实拒绝**，不假装执行；
- 审批是**两步**的（只敲 `auth:approve` 不执行、错误确认码不执行），过期提案明确回「已超时作废」而不是静默消失；
- 轮次耗时与工具耗时以**直方图**导出（`_bucket{le=…}` / `_sum` / `_count`），能看到 P50/P99 而不只是总和；
- 容器后端：隔离参数一个不少、镜像之后才是命令、只挂工作目录、外部命令**必须经 docker**（不裸跑）；配了 docker 但不可用时**拒绝一切执行**而不是退回裸跑。

设计原则（借自 dsh 的 testing 文档）：**只 mock LLM**，Redis 用真的；**断言落到 Redis 的事件序列**，不断言模型回复的文案；外部依赖不可用就 **skip 而不是 fail**。详见 [`test/README.md`](test/README.md)。

## 在公司电脑上开发（端点防护 / EDR）

公司电脑上的端点防护（EDR）经常会对 Go 编译产物报毒 —— **这不是代码有问题，是编译产物"长得像"而已**：
未签名、自带运行时、会开网络连接和子进程，这正是远控木马的画像。

而 `go run` 会让这件事**每次都发生**：它每次都生成一个**全新的临时 exe**
（`%TEMP%\go-buildXXX\...`），对 EDR 来说每次都是一个没见过的样本。

三条办法，按推荐顺序：

1. **在 WSL 里跑**（最有效，不需要 IT 权限）。Windows 的端点代理管不到 WSL 内部的 Linux 进程：
   ```bash
   # WSL 里（项目在 E 盘，通过 /mnt/e 访问）
   cd "/mnt/e/agent study/Go-AI-Gateway"
   scripts/test-fast.sh        # 秒级回归
   scripts/build.sh            # 编译 Linux 版到 bin/
   scripts/run-api.sh          # 起服务
   ```
   Redis / MySQL 仍走 Docker Desktop 暴露的 `localhost` 端口，直接能用。
   脚本已经做了 Windows / Linux 双平台适配（自动识别可执行文件后缀）。

2. **把三个目录加进 EDR 白名单**（需要 IT 权限，最彻底）：
   - Go 编译缓存：`%LOCALAPPDATA%\go-build`
   - Go 模块缓存：`%USERPROFILE%\go\pkg\mod`
   - 本项目产物：`<项目目录>\bin`

3. **不用 `go run`，改成"编译一次、复用同一个文件"**（本项目脚本已经全部这么做）：
   - `scripts/build.sh` 一次编译出全部工具；`scripts/run-api.sh` / `scripts/fake-model.sh`
     都是"先 build 到固定路径再执行"，不再产生新临时文件；
   - 秒级回归用 `scripts/test-fast.sh`：测试在**进程内**用 httptest 起假模型，
     **不启动任何独立服务**，是触发面最小的一条路。

> 顺带一条纪律：这台机器上**凡是 `cmd/` 下的工具，一律编译到 `bin/` 再执行**，
> 不要用 `go run`。踩过两次，两种报错都是它——
> `An Application Control policy has blocked this file`（AppLocker 按文件名拦）
> 和 `Operation did not complete successfully because the file contains a virus`（杀软拦）。

## 目录结构

```
cmd/api             网关入口（装配 + 优雅停机）
cmd/radar           WebSocket 终端调试客户端
cmd/fakemodel       本地假模型（OpenAI 兼容，开发/测试用）
cmd/verify          6 段端到端冒烟（本地验证工具）
cmd/probe           只读扫描 Redis 记忆日志，统计压缩/溢出/事件分布；`probe validate` 全量体检
cmd/bench           压测工具
internal/
  harness/          自建 Agent Harness —— 固定内核 + 外包器官（每个器官一个子包，可单独替换）
    agent           自研 agent 循环（step 一等公民）+ 模型适配器缝 + 工具注册表
    prompt          Prompt 组装：带 order 的片段排序拼接（身份/人设/工具指引/动态上下文）
    retry           模型调用重试：指数退避 + 抖动，只重试可重试错误，每次重试落 llm/retry 事件
    session         记忆：只追加事件日志 + pair-or-drop 投影 + 摘要压缩 + 折叠快照 + 写入侧不变量 + 格式版本
    approval        审批：人在回路挂起状态机（提案带可执行命令，批准后交给沙箱）
    guard           把关：滑动窗口限流 + 工具执行流水线四道关（pre/guard/execute/post）
    sandbox         沙箱：只收 argv（不经 shell）+ 内置文件动作 + 本机/容器两种后端 + 隔离等级诚实上报
    hooks           钩子插槽：pre 拦截（waterfall）/ post 观察
    subagent        子智能体：delegate_task / delegate_tasks 并行 fan-out + 深度上限
    spill           溢出存储：大内容外存留定位符（store/load_large_content）
    mcp             MCP 集成：外部 server 工具桥进统一注册表（mcp__server__tool）
    appserver       app-server 协议：JSON-RPC v1 双向契约（流式推送 + 审批回调）
    metrics         可观测性：计数/求和/仪表/直方图 + Prometheus 文本导出
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

`auth:approve` 之后是真的走沙箱执行器把动作跑起来，并把退出码和输出回执给你；
回执里**始终写明隔离等级**（`none` / `partial` / `full`）和执行方式 ——
人得知道批的是"沙箱内动作"还是"宿主机裸跑"。同时落一条 `audit/action` 审计事件
（log-only，不喂模型），可追溯谁批的、批了什么、结果如何。

打开 `REQUIRE_SANDBOX_ISOLATION=true` 后，沙箱给不出隔离就 **fail-closed 拒绝执行**，
而不是"反正批都批了，跑吧"。审批这条链上的默认值必须往"不做"那边偏：
没人能保证安全时，宁可不做。（对比：限流这种"人为、可恢复"的过载是 fail-open ——
默认值应该按"猜错的代价"选，而不是随手写死。）

**执行计划只有 argv，没有"命令字符串"。** 挂起时定下的计划是 `sandbox.Request`
（命令 + 参数数组，或一个内置动作），批准后原样交给执行器 ——
没有"拼一段 shell 命令"这一步，所以没有注入面，也没有引号转义问题。
本项目的"写审计文件"动作就走**内置文件动作**（纯 Go 写），不是 `sh -c 'echo x >> y'`：
带空格的路径（这个项目的目录就叫 `agent study`）和中文内容都因此不再需要特殊处理。
`Validate()` 会拦下任何想交给 shell 解释器（`cmd` / `sh` / `powershell`…）的计划，
除非显式 `SANDBOX_ALLOW_SHELL=true`。

**审批是两步的，超时是显式的。** 敲 `auth:approve` 只会回显完整执行计划
（含「将要执行：…」原文、剩余有效期、可选项）和一个短确认码，**不执行**；
要把码再打一遍（`auth:approve <码>`）才真的执行 —— 防的是"看都不看就批"。
提案有有效期（默认 5 分钟），过期后批准会得到明确的「已超时作废」回执，
而不是静默变成"没有待审批任务"（以前超时由 Redis TTL 隐式发生，超时了没人知道）。
可选项由提案方给出（现在是批准 / 拒绝两个），将来加"仅本次允许 / 永久允许"不用改交互协议。

一个刻意的选择：**流水线不拦 `execute_system_defense` 这个工具调用**。它只是"起草提案"、本身不高危；
真正高危的是批准后执行的动作，所以不变量落在**执行点**而不是工具调用点——拦错地方会直接破坏审批链。

**沙箱有两种后端，按 `SANDBOX_BACKEND` 选。** `local`（默认）在宿主机直跑、如实上报
隔离等级 `none`；`docker` 把命令放进一次性容器 —— `--rm --network=none --read-only
--cap-drop=ALL --security-opt=no-new-privileges --pids-limit=64 --memory=256m --cpus=1`，
上报 `full`。选 docker 但 docker 连不上时**所有审批都拒绝执行**（fail-closed），
绝不悄悄退回本机直跑 ——「配了要隔离却实际裸跑」比「根本没配隔离」危险得多。

想自己验证隔离真的生效（需要 docker 与 `alpine:3.20` 镜像，注意这两条直接调 docker，
不经过本项目的沙箱接口 —— 那个接口不接受 shell 解释器）：

```sh
docker run --rm --network=none alpine:3.20 wget -T3 -qO- https://example.com || echo "✅ 出不去"
docker run --rm --read-only alpine:3.20 touch /x || echo "✅ 根文件系统只读"
```

另一处**刻意保留的诚实缺口**：被工具流水线拦下的"某次工具调用"挂起时**没有**执行计划
（它还没被翻译成 argv）。批准后会**如实拒绝执行**并说明原因，而不是假装执行过。
要把这条链补全属于审批升级链（HARNESS-TODO P3-2）。

## Roadmap

> 功能实现与已验证范围分别记录。工作区完整清单见仓库外的 `../HARNESS-TODO.md`（它不随仓库分发），测试范围见 `test/README.md`。

- **可靠性** —— step 事件、Repair 与日志续答已有基础实现；优先补压缩保真、持久化确认、结束原因传播、审批并发与未知结果处置。
- **策略配置化 / 审批升级链** —— guard 规则与 hooks 现在都是编译期注册，加一条策略要改代码重启；命令种类够多之后再做"批准即落规则"的升级链。
- **MCP 执行闭环** —— 连接与工具发现已有历史验证；批准后执行原调用仍未闭环。
- **Web Console** —— 版本化双向协议驱动 agent，审批/进度可视化（app-server 协议已具备）。
- **真实任务评测** —— 已有耗时直方图；后续围绕确定的用途记录任务质量、成本、耗时与人工接管，用途尚未定案。

---

*Go-AI-Gateway 正在持续进化——从一个 IM 网关，长成一个真正可用的 AI Agent 运行时。*
