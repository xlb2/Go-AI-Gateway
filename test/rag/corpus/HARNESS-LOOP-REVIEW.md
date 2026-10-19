# HARNESS-LOOP-REVIEW.md — 自研 agent 循环替换的真实记录

## 当前阅读说明（2026-09-17）

本文保留 09-14 至 09-15 的历史分析与演练，不是当前工程状态表。当前待办见 `HARNESS-TODO.md` 的 R 系列。

- 第六节已有真实杀进程恢复演练；前文“没演练”是当时状态，已被后续记录取代。
- 代码已有循环独立测试、usage 聚合和 turn/end JSON。当前新增取消检查属于部分实现，不证明整个生命周期已解决。
- `ctx.Err()` 检查不能打断已经阻塞的 `Send/Recv`；静默上游和满缓冲仍需专项验证，不能把责任仅归给 SDK。
- 日志续答不保证副作用恰好一次；写失败不阻止执行、未知结果重新发起等问题转入 R-2/R-4。
- 本次只做静态复核和文档调整，未运行测试；历史通过记录不覆盖当前未提交改动。

## 历史复盘原文

> 日期：2026-09-14　范围：教学阶段 2（`react.Agent` → 自研循环）
> 写法：**只说真的**。改了什么、验证到哪一步、**没验证什么**、缺点、缺口、可优化点，全列出来。
> 目的：防止下一个 agent（或三个月后的自己）看到"58 全绿"就以为这件事完美收尾了。

---

## 〇、一句话结论

**外部可观察行为对齐了（61 用例、不设 env 全绿）。P3-1（step 事件 / 结构化原因 / Repair / resume）已落地。**
**真模型六段冒烟已于 2026-09-15 跑过（6/6 通过，历史记录）。** 循环已有独立单测，剩余验证面以本文顶部和 TODO 为准。

---

## 一、改了什么（逐文件）

### 新增

| 文件 | 内容 |
|---|---|
| `internal/harness/agent/loop.go` | 自研循环 `ownLoop`：`Stream` / `run` / `drain` / `bindTools` / `executeTools` / `runOne`。约 230 行 |
| `internal/harness/agent/runner.go` | 循环缝：`Loop` 接口 + `NewLoop(ctx)`（`AGENT_LOOP` 只剩 `own`） |

### 删除

| 位置 | 删了什么 | 为什么 |
|---|---|---|
| `internal/harness/agent/agent.go` | `BuildEinoAgent()`（`react.NewAgent` 组装） | 循环不再是 Eino 的 |
| `internal/harness/agent/agent.go` | `newMemoryLogModifier()`（`MessageModifier` 钩子，靠**下标差分**落 tool 事件） | 事件改由循环自己落（`executeTools`），钩子没必要且更脆弱 |
| `internal/harness/agent/agent.go` | `react` / `compose` 两个 import | 随上面一起 |
| `README.md` / 仓库 `AGENTS.md` | "Eino react 固定内核"等 7 处文案 | 与代码对齐 |

### 保留（易被误删，标一下）

`agent.go` 里 `retryPolicyFromEnv` / `envInt` / `retryAttemptsSoFar` / `retryLogger` / `memoryDTOFromToolCall` /
`memoryDTOFromToolResult` **都还在用**（`NewOwnLoop` 与 `loop.go` 复用），不是遗留垃圾。

### 行为等价性靠什么保证

`NewOwnLoop` 与旧 `BuildEinoAgent` 共用**同一份**装配：`newChatModel` → `retry.Wrap`（同一策略、同一
"本轮已用重试数"恢复）→ `AllTools()`（同一 guard 流水线）。所以换的是"循环"，不是"模型/工具/重试"。

---

## 二、验证到什么程度

**验证过（这才是"绿"的含义）**

- `scripts/test-fast.sh`（假模型 + 真 Redis，58 用例）：**不设 env 全绿**。
- 反证 1：`AGENT_LOOP=bogus` → e2e 全挂、报 `未知的 AGENT_LOOP="bogus"` ⇒ env 确实进到代码里。
- 反证 2：`AGENT_LOOP=react` → 明确报"已删除" ⇒ 旧路径真没了。
- 本地 `go vet ./...` 通过。

**没验证（别自欺）**

- ✅ **真模型六段已跑（2026-09-15，`scripts/test-real.sh`）—— 6/6 通过。** 实测覆盖：
  子智能体并行 fan-out（子 agent 也走自研循环）、spill 存/取、防御审批挂起、两步审批 + 沙箱真实执行、
  自动 spill、10 轮对话。**说明流式 `tool_call` 分片拼装、工具回灌在真模型形态下也没问题。**
- ❌ **但第 6 段的"压缩"没被真正触发**（默认 128k 窗口，10 轮短消息压不动）—— "真模型 + 压缩"仍未验证。
  （想验：把 `MODEL_CONTEXT_WINDOW` 调小，或灌长消息。）
- 后续已做真实杀进程演练，见第六节 A2；发现未知结果下模型可能再次发起工具调用。
- ❌ 中断流（非 EOF）、**ctx 取消** 这些路径真模型下没覆盖（e2e/单测里有模拟）。
- ✅ `loop.go` **已有独立单测**（2026-09-15，`internal/harness/agent/loop_test.go`，4 条：
  文本流 / 工具交棒回灌 / 建流失败同步 / 中途断流透传；注入假模型 + 计数工具，不碰 Redis）。

---

## 三、缺点与风险（诚实清单）

1. **step 事件 / 结构化结束原因 / Repair 扩展 / resume —— ✅ 全部已做（3.1–3.3，2026-09-15）。**
   `loop.go` 每步落 `step/start` / `step/end`（带结构化原因 `handoff/completed/max-tokens/aborted/error`）；
   `tool/call` 在执行**前**落盘；`Repair` 闭合开放 step + 悬空 tool/call；`Harness.ResumeTurn` 从投影历史续跑、不重放。
   后续 `turn/end` JSON 和真实演练见第六、八节；可靠执行保证仍需 R 系列补齐。

2. **usage 记账 —— ✅ 已修（2026-09-15）。**
   原实现 `usage = ...` 覆盖式接收 → 多步对话只记最后一步；校准还拿"首轮估算 vs 末轮实际"配对。
   现在按**聚合**记：`firstPrompt`（配校准，和 `fullMessages` 是同一份）、`lastPrompt`（上下文有多满）、
   `prompt_total` / `completion_total`（各 step 累加 = 真实花费），指标也改成累加。

3. **goroutine 生命周期没有兜底。**
   `Stream` 把 `run` 丢进 goroutine；若调用方提前停止读取（ctx 取消、上层 return 而不 drain），
   `sw.Send` 可能一直阻塞 → **goroutine 泄漏**。当前 `RunAgentTurn` 总会读干，所以 e2e 没照到，
   但这是"取消路径"的真实隐患。

4. **ctx 取消没有映射成结构化原因。**
   模型流报错时，我们不区分"用户取消"和"上游真错"，统一 `sw.Send(nil, err)`。与第 1 条同源。

5. **`opts ...agent.AgentOption` 被直接丢弃。**
   react 会用它（回调等）。当前调用方没传，所以没暴露；但**接口语义被削弱**，将来要用会踩。

6. **不再支持 `ToolReturnDirectly` / `react.SetReturnDirectly`。**
   react 有"工具结果直接作为最终答复"的机制。我们的循环没有。当前内置工具都没用它，所以没差；
   但若将来某个工具调用 `react.SetReturnDirectly`，在自研循环里会拿不到状态（报错），**没有等价物**。

7. **`ChatModelWithTools(l.model, nil, infos)` 走的是 deprecated 的 `BindTools`（原地 mutate）。**
   Eino 自己警告它**并发不安全**。目前 `NewOwnLoop` 每轮新建实例，实际无共享，风险低；
   但这是"技术上站在 deprecated 路径上"。

8. **`maxSteps` 语义与 react 不同。**
   react 默认 `len(chanSubscribeTo)+10`（动态）；我们是固定 `10`（可用 `AGENT_MAX_STEPS` 覆盖）。
   长工具链对话的上限行为与旧版**不一定一致**，测试没钉这条。

9. **工具查找失败/未知工具的处理与 guard 有轻微重复。**
   `runOne` 自己把 unknown/err 包成字符串；guard 的 `execute` 也包一层。两层语义重叠，不够干净。

10. **`drain` 的 `sw.Send` 只在返回 closed 时停，不看 ctx。**
    与第 3 条配套；取消时不会立刻收敛。

---

## 四、少了什么（对照 react / dsh / codex）

| 能力 | react（旧） | 自研循环（现在） | dsh / codex |
|---|---|---|---|
| step 边界 | 内部有，不暴露 | **已落 `step/start`+`step/end` 事件（3.1）** | 落事件 / 冻结快照 |
| 结构化结束原因 | 内部有 | step 原因与 turn/end JSON 已有，跨层传播仍有缺口 | `turnEnds` 五档 / 结构化 outcome |
| step 级 checkpoint / resume | 无 | 已有 Repair + 日志投影续答，可靠性边界见 R-2/R-4 | codex `capture_step_context` + `ActiveTurn` |
| 中断原因分类 | 部分 | **无**（只有 error 与否） | dsh `Agent.cancel(cause)` 类型化 |
| 并行工具调用 | 有（ToolsNode 支持） | **串行**（按序执行，结果保序） | dsh 有界滚动池 / codex `tool_supports_parallel` |
| 每步 token 计量 | 透传 | 已聚合主循环 usage，更多预算边界见 R-3/R-6 | 按步计量 |
| 输入队列 / steering | 无 | **无** | codex `input_queue.get_pending_input` |
| "只保留已投递内容"的中断裁剪 | 内部有 | **机制不同、结果等价**：我们不落 assistant 消息，由 harness 落已收文本；未执行的工具调用自然不落 | dsh `interruptedBlocks()` |
| 并发安全造模型 | 同样走 BindTools | 同 | — |

---

## 五、可以优化哪里（按**性价比**排序 —— 先关风险，再加功能）

1. ~~先把真模型六段跑一遍~~ ✅ 已完成（2026-09-15，6/6 通过，见 §二）。
   剩：验证真模型压缩保真；杀进程演练已有历史记录，未知结果语义仍待完善。
2. ~~修 usage 记账~~ ✅ 已完成（2026-09-15，见 §三.2）。
3. ~~P3-1 子步 3.1：落 `step/start` + `step/end` + 结构化结束原因~~ ✅ 已完成（2026-09-15）。
   3.2/3.3 基础实现已完成；新的验收缺口见 TODO 的 R 系列。
4. **goroutine 兜底**：`run` 里 `select ctx.Done()` + `sw.Send` 的 closed 反馈 + 必要时主动关 reader。
5. ~~`loop.go` 单测~~ ✅ 已完成（2026-09-15，4 条，见 §二）。
6. **改用 `ToolCallingChatModel.WithTools`**（若底层支持），摆脱 deprecated `BindTools` 的并发隐患。
7. **识别 `AgentOption`**（至少中断/回调），别无声吞掉。
8. **统一 unknown/failed 工具的错误处理**，去掉 loop 与 guard 的重复包裹。
9. 并行工具调用、steering 队列：**有真实收益再上**，现在上属于过度工程。

---

## 六、追加（2026-09-15）：真实数据抓出来的一个 bug —— 子 agent 污染父日志

**现象**：`cmd/probe` 看真实日志发现 **`step/start=35` 但 `step/end=50`**（不平衡）；
`probe validate` 报 19 处"`step/end` 找不到配对的 `step/start`" + 4 处"连续两个 `step/start`"。

**根因**：`delegate_task` 的**子 agent 与父 agent 用同一个 userID、写同一份日志**，两股 step 事件**交错**：
```
父 start, 子 start, 父 end, 子 end   ← 交错后就不再是严格的 start/end 交替
```
而 `Repair` 用朴素的"交替计数"判断"开放 step"，在交错日志上**误判**，于是**每轮多补一条 `step/end`** → 越积越多。

**顺带暴露的更大问题**：子 agent 的**所有**中间事件（工具调用等）一直在往父会话日志里写 ——
这和 M8"子 agent 上下文隔离、中间过程不回流"的设计**直接矛盾**。之前没发现，是因为 e2e 里
根本没注入 `subagent.SetRunner`，**子 agent 从来没在测试里跑起来过**。

**修复**：
1. `subagent.IsNested(ctx)`；自研循环在嵌套时把 `userID` 置 0 → **子 agent 不写父日志**（隔离 + 不打乱父的 step 序列）。
2. `Repair` 不再把"连续 start"当成多个未闭合（抗交错、幂等，存量脏数据不再被越补越多）。
3. 新增 `session.ValidateLog` + 接进 `probe validate`：**序列级**校验（单条 `Validate` 看不到顺序）。
4. 新增回归用例 `TestNestedAgentDoesNotPolluteParentStepLog`（e2e 里也补上了 `subagent.SetRunner`）。

**没做**：存量脏数据改不了（只追加）；**子 agent 的独立日志**（理想形态是每个 agent 一份日志/一个 scope id）没做。

**教训（比 bug 本身值钱）**：
- **单条校验 ≠ 序列校验**：`Validate` 拦得住"一条事件结构非法"，拦不住"顺序/配对错乱"。
- **多写者共享一份日志 = 不变量失效**：只要有两个执行体往同一份日志写，任何基于"顺序"的推断都不成立。
- **测试盲区会长成生产 bug**：子 agent 没在测试里跑过，所以它污染日志这件事谁都看不见。

### A2 实机演练（2026-09-15）的发现

真·杀进程 → `gwresume` 跑通了：`续跑点：已闭合 step = 7，末尾开放轮次 = true` →
`Repair` 补了 `1 个 step/end + 1 条 tool/result` → 从投影历史（19 条）接着跑 → 正常收尾。
**我们没有重放任何工具**（dangling 的 `delegate_tasks` 是合成"结果未知"，不是重新执行）。

两个发现：
1. **`cmd/resume` 漏注入 `subagent.SetRunner`** → 续跑里模型再派活时 `delegate_*` 直接失败（已修）。
2. ⚠️ **`Repair` 合成"结果未知"之后，模型自己又把工具重发了一遍** —— 所以 resume 仍可能让**非幂等副作用做两遍**。
   注意：这**不是我们重放**，是模型看到"结果未知"后的自主决定。要根治得靠两条之一：
   **工具本身幂等**，或**"结果未知"时禁止重发**（在策略层标记）。

---

## 七、给下一个 agent 的提醒

- "58 全绿"**只**证明"对假模型的外部行为没退化"，**不**证明"真模型可用"，更**不**证明"step 是完整能力"。
- 想动这个循环：先读 `AGENT-ENGINEER-PLAN.md` 阶段 2 的**硬契约 6 条**，再跑 `scripts/test-fast.sh` 和
  （如果是流式相关改动）`scripts/test-real.sh`。
- 本文档随 P3-1 落地后应更新：第 1/2 条修掉时，把对应行标掉。

---

## 八、技术债还账记录（2026-09-15）

**已还**
- **`turn/end` 结构化**：content 从裸串 `"completed"` 改成 JSON
  `{"reason":"completed"|"interrupted","detail":...}`（`session.TurnEndContent`；harness 与 Repair 都走它）。
- **取消路径兜底**：`run` 每步开头检查 `ctx.Err()`；`drain` 每次 `Send` 前检查 —— 调用方取消后不再往 pipe 写，
  避免"pipe 满 + 没人读 → goroutine 永远挂着"。新增单测 `TestOwnLoop_ContextCancelStops`。
  **残留**：`reader.Recv()` 本身不响应 ctx —— 若上游一条 chunk 都不发，取消时 goroutine 仍会卡在 Recv
  直到上游报错/超时。彻底解决要给流加 ctx 感知，属上游 SDK 范畴。

**没还（不是"忘了"，是需要设计/产品决策）**
- ⚠️ **"结果未知"时模型会自己重发工具** → 这是**产品语义**问题：要么工具幂等，要么策略层禁止重发。
  硬改循环（例如禁止同参数重发）会误伤合理重试，所以留作决策（见 §六 A2 发现）。
- **子 agent 独立日志（scope id）**：正确形态是每次 agent 执行一份日志（或事件带 scope），
  现在只是"子 agent 不写父日志"。这是 feature，不是 debt patch。
- **`ToolCallingChatModel.WithTools` / 识别 `AgentOption`**：收益低、改动面不小，暂不。
- **存量脏数据**：只追加日志改不了；`probe validate` 会一直报那 25 条（那是事实，不是回归）。
