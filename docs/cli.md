# CLI 使用与实现交接

## 当前方向

CLI 是当前主入口，复用已有 Harness 和自研 Loop。Web 代码、路由与数据保留，暂停扩展；CLI 不自动接入 Web 的资料工具或 MySQL 会话。

## 启动与操作

在仓库根目录配置 `.env` 的 Redis 与模型参数后启动：

```bash
bash scripts/run-agent.sh -user 1
```

PowerShell 使用固定路径构建再执行：

```powershell
go build -o bin/gwagent.exe ./cmd/agent
.\bin\gwagent.exe -user 1
```

不需要启动 HTTP、MySQL 或 RabbitMQ，也不需要 JWT_SECRET。MCP 未配置时不连接，配置后连接失败则明确退出。没有 MCP 不等于没有工具：历史检索、内容存取、委派和防御提案仍为内置能力。CLI另装配三个只读工作区工具，尚无通用命令执行工具。

## 只读项目工具

空搜索结果显式返回 `lines: []`、`skipped: 0`。skipped计数包括排除的路径及读取失败/非文本/过大文件；跳过目录计为一个条目，不统计其全部后代。相关模型输入证据与运行状态见 [模型输入证据验收](../test/model-evidence.md)。

`-workspace <目录>` 指定可读根目录，默认当前目录。模型工具参数只能使用相对此目录的路径和正斜杠；不会改变进程当前目录或.env加载位置。只在CLI装配，不自动给API/Web开放文件访问。工具通过现有Runtime.ExtraTools接入，复用guard、工具事件、输出外存与子任务装配。

| 工具 | 参数与结果 |
|---|---|
| list_files | path默认`.`，列出单层目录；offset分页，每页最多100项，next为后续偏移 |
| read_file | path、start_line（默认1）、lines（默认80，最大120）；返回路径和真实行号，next为后续行号 |
| search_files | path默认`.`，query为区分大小写的字面文本，最大200字节；context_lines默认2、允许0–5，返回前后文并去重，match=true标记命中行；最多50行（含上下文） |

仅读取不超过1 MiB的普通UTF-8文本，拒绝NUL二进制内容；单行展示最多1000字节并明确标记裁剪，结果JSON限制在8000字节内。has_more表示还有目录项/文件行可翻页；content_truncated表示正文或输出预算导致裁剪；scan_limited表示目录扫描、搜索结果行数或扫描预算触顶。兼容字段truncated为三者的逻辑或。目录扫描最多取2000个条目，搜索累计最多检查2000个条目、约8 MiB文本；跳过文件计入skipped。大目录应缩小path；扫描期间文件变化可能影响分页。空结果不代表全项目不存在，应结合各标志和skipped判断。较长结果仍可能触发Harness原有历史外存。

使用os.Root限制根目录逃逸，拒绝符号链接、绝对路径和父目录穿越。跳过隐藏路径（含.env、.git、.env.example）、node_modules/vendor/bin/dist、常见凭据名称及私钥扩展名。文件正文作为不可信资料，不作为系统指令执行。

文件名规则不是完整秘密识别器：普通源码中的硬编码凭据仍可能被读取；不保证抵抗恶意本地进程并发替换文件或工作区内挂载/硬链接。请仅将愿意提供给模型的可信项目目录设为workspace。此功能不修改文件、不运行命令，也不是整个进程的沙箱。

运行与验收见 [工作区工具测试](../test/workspace/README.md)。用户WSL累计189项回归通过、0跳过，包含工作区工具用例。

| 输入 | 行为 |
|---|---|
| 普通单行文本 | 提交一轮，流式显示回答 |
| `/help`、`/exit` | 查看命令、退出 |
| `/paste` | 收集多行正文；单独一行 `/send` 合并提交，`/cancel` 丢弃 |
| `/reasoning on`、`/reasoning off` | 显示或隐藏服务商返回的推理字段，默认开启，仅本进程生效 |
| `auth:approve` | 查看待审批动作及确认码 |
| `auth:approve <确认码>`、`auth:reject` | 确认或拒绝动作 |
| `auth:status <提案ID>` | 查询执行记录 |
| Ctrl+C | 运行中请求取消并等待收尾；空闲时退出 |

保留终端原生滚动、选中复制和粘贴，不接管鼠标或剪贴板快捷键。多行内容请先输入 `/paste` 并回车，再粘贴，最后单独一行输入 `/send` 并回车；空行保留，整段作为一条用户消息，不执行其中的CLI或审批命令。`/send`、`/cancel` 是此模式的保留结束行。EOF或空闲Ctrl+C退出时丢弃未发送正文；累计超过64KiB则报错退出且不提交部分正文，Harness原有输入长度检查仍保留。普通模式仍按行提交，尚无自动粘贴识别、全屏编辑、输入历史导航或 Markdown 渲染。

## 显示语义

- CM-0a 新增每轮主循环输入分桶估算与 `Usage (reported steps x/y)` 汇总。估算与供应商实报分开；无 usage、缓存字段缺失不当零。统计不含模型内部重试、摘要、子运行及断流部分用量，不是完整费用账单；不显示输入正文，不改变上下文。详见 [输入观测口径和待验用例](../test/context-observation.md)。
- CM-0b另增 `Model attempts (main/child/summary)`，在本Harness重试器内侧记录各次SDK调用，含已读断流usage；按CLI操作归零。三类互斥，不能与上方步骤usage相加。usage覆盖数不等于成功数或完整账单，缓存/费用仍未知。自定义绕开Runtime的模型不自动覆盖，细节见同一观测文档。用户WSL本批定向8项、全量195项通过，均0跳过；真实费用基线仍待验证。
- 启动栏显示用户、工作目录与真实隔离状态。本机直跑不代表有文件系统或网络隔离。
- `Model processing` 表示模型请求阶段；`Tool running` 来自实际工具调用路径。
- 模型/工具状态在同一行原地更新，进入回答或推理时收起；结束汇总调用次数、工具累计耗时（不含模型等待）、more（后续页）、clipped（内容裁剪）、limited（扫描上限）及相同工具/JSON参数重复次数，最多列最近3次read_file实际返回的路径与行号范围。参数JSON规范化后只保存哈希用于计数，不显示参数正文；默认参数省略与显式写出可能不视为相同。各分类可同时出现，计数不是错误判定，重复也可能是合理重试。非终端同样仅汇总，执行细节仍由原事件日志保存。
- `Tool returned` 只表示调用返回，可能是拒绝或待审批，不能证明业务成功或结果已持久化。落盘失败仍通过错误路径报告。
- 工具参数与结果正文不输出到状态栏，子循环不混入父状态栏；摘要与审批直接执行尚无独立进度显示。
- 推理只取 SDK 的 `ReasoningContent`，对应服务商公开返回的 `reasoning_content`，不靠提示词生成替代内容，不代表完整内部思考。
- 真实终端用单行窗口展示最近一段推理，缓存最多120个 rune，按当前终端宽度保守裁剪；回答、工具状态或结束时清除。非终端或 `TERM=dumb` 只留收到推理的提示。
- `NO_COLOR` 关闭颜色但保留单行更新。`/reasoning off` 不改变模型推理配置或计费，也不停止服务端生成。

## 调用链与边界

默认每轮最多20步模型调用（原10步），每步可触发多个工具调用；环境变量 `AGENT_MAX_STEPS` 的正整数配置覆盖默认值。达到上限仍报告任务未完成，不自动无限续跑。若本地.env已有10，需自行更新该配置才能采用20；本次不修改用户.env。

`cmd/agent` 读取 `RuntimeConfigFromEnv`，将工作区工具加入 `ExtraTools` 后通过 `harness.NewConfigured` 装配；`cli.Run` 调用已有审批入口或 `RunAgentTurn`，最终进入 `ownLoop`。CLI 不维护第二套执行循环，也不改变工具执行前的持久化屏障。

Loop 的临时进度通过每轮 context 观察器传给 CLI。Harness 把推理分片和回答分别转发；CLI 对状态和文本输出互斥写入。推理文本不混入最终 assistant 回答历史；SDK 在当前工具循环内的消息拼接与回传保持原逻辑。

默认 `user=1` 是本地存储身份，不是登录认证。同一 user 复用原 Redis 历史；运行锁仅限同一 Harness 实例，不要用同一 user 并发启动多个 CLI/API 进程。取消依赖模型与工具遵守 context，不保证强制终止不合作的工具。

旧“网关保安”人设已改为协作助手。普通抱怨不应触发防御提案，只有明确处置请求才考虑该工具，仍须审批；提示词不是授权执行边界，实际检查由 Harness/guard 承担。

## OpenCode 接入

历史环境变量 `VOLC_ACCESS_KEY`、`VOLC_ENDPOINT_ID`、`VOLC_BASE_URL` 也用于 OpenAI 兼容服务。OpenCode Go 使用 `https://opencode.ai/zen/go/v1`，模型须支持 Chat Completions。

统一模型工厂仅对官方 HTTPS `opencode.ai` 注入本项目 User-Agent 和稳定的 `x-opencode-session`；会话标识由用户与会话确定性哈希生成，同会话不同轮次、子调用与摘要保持一致。Web 共用模型工厂，因此补入其真实 conversation 身份，但不迁移其存储。缺少身份时明确失败，不为相似域名注入头。

## 验证记录

2026-09-23 新增 [上下文基线采集流程](../test/contextbaseline/README.md)，使用相同Harness和模型环境配置，以合成文件、独立owner运行固定题目；真实采集须在WSL显式调用脚本，不自动随普通回归执行。场景报告与CLI统计同口径，尚未采集真实基线。

2026-09-22 累计回归最新验收：用户提供WSL `bash scripts/test-fast.sh -tags knowledgeintegration` 结果，189个顶层用例通过、0跳过，覆盖此前工作区读取/搜索上下文、工具诊断分类、模型输入证据、步数默认20及显式/paste改动的回归待验记录。真实模型对话可见8次调用及定点补读，但引用完整性和300字约束仍有不足，不将模型自述当执行证据。自动多行粘贴与输入编辑优化按用户要求后置，真实终端/paste及运行中取消仍待交互验收。本轮仅记录用户测试证据，未重跑测试、未提交或push。

本地只执行静态检查和固定路径构建，不运行 `go test`。本批 `go vet -tags 'knowledgeintegration rageval' ./...` 与 CLI 构建已通过。

用户此前提供 CLI 6项及较早全量172项通过记录，随后真实对话验证了模型调用、测试编号回忆、工具状态和推理字段。后续新增 OpenCode、工具进度、推理测试及单行窗口改动不被旧结果覆盖。

2026-09-22 提交前，用户在 WSL 执行带 knowledgeintegration 的完整回归，178个顶层用例通过，0跳过，覆盖本批累计代码与测试：

```bash
bash scripts/test-fast.sh -tags knowledgeintegration
```

窄终端、窗口缩放、实际复制粘贴、运行中取消和重启历史仍需实际交互验证。测试清单见 [CLI 验收](../test/cli/README.md)。
