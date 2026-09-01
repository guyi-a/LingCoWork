# 08 · ADK 与 Multi-Agent（Supervisor / AgentAsTool / DeepAgent / HITL）

**本项目的核心**。这一章把 ADK 的整体架构（`Runner` / `Agent` / `AgentEvent`）、`ChatModelAgent` 的内部实现、四种多 Agent 拓扑、三个 prebuilt 模式、Interrupt & CheckPoint 的完整 HITL 链路、AgentMiddleware 生态，最后落到本项目 `Supervisor + 4 个 sub-agent（deep_research / job_search / resume_analyzer / question_planner）` 的实际装配上一次讲清楚。

前置阅读：[01 核心抽象](01-core-abstractions.md)、[07 ReAct](07-react-agent.md)（ADK 内部就是它）、[06 Callback](06-callbacks.md)、`approval` 那次讨论。

---

## 1. ADK 定位：为什么在 v0.5 引入

老路线 `flow/agent/react` 是**单 Agent + 手搭图**。生产里通常需要：

- **多 Agent 协作**：一个总管 + 若干专才（本项目 supervisor + deep_research / job_search / resume_analyzer / question_planner）
- **过程可观察**：不只要最终结果，要给用户看"agent 现在在想什么、在调什么工具"
- **HITL**：某些工具调用要用户批准才能继续
- **异步/事件驱动**：跑长任务时用户能中途 cancel、能刷新页面回连

`flow/agent/react` 硬撑这些能力代价很大（要自己包 sub-agent、要自己写 callback trace、要自己实现 interrupt）。ADK 是 v0.5 起把这套抽象**独立成一层**的答案：

- 统一的 **`Agent` 接口**（3 方法）—— 让所有 agent 可组合
- **`Runner`** 作为执行入口，返 `AsyncIterator[*AgentEvent]` —— 天然事件流
- **`NewAgentTool`** 把 Agent 包成 Tool 挂给 parent —— AgentAsTool 一等公民
- 内建 **Interrupt & CheckPoint** —— HITL 不用自己攒
- v0.8 加 **AgentMiddleware** —— skill/todo/summarization/filesystem 生态

**一句话**：**ADK = 把"多 Agent + 事件流 + HITL"从 Graph 编排里独立出来的高层抽象**。老路线还在，处理"单 Agent 简单 pipeline"够用；生产环境走 ADK。

---

## 2. `Agent` 接口回顾（3 方法，不是 4 个 Runnable 方法）

见 `01-core-abstractions.md`。这里精简：

```go
type Agent = TypedAgent[*schema.Message]

type TypedAgent[M MessageType] interface {
    Name(ctx context.Context) string
    Description(ctx context.Context) string
    Run(ctx context.Context, input *TypedAgentInput[M], opts ...AgentRunOption)
        *AsyncIterator[*TypedAgentEvent[M]]
}
```

- **Name / Description** 是 Agent 的"身份牌"，被 `NewAgentTool` 包成 tool 时 → 就是 `ToolInfo.Name` / `ToolInfo.Desc`
- **Run** 是唯一执行入口，返 `AsyncIterator` 而不是 `Runnable`，语义完全不同

**`ResumableAgent` 子接口**（支持中断恢复）：

```go
type ResumableAgent = TypedResumableAgent[*schema.Message]

type TypedResumableAgent[M MessageType] interface {
    TypedAgent[M]
    Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption)
        *AsyncIterator[*TypedAgentEvent[M]]
}
```

**本项目所有 Agent 都是 ResumableAgent**（ChatModelAgent 默认实现），因为要支持 HITL 的 approval 恢复。

---

## 3. `ChatModelAgent` —— ADK 的主力实现

`adk/chatmodel.go`。**ADK 里 90% 场景就靠这一个**（预制模式如 DeepAgent 内部也是它 + 一堆 middleware）。

### 3.1 Config

关键字段（`ChatModelAgentConfig`，`adk/chatmodel.go:260-417`）：

```go
type ChatModelAgentConfig = TypedChatModelAgentConfig[*schema.Message]

type TypedChatModelAgentConfig[M MessageType] struct {
    // 身份
    Name        string   // 用作 NewAgentTool 后的 tool 名
    Description string   // 用作 NewAgentTool 后的 tool 描述（parent agent 决策依据）
    Instruction string   // System prompt

    // 模型 & 工具
    Model       model.ToolCallingChatModel
    ToolsConfig ToolsConfig                // 内嵌 compose.ToolsNodeConfig

    // 执行控制
    MaxIterations int                       // ReAct 循环上限（本项目 50，resume_analyzer 30）
    GenModelInput TypedGenModelInput[M]     // 自定义"如何拼 messages 喂给 model"

    // 生命周期 hook（before/after agent 运行）
    // Middlewares 是 v0.8 加的 AgentMiddleware 数组
    Middlewares []AgentMiddleware
    Handlers    []AgentCallback             // 更细粒度的 callback

    // 其他
    BeforeAgentHook / AfterAgentHook        // 简化版 hook
    ExitTool                                // 默认注入 exit tool，模型可主动结束
    // ...
}
```

**注意 `ToolsConfig`**：

```go
type ToolsConfig struct {
    compose.ToolsNodeConfig                    // 内嵌 Graph 层的 ToolsNode config
    EmitInternalEvents bool                    // ★ 关键：让 sub-agent 内部事件冒泡
}
```

`EmitInternalEvents = true`（本项目 supervisor 用了）—— sub-agent（通过 NewAgentTool 挂上来的）内部的 assistant/tool 事件也会通过 root Runner 的 `AsyncIterator` 冒泡出来。**没有它，用户就看不到"deep_research 正在做什么"**，只能看到 root supervisor 调了 deep_research 这个工具、拿到最后结果。

### 3.2 内部图长什么样

引用 [07 §2](07-react-agent.md#2-图结构4-个节点)：**ChatModelAgent 内部就是一个 ReAct 循环**（`adk/react.go:326`：`type reactGraph = *compose.Graph[*reactInput, Message]`）。差别：

| 项 | `flow/agent/react` 老路线 | `adk.ChatModelAgent` 新路线 |
|---|---|---|
| 图结构 | 用户可见的 `*compose.Graph[[]Msg, Msg]` | 藏在 agent 内部，返 `AsyncIterator[Event]` |
| 组织 | 手搭 Model / Tools / Branch | ADK 帮你搭好 |
| 输出 | `Runnable` 的 4 范式 | `AgentEvent` 事件流 |
| 观察 | Callback | Callback + AgentEvent（后者信息更丰富） |
| Middleware | 只有 ToolMiddleware | ToolMiddleware **+ AgentMiddleware** |
| Sub-agent | 靠 tool.BaseTool 手包 | `NewAgentTool(ctx, subAgent)` 一等公民 |
| Interrupt/Resume | 支持但生态弱 | 一等公民 + `ResumeWithParams` |

### 3.3 Compile 发生的时机

见 [01 §6.2](01-core-abstractions.md)：`NewChatModelAgent` 只存 config，**首次 `Run` 时才建图并 Compile**（`chatmodel.go:976 buildNoToolsRunFunc` / `1097 buildMessageReActRunFunc` / `1236 buildAgenticReActRunFunc` 内部都有 `chain.Compile(ctx)`）。

---

## 4. `Runner` —— 执行入口

`adk/runner.go`。四个方法：

```go
type Runner = TypedRunner[*schema.Message]

func NewRunner(_ context.Context, conf RunnerConfig) *Runner

func (r *Runner) Run(ctx, messages []*schema.Message, opts ...AgentRunOption)
    *AsyncIterator[*AgentEvent]
    
func (r *Runner) Query(ctx, query string, opts ...AgentRunOption)
    *AsyncIterator[*AgentEvent]
    
func (r *Runner) Resume(ctx, checkpointID string, opts ...AgentRunOption)
    (*AsyncIterator[*AgentEvent], error)

func (r *Runner) ResumeWithParams(ctx, checkpointID string, params *ResumeParams,
                                   opts ...AgentRunOption)
    (*AsyncIterator[*AgentEvent], error)
```

区别：

| 方法 | 场景 |
|---|---|
| `Run` | 完整 messages history → 起新一轮（**本项目用**） |
| `Query` | 直接给字符串，内部包成 `UserMessage` |
| `Resume` | 恢复中断，无新参数（例如超时重跑） |
| `ResumeWithParams` | 恢复中断，**带用户决策**（本项目 HITL approval 用它） |

### 4.1 RunnerConfig

```go
type RunnerConfig struct {
    Agent           Agent                // root agent
    EnableStreaming bool                 // 事件里包含 stream vs 只包含最终 message
    CheckPointStore CheckPointStore      // 支持 HITL 的持久化后端
}
```

**本项目**（`adk_agent.go::NewInterviewADKAgent` 末尾）：

```go
runner := adk.NewRunner(ctx, adk.RunnerConfig{
    Agent:           supervisor,
    EnableStreaming: true,
    CheckPointStore: checkpoint.NewDBStore(checkpointRepo),
})
```

**CheckPointStore 已经换成 SQLite 持久化**（`internal/agent/checkpoint/store.go::DBStore`，落 `checkpoints` 表）。早期版本没设这个字段、用 ADK 默认的内存 store，重启进程后等待审批的 run 就恢复不了；现在中断状态序列化进 DB，**进程重启后用户点批准仍然能 `ResumeWithParams` 接着跑**。配套的 `approval.PendingStore` 也同样落了 `pending_approvals` 表（内存 map 只是索引）。

### 4.2 `WithCheckPointID` —— 给这次 run 打 checkpoint 名

`adk/interrupt.go:192`：

```go
func WithCheckPointID(id string) AgentRunOption
```

本项目直接用 conversation id（`chat.go::runAgent`）：

```go
iter := s.runner.Run(ctx, msgs, adk.WithCheckPointID(convID))
```

**一个对话一次活跃 run** 的模型下，`convID` 就是唯一的 checkpoint 名，恢复时 `runner.ResumeWithParams(ctx, convID, params)` 直接找回。

### 4.3 消费 AsyncIterator 的正确姿势

见 [ADK Event 消费那章](06-callbacks.md) + `internal/stream/adk_handler.go::ConsumeADKEvents`：

```go
for {
    select {
    case <-ctx.Done():
        return ctx.Err()
    default:
    }
    ev, ok := iter.Next()
    if !ok { return nil }               // 流耗尽
    if ev.Err != nil { return ev.Err }  // agent 层错误
    if ev.Action != nil && ev.Action.Interrupted != nil {
        emitInterrupt(...)               // HITL 中断（审批卡 / 提问卡）
        continue
    }
    if ev.Output == nil { continue }    // Action-only 事件
    // Output.MessageOutput.IsStreaming? → 消费 stream
    // Role=Tool? → tool result
    // 其他 → 完整 assistant message
}
```

---

## 5. `AgentEvent` / `AgentAction` / `AgentInput`

### 5.1 AgentEvent

```go
type AgentEvent = TypedAgentEvent[*schema.Message]

type TypedAgentEvent[M MessageType] struct {
    AgentName string                     // 谁产生的 (root vs sub-agent)
    RunPath   []RunStep                  // root→当前的路径（v0.7 加，用得少）
    Output    *TypedAgentOutput[M]       // 输出（message 或 stream）
    Action    *AgentAction               // 动作（Exit / Interrupted / Transfer / BreakLoop）
    Err       error                      // 错误
}
```

**四个字段互斥**：一个事件走 Output / Action / Err 其中一路。

### 5.2 `AgentAction` 四类

```go
type AgentAction struct {
    Exit             bool                     // 结束
    Interrupted      *InterruptInfo           // HITL 中断
    TransferToAgent  *TransferToAgentAction   // 转手给别的 agent
    BreakLoop        *BreakLoopAction         // LoopAgent 里主动跳出循环
    CustomizedAction any                      // 自定义
}
```

本项目 **只处理 Interrupted**（`adk_handler.go:110`）。其他 Action 因为拓扑不用（AgentAsTool 走 tool result 而不是 Transfer；本项目不用 LoopAgent）暂时跳过。

### 5.3 AgentInput

```go
type AgentInput = TypedAgentInput[*schema.Message]

type TypedAgentInput[M MessageType] struct {
    Messages        []M
    EnableStreaming bool
}
```

**内部结构**很简单。你想传自定义参数（cache key、trace id 等）不用改 input，走 `AgentRunOption` 或 context。

---

## 6. Multi-Agent 四种拓扑

### 6.1 AgentAsTool（**推荐 & 本项目模式**）

```go
subAgent, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Name:        "deep_research",
    Description: "后台研究员...",
    ...
})

subTool := adk.NewAgentTool(ctx, subAgent)     // ← 把 Agent 包成 tool.BaseTool

parentAgent, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Name: "supervisor",
    ToolsConfig: adk.ToolsConfig{
        ToolsNodeConfig: compose.ToolsNodeConfig{
            Tools: append(baseTools, subTool),  // ← 挂给 parent
        },
        EmitInternalEvents: true,               // ★ 让 sub 事件冒泡
    },
})
```

**语义**：Parent 用普通 function-calling 决定要不要"调"这个 sub-agent；sub-agent 用 `agentToolRequest{Request string}` 结构接收任务描述；Parent 拿到 sub 的最终 message 作为 tool result。

**为什么推荐**：
- **统一心智**：模型只知道"调工具"，不需要理解"多 agent 转手"
- **上下文隔离**：sub-agent 有自己的 message history（parent 的 tool_call 就是它的初始 input），不会污染 parent 的历史
- **可组合**：sub-agent 内部还能再挂 sub-agent（不过官方建议**别套太深**）

### 6.2 Workflow Agents（Sequential / Parallel / Loop）

`adk/workflow.go:686-704`。

```go
func NewSequentialAgent(ctx, config *SequentialAgentConfig) (ResumableAgent, error)
func NewParallelAgent  (ctx, config *ParallelAgentConfig)   (ResumableAgent, error)
func NewLoopAgent      (ctx, config *LoopAgentConfig)       (ResumableAgent, error)
```

- **Sequential**：按顺序跑一组 sub-agent，前一个输出作后一个输入。**确定性**流程（例："先 planner → 再 executor → 再 checker"）
- **Parallel**：并发跑一组 sub-agent，合并输出。**分工**（例："三个专家各自评审同一份文档"）
- **Loop**：反复跑一个 sub-agent 直到它 emit `BreakLoopAction`。**自主收敛**（例："改代码 → 测试 → 失败就再改 → 直到通过"）

**本项目未用**。适合"任务流程已知、想要确定性"的场景 —— 本项目是开放对话，用 ChatModelAgent + AgentAsTool 更合适（Agent or Graph 那章讲的选型逻辑）。

### 6.3 Sub-agent Transfer（不推荐）

```go
type OnSubAgents interface {
    OnSetSubAgents(ctx context.Context, subAgents []Agent) error
    ...
}
```

parent agent 声明 sub-agents 后，可以 emit `AgentAction.TransferToAgent{DestAgentName: "x"}` **把控制权完全转移**给 sub-agent（parent 挂起，sub 拿到完整历史继续跑）。

**官方明确不推荐**（`adk/interface.go:471`）：

> "NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven to be more effective empirically. Consider using ChatModelAgent with AgentTool or DeepAgent instead for most multi-agent scenarios."

原因：全历史共享让 sub-agent 的 context 很快爆炸，且 UI 上不好呈现"到底谁在说话"。AgentAsTool 用 tool_call/tool_result 的边界天然做了隔离。

### 6.4 直接手写编排（compose.Graph）

不用 ADK，回到 `compose.Graph` 层自己编 agent-as-node —— 官方也不推荐（Agent or Graph 那章：Agent 有 memory、产异步流，Graph 下游节点很难消费）。

---

## 7. 三大 Prebuilt 模式

`adk/prebuilt/`。都是"用 ChatModelAgent + 一堆预置 Middleware / SubAgent"打包出来的。

### 7.1 `deep` —— DeepAgent（本项目用）

`adk/prebuilt/deep/deep.go`。**Anthropic 的 "Building effective agents" 里 Deep Research 模式的实现**。

Config 关键字段（本项目 `adk_agent.go::NewInterviewADKAgent` 里 `deep.New` 那段用到）：

```go
type Config = TypedConfig[*schema.Message]

// (从 test file 拼出来的字段)
type Config struct {
    Name                    string
    Description             string
    ChatModel               model.ToolCallingChatModel
    Instruction             string
    MaxIteration            int
    WithoutWriteTodos       bool        // 关掉"写 todos"中间件
    WithoutGeneralSubAgent  bool        // 关掉自动生成的 general sub-agent
    ToolsConfig             adk.ToolsConfig
    SubAgents               []adk.Agent // 手动加的 sub-agent
    Middlewares             []adk.AgentMiddleware
    Handlers                []adk.AgentCallback
    ModelFailoverConfig     ...
    TaskToolDescriptionGenerator ...
}
```

**内部装配**：`deep.New(cfg)` 内部（`prebuilt/deep/deep.go`）：

1. 构建**内置 middleware chain**（filesystem / summarization / todo-writing …）
2. **除非** `WithoutGeneralSubAgent=true`，否则自动挂一个"通用 sub-agent"用于处理主 agent 委派
3. 传给 `adk.NewTypedChatModelAgent` 得到 `ResumableAgent`

**本项目为什么用它**（`adk_agent.go::NewInterviewADKAgent`）：

```go
deepAgent, err := deep.New(ctx, &deep.Config{
    Name:                   DeepResearchAgentName,
    Description:            "后台研究员...",
    ChatModel:              cm,
    Instruction:            deepResearchInstruction,
    MaxIteration:           50,
    WithoutWriteTodos:      true,      // 关掉默认 todo 工具（不需要）
    WithoutGeneralSubAgent: true,      // 关掉套娃（不让 deep_research 再 spawn agent）
    ToolsConfig: adk.ToolsConfig{
        ToolsNodeConfig: compose.ToolsNodeConfig{
            Tools: baseTools,
            ToolCallMiddlewares: []compose.ToolMiddleware{
                approvalMW,            // approval 在外层，effect 驱动
                toolerr.Middleware(),
            },
        },
    },
    Handlers: []adk.ChatModelAgentMiddleware{skillsMW},  // 技能索引每轮注入
})
```

**关掉两个默认**：项目里 workspace 文件工具已经够用，不需要 DeepAgent 自带的 todo；不允许 deep_research 再套娃 sub-agent 避免失控。

### 7.2 `supervisor` —— Supervisor 预制

`adk/prebuilt/supervisor/supervisor.go:101 New`。语义：**parent agent 决定何时把控制权交给 sub-agent，sub 完成后 return 给 parent**。跟 AgentAsTool 相似但用 Transfer 语义。

**本项目没用预制的 supervisor**，而是**手写**了一个 `adk.NewChatModelAgent(Name="supervisor")` + AgentAsTool 挂 sub。原因：AgentAsTool 语义更简单（tool call/result 天然边界），预制 supervisor 的 Transfer 语义反而多余。

### 7.3 `planexecute` —— Plan-Execute-Replan

`adk/prebuilt/planexecute/plan_execute.go`。**LangChain Plan-Execute 论文的实现**。

三段：

- **Planner**：拿到用户任务 → 输出一份 step-by-step plan
- **Executor**：拿一个 step → 用 sub-tools 执行 → 返结果
- **Replanner**：拿 (原 plan, 已执行结果) → 决定继续执行 / 修改 plan / 结束

签名：

```go
func NewPlanner   (ctx, cfg *PlannerConfig)   (adk.Agent, error)
func NewExecutor  (ctx, cfg *ExecutorConfig)  (adk.Agent, error)
func NewReplanner (ctx, cfg *ReplannerConfig) (adk.Agent, error)
func New          (ctx, cfg *Config)          (adk.ResumableAgent, error)  // 一键组装
```

**本项目未用**。适合"任务能分解成 step + 需要中间校准"的场景（例："写一个后台服务：先出接口设计 → 实现 → 写测试 → 遇到问题回来改设计"）。

---

## 8. Interrupt & CheckPoint —— HITL 完整链路

**承接** [approval 那次讨论](../..)。这里给完整时序图。

### 8.1 触发（中间件返回 sentinel）

`internal/approval/middleware.go`。中间件签名现在是 effect 驱动的：

```go
func Middleware(store *ModeStore, classifier *Classifier, registry *effect.Registry) compose.ToolMiddleware
```

判定链：`effect.Registry` 按工具名查（或派生）这次调用的 `Effect`（读写/破坏性/OpenWorld/AutoApproved 等），结合会话的审批模式（`ModeStore`）决定要不要拦；MCP 等未注册工具走 `Classifier` 兜底。sub-agent 作为工具被调用时注册了 `KindDelegate` 的静态 effect，委派本身不弹审批卡（它内部的工具调用由它自己那份中间件把关）。需要审批时：

```go
return nil, tool.Interrupt(ctx, &stream.ApprovalInfo{
    Tool:   input.Name,
    Args:   input.Arguments,
    CallID: input.CallID,
})
```

`tool.Interrupt(ctx, info)` 返回的是**框架识别的 sentinel error**（不是普通 error）。判定细节见 [approval 笔记](../interview-notes/02-approval-mechanism.md)。

### 8.2 框架识别 → 生成 AgentEvent

框架 unwrap sentinel → 保存 checkpoint（含 graph state + 中断信息）→ 生成一个特殊 event：

```go
AgentEvent{
    Action: &AgentAction{
        Interrupted: &InterruptInfo{
            InterruptContexts: []*InterruptContext{
                {ID: "<interrupt-id>", Info: <ApprovalInfo>, IsRootCause: true},
            },
        },
    },
}
```

iter 之后**自然终止**（下一次 `Next()` 返 `!ok`）。

### 8.3 上层消费

`internal/stream/adk_handler.go:110`：

```go
if ev.Action != nil && ev.Action.Interrupted != nil {
    emitInterrupt(ev.Action.Interrupted.InterruptContexts, checkpointID, sink, buf)
    continue
}
```

`emitInterrupt` 干两件事：
1. 每个 `InterruptContext` 按 payload 类型发一条 SSE 帧——`*ApprovalInfo` → `approval_required`（携带 checkpointID / interruptID / tool name / args / effect），`*QuestionInfo` → `question_required`（AskUser 工具的提问卡）
2. `sink.Record(checkpointID, interruptID, info)` —— 把中断存到 `approval.PendingStore`（内存索引 + SQLite `pending_approvals` 表）

**同时** `service/chat.go::applyWaitingStatus` 按 pending 队首的 kind 设会话状态——approval → `waiting_approval`，question → `waiting_user`——让前端侧栏 pill 亮起。

### 8.4 用户决策 → HTTP 端点

用户点批准/拒绝 → HTTP endpoint → `ChatService.Resume(convID, interruptID, decision)`。

```go
func (s *ChatService) Resume(convID, interruptID string, dec approval.Decision) (bool, error) {
    item, ok := s.pending.Take(convID, interruptID)
    if !ok {
        return false, nil                          // 已经处理过 / 过期
    }
    s.applyWaitingStatus(convID, "running")        // 还有别的 pending 就保持 waiting_*
    buf := s.manager.Create(convID)                // 新 SSE 缓冲（老的已经 Finish 过）
    runCtx, cancel := context.WithCancel(context.Background())
    buf.SetCancel(cancel)
    go s.resumeAgent(runCtx, convID, item.CheckpointID, interruptID, dec, buf)
    return true, nil
}
```

另有一个平行入口 `ResumeQuestion(convID, interruptID, answers hitl.Answers)`：AskUser 工具的提问卡走这条，恢复时传给 `Targets` 的 payload 是 `hitl.Answers` 而不是 `approval.Decision`，工具体用 `GetResumeContext[hitl.Answers]` 取回。同一套 checkpoint/pending 机制，payload 类型区分用途。

`resumeAgent` 里：

```go
iter, err := s.runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{
    Targets: map[string]any{interruptID: dec},
})
```

`Targets` 里 key 是 `interruptID`（可能同时挂起多个 tool call 等审批），value 是 `Decision`（`{Approved bool, Reason string}` 之类）。

### 8.5 恢复时中间件读回决策

`internal/approval/middleware.go:77`：

```go
func resumeDecision(ctx context.Context) (bool, Decision, bool) {
    interrupted, _, _ := tool.GetInterruptState[any](ctx)  // 是否 resume 场景
    if !interrupted { return false, Decision{}, false }
    _, has, dec := tool.GetResumeContext[Decision](ctx)    // 用户传的决策
    return true, dec, has
}
```

- `Approved=true` → `next(ctx, input)` 正常调工具
- `Approved=false` → 返 denial message，模型下一轮 ReAct 看到"用户拒绝执行 X"决定下一步

### 8.6 完整时序图（本项目版）

```
┌──────────────────────────────────────────────────────────────────────────┐
│ USER                            BACKEND                          FRONTEND │
├──────────────────────────────────────────────────────────────────────────┤
│                                                                            │
│   POST /chat/:id "帮我改 X 文件"                                            │
│                              → chat.Start → runAgent                       │
│                                → runner.Run(msgs, WithCheckPointID(cid))   │
│                                → iter                                       │
│                                  ...                                        │
│                                  model 决定调 write_file                     │
│                                  ToolsNode dispatch                         │
│                                    → approval.Middleware()                  │
│                                        NeedsApproval(write_file)? yes       │
│                                        return tool.Interrupt(ctx, info)     │
│                                  ← framework catch sentinel                 │
│                                  ← save checkpoint(cid, graph_state)        │
│                                  ← emit AgentEvent{Action:Interrupted}      │
│                                                                            │
│                                ConsumeADKEvents 拿到 → emit SSE            │
│                                                             ────────►     │
│                                                        approval_required   │
│                                                        {tool, args, ...}  │
│                                                                            │
│                                pending.Record(cid, iid, info)              │
│                                SetAgentStatus(cid, waiting_approval)       │
│                                                                            │
│                                                            ◄──────         │
│   USER CLICKS "APPROVE"                             /approval/:cid/:iid   │
│                              → chat.Resume(cid, iid, dec)                 │
│                                → pending.Take(cid, iid)                     │
│                                → new SSE buf                                │
│                                → runner.ResumeWithParams(cid,               │
│                                     ResumeParams{Targets:{iid: dec}})       │
│                                → iter2                                      │
│                                  approval.Middleware() 再次跑：             │
│                                    resumeDecision(ctx) → Approved=true      │
│                                    → next(ctx, input)  ← 真的调 write_file  │
│                                  tool result → 回 ReAct 循环               │
│                                                                            │
│                                                            ◄──────         │
│                                                     GET /chat/:cid (SSE)  │
│                                                     drain new buf         │
│                                                                            │
└──────────────────────────────────────────────────────────────────────────┘
```

**几个易混点**：

- **一次 checkpoint 可能挂起多个 interrupt**（一次 assistant 决定并发调 3 个工具，其中 2 个需要审批）→ `Targets` 是个 map，用户可以一次批准/拒绝多个
- **老 SSE buf 用完就 Finish**：resume 时新建 buf，前端要重新 `GET /chat/:cid` 订阅新流。这就是 `chat.go::Resume` 里 `manager.Create(convID)` 覆盖老 buf 的原因
- **pending store 和 checkpoint 都已持久化**：`approval.PendingStore` 是内存索引 + SQLite `pending_approvals` 表双写，checkpoint 落 `checkpoints` 表（见 §4.1）。进程重启后待审批的中断能恢复，这一条曾经是"生产要挂持久化 store"的 TODO，现在已经做了

---

## 9. `AgentMiddleware`（v0.8+）

`adk/middlewares/`。跟 `compose.ToolMiddleware` 不同粒度：

| | AgentMiddleware | ToolMiddleware |
|---|---|---|
| 作用于 | 一个 agent 的完整生命周期（每次迭代 / 每次 model 调用 / 每次 tool 调用） | 单个 tool 的调用 |
| 装载 | `ChatModelAgentConfig.Middlewares` | `ToolsNodeConfig.ToolCallMiddlewares` |
| 用途 | 改 prompt / 改工具集 / 改历史 / summarization | 拦截 tool 调用，改结果 |
| 例子 | skill loader / todo writer / filesystem / summarization | approval / toolerr |

**`adk/middlewares/` 生态**（现成中间件）：

| 目录 | 作用 |
|---|---|
| `filesystem` | 给 agent 挂 filesystem 工具集（ARK sandbox / 本地 fs） |
| `skill` | 加载 SKILL.md 定义的技能包（本项目自己实现了类似的） |
| `summarization` | 上下文超长时自动压缩历史 |
| `reduction` | Tool 返回结果超长时截断 / 压缩 |
| `plantask` | 类似 Plan-Execute 但更轻，让 model 写 todo list |
| `toolsearch` | 工具集太大时先让 model "搜工具"再调 |
| `patchtoolcalls` | 修复模型输出的错格式 tool_call |
| `agentsmd` | 加载 AGENTS.md 目录指令（类似 Claude Code 的 CLAUDE.md） |
| `dynamictool` | 运行时动态注册/注销 tool |

**本项目没用 `adk/middlewares/` 的现成中间件**（DeepAgent 内部自动装配了一部分），但**自己写了三个 `adk.ChatModelAgentMiddleware`**（`internal/agent/runtimectx/`），全部只重写 `BeforeAgent`：

| 中间件 | 挂在哪 | 干什么 |
|---|---|---|
| `SkillsIndexMiddleware` | 全部 5 个 agent | 每轮运行前 `loader.Refresh()` 重扫技能目录，把最新技能索引拼进 instruction。Skill Hub 装完的技能**下一轮就生效**，不用重启 |
| `WorkspaceMiddleware` | supervisor / resume_analyzer / question_planner | 每轮把当前 conversation 的 workspace 状态（项目目录、文件清单）拼进 instruction |
| `DynamicToolsMiddleware` | **只有 supervisor** | 每轮调 `provide(ctx)` 拿当前可用的 MCP 工具，改 `runCtx.Tools` 注入。解决两个问题：MCP server 可能在启动后才 OAuth 授权上线（构建期拿不到）；远端工具集不该污染 sub-agent 的 prompt |

这三个是"eino 图 Compile 后不可变"约束下把动态性找补回来的标准姿势：图不动，每轮改 instruction / 工具集。

面试点：ADK **v0.8 加入的 middleware 生态是 ADK 相对 flow/agent/react 老路线的最大进化**，让 agent 的能力扩展不需要改 agent 代码。本项目三个自研 middleware 就是实例。

---

## 10. TurnLoop（v0.9）

`adk/turn_loop.go`。**多轮对话运行时**，支持 push（新 user message）、preempt（打断当前 turn 塞新 message）、stop。

场景：把 agent 当**长连接机器人**跑，不是每轮 user message 单独发起一次 run。类似 WhatsApp bot / 语音助手的模型。

**本项目未用**。当前"一条 user message 一次 Run"够简单。TurnLoop 更适合"用户消息可能连续快速到达、需要打断"的场景。

面试点：**TurnLoop 是 v0.9 的招牌能力，为 real-time agent 场景铺路**。

---

## 11. 本项目完整拓扑一图

```
┌─────────────────────────────────────────────────────────────────────────┐
│  cmd/api/main.go                                                          │
│    └─ agent.NewInterviewADKAgent(ctx, cm, baseTools,                      │
│           dynamicTools,   ← func(ctx) []tool.BaseTool，每轮取 MCP 工具    │
│           skillLoader, checkpointRepo, convRepo, projectRepo,             │
│           approvalModes, classifier, effects)                             │
└──────────────────────────┬──────────────────────────────────────────────┘
                            │
                            ▼
      internal/agent/adk_agent.go::NewInterviewADKAgent
        │
        ├─ 0. 前置：给 4 个 sub-agent 名注册 KindDelegate effect（委派不弹审批卡）
        │      approvalMW  := approval.Middleware(modes, classifier, effects)  ← 全拓扑共用一份
        │      skillsMW    := runtimectx.NewSkillsIndexMiddleware(skillLoader)
        │      workspaceMW := runtimectx.NewWorkspaceMiddleware(convRepo, projectRepo)
        │      dynamicToolsMW := runtimectx.NewDynamicToolsMiddleware(dynamicTools)
        │
        ├─ 1. deepAgent := deep.New(ctx, &deep.Config{
        │        Name: "deep_research", MaxIteration: 50,
        │        WithoutWriteTodos: true, WithoutGeneralSubAgent: true,
        │        ToolsConfig: { Tools: baseTools,
        │                       ToolCallMiddlewares: [approvalMW, toolerr] },
        │        Handlers: [skillsMW],
        │      })                        → prebuilt DeepAgent
        │
        ├─ 2. jobAgent    (ChatModelAgent, MaxIterations 50,
        │                   Handlers: [skillsMW])                 ── 招聘搜索员
        ├─ 3. resumeAgent (ChatModelAgent, MaxIterations 30,
        │                   Handlers: [skillsMW, workspaceMW])    ── 简历自评员
        ├─ 4. plannerAgent(ChatModelAgent, MaxIterations 50,
        │                   Handlers: [skillsMW, workspaceMW])    ── 模拟题生成员
        │      （2-4 的 ToolsConfig 同 1：baseTools + [approvalMW, toolerr]）
        │
        ├─ 5. 各自 adk.NewAgentTool(...) 包成 tool.BaseTool
        │
        ├─ 6. supervisor := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
        │        Name: "supervisor", MaxIterations: 50,
        │        ToolsConfig: {
        │          Tools: baseTools + [deepTool, jobTool, resumeTool, plannerTool],
        │          ToolCallMiddlewares: [approvalMW, toolerr],   ← ★ AgentAsTool
        │          EmitInternalEvents: true,          ← ★ sub-agent 事件冒泡
        │        },
        │        Handlers: [skillsMW, workspaceMW, dynamicToolsMW],
        │      })                        MCP 工具不在 Tools 里，每轮由 dynamicToolsMW 注入
        │
        └─ 7. runner := adk.NewRunner(ctx, adk.RunnerConfig{
                 Agent:           supervisor,      ← root
                 EnableStreaming: true,
                 CheckPointStore: checkpoint.NewDBStore(checkpointRepo),
               })                                   ← SQLite 持久化，重启可恢复中断
```

**运行时形态**：

```
                          runner.Run(ctx, msgs, WithCheckPointID(convID))
                                │
                                ▼
              ┌─────────────────────────────────────────────┐
              │ supervisor (ChatModelAgent, root)             │
              │  ReAct loop:                                  │
              │   ├─ 直接调 baseTools (read_file / mkdir…)    │
              │   ├─ 调 deep_research(request=...)             │
              │   │     → deepAgent 拿到 request 作 UserMsg    │
              │   │       → 自己的 ReAct loop（含 deep 内建 mw）│
              │   │       → EmitInternalEvents=true 让内部        │
              │   │         assistant/tool 事件冒泡到 root iter  │
              │   │       → 结束时返最终 message → 作为工具结果  │
              │   └─ 调 job_search(request=...)                │
              │         → jobAgent 拿到 request → 自己的 ReAct  │
              │         → 结束时返 markdown 列表 → 作为工具结果  │
              │   （resume_analyzer / question_planner 同理，   │
              │     也是 AgentAsTool，各自独立 ReAct loop）      │
              │                                              │
              │  events emit 到 root iter (含 sub-agent 冒泡):│
              │   - IsStreaming (supervisor 说话)             │
              │   - Tool result (root 调的直接工具)            │
              │   - IsStreaming with AgentName="deep_research"│
              │     (sub 冒泡)                                │
              │   - Tool result with AgentName="deep_research"│
              │   - Action.Interrupted (approval 触发)        │
              └────────────────────┬────────────────────────┘
                                   │
                                   ▼
                    ConsumeADKEvents（stream/adk_handler.go）
                    router 根据 AgentName 区分 root vs sub
                    root 事件 → collector.turns / SSE 主帧
                    sub 事件 → collector.subEvents / SSE 带 agent 字段
                    Interrupt → emitInterrupt → pending store
```

**关键设计选择清单**（每个都有面试价值）：

1. **走 AgentAsTool 不走 Transfer**：语义简单，边界清晰，UI 天然能挂"deep_research 的进度到 root 的 deep_research 工具卡片下面"
2. **`EmitInternalEvents: true`**：sub-agent 事件冒泡，前端能看到 deep_research 内部在干什么，不是"黑盒等结果"
3. **DeepAgent 用 prebuilt / 其余 sub-agent 手写 ChatModelAgent**：DeepAgent 有一堆 middleware（summarization / …）适合"后台研究员"这种复杂任务；job_search / resume_analyzer / question_planner 职责单一，轻量 ChatModelAgent 就够
4. **`WithoutGeneralSubAgent: true`** + **`WithoutWriteTodos: true`**：DeepAgent 默认会挂一个"general sub-agent"允许它 spawn 子任务 → 我们不想让 deep_research 再套娃；DeepAgent 默认还挂 todo-writing middleware → 项目里 workspace 文件工具够用不需要
5. **全拓扑共用同一个 approvalMW 实例 + toolerr（顺序：approval 外 / toolerr 内）**：五处各建一份的话，改一处漏四处；effect 驱动的判定规则见 [approval 笔记](../interview-notes/02-approval-mechanism.md)
6. **MCP 工具只给 supervisor、且每轮动态注入**（`DynamicToolsMiddleware`）：远端工具集无边界，不该膨胀 sub-agent 的 prompt；MCP server 可能在 OAuth 授权后才上线，构建期的静态 slice 会被冻结
7. **技能索引每轮注入**（`SkillsIndexMiddleware`）：Skill Hub 装/卸技能下一轮生效，不烤进构建期 instruction
8. **checkpoint id = conversation id**：一个 conv 一次活跃 run，简单可靠；checkpoint 落 SQLite，重启可恢复
9. **MaxIterations**：supervisor / deep / job / planner 50，resume_analyzer 30

---

## 12. 记忆锚点

- **ADK 定位**：把"多 Agent + 事件流 + HITL"从 Graph 编排里独立出来的高层抽象（v0.5 引入）
- **Agent 接口 3 方法**：Name / Description / Run(→ AsyncIterator[AgentEvent])
- **ChatModelAgent** 是主力实现；内部就是 ReAct 循环（[07](07-react-agent.md)）；首次 Run 才 Compile
- **Runner 4 方法**：Run / Query / Resume / ResumeWithParams
- **`WithCheckPointID` + SQLite CheckPointStore（`checkpoint.DBStore`）** 是本项目 HITL 的持久化底座，重启可恢复
- **AgentEvent 三字段互斥**：Output / Action / Err；**AgentAction 4 类**：Exit / Interrupted / TransferToAgent / BreakLoop
- **`EmitInternalEvents: true`** 让 sub-agent 内部事件冒泡到 root iter
- **Multi-Agent 拓扑 4 种**：AgentAsTool（推荐）/ Workflow (Seq/Par/Loop) / Transfer (不推荐) / 手写 Graph (不推荐)
- **Prebuilt 3 大件**：deep（本项目 deep_research 用）/ supervisor / planexecute
- **HITL 链路**：tool.Interrupt sentinel → framework catch → 保存 checkpoint → 发 Interrupted event → 上层 sink 记 pending → 用户决策 → ResumeWithParams(id, Targets) → 中间件用 GetResumeContext 拿 decision
- **AgentMiddleware（v0.8+）vs ToolMiddleware**：Agent 生命周期 vs 单 tool 调用；本项目自研三个 AgentMiddleware（skills 索引 / workspace / MCP 动态工具），全部走 `BeforeAgent`
- **TurnLoop（v0.9）** 是新的多轮对话运行时，本项目未用
- **本项目拓扑**：手写 supervisor（ChatModelAgent）+ `NewAgentTool` 挂 4 个 sub（deep 用 prebuilt，job / resume / planner 手写 ChatModelAgent），root 装 `EmitInternalEvents`；全拓扑共用一份 effect 驱动的 `approval` + `toolerr` middleware；MCP 工具仅 root、每轮动态注入；checkpoint id = conv id、落 SQLite
