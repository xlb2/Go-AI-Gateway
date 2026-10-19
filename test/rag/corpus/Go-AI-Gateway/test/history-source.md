# CM-1a 历史来源存储基础

## CM-2a：有来源的字面检索（待WSL）

CM-1d用户WSL合并定向8/全量206通过，0跳过。新增search_history_sources和SearchSources：query为1..100码点非空白字面字符串，区分大小写，不分词，因此中文短语、单字、代码标识符都可查；不做同义词召回或相关度排名。每页最多扫描50原始事件，最多5个匹配，按新到旧排列。匹配读取持久Content全文，返回首次命中前40码点起最多200码点预览、原seq和来源ID；用户/助手消息、工具结果、摘要可命中，审计与执行控制事件不作为匹配结果。未来版本/损坏事件明确失败。

next_cursor沿用最后扫描事件的来源ID，同query继续更早页，追加不漂移；游标本身不绑定query，换query应从空cursor重新开始。Scanned含不参与匹配的事件，has_more说明还有更早事件，不保证那里有命中；sources=[]且has_more=true必须继续或明确范围有限。external_unsearched统计本页遇到的结构化外存引用，外存正文未展开；旧自然语言外存提示无法可靠识别，不计入该数，不能把0当已查完外存。

扫描50条是事件数量预算，不是字节/内存预算，Redis仍整条读取JSON，大事件的传输和解码开销未优化。未建倒排/向量索引，不改变旧search_memory_archive或默认上下文选择；新增工具定义成本须在后续对照中计入。

| 新回归 | 边界 |
|---|---|
| TestHistorySourceSearchPaging | 中文与代码标识符、命中超过预览开头、来源分页、追加不漂移、跨用户拒绝、查询上限与大小写 |
| TestHistorySourceSearchToolAndCoverage | 真实工具装配、无命中仍可续扫、50事件上限、外存未覆盖计数、命中ID回读 |

默认装配断言同步search_history_sources。三标签go vet通过，WSL合并定向预计10、全量208待验：

```bash
bash scripts/test-fast.sh -run 'HistorySource|SpillOwnedPages|RuntimeDefaultAssembly'
bash scripts/test-fast.sh -tags knowledgeintegration
```

## CM-1d：发现来源与邻居工具（待WSL）

用户已提供默认装配定向1、全量204通过，0跳过，覆盖CM-1c及旧工具数断言修复。

新增ListSources按seq倒序分页，最多5条原始事件、每条200码点预览；next_cursor为上一页最旧来源ID，严格读取其之前的事件，期间追加不改变翻页位置。Lua原子检查日志代次、锚点和读取范围，缺失/重建游标拒绝，空日志返回sources=[]。仍整条加载事件再截预览，不保证单事件内存开销有界。

新增browse_history_sources：无anchor时列最近来源，cursor继续更早页；提供anchor时查看前后各2条事件（正序），不允许同时传cursor。来源ID再交给read_history_source完整分页。邻居模式不返回继续游标，has_more=false只表示没有列表续页，不代表整个日志无其它事件。工具根据可信context绑定owner，不接用户ID参数。

这是原始事件浏览，包含工具/审计等事件，不是用户消息列表或关键词搜索；工具调用自身也会追加日志，向后游标用于避免不停读取新增的自身事件。项目隔离、多代次归档及旧外存读取路径仍不在本接口保证范围。不改变默认历史组装。

| 新回归 | 边界 |
|---|---|
| TestHistorySourceBrowseCursor | 空日志、首尾分页、追加不漂移、跨用户游标拒绝、重建游标失效 |
| TestHistorySourceBrowseTool | 实际工具发现ID→邻居浏览→按ID回读 |

RuntimeDefaultAssembly同步新增工具名称集合。三标签go vet通过，用户WSL待验：

```bash
bash scripts/test-fast.sh -run 'HistorySource|SpillOwnedPages|RuntimeDefaultAssembly'
bash scripts/test-fast.sh -tags knowledgeintegration
```

预计8项/206项；不把204通过当本批证据。新增工具schema也有成本，后续策略对照应重新核算。

## CM-1c：结构化引用与已知来源工具（待WSL）

CM-1b用户WSL定向4、全量203通过，0跳过。新增MemoryDTO.spill_locator，仅自动外存成功时写入；历史投影仍沿用旧Content。新read_history_source由支持ReadSource的session存储装配，owner从调用context取得，支持正文分页和expand_spill显式外存分页。展开时source部分是持久提示的1字符定位预览，expanded部分才是请求的外存正文页；各自offset/next/total不混用。缺结构化引用、旧提示文本、存储不支持归属分页、内容丢失均报错，不解析自然语言定位符。

新增TestHistorySourceToolSpill覆盖自动外存→持久来源→实际工具分页、内容丢失拒绝、拒绝解析用户正文中的定位符。go vet三标签通过，WSL `bash scripts/test-fast.sh -run 'HistorySource|SpillOwnedPages'` 预计5，带knowledgeintegration全量204待验。工具仅支持已知ID；来源检索发现与邻居工具尚未接入，旧load_large_content路径不变。注册新增工具会增加工具定义token，不能复用旧基线固定开销数值宣称节省。

## CM-1b：相邻事件与显式归属外存分页（待WSL）

SourceNeighbors从同代次原始日志原子读取锚点前后各0..5个事件，包含锚点，每条预览最多200码点；返回ID可继续ReadSource翻页。这里是相邻事件，不跳过审计/工具事件，不等于相邻用户消息。缺锚点、代次不符或坏事件明确报错，不返回混代次的部分结果。存储读取仍取得完整事件JSON，预览上限不是Redis内存/传输上限。

spill.RedisStore.ReadPage要求可信owner与标准Redis定位符归属一致，正文每页1..2000码点，丢失外存报错。仍整条加载后切页，不提供版本快照保证；既有LoadText及模型工具尚未切换，不声称旧读取路径已获得相同保护。历史事件里的自然语言外存提示未自动解析，结构化引用和工具接入待后续。

| 新回归 | 边界 |
|---|---|
| TestHistorySourceNeighbors | 首尾范围裁剪、原seq与ID、预览续读、跨用户拒绝、邻居数量上限 |
| TestSpillOwnedPages | 中文/emoji完整拼接、跨用户拒绝、页大小限制、内容删除后明确失败 |

本地三标签go vet通过；用户WSL定向预计4、全量203，尚未运行：

```bash
bash scripts/test-fast.sh -run 'HistorySource|SpillOwnedPages'
bash scripts/test-fast.sh -tags knowledgeintegration
```

下方CM-1a验收记录仅覆盖上一批。

SourceAt(owner, seq, offset, limit)从原始主日志建立来源；ReadSource(owner, id, offset, limit)回读。owner来自可信入口，不从模型参数获得。ID格式history:v1:owner:随机代次:原始seq，对消费者视为不透明。当前日志按user划分，没有独立项目空间；不声称具备项目隔离。子日志不在本接口范围。

每次单条/批量追加通过Lua同时管理generation键与RPUSH；日志为空时更换代次，非空时兼容补建元数据。读取以Lua原子核对代次并LINDEX。旧日志只创建旁路键、不重写事件；代次键丢失时旧ID失效，重新发现后获得新ID，不尝试猜测旧身份。正常重启保留Redis键则ID不变，但未执行真实进程重启验收。任意外部LSET/LTRIM、绕过新版写入口重建日志、回滚Redis整套快照均不在保证范围，历史索引仍依赖只追加契约。Redis Cluster多键槽支持未提供（与当前单Redis部署一致）。

正文按Unicode码点从0分页，limit=1..2000；offset等于末尾返回空页，超过末尾报错。返回事件type/role/interrupted，不能把中断回复当完成回复。只读取持久Content，不展开spill定位符，不包含ToolCalls参数；空正文不代表事件没有其它字段。返回大小有界，但当前仍取完整单条JSON并解码，非存储端流式大对象分页。

| 回归 | 边界 |
|---|---|
| TestHistorySourcePagesAndIdentity | 中文/emoji完整拼接、追加摘要后原seq仍可读、跨用户拒绝、页大小限制、单条及批量写入口、重建日志旧ID失效 |
| TestHistorySourceLegacyAndUnavailable | 旧事件原文不变、中断标志保留、代次丢失拒绝旧ID、坏JSON/未来版本/缺失事件报错 |

本地三标签go vet通过；2026-09-23用户WSL定向2、全量201通过，均0跳过。复验命令：

```bash
bash scripts/test-fast.sh -run HistorySource
bash scripts/test-fast.sh -tags knowledgeintegration
```

上述运行结果为用户提供的WSL证据，本轮未重跑。尚待CM-1后续：相邻事件/消息回读、外存完整内容与归属检查、来源发现和模型工具接入；本批不改变模型上下文选择，不增加模型调用。不把CM-1a当CM-1全部完成。
