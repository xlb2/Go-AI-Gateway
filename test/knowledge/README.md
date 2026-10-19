# 知识资料模块

`internal/knowledge` 提供资料库、不可变原文版本、持久会话、读取证据和检索。Web 页面位于 `/knowledge`；CLI 不自动加载本模块的 MySQL 会话或资料工具。Web 页面功能扩展当前暂停，检索与评测独立演进。

## 能力与契约

| 主题 | 说明 |
|---|---|
| 资料导入 | UTF-8 `.md`/`.txt`，单文件最多2MiB，同库按正文哈希去重 |
| 版本与归属 | 用户 → 资料库 → 来源 → 不可变版本，访问逐层校验 |
| [持久会话](conversations.md) | 请求幂等、轮次状态、历史加载与写入屏障 |
| [读取工具](reading-tools.md) | 列表、固定版本分页读取、精确搜索 |
| [读取证据](evidence.md) | 从已提交工具事件生成可回看的片段快照 |
| [分段索引](chunks.md) | 版本化 Unicode 窗口及事务重建 |
| [关键词搜索](search.md) | 当前版本字面定位，分段与原文回退 |
| [向量检索](vector.md) | 显式 embedding、MySQL 向量存储与余弦召回 |
| [混合检索](hybrid.md) | 关键词与向量的 source 级 RRF 融合 |

MySQL 迁移使用同连接 `GET_LOCK` 串行化，成功后记录 schema 版本；DDL 可重入，不承诺 DDL 的事务回滚，不迁移旧 IM 表。原文和文本分段同事务写入；embedding 独立显式构建，不在导入时调用远端模型。

## 运行与测试

知识库集成测试使用独立随机数据库，不调用真实模型：

```bash
docker compose up -d redis mysql
bash scripts/test-fast.sh -tags knowledgeintegration -run Knowledge
```

默认 MySQL 宿主端口3307；测试账号需有建库/删库权限，可用 `KNOWLEDGE_TEST_DSN` 指定开发实例。数据库不可用时集成测试失败。

运行保留的 Web 入口需要完整 API 依赖与配置，包括 MySQL、Redis、RabbitMQ、模型及 `JWT_SECRET`：

```bash
docker compose up -d redis mysql rabbitmq
bash scripts/run-api.sh
```

随后访问 `http://localhost:8080/knowledge`。接口注册以 API 实现为准；最小 CLI 的依赖要求更少，见 [项目首页](../../README.md)。

## 页面预览

```bash
node test/knowledge/preview.cjs --fixtures
```

脚本输出随机本地地址，使用内存资料和固定回复预览交互，不连接数据库或真实模型。无 `--fixtures` 时仅预览静态资源；两者都不作为生产服务或持久化证明。

当前 Markdown 按纯文本展示。资料更新/删除、富文本、后台索引任务及自动 RAG 装配尚未提供。检索实验方法见 [RAG 评测](../rag/README.md)。
