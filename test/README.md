# test/ — 假模型与端到端测试

> 目的：把"依赖真模型的验证"从 **10 分钟**降到 **秒级**（`HARNESS-TODO.md` 的 P0-1）。
> 一句话：**不 mock LLM 就没法回归；不敢回归就没法改；改不动就永远是玩具。**

## 目录

```
test/
  fakemodel/            假模型库（OpenAI 兼容的本地替身）
    fakemodel.go          Server / Handler / 场景匹配 / 请求记录
    scenario.go           内置场景 + JSON 场景加载
    scenarios/default.json 场景文件格式示例
  e2e/
    e2e_test.go           端到端测试（假模型 + 真 Redis）
```

可执行入口在 `cmd/fakemodel`（Go 不允许 import `package main`，所以库和入口分开）。

## 怎么用

### 1. 秒级回归（最常用）

```sh
scripts/test-fast.sh              # 全部用例
scripts/test-fast.sh -v
scripts/test-fast.sh -run Approval -v
```

- **不需要真实模型 key，不烧 token，不依赖外网。**
- 需要 `im_redis` 容器在跑；没起的话用例会 **skip 而不是失败**（这样别人机器上也能跑）。
- 全部用例约 **1 秒**（首次编译几秒）。

### 2. 手动起假模型，让整个服务跑在假模型上

```sh
scripts/fake-model.sh
# 然后把 .env 改成：
#   VOLC_BASE_URL=http://127.0.0.1:9099/api/v3
# 再正常起服务：go run ./cmd/api
```

**业务代码一行不用改** —— 这正是把模型适配做成一条"缝"（M3）换来的好处。
适合演示、联调前端、反复调提示词而不烧钱。

### 3. 真模型端到端（发布前/大改后跑一次）

```sh
scripts/test-real.sh          # 6 段全跑（慢、要真 key）
scripts/test-real.sh pump     # 只跑"灌对话逼压缩"
```

### 4. 从干净状态开始

```sh
scripts/reset-state.sh        # 清所有 agent:* 键 + audit 产物
scripts/reset-state.sh 3      # 只清 UserID 3
```

## 测试覆盖了什么

**纯函数（不需要 Redis / 模型，永远跑）**

| 用例 | 守住的不变量 |
|---|---|
| `TestProjectMessagesPairOrDrop` | 悬空的 `tool/result` 不进投影（pair-or-drop） |
| `TestProjectMessagesHonoursBaseSeq` | 折叠后数组下标 ≠ seq，遮蔽区间必须加偏移 |

**端到端（假模型 + 真 Redis）**

| 用例 | 守住的不变量 |
|---|---|
| `TestTurnAndLogInvariants` | 一轮对话落 `user/message` + `assistant/message`；`system/prompt` 只写 1 次 |
| `TestSystemPromptWrittenOnlyOnce` | 3 轮之后 `system/prompt` 仍是 1 条（每轮重写会白涨日志） |
| `TestToolCallResultsArePaired` | `tool/call` 与 `tool/result` 数量恒等，且每条 result 都能找到配对的 call |
| `TestApprovalSuspendsThenReallyExecutes` | 挂起时**必须带可执行命令**；批准前不许执行；批准后恰好执行 1 次；落审计；重复批准不重复执行 |
| `TestRejectDoesNotExecute` | 拒绝不执行，但仍留审计 |

## 设计原则（借自 dsh 的 testing 文档）

1. **只 mock LLM**，Redis 用真的 —— 记忆/审批/折叠逻辑正是要验的东西，mock 掉就白测了。
2. **断言落到 Redis 的事件序列**，不断言模型回复的文案 —— 文案是模型自由发挥的，断言它必然不稳定。
3. **外部依赖不可用就 skip，不要 fail** —— 否则没人愿意在本地跑。
4. **场景驱动，不写死回复** —— 见下。

## 场景文件格式

```jsonc
{
  "default": { "text": "收到。" },          // 兜底回复
  "rules": [
    {
      "match": "愤怒",                       // 请求里最后一条 user 消息包含它就命中
      "reply": {
        "text": "",                          // 可选
        "tool_calls": [                      // 可选：让假模型发起工具调用
          { "name": "execute_system_defense",
            "arguments": "{\"emotion\":\"愤怒\",\"threat_level\":\"high\"}" }
        ]
      },
      "max_fires": 1                         // 默认 1：防止工具调用死循环
    }
  ]
}
```

两个容易踩的点，代码里已经处理：

- **`max_fires` 默认 1**：模型回了 `tool_calls` 之后，下一轮请求会带着工具结果再来。
  规则无限触发就会死循环；限制次数就自然得到"调一次工具、然后收尾"的确定性行为。
- **请求不带 `tools` 时一律回 `default`**：压缩器（`agent.Summarize`）用同一份凭证但不带工具，
  如果拿它去匹配"愤怒"规则，就会把摘要请求变成一次防御工具调用。这类交叉污染很隐蔽。

## 还没有的（后续补）

- **断言"模型收到了什么"**：`Server.Requests()` 已经在记录了（`RequestInfo`），
  目前还没写用例去断言 —— codex 的 `saw_function_call` / `function_call_output_text` 就是这个思路。
- **快照测试**：把一轮完整的事件序列签成 golden 文件，改动后 diff（codex 用 `insta`）。
- **`MaxHistory` 换成 token 预算**之后，在这里加"20 条短消息不压缩 / 5 条长文触发压缩"的用例。
