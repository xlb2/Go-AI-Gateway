# 第 01 课　一个 agent loop 到底由哪几块组成

> 阶段 0 · 不写代码，只读懂。预计 40–60 分钟。
> 学完你应该能：拿到任意一个 agent 框架的源码，**五分钟内指出它的循环在哪、状态在哪、取消怎么传播**。

---

## 一、先给结论：最小骨架

一个 agent loop 永远是这三层：

```
run        一次"会话运行"（可能包含多轮）
 └ turn     一轮用户请求（从用户说话到给出答复）
    └ step  一次模型调用 + 它要求的工具执行   ← 关键就在这一层
```

**step 是最小单位。** 它之所以关键，是因为下面所有能力都建立在它之上：

| 能力 | 为什么需要 step |
|---|---|
| token 预算 | 每一步花多少，而不是"一轮"花多少 |
| 断点续跑 | 从第 3 步接着跑第 4 步 |
| 精确中断 | 取消当前 step，保留已完成的 |
| 可观测 | 知道"卡在哪一步" |

**我们的项目缺的正是这一层。** 下面看两个真实实现怎么做的。

---

## 二、教材 1：dsh（TypeScript）

**文件**：`deepseek-harness/packages/core/agent-loop/src/agent.ts`
**位置**：外层循环在 **263 行**，内层循环在 **339 行**

### 外层：turn 里的 step 循环

```ts
while (true) {
  signal.throwIfAborted()
  const step = phase.step + 1
  const decision = await this.preStep(target, { turn, step })
  if (decision.kind === 'reject') { turnEnds = { kind: 'blocked' }; return false }
  if (turnEnds && decision.messages.length === 0) break

  this.session.append('step/start', { turn, step })      // ← 每一步的开始，落事件
  phase.step = step
  try {
    for (const message of decision.messages) {
      this.session.append('user/message', message, { surfaceOp: 'append' })
    }
    const stepEnd = await this.step(decision.assembly)   // ← 内层：模型 + 工具
    if (turnEnds === null || turnEnds.kind !== 'max-tokens') turnEnds = stepEnd
  } finally {
    this.session.append('step/end', { turn, step })      // ← 每一步的结束，落事件
  }

  if (turnEnds && this.inbox.nextStep.length === 0) break
  target = 'next-step'
}
```

**看三件事**：

1. 每一步都有一对 `step/start` / `step/end` 事件 —— **状态是事件流，不是内存变量**。
2. `turnEnds` 是个**结构化的原因**：`completed` / `blocked` / `aborted` / `error` / `max-tokens`。
3. `finally` 里也写 `step/end` —— **失败路径同样留下痕迹**，不会出现"这步做了没有"谁也说不清。

### 内层：一个 step 里的"模型 → 工具 → 再模型"

```ts
while (true) {
  const { request, preparedCall } = await this.buildRequest(turn, step, ...)
  const assembler = new BlockAssembler()
  try {
    const stream = preparedCall?.stream(request) ?? this.loopCtx.llm.stream(request)
    for await (const chunk of stream) {
      chunkSeqs.push(this.session.append('assistant/chunk', { turn, step, chunk }).seq)
      assembler.push(chunk)
    }
  } catch (error) {
    if (signal.aborted) {
      const content = assembler.interruptedBlocks()   // ← 只保留已投递的文本
      ...
    }
  }
  // ...（拿到工具调用就去执行，然后回到循环开头再调一次模型；没有工具调用就退出）
}
```

**这一层就是"ReAct"本身**：调模型 → 它要求调工具 → 执行 → 把结果喂回去再调模型 → 直到它不再要求工具。

> 我们项目 P1-2 学过的 `interruptedBlocks()`（半截回复只保留已投递文本、丢弃未派发的工具调用）
> 就在这儿 —— 你现在看到它在原实现里的位置了。

---

## 三、教材 2：codex（Rust）

**文件**：`codex/codex-rs/core/src/session/turn.rs`
**位置**：主循环在 **301 行**（`run_turn` 函数从 153 行开始）
外层还有一层：`tasks/regular.rs:76`

```rust
let mut next_step_context = Some(first_step_context);
loop {
    let pending_input = sess.input_queue.get_pending_input(&sess.active_turn).await.0;
    if run_hooks_and_record_inputs(&sess, &turn_context, &pending_input, ...).await {
        break;
    }

    // 关键：**每一步先冻结一份"模型可见视图"**
    let step_context = match next_step_context.take() {
        Some(step_context) => step_context,
        None if pending_input.is_empty() => {
            sess.capture_step_context(Arc::clone(&turn_context), &cancellation_token).await?
        }
        None => { /* 有新输入时，要重新算"需要哪些 MCP server"再冻结 */ }
    };

    // 用这份冻结的视图去采样（调模型）→ 执行工具 → 回到 loop
}
```

**看两件事**：

1. `capture_step_context` —— **每一步开始时把"模型将看到什么"冻结成一份快照**。
   为什么要冻结而不是现取？因为过程中状态会变（用户中途插话、上下文被压缩、工具列表变了），
   **现取会导致"模型看到的东西"和"这一步骤账时依据的东西"不是同一份**。
2. 外层 `regular.rs:76` 那个 `loop` 处理的是"一轮结束后又有新输入" —— 所以是 `run` / `turn` / `step` 三层。

---

## 四、三份对比（这一课的核心）

| 问题 | dsh | codex | **我们（现在）** |
|---|---|---|---|
| 循环在哪 | 自己写（两层 `while`） | 自己写（`turn.rs` 的 `loop`） | `react.NewAgent`（Eino），**看不见** |
| step 是一等数据吗 | ✅ `step/start` / `step/end` 事件 | ✅ `step_context` 冻结快照 | ❌ 完全没有 |
| 状态存在哪 | 全部 `append` 进 session 事件流 | rollout 事件 + step_context | 消息日志（与 react **内部状态两套**） |
| 一轮结束的"原因" | `turnEnds{kind: ...}` 五档 | 结构化 outcome | 一个 `interrupted bool` |
| 取消的粒度 | step 级 `signal.throwIfAborted()` | `cancellation_token.child_token()` | 只能整条 stream |
| 流中断怎么处理 | `interruptedBlocks()` 只留已投递的 | 同类做法 | ✅ P1-2 已经学了同一招 |

**结论**：我们不是"少写了代码"，是**少了一层抽象**。这一层就是阶段 1 要补的。

> **更新（2026-09-14，阶段 2 完成）**：上面"**我们（现在）**"那一列**已经过时** ——
> 循环已自研（`internal/harness/agent/loop.go`），step 是一等公民，`react.NewAgent` 路径已删除。
> 这张表现在当作"替换前的基线"读。

---

## 五、送你一个可迁移的方法：怎么读一个陌生的循环

别从头读。按这个顺序，五分钟能摸清结构：

1. **先找循环** —— `grep -n "while (true)\|loop {"`，找出有几层
2. **看循环体的第一件事和退出条件** —— 这两样决定整个结构（dsh 是"算 step 号 + 检查 pending"，codex 是"取 pending 输入"）
3. **看每轮结束时写了什么** —— 决定"状态存在哪"（dsh 写 `step/end`，说明状态在事件流里）
4. **最后看出错路径** —— `catch` / `?` / `finally`，决定它的可靠性设计（dsh 连 `finally` 都写事件）
5. **找不到循环？** 说明循环被框架封装了 —— 那就去看框架文档，或者在调用处看它暴露了什么钩子（我们项目只有 `MessageModifier` 一个口子）

---

## 六、本课作业（三个问题，不用写代码）

回答完发我，我们对着答案讨论。**答错不影响进度，答不出来才说明这课得重看。**

**Q1.** dsh 内层 `while (true)`（`agent.ts:339`）**什么时候退出**？退出意味着什么？
> 提示：看循环体里在干什么（调模型、取 chunk），然后在函数里找 `break` 在哪儿。

**Q2.** codex 为什么要在每一步**先冻结**一份 `step_context`，而不是每次要用时现取？
> 提示：如果这期间 session 状态变了（用户中途插话 / 上下文被压缩 / 工具列表变了），
> "现取"会导致什么问题？

**Q3.** 我们的 `interrupted bool` 和 dsh 的 `turnEnds` 相比，**丢了什么信息**？丢了会造成什么后果？
> 提示：P1-2 那一课里"分不清'模型没回'和'断了'"就是这类信息缺失造成的。
> 再想想：如果将来要区分"用户主动取消"、"撞了 token 上限"、"被审批拦下"，
> 一个 `bool` 够吗？

---

## 七、Q1 参考答案（2026-09-14 已讨论）

### 第一件事：先分清两层

这一课最容易犯的错，是**把两层的退出条件搞混**——因为两层都是 `while (true)`，都是 `break`/`return`，看起来一样。所以先把它们钉死：

| | 外层 `turn()` | 内层 `step()` |
|---|---|---|
| 行号 | 263 | 339 |
| 循环body | 算 step 号 → `preStep` → `step()` | 调模型 → 收 chunk → 落 `assistant/message` |
| 出口形式 | **两个 `break`**（271 / 299） | **四个 `return` + 一个 `continue`**，没有 `break` |
| 一层循环的含义 | 一个 turn 里可以跑很多个 step | 一次模型调用及其善后（含重试） |

**记法：`step()` 里没有 `break`。看到 `break` 就一定在外层。**

### 内层真正的四个出口

```ts
while (true) {
  ...调模型、收 chunk、落 assistant/message...

  if (finish.kind === 'max-tokens') return { kind: 'max-tokens' }   // ① 被截断
  const toolCalls = message.content.filter(b => b.type === 'tool-call')
  if (toolCalls.length === 0) return { kind: 'completed' }          // ② 模型说完了
  const { concluded } = await executeToolCalls(...)
  return concluded ? { kind: 'completed' } : null                   // ③ 交棒
}                                                                    // ④ catch 里 continue
```

**返回值是 `StepEndReason | null`，两种语义完全不同：**

- 非 `null` → **这一步有结论了**（`completed` / `max-tokens`），交给外层
- `null` → **这一步没完**。外层开一个新 step，把工具结果当新输入再调一次模型

> **关键洞察：多步不是内层循环做的。** 内层跑一次就 `return` 了，"模型 → 工具 → 再模型"
> 是外层靠 `null` 一步步推出来的。内层那个 `while (true)` 存在的**唯一理由**是第 ④ 条 `continue` 重试。

### 外层那两个 `break` 的真正含义

| 行 | 位置 | 含义 |
|---|---|---|
| 271 | 循环**开头**，调模型之前 | 上一步已有结论，这次没产出新消息（唤醒消息被移除）→ **连模型调用都不花**，直接收尾 |
| 299 | 一步做完之后 | 这一步有结论了 + `inbox.nextStep` 队列为空 → 收尾 |

关键在 `inbox.nextStep`：它是**外部往里塞东西的队列**（工具结果就是被 `splice` 进去的，见 416 行）。
所以这两个条件读作「**有结论 + 没活干**」，**不是**"模型想不想继续"。

> 模型想不想继续，管在 `toolCalls.length === 0` 那条 —— 那是**内层**的事。

### 退出意味着什么

```ts
} finally {
  this.session.append('turn/end', { turn, reason: turnEnds! })  // 319：落一条终局事件
}
if (!this.inbox.hasPending) return false   // 324：没 pending → 驱动器收工
phase.step = 0
return true                                 // 有 pending → 再开一个新的 turn
```

**退出外层循环 = 这一轮有了结论并被永久记录，同时决定要不要立刻开始下一轮。**

### 附带一笔：结束原因是「黏」的

```ts
// 290
if (turnEnds === null || turnEnds.kind !== 'max-tokens') turnEnds = stepEnd
```

某一步撞了 token 上限，后面正常完成的步骤**不许**把它改回 `completed` ——
"这轮输出被截断"是个**事实**，不会因为后面又跑了一步就变得没发生。

这条设计直接对应 Q3：**结束原因必须是结构化、可共存、不可降级的**，一个 `bool` 撑不住。

---

## 八、Q2 参考答案（2026-09-14 已讨论）

**一句话**：冻结的是**「模型可见视图」**，而且必须在 step 开始那一刻取一份**不可变快照** ——
保证「模型看到的」和「你据以决策的」是**同一份**。

一个 step 里中途会变的三样东西：

- 用户**中途插话**（新输入进队列）
- 上下文被**压缩**
- **工具列表**变了（某个 MCP server 上下线）

**为什么要冻结，不能现取** —— 两层伤害：

| 层次 | 坏在哪 |
|---|---|
| **同一步内自相矛盾** | 一个 step 里有先后两件事：先"决定发什么给模型"、再发送、再**记账**（token 消耗 / 要不要压缩）。中途状态一变，模型实际看到的是 A，你记账/决策依据的是 B → **对不上账** |
| **不可复现 / 不可审计** | 事后回放要能还原"模型当时到底看到了什么"。现取的话，回放用的状态和当时早就不是一份了 |

> **Go 类比**：别在一个长操作里反复读共享的可变字段，先 `snapshot := *x` 拷一份再算。

用户的回答（"状态下一秒就变、没法正确选择和回答"）方向对了，补的就是上面"冻结的是什么 + 两个层次"。

---

## 九、Q3 参考答案（2026-09-14 已讨论）

**先纠一个点**：丢的**不是"之前做了什么"**（那在日志里有 —— `tool/call`、`tool/result` 都记着），
丢的是**「这一轮为什么结束」的结构化原因**。

### 四种情况，`bool` 全塌成一个 `true`

| 实际情况 | 用 `bool` 写出来 |
|---|---|
| 用户主动取消 | `Interrupted = true` |
| 撞了 token 上限 | `Interrupted = true` |
| 被审批拦下挂起 | `Interrupted = true` |
| 上游报错挂了 | `Interrupted = true` |

### 谁受损：决定"下一步怎么办"的那段逻辑

| 结束原因 | 正确处置 |
|---|---|
| 用户取消 | **不要**自动重试、**不要**自动恢复 |
| 撞 token 上限 | 压缩上下文后**继续**（不是重试） |
| 审批挂起 | **等批准**再恢复 |
| 上游报错 | 才考虑重试 |

一个 `bool` 分不清这四种，决策者只能保守处理：要么"全当可重试"→ 多余重试（用户都取消了还重试），
要么"全当不可恢复"→ 该续跑的没续。

**根因**：`bool` 只能表达"**有没有被截断**"，表达不了"**为什么停**"。
而 **resume 的前提正是"先知道为什么停"** —— 这就是 Q3 被当成 **P3-1 checkpoint 入口**的原因。

### 和 Q1 那条串起来

Q1 结尾那句「结束原因必须**结构化、可共存、不可降级**」是同一件事的另一面：
`turnEnds` 能表达五种原因（还能"黏"住 `max-tokens` 不被后面改写），
`bool` 连"两种同时存在"都表达不了。**阶段 1 要写的最小循环，第一个必须写对的就是这个。**
