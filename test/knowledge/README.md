# K-1 第一批：资料导入与原文阅读

**分段索引增量：** 新导入同步构建版本化片段，原文和片段同事务；重建、状态查询与旧数据处理见 [chunks.md](chunks.md)。4项新增回归待WSL，预计全量163；此前关键词全量159已由用户验证。

**最新状态：** 持久会话和轮次执行日志已由用户WSL全量150验收，0跳过，覆盖下方历史待验记录。新增资料只读工具已实现，3项新回归待验，全量预计153；工具链及边界见 [reading-tools.md](reading-tools.md)。

**当前增量：持久会话已实现、运行待验。** 新增 v2 表、服务端历史与网页会话列表，刷新读取已保存记录；下方“只存页面内存”是历史行为。请求幂等、写入屏障、状态边界及 5 项新测试见 [conversations.md](conversations.md)。本次定向 8/全量 147 待用户 WSL，不沿用旧 142 作为当前通过证据。

**最新回归证据（2026-09-21）：** 用户 WSL KnowledgeChat 定向 3、带 knowledgeintegration 全量 142 通过，0 跳过。下文 Go 回归待验为历史记录；真实模型质量与浏览器上传/停止仍未由这些结果证明。

## 对话工作台页面验收（2026-09-21）

页面改为全屏聊天工作台，右侧展开资料、左侧选择库，手机导航抽屉。新增复制消息、Enter 发送（Shift+Enter 换行、输入法组合中不发送）、上传状态；生成中禁用输入，避免用户新草稿被完成回调清空。聊天仍只存页面内存。

运行 `node test/knowledge/preview.cjs --fixtures` 可启动随机本地端口的模拟页面；使用任意满足长度要求的模拟账号登录。fixtures 只有内存资料和固定回复，不连接 MySQL/Redis/模型，不证明生产上传、身份隔离或模型质量。默认不带参数仍为纯静态资源预览。不得将此入口作为生产服务。

已用浏览器验证：桌面 1440×900，手机 390×844、320×740；模拟发送、选附件、原文读取、资料库切换，手机导航开关，未见横向溢出；控制台无脚本错误。停止验证遭自动审批拒绝，未计为通过。真实 API 重建后仍需检查上传、停止、刷新清空与重新登录。此次 UI 改动未新增 Go 测试；前一轮 3 项 Chat 回归仍待 WSL。

## 基础对话增量（2026-09-21）

此前 5/139 已由用户 WSL 验证。新增 Chat 两项契约测试及一项带 knowledgeintegration 的真实数据库 HTTP 流式测试，合计 Knowledge 8、Chat 3、带 tag 全量预计 142，运行待验。静态检查通过不代表模型或浏览器交互通过。

```bash
scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeChat
scripts/test-fast.sh -tags knowledgeintegration
```

重建 API 后进入 /knowledge 的“对话”：创建/选择库，上传小型 .md/.txt 或选择已有资料，询问其中事实，检查流式回复；停止生成后应保留未完成标记，下一轮不带入残缺回复。切换库不得混入旧对话，退出不得留下旧账号聊天。附件持久存入资料库，对话仅保留当前页面内存，刷新清空；无服务端恢复、自动检索或工具执行。

上传仍为 2 MiB 上限；一次直读最多 4 份附件，含元数据 JSON 64 KiB 上限，超限返回明确错误。历史最多 24 条、16000 rune；长对话需要新建。模型配置沿用现有 RuntimeConfig，回复按纯文本展示。测试用假模型不证明真实回答质量，取消与完整页面交互须真实运行核验。

本批包含 MySQL 持久化、资料库所有权、2 MiB UTF-8 .md/.txt 导入、同库正文哈希去重、首次不可变版本、列表游标及原文读取，页面位于 `/knowledge`。

Markdown 当前按纯文本安全显示，未实现富文本渲染、标题目录、专题、笔记、更新、删除、RAG 或全文搜索。不把这个切片标为整个 K-1 完成。

## 验证

| 顶层用例 | 验证内容 |
|---|---|
| TestKnowledgeDocumentValidation | 编码、空文本、BOM、上限、标题、格式；保留合法正文 |
| TestKnowledgeHTTPRejectsMalformedUpload | 错误 ID、非 multipart 请求不触达存储、不 panic |
| TestKnowledgePageHeaders | 内嵌页面与资源可返回，携带 CSP |
| TestKnowledgeMySQLImportAndIsolation | 重复迁移、真实并发去重、原文往返、跨用户/库拒绝、版本写失败时事务回滚 |
| TestKnowledgeMySQLHTTPFlow | 真实数据库的创建库、multipart 导入、JSON 原文读取 |

后两项使用 `knowledgeintegration` build tag，普通快速回归不编译它们，不通过 SKIP 冒充验收。运行时创建随机 `gw_knowledge_test_` 数据库，结束仅删除该测试库；需要测试账号具备建库和删库权限。默认连接本地 Docker MySQL 的 3307 端口，账号使用 compose 中的开发配置。自定义连接使用 `KNOWLEDGE_TEST_DSN`，不要指向生产服务器。

在 WSL 中执行：

```bash
cd "/mnt/e/agent study/Go-AI-Gateway"
docker start im_mysql im_redis
scripts/test-fast.sh -tags knowledgeintegration -run Knowledge
scripts/test-fast.sh -tags knowledgeintegration
```

MySQL 启动后需要等到可接受连接；不可用时集成测试失败，不跳过。预期定向 5、全量 139（含两个 MySQL 用例），普通无 tag 全量为 137；实际结果为准。

## 真实页面

本机限制下没有执行 Go 服务或测试。静态检查含集成测试；浏览器只验证静态登录页面桌面/移动端排版。动态浏览器执行工具被自动审批拒绝，未将导入阅读流程标为浏览器实测。

API 仍沿用现有整体启动，需要数据库、Redis、RabbitMQ 和 Harness 配置。新资料模块自身不调用模型或 Redis，不等于现有 API 可在缺少全局依赖时启动。

```bash
docker start im_mysql im_redis im_rabbitmq
scripts/run-api.sh
```

确认 `.env` 有新 JWT_SECRET，DB_DSN 使用映射到宿主机的 3307。浏览器打开 `http://localhost:8080/knowledge`，注册/登录，创建库，选择一份 Markdown，导入并读取。重复导入相同正文应提示已存在；重新登录后记录仍在。切换其他用户不能看见原用户资料。

初次 API 启动会执行 knowledge v1 显式建表步骤并记录 `knowledge_schema_versions`；同连接 MySQL GET_LOCK 防并发迁移。MySQL DDL 不承诺事务回滚，步骤可重入，全部成功后才写完成记录。不迁移或覆盖旧 IM 表。

`preview.cjs` 仅是开发期间静态页面检查入口，没有 API，不能用于登录或验收持久化。
