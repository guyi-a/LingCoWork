# 00 · Eino 总览与设计哲学

> 面向面试准备的技术手册。

---

## 1. Eino 是什么

**一句话定位**：Eino 是字节跳动 CloudWeGo 团队开源的 **Go 语言 LLM 应用开发框架**，2024 年开源，仓库 [github.com/cloudwego/eino](https://github.com/cloudwego/eino)。

**名字**：Eino 读作 `['aino]`，谐音 "i know"，寓意"框架能懂你要做什么"。

**官方定位原文**：
> "Eino aims to provide the ultimate LLM application development framework based on the Go language."

**灵感来源**：官方明确写了参考 LangChain、LlamaIndex，同时融合了 LangGraph、AutoGen、CrewAI、Google ADK 这些多智能体框架的思路，最后按 Go 的编程习惯重新设计。

**为什么需要它**：Python 生态（LangChain/LlamaIndex/LangGraph）在 Go 里没有对等物。字节内部大量 Go 微服务需要接入大模型，Eino 就是这条路上的官方答案。

---

## 2. 设计哲学（面试必背）

官方给出四个关键词：**简洁、可扩展、可靠、有效**（simplicity, extensibility, reliability, effectiveness）。

在此之下，Eino 有几个和 LangChain 明显区别的设计取向：

### 2.1 强类型 I/O（Go 泛型 + 编译期类型检查）

LangChain 的组件之间几乎全是 `Any` 传递，靠运行期 duck typing 拼接。Eino 把每个组件的输入输出类型都固化在接口上（`Runnable[I, O]`），编排 Chain/Graph 时上下游类型不匹配会**编译期 or 图构建期报错**，不用等到线上跑起来才炸。

面试点：**Go 泛型（1.18+）落地在 LLM 框架的一个典型场景**。

### 2.2 流式一等公民

LLM 的输出天然是 token 流。Eino 把流式抽象成 `StreamReader[T]`，并规定每个组件都要声明自己的 **四种运行范式** 支持哪一种（详见 `03-streaming.md`）：

| 范式 | 输入 | 输出 |
|---|---|---|
| Invoke | 非流 | 非流 |
| Stream | 非流 | 流 |
| Collect | 流 | 非流 |
| Transform | 流 | 流 |

框架自动帮你做流的拼接（Concat）、复制（Copy）、合并（Merge）、转换。你写业务时不用去关心"上游是不是流"。

### 2.3 编排即图，图是静态的

Graph 在 `Compile()` 阶段就完成了拓扑校验、类型检查、并发计划。**运行时不能改图**。这和 LangGraph 的可变 StateGraph 是不一样的取向 —— Eino 更强调确定性和可推理性。

这条不只影响 `compose` 层：**ADK 的 ChatModelAgent 内部也是编译出来的图**，
所以它的工具表在首次运行时就被 `sync.Once` 冻结了。
「运行期才出现的工具怎么进入模型视野」因此成为一个必须显式处理的问题——
这是本项目最核心的一个约束，见 [10 §5](10-in-this-project.md)。

### 2.4 Component 抽象透明

一个 `Retriever` 无论内部是简单向量库还是 `MultiQueryRetriever`，从外面看都只是 `Retriever` 接口。**实现的复杂度不外泄**。

### 2.5 用两条路线覆盖不同场景

这是 Eino 最重要的一个架构决策，专门写在 `03-orchestration` 和 `08-multi-agent` 之前一定要先理解：

- **Graph 路线**（`eino/compose`）：确定性、结构化、可控 → 用来做"功能按钮/后端服务"（RAG、总结、抽取等）
- **Agent 路线**（`eino/adk`）：自主性、对话式、不确定 → 用来做"智能助手/多步任务"

官方那篇 [Agent or Graph?](https://www.cloudwego.io/docs/eino/overview/graph_or_agent/) 给出的推荐融合点是：

> **"The best integration point is to encapsulate Graph as Agent's Tool."**
>
> 即：把 Graph 封装成 Agent 的 Tool，让 Agent 层去做决策，Graph 层去做确定性子任务。

---

## 3. 核心模块地图

一张脑图（面试可以画）：

以下按 **v0.9.1 实际的包结构**（`go/pkg/mod` 里核对过）：

```
Eino
├─ schema/               # Message、Document、RoleType、StreamReader 等基础类型
├─ components/           # 组件抽象层（接口 + 默认实现）
│   ├─ model/            #   ChatModel、ToolCallingChatModel
│   ├─ tool/             #   Tool（BaseTool / InvokableTool / StreamableTool）
│   ├─ prompt/           #   ChatTemplate
│   ├─ retriever/ indexer/ embedding/
│   ├─ document/         #   Document Loader / Parser / Transformer
│   └─ ...
├─ compose/              # 编排层
│   ├─ chain.go graph.go workflow.go   # 三种编排入口
│   ├─ pregel.go dag.go                # 两种执行引擎
│   ├─ state.go                        # 图的局部状态（WithGenLocalState）
│   ├─ interrupt.go resume.go checkpoint.go  # v0.7 的中断/恢复基础设施
│   └─ agentic_tools_node.go           # v0.9 agentic-runtime 的工具节点
├─ callbacks/            # 切面：Handler、5 种 hook
├─ flow/                 # 老路线的高级组合
│   ├─ agent/react/      #   经典 ReAct Agent
│   └─ agent/multiagent/host/  # Host Multi-Agent
└─ adk/                  # Agent Development Kit（新路线，v0.5 引入）
    ├─ interface.go      #   Agent 接口
    ├─ chatmodel.go      #   ChatModelAgent（本项目主力）
    ├─ runner.go turn_loop.go turn_buffer.go
    ├─ agent_tool.go     #   AgentAsTool
    ├─ workflow.go       #   Workflow Agents (Sequential/Parallel/Loop)
    ├─ interrupt.go cancel.go        # 中断 vs 取消，两套不同机制
    ├─ retry_chatmodel.go failover_chatmodel.go   # v0.9：重试与多模型故障转移
    ├─ filesystem/       #   DeepAgent 用的文件后端抽象
    ├─ middlewares/      #   v0.8 引入的官方中间件（见下）
    └─ prebuilt/         #   deep / planexecute / supervisor 三种预制模式
```

**`adk/middlewares/` 是 v0.8 的重头戏**，官方给了 8 个。

先要理解它们共用的接口 `adk.ChatModelAgentMiddleware`——**它横跨三个粒度，一共 9 个钩子**：

| 粒度 | 钩子 | 触发频率 |
|---|---|---|
| **Agent 生命周期** | `BeforeAgent`、`AfterAgent` | 一轮 agent 运行 1 次 |
| **模型调用** | `WrapModel`、`BeforeModelRewriteState`、`AfterModelRewriteState` | 每次调模型（一轮里 N 次） |
| **工具调用** | `WrapInvokableToolCall`、`WrapStreamableToolCall` + 两个 Enhanced 版 | 每次调工具 |

嵌入 `adk.BaseChatModelAgentMiddleware` 可以拿到全部空实现，只覆盖需要的那几个。

官方 8 个中间件的实际分布（v0.9.1 源码核对）：

| 中间件 | 实现的钩子 | 作用 |
|---|---|---|
| `filesystem` | `BeforeAgent` | 文件系统工具注入 |
| `plantask` | `BeforeAgent` | 任务规划 / todo |
| `skill` | `BeforeAgent` + `WrapModel` | 技能加载 |
| `dynamictool/toolsearch` | `BeforeAgent` + `BeforeModelRewriteState` | **延迟工具**：工具太多时不全塞进提示词，给模型一个搜索工具按需加载 |
| `agentsmd` | `BeforeModelRewriteState` | 加载 AGENTS.md 目录指令 |
| `patchtoolcalls` | `BeforeModelRewriteState` | 修补模型产出的畸形 tool_call |
| `reduction` | `BeforeModelRewriteState` | 上下文裁剪 |
| `summarization` | `BeforeModelRewriteState` | 历史摘要压缩 |

**注意：官方这 8 个没有一个用工具调用钩子。**那 4 个 `Wrap*ToolCall` 接口存在但官方自己没用；
本项目的审批和错误处理走的也不是它们，而是更底层的 `compose.ToolMiddleware`。

### 该记住的分界线：`BeforeAgent` vs `BeforeModelRewriteState`

分配依据是**这个东西在一轮之内会不会变**：

- 技能索引、文件系统工具、任务规划 → 一轮之内不变 → `BeforeAgent`，算一次
- 历史摘要、上下文裁剪、修补 tool_call → 随循环推进不断变化 → `BeforeModelRewriteState`，每次模型调用前算

本项目的 `WorkspaceMiddleware` 选 `BeforeAgent` 用的是同一套判断，代码注释里写了理由。

> **由此引出本项目的一个已知边界**：`summarization` / `reduction` 挂在
> `BeforeModelRewriteState` 上，说明官方认为压缩该在**每次模型调用前**检查。
> 而本项目的压缩跑在 `runner.Run` 之前、turn 与 turn 之间——
> 一轮之内 agent 跑几十步把历史撑爆是不会触发压缩的。
> 这个取舍的理由见 [10 §8](10-in-this-project.md)，但要清楚它的边界在哪。

### 别和本项目的 `DynamicToolsMiddleware` 搞混

`dynamictool/toolsearch` 解决的是**工具数量爆炸**（接了一堆 MCP server），
本项目的 `DynamicToolsMiddleware` 解决的是**工具晚到**（OAuth 授权在启动后才完成）。
名字像，问题不同——详见 [10 §5](10-in-this-project.md)。

对应的外围仓库：

- **`github.com/cloudwego/eino`** —— 核心（接口、编排、类型、流、回调、ADK）
- **`github.com/cloudwego/eino-ext`** —— 各家 ChatModel/Tool/Retriever 实现、Callback handler、DevOps 工具
- **`github.com/cloudwego/eino-examples`** —— 示例
- **`eino-ext/devops`** —— 可视化开发/调试 IDE 插件

面试点：**为什么切成三个仓库？** —— 核心接口稳定，实现随第三方生态迭代快，用 tag 独立版本，避免因为某个 SDK 升级把核心带坏。

---

## 4. 六大价值主张（官方原文提炼）

1. **Component**：精选的组件抽象与实现，可组合可复用
2. **ADK**：多 Agent 编排、HITL、预制 Agent 模式
3. **Orchestration**：类型检查、流处理、并发、切面、Option 分发
4. **API 简洁**：接口设计强调简单和一致
5. **Flow & Examples**：内置最佳实践
6. **DevOps**：从可视化开发到线上 tracing/evaluation 全生命周期

面试如果被问"和 LangChain 比它的独特卖点"，回答 **1. 强类型 2. 流式一等公民 3. 编排即图（编译期校验）4. Graph 与 Agent 双路线明确分工 5. Go 原生（没有 Python GIL、部署简单）**。

---

## 5. 两条路线深挖：Graph vs Agent

官方 [Agent or Graph?](https://www.cloudwego.io/docs/eino/overview/graph_or_agent/) 给的对比（面试爱问）：

| 维度 | Agent（ADK） | Graph（compose） |
|---|---|---|
| 核心驱动 | LLM 自主决策 | 开发者预设流程 |
| 输入 | 非结构化（自然语言/图像） | 结构化数据 |
| 交付物 | 过程和结果同等重要 | 只关心最终结果 |
| 状态 | 长期、跨执行 | 单次执行、无状态 |
| 运行模式 | 倾向异步（AsyncIterator） | 倾向同步 |

**决策规则**（面试可背）：
- 任务是"开放/不确定"的 → Agent
- 任务是"封闭/确定"的 → Graph
- 想两者结合 → **Graph 封装成 Agent 的 Tool**（不要反过来把 Agent 塞进 Graph 节点，官方明确不推荐，原因：Agent 会拉 Memory、产异步流，下游 Graph 节点很难消费）

---

## 6. 版本演进（面试可能问"你用的是哪个版本"）

| 版本 | 关键变化 |
|---|---|
| v0.1 | 首发，Component + Chain/Graph 编排 |
| v0.2 | 二次迭代，编排能力完善 |
| v0.3 | 小破坏性变更 |
| v0.4 | compose 优化 |
| **v0.5** | **ADK 首次登场**（重要分水岭：ADK 独立于 flow/agent） |
| v0.6 | jsonschema 优化 |
| v0.7 | interrupt/resume 重构（HITL 基础） |
| v0.8 | ADK middleware 体系（skill、summarization、todo 等） |
| **v0.9** | **agentic-runtime**（当前项目使用的版本 v0.9.1） |

> 这张表的措辞和官方 release notes 的文档标题一一对应，可以直接背。
>
> 需要知道的现状：**v0.9.1 已经不是最新版**，上游在推 v0.10 的 alpha。
> 如果被问「为什么不升」，诚实的答案是 v0.10 还在 alpha、没有稳定版，
> 而 v0.9.1 已经覆盖了项目需要的全部能力（ADK + 中间件 + interrupt/checkpoint）。

面试点：能说清楚"从 flow/agent/react + host multi-agent 的老路线，迁移到 ADK + AgentAsTool 的新路线"这个演进过程，加分。

---

## 7. 本项目（Interview-Agent）在架构中的位置

一句话：**我们走的是 ADK 路线（新路线）**，用 `Runner + ChatModelAgent 作为 root（supervisor） + AgentAsTool 挂载 sub-agent`。

| 项目组件 | Eino 侧对应 |
|---|---|
| `internal/agent/adk_agent.go::NewInterviewADKAgent` | 装配 root supervisor + **4 个 sub-agent** + Runner |
| `agent.SupervisorAgentName` (root) | `adk.NewChatModelAgent(...)` |
| `agent.DeepResearchAgentName` (sub) | `adk/prebuilt/deep.New(...)` —— DeepAgent 预制模式 |
| `agent.JobSearchAgentName` (sub) | `adk.NewChatModelAgent(...)` |
| `agent.ResumeAnalyzerAgentName` (sub) | `adk.NewChatModelAgent(...)` |
| `agent.QuestionPlannerAgentName` (sub) | `adk.NewChatModelAgent(...)` |
| Sub-agent 挂载方式 | `adk.NewAgentTool(ctx, subAgent)` —— **AgentAsTool** |
| `EmitInternalEvents: true` | 子 agent 事件冒泡到 Runner 的 AsyncIterator（仅 supervisor 开） |
| `internal/agent/runtimectx/*.go` | `adk.ChatModelAgentMiddleware` 的 `BeforeAgent`：每轮注入 workspace / 技能索引 / MCP 工具 |
| `internal/service/chat.go::runAgent` | 消费 `runner.Run(ctx, msgs)` 返回的 iter → SSE |
| `internal/agent/checkpoint/dbstore.go` | `adk.CheckPointStore` 的 SQLite 实现，中断态跨重启可恢复 |
| `internal/agent/tools/*.go` | 各种 `tool.BaseTool` 实现（工具用 `utils.InferTool` 自动推 schema） |
| `internal/agent/agent.go::NewReActAgent` | **旧路线残留**：`flow/agent/react.NewAgent(...)`，当前主流程未使用 |

详见 [10 · eino 在本项目的落地](10-in-this-project.md)。

面试问"为什么用 ADK 不用 Graph？"—— 因为面试对话是开放式、多轮、需要 sub-agent 处理复杂子任务（deep_research 写报告、job_search 抓 Boss 直聘），刚好落在"Agent 路线"这一侧。

---

## 8. 后续章节导航

- `01-core-abstractions.md` —— Component、Runnable、Lambda、类型系统
- `02-orchestration.md` —— Chain / Graph / Workflow 三种编排
- `03-streaming.md` —— StreamReader/Writer + 四种运行范式（招牌，面试重点）
- `04-chatmodel-and-message.md` —— ChatModel 接口、Message 结构
- `05-tools-and-function-call.md` —— Tool 抽象、ToolsNode、function call 全流程
- `06-callbacks.md` —— Callback/Handler 与 5 种 hook
- `07-react-agent.md` —— 老路线：`flow/agent/react` 源码级拆解
- `08-multi-agent.md` —— **重点**：ADK / ChatModelAgent / Runner / AgentAsTool / Supervisor / DeepAgent / Plan-Execute
- `09-eino-ext.md` —— 生态扩展仓库
- `10-in-this-project.md` —— 本项目落地案例（结合代码）
