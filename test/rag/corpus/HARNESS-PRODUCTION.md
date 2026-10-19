# HARNESS-PRODUCTION.md — 从"器官齐全"到"真正能用"

## 2026-09-21 会话与执行连接对照

最新源码证据和取舍见 [第06课](lessons/06-harness-reference-and-integration.md)：dsh `b150a551b8` 的 Agent.turn/step、Session.deriveMessages；Codex `a8468330bb` 的 turn_start_inner、Session、run_turn、StepContext；Go `d8e28bc` 的 RunAgentTurn 与 knowledge.respond 两条路径。借鉴状态归属、日志投影和单步工具一致性；不移植全插件系统或复杂调度。仅源码对照，未运行参考项目，不由append推导物理持久化；会话接入方案尚未实施。下方历史差距清单不能替代当前代码判断。

## 当前用途与参考版本（2026-09-17）

本文作为设计证据库；当前工程状态统一见 `HARNESS-TODO.md`。下文 2026-09-11 的差距报告保留为历史，不能据此判断当前没有自研循环、假模型、token 预算或日志版本号。

本次核对本地参考仓库 HEAD（未更新远端，也未修改参考代码）：

- dsh：`b150a551b8d465e31e418e1b2eaf5e79bbb7d28e`
- Codex：`a8468330bb5f45e9f4d2ec630b01ea8c52908be3`

| 问题 | 本地参考源码 | 本次读取范围与借鉴方向 |
|---|---|---|
| 保留尾部与压缩区间 | `deepseek-harness/packages/compaction/compaction-basic/src/region.ts` | 已读文件前部类型与选区说明：摘要选区、配对、状态稳定性；实施前继续读实现与相应测试 |
| 持久化写入边界 | `deepseek-harness/packages/session/session-persistence/src/coordinator.ts` | 已核对 `session/event` enqueue 与 `session/flush` 注册；区分缓冲与确认，不能直接推导 Redis 物理耐久性 |
| 取消的发生时点 | `deepseek-harness/packages/core/agent-loop/tests/cancel.spec.ts` | 已读测试入口与空闲取消用例；学习按时点构造反例，不直接移植 TS 的任务调度模型 |
| 审批、沙箱与尝试 | `codex/codex-rs/core/src/tools/orchestrator.rs` | 已核对入口说明与审批调用位置；实施前跟踪具体函数、状态及测试，不移植不需要的提权机制 |
| step 与恢复上下文 | `codex/codex-rs/core/src/session/step_context.rs`、`rollout_reconstruction.rs` | 已定位 `StepContext`、`reconstruct_history_from_rollout`；作为后续精读入口，不代表已完整审计 |

后续每条证据记录：问题 → 仓库版本/文件/函数 → 实现及测试证据 → 前提条件 → Go 方案 → 不采用部分及理由 → 验收反例。参考代码也可能有缺陷；“它这样做”不是采用理由。

## 历史差距报告（2026-09-11）

> 2026-09-11 生成。方法：派两个子智能体并行精读本地两个开源 harness
> （`deepseek-harness` = dsh，TypeScript；`codex` = OpenAI Codex，Rust），
> 只带回结论，再对照 `Go-AI-Gateway` 现状。所有引用都带真实文件路径，可自行核对。
>
> 前提：项目现在 **12 个器官齐全、端到端跑通**。所以这份清单不是"还缺哪个器官"，
> 而是"**demo 能跑** 和 **真实用户用起来不出事** 之间的全部距离"。

---

## 零、一句话结论

两个仓库的答案高度一致，差距集中在三件事上 —— **没有一件是"再加一个器官"**：

1. **我们把"一轮对话"当成一个函数调用；它们把 `step` 当成一等公民。**
   step 是计量、取消、恢复的最小单位。不把它抽象出来，就做不了 token 预算、做不了中断、做不了断点续跑。
2. **我们的持久化是"把历史存下来了"；它们的持久化是"只追加 + 可重建 + 崩溃后能恢复到最后一致点"。**
   我们只做到了"只追加"这一半。
3. **我们没有"假模型"测试。** 改一行代码要花 10 分钟真模型回归 —— 这类系统没有测试基建，
   就永远停在"能演示"，因为没人敢改它。

下面按「先修错的 → 再补正确性 → 再建基建 → 最后扩能力」排优先级。

---

## 一、Tier 0：现在就是**做错了**的（不是没做）

### 1. 上下文预算用"条数"，不用 token ⭐ 最玩具的一处

**我们**：`session.go` 的 `MaxHistory = 20` —— 窗口按**消息条数**算。
20 条"收到"和 20 条千字长文，差两个数量级，但我们的行为完全一样。
压缩阈值也因此是"超过 20 条就压"，而不是"快撑爆上下文窗口了才压"。

**dsh**：
- `packages/llm/token-meter/src/index.ts` —— 回放式计价：优先复用最近的 `usage`（要求 envelope 匹配），
  否则启发式估算，产出带位置节点的 `TokenMeasurement`。
- `packages/compaction/compaction-basic/src/config.ts` —— 用
  `contextWindow × thresholdRatio`（默认 **0.8**）触发压缩，`retainRatio`（**0.16**）决定保留尾部多少。

**codex**：
- `core/src/session/context_window.rs` —— 区分 `active_context_tokens` 与
  `auto_compact_scope_tokens`（`Total` 和 `BodyAfterPrefix` 两种口径）、full-window 硬顶、fallback 预留 buffer。
- `core/src/session/token_budget.rs` 管提示阈值，`core/src/compact.rs` 执行压缩（带 `CompactionReason/Phase`）。

**我们该做**：把 `MaxHistory` 换成 `TokenBudget`。
先做"估算法"就够（中文按 ~1.5 字/token、英文 ~4 字符/token，加上模型返回的真实 usage 校准），
触发条件从"20 条"改成"占窗口 80%"。`contextWindow` 从模型配置里读。
**验收**：20 条短消息不触发压缩；5 条长消息就触发。

### 2. 沙箱"没隔离也照跑"，没有 fail-closed

**我们**：`sandbox.LocalExecutor` 无条件在本机执行，而且**没有把"我没隔离"这件事告诉任何人**。
审批链条上，人以为批的是"沙箱内的动作"，实际是裸跑。

**dsh**：`packages/sandbox/sandbox/src/index.ts` —— `sandbox.confine(argv, policy)`
返回**受限后的 argv** 或 **fail-closed**（`SANDBOX_UNAVAILABLE`），
**明令禁止静默无隔离透传**；本机后端还会诚实上报 full / partial 隔离等级。

**codex**：`sandboxing/src/manager.rs` 按平台选 `MacosSeatbelt / LinuxSeccomp / WindowsRestrictedToken`
并 transform 成真实 argv。

**我们该做**：`Executor` 加一个 `Isolation() Level`（none/partial/full），
`none` 时要么拒绝执行、要么在审批回执里**明说"本次无隔离执行"**。零成本，但把谎说圆了。
**验收**：审批回执里能读到隔离等级；配置成"要求隔离"时，占位实现会被拒绝。

---

## 二、Tier 1：崩溃 / 中断的正确性（真用户一定会遇到）

### 3. "开放轮次"检测与合成收尾（repair）

**我们**：`RunAgentTurn` 中途崩溃 → 日志里 `user/message` 后面什么都没有。
下一轮投影时它只是被当成"模型没回"，**没人知道那轮是断了还是真没回**。

**dsh**：`packages/session/session-persistence/src/coordinator.ts:902` —— `load` 时遇到没有 `turn/end`
的开放轮次，**合成**一条 `turn/end{interrupted}`（不是截断日志），且只对"冷会话"修，
活会话未闭合则拒绝加载。

**codex**：`core/src/session/rollout_reconstruction.rs` —— 反向扫描 + 按 compaction checkpoint 截断 +
支持 `ThreadRolledBack`（丢最近 N 轮）。

**我们该做**：`LoadLog` 时扫一遍，发现"有 user/message 但没有配对的 assistant/message 或 turn 结束标记"，
就补一条 `turn/end:interrupted` 合成事件（**只追加、不改历史**，和我们的 folding 不冲突）。
**验收**：kill -9 服务后再启动，日志里能看到合成收尾事件，模型知道上轮被中断过。

### 4. 流中断语义：区分"正常结束"和"被打断"

**我们**：`RunAgentTurn` 里 `for { msg, err := responseStream.Recv(); if err != nil { break } }` ——
任何 err 都当"流结束了"，然后照常落盘一条 `assistant/message`。**半截回复被当成完整回复存进历史**。

**dsh**：`packages/llm/llm/src/{types,assembler}.ts` ——
中断时 `interruptedBlocks()` **只保留已投递的文本/推理，丢弃尚未派发的工具调用**
（否则就会伪造一个"模型要求调用工具"的历史），持久化的 `assistant/message` 带 `interrupted:true`。

**codex**：`core/src/tasks/mod.rs` 的 `handle_task_abort` 给 100ms 优雅窗口再 abort，
并写入 model-visible 的 interrupted marker。

**我们该做**：落盘前判断这次流是正常收到 finish 还是被切断；
被切断时给 `MemoryDTO` 加 `Interrupted bool`，投影时标注。
**验收**：中途断开连接，日志里那条 assistant/message 带中断标记，而不是冒充完整回复。

### 5. LLM 层重试：不但要退避，**重试状态本身也要记进日志**

**我们**：完全没有 LLM 层重试。一次 429/网络抖动就把整轮对话打挂。

**dsh**：`packages/llm/llm-retry/src/index.ts` + `packages/llm/llm/src/retry-policy.ts` ——
指数退避 + 对称抖动（默认 500ms 起、上限 10s、jitter 0.1、最多 5 次），
**退避前先 `session.append('llm/retry')`，重试次数从日志 `findLast` 恢复** ——
也就是"重试状态幂等于日志"；只重试规范化错误码（EMPTY_RESPONSE/RATE_LIMIT/SERVER/TIMEOUT/TRANSPORT），
尊重 `providerRetryAfterMs`，超上限就拒绝而不是硬等。

**codex**：`core/src/responses_retry.rs` —— 可重试错误走指数退避，连接失败可无限重试，
达到上限后从 WebSocket **降级到 HTTPS** 传输。

**我们该做**：在 `agent` 包外面包一层重试（Eino 的 model 接口可以包），
重试前 `AppendEvent(llm/retry)`。**这一条恰好能复用我们刚做的 audit 事件机制**。
**验收**：把 key 改错触发失败，日志里出现重试事件，且注掉重启后能从日志数出重试了几次。

### 6. 工具调度：我们完全不控制（先认清这一点）

**dsh**：`packages/core/agent-loop/src/tool-calls.ts:237` —— 按 `executionMode` 分
`exclusive`（屏障）与 `parallel`（有界滚动池 `maxParallelToolCalls`），
**结果强制按模型给出的顺序提交**（`commitReady`）；调度中断后给未启动的调用写合成错误结果、已启动的 drain 掉。

**codex**：`core/src/tools/parallel.rs` —— 按 `tool_supports_parallel` 分派 + per-tool 锁 + readiness gate。

**但我们**：循环在 Eino 的 `react.Agent` 里，**这几个开关不在我们手上**。
所以这一条不是"补实现"，而是"**去查 Eino 有没有对应的并发配置**"——没有的话记进
"固定内核的取舍"，别假装能做到。

### 7. 不变量要"主动校验"，不能只靠投影时被动丢弃

**我们**：`pair-or-drop` 是投影时发现悬空的就丢掉 —— **事后兜底**。

**dsh**：`packages/runtime-diagnostics/invariants/src/index.ts` —— 不变量注册表，
每个包带 `./invariant`，强制"轮次/步骤封闭、工具调用-结果配对"，**写入时就检查**。

**我们该做**：给 `AppendEvent` 加一层可选校验（生产开着、测试必开）：
不允许"写 tool/result 却没有配对的 tool/call"这种事发生。

---

## 三、Tier 2：测试基建（**没有这个，前面全部做不了**）

### 8. 假模型 —— 把 10 分钟真模型回归变成秒级

**我们**：全仓 **0 个 `_test.go`**（你定的"测试文件不进仓库"），回归只能真模型端到端跑 6 段、约 10 分钟。
后果很实际：**今天为了验证一个压缩语义，我们花了 10 分钟跑一轮**；以后没人敢频繁改这个系统。

**codex**：`core/tests/common/responses.rs` 用 `wiremock` 起**假的 SSE / WebSocket 模型服务器**，
并且**能断言模型收到了什么**（`saw_function_call`、`function_call_output_text`）；
`core/tests/common/test_codex.rs` 是完整测试 harness；`core/tests/suite/` 有 128 个集成测试
（`approvals.rs`、`fork_thread.rs`、`guardian_review_cancellation.rs`…）；快照用 `insta`。

**dsh**：`docs/testing.zh.md` —— 100% 行覆盖门禁 + 真实 key 的 e2e（**缺 key 自动跳过**）+
snapshot 回放录制的会话（ACP 启动真实示例、diff 归一化后的 JSON-RPC）+ CI 强制 replay 只读。
原则：**只 mock LLM / 网络 / 时钟，其它一律用真实实现；断言外部世界而不是 agent 自报**。

**我们该做**（**这是我最建议先做的一件**）：
- 起一个假的 OpenAI 兼容 server（几行 net/http，按脚本吐 SSE），指向 `VOLC_BASE_URL`。
  → 压缩、折叠、审批、流水线全部变成**秒级、确定性**可测。
- Python 单测同理，但 Go 侧的假模型收益最大。
- 关于你"测试文件不进仓库"的约定：假模型 server 可以放在 `cmd/`（那是工具不是测试），
  真正的 `_test.go` 你仍然可以选择不提交 —— 至少让**验证成本从 10 分钟降到 1 秒**。

---

## 四、Tier 3：能力补齐（前面做完再谈）

### 9. step 级状态机 + 逐节点 checkpoint

**我们**：`RunAgentTurn` 是一个函数，循环在 Eino 里，我们**看不到 step**，
所以只有"轮次级"日志，没有 step 级 resume —— 这是 `HARNESS-TODO.md` 里唯一还空着的能力。

**codex**：`core/src/tasks/mod.rs` 的 `SessionTask` trait 把采样循环拆成独立 task，
状态挂在 `ActiveTurn` / `RunningTask`（含 `cancellation_token`、`done: Notify`、`turn_state`）；
`core/src/session/turn.rs` 的每一步先 `capture_step_context`（`session/step_context.rs`）**冻结"模型可见视图"**，
再 diff `world_state`，采样后按 `needs_follow_up` / `token_limit_reached` 决定继续、压缩还是结束。
**关键差异：每步的输入队列（steering）、world-state diff、compaction、hook 全是 step 级一等公民。**

**dsh**：一个 agent 同时只有一个 driver；`Agent.cancel(cause, {keepInbox})` 清未启动工作并 abort，
cause 是类型化的（user / parent / hook / disposed）。

**我们该做**：Eino 的 `react.Agent` 不暴露 step 回调的话，退一步：
用 `MessageModifier`（我们现在已经在用的那个插槽）每步落一条 `step/start` / `step/end`，
换取"step 级可观测 + 可 resume"的最小版本。

### 10. 真沙箱 + 策略引擎 + 升级链（codex 最值钱的一段）

**codex 四件套**：
1. **策略 DSL**：`execpolicy/src/parser.rs` 解析 `.codexpolicy`（Starlark），
   `policy.rs` 做 `prefix_rule` 前缀匹配 + `match/not_match` 样例自校验。
2. **决策**：`core/src/exec_policy.rs` 把命令解析成多段后 `check_multiple`，
   产出 `Skip / NeedsApproval / Forbidden`，且**要求每一段命令都被规则显式允许才允许绕过沙箱**。
3. **真沙箱**：`sandboxing/src/manager.rs` 按平台选后端并 transform argv。
4. **升级链**：`core/src/tools/orchestrator.rs` —— 先在沙箱里跑，命中 `SandboxErr::Denied` 后
   按 `escalate_on_failure` **绕过沙箱重试**；用户批准还能**持久化成规则**
   （`append_amendment_and_update`）—— **系统会从人的决策里学习**。

**我们该做**（分两步）：① 容器隔离（`docker run --network=none --read-only --pids-limit`），
先把 `Isolation()` 从 none 变成 full；② 再做规则 DSL，最后才是"批准即学习"。
`Command []string` 的 argv 化（`HARNESS-TODO.md` 里那条）是这一步的前置。

### 11. 审批的工程细节（我们缺三样）

- **超时归属**：codex 引擎侧**没有审批超时**，超时是**前端决策之一**（`ReviewDecision::TimedOut`）；
  发送端被 drop 即视为拒绝。我们用的是 Redis 5 分钟 TTL —— 隐式超时、且没人知道它超时了。
- **可选项动态生成**：codex 的 `AvailableDecisions` 由策略生成（`protocol/src/approvals.rs`）；
  我们永远只有 approve/reject。
- **自动审查子代理**：`core/src/guardian/` —— **再起一个模型读转录，自动给 approve/deny 决定**，
  人不在时也能给出有理由的判定。这是我们完全没想到的一格（也可以当成 M6 的进阶形态）。

### 12. 可观测性：我们其实**领先一半**

- **dsh 没做**：只有日志 + 事件 + invariants，**没有 span/trace，也没有独立 metrics 服务**。
- **codex**：`otel/src/metrics/names.rs` 集中指标名（`codex.turn.token_usage`、`codex.guardian.review`…）
  + `diagnostics` 进程级 Gauge + `app-server/src/app_server_tracing.rs` + rollout-trace；
  `app-server/src/turn_cost_worker.rs` 轮询后端拿 API-key 的**真实花费**。
- **我们**：metrics 器官已有，但**缺直方图**（看不到 P50/P99）。补 histogram 是低成本高收益。

---

## 五、建议的动手顺序

| 顺序 | 做什么 | 为什么排这里 | 成本 |
|---|---|---|---|
| **1** | **假模型 + 秒级回归**（Tier 2 第 8 条） | 不做这个，后面每一件都要花 10 分钟验证，等于没有迭代能力 | 小 |
| **2** | **token 预算替换条数窗口**（Tier 0 第 1 条） | 现在**就是错的**，而且是"玩具感"的最主要来源 | 中 |
| **3** | **沙箱诚实上报 + fail-closed**（Tier 0 第 2 条） | 零成本把"谎"说圆；审批链上不能有假信息 | 小 |
| **4** | **repair（合成收尾）+ 中断标记**（Tier 1 第 3、4 条） | 真用户一定会遇到的崩溃/断连路径 | 中 |
| **5** | **LLM 重试 + 重试进日志**（Tier 1 第 5 条） | 复用刚做的 audit 事件机制，顺带把"重试幂等于日志"学到手 | 中 |
| **6** | **真沙箱容器化**（Tier 3 第 10 条①） | 前置的 argv 化已具备，换实现即可 | 中 |
| **7** | **step 级 checkpoint** | 唯一还空着的器官能力，也是 M13 课的正式落地 | 大 |

---

## 六、明确不建议照抄的（避免过度工程）

- **dsh 的 100% 行覆盖门禁** —— 对单人学习项目是负担，不如把"假模型 + 关键路径快照"做到位。
- **codex 的 `guardian` 自动审查子代理** —— 有意思，但在你没有真实误报率数据之前，
  加一个"会自己下决定的第二个模型"只会让行为更难预测。**当进阶形态收录，不急做。**
- **codex 的 Starlark 策略 DSL** —— 等你的命令种类超过 10 个再上，现在直接写 Go 判断更清楚。
- **多后端持久化（JSONL + SQLite + zstd + checksum）** —— 我们的 Redis 日志已经满足。
  但**"格式版本不符就拒绝加载"**这个纪律值得抄：现在我们的 DTO 没有版本号。

---

## 附：两个仓库的关键坐标（方便自己翻）

**dsh**
- 重试：`packages/llm/llm-retry/src/index.ts`、`packages/llm/llm/src/retry-policy.ts`
- 崩溃修复：`packages/session/session-persistence/src/coordinator.ts:902`
- 工具调度：`packages/core/agent-loop/src/tool-calls.ts:237`
- 不变量：`packages/runtime-diagnostics/invariants/src/index.ts`
- 流与组装：`packages/llm/llm/src/types.ts`、`assembler.ts`
- 持久化 seam：`packages/session/session-persistence/src/index.ts`
- token 计价：`packages/llm/token-meter/src/index.ts`
- 压缩阈值：`packages/compaction/compaction-basic/src/config.ts`
- 沙箱：`packages/sandbox/sandbox/src/index.ts`；审批：`packages/interaction/user-approval/src/index.ts`
- 安全清单：`docs/defensive-patterns.zh.md`；测试：`docs/testing.zh.md`
- 遥测：`packages/session/session-telemetry/src/index.ts`

**codex**（根 = `codex-rs/`）
- turn/step：`core/src/tasks/mod.rs`、`core/src/session/turn.rs`、`core/src/session/step_context.rs`
- 持久化：`rollout/src/recorder.rs`、`compression.rs`、`reverse_jsonl_scanner.rs`、`state_db.rs`
- 重建/resume：`core/src/session/rollout_reconstruction.rs`
- 审批：`core/src/session/mod.rs`(`request_command_approval`)、`protocol/src/approvals.rs`、
  `app-server/src/bespoke_event_handling.rs`、`core/src/elicitation.rs`、`core/src/guardian/`
- 沙箱/策略：`protocol/src/permissions.rs`、`sandboxing/src/manager.rs`、
  `execpolicy/src/parser.rs`+`policy.rs`、`core/src/exec_policy.rs`、`core/src/tools/orchestrator.rs`
- 重试/并发：`core/src/responses_retry.rs`、`core/src/tools/parallel.rs`、`core/src/tools/executed_tool_calls.rs`
- 预算/成本：`core/src/session/context_window.rs`、`token_budget.rs`、`core/src/compact.rs`、
  `app-server/src/turn_cost_worker.rs`
- 测试：`core/tests/common/responses.rs`、`test_codex.rs`、`core/tests/suite/`（128 个）
- 指标：`otel/src/metrics/names.rs`
