# CLI 首批验收

当前状态（2026-09-29，WSL自动回归及Windows Terminal手测通过）：交互输入已移除 `You` 标签并在内容上下绘制横线；PTY用例覆盖80列缩至60列后两条横线更新。编辑器监听 `SIGWINCH` 并按输出终端宽度重绘整框。用户WSL重跑编辑器定向1项、`CLI|CanceledTurn`定向13项及knowledgeintegration全量231项，均通过、0跳过。用户重装最新 `gwagent` 后确认实际终端缩放正常；首次运行仍使用旧安装二进制。用户确认桌面快捷方式可启动独立Windows Terminal/WSL CLI、右键多行粘贴无shell警告、长文本显示和光标编辑正常。前两次回归的测试辅助函数问题已通过精确横线段计数修复。Linux目标 `go vet` 通过；本机未运行Go测试或程序。

验收状态（2026-09-28）：用户WSL `-run CLI` 定向10项及原knowledgeintegration全量228项通过，均0跳过。真实 CLI 在隔离 user `9280928` 下可跨 `/exit` 重启回忆标记。用户实测 `/paste` 中 PASTE-A/B/C 只提交一次；运行中 Ctrl+C 后回到提示符并继续对话，空闲 Ctrl+C 返回 shell。取消持久化回归经用户重跑 `-run CanceledTurn` 1项、knowledgeintegration全量229项通过，均0跳过。真实 CLI 长回复中 Ctrl+C 后未见记忆落盘失败提示，随后一轮仍可正常对话；本次未重启核对部分回答是否可恢复。当前多行输入编辑器回归也已通过：用户报告编辑器定向1项、`CLI|CanceledTurn`定向13项、knowledgeintegration全量231项，均0跳过。首次PTY测试因ANSI颜色码隔开换行和提示文本而超时，改用提示文本匹配后复验通过。用户已成功运行安装脚本并从 `gwagent -user 9280928` 启动，并确认方向键、Home/End、Backspace/Delete、Up/Down历史、`/help`、`/exit`与空闲Ctrl+C均正常。桌面快捷方式已创建并回读核对目标参数，尚未由用户双击验收。Windows Terminal右键多行粘贴无警告、窄窗口/尺寸调整和受控剪贴板验收仍待做。

2026-09-28 单行输入编辑器的历史验收记录：Linux/WSL TTY模式此前新增单行编辑与进程内历史；支持左右/Home/End、Backspace/Delete及上下浏览，raw mode只在等待输入时开启。用户WSL定向1项、合并13项和knowledgeintegration全量231项通过、0跳过，真实终端确认既有按键可用。此前结果不覆盖新增多行粘贴改动；本批 bracketed-paste PTY回归已通过，Windows Terminal手测见上方当前状态。

当前终端实现更新：raw mode 与 bracketed paste 只在等待输入时开启，提交或退出后恢复；Windows、重定向及管道输入继续逐行读取。交互输入区显示上下横线，不显示 `You`；收到 `SIGWINCH` 时读取输出终端宽度并重绘边框，PTY用例覆盖80列缩至60列时上下线同时更新，用户WSL定向及全量回归已通过。用户重装最新 `gwagent` 后确认实际终端缩放正常。`TestCLIInteractiveInputEditing` 还覆盖Enter前不提交、按Enter只提交一次、粘贴 `/exit` 作为正文、Up键在多行中移动光标及模式/termios恢复；`TestCLIUsesLineReaderWhenInputIsNotTerminal` 继续覆盖管道回退。用户已在Windows Terminal实测桌面快捷方式启动、右键多行粘贴、长文本显示和光标编辑正常。

在WSL验收：

```bash
bash scripts/test-fast.sh -run TestCLIInteractiveInputEditing
bash scripts/test-fast.sh -run 'CLI|CanceledTurn'
bash scripts/test-fast.sh -tags knowledgeintegration
bash scripts/install-agent.sh
export PATH="$HOME/.local/bin:$PATH"
gwagent -user 9280928
```

WSL 测试通过后，在 Windows Terminal 启动 CLI，右键粘贴包含三行和空行的文本。粘贴时不应弹出多行 shell 粘贴警告；文本应留在同一编辑区，按 Enter 前不触发模型请求，按 Enter 后只发起一轮。再单独粘贴 `/exit` 并按 Enter，测试它作为正文发送且 CLI 不退出。该手测会实际调用模型，使用隔离测试账号和无敏感内容。

按键名操作时要按键盘上的物理按键，不要在提示里输入 `/home`、`/left` 等文字；斜杠开头会作为CLI命令处理。可以用本地命令低成本验证 Home/End：输入 `xit`，按键盘上的 Home，输入 `/e`，再按 End；提示行应变成 `/exit`，按 Enter 后直接退出而不调用模型。重新启动后输入 `/exiXt`，按 Left 两次再按 Delete，提示行也应变成 `/exit`；按 Enter退出。历史键可用 `/help` 建立一条本地历史，然后在下一行输入临时草稿，按 Up 回看 `/help`、按 Down 恢复草稿，最后按空闲 Ctrl+C 退出。按键验证后可再观察窄窗口、调整窗口尺寸和终端模拟器的复制粘贴行为；退出后终端应恢复正常回显。

2026-09-22 CLI多行粘贴（未提交）：逐行Scanner会把多段粘贴排成多轮，新增显式/paste收集、独立行/send提交、/cancel丢弃；保留空行，粘贴正文不走CLI/审批命令分派，EOF/Ctrl+C退出不提交未完成正文，64KiB累计上限。普通模式仍逐行提交，未实现自动粘贴识别。新增TestCLIPaste覆盖多段CRLF、命令正文、取消、空提交、EOF及超限；go vet含tag与CLI构建通过，用户WSL定向及全量待验，未运行本地测试。

| 新增用例 | 守住的不变量 |
|---|---|
| TestCLIPaste | 多段与空行只提交一轮；粘贴的命令只作正文；取消、EOF、空内容、超限不提交部分正文 |

定向运行：`bash scripts/test-fast.sh -run CLIPaste`。真实终端需先输入`/paste`回车，粘贴三段文字，单独输入`/send`回车，确认仅回答一次。普通模式直接粘贴多行仍会逐行发送。

2026-09-22 CLI工具状态收起（未提交）：按用户要求将模型/工具状态改为终端单行原地更新，回答/推理/结束前清除，每轮只保留工具调用次数；非TTY仅汇总，不刷状态明细。工具事件持久化与错误传播不变，修正NO_COLOR下等待行清理。调整TestCLIToolProgress断言，go vet含tag及CLI构建通过，WSL CLI8项及累计全量182待验。用户真实日志证明工作区工具能调用，但本次代码解释触及10步上限未完成；不因此标记任务成功，本批不调整步数预算。

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
| TestCLIInteractiveInputEditing（Linux/WSL） | 单行/多行编辑；bracketed paste不提前提交、Enter仅提交一次、命令样式正文不执行；`/paste`边界、空闲Ctrl+C及终端状态恢复 |
| TestCLIUsesLineReaderWhenInputIsNotTerminal（Linux/WSL） | 输出虽为终端但标准输入为管道时，回退Scanner而不尝试raw mode |
| TestCLIHarnessHistory（test/e2e/） | 假模型与真Redis下，CLI经过完整Harness保存两轮历史和step/turn事件 |
| TestCanceledTurnPersistsPartialReply（test/e2e/） | 收到部分输出后取消仍将其标记为interrupted持久化，不写completed，Repair可补中断收尾 |

```bash
cd "/mnt/e/agent study/Go-AI-Gateway"
bash scripts/test-fast.sh -run CLI
bash scripts/test-fast.sh -tags knowledgeintegration
bash scripts/run-agent.sh -user 9280928
```

此前已验证：使用未用于日常对话的正整数user ID，标记跨进程重启后可回忆；显式 `/paste` 三项只提交一次；运行中 Ctrl+C 后可继续，空闲 Ctrl+C 可退出；取消持久化回归与knowledgeintegration全量已通过。真实 CLI 长回复中 Ctrl+C 后未见 `记忆落盘失败: context canceled`，并能继续对话；本次未重启核对部分回答是否可恢复。输入编辑器自动回归已通过（定向1项、合并13项、全量231项均0跳过），用户已完成安装及Windows Terminal手测：快捷方式启动、窗口缩放、右键多行粘贴、长文本显示和光标编辑正常。bracketed paste协议支持的自动多行接收已实现；没有该协议标记时的启发式识别仍后置。审批输入沿用auth:approve（先展示确认码）、auth:approve <码>、auth:reject、auth:status <提案ID>，不自动批准。

调用链：cmd/agent → harness.NewFromEnv → cli.Run → HandleApprovalCommand / RunAgentTurn → Runtime.NewLoop → ownLoop。CLI负责读行、显示、信号；历史、工具执行、审批和日志由原Harness处理。

CLI只需Redis和模型配置，可读取仓库根目录.env；不启动API/MySQL/RabbitMQ，不要求JWT_SECRET。MCP仅在配置时连接；连接失败明确退出。可执行文件先构建到bin/gwagent，Windows为bin/gwagent.exe，不使用go run。

边界：默认user=1是本地存储身份，不是登录认证；应在自己的开发环境使用。同user复用旧Redis历史/审批，Web的MySQL对话不迁移也不自动导入。当前运行互斥仅在同一Harness实例内，不能用同user同时开多个CLI或与API并发运行。取消等待Harness入口返回，不承诺强制终止忽略context的工具。常规过程打印已删除，异常诊断保留，不提供新的结构化工具面板。Linux/WSL TTY提供单行编辑和进程内历史；bracketed paste协议可将多行粘贴作为一次可编辑输入，不支持该协议的终端仍可使用显式`/paste`；仍无全屏TUI、无多会话选择。不新增通用文件或shell工具。

Web保留原代码和路由，暂停开发；CLI启动不启动Web。资料业务代码和数据保留，后续按实际需要复用，不能把未接入的knowledge工具宣称为CLI现有能力。
