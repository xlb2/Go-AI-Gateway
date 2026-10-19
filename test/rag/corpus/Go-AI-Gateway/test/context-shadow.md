# CM-3 影子上下文计算与来源基础（默认关闭）

2026-09-24 CM-3k隔离A/B应用实现（待WSL验）：影子预算只有在真实基线脚本显式加 `--apply-shadow` 且预算为正时，才通过评测专用ChatModel包装器把安全候选传给主循环模型适配器。系统消息、当前完整用户轮、所有已选整轮必须齐全；来源不匹配、输入计数漂移、未知schema、未解决约束/回读、非法索引或拆半轮均保留全量输入并报告原因。SDK建流重试重用同一候选；下次逻辑请求以新影子报告替换候选。摘要模型与子agent使用未包装工厂，普通Harness/CLI不受影响。上一批WSL20/223仅验CM-3j，新加回归5项待本批重跑；三标签 `go vet` 退出0；未本机测试/真实模型、未提交/push。

隔离真实对照命令、报告的 `shadow_input_applications` 字段和估算/usage差别见[上下文成本基线](contextbaseline/README.md)。两种模式都会产生真实模型费用；先等测试通过再做同一case/预算的候选观测与应用配对。报告只证明消息切片传给适配器，不是HTTP wire捕获；质量仍须人工逐题复核，缓存与费用仍未知。此次仅为评测包装器，不是生产切换或CLI功能。

2026-09-24 CM-3j WSL验收：首轮影子定向/全量失败定位为端到端用例用了旧 `harness.New()`，未装配 Runtime 历史来源读/搜工具，假模型返回未知工具兜底，负向用例也可能被掩盖。改用 `harness.NewFromEnv` 并增加实际工具结果断言后，用户运行 `bash scripts/test-fast.sh -tags knowledgeintegration`，输出20项与223项均通过、0跳过，覆盖成功回读和搜索/失败/外存指针对照。生产影子逻辑与真实SDK输入策略未改；三标签 `go vet` 退出码0，差异格式检查通过。本机未运行测试或 `go run`，未提交/push。下一步按计划做隔离质量/成本对照，不切生产策略。

CM-3j拟覆盖的行为：模型工具 `read_history_source` 返回非空普通来源页，或成功展开并返回非空外存正文后，才将其source ID加入本轮请求级去重收集器；`search_history_sources`/`browse_history_sources` 的预览不算已读，失效来源、未展开的外存定位符及外存展开失败也不计入。影子只接受与初始Harness历史完整匹配、仍映射到当前SDK消息的非system且未中断来源；不可映射记录为 `unresolved_retrievals` 并要求复核。有效来源把其所在完整用户轮（连同assistant/tool调用和结果）按回读先后加入预算候选，优先于普通近期轮；超预算不拆轮，报告 `omitted_evidence_turns`，随后仍按近到远填充近期轮。显式用户约束依旧高于回读证据。`retrieved_sources`/每条source的 `retrieved` 仅描述本轮成功工具调用及快照映射，不代表模型确实理解证据。没有改真实SDK输入、来源快照、默认预算或持久记忆。

2026-09-24 CM-3i验收：用户WSL `bash scripts/test-fast.sh -tags knowledgeintegration` 输出17项与220项均通过、0跳过。TestContextShadowApprovalEvidence的approve/reject/failure三个分支都验证了显式用户来源、到当前请求的完整连续候选后缀、required_over_budget状态，以及来源匹配、SDK输入和执行次数不变。影子仍不改变实际请求；下一切片将评估成功回读的旧来源作为影子预算候选。

2026-09-24 CM-3h验收：用户WSL定向17项及knowledgeintegration全量220项通过，均0跳过。ResumeTurn显式来源进入必留候选，SDK输入不变，已派发工具不重放。审批续答显式约束为当前待验切片。

2026-09-24 CM-3h恢复入口约束回归（待WSL验）：增强TestResumeTurnContinuesWithoutReplayingTool，以未中断的原用户消息来源ID调用ResumeTurn，断言其进入影子候选必留集合且没有未解决约束；真实SDK请求仍含原历史，既有已派发工具不重放与正常收尾断言保持。未增加顶层用例数，也未改生产输入。定向17/全量220待用户重验；通过后再单独覆盖审批续答。

2026-09-24 CM-3g验收：用户WSL定向17项及knowledgeintegration全量220项通过，均0跳过；覆盖显式来源约束在普通Harness轮次的候选标记，以及快照与实际历史对象的映射修复。影子计算仍未用于真实SDK裁剪，CLI无pin命令/持久约束。下一切片增强既有ResumeTurn测试：显式用户来源在恢复入口也必须映射为候选必留，并保持原模型输入与工具不重放行为。审批续答另行验收。

2026-09-24 CM-3g来源约束映射修复（待WSL复验）：用户重跑后定向和knowledgeintegration全量均在present断言失败，missing/assistant对照通过。来源快照与实际Harness历史由两次读取构造，DeepEqual只证明值相等，不保证指针相同；必留标记原先挂在快照对象上，影子选择器按Harness消息指针查找而漏标。现按已匹配的相同下标绑定Harness消息。实际SDK输入仍不裁剪；之前两条WSL命令均失败，定向17/全量220待复验。本轮未在Windows侧运行Go验证，未提交/push。

2026-09-24 CM-3g首次WSL回归未通过：用户定向与knowledgeintegration全量均在新用例present子场景因e.h为nil而panic；newEnv不会自动构造Harness。已补显式Harness初始化，尚未重跑。其它包显示PASS不抵消e2e包失败，整体仍待验；修复后预计17/220。15/218仅为前一批CM-3f。

2026-09-24 CM-3g（待WSL验）：WithRequiredHistorySources允许可信调用方显式指定最多16个来源ID。快照须证明来源是未中断的用户消息；助手建议、摘要、工具结果、缺失/过期ID不能成为用户约束。有效来源将其所在用户轮及之后的完整连续后缀作为影子候选必留区，过预算状态为required_over_budget；缺失或非用户来源计入unresolved_requirements并触发needs_constraint_review。实际请求保持原GetHistory结果，未把候选发给模型。

该接口仅是Harness内部显式调用契约，尚无CLI pin命令或跨进程持久约束配置；不应暗示用户已能在CLI交互中固定历史。调用方必须拿到用户明确选择的ID，不能从助手文字或模型工具参数推断“必须保留”。本批不处理被摘要折叠、已不在当前投影中的旧用户消息，此时明确标未解决。

| 用例 | 守住的边界 |
|---|---|
| TestContextShadowExplicitConstraintSelection（新增） | 正确 user index 去重、整段连续后缀保留、超预算报告、非user/越界index不接受 |
| TestContextShadowRetainsRequiredUserSource（新增） | 显式用户来源被影子强制保留；缺失ID和助手来源均 unresolved；实际模型输入仍包含原历史 |

三标签go vet通过。WSL运行 `bash scripts/test-fast.sh -run 'HistorySnapshot|ProjectionSource|ContextShadow|ContextBaseline|ResumeTurn'`（预计17项）及 `bash scripts/test-fast.sh -tags knowledgeintegration`（预计220项）；15/218是前批CM-3f证据，不能覆盖本批。未在Windows本机运行新脚本/测试程序，未提交/push。

2026-09-24验收：用户WSL定向15、knowledgeintegration全量218通过，0跳过，覆盖CM-3f Harness来源观察普通/恢复/审批路径。当前进入显式用户来源约束；只允许调用方明确提供来源ID并验证其为完整的用户消息，不从助手文本、模型摘要或语义推断约束。以下CM-3g代码与回归尚待WSL。

2026-09-23 CM-3f：用户上一批定向13/全量217通过、0跳过。Harness普通对话、恢复、审批续答接ObserveHistorySources，仅正影子预算启用；先保留原GetHistory结果，再额外读取快照，完整序列（含工具元数据）逐条相等才按消息对象绑定来源。不使用来源快照替换实际输入。影子报告sources包含输入index/source_id/kept/summary/interrupted，不含正文或工具参数；unmapped_messages包含系统提示、本轮新消息及未确认来源的消息。

source_status为matched/mismatch/unavailable/unsupported；绕过Harness时为not_observed。失败只影响诊断，不传播存储原始错误到报告；关闭影子不增加快照读取。matched只说明第二次快照与实际历史投影相等，不能证明两次读取属于同一个原子事务，尤其无法检测内容完全相同的日志重建。ID对应第二次快照，回读仍验证代次；本阶段不能把该诊断用作生产输入裁剪的授权依据。额外读取增加诊断开销，不能用此次耗时直接比较先前关闭影子的基线。

| 用例 | 守住的边界 |
|---|---|
| TestContextShadowSourceDiagnostics（新增） | 重复正文按角色/位置映射；不匹配、读取失败、不支持不猜来源；关闭影子零额外读取；实际输入对象不替换 |
| TestContextBaselineHarnessReport（加强） | 实例重建后的历史来源可回读并对应实际SDK输入，候选省略旧历史不影响实际输入 |
| TestContextShadowApprovalEvidence（加强） | 批准/拒绝/执行失败续答工具对保留来源，回读对应实际SDK内容 |
| TestResumeTurnContinuesWithoutReplayingTool（加强） | 恢复路径保留显式指定用户来源，实际输入不变，且已派发工具不重放 |

三标签go vet通过；WSL `bash scripts/test-fast.sh -run 'HistorySnapshot|ProjectionSource|ContextShadow|ContextBaseline|ResumeTurn'` 预计15项，全量knowledgeintegration预计218项，运行待验。下一步显式约束与检索候选，未切换生产上下文策略。以下为历史切片记录。

2026-09-23 CM-3e：用户上一批定向11/全量215通过、0跳过。本轮GetHistorySources在同一Lua快照内读取折叠提示、日志代次和尾部，只有尾部存在当前可读版本的前缀摘要（from=0，to>=base-1且to早于摘要自身）才省略前缀；否则回退完整日志。不改写折叠提示，不重建缓存。水位缺失/非法/越界、旧版摘要位置提示或新日志残留提示均不能自行证明可跳过前缀。

返回真实base用于投影和来源ID，摘要前尚未覆盖的近期消息必须保留。已覆盖前缀不读取也不校验，与上一批全日志严格校验的范围不同；读取到的活跃区坏JSON/未来版本仍整体拒绝。脚本仍可能读取完整日志或很长的活跃尾部，无字节上限，未做性能计时/并发压力测试；尚未接入默认Harness，不代表模型输入或账单已减少。

| 新增用例 | 守住的边界 |
|---|---|
| TestHistorySnapshotVerifiedFold | 全量/尾部投影及ID一致，未覆盖近期消息保留，来源可回读；覆盖区故障不加载，活跃区故障拒绝 |
| TestHistorySnapshotUnprovenFoldFallback | 旧版摘要位置、越界/负数/小数/非数字提示回退；日志重建后数值有效的残留水位不漏新前缀 |

三标签go vet通过；WSL `bash scripts/test-fast.sh -run 'HistorySnapshot|ProjectionSource|ContextShadow|ContextBaseline'` 预计13项，knowledgeintegration全量预计217项，运行待验。下一步接Harness来源观察，仍不直接启用候选上下文。以下为历史切片记录，以上述状态为准。

2026-09-23 CM-3d：用户定向9/全量213通过，0跳过。新增GetHistorySources显式快照API，原子读取owner日志代次和全量事件，将投影来源绑定为read_history_source可读的ID，再复用TrimToBudget。裁剪通过消息对象对应来源，不按相同正文猜测；摘要ID指向摘要本身，中断前缀不是原文。空日志返回空列表；坏JSON或未来版本拒绝整个结果，不部分成功。

这不是默认GetHistory替换：尚未接入Harness/影子报告，读取量为完整日志，无事件/字节上限；Redis脚本期间占用服务器，应先完成活跃尾部快照读取再接生产。maxTokens<=0沿用现有硬裁剪的“不裁剪”含义，不等于影子预算关闭语义；极小正预算可以裁空。原生外部修改列表（LSET/LTRIM）不在代次保证范围，回读仍核对owner及代次。并发重建的原子性由脚本提供，本轮回归仅验证顺序追加/重建，未声称完成并发压力测试。

| 新增用例 | 边界 |
|---|---|
| TestHistorySnapshotTrimAndReadback | 重复正文与工具边界裁剪不串来源，ID可回读，追加不失效，跨owner与重建后旧ID拒绝 |
| TestHistorySnapshotSummaryAndInvalid | 空日志、摘要自身来源、中断信息、坏JSON及未来版本整体拒绝 |

三标签go vet通过；WSL `bash scripts/test-fast.sh -run 'HistorySnapshot|ProjectionSource|ContextShadow|ContextBaseline'` 预计11项，全量knowledgeintegration预计215项，运行待验。以下为此前切片记录。

2026-09-23 最新验收：用户WSL影子/基线定向7、全量211通过，0跳过，覆盖下方CM-3b待验记录。本轮新增投影来源两项，运行尚待验。

ProjectMessagesWithSources与原ProjectMessagesFrom共用投影实现，在消息生成时记录原始seq，不用正文匹配或投影下标推算。摘要指向摘要事件自身，CoveredFrom/To单独表示覆盖范围；中断消息保留Interrupted，完整工具对记录各自来源，悬空或未配齐工具消息不会产生来源。Seq仅在调用方提供的日志快照内有意义，尚未绑定owner/日志代次，不能充当稳定回读ID；也尚未对齐后续token裁剪或接入影子报告。

| 新增用例 | 守住的边界 |
|---|---|
| TestProjectionSourceMapping | 非零起点、重复正文、摘要置顶和覆盖、孤立结果过滤、工具结果顺序与中断标记不造成来源错配；消息与原投影一致 |
| TestProjectionSourceDropsIncompletePairs | 不完整工具对不进入来源列表，旧格式用户事件可推断类型 |

本轮三标签go vet通过；WSL请运行 `bash scripts/test-fast.sh -run 'ProjectionSource|ContextShadow|ContextBaseline'` 和 `bash scripts/test-fast.sh -tags knowledgeintegration`，预计9/213项。下一步绑定存储快照代次与裁剪来源，再推进显式约束和相关历史组装；实际模型输入未改变，不能声称已节省。

以下为此前切片记录，待验数量以上述最新状态为准。

2026-09-23：用户WSL ContextShadow|ContextBaseline定向6、全量210通过，均0跳过。后续新增TestContextShadowApprovalEvidence（批准/拒绝/执行失败三个子场景），强制1-token影子预算，核对超预算诊断、候选保留审批工具对位置、实际SDK仍含原用户历史和对应结果、执行次数正确；该新增用例运行待验。只核对SDK角色/内容与候选位置，未新增工具call ID快照断言。

真实基线新增可选环境变量CONTEXT_BASELINE_SHADOW_BUDGET，必须为正整数；未设置保持关闭，错误值在采集前拒绝。报告metadata含context_shadow_budget与context_shadow_applied=false，每次主请求的context_shadows记录估算；不代表候选经过模型质量检验。以下4000仅为实验示例，不是默认值或推荐预算，命令仍会按原输入调用真实模型并计费，本批无需先执行：

```bash
CONTEXT_BASELINE_SHADOW_BUDGET=4000 bash scripts/context-baseline.sh --real reading-dev
```

本批仅基线入口及回归，三标签go vet通过；用户WSL ContextShadow|ContextBaseline预计7、全量211待验，未本地测试/真实采集。后续仍需用户约束与来源选择，不因审批路径影子证据存在就切换默认策略。

WithContextShadow(ctx, budget)仅为该context下的主循环请求计算候选统计。EvaluateContextShadow是纯计算，不调用模型/检索/摘要，不修改消息，不持久化候选；结果随Progress.Shadow报告。未加CLI开关或默认预算；回归与上述基线入口可显式开启。

规则：所有system消息、最后一条user开始的当前轮，以及显式指定用户来源要求保留的连续后缀先确定。随后按模型成功回读的来源顺序尝试其完整历史轮；某个回读轮放不下时，不拆轮或尝试其后更低优先的回读候选。最后再按近到远填充普通历史轮，遇到第一轮超预算即停止，不跳选更便宜旧轮。无user边界时全部必留。预算包括现有估算器中的工具定义、推理及协议结构，非真实分词或账单。输入必须已经协议合法，本算法不修复悬空工具消息。

状态：within_budget只表示估算放得下；mandatory_over_budget表示普通必需内容已超出、仍保留；required_over_budget表示显式来源及其连续后缀已超出、仍保留；unknown_tool_schema表示不能计算可信总预算；disabled表示没有正预算。kept_indices只对应此次SDK输入下标，不是持久seq或来源ID。省略历史、约束/回读来源未映射或回读整轮放不下时，needs_constraint_review=true；不能因此直接切为生产输入策略。

| 用例 | 边界 |
|---|---|
| TestContextShadowWholeTurns | 当前工具对不拆、旧轮整组省略、充足预算保留全部、输入不修改 |
| TestContextShadowMandatoryAndUnknown | 当前输入超预算、工具定义未知、无user边界整组必留 |
| TestContextShadowSelectsRetrievedWholeTurnBeforeRecentHistory | 成功回读来源所在整轮先于普通近期轮；过大证据不拆分且明确省略 |
| TestContextShadowUsesSuccessfulReadbackAsEvidence | Runtime实际读取来源正文，映射到matched来源；证据只进影子预算且不裁剪真实SDK输入 |
| TestContextShadowDoesNotPromoteSearchOrFailedReadback | Runtime实际执行搜索/读取；预览、失败和未展开外存定位符不计为已读证据 |
| TestContextBaselineHarnessReport（加强） | 1-token影子预算只产生超预算诊断，实际SDK仍收到重建前历史并完成三轮 |

以下预计6/210及“未有来源映射/显式约束”的说明是2026-09-23 CM-3a初版时点的历史记录，不代表当前能力；当前状态以本页顶部CM-3j交接为准。CM-3a当时的验证命令为：

```bash
bash scripts/test-fast.sh -run 'ContextShadow|ContextBaseline'
bash scripts/test-fast.sh -tags knowledgeintegration
```

预计6项/210项。此切片不是CM-3全部实现：没有来源映射、相关历史选择、显式用户约束层，也未独立验收审批续答影子路径或真实质量对照。下一步先补可回溯的约束/来源选择设计和普通/恢复/审批路径回归，再考虑隔离数据实验，不能凭候选token更少宣布节省。
