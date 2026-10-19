# 已读取资料的证据片段

证据链接来自已提交的工具调用和结果，不从模型生成的引用文字推断。

## 投影与读取

会话详情的每个 turn 包含 evidence 元数据：event_id、turn_id、source_id、version_id、version、title、offset、next_offset。服务端按事件顺序匹配 read_source 调用与结果，校验调用ID、工具名、版本、偏移与字符长度；失败、孤立或不匹配事件不产生证据。

详情不包含原文。点击证据时请求：

```text
GET /api/v1/libraries/:library_id/conversations/:conversation_id/turns/:turn_id/evidence/:event_id
```

服务端逐层校验用户、库、会话、轮次、事件，返回当时工具读取的片段快照及元数据；不存在或不属于当前范围返回404，响应使用 `Cache-Control: no-store`。

原文来自已提交工具结果，不随资料更新漂移。每次片段读取保持独立事件 ID，不合并成“整份已读”，也不开放整个执行日志或完整历史版本浏览。

## 页面语义

来源显示为“已读取资料”，包含版本与范围。内部坐标零起点、右侧不含，页面显示为从1开始的包含式范围。正文使用 textContent，旧异步请求不能覆盖新选择；查看证据时不能将片段误作最新整份资料附件。

来源在轮次结束后的历史重载出现，不逐工具实时推送。失败/取消轮次也可能存在已读片段；用户附件与主动读取来源分开。“已读取”证明工具结果存在，不证明回答正确或每句话均有原文支持。

## 测试

```bash
bash scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeEvidence
```

覆盖快照稳定性、模型伪引用排除、元数据与正文分离、HTTP归属以及孤立/失败结果过滤。当前从会话事件投影，不提供独立来源索引或大规模性能保证。
