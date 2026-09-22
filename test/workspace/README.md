# 只读工作区工具验收

最新验收：用户WSL带knowledgeintegration全量189项通过、0跳过，包含本页全部用例，覆盖下方历史待验记录。

2026-09-22 上下文搜索与限制分类：新增2项，静态检查/构建通过；Workspace定向预计7项、累计全量188项待用户WSL。下方旧182为历史待验记录。

2026-09-22：静态检查与CLI构建通过；运行测试待用户WSL，新增4项，带knowledgeintegration全量预计182项。

| 用例 | 行为 |
|---|---|
| TestWorkspaceSearchContext | 默认2行/显式0或1行上下文、相邻窗口去重、命中标记与行号、非法范围拒绝 |
| TestWorkspaceResultLimitsDistinct | 正常分页、长行裁剪、输出预算裁剪、搜索扫描上限分别分类 |
| TestWorkspaceReadAndSearch | 目录可见、UTF-8文本和真实行号、读取分页、字面搜索 |
| TestWorkspaceEmptySearchFields | 空结果显式lines=[]、skipped=0，跳过隐藏文件计数可区分 |
| TestWorkspaceBoundaries | 拒绝绝对路径/父目录/Windows流路径、敏感文件、根外及敏感文件符号链接 |
| TestWorkspaceLimitsAndCancellation | 文件大小/二进制拒绝、长行和搜索结果截断、取消 |
| TestWorkspaceHarnessRead（test/e2e） | 假模型通过真实Harness调用read_file，SDK正文/行号证据与Redis工具对，CLI实际行范围/截断/规范化参数重复计数 |

```bash
bash scripts/test-fast.sh -run Workspace
bash scripts/test-fast.sh -tags knowledgeintegration
bash scripts/run-agent.sh -user 1 -workspace .
```

真实模型验收问题：读一下我的 Harness，解释用户输入如何走到模型和工具执行，请实际查看代码并引用文件路径与行号。

这些测试不代表真实模型回答正确，也不模拟恶意进程并发替换工作区文件、挂载点或硬链接；请仅开放可信项目目录。工具不会修改文件或启动命令。
