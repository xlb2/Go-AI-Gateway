# CM-0 输入与模型调用观测

2026-09-23 CM-0b调用计量回归已验（用户WSL定向8项/全量195项通过、0跳过）：覆盖默认Runtime和旧入口的主/子调用、摘要、Harness重试及已读部分usage。新增3项ModelAccounting回归，加强CLI跨轮/多步SDK断言；静态检查与CLI构建通过。下方CM-0a的192项通过不是本批证据。真实基线与完整费用仍未完成。

状态：2026-09-23 用户提供 WSL 日志，定向7项、带knowledgeintegration全量192项通过，均0跳过；CM-0a回归已验。本批只建立观测，不改变上下文、tokenmeter 的压缩策略、请求或原使用量持久化逻辑。真实模型输入基线及省钱效果尚未验证，整个CM-0未完成。助手未重跑测试，未提交/push。

## 可读统计

CLI 每轮显示 `Input estimate (main loop)`：本轮各次逻辑请求的累计启发式估算，按 system/user/assistant/tool-results/reasoning/structure/tools 分桶。重复发送的历史和工具定义会重复计入，符合多步请求的输入成本来源。各桶互斥；角色不等同于历史/新输入。结构桶含调用参数、ID 和消息结构估算；工具桶用同次绑定的工具定义转为 JSON 估算，不额外调用工具 Info。

`Usage (reported steps x/y)` 是完整读取的步骤中有 SDK usage 的数量/发起的逻辑请求数，显示这部分实报输入与输出。无 usage 或建流失败不伪造零；部分覆盖时不是总账。相同步骤中多次累计 usage 通过 SDK ConcatMessages 合并，只报告一次。cache 一律标 unknown：当前 SDK 用非指针整数字段表示缓存量，无法区分缺失和真实零。

CM-0a步骤汇总的限制：模型内重试、摘要、子运行不在该汇总内，中途断流的部分 usage 也未计入；这些现在另见下方CM-0b尝试汇总。输入估算只针对现有文本调用，不计多模态及模型选项开销；工具结构 JSON 不保证与服务商 wire 序列化相同。schema 估算失败标 unknown，合计只含已估部分。供应商 usage 与估算分开，不能据此算缓存命中率或完整费用。

## CM-0b 尝试汇总（回归已验）

`Model attempts (main/child/summary)` 按一次CLI操作收集，遇到下一条普通输入或审批命令重新计数。主/子模型的包装顺序为 Loop → retry → accounting → SDK；摘要为 summarize → accounting → SDK Generate。每次SDK调用前计数，失败重试也有独立尝试。上下文携带单个线程安全收集器，子任务继承但不向父主循环进度刷屏。仅保留数值，空间不随调用数增长，不新增协程或持久化事件。

- `input-estimate` 包含每次尝试重复输入的估算，工具定义来自同次BindTools；unknown-schemas大于零时不完整。不是供应商实报。
- `usage=x/y` 为收到过usage的尝试数/SDK调用总数。Prompt/Completion按同次调用累计快照的最大值增量累加，与现有SDK拼接语义一致；不会把多次相同或递增快照重复加总。Generate也纳入。
- 中断前已经消费的usage保留，未读到或未返回的用量未知；即使x=y也不证明服务商最终计费已全部取得。
- observed-errors仅表示实际观测到的建流/Generate/读流错误；提前Close、取消后未继续读等不一定经过读错误回调。零错误不代表业务完成。
- 三类尝试统计互斥，可以相加；**不能再加上上方主Loop步骤统计**。后者是排查输入组成的另一个视图。
- 计数边界是SDK方法调用，包含本Harness的重试，不保证看见SDK、网络代理或服务端内部重试。自定义Dependencies/ConfiguredLoop绕开Runtime的模型，须显式接入才有尝试统计，零条不证明未调用。
- 现有事件日志和EventUsage不改，尝试统计只在本次运行内，退出后需保留终端报告。缓存字段存在性/费率/服务商最终账单未知，CLI明确显示Cache/fees unknown，不伪造总价。

| 本批用例 | 不变量 |
|---|---|
| TestModelAccountingRuntime | 真Harness/Runtime路径中主调用3次、子调用2次、摘要1次；两次429分别计入；缺失usage不造零；新收集器不串轮 |
| TestModelAccountingPartialUsage | 一次流100/2→100/5只计100/5；断流仍保留部分usage并透传错误；摘要40/4独立计入 |
| TestModelAccountingConcurrentSummary | 并发调用和读取快照不丢计数；主/子/摘要桶隔离；不同操作不串账 |
| TestCLIContextObservationAcrossTurns、TestCLIToolProgress（加强） | 真实SDK调用计数通过CLI显示；跨轮归零，多步覆盖正确 |

用户WSL本批命令：

```bash
bash scripts/test-fast.sh -run 'ModelAccounting|ContextObservation|CLIToolProgress|RuntimeHarnessAssembly'
bash scripts/test-fast.sh -tags knowledgeintegration
```

用户WSL实测定向8项、全量195项通过，均0跳过。新增并发收集器可另用 `bash scripts/test-fast.sh -run ModelAccounting -race` 检查数据竞争；本机未运行任何测试。

静态入口核对：RunAgentTurn与ResumeTurn均调用h.summary和h.loopFactory；resolveToolApproval追加调用对后调用h.runLoop，同样落到Runtime.NewLoop。CLI在审批分派之前装入本次收集器；恢复入口可由调用方通过WithModelAccounting装入。未声称这三个入口的新增计量都已独立运行验证。

## 不变量与用例

| 用例 | 保证 |
|---|---|
| assembly/TestContextObservation | 输入不被观测修改；工具交棒后的正文、推理、结构都进入对应桶；定义非零；桶可加总；重复累计 usage 不翻倍，缺失步骤不造数 |
| assembly/TestContextObservationFailedRequest | 建流失败仍记录尝试输入，不产生 usage，不吞原错误 |
| e2e/TestCLIContextObservationAcrossTurns | 真 SDK → Harness → CLI 两轮计数各自归零；历史仍在，诊断不进入模型 |
| e2e/TestCLIToolProgress（加强） | 工具多步请求经 SDK 汇总为 2/2；缓存未知，维持收起显示 |
| assembly/TestConfiguredLoopsKeepDependenciesSeparate（兼容调整） | 增加输入附件后既有工具诊断和实例隔离仍守住 |

用户在 WSL 执行：

```bash
bash scripts/test-fast.sh -run 'ContextObservation|CLIToolProgress|ConfiguredLoop'
bash scripts/test-fast.sh -tags knowledgeintegration
```

新增3个顶层用例；上述命令的用户WSL实际结果分别为7项、192项通过，均0跳过。

## 基线采集的下一步

现已有 [固定基线采集流程](contextbaseline/README.md)：4个开发场景与2个留出场景，显式真实调用脚本和逐轮JSON报告；本批回归和真实采集尚待用户WSL。报告不自动判质量，未知费用/缓存为null，不把Harness实例重建冒充真实时间间隔。

CM-0a 可先在 CLI 同一任务连续两轮观察桶变化，保留终端汇总即可，不额外导出正文。等待 CM-0b 之后再登记完整费用和对照数据。开发/留出各自覆盖：连续源码阅读；退出后重启“继续”；用户 A 改 B；单编号/路径；换话题；长文中尾回读；失败取消；作用域；短问答；同义改写。留出案例不用于调参，精确提示和期望事实在运行前固定，不能看结果后改门槛。

报告列：版本/配置、场景与使用节奏、逻辑请求数、各估算桶、已覆盖/未知 usage、缓存字段可信度、主/辅/重试费用（未知则注明）、首字/整轮耗时、约束和事实命中、引用支持、任务完成与失败。首批不具备的项填“未覆盖”，不填零。不启动真实模型或为缓存保活。
