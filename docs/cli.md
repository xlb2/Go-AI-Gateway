# CLI 使用指南

CLI 直接复用 Harness 和自研 Agent Loop。最小运行依赖为 Redis 与支持工具调用的 Chat Completions 模型服务；不需要 MySQL、RabbitMQ 或 HTTP 服务。配置示例见 [项目首页](../README.md#快速开始)。

## 启动与安装

从仓库根目录启动：

```bash
bash scripts/run-agent.sh -user 1 -workspace .
```

PowerShell：

```powershell
go build -o bin/gwagent.exe ./cmd/agent
.\bin\gwagent.exe -user 1 -workspace .
```

WSL/Linux 可以安装到用户目录：

```bash
bash scripts/install-agent.sh
export PATH="$HOME/.local/bin:$PATH"
gwagent -user 1 -workspace .
```

安装脚本构建到 `$HOME/.local/bin`，必要时将目录加入 `~/.bashrc`。更新代码后重新执行安装脚本即可更新命令。

`.env` 从进程当前目录加载，环境变量优先。`-workspace` 只决定文件工具的可读范围，不改变配置加载位置。从其他项目目录启动时，需要通过该目录的 `.env` 或环境变量提供配置。

`-user` 默认 1，用于选择 Redis 历史，不是登录认证。同一 ID 不应被多个 CLI/API 进程并发使用；运行互斥只覆盖同一 Harness 实例。

## 输入与操作

| 操作 | 行为 |
|---|---|
| 普通文本 + Enter | 提交一轮对话 |
| `/help`、`/exit` | 帮助、退出 |
| `/paste` | 多行收集；独立一行 `/send` 提交，`/cancel` 丢弃 |
| `/reasoning on`、`/reasoning off` | 显示或隐藏服务商返回的推理字段 |
| `auth:approve` | 查看待审批动作及确认码 |
| `auth:approve <确认码>`、`auth:reject` | 批准或拒绝动作 |
| `auth:status <提案ID>` | 查询执行记录 |
| Ctrl+C | 运行中请求取消，空闲时退出 |

Linux/WSL 的输入和输出均为 TTY 时启用交互编辑：Left/Right、Home/End、Backspace/Delete 编辑内容；多行时 Up/Down 移动光标，单行时浏览本进程最多100条输入历史。历史不跨进程保留。

支持 bracketed paste 的终端会将多行粘贴插入同一个编辑区，按 Enter 后提交一次；粘贴中的命令只作消息正文。输入框随窗口宽度重绘。等待输入时启用 raw mode 和粘贴模式，提交或退出后恢复；Windows 原生、管道和重定向输入使用逐行回退。终端不支持该协议时使用 `/paste`。

交互输入最多64KiB，超出时不提交部分内容；Harness 的独立输入长度限制仍生效。当前不提供全屏 TUI 或 Markdown 渲染。

## 只读工作区工具

路径相对于 `-workspace`，模型参数使用正斜杠。

| 工具 | 行为 |
|---|---|
| `list_files` | 列出单层目录；默认路径 `.`，每页最多100项，offset 翻页 |
| `read_file` | 从 start_line 开始读取，默认80行、最多120行，返回真实行号和下一页位置 |
| `search_files` | 区分大小写的字面搜索；query 最多200字节，上下文默认2行、允许0–5行，最多返回50行（含上下文） |

只读不超过1MiB的普通 UTF-8 文本，拒绝 NUL 二进制内容；单行最多展示1000字节，结果 JSON 最多8000字节。目录扫描最多2000项，搜索累计最多检查2000项、约8MiB文本。工具使用 `os.Root` 限制根目录逃逸，拒绝符号链接、绝对路径、父目录穿越，并跳过隐藏路径、常见凭据及构建目录。

- `has_more`：还有文件行或目录项可翻页。
- `content_truncated`：正文或输出预算导致裁剪。
- `scan_limited`：扫描预算或搜索结果上限触顶。
- `skipped`：排除或无法读取的条目；被跳过目录计一个条目。

兼容字段 `truncated` 是前三者的逻辑或。空搜索结果不代表扫描了整个项目，应结合上述字段判断覆盖范围。文件名过滤不能识别普通源码中的所有秘密，也不提供进程级隔离；请仅开放合适的工作目录。详见 [工作区测试](../test/workspace/README.md)。

## 状态与用量

模型和工具状态在终端单行更新，非终端输出保留汇总。工具汇总包括次数、耗时、分页、裁剪、扫描上限和重复参数计数；不打印工具参数或结果正文。`Tool returned` 只表示调用返回，不保证业务成功或持久化成功。

推理显示读取服务商公开返回的 `reasoning_content`，不混入最终回答历史。终端仅显示最近一段；非终端或 `TERM=dumb` 只提示收到推理字段。开关只控制显示，不改变模型推理配置或计费。`NO_COLOR` 关闭颜色。

输入估算、逻辑步骤 usage 和主/子/摘要 SDK 尝试统计分别展示，避免重复记账。未知缓存和费用不按零处理，详见 [观测口径](../test/context-observation.md)。

## 执行边界

默认每轮最多20次模型调用，可用 `AGENT_MAX_STEPS` 设置；一次调用可产生多个工具请求。达到上限会报告未完成。取消依赖模型和工具响应 context，不保证强行停止第三方执行。

工作区工具只读，MCP 与审批动作各有执行边界。`SANDBOX_BACKEND=local` 是宿主机直跑；需要隔离时配置 Docker 后端及 `REQUIRE_SANDBOX_ISOLATION`。启动信息会显示实际隔离状态。

## OpenCode 兼容配置

`VOLC_ACCESS_KEY`、`VOLC_ENDPOINT_ID`、`VOLC_BASE_URL` 同样用于其他兼容服务。连接官方 HTTPS `opencode.ai` 时，模型工厂注入项目 User-Agent 与稳定的 `x-opencode-session`；相似域名不注入。同会话的主调用、子调用及摘要沿用同一会话标识。模型需支持 Chat Completions 和所需工具协议。
