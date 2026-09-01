# 07b · ADK `ChatModelAgent` 单 Agent 内部拆解

**本章聚焦：一个 `adk.ChatModelAgent` 内部的 ReAct 循环到底是怎么跑的**。多 Agent 组合、Runner、AgentAsTool、HITL 完整时序图那些留给 [08-multi-agent.md](08-multi-agent.md)。

前置阅读：[07-react-agent.md](07-react-agent.md)（老路线 `flow/agent/react` 的图结构和 state 概念，是理解本章的基础）、[01-core-abstractions.md](01-core-abstractions.md)（`Agent` 接口 3 方法 vs `Runnable` 4 范式）、[06-callbacks.md](06-callbacks.md)（Callback vs AgentEvent 的差异）。

**为什么单独抽一章**：ADK 的 `ChatModelAgent` 是本项目主力 agent 类型（supervisor / deep_research / job_search 全是它），但它在原来的 07 章里被"老路线附送"、在 08 章里被"多 agent 装配的一部分"，没有一份文档把它当**主角**讲。这一份就是那份主角文档。

---

## 1. 定位

`adk.ChatModelAgent` 是 ADK 里"能自己跑 ReAct 循环、能被组合成 sub-agent、能被中断恢复"的**最小完整 Agent 单位**。

**跟老路线 `react.Agent` 对比一句话**：
- 老路线：**你手搭图，我给你 Runnable**
- 新路线：**你写 config，我给你 Agent（发事件流）**

**跟 ADK 里其他 Agent 类型对比**：

| Agent 类型 | 什么时候用 |
|---|---|
| `ChatModelAgent` | 通用 ReAct，本章主角 |
| `SequentialAgent` / `ParallelAgent` / `LoopAgent` | 确定性流程（前一个输出给后一个 / 并发跑多个 / 反复直到 break） |
| `deep.New(...)` (prebuilt) | 后台研究员风格：内建 filesystem / summarization / todo 等中间件 |
| `supervisor.New(...)` (prebuilt) | 手动 Transfer 语义（本项目不用；见 08） |
| `planexecute.New(...)` (prebuilt) | Plan-Execute-Replan 三段式 |

`ChatModelAgent` 是**其他所有类型的基础**（deep/supervisor/planexecute 内部都实例化它 + 装一堆预置 middleware）。

---

## 2. 三种 RunFunc（不是所有 ChatModelAgent 都走 ReAct）

`adk/chatmodel.go:1373` 附近，根据配置**懒建图**，分派到三种 runFunc：

```go
// pseudo-code
if len(toolInfos) == 0 {
    run, err = a.buildNoToolsRunFunc(ctx)          // (A) 无工具直出
} else {
    run, err = a.buildReActRunFunc(ctx, bc)         // (B) or (C) 有工具走 ReAct
}

// buildReActRunFunc 内部（chatmodel.go:1079）再按 M 分：
switch any(zero).(type) {
case *schema.Message:
    return a.buildMessageReActRunFunc(ctx, bc)     // (B) 传统 ChatModel，需要 ReAct 循环
case *schema.AgenticMessage:
    return a.buildAgenticReActRunFunc(ctx, bc)     // (C) Agentic model 单发（模型自带 agent 语义）
}
```

- **(A) `buildNoToolsRunFunc`** —— 没配任何工具（`ToolsConfig.Tools == nil`）走这条。**根本不走 ReAct 循环**，直接 `model.Generate/Stream` 一发。适合纯 completion / chatbot 型 agent
- **(B) `buildMessageReActRunFunc`** —— 主流路径，本项目所有 agent 都走这条。拓扑跟老路线 `flow/agent/react` 一致（Model → Branch → Tools → 环回），套在 `AsyncIterator` 事件流上
- **(C) `buildAgenticReActRunFunc`** —— 模型自身就是 agentic model（一次响应里含 tool call + 决策链），走"单发但内含循环"的语义。本项目未用

**面试点**：老路线 `react.NewAgent` 拿到任何 model 都建**同一张图**；ADK 三条路径 —— **懒建图 + 按需分派**。首次 `Run` 才真正拼图 `chain.Compile(ctx)`。

---

## 3. `buildMessageReActRunFunc` 的图（等价于老路线 + ADK 加成）

打开 `chatmodel.go:1097` 的拼图代码，等价物：

```
                 ┌─────────────────────────────────────┐
                 │  reactRunInput { input, instruction } │
                 └────────────────┬────────────────────┘
                                  │
                                  ▼
                 ┌────────────────────────────────────┐
                 │  StatePreHandler on Model node       │
                 │   - genModelInput(instruction, input)│  ← ADK 独有：instruction 拼进 messages
                 │   - state.Messages ← 累积            │
                 │   - state.RemainingIterations--      │
                 └────────────────┬────────────────────┘
                                  ▼
                 ┌────────────────────────────────────┐
                 │  Model node（wrapper chain, 外→内）    │
                 │   AgentMW.BeforeChatModel (hook)      │
                 │   Handler.BeforeModelRewriteState     │
                 │   failoverModelWrapper                │
                 │   retryModelWrapper                   │
                 │   eventSenderModelWrapper (发 SSE)    │  ← ADK 独有：每 chunk 一个事件
                 │   Handler.WrapModel (改 IO)           │
                 │   callbackInjectionModelWrapper       │
                 │   Model.Generate/Stream               │
                 │   Handler.AfterModelRewriteState      │
                 │   AgentMW.AfterChatModel (hook)       │
                 └────────────────┬────────────────────┘
                                  ▼
                 ┌────────────────────────────────────┐
                 │  Branch (has tool_calls?)            │  ← 同老路线，StreamGraphBranch
                 └────────┬──────────────────┬────────┘
                          │有                │无
                          ▼                   ▼
              ┌─────────────────────┐     END (发最终 Message 事件)
              │  ToolsNode           │
              │   Middleware chain（外→内）：
              │     eventSenderToolWrapper (发工具事件)
              │     ToolsConfig.ToolCallMiddlewares
              │     AgentMW.WrapToolCall
              │     Handlers.WrapToolCall
              │     callbackInjectedToolCall
              │     Tool.InvokableRun / StreamableRun
              │   - 并发跑 tool_call │
              │   - 返 []Message     │
              └──────────┬──────────┘
                         │
                         ▼
                 (回到 Model node，走下一轮)
```

**跟老路线图的核心差**（对照 [07-react-agent.md §2](07-react-agent.md)）：

1. **入口不是 `[]Message` 而是 `reactRunInput{input, instruction}`** —— ADK 把 system prompt (`Instruction`) 显式抬为一等公民，`GenModelInput` 函数拼装
2. **Model 节点前有 `eventSenderModelWrapper`** —— 把 model 输出转成 `AgentEvent` 发到 iter；老路线只能靠 Callback 事后抓
3. **Model 节点内建 `failoverProxyModel` + `retryModelWrapper`** —— 多模型 failover 和自动重试；老路线要自己在 model 层包
4. **Model 节点前后有 AgentMiddleware/Handler 的多个钩点**（`BeforeChatModel` / `BeforeModelRewriteState` / `WrapModel` / `AfterModelRewriteState` / `AfterChatModel`）—— 老路线只有 `MessageModifier` 一个装饰点
5. **ToolsNode 有第二层 middleware `AgentMW.WrapToolCall` + `Handler.WrapToolCall`** —— 见 §8 双层 middleware
6. **`RemainingIterations` 直接放在 state 里**，每次进 Model preHandle 减一；老路线的 MaxStep 是 Pregel SuperStep 数（框架层管），ADK 是 agent 层显式计数

---

## 4. `ChatModelAgentConfig` 关键字段

`adk/chatmodel.go:260-417`。挑**跟 ReAct 循环本身相关**的字段拆解（Name / Description / Sub-agent 组合放 08 章讲）。

### 4.1 `Instruction` + `GenModelInput`

```go
Instruction   string
GenModelInput TypedGenModelInput[M]   // 默认 defaultGenModelInput
```

**默认行为**（`chatmodel.go:167 defaultGenModelInput`）：

```go
msgs := make([]Message, 0, len(input.Messages)+1)
if instruction != "" {
    sp := schema.SystemMessage(instruction)
    if vs := GetSessionValues(ctx); len(vs) > 0 {
        // FString 模板：instruction 里 "{User}" 之类占位符被填成 session value
        ct := prompt.FromMessages(schema.FString, sp)
        ms, _ := ct.Format(ctx, vs)
        sp = ms[0]
    }
    msgs = append(msgs, sp)
}
msgs = append(msgs, input.Messages...)
return msgs, nil
```

**跟老路线的 `MessageModifier` 对比**：

| 项 | 老路线 `MessageModifier` | ADK `GenModelInput` |
|---|---|---|
| 触发点 | 每次 model 调用前（modelPreHandle 末尾） | 每次 model 调用前（Model 节点 StatePreHandler） |
| 入参 | `[]*schema.Message` | `instruction string, input *AgentInput` |
| 是否含 system prompt | 不含（`Modifier` 自己 prepend） | **含**（`instruction` 独立参数） |
| 会话上下文 | 无 | `GetSessionValues(ctx)` 可注入运行时变量 |

**关键差**：ADK 把 system prompt **提到 config 顶层**（`Instruction` 字段），并允许**运行时用 SessionValues 替换占位符**。老路线的 `MessageModifier` 是纯函数，注入运行时数据要自己在 ctx 里传。

**本项目**（`adk_agent.go` 里的 supervisor / deep_research / job_search）: 直接用 `Instruction` 字段配 system prompt，没自定义 `GenModelInput`。

**踩坑警告**：Instruction 里如果**真的含大括号**（例如输出 JSON schema 的示例），默认 FString 引擎会把它当占位符炸掉。要么转义 `{{`，要么写自定义 `GenModelInput` 跳过 FString。

### 4.2 `MaxIterations`

```go
MaxIterations int  // 默认 20
```

**跟老路线的 `MaxStep` 对比**：

| 项 | 老路线 `MaxStep` | ADK `MaxIterations` |
|---|---|---|
| 语义 | Pregel SuperStep 上限（框架层） | ReAct 循环迭代次数（agent 层） |
| 计数点 | 每次 START→END 通过 | 每次 model call |
| 默认 | 12 = 2 nodes + 10 | 20 |
| 超限 | 返 `ErrExceedMaxStep` | agent 层直接 fail |
| 本项目 | 老 `NewReActAgent` 设 12 | 全部设 50（supervisor / deep / job） |

**为什么 ADK 设更大**：ADK 场景通常带 sub-agent（AgentAsTool），一次外层任务里可能触发多轮 sub-agent 调用，每次 sub 调用都算 root 的一次迭代（因为 sub-agent 是 tool call），50 才够用。

### 4.3 `Exit` + `ExitTool`（老路线没有的独立能力）

```go
Exit tool.BaseTool   // 一般直接用 adk.ExitTool{}
```

**语义**：model 显式调 `exit(final_result="...")` 主动终结 agent，把 `final_result` 作为最终输出返上层。等价于老路线的 `ToolReturnDirectly` + 一个专门的 exit 工具，但 ADK 内建 + 发 `AgentAction.Exit` 事件让上层能识别。

**`ExitTool.InvokableRun`（`chatmodel.go:603`）**：

```go
func (et ExitTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
    params := &exitParams{FinalResult: ""}
    _ = sonic.UnmarshalString(argumentsInJSON, params)
    _ = SendToolGenAction(ctx, "exit", NewExitAction())   // ← 塞进 state.toolGenActions
    return params.FinalResult, nil                         // ← 返内容给 model；下一轮 preHandle 拿 action 短路
}
```

**跟老路线 `ToolReturnDirectly` 对比**：

| 项 | 老路线 `ToolReturnDirectly` | ADK `ExitTool` |
|---|---|---|
| 声明方式 | Config 里 map；或工具内 `SetReturnDirectly` | 一个真的 tool，模型显式调 |
| 语义 | "这个工具的输出直接是答案" | "任何结论 model 想给的，都通过 exit 结束" |
| 事件 | 无特殊事件 | 发 `AgentAction{Exit: true}` |
| 模型侧感知 | 无（工具就是普通工具） | 有（model 明确知道自己要 exit） |
| 本项目 | 老 `NewReActAgent` 未用 | supervisor / deep / job 都没配 —— 让 model 自然停在"无 tool_call"的一轮，走 END 分支 |

**为什么本项目没用 ExitTool**：ExitTool 更适合"model 需要显式声明任务完成"的场景（例如 supervisor 明确说"我已交付完成"）；本项目 agent 是对话式的，一轮 model 没再 emit tool_call 就自然停，够用。

### 4.4 `ModelRetryConfig` + `ModelFailoverConfig`（老路线要自己包）

```go
ModelRetryConfig    *TypedModelRetryConfig[M]
ModelFailoverConfig *TypedModelFailoverConfig[M]
```

- **Retry**：model call 失败自动重试，支持指数退避、指定错误类型
- **Failover**：配多个模型（primary + fallback），primary 失败切 fallback，成功后回粘到 primary

**老路线怎么做**：自己在传给 `AgentConfig.ToolCallingModel` 之前包一层带 retry 的 model。ADK 把这两件事内建到 Model wrapper chain，配一下就好。**面试点**：ADK v0.7+ 把"模型可用性问题"从业务代码里抽出来。

**本项目未用** —— 单 Anthropic 兼容端点，出问题让上层重跑更简单。

### 4.5 `OutputKey`（把 agent 输出写进 SessionValues）

```go
OutputKey string
```

设了以后，agent 跑完把 `msg.Content` 塞进 `AddSessionValue(ctx, outputKey, content)`。**给同一 Runner 里其他 agent 用**（例如 Sequential Agent 里前一个 agent 的输出给后一个当 session 变量）。老路线没有 session 概念。

**本项目未用**（对话式 agent，用不到跨 agent 的静态状态传递）。

### 4.6 `ToolsConfig`（内嵌 `compose.ToolsNodeConfig`）

```go
type ToolsConfig struct {
    compose.ToolsNodeConfig                    // 内嵌 Graph 层的 ToolsNode config
    EmitInternalEvents bool                    // ★ 关键：让 sub-agent 内部事件冒泡
}
```

**主要字段**（从 `compose.ToolsNodeConfig` 继承）：
- `Tools []tool.BaseTool` —— 工具列表
- `ToolCallMiddlewares []compose.ToolMiddleware` —— **单 tool 级别**的中间件（approval / toolerr 就装在这里）

**ADK 独有的 `EmitInternalEvents`**：本 agent 作为 sub-agent 被别人挂时，内部 assistant/tool 事件是否冒泡到 root iter。**本项目 supervisor 设为 `true`** —— 前端要看到 deep_research 内部在干什么，不是"黑盒等结果"。详细见 08 章。

---

## 5. state (`typedState[M]`)

`adk/react.go:35-63`：

```go
type typedState[M MessageType] struct {
    Messages                 []M
    RemainingIterations      int
    ReturnDirectlyToolCallID string
    ReturnDirectlyEvent      *TypedAgentEvent[M]
    RetryAttempt             int
    toolGenActions           map[string]*AgentAction  // exit / breakLoop / transferToAgent 等 tool 侧 emit 的 action
    toolMsgIDs               map[string]string
}

type State = typedState[*schema.Message]
```

**跟老路线 `state`（`flow/agent/react/react.go:56`）对比**：

| 字段 | 老路线 state | ADK typedState |
|---|---|---|
| Messages | ✓ | ✓ |
| MaxStep / RemainingIterations | 框架层 SuperStep 计数 | `RemainingIterations`（agent 层） |
| ReturnDirectlyToolCallID | ✓ | ✓ |
| ReturnDirectlyEvent | — | ✓（ADK 事件流原生，需要存整个 event） |
| RetryAttempt | — | ✓（`ModelRetryConfig` 用） |
| toolGenActions | — | ✓（ExitTool / BreakLoopTool 等 emit 的 Action，Model preHandle 里读并短路） |
| toolMsgIDs | — | ✓（一次 assistant 里多个 tool_call 都并发跑，需要用 callID 定位对应的 SSE 消息 id） |

**核心增量**：ADK 的 state 除了消息历史，还要**承载框架层信号**（Action / RetryAttempt / MsgID）—— 因为 ADK 输出是**事件流**而不是纯数据，工具-agent 之间的信号要经过 state 传递。

**`toolGenActions` 用法**（`adk/react.go:274 SendToolGenAction`）：

```go
// 工具内部调用
err = SendToolGenAction(ctx, "exit", NewExitAction())
```

写进 `state.toolGenActions[callID]`。Model preHandle 里遍历 tool result messages，如果发现有关联的 action，就把 action 塞进 event 短路后续。

**面试点**：老路线的"状态"就是消息 + 简单标志；ADK 的"状态"要多带**信号 & 通信**信道，因为它要维护事件流语义。

---

## 6. Compile 时机差异

| 项 | 老路线 `react.NewAgent` | ADK `NewChatModelAgent` |
|---|---|---|
| Compile 触发 | `NewAgent` 内部立刻建图并 Compile | `NewChatModelAgent` **只存 config**；首次 `Run` 触发 `buildXxxRunFunc` → `chain.Compile(ctx)` |
| 好处 | 早失败（config 有问题构造就报错） | 允许运行时改 Config（例如 Middleware 动态加）；三种 runFunc 按 M 分派 |
| 坏处 | Config 一定要在 New 时就齐备 | 首次 Run 有一点点建图成本；Config 错要跑起来才发现 |
| 编译产物类型 | `Runnable[[]*schema.Message, *schema.Message]` | 内部 `typedRunFunc[M]`，用户看不到 |

**面试点**：ADK 的**懒 Compile** 是"config-first / lazy build"的设计选择 —— 更适合 middleware chain 这种**动态叠加**的东西；老路线的"eager Compile"更适合"图结构定死、编译期就想验证"的传统 Runnable 用法。

---

## 7. 事件流 vs Runnable —— 消费形态的核心差

### 7.1 老路线：Runnable 四范式

```go
agent, _ := react.NewAgent(ctx, cfg)

msg, err := agent.Generate(ctx, msgs)   // Invoke
sr, err := agent.Stream(ctx, msgs)      // Stream
// Collect/Transform 靠框架自动补齐
```

**输出**：一次调用 → 一个 message 或一个 stream。**过程不可见**（只能挂 Callback 事后抓）。

### 7.2 新路线：AsyncIterator 事件流

```go
runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true})
iter := runner.Run(ctx, msgs, adk.WithCheckPointID(convID))

for {
    ev, ok := iter.Next()
    if !ok { break }
    // ev = *AgentEvent { AgentName, Output?, Action?, Err? }
}
```

**输出**：一次调用 → **一串事件**。每个事件三态互斥（Output / Action / Err）。**过程即事件**（model 说话、tool result、interrupt 都是事件）。

### 7.3 `AgentEvent` 三态互斥

```go
type AgentEvent = TypedAgentEvent[*schema.Message]

type TypedAgentEvent[M MessageType] struct {
    AgentName string                     // 谁产生的 (root vs sub-agent)
    RunPath   []RunStep                  // root→当前的路径（v0.7 加）
    Output    *TypedAgentOutput[M]       // 输出（message 或 stream）
    Action    *AgentAction               // 动作（Exit / Interrupted / Transfer / BreakLoop）
    Err       error                      // 错误
}
```

**一次事件走 Output / Action / Err 三路之一**，不会同时出现。消费方 `internal/stream/adk_handler.go::ConsumeADKEvents` 分派：

```go
for {
    ev, ok := iter.Next()
    if !ok { return nil }
    if ev.Err != nil { return ev.Err }
    if ev.Action != nil && ev.Action.Interrupted != nil {
        emitApprovalRequired(...)        // HITL 中断，08 章讲
        continue
    }
    if ev.Output == nil { continue }    // Action-only 事件
    // Output.MessageOutput.IsStreaming? → 消费 stream
    // Role=Tool? → tool result
    // 其他 → 完整 assistant message
}
```

### 7.4 关键差表

| 项 | 老路线 `react.Agent` | 新路线 `adk.ChatModelAgent` |
|---|---|---|
| 消费入口 | `agent.Generate` / `agent.Stream` | `runner.Run(ctx, msgs).Next()` |
| 输出 | 完整 `*schema.Message` 或流 | `AsyncIterator[*AgentEvent]` 事件流 |
| 中间过程可见 | 只能靠 Callback 事后 | AgentEvent 本身就是过程（tool call / result / thinking 都单独发一条） |
| 事件带 agent 归属 | 无 | `AgentEvent.AgentName` —— sub-agent 冒泡的事件带自己的 name（`EmitInternalEvents=true` 前提） |
| Sub-agent 支持 | 无（要嵌套自己包） | `NewAgentTool` 一等公民（08 章讲） |
| Interrupt/Resume | v0.7 加，但生态弱 | ADK 默认支持 |
| 中间件层数 | 只有 ToolMiddleware | ToolMiddleware **+ AgentMiddleware**（§8） |

**为什么本项目走 ADK**：三个 sub-agent（supervisor + deep_research + job_search）+ HITL approval + 前端要展示"每个 sub-agent 现在在干什么"。老路线要自己攒事件流、自己包 sub-agent、自己实现 interrupt —— 每一件都可能翻车。ADK 把这三样都封成一等公民。

---

## 8. 双层 Middleware —— ADK 独有

老路线只有一层中间件：**ToolMiddleware**（`compose.ToolsNodeConfig.ToolCallMiddlewares`），拦截单个 tool 调用。

ADK 加了**第二层**：**AgentMiddleware**（`ChatModelAgentConfig.Middlewares` / `Handlers`），作用于 agent 完整生命周期。

### 8.1 两层的作用域

```
                   AgentMiddleware
   ┌──────────────────────────────────────────────────────────┐
   │                                                            │
   │   BeforeAgent hook                                          │
   │                                                            │
   │   ┌─────────────────────────────────────────────────┐    │
   │   │           一轮 ReAct 循环                          │    │
   │   │                                                    │    │
   │   │   BeforeChatModel / BeforeModelRewriteState        │    │
   │   │   ┌──────────────────────────────────────────┐    │    │
   │   │   │   WrapModel chain（可以改 model IO）       │    │    │
   │   │   │   ...failover / retry / eventSender...    │    │    │
   │   │   │   Model.Generate/Stream                    │    │    │
   │   │   └──────────────────────────────────────────┘    │    │
   │   │   AfterModelRewriteState / AfterChatModel          │    │
   │   │                                                    │    │
   │   │   ┌──────────────────────────────────────────┐    │    │
   │   │   │   ToolsNode                               │    │    │
   │   │   │   eventSender → ToolsConfig.MW → AgentMW  │    │    │
   │   │   │     .WrapToolCall → Handlers.WrapToolCall │    │    │
   │   │   │     → Tool.InvokableRun                    │    │    │
   │   │   └──────────────────────────────────────────┘    │    │
   │   │                                                    │    │
   │   └─────────────────────────────────────────────────┘    │
   │                                                            │
   │   AfterAgent hook                                           │
   └──────────────────────────────────────────────────────────┘
```

### 8.2 什么放 AgentMiddleware，什么放 ToolMiddleware

| 需求 | 该放哪里 | 例子 |
|---|---|---|
| 拦一个 tool 决定跑不跑 / 改结果 | ToolMiddleware | approval / toolerr / cache |
| agent 开跑前修改 tool 列表 | AgentMiddleware BeforeAgent | 动态工具挑选（`adk/middlewares/dynamictool`） |
| 每次 model call 前压缩历史 | AgentMiddleware BeforeModelRewriteState | `adk/middlewares/summarization` |
| 每次 model call 后改 assistant message | AgentMiddleware AfterModelRewriteState | `adk/middlewares/patchtoolcalls` |
| 装载 skill / todo / filesystem 生态 | AgentMiddleware | `adk/middlewares/skill` 等 |

**本项目**：只用 ToolMiddleware（`approval` + `toolerr`），因为 skill 加载是自己写的 `skills.Loader`（不是 middleware 语义）；DeepAgent 内部自动装了一堆 AgentMiddleware（filesystem / summarization 等）不需要业务层再叠。

### 8.3 `Handlers` vs `Middlewares`（v0.8+ 的过渡）

Config 里有两个字段：

```go
Middlewares []AgentMiddleware                     // Deprecated in v0.8+
Handlers    []TypedChatModelAgentMiddleware[M]    // 推荐
```

- **Handlers 是接口版**（自定义 struct 实现方法，可以带字段和自定义方法）
- **Middlewares 是 struct 版**（旧，纯闭包）

行为对齐，Handlers 在 Middlewares 之后按注册顺序跑。**新代码写 Handlers 就行**。

**面试点**：ADK **v0.8 加入的 AgentMiddleware 双层生态是相对老路线的最大架构升级** —— 让 agent 能力扩展不用改 agent 代码；老路线要新能力，得改 `react.NewAgent` 或者塞进 ToolMiddleware（能力错位）。

### 8.4 `adk/middlewares/` 现成生态

| 目录 | 作用 |
|---|---|
| `filesystem` | 给 agent 挂 filesystem 工具集 |
| `skill` | 加载 SKILL.md 定义的技能包 |
| `summarization` | 上下文超长时自动压缩历史 |
| `reduction` | Tool 返回结果超长时截断 / 压缩 |
| `plantask` | 类似 Plan-Execute 但更轻，让 model 写 todo list |
| `toolsearch` | 工具集太大时先让 model "搜工具"再调 |
| `patchtoolcalls` | 修复模型输出的错格式 tool_call |
| `agentsmd` | 加载 AGENTS.md 目录指令（类似 Claude Code 的 CLAUDE.md） |
| `dynamictool` | 运行时动态注册/注销 tool |

**本项目未直接用**（DeepAgent 内部装了一部分；skill 自己写）。

---

## 9. `ResumableAgent` 与内建 HITL

老路线要接 HITL，得自己在 ToolMiddleware 里抛 `tool.Interrupt` sentinel + 让框架保存 checkpoint（v0.7+ 才勉强支持）。

ADK 的 `ChatModelAgent` **默认就是 `ResumableAgent`**：

```go
type ResumableAgent = TypedResumableAgent[*schema.Message]

type TypedResumableAgent[M MessageType] interface {
    TypedAgent[M]
    Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption)
        *AsyncIterator[*TypedAgentEvent[M]]
}
```

三件事内建：

1. **中断 sentinel 识别** —— ToolMiddleware 里返 `tool.Interrupt(ctx, info)`，ADK 框架自动捕获
2. **CheckPoint 持久化** —— Runner 的 `CheckPointStore` 存图状态（默认内存 store，可换 Redis）
3. **`ResumeWithParams(ctx, checkpointID, params)`** —— 恢复时把用户决策塞回 ctx，middleware 用 `tool.GetResumeContext[Decision]` 拿到

**完整 HITL 时序图**（触发 → 保存 → 用户决策 → 恢复）放在 [08-multi-agent.md §8](08-multi-agent.md)，因为要跟本项目 supervisor + approval middleware 装配一起讲。

**面试点**：**HITL 是 ADK 相对老路线的"第二个决定性升级"**（第一个是事件流）。老路线要 HITL 得攒半天；ADK 抛 `tool.Interrupt` 一行就够。

---

## 10. 老 → 新 迁移 checklist

从 `react.NewAgent` 迁到 `adk.NewChatModelAgent` 的字段映射：

| 老 (`react.AgentConfig`) | 新 (`adk.ChatModelAgentConfig`) | 备注 |
|---|---|---|
| `ToolCallingModel` | `Model` | 类型同 |
| `ToolsConfig` (`compose.ToolsNodeConfig`) | `ToolsConfig.ToolsNodeConfig` | ADK 再包了一层 `adk.ToolsConfig` 加 `EmitInternalEvents` |
| `MessageModifier` (prepend system prompt) | `Instruction` | 直接给字符串就行 |
| `MessageModifier` (改本次入参非 system) | `GenModelInput` | 自定义拼装逻辑 |
| `MessageRewriter` (长期改 state.Messages) | AgentMiddleware `BeforeModelRewriteState` | 现在是中间件而不是函数指针 |
| `MaxStep` | `MaxIterations` | 记得从 12 加到 20+（ADK 默认 20，本项目 50） |
| `ToolReturnDirectly` | ~~保留~~ / 换 `Exit` + `ExitTool` | ADK 更倾向让 model 显式 exit |
| `StreamToolCallChecker` | ~~不需要~~ | ADK 内部另外一套 tool_call 检测，不受 Claude 顺序问题影响 |
| — | `Handlers` / `Middlewares` | 新增：AgentMiddleware 生态 |
| — | `ModelRetryConfig` / `ModelFailoverConfig` | 新增：内建重试和 failover |
| — | `OutputKey` | 新增：跨 agent 传 session 变量（本项目未用） |
| — | `Exit` (ExitTool) | 新增：显式终结 |
| 消费：`agent.Generate/Stream` | `runner.Run(...).Next()` | 从 Runnable 迁到事件流是最大改动 |

**本项目 `NewReActAgent` → `NewInterviewADKAgent` 的实际迁移点**（对照 `internal/agent/agent.go` vs `internal/agent/adk_agent.go`）：

1. **`react.NewAgent(cfg)` → `adk.NewChatModelAgent(ctx, cfg)`**：cfg 换成 ADK 版
2. **`systemPrompt` 从 `MessageModifier` 移到 `Instruction`**：不再手动 prepend
3. **`MaxStep: 12` → `MaxIterations: 50`**：为多 agent 委派留余量
4. **新增 Runner 层**：`adk.NewRunner(ctx, adk.RunnerConfig{Agent: root, EnableStreaming: true, CheckPointStore: ...})`
5. **消费方式改**：`for ev := iter.Next() ... switch ev.Output/Action/Err`（`internal/stream/adk_handler.go::ConsumeADKEvents`）
6. **HITL 中间件**：`approval.Middleware()` 从概念（老路线里根本没接过）变成 ToolMiddleware 装到 `ToolsConfig.ToolCallMiddlewares`
7. **Sub-agent**：`deep.New(...)` 一个 + `adk.NewChatModelAgent(...)` 三个，共 **4 个 sub-agent** 经 `adk.NewAgentTool(...)` 挂给 supervisor（详见 08）
8. **Agent 中间件**：`Handlers: []adk.ChatModelAgentMiddleware{...}` 的 `BeforeAgent` 每轮注入 workspace / 技能索引 / MCP 工具——这是应对「图编译后工具表被冻结」的必需品（详见 [10 §5](10-in-this-project.md)）

---

## 11. 本项目 `NewInterviewADKAgent` 简化装配（详见 08）

```go
// internal/agent/adk_agent.go

// 1. deep_research (prebuilt DeepAgent)
deepAgent, _ := deep.New(ctx, &deep.Config{
    Name:                   "deep_research",
    ChatModel:              cm,
    Instruction:            deepResearchInstruction,
    MaxIteration:           50,
    WithoutWriteTodos:      true,
    WithoutGeneralSubAgent: true,
    ToolsConfig: adk.ToolsConfig{
        ToolsNodeConfig: compose.ToolsNodeConfig{
            Tools:               baseTools,
            ToolCallMiddlewares: []compose.ToolMiddleware{approval.Middleware(), toolerr.Middleware()},
        },
    },
})

// 2. job_search (纯 ChatModelAgent)
jobAgent, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Name:          "job_search",
    Instruction:   jobSearchInstruction,
    Model:         cm,
    MaxIterations: 50,
    ToolsConfig: adk.ToolsConfig{
        ToolsNodeConfig: compose.ToolsNodeConfig{
            Tools:               baseTools,
            ToolCallMiddlewares: []compose.ToolMiddleware{approval.Middleware(), toolerr.Middleware()},
        },
    },
})

// 3. supervisor (root ChatModelAgent)
supervisor, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Name:          "supervisor",
    Instruction:   supervisorInstruction,
    Model:         cm,
    MaxIterations: 50,
    ToolsConfig: adk.ToolsConfig{
        ToolsNodeConfig: compose.ToolsNodeConfig{
            Tools:               append(baseTools, adk.NewAgentTool(ctx, deepAgent), adk.NewAgentTool(ctx, jobAgent)),
            ToolCallMiddlewares: []compose.ToolMiddleware{approval.Middleware(), toolerr.Middleware()},
        },
        EmitInternalEvents: true,   // ★ sub-agent 事件冒泡
    },
})

// 4. Runner (root 是 supervisor)
runner := adk.NewRunner(ctx, adk.RunnerConfig{
    Agent:           supervisor,
    EnableStreaming: true,
})
```

**关键设计选择清单**（每个都有面试价值）：

1. **走 AgentAsTool 不走 Transfer**：语义简单，边界清晰，UI 天然能挂"deep_research 的进度到 root 的 deep_research 工具卡片下面"
2. **`EmitInternalEvents: true`**：sub-agent 事件冒泡，前端能看到 deep_research 内部在干什么，不是"黑盒等结果"
3. **DeepAgent 用 prebuilt / JobSearch 手写 ChatModelAgent**：DeepAgent 有一堆 middleware（todo / summarization / …）适合"后台研究员"这种复杂任务；job_search 只是加载 skill + 抓 boss 直聘，用轻量 ChatModelAgent 就够
4. **`WithoutGeneralSubAgent: true`** + **`WithoutWriteTodos: true`**：DeepAgent 默认会挂"general sub-agent"允许它 spawn 子任务 → 不想让 deep_research 再套娃；DeepAgent 默认还挂 todo-writing middleware → 项目里 workspace 文件工具够用不需要
5. **每个 agent 都装 approval + toolerr middleware（顺序：approval 外 / toolerr 内）**
6. **checkpoint id = conversation id**：一个 conv 一次活跃 run，简单可靠
7. **root supervisor 的 MaxIterations=50**：允许多轮委派 sub-agent + 自己直接调工具组合

---

## 12. 记忆锚点

- **`ChatModelAgent` 是 ADK 里最小完整 Agent 单位**；deep/supervisor/planexecute 内部都是它 + 一堆预置 middleware
- **三种 RunFunc 分派**：`buildNoToolsRunFunc`（无工具直出）/ `buildMessageReActRunFunc`（主流）/ `buildAgenticReActRunFunc`（agentic model 单发）
- **内部图与老路线拓扑等价**，多出的：入口带 `Instruction`（走 `GenModelInput`）、Model 前套 `eventSenderModelWrapper` + failover + retry、ToolsNode 有第二层 AgentMiddleware `WrapToolCall`
- **Instruction + GenModelInput 替代 MessageModifier**，默认 `defaultGenModelInput` 走 FString 模板 + SessionValues（Instruction 里字面大括号要转义 `{{`）
- **MaxIterations 默认 20**（本项目 50），是 agent 层显式计数，不是 Pregel SuperStep
- **`Exit` + `ExitTool` 替代 ToolReturnDirectly**：model 显式调 `exit(final_result=...)` 主动终结，发 `AgentAction.Exit` 事件；本项目未用
- **`ModelRetryConfig` / `ModelFailoverConfig`**：内建重试和多模型 failover；老路线要自己在 model 上包
- **`OutputKey`**：把 agent 输出写进 SessionValues 供后续 agent 用；本项目未用
- **`ToolsConfig.EmitInternalEvents`**：sub-agent 事件冒泡的开关，本项目 supervisor 设 true
- **typedState** 除消息历史还带：`RemainingIterations` / `ReturnDirectlyEvent` / `RetryAttempt` / `toolGenActions` / `toolMsgIDs`
- **Compile 时机**：`NewChatModelAgent` 只存 config；首次 `Run` 才 `chain.Compile(ctx)` —— 允许运行时改 config，代价是 config 错要跑起来才知道
- **事件流三态互斥**：`AgentEvent{Output? Action? Err?}`；`AgentAction` 四类：`Exit / Interrupted / TransferToAgent / BreakLoop`
- **双层 Middleware**：ToolMiddleware（拦单 tool）+ AgentMiddleware（拦 agent 生命周期），v0.8 起用 `Handlers` 字段是推荐版，`Middlewares` 已 Deprecated
- **`ChatModelAgent` 默认是 `ResumableAgent`**：内建 `tool.Interrupt` sentinel + CheckPointStore + `ResumeWithParams`，HITL 一行就够
- **老 vs 新最锋利的两个对比**：
  1. 返回形态：老返 `Runnable[[]Msg, Msg]`（纯数据），新返 `AsyncIterator[*AgentEvent]`（事件流，带过程 + AgentName + Action）
  2. 扩展方式：老只有 ToolMiddleware 一层；新有 ToolMiddleware + AgentMiddleware 双层
- **本项目走 ADK 的三个决定性理由**：多 sub-agent 事件冒泡、HITL approval 内建 checkpoint、多个 agent 都能挂同一套 middleware
