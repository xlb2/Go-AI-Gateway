# 上下文成本基线 v1

2026-09-23 工具证据最新验收：用户WSL ContextBaseline定向4/knowledgeintegration全量199通过、0跳过。reading-dev补采run-20260923T084722Z-66816.json三轮PASS，工具证据确认1–27截断且有后续，45–55完整但有后续，90–100完整且无后续；回答82/77/68字符，助手复核本题通过。实报输入13894/输出349、5次主调用均有usage；未改模型策略，不把与旧报告差异当节省或质量修复。评审见test/contextbaseline/review-20260923-reading-evidence.md，原失败和quality=unreviewed保留。停止重复补采，下一工程切片CM-1稳定来源/分页回读；费用缓存及旧evidence问题仍未完成。仅更新文档，未本地测试/调用模型、未提交/push。

2026-09-23 CM-0工具证据补齐（未提交）：采集器逐轮保存主循环工具返回/失败事件，复用ToolObservation名称/参数哈希/耗时/实际行范围及分页截断标志，复制快照，不保存原始参数或结果正文；缺观察值保留null。新增ContextBaselineToolEvidence真Harness/文件工具回归，覆盖中部has_more与末尾无后续、跨轮隔离及正文不泄漏。生产循环不变，无额外模型调用；go vet三标签通过，WSL ContextBaseline预计4/全量199待验。回归通过后仅重采reading-dev核对分页证据；不覆盖旧报告或宣称修正了模型质量，CM-1尚未开始。未本地跑测试/真实模型、未提交/push。

2026-09-23 四开发场景基线交接：用户evidence-dev/reading-dev各三轮采集PASS，但助手复核并非质量全过。evidence末轮去Markdown后105字符超100，step定义未满足项目预期且题面缺专有定义；reading验收码/行号正确，但45–55无后续页说法混淆文件分页，实际调用范围因报告缺工具元数据无法核实。四题合计实报输入37954/输出1952、17次主调用均有usage；费用缓存未知。详情test/contextbaseline/review-20260923-evidence-reading.md。下一切片先复用工具进度补采集证据，再推进CM-1；不重跑已判定前两题或留出集。CM-0未整体完成，本轮仅评审和文档，未改生产代码/跑模型/测试/提交。

2026-09-23 恢复场景基线：用户WSL resume-dev三轮PASS，报告run-20260923T083944Z-65615.json。助手按expect复核通过：重建Harness后保留目标/约束/下一步，第二轮79字符；项目编号正确且未编造上线日期。实报输入7579/输出458，4次主调用均有usage；第三轮含工具反馈的两个逻辑步骤，累计输入4021。报告未存工具名/正文，不凭模型自述认定检索范围。仅同进程重建，非真实时间间隔/进程重启/零缓存验证；费用缓存仍未知。评审见test/contextbaseline/review-20260923-resume.md。下一步evidence-dev/reading-dev；CM-0未整体完成，未改生产代码、未本地跑测试或模型、未提交/push。

2026-09-23 最新验收与首份完整基线：用户WSL ApprovalClaim定向5/全量198通过、0跳过，覆盖过期测试前提修正。continuity-dev真实3轮采集PASS，报告run-20260923T083801Z-65467.json；助手逐项复核既定expect通过（末轮51字符），非独立人工复核，原quality=unreviewed保留。实报输入5463/输出388，主调用3次均有usage，无子/摘要调用；费用缓存未知。每轮系统+工具估算1533，短对话固定开销占主导，不证明已节省。详见test/contextbaseline/review-20260923-continuity.md；下一步resume-dev，再补来源及长工具场景。CM-0仍未整体完成。本轮仅读报告/更新文档，未本地运行测试或模型、未提交/push。

2026-09-23 审批过期测试前提纠错（未提交）：用户ContextBaseline定向3通过、全量197通过/1失败/0跳过；唯一失败仍为ApprovalClaimChecksExpiryAtConsumption。新增诊断证明deadline=16:34:44.622，应用=44.553、Redis=44.553，断言时两端均未到期；此前两端时差假设不能解释本次失败。固定Sleep依赖单调时钟，不能证明JSON持久化期限对应的墙钟已越界（具体时钟调整原因未确认）。现原用例轮询应用和Redis墙钟，均超过期限10ms再断言，单调时间5秒上限，未满足前提单独报错；保留真实读取快照及无执行记录/未消费断言，不改生产代码、不新增顶层用例。go vet三标签及差异检查通过，WSL ApprovalClaim定向5/全量198待重验；基线身份修复的定向回归已验，真实采集未重跑。未本地运行测试、未提交/push。

2026-09-23 真实基线入口纠错（未提交）：用户continuity-dev首次采集失败，部分报告run-20260923T082823Z-63557.json仅1轮，轮耗时6ms、SDK尝试0。采集入口遗漏CLI所需user_id上下文，Runtime在模型调用前失败；旧回归自带身份掩盖该遗漏。现采集Run显式接收owner并注入身份，Harness回归改从空白上下文启动，报告新增安全error_code且不存原始错误。go vet含knowledgeintegration/rageval/contexteval及差异检查通过；修复后WSL定向3/全量198和真实三轮采集待重验，此前198通过不覆盖本修复。失败报告保留，不计作质量/费用基线；未运行本地测试或真实模型、未提交/push。

2026-09-23 最新验收：用户WSL ApprovalClaim定向5项、带knowledgeintegration全量198项通过，均0跳过，覆盖审批过期修复及CM-0c采集器的常规回归；此前ContextBaseline定向3项通过。真实模型采集尚无报告，不能据此声称质量/费用基线完成。仅记录用户证据，未重跑测试、未提交/push。下一步用户在WSL显式采集continuity-dev三轮，助手再读取报告分析。

2026-09-23 用户WSL反馈：ContextBaseline定向3项通过、0跳过；带knowledgeintegration全量197通过/1失败/0跳过，失败为已有审批消费过期用例。该问题已另行修复并待重新回归；不把定向通过当全量通过，真实采集仍未执行。

CM-0c：固定输入、保存用量和回答，供下一阶段上下文选择前后对照。工具与循环复用现有 Harness/Runtime，代码仅在 test/ 与 scripts/。没有另写 agent loop，也没有自动记忆抽取。go vet含knowledgeintegration/rageval/contexteval与Bash语法检查通过，运行回归待用户WSL。本批尚未采集真实报告，不代表节省效果已经验证。

## 场景与范围

| ID | 集合 | 轮数 | 检查目标 |
|---|---|---|---|
| continuity-dev | 开发 | 3 | 用户偏好、纠正决定、未完成事项；短历史固定开销 |
| resume-dev | 开发 | 3 | 第2轮重建Harness，同一Redis历史；“继续”和未知上线日期 |
| evidence-dev | 开发 | 3 | 原文引用、事实与推断区分、换话题 |
| reading-dev | 开发 | 3 | 长工具正文、范围/分页标志、补读中部48行和尾部96行 |
| continuity-holdout | 留出 | 3 | 不同项目和编号下的最新决定与优先级 |
| resume-holdout | 留出 | 3 | 不同事实的续接和不捏造日期 |

cases.json 中 expect 是人工评审标准，不送给模型。只有两份合成资料复制到临时只读工具根目录，预期答案和真实项目文件都不在该根目录。MCP不连接；内置工具与默认Runtime保持一致。使用新随机评测owner，既不读取user 1历史，也不复制/删除个人数据。owner保留在报告中；Redis中预留键和该owner的日志留存，不自动清理。预留只协调本采集器，不能防止其它程序刻意使用同一owner。

重建的是Harness实例，仍在**同一进程**，没有时间等待和缓存清空。这不是实际隔几天、进程重启或零缓存实测。只能证明评测时是否从相同持久记录接上。长文件只模拟工具结果变大，并不是长达数月的历史。权限/取消/恢复/长消息回读等硬边界还要依赖专项回归，不能用这六个场景替代全部CM-5验收。

## 执行

先由用户在WSL运行不调用真实模型的回归：

```bash
bash scripts/test-fast.sh -run ContextBaseline
bash scripts/test-fast.sh -tags knowledgeintegration
```

本批新增3个常规顶层用例，预计定向3项/全量198项。实际通过数与零跳过才是运行证据。真实采集用例由独立contexteval标签隔离，不属于这198项。

确认后采集**一个**开发场景；以下命令会使用已有CLI模型配置并产生真实调用费用：

```bash
bash scripts/context-baseline.sh --real continuity-dev
```

脚本默认只运行该场景的3轮，不自动跑全套。随后按需要分别选择resume-dev、evidence-dev、reading-dev。每轮最多3分钟，步数与重试使用现有环境配置；没有精确金额上限。每次创建不同owner，结果在test/contextbaseline/results/，以UTC时间和进程号命名，禁止覆盖已有报告。Redis/模型配置错误会失败，不静默跳过。报告内容包括合成输入和真实回答，不含API key、完整endpoint或原始错误正文；模型可能将自己推断的内容写入回答，应人工核对。

留出题在开发调参结束后才运行，例如：

```bash
bash scripts/context-baseline.sh --real resume-holdout --holdout
```

留出集是流程约束，不是保密考题；源码中可以看到。不要看完留出结果再针对该题调参并宣称仍是独立验收。

## 报告怎么读

- 新报告逐轮增加tool_events：仅主循环工具返回/失败事件，包含名称及已有ToolObservation的参数哈希、耗时（Elapsed为纳秒）、实际Path/FirstLine/LastLine和HasMore/ContentTruncated/ScanLimited。无事件为[]，旧报告缺字段不能当未调用；observation=null表示元数据不可用。失败/未知工具的零字段不能解释为完整读取，returned也不等于业务成功。无原始参数和结果正文，不能用哈希还原请求范围；没有next值或调用ID，也不覆盖子循环/审批直接执行。相同行范围不保证中间每行内容正确，必要时仍核对持久事件。
- error_code仅保存允许的分类：missing_user_id、http_状态码、deadline_exceeded、canceled、restart_error或run_error；不记录原始错误正文。旧报告可能没有该字段。
- source_sha256标识本地internal源码、采集器Go源码及go.mod/go.sum，包含未提交代码；corpus_sha256标识题目和两份资料。记录模型名、provider host、预算、步数、重试策略、Go版本与系统；不记录秘密配置。
- 每轮logical_main_inputs是主循环逐次估算的组成；sdk_attempts的Main/Child/Summary是实际SDK尝试汇总。三桶互斥，累加所有轮次，**不要再加一次逻辑步骤的usage**，准备轮也不能省略。
- Prompt/Completion仅累计已返回usage；WithUsage小于Attempts说明用量不全。即使相等，断流最终账单仍可能未知。EstimatedInput是估算，UnknownSchemas非零时还缺部分定义。Fees与cached_tokens保持null，不能当作零。
- elapsed_ms包括该轮Harness调用全过程（包括其同步摘要/重试）；first_text_ms只看最终回答的首次非空分片，不把推理字段当首字，无文本分片则为null。重建Harness时间不在轮耗时内，不能当CLI冷启动耗时。
- status=returned或execution_complete=true仅表示调用正常返回，不是答案正确或业务成功。quality固定unreviewed，禁止自动把文件生成/测试PASS当评测通过。错误或取消立即停止后续轮，部分报告仍保存；初始化失败可能只留下空文件，应视为无有效报告。

## 人工判定与成本口径（运行前固定）

逐轮按expect逐项填“通过/失败/无法判断”，记录证据。所有标注的事实、最新决定、约束和引用都必须通过；编号和行号必须精确，未知日期不得编造，长度按可见回答Unicode字符含标点计。执行失败、未评审或无法判断的场景不能算达标。引用要支持对应陈述，不只是出现文件名。

对照必须保持同一语料、源码/策略标识、模型和工具配置。新策略实现后至少分别重复连续/续接两类，报告每次结果，不能只挑最便宜一次。usage和质量逐轮先列明，再汇总整个场景。一个场景是一个任务，不用成功轮数当“达标任务”分母。

没有可验证费率、缓存字段或完整用量时，报告费用未知，先比较包含辅助调用的已知输入/输出token及覆盖范围。若后续做**按零缓存折算**，必须明确标记反事实假设，分别用全部输入与输出乘对应费率，不是服务商实付账单；不同模型分别计价。真实费用/达标任务只有分子完整且分母大于零才能报告。任何关键质量退化都不能因token下降而通过。

### 本批回归

| 用例 | 守住的边界 |
|---|---|
| TestContextBaselineFixtures | 固定续接场景可用，未知ID/未显式允许的留出题被拒绝 |
| TestContextBaselineToolEvidence | 假模型经真实文件工具读取中部/末尾，核对实际行范围、分页标志、哈希、跨轮隔离；报告不含工具正文和原始参数 |
| TestContextBaselineStopsOnFailure | 第二轮失败后不继续；部分回答和首字证据保留；费用未知；不自动判优 |
| TestContextBaselineHarnessReport | 从空白上下文验证采集器注入owner；假HTTP模型→真SDK→Harness→报告三轮计量；实例重建后仍保留历史；每轮独立统计；未知值序列化null |

仅静态检查不代表上述用例已运行。真实采集报告须单独提供，不能由假模型结果推断质量或收益。
