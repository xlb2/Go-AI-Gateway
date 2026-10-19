# test/ — 假模型与端到端测试

2026-10-19 仓库同步整理：RAG 的 18 份固定语料已收录到 `test/rag/corpus/`，两个显式评测入口改读仓库内副本；SHA256 全部匹配既有 `manifest.json`，未改题集和语料清单、未运行 holdout。下方用例表中 `TestRAGDevelopmentEvaluation` 与 `TestRAGRetrievalComparison` 的语料现可随单独克隆复现。Windows/Linux 目标三标签 `go vet`、固定 CLI 构建与差异检查通过，未运行程序或测试。普通回归不启用 `rageval`，也不调用真实 embedding。用户于 2026-10-19 在 WSL 运行 `bash scripts/test-fast.sh -tags knowledgeintegration`：241 个顶层用例通过、0 跳过；`bash scripts/eval-rag.sh` 执行通过，验证副本读取（仅关键词、无真实模型调用）：dev 20 题、17 可答、recall@5=0.765、完整召回12/17、无答案有候选3/3。Q02/Q11/Q12/Q15/Q17 仍有来源漏检；未重跑向量/混合评测，未运行 holdout-2。

2026-09-24 CM-3k/reading-dev 4000重复A/B复核：两份报告3轮完成且配置匹配。候选组长度101/79/48，第1轮超1字；应用组59/81/80、语义与长度门槛均通过。应用组仅第3轮实际裁剪4条消息，估算4019→3864（155 token，约该运行估算总输入0.9%）；前两轮续接都回退全量。Prompt 17055/17012、Completion 641/1004且usage均5/5，差额不能归因，费用/缓存未知。与前一对应用组两轮超长合并看，长度质量表现混合，不宣称稳定通过；保留失败后转测resume-dev/4000，不切默认。详情见 `test/contextbaseline/review-20260924-reading-shadow-ab.md`。未本机跑Go测试/模型，未提交/push。

历史记录（reading-dev首次4000真实A/B）：用户WSL两组3轮均完整。事实/分页核对通过；按题面与人工规则检查长度后，应用组第1、2轮133/189字符，超过100限制，完整质量门槛失败（候选组67/95/76，应用第3轮90）。实际在两次调用中各丢弃4条旧消息，候选估算4073→3900、4082→3909（合计346估算token，约2%总输入估算）；首轮一次mandatory_over_budget回退全量。Provider Prompt 17060→16734、Completion 723→621，但模型回复/后续历史不同，不能将总差额归因于裁剪；费用/缓存未知。详情见 `test/contextbaseline/review-20260924-reading-shadow-ab.md`。当时计划重复reading-dev/4000核对质量失败是否复现。未本机跑Go测试/模型，未提交/push。

历史记录：2026-09-24 CM-3k WSL回归与首对真实A/B复核：定向5项和knowledgeintegration全量228项通过、0跳过。`-run` 未匹配包的 `no tests to run` 是预期提示。reading-dev/8000两组答案的固定期望均通过，但应用报告实际删消息数为0，不能视为节省证据；prompt/completion差异不归因于策略，费用及缓存未知。详见 `test/contextbaseline/review-20260924-reading-shadow-ab.md`。修复后的本机三标签 `go vet ./...`、差异检查通过，未本机运行Go测试/真实模型，未提交/push。当时下一次诊断配对计划使用4000；此前把e2e候选估算6790误称为必需下限，已纠正。

2026-09-24 CM-3j验收：首轮失败来自旧 `harness.New()` 测试夹具没有Runtime装配的来源读取/搜索工具，假模型返回未知工具结果，负向用例也可能被掩盖。修正为 `harness.NewFromEnv` 后，正向断言实际读取正文，负向断言实际搜索/读取结果。用户WSL `bash scripts/test-fast.sh -tags knowledgeintegration` 报告20项与223项全部通过、0跳过。带三标签 `go vet` 退出码0、差异检查通过；本机未跑测试/`go run`。下一步按计划做隔离质量/成本对照，不切生产输入。

2026-09-24 CM-3i验收：用户WSL运行 `bash scripts/test-fast.sh -tags knowledgeintegration`，输出17项与220项均通过、0跳过。批准/拒绝/执行失败三条审批续答都保留显式用户来源和完整连续候选后缀，预算不足诊断明确，实际输入及执行语义未变。下一切片为旧来源检索证据的影子预算选择，尚未接管模型输入。

2026-09-24 CM-3h验收：用户WSL定向17项、knowledgeintegration全量220项均通过、0跳过。ResumeTurn显式指定用户来源后，影子候选标为required/kept且实际SDK输入不变，已派发工具不重放。新审批续答断言仍待本批验证。

2026-09-24 CM-3h恢复入口约束回归（待WSL验）：增强既有TestResumeTurnContinuesWithoutReplayingTool，验证显式指定的未中断用户来源在ResumeTurn影子候选中required/kept、无未解决约束，同时SDK实际输入保留该用户消息，已派发工具不重放。未增加顶层测试数、未改生产代码。WSL定向仍为17项、knowledgeintegration全量220项，待重验；本地未运行Go命令。

2026-09-24 CM-3g WSL验收：用户运行 `bash scripts/test-fast.sh -run 'HistorySnapshot|ProjectionSource|ContextShadow|ContextBaseline|ResumeTurn'`，17项通过、0跳过；随后 `bash scripts/test-fast.sh -tags knowledgeintegration`，220项通过、0跳过。显式来源约束与对象映射修复已验。影子选择仍仅诊断，不改变SDK输入。下一切片覆盖ResumeTurn显式约束；本轮不代表恢复入口新断言已通过。

2026-09-24 CM-3g来源约束映射修复（待WSL复验）：用户重跑后定向和knowledgeintegration全量均在TestContextShadowRetainsRequiredUserSource/present断言失败，missing/assistant对照通过。根因是来源快照消息与Harness历史消息为不同指针；值相等但约束被写入快照对象，影子按Harness对象查找时漏掉标记。现改为按已验证相同下标绑定Harness历史对象，实际模型输入不变。此前两条命令均失败，本批未在Windows侧跑Go验证；WSL定向17/全量220待复验，未提交/push。

2026-09-24 CM-3g首轮WSL回归失败与修复（待重跑）：用户执行定向与knowledgeintegration全量时，均在TestContextShadowRetainsRequiredUserSource/present因newEnv未构造Harness而nil panic；根因为测试fixture漏设e.h，非生产路径失败。已在用例显式调用e.newHarness(&recordingExecutor{})修复，未重跑；15/218是前一CM-3f批结果，不代表本批通过。新回归仍待定向17/全量220确认，未本地测试/模型、未提交/push。

2026-09-24 CM-3g显式来源约束（未提交、待WSL验）：基于已验CM-3f来源映射，新增WithRequiredHistorySources供可信调用方显式传入最多16个来源ID；仅完整、未中断的EventUserMessage可成为必留约束。影子候选保留该用户消息至当前轮的连续后缀，预算不足报告required_over_budget；缺失/助手来源/超额ID记未解决并要求复核。实际SDK输入未变，没有自动提取、CLI pin命令或持久pin状态。新增ContextShadowExplicitConstraintSelection与ContextShadowRetainsRequiredUserSource（缺失/助手对照）；三标签go vet通过，WSL定向预计17/全量220待验。此前15/218只覆盖CM-3f，不覆盖本批。未本地go test/go run、未提交/push。

2026-09-24 CM-3f验收与CM-3g显式约束边界（未提交）：用户WSL定向15、全量218通过0跳过，覆盖普通/恢复/审批三入口来源观察。下一步只处理显式用户指定的历史来源如何在影子候选中被强制保留；不做模型自动提取或把助手答案升格为用户约束。CM-3f静态检查通过；CM-3g待实现和WSL复验。CM-3仍未完成，未本地测试/模型、未提交/push。

2026-09-23 CM-3f Harness来源观察（未提交）：用户定向13/全量217通过0跳过。普通对话、恢复续跑和审批续答仅在影子开启时额外读取来源快照，整组角色/正文/工具元数据一致才绑定实际消息对象；失败/不支持/不一致只记状态，不替换输入。影子报告新增来源ID、输入位置、候选保留、摘要/中断标志及未映射数量，本轮新生成消息不猜ID。新增SourceDiagnostics一项并加强基线/审批/恢复既有用例，三标签go vet通过，WSL含ResumeTurn定向15/全量218待验。额外读取有成本，matched只证明快照投影一致，不证明两次读取原子或真实质量/token节省。下一步显式约束层与候选检索组装，CM-3未完成。未本地测试/模型、未提交/push。

2026-09-23 CM-3e活跃尾部快照（未提交）：用户快照/投影/影子/基线定向11、全量215通过0跳过。GetHistorySources在同一Lua快照内核对折叠水位及尾部摘要覆盖证据，有证据才读活跃尾部，缺失/非法/旧位置/重建残留水位无证据则回退完整日志；返回原始base保持来源seq稳定。新增VerifiedFold/UnprovenFoldFallback两项，三标签go vet通过，WSL合并定向13/全量217待验。已覆盖前缀不再读取或校验，活动区坏数据仍拒绝；无读取字节上限、无性能数字或并发压力验收。未替换默认GetHistory/接Harness影子，下一步接真实调用链来源观察与约束层，CM-3未完成。未本地测试/模型、未提交/push。

2026-09-23 CM-3d快照来源与裁剪（未提交）：用户定向9、全量213通过0跳过。新增显式GetHistorySources，Lua一次读取代次与完整日志，投影后复用既有硬预算裁剪，来源绑定owner/代次/seq，可由ReadSource回读。新增HistorySnapshot两项，覆盖重复正文、工具边界、摘要、中断、追加/重建、跨owner与坏数据/未来版本拒绝。三标签go vet通过，WSL合并定向11/全量215待验。当前全日志读取且严格拒绝坏数据，未替换默认GetHistory或接影子报告；后续需活跃尾部快照优化与Harness接入，再做约束层，CM-3未完成。未本地测试/模型、未提交/push。

2026-09-23 CM-3c投影来源基础（未提交）：用户影子/基线定向7、全量211通过0跳过，覆盖CM-3b待验。现有历史投影同步记录原始事件seq，摘要指向自身并另存覆盖范围，保留中断信息，工具配对过滤与原路径共用实现。新增ProjectionSource两项，三标签go vet通过，WSL合并定向9/全量213待验。当前仅快照内映射，尚未绑定owner/日志代次或接入预算裁剪；不能作为稳定回读ID，也未改变实际模型输入。下一步接存储快照与裁剪来源对齐，再推进约束层，CM-3未完成。未本地测试/模型、未提交/push。

2026-09-23 CM-3b影子入口及审批验证（未提交）：用户影子/基线定向6、全量210通过0跳过。真实基线新增显式CONTEXT_BASELINE_SHADOW_BUDGET正整数开关，报告标记未应用实际输入；无CLI默认预算。新增ContextShadowApprovalEvidence覆盖批准/拒绝/执行失败，核对影子超预算与实际SDK原历史/结果仍在，不改生产输入或执行次数。三标签go vet通过，WSL合并定向7/全量211待验；本轮无需付费采集，约束层/来源映射与检索组装尚待实现。未本地测试/模型、未提交/push。

2026-09-23 CM-3a影子计算（未提交）：用户检索合并定向10/全量208通过0跳过。新增显式context启用的影子预算计算，系统+当前轮必留、历史整轮按近到远加入，超预算/未知工具定义明确状态，省略历史标needs_constraint_review。仅Progress与基线helper记录候选统计，不改变实际模型输入，默认关闭、无CLI默认预算。新增ContextShadow两项并加强基线SDK历史不变断言，三标签go vet通过，WSL ContextShadow|ContextBaseline预计6/全量210待验。test/context-shadow.md明确未做约束层/来源映射/检索组装及审批专项；非CM-3完成。未本地测试/真实模型、未提交/push。

2026-09-23 CM-2a字面检索（未提交）：用户定向8/全量206通过0跳过。新增SearchSources/search_history_sources，支持中文与标识符精确匹配，50事件扫描/5命中/200码点预览，携带来源ID和稳定向前游标；标明结构化外存未搜索数量，无命中不等于查遍历史。未搜索外存正文、无向量/索引/语义排序、不改旧搜索或默认上下文；50条非字节预算。新增SearchPaging/SearchToolAndCoverage两项，默认装配已同步，三标签go vet通过，WSL合并定向10/全量208待验。边界见test/history-source.md。未本地测试/真实模型、未提交/push。

2026-09-23 CM-1d来源发现（未提交）：用户默认装配1/全量204通过0跳过。新增ListSources同代次按原seq倒序游标分页，最多5条/每条200码点；追加不漂移、重建游标失效。browse_history_sources可发现ID或读锚点前后各2事件，配合read_history_source；仅原始事件浏览非关键词搜索，不改上下文默认策略。新增BrowseCursor/BrowseTool两项，默认装配集合已同步，三标签go vet通过；WSL合并定向8/全量206待验。详情test/history-source.md。未本地测试/真实模型、未提交/push。

2026-09-23 CM-1c装配断言纠错（未提交）：用户定向5通过；全量203通过/1失败/0跳过，唯一失败RuntimeDefaultAssembly仍硬编码6工具，新read_history_source使默认为7。现断言完整工具名称集合并拒重复，覆盖父/子/父请求，不修改生产逻辑、不新增顶层用例。三标签go vet通过，WSL RuntimeDefaultAssembly定向1/全量204待重验。未本地测试/真实模型、未提交/push。

2026-09-23 CM-1c已知来源工具（未提交）：用户CM-1b定向4/全量203通过0跳过。自动外存成功新增结构化spill_locator，来源页携带引用；支持ReadSource的存储新增read_history_source工具，按当前owner读取正文或显式外存分页，缺引用/外存丢失明确失败，不解析旧提示。新增HistorySourceToolSpill一项，三标签go vet通过，WSL定向5/全量204待验。来源发现、邻居工具和旧load_large_content路径仍待后续；新增schema有输入开销，不能沿用旧基线固定量。未本地测试/真实模型、未提交/push。

2026-09-23 CM-1b读取基础（未提交）：新增SourceNeighbors同代次原子读取前后各最多5条原始事件，200码点预览及来源续读；新增spill.ReadPage显式owner校验与Unicode分页，缺失内容报错。相邻事件不等于相邻用户消息；旧LoadText/模型工具未切换，历史提示尚无结构化外存引用，不宣称旧路径已加固。新增HistorySourceNeighbors/SpillOwnedPages两项，go vet三标签通过，WSL定向4/全量203待验。文档test/history-source.md；下一步结构化引用、来源发现和模型工具接入，CM-1未完成。未本地测试/真实模型、未提交/push。

2026-09-23 CM-1a验收与README重构：用户WSL HistorySource定向2/knowledgeintegration全量201通过、0跳过，覆盖此前CM-1a待验。按用户要求重构仓库首页为CLI Harness定位、最小启动、能力表、上下文路线及明确边界；删除首页历史交接堆叠，核对仓库内相对链接和差异格式。仅文档更新，未重跑测试、未提交/push；CM-1相邻消息/外存/工具接入仍待后续。

2026-09-23 CM-1a存储基础（未提交）：新增SourceAt/ReadSource，来源绑定owner+随机日志代次+原始seq，Unicode正文分页上限2000；单条/批量写入口用Lua管理代次并追加，读取原子核对代次，重建或代次丢失拒绝旧ID。兼容旧日志仅补元数据不改原文；保留type/role/interrupted。新增HistorySource两项回归，go vet三标签通过，WSL定向2/全量201待验。契约test/history-source.md：仅主user日志，尚无项目空间/相邻消息/外存展开/模型工具，不改历史选择；CM-1未完成。未本地运行测试/真实模型、未提交/push。

2026-09-23 工具证据最新验收：用户WSL ContextBaseline定向4/knowledgeintegration全量199通过、0跳过。reading-dev补采run-20260923T084722Z-66816.json三轮PASS，工具证据确认1–27截断且有后续，45–55完整但有后续，90–100完整且无后续；回答82/77/68字符，助手复核本题通过。实报输入13894/输出349、5次主调用均有usage；未改模型策略，不把与旧报告差异当节省或质量修复。评审见test/contextbaseline/review-20260923-reading-evidence.md，原失败和quality=unreviewed保留。停止重复补采，下一工程切片CM-1稳定来源/分页回读；费用缓存及旧evidence问题仍未完成。仅更新文档，未本地测试/调用模型、未提交/push。

2026-09-23 CM-0工具证据补齐（未提交）：采集器逐轮保存主循环工具返回/失败事件，复用ToolObservation名称/参数哈希/耗时/实际行范围及分页截断标志，复制快照，不保存原始参数或结果正文；缺观察值保留null。新增ContextBaselineToolEvidence真Harness/文件工具回归，覆盖中部has_more与末尾无后续、跨轮隔离及正文不泄漏。生产循环不变，无额外模型调用；go vet三标签通过，WSL ContextBaseline预计4/全量199待验。回归通过后仅重采reading-dev核对分页证据；不覆盖旧报告或宣称修正了模型质量，CM-1尚未开始。未本地跑测试/真实模型、未提交/push。

2026-09-23 四开发场景基线交接：用户evidence-dev/reading-dev各三轮采集PASS，但助手复核并非质量全过。evidence末轮去Markdown后105字符超100，step定义未满足项目预期且题面缺专有定义；reading验收码/行号正确，但45–55无后续页说法混淆文件分页，实际调用范围因报告缺工具元数据无法核实。四题合计实报输入37954/输出1952、17次主调用均有usage；费用缓存未知。详情test/contextbaseline/review-20260923-evidence-reading.md。下一切片先复用工具进度补采集证据，再推进CM-1；不重跑已判定前两题或留出集。CM-0未整体完成，本轮仅评审和文档，未改生产代码/跑模型/测试/提交。

2026-09-23 恢复场景基线：用户WSL resume-dev三轮PASS，报告run-20260923T083944Z-65615.json。助手按expect复核通过：重建Harness后保留目标/约束/下一步，第二轮79字符；项目编号正确且未编造上线日期。实报输入7579/输出458，4次主调用均有usage；第三轮含工具反馈的两个逻辑步骤，累计输入4021。报告未存工具名/正文，不凭模型自述认定检索范围。仅同进程重建，非真实时间间隔/进程重启/零缓存验证；费用缓存仍未知。评审见test/contextbaseline/review-20260923-resume.md。下一步evidence-dev/reading-dev；CM-0未整体完成，未改生产代码、未本地跑测试或模型、未提交/push。

2026-09-23 最新验收与首份完整基线：用户WSL ApprovalClaim定向5/全量198通过、0跳过，覆盖过期测试前提修正。continuity-dev真实3轮采集PASS，报告run-20260923T083801Z-65467.json；助手逐项复核既定expect通过（末轮51字符），非独立人工复核，原quality=unreviewed保留。实报输入5463/输出388，主调用3次均有usage，无子/摘要调用；费用缓存未知。每轮系统+工具估算1533，短对话固定开销占主导，不证明已节省。详见test/contextbaseline/review-20260923-continuity.md；下一步resume-dev，再补来源及长工具场景。CM-0仍未整体完成。本轮仅读报告/更新文档，未本地运行测试或模型、未提交/push。

2026-09-23 审批过期测试前提纠错（未提交）：用户ContextBaseline定向3通过、全量197通过/1失败/0跳过；唯一失败仍为ApprovalClaimChecksExpiryAtConsumption。新增诊断证明deadline=16:34:44.622，应用=44.553、Redis=44.553，断言时两端均未到期；此前两端时差假设不能解释本次失败。固定Sleep依赖单调时钟，不能证明JSON持久化期限对应的墙钟已越界（具体时钟调整原因未确认）。现原用例轮询应用和Redis墙钟，均超过期限10ms再断言，单调时间5秒上限，未满足前提单独报错；保留真实读取快照及无执行记录/未消费断言，不改生产代码、不新增顶层用例。go vet三标签及差异检查通过，WSL ApprovalClaim定向5/全量198待重验；基线身份修复的定向回归已验，真实采集未重跑。未本地运行测试、未提交/push。

2026-09-23 真实基线入口纠错（未提交）：用户continuity-dev首次采集失败，部分报告run-20260923T082823Z-63557.json仅1轮，轮耗时6ms、SDK尝试0。采集入口遗漏CLI所需user_id上下文，Runtime在模型调用前失败；旧回归自带身份掩盖该遗漏。现采集Run显式接收owner并注入身份，Harness回归改从空白上下文启动，报告新增安全error_code且不存原始错误。go vet含knowledgeintegration/rageval/contexteval及差异检查通过；修复后WSL定向3/全量198和真实三轮采集待重验，此前198通过不覆盖本修复。失败报告保留，不计作质量/费用基线；未运行本地测试或真实模型、未提交/push。

2026-09-23 最新验收：用户WSL ApprovalClaim定向5项、带knowledgeintegration全量198项通过，均0跳过，覆盖审批过期修复及CM-0c采集器的常规回归；此前ContextBaseline定向3项通过。真实模型采集尚无报告，不能据此声称质量/费用基线完成。仅记录用户证据，未重跑测试、未提交/push。下一步用户在WSL显式采集continuity-dev三轮，助手再读取报告分析。

2026-09-23 审批过期纠错（修复待WSL）：用户ContextBaseline定向3项通过、0跳过；全量日志197通过/1失败/0跳过，失败为TestApprovalClaimChecksExpiryAtConsumption（expiry claim: <nil>），不能标198全绿。发现应用时间生成/读取期限、Redis时间消费的口径差异，现Lua在原快照校验后同时检查Redis时间与调用时应用时间，任一到期拒绝。时钟偏差符合故障但用户日志未测量实际差值，不声称已复现。原用例加强无执行记录/未消费及双时钟诊断，无新增顶层用例。go vet含knowledgeintegration/rageval/contexteval与固定CLI构建通过，用户WSL重跑待验；未运行本地测试、未提交/push。真实基线尚未提供。

2026-09-23 CM-0c基线采集（运行待验）：固定4开发/2留出场景，test/contextbaseline复用Harness采集逐轮回答、输入组成、主/子/摘要用量与时间；scripts/context-baseline.sh显式--real一次一题，独立contexteval标签不进日常回归，随机隔离owner及合成文件工具根。新增ContextBaseline三项常规回归，预计定向3/全量198待WSL；真实模型尚未执行，质量固定unreviewed、缓存/费用null。说明见 [基线流程](contextbaseline/README.md)。生产Harness未修改，未提交/push。

2026-09-23 CM-0b最新验收：用户提供WSL ModelAccounting|ContextObservation|CLIToolProgress|RuntimeHarnessAssembly定向8项、带knowledgeintegration全量195项通过，均0跳过，覆盖此前CM-0b待验记录。调用计量回归已验；真实基线、缓存字段存在性、费率及完整费用仍未完成，未证明实际节省效果。未提供-race结果，不宣称已通过竞争检测。仅记录用户日志，未重跑测试、未提交/push。

2026-09-23 CM-0b调用计量（运行待验）：新增ModelAccounting三项及CLI既有用例断言，守住主/子/摘要/重试归属、流累计usage去重、断流保留、并发和跨轮隔离。go vet含knowledgeintegration/rageval与固定CLI构建通过；用户WSL定向预计8/全量195，命令和限制见 [输入观测](context-observation.md)。未运行本地测试/程序或真实模型，未提交/push；192通过仅为前批证据。

2026-09-23 CM-0a回归已验：用户提供WSL定向7项、带knowledgeintegration全量192项通过，均0跳过。覆盖新增3项输入观测回归及加强的CLIToolProgress，详见 [用例表与统计边界](context-observation.md)。仅记录用户日志，未重跑、未提交/push；真实模型基线、完整费用和节省效果尚未验证。

2026-09-22 累计回归最新验收：用户提供WSL `bash scripts/test-fast.sh -tags knowledgeintegration` 结果，189个顶层用例通过、0跳过，覆盖此前工作区读取/搜索上下文、工具诊断分类、模型输入证据、步数默认20及显式/paste改动的回归待验记录。真实模型对话可见8次调用及定点补读，但引用完整性和300字约束仍有不足，不将模型自述当执行证据。自动多行粘贴与输入编辑优化按用户要求后置，真实终端/paste及运行中取消仍待交互验收。本轮仅记录用户测试证据，未重跑测试、未提交或push。

2026-09-22 CLI多行粘贴（未提交）：逐行Scanner会把多段粘贴排成多轮，新增显式/paste收集、独立行/send提交、/cancel丢弃；保留空行，粘贴正文不走CLI/审批命令分派，EOF/Ctrl+C退出不提交未完成正文，64KiB累计上限。普通模式仍逐行提交，未实现自动粘贴识别。新增TestCLIPaste覆盖多段CRLF、命令正文、取消、空提交、EOF及超限；go vet含tag与CLI构建通过，用户WSL定向及全量待验，未运行本地测试。

2026-09-22 搜索上下文与限制分类（未提交）：search_files默认前后2行、context_lines可选0..5，重叠窗口去重并标记match，50行上限含上下文。结果新增has_more/content_truncated/scan_limited，旧truncated为三者或；CLI改为more/clipped/limited分项统计。新增WorkspaceSearchContext和WorkspaceResultLimitsDistinct，调整真实Harness汇总断言；go vet含tag与CLI构建通过，WSL Workspace预计7/累计全量188待验。未改外存阈值、未增加符号工具，待同题实际对照调用数与回答质量。

2026-09-22 CLI工具诊断汇总（未提交）：Progress增加实际工具耗时/规范化参数哈希和已知文件工具结果元数据，CLI每轮汇总次数、耗时、截断、重复，最多显示最近3次read_file实际行范围，不恢复逐调用刷屏，不打印参数正文。截断含分页，重复非自动错误判定；未知结果不猜测。加强ConfiguredLoopsKeepDependenciesSeparate和WorkspaceHarnessRead（真实SDK输入证据及CLI统计）既有用例，无新增顶层测试；go vet含tag及CLI构建通过，WSL定向2/全量186待重验。Agent最新“全是定位符”自述仍非事实证据，本轮不改外存、不新增outline。

2026-09-22 验收与步数调整（未提交）：用户WSL模型输入证据定向4项、knowledgeintegration全量186项通过、0跳过，覆盖此前工作区工具/CLI收起/模型证据待验。按用户要求默认maxSteps从10改为20，仍可由AGENT_MAX_STEPS正整数覆盖，未改用户.env，不改变触顶报错语义。每步为一次模型调用，可含多个工具；本次常量调整后go vet含tag及CLI构建通过，运行未重验，不能把此前186当调整后证据。

2026-09-22 模型输入证据回归（未提交）：针对Agent外存/推理反馈新增TestStorageSpillThresholdPerResult、TestModelEvidenceLargeResultAcrossTurns、TestModelEvidenceReasoningRoles及TestWorkspaceEmptySearchFields。假模型测试快照保留role/content/reasoning_content（不经摘要JSON接口暴露），核对单条2000rune阈值、本轮正文/跨轮定位符/再次load、同轮assistant推理字段与user/历史隔离。工作区结果显式lines=[]、skipped=0；未调整外存策略或步数。go vet含tag与CLI构建通过，WSL定向4/累计全量186待验；未声称已证实或复现真实历史对话异常。详见test/model-evidence.md。

用例表见 [模型输入证据验收](model-evidence.md)。

2026-09-22 CLI工具状态收起（未提交）：按用户要求将模型/工具状态改为终端单行原地更新，回答/推理/结束前清除，每轮只保留工具调用次数；非TTY仅汇总，不刷状态明细。工具事件持久化与错误传播不变，修正NO_COLOR下等待行清理。调整TestCLIToolProgress断言，go vet含tag及CLI构建通过，WSL CLI8项及累计全量182待验。用户真实日志证明工作区工具能调用，但本次代码解释触及10步上限未完成；不因此标记任务成功，本批不调整步数预算。

2026-09-22 CLI只读工作区（未提交）：新增internal/workspace及CLI -workspace（默认当前目录），通过Runtime.ExtraTools装配list_files/read_file/search_files，不改Loop或API/Web文件权限。os.Root限根、拒符号链接/父目录/绝对路径、排除隐藏与常见敏感/生成路径，普通UTF-8文件1MiB上限，行号/分页/截断及扫描预算明确。并发恶意文件替换、挂载/硬链接及源码硬编码秘密不保证，范围见docs/cli.md。新增test/workspace三项与e2e一项，go vet含tag及CLI构建通过；WSL Workspace预计4/带knowledgeintegration全量182待验，真实模型读代码待验。当前git main与本地origin/main一致（只读status，未fetch/push），本轮不提交。

用例表与运行命令见 [只读工作区工具](workspace/README.md)。

2026-09-22 提交前最终验收：用户 WSL `bash scripts/test-fast.sh -tags knowledgeintegration` 全量178项通过、0跳过，覆盖本批CLI、OpenCode、工具进度、推理字段及单行窗口相关测试，覆盖下方旧待验状态。本地最终go vet含knowledgeintegration/rageval通过，固定CLI构建通过；真实窄终端/缩放/复制粘贴、运行中取消仍不在本次自动回归证据内。完整交接见 [CLI 文档](../docs/cli.md)。

2026-09-22 推理单行窗口（未提交）：用户确认真实推理字段可见，要求缩短显示。CLI仅保留最近120 rune，按实时终端宽度保守双列预算截尾，原地重绘，回答/工具状态/结束时清除；过滤换行及控制字符，NO_COLOR仍可单行更新，非TTY或TERM=dumb只输出一次收到推理的提示、不输出全文。使用已在go.sum和本地缓存的x/term v0.41.0读取宽度。保留终端原生输入、选中复制和粘贴，未接管剪贴板/鼠标，仍不支持将多行粘贴作为单条编辑。调整TestCLIReasoning纯文本断言；静态检查及构建通过，WSL CLI8项与实际窄窗口/复制粘贴待验，未运行本地测试。

2026-09-22 CLI推理字段显示（未提交）：本地锁定依赖Eino v0.8.11 schema及OpenAI ACL v0.1.17确认reasoning_content映射为ReasoningContent，Loop原样转发；Harness新增独立回调，CLI默认显示Reasoning (provider)，/reasoning on|off仅控制显示，无字段无占位，不强制开启模型推理，不混入最终回答持久化。新增TestCLIReasoning三子场景（显示/隐藏/无字段）经假SSE→真实SDK→Harness→CLI验证并检查历史隔离；go vet含tag和CLI构建通过，WSL CLI预计8、合并ConfiguredLoop预计11、带knowledgeintegration全量预计178待验。未请求真实模型，当前服务是否返回字段待验；用户此前日志已确认工具进度真实可见，工具进度回归运行结果未提供。

2026-09-22 CLI工具进度（未提交）：ownLoop通过每轮context观察器报告模型请求、工具开始/返回/报错，CLI串行化状态与文本写入；不显示参数/结果正文，returned不等于业务成功或落盘成功，未知工具标记failed/unavailable。子循环不混入主面板；审批直接执行/摘要未接独立状态，工具结果写失败仍由原错误路径报告。新增TestCLIToolProgress（假模型+真Redis）并加强ConfiguredLoopsKeepDependenciesSeparate、ConfiguredLoopStepWriteBarrier断言。go vet含tag、CLI构建通过；用户WSL CLI预计7、合并ConfiguredLoop预计10、带knowledgeintegration全量预计177待验，真实显示待验。

2026-09-22 CLI显示验收补记（用户WSL日志）：CLI定向6项通过、0跳过；run-agent.sh重建后启动元信息、You/Agent分区和Done耗时已实际显示，真实模型正常回复，语气较旧人设自然。文本记录不证明颜色、窄窗口布局或运行中取消已验；带knowledgeintegration全量预计176及OpenCode定向3结果仍待提供。本轮只记录验收，不扩工具、不提交。

2026-09-22 CLI显示优化（未提交）：新增display.go，启动元信息、You/Agent分区、等待提示、耗时和状态颜色；复用已有go-isatty依赖，非终端/NO_COLOR/TERM=dumb纯文本降级，不接管全屏或改变Harness调用链。新增TestCLIBanner，已有对话测试补纯文本断言；go vet含tag和CLI构建通过，WSL CLI预计6/带knowledgeintegration全量预计176及真实显示待用户验收。保留单行输入、原样流式文本，未实现Markdown渲染、工具面板或输入编辑器。

2026-09-22 CLI人设纠正（未提交）：用户对话暴露旧“网关保安/极度冷酷/抱怨即防御”提示仍在生效。已将Harness系统提示改为协作助手，同步防御工具描述，只在明确处置请求时考虑提案；区分未知与保密、历史缺失与事实不存在，普通问答不强制查历史，短记忆不滥用外存。不注入或猜测真实模型名，不新增源码读取能力；审批机制不变。go vet含tag与CLI构建通过，真实语气效果待用户重建验证；既有历史保留。用户最新日志确认过程打印已消失、空闲Ctrl+C可退出。

2026-09-22 CLI输出清理（未提交）：删除记忆读写/检索、修复成功、压缩开始、重试过程、MCP连接和回复字数等过程打印，去掉沙箱/流中断的重复输出；错误提示、事件持久化及指标保留，不新增日志框架。go vet含knowledgeintegration/rageval及CLI构建通过；本批未跑运行测试。用户真实模型两轮已记住并答出CLI-7392，覆盖此前真实对话待验；重启历史与Ctrl+C仍待验，OpenCode新增3项WSL结果尚未收到。

OpenCode修复（2026-09-22，WSL待验）：`test/assembly/opencode_test.go`新增3项，截获真实SDK生成的HTTP请求，不访问外网或使用真实Key。`TestOpenCodeSessionHeaders`覆盖流式多步/多轮/子运行/摘要的稳定头和不同身份隔离；`TestOpenCodeRequiresSessionIdentity`覆盖缺身份时明确失败；`TestOpenCodeHeadersDoNotAffectOtherProviders`覆盖火山及相似域名不注入专用头。go vet带tag及CLI构建通过；定向OpenCode预计3、带knowledgeintegration全量预计175待用户WSL。Web共用工厂时附加已有conversation身份，不改变其存储或恢复边界。

CLI最新验收（2026-09-22，用户WSL日志）：CLI定向5项、带knowledgeintegration全量172项通过，均0跳过；run-agent.sh重建启动至user:1输入提示符，local/none隔离状态已显示。覆盖下方CLI待验记录；真实模型对话、终端Ctrl+C与重启历史尚未验证，未提交。

CLI增量（2026-09-22，WSL待验）：新增 `TestCLIConversationAndApproval`、`TestCLIErrorAndCommands`、`TestCLICancelWaitsBeforeNextTurn`、`TestCLIInputAndOutputFailures`，以及真Harness/Redis链路 `TestCLIHarnessHistory`。定向CLI预计5项，带knowledgeintegration全量预计172；静态检查与编译通过，不代表运行验收。用例表与实际终端验收见 [cli/README.md](cli/README.md)。CLI为当前主入口，Web保留但暂停扩展。

最新验收补记（2026-09-21，依据用户 WSL 日志与工作区交接）：RAGEvaluation 定向2、带 knowledgeintegration 全量167项通过，0跳过；独立开发集评测执行通过。覆盖下方累计待验记录，本次提交整理未重跑运行测试。真实模型和页面端到端仍不在该证据范围内；后续暂停检索扩展，回到 Harness 学习与建设。

K-0评测增量（2026-09-21，WSL待验）：`TestRAGEvaluationQueriesAndRanking`验证去重查询、稳定排序；`TestRAGEvaluationScoringAndHoldout`验证多来源部分召回、无答案候选单独计数及排除holdout。普通带knowledgeintegration全量预计167。实际语料评测`TestRAGDevelopmentEvaluation`仅在额外rageval标签下启用，由 `bash scripts/eval-rag.sh`运行，独立1项，不混入167；执行PASS不表示质量达标，见 [rag/README.md](rag/README.md)。

最新验收（2026-09-21）：用户WSL `KnowledgeChunkSearch|KnowledgeSearch` 定向5、带knowledgeintegration全量165项通过，均0跳过。覆盖下方分段检索待验记录；这些是机制回归，不是K-0检索质量评分或性能测试。未提交。

分段检索增量（2026-09-21，WSL待验）：新增 `TestKnowledgeChunkSearchBoundaryAndFallback`（跨窗口长词、缺失/过期规则回退与重建切换）、`TestKnowledgeChunkSearchMixedPagination`（重叠去重、片段/原文混合分页）；原SearchAgentEvidence要求实际片段命中。定向 `KnowledgeChunkSearch|KnowledgeSearch` 预计5、带tag全量165，静态检查通过。用户上一批KnowledgeChunks定向4/全量163已通过，0跳过。

分段索引增量（2026-09-21，WSL待验）：新增 `TestKnowledgeChunksOriginalCoordinates`（原文无损与Unicode位置/重叠）、`TestKnowledgeChunksImportAndRebuild`（自动索引与并发重建）、`TestKnowledgeChunksFailureAtomicity`（片段/标记写失败回滚，保留旧索引）、`TestKnowledgeChunksHTTPAndExistingData`（旧资料missing、重建、分页与接口归属）。后3项需knowledgeintegration，定向KnowledgeChunks预计4，全量163；go vet通过。迁移重复检查更新至版本1/2/3/4。用户此前KnowledgeSearch定向3/全量159已通过、0跳过。

关键词基线增量（2026-09-21，WSL待验）：新增 `TestKnowledgeSearchIsolationAndExactMatch`（Unicode位置、字面符号、大小写、库/用户边界）、`TestKnowledgeSearchPaginationAndVersions`（分页及旧版本排除）、`TestKnowledgeSearchAgentEvidence`（搜索候选→读取→回答→可核对快照）。需knowledgeintegration，定向KnowledgeSearch预计3、全量159；go vet通过，未跑题集质量评分。契约见 [search.md](knowledge/search.md)。

最新验收（2026-09-21）：用户 WSL `scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeEvidence` 定向3、带 tag全量156项通过，均0跳过。覆盖下方工具来源展示待验状态；不等同于真实模型和页面端到端验收。未提交。

工具来源展示增量（2026-09-21，WSL待验）：`TestKnowledgeEvidenceSnapshot`（模型自报引用不参与、版本更新后仍打开旧片段、历史只带元数据）、`TestKnowledgeEvidenceHTTPIsolation`（用户/库/会话/轮次/事件逐层校验）、`TestKnowledgeEvidenceRejectsUnpairedResults`（孤立、失败、参数不匹配结果不生成来源）。需knowledgeintegration，定向KnowledgeEvidence预计3，全量156，静态检查通过。浏览器验证范围见 [evidence.md](knowledge/evidence.md)。

最新验收（2026-09-21）：用户 WSL `scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeReadingTools` 定向3、带 tag全量153项通过，均0跳过，覆盖下方资料只读工具待验记录。假模型集成回归不等于真实模型工具选择或页面端到端验收，未提交。

资料只读工具增量（2026-09-21，WSL待验）：`TestKnowledgeReadingToolsScopeAndVersions` 覆盖同用户跨库/跨用户/版本串读拒绝、Unicode分页、旧版本与每轮预算；`TestKnowledgeReadingToolsPagination` 覆盖列表游标；`TestKnowledgeReadingToolsHTTPAndWriteBarrier` 覆盖列表→原文→回答及工具日志写失败停止。均需 knowledgeintegration，定向 KnowledgeReadingTools 预计3、全量153，go vet通过。详见 [reading-tools.md](knowledge/reading-tools.md)。

最新验收（2026-09-21）：用户 WSL `scripts/test-fast.sh -tags knowledgeintegration -run 'KnowledgeExecutionEvents|ConfiguredLoopStepWriteBarrier'` 定向3、带 tag全量150项通过，均0跳过，覆盖下方执行日志增量待验记录。不代表真实模型/浏览器端到端已验收，未提交。

会话执行日志增量（2026-09-21，WSL 待验）：新增 `TestKnowledgeExecutionEventsHTTP`（成功 step 对、start/end 写失败无 done）、`TestKnowledgeExecutionEventsFencing`（不同会话轮次隔离、结束/超期拒写），均需 knowledgeintegration；新增 `TestConfiguredLoopStepWriteBarrier`（start 失败不派工具、handoff 失败不继续调模型）。重复迁移现断言版本1/2/3。静态检查通过，定向 `KnowledgeExecutionEvents|ConfiguredLoopStepWriteBarrier` 预计3、带 tag全量150；下方147不覆盖本批。

最新验收（2026-09-21）：用户 WSL `scripts/test-fast.sh -tags knowledgeintegration -run 'KnowledgeConversation|KnowledgeChat'` 定向 8、带 tag 全量 147 个顶层用例通过，均 0 跳过。覆盖下方持久会话迁移纠错与回归待验状态；不等同于真实模型和真实浏览器端到端验收。未提交。

持久会话回归纠错：用户运行定向6项/全量8项因迁移版本2主键重复而失败，均发生在数据库初始化。已隔离 GORM 固定连接内的语句状态；knowledge/database 测试辅助仍连续迁移两次，并逐次核对版本集合恰为1/2。顶层用例数量不变，8/147待重验，不能标通过。

持久会话增量（2026-09-21，待 WSL）：新增 5 项 KnowledgeConversation 测试，旧 Chat HTTP 测试切换会话协议，覆盖存储恢复/归属、版本快照、并发去重/过期页面、超期 writer、写入屏障与取消。详见 [knowledge/conversations.md](knowledge/conversations.md)。定向 KnowledgeConversation|KnowledgeChat 预计 8，带 tag 全量 147；前一轮 142 不覆盖本次代码。

最新验收（2026-09-21）：用户 WSL `scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeChat` 定向 3、带 tag 全量 142 个顶层用例通过，0 跳过，覆盖下方 Chat 待验记录。该证据不包含真实模型和浏览器上传/取消端到端验证。

2026-09-21：用户 K-1 首批 Knowledge 定向 5、带 knowledgeintegration 全量 139 通过，0 跳过。新增 Chat 三项（下表）静态检查通过、WSL 待验；带 tag 全量预计 142。旧待验记录由本条覆盖，但不将旧结果用于新增代码。

| 用例 | 守护的不变量 |
|---|---|
| TestKnowledgeChatHistoryContract | 限制角色、轮次和长度，拒绝客户端 system 注入 |
| TestKnowledgeChatAttachmentContract | 原文和标题进入本轮上下文，缺少附件及超限拒绝 |
| TestKnowledgeChatHTTPIsolationAndStream | MySQL 归属过滤、模型收到附件、流式结束、错误脱敏及释放名额、无旧历史混入 |

K-1 第一批：新增 knowledge/ 三项无数据库测试与两项显式 `knowledgeintegration` MySQL 集成测试，详见 [knowledge/README.md](knowledge/README.md)。静态检查通过，WSL 未运行；`scripts/test-fast.sh -tags knowledgeintegration -run Knowledge` 预计 5 项，全量带 tag 预计 139 项。普通快速回归不包含两个 MySQL 用例，不据此宣称数据库通过。

### K-2 向量索引与召回（2026-10 切片2，待 WSL）

| 用例 | 守护的不变量 |
|---|---|
| TestEmbedderFromEnv | 全空=明确未启用；半配置=报错，不静默退回关键词 |
| TestRemoteEmbedderBatchesAndOrders | 按批次（≤10 条）调用、按响应 index 归位、从响应学习维度并生成稳定指纹 |
| TestRemoteEmbedderRejectsBadResponse | 条数不符 / 非 200 明确报错，不静默接受坏数据 |
| TestKnowledgeVectorIndexAndSearch（knowledgeintegration） | 幂等重建、原文可召回、跨用户拒绝、规则不匹配降级为 indexed=false、换规则后旧向量不可达、空库 indexed=false |

`TestKnowledgeVectorIndexAndSearch` 用确定性假 embedder（不联网），只验证机制，不代表语义质量；真实 embedding 质量由 `rageval` 三路对照报告评估。本轮 schema 迁移到 v5（`knowledge_chunk_embeddings` / `knowledge_embedding_indexes`），契约见 [knowledge/vector.md](knowledge/vector.md)，选型见 [rag/vector-retrieval-design.md](rag/vector-retrieval-design.md)。

### K-2 混合召回（2026-10 切片3，待 WSL）

| 用例 | 守护的不变量 |
|---|---|
| TestKnowledgeQueryTerms | 关键词侧抽词：标识符 + 汉字双字词、去重、上限 32，与评测同口径 |
| TestKnowledgeHybridSearchFusion（knowledgeintegration） | RRF 融合两路、新进程不假降级、跨用户拒绝、无向量索引时 VectorIndexed=false 并退化为仅关键词 |
| TestKnowledgeHybridToolScope（knowledgeintegration） | 不传 embedder 时仍 3 个工具（search_sources 契约不变）；传 embedder 追加第 4 个 `search_sources_hybrid`；工具仍按 owner/library 授权 |
| TestKnowledgeHybridKeywordMultiPage（knowledgeintegration） | 关键词侧候选超过单页 10 份时翻页收齐（12 份全命中），不静默截断第一页；nil embedder 时 VectorIndexed=false |

混合检索停在 source 粒度、保留两侧坐标供 `read_source` 定位；工具独立、不改 `search_sources`。契约见 [knowledge/hybrid.md](knowledge/hybrid.md)。

### K-3 三路对照评测（2026-10 切片4，待 WSL + 真实 embedding）

| 用例 | 守护的不变量 |
|---|---|
| TestRAGRetrievalComparison（knowledgeintegration,rageval） | 同一题集/语料/分段规则下产出 关键词 vs 向量 vs 混合 的 source-level recall@5 对照；报告记录语料/实现文件 SHA256、embedding 模型与规则、语料与查询用量；holdout 仅最终跑一次 |

该测试**真实调用远端 embedding API**（WSL 里需 `export EMBEDDING_*`），报告写入 `test/rag/results/`（不入库，不覆盖旧报告）。脚本 `scripts/eval-rag-compare.sh`，说明见 [rag/README.md](rag/README.md)。`eval-rag.sh` 的关键词基线仍不调用模型。

最新验收（2026-09-21）：用户在 Redis PONG 后运行 JWT|LegacyWS|F3Boundary 定向 8、全量 134 项，均通过、0 跳过。覆盖下方 JWT 待验与此前 Redis 不可用时的跳过结果；不代表真实 API 启动或生产部署已验证。未提交。

2026-09-21 JWT 修复：新增 `assembly/jwt_test.go` 三个顶层用例，覆盖无效配置、旧公开密钥伪造、算法/有效期/用户 ID、密钥更换与 HTTP/WS/RPC 拒绝；旧 WS 用例改用显式测试密钥。`go vet ./...` 通过，用户 WSL 待验：`scripts/test-fast.sh -run 'JWT|LegacyWS|F3Boundary'` 和全量，预计 8/134，以实际为准。旧公开密钥只在负向测试中保留，不作为配置默认值。

F-4 状态更正：用户已提供定向 2、全量 131 项通过，0 跳过，下方待验为历史记录；该结果不覆盖本次 JWT 改动。K-0 RAG 样本见 `rag/README.md`，仅为待运行的评测设计，不计入 Go 测试数量。

F-4 固定场景验收已实现，待用户 WSL 执行 `scripts/test-fast.sh -run Framework` 与全量。说明见 [framework.md](framework.md)。本批只增加测试与文档，静态检查通过，尚不能标记 F-4 验收完成。

| 用例 | 守护的不变量 |
|---|---|
| `TestFrameworkAcceptance` | 同一发布任务覆盖成功/拒绝/失败/取消，检查执行次数、审批状态、名额释放、新 Harness 读取历史且不重放副作用 |
| `TestFrameworkReplacement` | 两个独立模型 HTTP 替身与两个工具通过统一配置接入，原模型零请求，不修改核心循环 |

F-3 收口验收通过（2026-09-18）：用户 WSL F3Boundary 定向 3、全量 129 个顶层用例通过，0 跳过；提交前 go vet 通过。F-2/F-3 在约定框架范围内完成，下方各批待验描述保留为历史记录，以本条最新验收为准；真实任务统一验收归 F-4。

| 用例 | 守护的不变量 |
|---|---|
| `TestF3BoundaryCrossEntrypoints` | 旧 WS 与 RPC 共享同一 Harness：跨入口查到同一运行、并发被拒绝、取消传播且退出后释放 |
| `TestF3BoundaryNestedChildrenAndApproval` | 孙任务关联直接父运行及委派调用，各层日志合法；子防御动作明确拒绝挂起，不能占父审批槽 |
| `TestF3BoundaryCanceledChildrenDoNotStart` | 预取消队列不构造模型，构造期间取消不继续建流，空子实例明确返回错误 |

F-3 第五批新增 `e2e/child_trace_test.go` 三项，静态检查通过，待 WSL `scripts/test-fast.sh -run ChildTrace` 与全量回归。上一批 RunTrace 定向 3、全量 123 已由用户验证，0 跳过。

| 用例 | 守护的不变量 |
|---|---|
| `TestChildTraceParallelIsolation` | 两个并行子任务有不同运行 ID、正确父运行/委派调用，各自保存输入/step/工具配对/结束，父日志不混写，跨用户查不到子日志 |
| `TestChildTracePersistenceFailures` | 子开始日志失败零构造，结束写失败不能报告成功，构造失败有中断终态，子记录不落父历史 |
| `TestChildTraceCancellation` | 父取消传播到子工厂，独立收尾时限记录 interrupted/aborted，而非 completed |

Redis 子日志键为 `agent:child:history:<uid>:<runID>`，父子索引为 `agent:child:index:<uid>:<parentRunID>`；测试按专用账号清理这些键。可用 session.RedisStore.ChildRunIDs/GetChildLog 读取，不改变父会话投影。不代表任意第三方忽略 context 时也能强制停止。

F-3 第四批 `e2e/run_trace_test.go` 已由用户 WSL 验证：RunTrace 定向 3、全量 123 个顶层用例通过，0 跳过。覆盖主运行与审批来源追溯，不代表子任务独立归属与日志已完成。

| 用例 | 守护的不变量 |
|---|---|
| `TestRunTraceApprovalOrigin` | 提案来源与原轮事件一致，审批执行/续答是同一新运行，审计及领取记录保留来源，工具调用配对不变 |
| `TestRunTraceFreshTurnsAndLegacyHistory` | 新轮次分配新运行 ID，兼容会话标识稳定，旧无归属日志不重写且投影可读 |
| `TestRunTraceRejectsWrongOwnerAndConflicts` | 跨用户或冲突归属拒绝写入，批次不部分落盘，错误提案不保存，不修改调用者 DTO |

最新验收：用户 LegacyWS 定向 2、全量 120 个顶层用例通过，0 跳过。F-3 第三批 `e2e/legacy_ws_test.go` 两项真实旧聊天 WS 测试已验证，接真 Redis、Harness 与受控循环（不调用模型）；不代表真实模型或高并发压力验收。

| 用例 | 守护的不变量 |
|---|---|
| `TestLegacyWSControlDuringRun` | 运行中仍处理心跳/查询/取消，重复聊天报忙，旧运行 ID 不能取消，取消后实际退出并释放 Harness 名额 |
| `TestLegacyWSReplacementCancelsOldRun` | 新连接顶掉旧连接会取消旧任务，旧入口清理不删除新在线记录，新连接断开后正常清理 |

旧 `/ws` 新控制消息：`{"type":"agent/status"}`；`{"type":"agent/cancel","run_id":"状态返回的ID"}`。控制响应为同 type 的 JSON，普通聊天/流式文本保持原格式，任务默认 60 秒时限不变。身份使用 JWT 用户，取消接受不等于任务已退出。尚未压力测试 Redis 推送与流式输出竞争；代码已统一经过 Client 写锁及写入期限。

最新验收：用户 RunControl 定向 2、全量 115 通过，0 跳过。F-3 第二批新增 `test/appserver/control_test.go` 三项，静态检查通过，待 WSL `scripts/test-fast.sh -run RPC` 与全量回归；协议与边界见 [appserver/README.md](appserver/README.md)。

| 用例 | 守护的不变量 |
|---|---|
| `TestRPCControlDuringRun` | 流式运行期间同连接可查询/取消，第二个耗时操作报忙，跨用户/旧 ID 不能取消，取消信号与实际退出分离，chunk 关联请求 |
| `TestRPCDisconnectCancelsOperation` | 普通运行和审批操作均在连接断开后收到 context 取消 |
| `TestRPCControlValidationAndCompatibility` | 旧 Runner 仍可运行，控制能力缺失明确报错，取消必须提供运行 ID |

最新验收：用户 MCP 定向 1、全量 113 通过，0 跳过，覆盖下方 MCP 失败及待验记录。F-3 本批新增以下两项，静态检查通过，待 WSL `scripts/test-fast.sh -run RunControl` 与全量回归。

| 用例（`assembly/run_control_test.go`） | 守护的不变量 |
|---|---|
| `TestRunControlAdmissionAndCancellation` | 普通/续跑/审批不能与当前同用户任务重叠，忙时不写历史或领取审批；取消校验用户和运行 ID，旧 ID 不影响新任务，取消后直到实际退出才释放 |
| `TestRunControlIndependentUsersAndExit` | 不同用户可并行，父 context 取消后退出释放，预取消请求零写入，空续跑不泄漏占用 |

2026-09-18 10:39 验收纠错：MCP 定向/全量均仅 success 子场景失败，另外三个子场景通过。测试错误地要求假模型的 240 字摘要包含完整长结果；现改为回复检查结果引用，事件精确检查完整结果，远端实收参数/执行次数检查保留。成功场景后续断言尚待运行，需重跑 MCPApprovalTransport 和全量。

F-2 最新证据：用户第三批 ApprovalExecution 定向 4、全量 112 用例通过，0 跳过。本轮新增 MCP 真实 stdio 传输验收，静态检查通过，待用户 WSL 执行 `scripts/test-fast.sh -run MCPApprovalTransport` 与全量回归。

| 用例（`e2e/mcp_approval_test.go`） | 守护的不变量 |
|---|---|
| `TestMCPApprovalTransport` | 真实子进程握手/发现/调用；批准前零调用、拒绝零调用、批准原参数仅执行一次；结果配对回填且模型收到工具结果；isError（含空错误文本）不能记成功，重复确认不重放 |

该用例复用测试二进制启动 SDK stdio 服务端，临时文件记录服务端实际调用，退出时关闭桥与清理文件；假模型、真 Redis、真 MCP 传输，无真实模型费用。不证明第三方 MCP 服务兼容性、真实模型工具选择或进程崩溃恢复；这些与协议链路验收分开记录。

第三批已验证用例：

| 用例（`e2e/approval_execution_test.go`） | 守护的不变量 |
|---|---|
| `TestApprovalExecutionClaimEvidence` | 领取后提案消失但原计划及状态保留，新 Harness 可查询；跨用户查不到，重复开始与状态回退被拒绝，重复确认不重放 |
| `TestApprovalExecutionClaimStorageFailure` | 记录键类型错误时领取失败，提案不丢失且零执行 |
| `TestApprovalExecutionWriteBarriers` | running 写入失败零执行，终态写失败保留 running 且不续答，两者均不重放 |
| `TestApprovalExecutionOutcomes` | 成功/拒绝/错误有独立状态，结果回填失败不抹掉执行成功证据，取消后仍保存未知结果，沙箱路径也接状态 |

第二批已验证用例：

| 用例 | 守护的不变量 |
|---|---|
| `e2e/TestToolApprovalFullCallAndContinuation` | 超过展示长度的参数完整保存/执行，结果按新调用 ID 配对进入模型，重复确认不执行，下次工具调用仍需审批 |
| `e2e/TestToolApprovalRejectAndFailure` | 拒绝零执行，执行错误明确回填，均能续答且日志配对合法 |
| `e2e/TestToolApprovalPersistenceBarriers` | 执行前写失败零执行，结果写失败不请求模型，重复确认不重放 |
| `e2e/TestToolApprovalRejectsDifferentRuntime` | 重建实例不能执行旧实例挂起的工具提案 |
| `e2e/TestToolApprovalProposalCannotOverwrite` | 新提案不能覆盖未处理提案，过期提案不阻塞新提案 |
| `assembly/TestToolApprovalGuardRetainsRestrictions` | 一次授权保留 Deny/入参限制/超时，成功与失败结果均脱敏 |

这些用例使用本地 MCP 命名工具、假模型和真 Redis，不代表真实 MCP 服务器已验收。本批通过停止流程模拟崩溃窗口，未实际杀进程/演练 Redis 故障切换；状态查询不自动恢复执行。

F-2 原子领取新增用例（`e2e/approval_claim_test.go`）：静态检查通过，WSL 运行待验。

| 用例 | 守护的不变量 |
|---|---|
| `TestApprovalClaimConcurrentConfirmation` | 16 个请求先读到同一提案再同时确认，只执行及审计一次 |
| `TestApprovalClaimRejectRacesWithApprove` | 批准/拒绝只接受一个，执行次数与获胜决策一致 |
| `TestApprovalClaimRejectsStaleSnapshot` | 旧快照不能消费新提案，修改本地对象不改变实际计划，重复领取失败 |
| `TestApprovalClaimChecksExpiryAtConsumption` | 读取时有效、领取时过期的提案不能执行 |
| `TestApprovalClaimFailureNeverExecutes` | 领取故障或存储缺少领取能力时零执行，提案仍保留 |

2026-09-18 用户验收：Runtime 定向 4、全量 97 顶层用例通过，0 跳过；test-real.sh 六段跑完，显示子任务响应、显式 spill 存取、防御两步审批执行（审计追加 68 字节、退出码 0）和 10 轮响应。第 5 段不能单凭定位符回复证明自动 spill，第 6 段不证明压缩/Repair；未提供重建启动日志，服务构建版本未独立核实。上述为用户终端证据，不扩大成所有机制的验收。

统一装配最新结果：用户重跑 `scripts/test-fast.sh -run Runtime`（4 个）与完整回归（97 个顶层用例）全部通过，0 跳过；覆盖下方断言修正后的待验状态。API 重建后的真模型验收仍待执行。

统一装配纠错：用户 17:58 定向/全量均在 TestRuntimeDefaultAssembly 的最终文本断言失败，其他三个 Runtime 用例通过。已按现有假模型 afterToolResult 行为补上“（据工具结果）”前缀，仍做精确比较；后面的父事件、请求数和工具清单检查在此次失败中尚未执行。修正后待 WSL 重验。

统一装配验证：上一批用户 WSL 存储定向 3、全量 93 个顶层用例通过，0 跳过。本轮新增以下 4 个 Runtime 用例，静态检查通过，运行待 WSL；可执行 `scripts/test-fast.sh -run Runtime` 后跑全量。

| 新增用例 | 守护的不变量 |
|---|---|
| `assembly/TestRuntimeHarnessAssemblyIsIsolated` | 两个完整 Harness 各跑两轮：主/子/摘要模型、归档、扩展工具、重试与事件归属独立；子重试不污染父日志；工具切片修改不影响实例 |
| `assembly/TestRuntimeRejectsInvalidAssembly` | 内置工具重名在构造时拒绝，工厂返回空模型/摘要模型时明确报错 |
| `assembly/TestRuntimeZeroRetryDoesNotRetry` | 显式零重试策略遇到 429 只请求一次，不被包装器默认值覆盖 |
| `e2e/TestRuntimeDefaultAssembly` | 新默认入口通过真 SDK/假模型/真 Redis 完成主→子→主，工具正确绑定、父事件不混入子 step；环境后改不改变已装配模型 |

最新验证：用户 WSL guard 定向 3 个、完整回归 90 个顶层用例通过，0 跳过。本轮存储装配测试静态检查通过，运行待 WSL。

| 新增用例（`assembly/storage_test.go`） | 守护的不变量 |
|---|---|
| `TestStorageToolsKeepStoresSeparate` | 两套归档/提案/外存读写使用所属 Store；guard Ask 写同一审批库且不执行工具；用户标识保留 |
| `TestStorageToolsAutomaticSpillUsesConfiguredStore` | 自动外存使用传入 Store，调用标识保留，外存失败不丢完整结果 |
| `TestStorageToolsRejectMissingStores` | 任一存储缺失在构造时拒绝 |

最新补充：用户 WSL 委派定向 2 个、完整回归 87 个顶层用例通过，0 跳过。本轮 guard 测试仅静态检查通过，待 WSL 运行。

| 新增用例（`assembly/guard_test.go`） | 守护的不变量 |
|---|---|
| `TestConfiguredGuardPoliciesAreIsolated` | 两个实例的审批/信任独立；原 map 修改不改变实例；无法审批时不执行 |
| `TestConfiguredGuardTimeoutsAreIsolated` | 工具收到所属实例的默认/覆盖截止时间，配置 map 修改不影响旧实例 |
| `TestConfiguredGuardRejectsInvalidConfiguration` | 非法超时、空工具名和空服务器名在构造时拒绝 |

最新验证：用户确认迁移后完整回归 85 个顶层用例通过、0 跳过，覆盖下方迁移后待验状态。本轮新增委派装配测试，静态检查通过，运行待 WSL；此前 85 用例不覆盖本轮新增内容。

| 新增用例（`assembly/delegation_test.go`） | 守护的不变量 |
|---|---|
| `TestDelegationToolsKeepFactoriesSeparate` | 两套委派工具在单任务与并行任务中使用各自的子循环工厂 |
| `TestDelegationToolsRejectMissingConfiguration` | 缺失工厂、非正并发上限明确拒绝；空 Runner 调用返回错误 |

## 验证状态的读法（2026-09-17）

目录约定：新增测试统一放本目录，按主题拆分；F-1 测试位于 `assembly/config_test.go` 和 `assembly/dependencies_test.go`，通过公开构造与运行入口验证。历史 `internal/` 包内测试暂保留，后续迁移不得丢覆盖。F-1 迁移前用户 WSL 定向 4 个、完整 85 个顶层用例通过、0 跳过；迁移后仅静态检查通过，待 WSL 重验。

F-1 第一批（待 WSL）：新增 `TestConfiguredLoopsKeepDependenciesSeparate`、`TestConfiguredLoopRejectsMissingDependencies`、`TestHarnessDependenciesAreInstanceScoped`、`TestHarnessDependenciesRejectMissing`。检查显式装配的模型/工具/事件写入、工具切片快照、缺少依赖时报错，以及两个 Harness 实例的循环/摘要/提示词工具清单和回复存储归属。静态检查通过，先前 81 用例记录不覆盖本轮。完整默认内置工具与子任务实例隔离尚未实现，不由这些用例作保证。

本文记录测试意图。最新验证：2026-09-17 15:24 用户在 WSL 对本轮未提交工作区执行压缩定向回归与 `scripts/test-fast.sh -v`，完整回归 72 个顶层用例通过、0 失败、0 跳过，e2e 包耗时 0.814s（假模型 + 真 Redis）。本地静态检查也通过；这不代表真模型摘要质量已经验证。

- 测试须由用户在 WSL 执行 `scripts/test-fast.sh -v`；Windows 本地按工作区约定只做 `go vet ./...`，不运行 `go test`。
- 验证记录包含日期、代码版本及未提交变更、环境、命令、实际通过/失败/跳过范围。必需 Redis 用例被跳过时只能记“部分验证”，不能称为全绿。
- 假模型检验运行机制；真模型任务集检验信息质量与实际任务完成。两者不能互相替代。
- 新增真模型验收：`sh scripts/test-real.sh compact`，先用 `MODEL_CONTEXT_WINDOW=4000 sh scripts/run-api.sh` 启动服务。独立账号、随机早期/近期编号、只读 Redis 证据和严格 JSON 答案断言；最多填充 12 轮，无压缩或事实缺失报失败。账号与日志保留，方便复盘。当前仅静态检查通过，待用户运行；上面的 72 用例记录不覆盖该新命令。
- 当前缺口见工作区 [`HARNESS-TODO.md`](../../HARNESS-TODO.md) 的 R 系列；以下计划尚未实现或执行。

最新补充（2026-09-17）：用户运行 `sh scripts/test-real.sh compact` 单样例 PASS，覆盖上文“待用户运行”状态。日志 `agent:V2:history:4`，第 3 轮填充后早期事实已被摘要覆盖；回答的 `ARCH-2319a387` 与 `RECENT-91acc868` 均匹配。仅证明当前服务的本次样例通过，不代表通用摘要质量；详见工作区 TODO。

| 计划验收 | 防止的错误 | 状态 |
|---|---|---|
| R-1 压缩后近期原文、工具配对与重建一致 | 只检查变小，漏掉未摘要尾部丢失 | `e2e/compaction_test.go` 已于 2026-09-17 在用户 WSL 回归通过 |
| R-2 写调用事件失败时工具零执行 | 把调用写入函数当成写入成功 | `TestToolPersistenceBarrier` 和 `TestToolPersistenceFailureStopsModelHandoff` 已于 2026-09-17 用户 WSL 定向运行通过；同期完整回归 74 个顶层用例通过、0 失败、0 跳过，e2e 包 0.848s。写入故障为注入模拟，真实 Redis 断连未演练 |
| R-3 中断跨层传播、取消收敛与预算上限 | 子任务半截结果伪装成功、满缓冲泄漏、超额调用 | `TestOwnLoop_TruncationIsError`、`TestOwnLoop_MaxStepsDoesNotRequestNextModel`、`subagent.TestChildStreamOutcome`、`TestInterruptedStreamIsMarked` 于 2026-09-17 15:51 用户 WSL 定向通过；同期完整回归 77 个顶层用例通过、0 失败、0 跳过，e2e 包 0.802s。取消收敛/重试预算仍缺测试 |
| R-4 并发批准与恢复再次中断 | 顺序去重冒充并发安全、未知结果重复副作用 | 待补并发和故障测试 |
| R-5 MCP 原调用批准执行 | 连接发现通过冒充执行闭环 | 待补端到端 |

> 目的：把"依赖真模型的验证"从 **10 分钟**降到 **秒级**（`HARNESS-TODO.md` 的 P0-1）。
> 一句话：**不 mock LLM 就没法回归；不敢回归就没法改；改不动就永远是玩具。**

取消收敛验证（2026-09-17，用户 WSL）：`TestOwnLoop_CancelSilentUpstream` 检查静默生产者遵守 context 时读取取消；`TestOwnLoop_CancelFullOutput` 检查不消费输出、缓冲已满时取消仍能停止生产者并返回取消错误。连同 `TestOwnLoop_ContextCancelStops`，定向 3 个用例通过；同期完整回归 79 个顶层用例通过、0 失败、0 跳过，无超时，e2e 包 0.893s。静态检查也通过。

重试预算验证（2026-09-17，用户 WSL）：`retry.TestRetryBudgetSharedAcrossCalls` 守护跨 Stream/Generate 的共享额度、恢复初始值和累计编号；`retry.TestRetryCancelDuringBackoff` 守护退避取消不追加请求，并保留上游错误与取消原因。定向 2 个及完整回归 81 个顶层用例通过，无失败或跳过，e2e 包 0.820s；静态检查也通过。

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

日常执行 `scripts/test-fast.sh`：成功只显示顶层用例通过/跳过数，跳过用例单独列出；失败保留完整输出和原退出码。需要排障时加 `-v` 查看完整日志。定向测试仍可用 `scripts/test-fast.sh -run RetryBudget`，无需默认加 `-v`。简洁输出改动尚待 WSL 实际运行。

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

**快速单元与装配层（不需要 Redis / 真实模型，默认参与回归）**

另有 `internal/harness/agent/loop_test.go` 的假模型循环测试（文本、工具回灌、建流错误、取消、断流），以及 `guard/pipeline_test.go` 的工具策略测试（外部 Ask、内置执行、无审批处理器拒绝）。这里只确认代码存在，当前执行结果待 WSL 验证。

| 用例 | 守住的不变量 |
|---|---|
| `TestProjectMessagesPairOrDrop` | 悬空的 `tool/result` 不进投影（pair-or-drop） |
| `TestContextShadowExplicitConstraintSelection` | 显式用户来源去重并保留连续后缀；无效位置不能伪装成已满足约束 |
| `TestContextShadowSelectsRetrievedWholeTurnBeforeRecentHistory` | 成功回读来源优先于普通近期轮；按完整轮加入，超预算省略而不裁半轮 |
| `TestContextShadowVariantSelectsOnlyValidWholeHistory` | 评测候选只保留完整旧轮、系统消息与当前轮，并保持原消息对象和源切片；质量复核标记不阻止本次隔离应用 |
| `TestContextShadowVariantFailsClosed` | 来源不匹配、必留约束/回读未解、缺当前轮、非法索引、计数漂移或切半轮时回退全量 |
| `TestShadowApplyingModelUsesCurrentCandidateAcrossRetries` | 适配器收到候选消息；建流重试复用同一候选，新逻辑请求使用新候选并记录估算 |
| `TestShadowModelWrapperIsInertWithoutEvaluationState` | 未显式启用评测应用态时模型不被包装 |
| `TestCompactionProjectionPreservesTailWithLegacyCheckpoint` | 摘要先于近期原文；旧 checkpoint 不能遮蔽未摘要尾部，全量与偏移投影一致 |
| `TestProjectMessagesHonoursBaseSeq` | 折叠后数组下标 ≠ seq，遮蔽区间必须加偏移 |
| `tokenmeter` 包的 6 个用例 | 中英文分档估算、工具参数也算进成本、预算自相矛盾会被修正、校准系数会移动且被夹住、裁剪不会把工具配对从中间切断 |
| `retry` 包的 6 个用例 | 退避指数增长并夹上限、抖动留在 ±jitter 内、只重试"再试可能好"的错误（429/5xx/连接/超时）、参数错与鉴权错坚决不重试、混合消息以"不重试"优先、状态码解析 |
| `sandbox` 包的 14 个用例 | **argv 化**：拒绝 `cmd`/`sh`/`powershell`（含 Windows 风格路径——黑名单不能因为跑在 Linux 就漏判）、不误杀名字含 sh 的普通命令、缺字段的计划被拒、`SANDBOX_ALLOW_SHELL` 只认真值、计划摘要说清要干什么。<br>**容器后端**：隔离参数一个不少、镜像之后才是命令、只挂工作目录、去调 docker 而不是裸跑、内置文件动作不进容器、如实上报 full、**docker 不可用时拒绝一切执行而不是退回裸跑** |
| `metrics` 包的 4 个用例 | 桶是**累计**语义（写成分档 P99 会静默算错）、导出符合 Prometheus 三件套、计数器/求和/仪表老行为不被改坏、桶上界不用科学计数法 |

**端到端（假模型 + 真 Redis）**

| 用例 | 守住的不变量 |
|---|---|
| `TestTurnAndLogInvariants` | 一轮对话落 `user/message` + `assistant/message`；`system/prompt` 只写 1 次 |
| `TestCompactionPreservesRecentTurnsAndRebuildsFold` | 摘要输入含早期工具事实；近期整轮原文与配对保留；原日志不改；删除指针、旧缓存与二次压缩后历史正确 |
| `TestCompactionSummaryFailureLeavesLogUnchanged` | 摘要器失败可辨，原日志和折叠水位不变 |
| `TestSystemPromptWrittenOnlyOnce` | 3 轮之后 `system/prompt` 仍是 1 条（每轮重写会白涨日志） |
| `TestToolCallResultsArePaired` | `tool/call` 与 `tool/result` 数量恒等，且每条 result 都能找到配对的 call；**step 边界闭合**（`step/start` == `step/end`，最后一步原因 = 结构化 `completed`） |
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
| `TestRepairClosesOpenStepAndDanglingToolCall` | 崩溃留下的**开放 step** 与**悬空 tool/call** 也要补掉（合成 step/end + "结果未知"的 result），幂等且不改历史 |
| `TestResumePointReportsLastClosedStep` | `ResumePoint` 能报告"续跑到哪"（闭合 step 数 + 末尾是否还有开放轮次） |
| `TestResumeTurnContinuesWithoutReplayingTool` | 崩溃后 `ResumeTurn` 不重放已派发工具，显式用户来源仍映射到候选且原输入保留，并正常收尾 |
| `TestContextShadowRetainsRequiredUserSource` | 显式来源约束绑定到实际Harness历史对象；缺失/助手来源 unresolved，SDK输入仍不被替换 |
| `TestContextShadowApprovalEvidence` | 批准/拒绝/执行失败续答均保留显式用户来源及后续完整连续区间；预算不足如实诊断，实际输入和工具执行语义不变 |
| `TestContextShadowUsesSuccessfulReadbackAsEvidence` | 成功读取并映射的来源进入影子候选；较新无关轮不替代该证据轮，SDK实际历史仍保留 |
| `TestContextShadowDoesNotPromoteSearchOrFailedReadback` | 搜索预览及读取失败不计为已读证据 |
| `TestContextBaselineShadowApplicationChangesOnlyEvaluationInput` | 真Harness接入评测包装器后，仅真实模型适配器输入裁掉安全候选里的旧整轮，当前输入保留并与应用报告消息数一致 |
| `TestNestedAgentDoesNotPolluteParentStepLog` | 子 agent 的 step 事件**不写进父会话日志**：父日志 step 成对、序列校验干净（防止交错日志骗 `Repair` 反复补事件） |
| `TestInterruptedStreamIsMarked` | 流被切断的回复标 `interrupted`（不写 `completed` 收尾）、投影里带说明、下一轮 Repair 能补上 |
| `TestCanceledTurnPersistsPartialReply` | 用户取消后保存已输出的部分回答并标 `interrupted`，不写 `completed`，Repair可补中断收尾 |
| `TestCLIInteractiveInputEditing` | Linux/WSL PTY下单行/多行编辑有效；输入区显示上下横线且不显示 `You`；终端从80列缩至60列后上下横线即时适配；bracketed paste在Enter前不提交、提交一次且命令样式正文不执行；`/paste`边界、空闲Ctrl+C和终端状态恢复正确 |
| `TestCLIUsesLineReaderWhenInputIsNotTerminal` | 输出连接终端而输入来自管道时，仍走逐行读取，不错误启用raw mode |
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
- 短消息不压缩与长消息触发压缩用例已经存在；接下来补“压缩后信息保真”的 R-1 验收。
