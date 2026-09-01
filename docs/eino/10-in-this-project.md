# 10 · eino 在本项目的落地

> 前 9 章讲 eino 框架本身，这一章讲**本项目怎么用它、被它约束成什么样、以及刻意不用它的哪些部分**。
>
> 面试里项目部分的所有技术决策，追到底基本都会落到这一章的某个约束上。
>
> 校准时间：对齐 eino v0.9.1 与当前代码。

---

## 一、当前架构全景

### 拓扑：1 个 supervisor + 4 个 sub-agent

```
Runner (EnableStreaming, CheckPointStore = SQLite)
└── supervisor (ChatModelAgent, root)
    ├── baseTools...                      // 文件 / shell / 浏览器 / 检索 / 联网
    ├── deep_research      (deep.New       → NewAgentTool)
    ├── job_search         (ChatModelAgent → NewAgentTool)
    ├── resume_analyzer    (ChatModelAgent → NewAgentTool)
    ├── question_planner   (ChatModelAgent → NewAgentTool)
    └── MCP 工具...                        // 不在静态表里，每轮中间件注入
```

代码在 `internal/agent/adk_agent.go`。四个 sub-agent 全部通过 `adk.NewAgentTool` 挂成工具，
**不用 `SubAgents` 列表，也不用 `TransferToAgent`**——这个选择后面单独讲。

### 用到的 eino ADK API

| API | 用途 |
|---|---|
| `adk.NewChatModelAgent` | supervisor + 3 个 sub-agent |
| `deep.New`（`adk/prebuilt/deep`） | deep_research，预制的 DeepAgent 模式 |
| `adk.NewAgentTool` | sub-agent → 工具 |
| `adk.NewRunner` | 运行入口 |
| `runner.Run(..., adk.WithCheckPointID)` | 起一轮 |
| `runner.ResumeWithParams` | HITL 恢复 |
| `adk.ChatModelAgentMiddleware` | 每轮改 instruction / tools |
| `compose.ToolMiddleware` | 包每次工具调用 |
| `adk.AsyncIterator[*adk.AgentEvent]` | 事件消费 → SSE |
| `adk.InterruptSignal` / `adk.ResumeParams` | 中断与恢复 |
| `adk.CheckPointStore` | 中断状态持久化 |

### 模型层

生产用的是 **eino-ext 的 openai adapter 接 DeepSeek**（`internal/agent/llm/llm.go`），
不是 claude。claude adapter 只在 `test/` 下的集成测试里出现。

thinking 模式通过 `ExtraFields` 透传 DeepSeek 的 `{"thinking":{"type":"enabled"}}`，
reasoning token 走 `Message.ReasoningContent` 回来，SSE 里是独立的 `thinking` 帧。

---

## 二、为什么走 ADK，不走 Graph

eino 有两条路线（见 00 章）：`compose` 的 Graph 路线做确定性流程，
`adk` 的 Agent 路线做开放式任务。

本项目落在 Agent 这一侧，理由是任务形态：

- **输入非结构化**：用户说的是自然语言，还可能带图片附件
- **步数不确定**：一个「分析我的简历再出套题」可能是 3 步也可能是 30 步
- **过程和结果同等重要**：用户要能看到 agent 正在做什么，不是等一个黑盒结果
- **需要跨执行的状态**：多轮对话、中断恢复

Graph 路线适合的是「输入结构化、流程预设、只关心最终结果」的场景，
比如一条固定的 RAG 管线。官方推荐的融合点是**把 Graph 封装成 Agent 的 Tool**，
本项目没有走到这一步——工具都是直接的 Go 函数，没有复杂到需要用 Graph 编排的子流程。

---

## 三、AgentAsTool：为什么不用 Transfer

eino ADK 提供了两种挂 sub-agent 的方式：

**Transfer（`SubAgents` + `TransferToAgent`）**：控制权真的转移过去，
sub-agent 接手整个对话，共享同一份消息历史。

**AgentAsTool（`NewAgentTool`）**：sub-agent 被包成一个工具，
supervisor 调它、拿返回值，控制权始终在 supervisor 手里。

选 AgentAsTool 的三个理由：

**上下文隔离**。sub-agent 干活会产生大量中间过程——deep_research 可能调二十次工具、
反复读写文件。这些如果进主对话历史，几轮之后上下文就爆了。
包成工具之后，sub-agent 有自己的上下文，只把最终结果作为 tool result 返回给 supervisor。

**UI 天然的父子层级**。工具调用在前端本来就是一张卡片，sub-agent 的事件挂在这张卡片下面展开，
结构和用户心智一致。Transfer 模式下没有这个天然的锚点。

**避免历史爆炸**。这是前两条的直接后果：主线历史里 deep_research 那一轮只占一次
tool_call + 一次 tool_result 的位置，不管它内部跑了多少步。

代价是 supervisor 拿不到 sub-agent 的中间推理，只能看结果。
对这个场景是可接受的——supervisor 的职责本来就是派发和汇总，不是复核。

---

## 四、eino 的三层切面（本项目最值得讲的结构）

这是理解整个项目代码组织的钥匙。本项目在三个层次上介入，各管各的事：

| 层 | 本项目用的 eino 类型 | 时机 | 用来做 |
|---|---|---|---|
| **Agent 级** | `adk.ChatModelAgentMiddleware` 的 `BeforeAgent` | 每轮 agent 运行开始 | 注入 workspace 状态、技能索引、MCP 工具 |
| **工具级** | `compose.ToolMiddleware` | 每次工具调用前后 | 审批拦截、错误转结果 |
| **观测级** | `AgentEvent` 流 | 事件产生时 | SSE 推送、落库收集 |

> 上表描述的是**本项目的用法**，不是 eino 能力的全貌。
> `adk.ChatModelAgentMiddleware` 这一个接口实际横跨三个粒度、共 9 个钩子
> （agent 生命周期 / 模型调用 / 工具调用），本项目只用了其中的 `BeforeAgent`。
> 完整钩子表见 [00 §3](00-overview.md)。
>
> 值得知道的是：ADK 也提供 `WrapInvokableToolCall` 等工具级钩子，
> 理论上审批可以挂在那里。选 `compose.ToolMiddleware` 的实际原因是
> `deep.New` 和 `NewChatModelAgent` 的 `ToolsNodeConfig` 都直接收它，
> 五个 agent 能共用同一个中间件值；而 ADK 的工具钩子只存在于 ChatModelAgent 上。

### Agent 级：`BeforeAgent`

三个中间件，都只实现 `BeforeAgent`：

```go
// internal/agent/runtimectx/
SkillsIndexMiddleware   // 所有 5 个 agent —— 把技能索引拼进 instruction
WorkspaceMiddleware     // supervisor / resume_analyzer / question_planner
DynamicToolsMiddleware  // 仅 supervisor —— 追加 MCP 工具
```

为什么选 `BeforeAgent` 而不是 `BeforeModelRewriteState`，代码里写了理由：

```11:17:internal/agent/runtimectx/middleware.go
// WorkspaceMiddleware 每次 agent 运行开始时把 workspace 状态拼进 instruction。
// 用 BeforeAgent hook（而非 BeforeModelRewriteState）：一次运行 = 一个用户 turn 的
// 响应循环，运行开始时的 workspace 状态就够了
```

粒度选择的依据是**这个状态在一轮之内会不会变**。
workspace 状态和技能索引在一轮之内不会变，所以每轮算一次就够；
如果选更细的钩子，同一轮里的每次模型调用都要重算，纯浪费。

### 工具级：`ToolMiddleware`

每个 agent 的 `ToolsNodeConfig` 上挂两个，**顺序有硬要求**：

```138:141:internal/agent/adk_agent.go
				ToolCallMiddlewares: []compose.ToolMiddleware{
					approvalMW,
					toolerr.Middleware(),
				},
```

`approval` 必须在外层。因为审批是靠抛一个 `tool.Interrupt` 的哨兵错误来实现的，
如果 `toolerr` 在外面，它会把这个哨兵当成普通工具错误包成「看起来成功的 tool result」，
**中断就被吃掉了**，审批卡永远不会弹出来。

代码注释里还提了一句：`toolerr` 现在也会放行中断错误，
但正确的顺序让我们不必依赖这层保险。**这是很好的工程习惯——
有兜底不等于可以不管顺序，兜底是给意外用的，不是给设计用的。**

### 观测级：AgentEvent

`ConsumeADKEvents`（`internal/stream/adk_handler.go`）消费迭代器，
把事件翻译成 SSE 帧。

### 为什么不用 eino 的 Callback

eino 还有一套 `callbacks` 机制（06 章），本项目**定义了但没用**
（`internal/stream/sse_handler.go` 里的 `NewSSEHandler` 是死代码）。

原因很具体：**Callback 拿不到 `AgentName`**，无法区分事件来自 root 还是某个 sub-agent。
而 `AgentEvent` 天然带这个字段，这是前端做父子层级渲染的必要信息。

这是 ADK 相对老路线（react agent + callback 观测）的一个实实在在的优势，
也是当初从 `flow/agent/react` 迁过来的动机之一。

---

## 五、图冻结与动态工具（核心约束）

### 约束是什么

eino 的图**编译后不可变**，具体到 ChatModelAgent 上的表现是：
第一次运行时用 `sync.Once` 冻结工具表和交给模型的 `toolInfos`。
构造时传进去的工具，此后就定型了。

这个设计对确定性是好事——运行时图不会变，行为可推理。
但它和「工具列表会在运行期变化」的需求直接冲突。

### 冲突场景：MCP

MCP server 可能在**应用启动之后**才连上，因为 OAuth 授权本身就是交互式的——
用户是在应用已经跑起来之后才点「授权」的。

如果在构造 agent 时把 MCP 工具切片传进去，那一刻还没有任何 MCP 工具，
授权完成之后模型也永远看不到它们。

### 解法

```10:23:internal/agent/runtimectx/dynamic_tools.go
// DynamicToolsMiddleware 每次 agent 运行开始时把当前可用的外部工具追加进工具表。
//
// 为什么必须走中间件：eino 在 agent 第一次运行时用 sync.Once 冻结工具表和
// 交给模型的 toolInfos（adk/chatmodel.go 的 prepareExecContext），构造时传进去的
// 工具此后就定型了。但 MCP 服务器可能在启动之后才连上——OAuth 授权本身就是
// 交互式的
//
// 而只要 agent 挂了任意一个 handler，eino 每轮都会从冻结的基准工具表复制一份
// runCtx.Tools 交给 BeforeAgent 改，改完重新跑 genToolInfos 并用 WithToolList
// 覆盖 ToolsNode 的派发表。
```

关键在后半段——这是个**框架行为的利用**：只要 agent 挂了任意一个 handler，
eino 每轮都会从冻结的基准表复制一份 `runCtx.Tools` 给 `BeforeAgent` 改，
改完重新生成 toolInfos 并覆盖派发表。

所以中间件里就一行：

```36:48:internal/agent/runtimectx/dynamic_tools.go
func (m *DynamicToolsMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	// ...
	runCtx.Tools = append(runCtx.Tools, extra...)
	return ctx, runCtx, nil
}
```

传进来的是一个 `func(context.Context) []tool.BaseTool`**函数**而不是切片——
函数每轮被调用一次，切片会被冻结。这个类型选择本身就是约束的产物。

### MCP 只给 supervisor

```56:60:internal/agent/adk_agent.go
// Only the root agent, because a remote server's tool set is unbounded and
// unrelated to what a sub-agent was built to do — handing job_search a
// filesystem server would grow every sub-agent's prompt with tools none of
// them were designed around. The approval gate would still hold, so this is
// about exposure and context cost rather than a hole.
```

理由讲得很清楚：远程 server 的工具集是**无界**的，而且和 sub-agent 的职责无关。
给 job_search 塞一个文件系统 server，只会让它的提示词膨胀一堆用不上的工具。

而且明确说了**这不是安全洞**——审批门依然拦得住，这纯粹是暴露面和上下文成本的考虑。
能把「这是成本问题不是安全问题」分清楚，比笼统说「为了安全」准确得多。

### 同一个约束的另一处体现

技能索引也是每轮注入而不是构建时烤进 instruction：

```113:115:internal/agent/adk_agent.go
	// Skills 索引改成每轮注入（而不是构建时烤进 instruction）：Skill Hub 装完
	// 的技能下一轮就能出现在索引里。所有 agent 共用一个实例。
	skillsMW := runtimectx.NewSkillsIndexMiddleware(skillLoader)
```

这正是从实习项目移植技能机制时踩到的那个坑：klingwork-app 那边技能索引本来就是
每轮扫描的，装完自然生效；eino 这边 instruction 在构造时固定，
必须显式补出「每轮重新拼接」这个语义。

**移植一个功能，难点常常不在功能本身，而在它隐含的架构前提。**

---

## 六、HITL：中断与恢复

### 三层配合

**中间件层**（`internal/approval/middleware.go`）：判断这次调用需要审批，
抛 `tool.Interrupt(ctx, info)` 哨兵错误。

**框架层**（eino 内部）：识别哨兵 → 保存 checkpoint → 生成
`AgentEvent.Action.Interrupted` → 迭代器终止。

**应用层**：发 SSE `approval_required` 帧 + 记录待审批；用户批准后
`runner.ResumeWithParams(checkpointID, &adk.ResumeParams{Targets: {interruptID: decision}})`，
中间件里用 `tool.GetResumeContext` 拿到决定，继续或拒绝。

### checkpoint 已经是持久化的

```256:260:internal/agent/adk_agent.go
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           supervisor,
		EnableStreaming: true,
		CheckPointStore: checkpoint.NewDBStore(checkpointRepo),
	})
```

早期用的是 eino 默认的内存 store，重启就丢中断态。现在换成了 SQLite 实现的
`checkpoint.DBStore`，**进程重启后待审批的任务能从断点继续**。

配套的是 `approval.PendingStore` 在启动时从数据库恢复 UI 侧的待审批元数据——
eino 的 checkpoint 存的是框架内部状态（字节流），
「这个中断在等什么、要展示什么给用户」是应用侧的信息，两边分开存、启动时各自恢复。

checkpoint id 直接用 **conversation id**，因为一个对话同时只有一个活跃 run，天然唯一。

### 两个实现细节

**gob 注册**：checkpoint 序列化走 gob，所有会进 `ResumeParams.Targets` 的类型
（审批决定、问答答案、中断信息）都要 `gob.Register`，否则恢复时反序列化失败。
集中在 `internal/stream/gob.go`、`internal/hitl/types.go`。

**sub-agent 的中断会以错误形式冒泡**。这是个真实的坑：
sub-agent 被包成工具之后，它内部的中断不总是以 `Action.Interrupted` 出现，
有时是以 `ev.Err`（`*adk.InterruptSignal`）的形式冒到 root。
所以事件消费层两条路都要认：

```84:99:internal/stream/adk_handler.go
//   - ev.Err != nil 且能提取出中断信息 → 同样按中断处理
```

只靠 `compose.ExtractInterruptInfo` 不够，还实现了 `signalToContexts` 来处理这种形态。

---

## 七、事件流到 SSE

### EmitInternalEvents

```245:247:internal/agent/adk_agent.go
			// Bubble up sub-agent (deep_research) internal events to the
			// Runner's iter so the UI can show real-time progress.
			EmitInternalEvents: true,
```

只在 supervisor 上开。作用是让 sub-agent 的内部事件冒泡到 root Runner 的迭代器。

**为什么必须要**：deep_research 可能跑几分钟。用户不能接受「发出请求 → 几分钟黑盒 → 一大坨结果」。
开了这个开关，前端能实时看到 sub-agent 在读什么文件、调什么工具。

### 父子关联

光有事件还不够，前端要知道**这个 sub-agent 事件该挂在哪张卡片下面**。

做法是维护一个 `sub-agent name → root tool_call_id` 的映射：
supervisor 说「我要调 deep_research，call_id=X」时记下，
此后所有 `AgentName="deep_research"` 的事件发 SSE 时带上 `parent_tool_call_id=X`。

恢复时这个映射要重建——`rebuildOpenToolCalls` 从数据库历史里把它恢复出来，
否则 resume 之后的 sub-agent 事件会挂不上父卡片。

### 帧类型

`thinking` / `text` / `tool_call` / `tool_result` / `usage` /
`approval_required` / `question_required` / `error`。

---

## 八、上下文管理刻意放在 eino 之外

长对话压缩**不是 eino 中间件**，跑在 `runner.Run` 之前，在 `ChatService.Start` 里：

```
用户消息进来
  → compactor.MaybeCompact(convID)      // 超阈值就把老消息折叠成摘要
  → toSchemaMessages(convID, prior)      // 从数据库投影出 []*schema.Message
  → runner.Run(ctx, msgs, ...)           // eino 从这里才接手
```

**为什么不做成中间件**：压缩的操作对象是**数据库里的持久化历史**，不是 eino 的内存消息列表。
折叠一次要落库、要标记哪些行被折叠了、要保证下次重启后还认得。
这是应用层的持久化关注点，塞进框架中间件反而会让「谁是历史的真相来源」变得含糊。

### 这个选择的边界（要能主动说出来）

eino 官方的 `summarization` 和 `reduction` 两个中间件都挂在 `BeforeModelRewriteState` 上，
也就是**每次模型调用前**检查是否需要压缩。本项目的压缩只在 turn 与 turn 之间跑。

差异是实打实的：**一轮之内 agent 跑几十步、历史不断膨胀，是不会触发压缩的**。
deep_research 那种 `MaxIteration: 50` 的场景恰恰最容易撑爆。

turn 间压缩的理由（落库、折叠标记、重启后可识别）依然成立，
但它是一个**有边界的方案**而不是完整方案。

### 为什么两层必须并存，不能只留一层

关键事实：**eino 的 state 生命周期只有一次 `Run`**。

本项目是「一条用户消息一次 `Run`」，每轮都用 `toSchemaMessages`
从数据库重建完整历史再传进去。所以框架中间件在 `BeforeModelRewriteState`
里做的压缩，改的是**这次 Run 的内存态消息列表**；Run 结束后落库的是
`RunCollector` 收集到的真实事件，压缩后的形态根本没进数据库。
下一轮 Run 开始，历史又从数据库原样重建回来。

推论：只挂框架的 `summarization`，等于每轮把同样的老历史重新摘要一遍——
token 和延迟每轮重复，而且模型有随机性，每轮摘出来的还可能不一致。

| | turn 间（本项目的 compactor） | turn 内（`BeforeModelRewriteState`） |
|---|---|---|
| 操作对象 | 数据库里的持久化历史 | 本次 Run 的内存消息列表 |
| 成果寿命 | 永久，跨重启 | 只活到本轮结束 |
| 解决的问题 | 历史长期可持续 | 这一次模型调用别超限 |

**turn 间治本，turn 内救急。** 本项目缺的是「救急」那一层：
deep_research 跑到第 40 步、上下文堆到临界时没有任何东西会出手，
而 turn 间那层在 `runner.Run` 之前就跑完了，够不着。

所以补的路径是**加一层而不是搬家**。

`toSchemaMessages` 这个投影函数是关键接缝：数据库里存的是结构化的行
（assistant 带 tool_calls、tool 行带结果），投影时要严格还原
tool_use / tool_result 的配对，并过滤掉孤儿调用——
否则下一轮请求会被模型 API 直接拒掉。

多模态也在这一层：`multimodal.BuildUserMessage` 把 `[image: 路径]` 标记
展开成 eino 的 `schema.MessageInputPart` 图片块。

---

## 九、刻意不用 eino 的部分

| eino 能力 | 本项目 | 原因 |
|---|---|---|
| `compose.Graph` / `Chain` | 只在测试里 | 没有复杂到需要图编排的确定性子流程 |
| `flow/agent/react` | 死代码，留着当演进证据 | 已迁到 ADK |
| `multiagent/host` | 从未用过 | AgentAsTool 更适合 |
| ADK workflow agents | 没用 | 流程是模型决定的，不是预设的 |
| `callbacks` | 定义了没用 | 拿不到 AgentName |
| retriever / indexer / embedding 组件 | **全部自研** | 见下 |
| document loader / parser | 自研 | 需要 docx/pptx/pdf 的定制提取 |
| eino-ext 的 MCP 适配器 | 自研 `mcp/adapter.go` | 需要自己控制连接和工具注入时机 |

### RAG 为什么全自研

`internal/rag/` 下是完整的一套：`chunker`（markdown 语义分块）、`embedding`、
`indexer`、`retriever`（`bm25.go` + `bruteforce.go` + `hybrid.go`）、`store`、`vector`。

**没有用 Milvus，也没有用 eino 的 retriever 组件。**

理由是这条链路的每一环都要能独立调试：
索引重建、单路召回验证、融合排序对比，都不该依赖跑通整个 agent。
用 eino 的组件抽象反而会把这些环节包进框架的执行模型里，
调一次要起一个图。自己拆开之后，每一层都是普通的 Go 函数，写个测试就能验。

规模也是原因：本地题库是有限的静态语料，暴力向量检索完全够用，
引入 Milvus 是纯粹的运维负担。

---

## 十、遗留与边界

| 项 | 状态 |
|---|---|
| `internal/agent/agent.go` 的 `NewReActAgent` | 死代码，零调用点，留作架构演进的实物证据 |
| `internal/stream/sse_handler.go` 的 `NewSSEHandler` | 死代码，老路线的 callback 实现 |
| `adk_agent.go:42` 的函数注释 | **已过时**，还写着「双 agent 拓扑」，实际是 5 个 |
| TurnLoop | 没用。目前是「一条消息一次 Run」，用户消息不能打断当前 turn |
| 模型 Failover | 没用。eino v0.9 有 ChatModel Failover 中间件，可以做 |
| tracing | 没接。callback 层加一个 handler 就能拿 token / latency / trace |
| Prompt 版本化 | 目前是字符串常量硬编码 |

---

## 十一、和实习项目的对照

同一批问题，在两个框架下的解法：

| 问题 | klingwork-app（TS/ai-sdk） | LingCoWork（Go/eino） |
|---|---|---|
| 工具列表会变 | 连接池 + manifest 缓存，每轮重建工具集 | 图冻结，靠 `BeforeAgent` 每轮重新注入 |
| 技能装完要生效 | 每轮 run 重新扫描目录，天然生效 | instruction 构造时固定，靠中间件每轮拼接 |
| sub-agent 挂载 | 内置专家 + 路由表，`agent_<id>` 派发工具 | `NewAgentTool` 包成工具 |
| 中断恢复 | 自己实现抢救 + 孤儿修复 | 框架提供 interrupt/checkpoint，应用层配合 |
| 审批 | ToolSpec 装饰器 | `compose.ToolMiddleware` |

有意思的是**审批的实现形态几乎一样**——都是「在工具执行外面包一层」。
说明这个模式和语言、框架无关，是问题本身的结构决定的。

而**「运行时变化的东西怎么进入模型视野」这个问题，两边差异最大**：
ai-sdk 那边每轮重新组装工具集是默认行为，eino 这边因为图冻结，
所有动态性都要显式地通过中间件补出来。

这个对比能说明一件事：**框架的核心取舍会渗透到应用的每个角落**。
eino 选了「编译期确定、运行时不可变」，换来类型安全和可推理性，
代价就是所有动态需求都要额外绕一道。
