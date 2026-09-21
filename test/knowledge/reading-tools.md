# 资料只读工具：调用与验收

本批将业务工具接到 Web 使用的 ownLoop，而不是切换到旧用户级 Harness。不会注册通用命令执行或资料写入能力。

后续增量：新增search_sources精确关键词定位工具，仍共享本轮64KiB结果预算。候选不返回正文，须再read_source取得依据；契约与待验范围见 [search.md](search.md)。

## 调用链

```text
JWT 用户 → 校验库/会话 → BeginTurn
  → 本轮 NewReadingTools(store, owner, library)
  → 模型请求 list_sources → tool/call落库 → 列表 → tool/result落库
  → 模型请求 read_source → tool/call落库 → 固定版本原文 → tool/result落库
  → 模型回答 → completed落库 → done
```

用户身份与资料库由闭包绑定，JSON 参数没有 owner/library 字段；未知字段、非对象及尾随JSON均拒绝。每次调用重新校验库归属，读取还校验 source 属于库、version 属于 source。不能通过知道其他资料ID绕过归属校验。SQL错误不直接返回模型。

## 工具契约

| 工具 | 参数 | 输出 |
|---|---|---|
| list_sources | after，可省略，首次0 | items，最多20项；next_after=0表示结束 |
| read_source | source_id、version_id必需；offset默认0 | 标题、版本号/ID、content、offset、next_offset、eof |

读取以Unicode字符而非字节分页，每页最多4000字符。version_id 使用列表返回的 current_version_id，后续分页保持相同ID；即使当前版本变更，也继续读取原来的不可变版本。它是枚举和原文读取，不是关键词或向量检索。

同一轮两个工具共享64KiB序列化结果预算；预算不足明确返回错误，不把截断内容伪装完整资料。预算包含JSON编码开销，独立于附件64KiB和历史256KiB限制。最多8个模型步骤，仍受请求2分钟超时约束。工具仍可能在最后一步执行后因步数耗尽返回失败，不伪造完整回答。

循环将工具调用和结果写入当前 turn 的事件表；写失败沿已有屏障终止。业务错误作为工具结果交回模型供其改正参数或说明无资料。来源标题、版本和范围是工具返回的证据，但回答中的文字引用仍由模型生成，不等于已核验引用。

## 当前边界

- 后续来源展示增量已实现：主动读取的资料由工具日志投影为可点击片段；运行回归待验，见 [evidence.md](evidence.md)。用户附件标注仍与主动读取来源分开。
- 下一轮只载入已完成问答和原附件快照，不重放工具结果；提示词要求需要原文证据时重新读取。
- 长原文分页，目前数据库仍取出该版本全文再分页，单文档沿用2MiB导入限制；不是流式数据库读取。
- 工具调用次数/参数总量未另设独立额度；当前限制是步骤、超时和成功结果总字节预算。
- 没有接审批、子任务、自动压缩、任务恢复或完整会话Runtime，没有新增数据库迁移。

## 验证

新增3项顶层集成测试位于 tools_test.go，覆盖归属/版本/分页/预算，以及假模型真实HTTP和MySQL工具循环、工具事件写入故障。数据库沿用独立随机测试库。

```bash
scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeReadingTools
scripts/test-fast.sh -tags knowledgeintegration
```

预计3/153，运行待用户WSL；go vet通过不等于运行通过。真实模型验证需重建 API 后在资料库内不选附件提问，确认模型确实调用工具及事件落库，不能仅凭回答文字认定已读取。
