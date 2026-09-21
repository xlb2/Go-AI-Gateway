# 已读取资料与原文核对

本批将执行证据展示在回答旁，不将模型输出的引用文本转换成可信链接。

## 契约

会话详情的每个turn新增evidence数组，含event_id、turn_id、source_id、version_id、version、title、offset、next_offset。详情不附带原文，避免每次刷新重复传输全部工具结果。服务端按事件顺序匹配read_source调用与结果，核对调用ID、工具名、source/version/offset、字符长度；失败、孤立和不匹配结果不产生来源。每个读取片段保留独立事件ID，不合并成“整份文档都已读”。

```text
已提交 tool/call + tool/result
  → 会话详情中的来源元数据
  → 点击 GET /api/v1/libraries/:library_id/conversations/:conversation_id/turns/:turn_id/evidence/:event_id
  → 校验用户→库→会话→轮次→事件
  → 返回当时的片段快照
```

接口返回相同元数据及content，Cache-Control:no-store。不存在或不属于当前范围均404，不返回整个执行日志。无新表或迁移。原文来自已提交工具结果，资料后续更新不会替换历史片段；当前只提供当时读取的部分，不提供完整历史版本浏览。

## 页面行为

来源标为“已读取资料”，标题后显示版本和字符范围；内部偏移为零起点、右侧不含，页面显示从1开始的包含式范围。点击右侧原文面板显示保存的片段，纯textContent渲染，不执行HTML。原文与来源共用请求序号，较早请求不能覆盖后来选择；切库或切会话后忽略旧返回。查看片段时禁用“附加到对话”，避免无意附加最新整份版本。

来源在流结束后的历史重载及刷新时出现，不随每次工具调用实时推送。失败/取消轮次也可能有已读取来源，轮次状态照常展示。“已读取”只证明有读取证据，不证明回答正确或每一句都得到原文支持。用户手动附件沿用原标注，不混成工具读取证据。

## 验证与限制

3项集成测试在evidence_test.go，覆盖快照不漂移、模型伪引用不参与、元数据与正文分离、HTTP范围校验、孤立/失败/不匹配结果过滤。历史工具链回归仍包含在全量中。

```bash
scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeEvidence
scripts/test-fast.sh -tags knowledgeintegration
```

预计定向3、全量156，WSL待用户运行。本地go vet带tag与JS语法通过。

独立Node fixture预置一条带来源对话。Playwright验证1440×900与390×844截图、320宽无横向溢出、打开/关闭并重开来源、焦点进入原文标题；含script标签的正文为纯文字且无script子节点，控制台零错误。此模拟服务不连接真实账号、MySQL或模型，不能替代真实页面端到端。

本批不是检索/RAG或答案引用评估。查询暂从所选会话的轮次事件中投影来源，尚未建立独立来源索引；当前规模沿用每会话100轮和每轮工具输出预算，性能深化后置。
