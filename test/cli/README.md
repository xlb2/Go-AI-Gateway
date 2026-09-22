# CLI 首批验收

2026-09-22 提交前最终验收：用户WSL带knowledgeintegration全量178项通过、0跳过，覆盖CLI8项及OpenCode、ConfiguredLoop相关回归，覆盖下方旧待验记录。真实终端单行推理窗口/缩放/复制粘贴与运行中取消仍待交互验收。操作及实现边界见 [CLI 文档](../../docs/cli.md)。

2026-09-22 推理单行窗口（未提交）：用户确认真实推理字段可见，要求缩短显示。CLI仅保留最近120 rune，按实时终端宽度保守双列预算截尾，原地重绘，回答/工具状态/结束时清除；过滤换行及控制字符，NO_COLOR仍可单行更新，非TTY或TERM=dumb只输出一次收到推理的提示、不输出全文。使用已在go.sum和本地缓存的x/term v0.41.0读取宽度。保留终端原生输入、选中复制和粘贴，未接管剪贴板/鼠标，仍不支持将多行粘贴作为单条编辑。调整TestCLIReasoning纯文本断言；静态检查及构建通过，WSL CLI8项与实际窄窗口/复制粘贴待验，未运行本地测试。

2026-09-22 CLI推理字段显示（未提交）：本地锁定依赖Eino v0.8.11 schema及OpenAI ACL v0.1.17确认reasoning_content映射为ReasoningContent，Loop原样转发；Harness新增独立回调，CLI默认显示Reasoning (provider)，/reasoning on|off仅控制显示，无字段无占位，不强制开启模型推理，不混入最终回答持久化。新增TestCLIReasoning三子场景（显示/隐藏/无字段）经假SSE→真实SDK→Harness→CLI验证并检查历史隔离；go vet含tag和CLI构建通过，WSL CLI预计8、合并ConfiguredLoop预计11、带knowledgeintegration全量预计178待验。未请求真实模型，当前服务是否返回字段待验；用户此前日志已确认工具进度真实可见，工具进度回归运行结果未提供。

2026-09-22 CLI工具进度（未提交）：ownLoop通过每轮context观察器报告模型请求、工具开始/返回/报错，CLI串行化状态与文本写入；不显示参数/结果正文，returned不等于业务成功或落盘成功，未知工具标记failed/unavailable。子循环不混入主面板；审批直接执行/摘要未接独立状态，工具结果写失败仍由原错误路径报告。新增TestCLIToolProgress（假模型+真Redis）并加强ConfiguredLoopsKeepDependenciesSeparate、ConfiguredLoopStepWriteBarrier断言。go vet含tag、CLI构建通过；用户WSL CLI预计7、合并ConfiguredLoop预计10、带knowledgeintegration全量预计177待验，真实显示待验。

2026-09-22 CLI显示验收补记（用户WSL日志）：CLI定向6项通过、0跳过；run-agent.sh重建后启动元信息、You/Agent分区和Done耗时已实际显示，真实模型正常回复，语气较旧人设自然。文本记录不证明颜色、窄窗口布局或运行中取消已验；带knowledgeintegration全量预计176及OpenCode定向3结果仍待提供。本轮只记录验收，不扩工具、不提交。

2026-09-22 显示优化：新增启动元信息、终端颜色与纯文本降级、You/Agent分区、等待提示及完成耗时。TestCLIBanner验证元信息控制字符过滤和写失败传播，原对话测试增加重定向不输出ANSI断言。静态检查/构建通过；用户WSL定向CLI预计6项、带knowledgeintegration全量预计176项待验，真实终端颜色/换行/取消待验。无全屏TUI、Markdown渲染或输入编辑器。

2026-09-22 CLI输出清理（未提交）：删除记忆读写/检索、修复成功、压缩开始、重试过程、MCP连接和回复字数等过程打印，去掉沙箱/流中断的重复输出；错误提示、事件持久化及指标保留，不新增日志框架。go vet含knowledgeintegration/rageval及CLI构建通过；本批未跑运行测试。用户真实模型两轮已记住并答出CLI-7392，覆盖此前真实对话待验；重启历史与Ctrl+C仍待验，OpenCode新增3项WSL结果尚未收到。

2026-09-22 OpenCode后续修复：用户真实调用返回400缺少x-opencode-session。已在统一模型工厂对官方HTTPS域名补稳定会话头和自有User-Agent；3项新增请求回归位于test/assembly/opencode_test.go，go vet及构建通过。用户WSL待跑 `bash scripts/test-fast.sh -run OpenCode`（预计3）、`bash scripts/test-fast.sh -tags knowledgeintegration`（预计175），再退出旧CLI并通过run-agent.sh重建重试。下方172是修复前证据，不证明本补丁已运行通过。

2026-09-22：CLI 直接调用已有 Harness，不创建第二套执行循环。静态检查和编译通过；用户WSL定向5项、带knowledgeintegration全量172项通过，均0跳过。run-agent.sh已重建并启动到user:1的you>提示符，沙箱报告local/none。真实模型对话、交互取消及重启后历史仍待验证，未提交。

| 用例 | 守护的行为 |
|---|---|
| TestCLIReasoning（test/e2e/） | SDK推理分片独立显示、显示开关、无字段不造内容、最终历史不混入推理文本 |
| TestCLIToolProgress（test/e2e/） | 真实Harness工具调用状态与Redis工具事件对应，状态顺序和模型续调可见 |
| TestCLIBanner | 启动信息可见、元信息控制字符过滤、纯文本输出及写失败传播 |
| TestCLIConversationAndApproval | 多轮输入保持真实用户身份；审批使用原入口；流式回复不重复打印；非流式拦截回复可见 |
| TestCLIErrorAndCommands | 错误与半截回复可见，下一轮仍可输入；本地命令不提交模型 |
| TestCLICancelWaitsBeforeNextTurn | Ctrl+C 传播取消，调用返回前不开始下一轮，取消后仍可交互 |
| TestCLIInputAndOutputFailures | 输入过大、输出失败显式返回；空闲中断可退出 |
| TestCLIHarnessHistory（test/e2e/） | 假模型与真Redis下，CLI经过完整Harness保存两轮历史和step/turn事件 |

```bash
cd "/mnt/e/agent study/Go-AI-Gateway"
bash scripts/test-fast.sh -run CLI
bash scripts/test-fast.sh -tags knowledgeintegration
bash scripts/run-agent.sh -user 1
```

定向5项、带 knowledgeintegration 全量172项已由用户验证，均0跳过。后续实际终端验收：连续问答两轮；运行中Ctrl+C取消后继续输入；空闲Ctrl+C或/exit退出；重新启动同一user后核对历史延续。审批输入沿用auth:approve（先展示确认码）、auth:approve <码>、auth:reject、auth:status <提案ID>，不自动批准。真实模型/真实终端信号行为尚未验证。

调用链：cmd/agent → harness.NewFromEnv → cli.Run → HandleApprovalCommand / RunAgentTurn → Runtime.NewLoop → ownLoop。CLI负责读行、显示、信号；历史、工具执行、审批和日志由原Harness处理。

CLI只需Redis和模型配置，可读取仓库根目录.env；不启动API/MySQL/RabbitMQ，不要求JWT_SECRET。MCP仅在配置时连接；连接失败明确退出。可执行文件先构建到bin/gwagent，Windows为bin/gwagent.exe，不使用go run。

边界：默认user=1是本地存储身份，不是登录认证；应在自己的开发环境使用。同user复用旧Redis历史/审批，Web的MySQL对话不迁移也不自动导入。当前运行互斥仅在同一Harness实例内，不能用同user同时开多个CLI或与API并发运行。取消等待Harness入口返回，不承诺强制终止忽略context的工具。常规过程打印已删除，异常诊断保留，不提供新的结构化工具面板。单行输入，无全屏TUI、多行编辑、多会话选择；不新增通用文件或shell工具。

Web保留原代码和路由，暂停开发；CLI启动不启动Web。资料业务代码和数据保留，后续按实际需要复用，不能把未接入的knowledge工具宣称为CLI现有能力。
