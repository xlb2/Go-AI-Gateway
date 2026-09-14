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
- 全部用例约 **1~2 秒**（首次编译几秒）。
- 测试在**进程内**用 `httptest` 起假模型，**不启动任何独立服务** ——
  在公司电脑上这是触发端点防护（EDR）告警最少的一条路。详见项目 README 的「在公司电脑上开发」一节。

### 2. 手动起假模型，让整个服务跑在假模型上

```sh
scripts/build.sh        # 先编译到 bin/（不要用 go run，原因见项目 README 的 EDR 一节）
scripts/fake-model.sh
# 然后把 .env 改成：
#   VOLC_BASE_URL=http://127.0.0.1:9099/api/v3
# 再起服务：scripts/run-api.sh
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
| `tokenmeter` 包的 6 个用例 | 中英文分档估算、工具参数也算进成本、预算自相矛盾会被修正、校准系数会移动且被夹住、裁剪不会把工具配对从中间切断 |
| `retry` 包的 6 个用例 | 退避指数增长并夹上限、抖动留在 ±jitter 内、只重试"再试可能好"的错误（429/5xx/连接/超时）、参数错与鉴权错坚决不重试、混合消息以"不重试"优先、状态码解析 |
| `sandbox` 包的 14 个用例 | **argv 化**：拒绝 `cmd`/`sh`/`powershell`（含 Windows 风格路径——黑名单不能因为跑在 Linux 就漏判）、不误杀名字含 sh 的普通命令、缺字段的计划被拒、`SANDBOX_ALLOW_SHELL` 只认真值、计划摘要说清要干什么。<br>**容器后端**：隔离参数一个不少、镜像之后才是命令、只挂工作目录、去调 docker 而不是裸跑、内置文件动作不进容器、如实上报 full、**docker 不可用时拒绝一切执行而不是退回裸跑** |
| `metrics` 包的 4 个用例 | 桶是**累计**语义（写成分档 P99 会静默算错）、导出符合 Prometheus 三件套、计数器/求和/仪表老行为不被改坏、桶上界不用科学计数法 |

**端到端（假模型 + 真 Redis）**

| 用例 | 守住的不变量 |
|---|---|
| `TestTurnAndLogInvariants` | 一轮对话落 `user/message` + `assistant/message`；`system/prompt` 只写 1 次 |
| `TestSystemPromptWrittenOnlyOnce` | 3 轮之后 `system/prompt` 仍是 1 条（每轮重写会白涨日志） |
| `TestToolCallResultsArePaired` | `tool/call` 与 `tool/result` 数量恒等，且每条 result 都能找到配对的 call |
| `TestApprovalSuspendsThenReallyExecutes` | 挂起时**必须带可执行命令**；批准前不许执行；批准后恰好执行 1 次；落审计；重复批准不重复执行 |
| `TestRejectDoesNotExecute` | 拒绝不执行，但仍留审计 |
| `TestApprovalReceiptReportsIsolation` | 审批回执必须写明**隔离等级与执行方式**（人得知道批的是沙箱内还是裸跑） |
| `TestApprovalRefusedWithoutIsolation` | 要求隔离却拿不到时 **fail-closed**：一次都不许执行，且仍留审计 |
| `TestApprovalWarnsWhenRunningWithoutIsolation` | 没有隔离时照常执行，但回执必须带明确警告 |
| `TestManyShortMessagesNeitherCompactNorTruncate` | 20 轮**短**消息既不压缩、也不被条数截断（40 条全留）——这是 P0-2 要修的毛病 |
| `TestLongMessagesTriggerCompaction` | 几条长消息必须触发压缩，压完落在触发线以内，并推进折叠水位 |
| `TestHugeSingleMessageFallsBackToHardTrim` | 单条就撑爆窗口时交给硬裁兜底，不硬造空摘要 |
| `TestUsageIsRecordedAndCalibrates` | 真实 usage 落盘成 `usage/report`，并喂给估算校准器 |
| `TestRepairClosesOpenTurn` | 崩溃留下的开放轮次要被认出并补**合成**收尾；幂等；不动历史 |
| `TestInterruptedStreamIsMarked` | 流被切断的回复标 `interrupted`（不写 `completed` 收尾）、投影里带说明、下一轮 Repair 能补上 |
| `TestLLMRetryRecoversFromRateLimit` | 429 两次后重试成功；留下 2 条 `llm/retry` 且写明原因；这一轮仍正常收尾 |
| `TestNonRetryableErrorFailsFast` | 400 参数错**立刻失败**，一条 `llm/retry` 都不许留 |
| `TestWriteInvariantsRejectDirtyEvents` | 缺 `ToolCallID` 的 `tool/result` 被**拒绝写入**（不是写进去再靠投影丢），合法事件照常放行 |
| `TestUnknownLogVersionRefusesToLoad` | 读到比本程序新的日志版本要明确拒绝加载；没有版本号的**老日志必须照常读** |
| `TestApprovalAppendFileNeedsNoShell` | 内置文件动作**不经 shell** 也能落盘：用**真**执行器写一个"带空格 + 中文"的路径，内容含中文也照写 |
| `TestApprovalNeedsTwoSteps` | 只敲 `auth:approve` **不执行**（只回显计划与确认码）；错误确认码不执行；正确确认码才执行且恰好 1 次 |
| `TestExpiredApprovalSaysSo` | 过期的提案批准时明确回「已超时作废」、不执行，也不静默退化成"没有待审批任务" |
| `TestMetricsExposeHistogramBuckets` | 跑完一轮后导出里必须有耗时**桶**与 `_count`（只有总和算不出分位数） |
| `TestApprovalUnderDockerBackendGoesThroughContainer` | 容器后端下外部命令**必须经 docker**（不裸跑）、参数带隔离项、回执写 `full` 且不再给"无隔离"警告 |
| `TestApproveWithoutPlanIsRefusedNotFaked` | 没有执行计划的挂起（被流水线拦下的工具调用）批准后**如实拒绝**，绝不凭空执行 |

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
