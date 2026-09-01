# Cursor Coding Agent 的公开实现与 LingCoWork 借鉴

> 本文依据 Cursor 官方文档和可观察的产品行为整理。Cursor 是闭源产品，模型编排、Patch 算法、
> 上下文选择、索引服务和安全分类器的内部代码不可见；本文只把公开事实与 LingCoWork 的设计推断
> 结合起来，不把推断写成 Cursor 的源码实现。

## 一、先看整体：Cursor 交付的是完整 Coding 闭环

Cursor 官方将 Agent 概括为三个基本组成：

```text
Instructions（系统提示词、Rules 等）
        +
Tools（搜索、读取、编辑、Terminal、MCP 等）
        +
Model（当前任务选用的模型）
        ↓
Agent 持续搜索、修改、执行、观察结果并继续工作
```

对用户而言，真正的产品闭环是：

```text
选择代码仓库
→ 描述任务
→ Agent 搜索并理解代码
→ 生成计划或直接实施
→ 编辑文件并运行命令
→ 用户查看工具过程、Todo 和 Diff
→ 测试修复
→ 提交、PR 或交给用户继续处理
```

因此 Cursor 不是一个单独的“代码生成模型”，而是模型、工具、上下文、安全策略和 IDE 界面的
组合。LingCoWork 应参考这个产品闭环，而不是猜测或复制 Cursor 的闭源内部代码。

## 二、核心模式与局部编辑

### 1. Agent 模式

Agent 模式拥有完整工具能力，适合直接实施：

- 搜索和读取代码。
- 修改文件。
- 执行 Terminal 命令。
- 运行测试、构建和格式化。
- 根据工具结果继续修复。

它本质上仍是多步 Agent Loop：模型给出工具调用，客户端执行工具，把结果作为 observation 返回
模型，直到任务完成、等待用户输入、被取消或达到执行上限。

### 2. Plan 模式

Cursor 的 Plan 模式在写代码前完成：

1. 询问澄清问题。
2. 搜索并研究代码库。
3. 生成可审阅的详细实施计划。
4. 用户通过对话或 Markdown 调整计划。
5. 用户确认后点击实施。

计划默认保存在用户目录，也可以保存到 Workspace。Plan 适合跨文件功能、架构选择和需求不明确的
任务，小改动仍可直接使用 Agent。

从产品语义看，Plan 不是另一个“规划子 Agent”，而是当前会话的受限模式。Cursor 没有公开具体
如何在内部裁剪工具；LingCoWork 可以通过“独立模式提示词 + 工具白名单 + effect 硬限制”实现：

```text
Plan：read / glob / grep / git read / ask
Agent：Plan 的工具 + edit / patch / shell / write
```

用户点击“开始实施”后继续沿用同一会话上下文，避免重新交接需求。

### 3. Ask 模式

Ask 模式用于只读地理解代码，不修改文件。它和 Plan 都可以搜索仓库，但交付物不同：

- Ask 回答“代码在哪里、为什么这样实现”。
- Plan 交付“接下来准备如何修改和验证”。
- Agent 直接执行修改。

LingCoWork 先保持现有 Agent 模式并打通最小 Coding 闭环，再按路线增加 Plan；普通问答继续由
现有主 Agent 承担，是否增加独立 Ask 模式可以等实际使用后决定。

### 4. Debug 模式

Cursor 当前还公开了 Debug 模式：Agent 先提出假设并插入运行时埋点，用户复现问题后，再根据日志
定位和修复。它适合无法仅靠静态阅读复现的问题，但依赖运行时采证、日志管理和清理埋点，不属于
LingCoWork 最小 Coding 闭环。

### 5. Inline Edit 不是主 Chat 模式

Cursor 的 `Cmd/Ctrl+K` Inline Edit 用于对选中代码做局部修改，也可以快速提问。当前官方文档没有
把 Edit 列为与 Agent、Ask、Plan、Debug 并列的核心 Chat 模式。LingCoWork 近期也不需要单独复制
Inline Edit，先把对话驱动的跨文件修改做好。

## 三、代码搜索：Instant Grep 与 Explore 子 Agent

### 1. 精确搜索优先

Cursor 官方将精确符号、变量、错误字符串和正则查询交给 Instant Grep。它是 Cursor 自己的搜索
引擎，官方称其在大型仓库中快于 `ripgrep`，支持正则和单词边界。

这说明 Coding Agent 的首要检索路径仍然是：

```text
已知符号或字符串 → grep
已知路径模式     → glob
找到候选文件     → read
```

LingCoWork 第一版无需复刻 Instant Grep，也无需先做代码向量数据库。使用受预算限制的
`glob + grep + read_file`，已经能够形成可靠闭环。

### 2. Explore 子 Agent

当问题范围很大时，Cursor 可以启动 Explore 子 Agent：

- 使用独立上下文窗口。
- 使用更快的模型。
- 并行执行多次代码搜索。
- 只把相关结论返回主 Agent。

它主要解决的不是“主 Agent 不会搜索”，而是搜索中间结果会迅速占满主上下文。目录列表、无关命中
和大量文件内容保留在 Explore 上下文中，主 Agent 只接收关键文件、符号和调用关系。

Cursor 会在判断广泛搜索有价值时自动使用 Explore，用户也可以直接要求使用。LingCoWork 的
Explore 第一版应更保守：

- 仅提供 `glob`、`grep`、`read_file` 和只读 Git 工具。
- 不允许修改文件和执行有副作用的命令。
- 主 Agent 修改前重新读取关键文件。
- 支持并行探索独立模块，但限制并发、耗时和返回长度。
- 小范围、位置明确的问题由主 Agent 直接搜索。

Cursor 还公开了 Bash 与 Browser 等内置子 Agent。LingCoWork 当前只需要 Explore；命令和浏览器
子 Agent 等出现真实上下文压力后再考虑。

Cursor 的子 Agent 可以前台等待、后台执行或并行运行，也可以配置不同模型和专用提示词。但多个
可写子 Agent 默认共享同一 checkout 时存在互相覆盖风险，可靠并行写入需要 worktree、独立分支或
Cloud Agent 隔离。这也是 LingCoWork 首版将 Explore 限制为只读的原因。

### 3. 代码库索引与忽略规则

除精确搜索外，Cursor 还公开提供自动代码库索引，并支持 multi-root workspace。`.gitignore` 会
影响索引，`.cursorignore` 可以进一步排除不希望进入索引和 Agent 上下文的文件；但忽略规则不能
被当成 Terminal 或 MCP 的安全边界。

官方没有公开索引的完整切块、增量更新、召回、重排和缓存算法，因此不能简单写成“Cursor 用 RAG
理解代码库”。更准确的表述是：Cursor 组合了精确搜索、代码库索引、显式上下文和 Explore，
具体混合检索策略不可见。LingCoWork 第一版继续使用精确搜索，后续有真实召回需求再增加语义索引。

## 四、Todo、Plan 和子 Agent 的客户端协议

Cursor 的 ACP 文档公开了几类 Agent 与客户端之间的交互消息：

- `cursor/create_plan`：阻塞式请求，等待用户接受或拒绝计划。
- `cursor/update_todos`：通知客户端更新 Todo，不要求返回结果。
- `cursor/task`：通知客户端一个子 Agent 任务及其结果。
- `cursor/ask_question`：阻塞式提问，等待用户回答。

公开结构中，Todo 使用稳定 `id`、`content`、`status` 和工具级 `merge`；Plan 还可以携带
`overview`、Markdown 正文、Todo 及可选 `phases`。Cursor 没有确认桌面端的持久化表结构，也没有
确认是否强制最多一个 Todo 处于 `in_progress`，这些应视为 LingCoWork 自己的产品约束。

这些协议不能证明 Cursor IDE 内部完全使用同一套代码，但清楚展示了合理的产品边界：

```text
Plan        = 需要用户作决定的阻塞状态
Todo update = Agent 单向更新的结构化执行状态
Subagent    = 独立任务，结束后向父 Agent 返回摘要
Question    = 暂停关键决策，等待用户补充信息
```

LingCoWork 已经有 SSE、审批和 `ask_user` 中断恢复，可以沿用同一思路：

- 新增 Todo 事件并持久化最新列表。
- Plan 确认作为一种新的 HITL 中断类型。
- Explore 通过 Agent Tool 调用，结果回到主 Agent observation。
- 页面刷新后从数据库重建 Plan、Todo 和子任务状态。

## 五、编辑、Diff 与 Checkpoint

Cursor 官方只确认 Agent 可以提出并自动应用文件编辑，没有公开底层究竟使用哪一种 Patch 算法、
如何做模糊匹配或冲突合并。因此不能简单断言 Cursor 内部就是某个 `apply_patch` 实现。

从产品层可以确认：

- Agent 修改真实工作区文件。
- 用户可以查看变更和 Diff。
- CLI 也提供 Changes 审阅。
- Agent 会在重要修改前自动建立 Checkpoint。
- Checkpoint 保存 Agent 会话中的文件快照，与 Git 分开。
- 用户可以恢复某个 Checkpoint；恢复文件不会删除聊天消息。

必须区分：

```text
Stop / Cancel       → 停止 Agent 继续执行，不撤销已经落盘的文件
Checkpoint Restore  → 恢复一次 Agent 会话中的文件快照
Git Revert / Reset  → 版本控制操作
```

LingCoWork 可以采用：

```text
apply_patch → 带上下文的多 hunk 修改，适合主要 Coding 编辑
write_file  → 新建或完整重写
git_diff    → 展示当前工作区事实
checkpoint  → 后续用于安全恢复 Agent 变更
```

第一版先“直接写入，再查看 Diff”。不要只在前端增加“拒绝 hunk”按钮；真正安全的接受/拒绝需要
Agent 变更基线、Patch 暂存和冲突检测，否则可能覆盖用户进入项目前已有的修改。

## 六、上下文是如何组成的

Cursor 官方列出的 Agent 上下文包括：

- System prompt。
- 工具定义。
- Project、User、Team Rules。
- Skills 描述。
- MCP 工具目录和说明。
- 可用子 Agent 说明。
- 压缩后的历史摘要。
- 当前会话消息和工具结果。

用户还可以通过 `@` 显式附加：

- 文件和文件夹。
- Terminal 输出。
- 其他 Chat。
- 当前 Git 工作区或分支 Diff。
- Browser 上下文。

当固定上下文窗口接近上限时，Cursor 会压缩较早的对话内容。Explore 子 Agent进一步隔离搜索噪声。
这给 LingCoWork 的启示是：

```text
明确指定上下文 → 用户附件
精确事实定位   → grep / glob / read
持久项目约束   → Rules / Skill
长历史         → compaction
广泛探索       → Explore 独立上下文
```

不要把整个仓库一次性塞进主模型，也不要让向量检索替代磁盘与 Git 这两个事实源。

## 七、Terminal、验证与运行控制

Cursor Agent 可以执行并监控 Terminal 命令。测试、构建、Lint 和格式化的输出会重新进入 Agent Loop，
模型根据失败结果继续修改。

Cursor 还支持：

- 将消息排队，等当前任务结束后处理。
- 在下一个工具边界发送即时 follow-up，调整当前任务方向。
- 停止当前运行。
- 使用 Checkpoint 回退文件。
- 将任务交给 Cloud Agent 在独立环境继续运行。

停止、进程树清理、工具取消传播等底层细节未完全公开。LingCoWork 不需要照猜内部实现；现有
`AbortController → cancel API → context.CancelFunc → 进程组清理` 已经是正确基础，后续只需保证
所有本地工具、MCP 和子 Agent 都遵循统一取消信号。

## 八、安全模型

Cursor 的本地 Run Modes 决定工具何时自动运行、进入沙箱或请求批准。当前官方公开的模式包括：

- Auto-review：安全调用自动执行，其余调用进入沙箱或安全审查。
- Allowlist：按确定性白名单控制哪些调用可以自动运行，并可配合沙箱。
- Run Everything：不逐次审批，也不使用沙箱，适合用户明确愿意承担风险的场景。
- Sandbox：限制 Auto-review 或 Allowlist 下命令的文件系统和网络访问。
- 高风险或无法沙箱执行的操作请求用户批准。
- MCP 默认执行前可展示工具名和参数并请求批准。

另外，Checkpoints 提供会话级文件恢复，Git 提供长期版本管理；二者职责不同。

LingCoWork 已有 effect 分类、审批、硬阻断和 Workspace 边界。向 Coding 演进时应继续补：

- 用户已有修改与 Agent 本轮修改分离。
- `.env`、私钥、凭证等敏感文件策略。
- Patch 应用前的旧内容校验。
- 所有 shell 子进程的取消与超时清理。
- 后续可选沙箱，而不是只依赖提示词约束。

## 九、扩展机制

Cursor 把基础 Agent 之外的能力拆成多种扩展：

- **Rules**：持久约束，可按始终、智能、文件模式或手动方式应用。
- **Skills**：按需加载的工作流和领域说明。
- **MCP**：接入外部工具与数据源。
- **Subagents**：隔离上下文并委派专项任务。
- **Hooks**：在工具、文件编辑、压缩和 Agent 生命周期节点执行确定性脚本。
- **Plugins**：打包 Rules、Skills、Agents、Commands、MCP 和 Hooks。
- **Cloud Agents**：在独立 VM 中克隆仓库、构建、测试并产生变更。
- **Automations**：通过定时、代码托管事件、Webhook 等触发 Cloud Agent。

这些机制不应该同时成为 LingCoWork Coding MVP 的前置条件。当前已有 Rules 类提示、Skills、MCP
和本地子 Agent 基础，近期只需要补 Coding Skill 与 Explore 子 Agent。

## 十、界面为什么是产品核心

Cursor 的能力不只体现在工具，还体现在用户始终可以看到并干预执行：

- Chat 展示推理摘要、工具调用和等待状态。
- 编辑器负责文件定位和代码阅读。
- Changes/Diff 负责审阅最终产物。
- Terminal 展示命令、输出和退出状态。
- Plan 页面承载可编辑、可确认的方案。
- Todo 展示当前进度。
- 子 Agent 卡片展示委派任务和返回结果。
- Checkpoint 提供恢复入口。

Cursor 当前还提供面向多任务的 Agents Window，用于管理本地、远程和 Cloud Agent、Diff、
worktree、commit 与 PR。经典 Editor 更强调代码编辑器、侧栏 Chat、Source Control、Terminal 和
Problems。LingCoWork 不需要复制两套壳，但需要先确定自己是“Chat 为主、右侧审阅”，还是进一步
发展为独立的 Agent-first 工作台。

长任务交互还应区分：

- **Queue**：消息排队，当前任务结束后处理。
- **Steer**：在下一个安全工具边界把新指令送入当前任务。
- **Stop**：停止当前任务，不自动回滚文件。

LingCoWork 不必复制 VS Code，但至少要形成：

```text
中间：Chat / Plan / Todo
右侧：Files / Changes / Problems / Terminal
工具卡：点击路径、诊断或命令后跳到右侧对应位置
```

Coding UI 需要单独设计。它不是在现有文件预览旁边简单加一个 Diff 组件，而是要统一“Agent 做了
什么、产生了什么结果、用户下一步能做什么”。

## 十一、LingCoWork 应该复制什么

应该参考：

1. Agent、Plan、Ask 的产品职责分离。
2. 精确搜索优先，广泛探索交给独立 Explore 上下文。
3. 主 Agent 对最终修改负责，子 Agent 返回结论。
4. Plan、Todo、Question、Task 都使用结构化状态。
5. 文件修改后必须进入 Diff 和验证闭环。
6. Rules、Skills、MCP 按需进入上下文。
7. Checkpoint 与 Git 分层处理临时恢复和版本管理。
8. Chat、工具卡、Changes、Terminal 和 Problems 相互跳转。
9. Queue、Steer、Stop 使用不同的运行控制语义。

暂时不复制：

1. Cursor 自研 Instant Grep，第一版使用受控的 `ripgrep` 即可。
2. 全仓语义索引，先用精确搜索验证需求。
3. 完整 IDE、LSP 和多根 Workspace。
4. Cloud Agent、VM 调度和 PR 自动化。
5. 大量通用子 Agent；先只做 Explore。
6. 没有真实基线与冲突检测的 hunk 接受/拒绝。
7. Debug、Design、Cloud 和 Automation 等尚无真实需求的远期能力。

## 十二、对 LingCoWork 的近期落地顺序

```text
Workspace（已完成）
→ Git 分支与脏状态
→ glob / grep
→ git_status / git_diff
→ apply_patch
→ 本轮变更基线
→ Coding Skill
→ TodoWrite
→ Explore 子 Agent
→ 自举完成真实 Bug 修复
→ Changes / Diff Coding UI
→ Plan 模式
→ Problems / Terminal / Checkpoint
```

目标不是做一个功能列表与 Cursor 一样长的产品，而是先保证 LingCoWork 能安全、稳定、可审阅地
修改 LingCoWork 自己。

## 参考资料

- [Cursor Agent 概览](https://cursor.com/docs/agent/overview)
- [Cursor Plan Mode](https://cursor.com/docs/agent/plan-mode)
- [Cursor Debug Mode](https://cursor.com/docs/agent/debug-mode)
- [Cursor Search 与 Explore 子 Agent](https://cursor.com/docs/agent/tools/search)
- [Cursor Subagents](https://cursor.com/docs/subagents)
- [Cursor Agents Window](https://cursor.com/docs/agent/agents-window)
- [Cursor Agent Prompt 与上下文](https://cursor.com/docs/agent/prompting)
- [Cursor Rules](https://cursor.com/docs/rules)
- [Cursor Skills](https://cursor.com/docs/skills)
- [Cursor MCP](https://cursor.com/docs/mcp)
- [Cursor Hooks](https://cursor.com/docs/hooks)
- [Cursor Plugins](https://cursor.com/docs/plugins)
- [Cursor Run Modes](https://cursor.com/docs/agent/security/run-modes)
- [Cursor Cloud Agents](https://cursor.com/docs/cloud-agent)
- [Cursor Automations](https://cursor.com/docs/cloud-agent/automations)
- [Cursor CLI ACP](https://cursor.com/docs/cli/acp)
