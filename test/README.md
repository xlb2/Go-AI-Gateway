# 测试指南

测试分为确定性回归、数据库集成、终端交互和显式模型评测。常规回归使用进程内 HTTP 假模型，不需要真实模型密钥。

## 运行回归

在仓库根目录的 WSL/Linux 环境执行：

```bash
docker compose up -d redis mysql
bash scripts/test-fast.sh -tags knowledgeintegration
```

Redis 用例依赖真实 Redis；未启动时部分用例会跳过。MySQL 集成测试通过 `knowledgeintegration` 标签启用，依赖不可用时失败。检查结果时同时查看失败和跳过数。

定向运行与详细输出：

```bash
bash scripts/test-fast.sh -run 'CLI|CanceledTurn'
bash scripts/test-fast.sh -tags knowledgeintegration -run Knowledge
bash scripts/test-fast.sh -run Framework -v
```

静态检查可以包含全部评测标签，不会调用真实模型：

```bash
go vet -tags 'knowledgeintegration rageval contexteval' ./...
```

## 覆盖范围

| 目录 | 内容 | 依赖 |
|---|---|---|
| `assembly/` | 依赖装配、工具写入屏障、运行互斥、用量观测与上下文候选 | 假模型；具体用例可能使用 Redis |
| `e2e/` | 完整 Harness 的对话、审批、压缩、恢复、来源回读及取消 | 假模型 + Redis |
| [cli/](cli/README.md) | 终端编辑、粘贴、输入输出错误和终端状态恢复 | Linux PTY；Harness 相关场景在 e2e |
| [workspace/](workspace/README.md) | 文件读取、搜索、路径边界和输出限制 | 临时文件；端到端场景另需 Redis |
| [appserver/](appserver/README.md) | WebSocket RPC、状态查询和取消 | 受控 Runner 与本地 WebSocket |
| [knowledge/](knowledge/README.md) | 导入、会话、来源证据、分段与检索 | MySQL；流式对话使用假模型 |
| [rag/](rag/README.md) | 检索评分、固定语料与三路对照 | 评分单测无服务；实际评测另需 MySQL |
| [contextbaseline/](contextbaseline/README.md) | 固定多轮场景采集与候选输入实验 | 回归使用假模型；真实采集另需模型服务 |
| `fakemodel/` | HTTP/SSE 假模型、请求快照及测试协议 | 测试辅助代码 |

历史包内单测仍参与 `./...` 回归；只运行 `./test/...` 会遗漏它们。

## 数据隔离

知识库集成用例创建随机 `gw_knowledge_test_` 数据库，结束后删除该测试库。账号需要建库、删库权限，默认使用 Compose 开发配置和宿主端口 3307；可用 `KNOWLEDGE_TEST_DSN` 指定独立测试实例。

Redis 测试使用测试身份和隔离键。真实模型采集使用新的评测身份，其日志不会自动删除。测试配置应指向开发实例。

## 真实模型与检索评测

以下入口独立于普通回归，真实模型或 embedding 调用会产生服务用量：

| 入口 | 用途 |
|---|---|
| `bash scripts/context-baseline.sh --real continuity-dev` | 固定对话场景的回答、工具证据与用量采集 |
| `bash scripts/eval-rag.sh` | 关键词开发集评测；只访问 MySQL，不调用真实模型 |
| `bash scripts/eval-rag-compare.sh` | 关键词、向量、混合三路对照；调用真实 embedding |
| `bash scripts/test-real.sh` | 真实模型端到端场景；服务配置见脚本 |

执行成功与质量评价分开：回归验证指定行为，检索评测衡量来源召回，回答的正确性和引用支持需要另行核对。报告保存在已忽略的 `results/` 目录，不随仓库分发。

## 行为契约

- [框架连接与恢复](framework.md)
- [历史来源与分页回读](history-source.md)
- [输入与模型用量观测](context-observation.md)
- [上下文预算候选](context-shadow.md)
- [工具正文与推理字段的模型输入证据](model-evidence.md)
