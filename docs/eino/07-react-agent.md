# 07 · ReAct Agent（`flow/agent/react` 老路线源码级拆解）

Eino 有**两条 ReAct 路线**：

- **老路线**：`github.com/cloudwego/eino/flow/agent/react`（v0.1 就有）—— 用 `compose.Graph` 显式搭 ReAct 循环
- **新路线**：`github.com/cloudwego/eino/adk.ChatModelAgent`（v0.5 引入）—— ADK 里封好的高层 Agent，内部**也**是 ReAct 循环，但走 `AsyncIterator[*AgentEvent]` 而不是 Runnable

**本章拆老路线**。新路线单 agent 内部拆解见 [07b-adk-chatmodel-agent.md](07b-adk-chatmodel-agent.md)（图结构、Config 字段、state 差异、事件流、双层 middleware、迁移 checklist）；新路线的**多 Agent 组合**（Runner / Multi-Agent 拓扑 / HITL 完整时序 / 本项目 supervisor 装配）继续在 [08-multi-agent.md](08-multi-agent.md)。老路线的图结构是理解新路线的基础，建议按 07 → 07b → 08 顺序读。

项目里 `internal/agent/agent.go::NewReActAgent` 是老路线的完整用法，但**主流程没用它，只在 ADK 出现前是主入口**。

---

## 1. ReAct 论文一句话

> [ReAct: Synergizing Reasoning and Acting in Language Models (Yao et al., 2022)](https://arxiv.org/abs/2210.03629)：让 LLM **交替**"推理"和"行动"—— 推理指思考下一步做什么、行动指调用工具，直到不再需要工具为止。

对应到实现层，就是一个**循环**：

```
model 生成回复
  │
  ├─ 回复里有 tool_calls？
  │     ├─ 是 → 调这些 tool → 把结果塞回 message history → 再让 model 生成 → 循环
  │     └─ 否 → 结束，返回最后的回复给用户
```

Eino 用一个**含环的 Graph**（Pregel 模式，见 `02-orchestration.md` §4）来表达这个循环。

---

## 2. 图结构（4 个节点）

`flow/agent/react/react.go:329-395` 的建图代码画成图：

```
                   ┌─────────────────────────┐
                   │      compose.START       │
                   └────────────┬────────────┘
                                │  []*schema.Message
                                ▼
              ┌─────────────────────────────────────┐
              │   ChatModel node (nodeKeyModel)      │
              │   - StatePreHandler: 追加 input     │
              │       到 state.Messages 并可选       │
              │       MessageRewriter/Modifier       │
              │   - 输出 *schema.Message (含 tool_calls?)│
              └────────────┬────────────────────────┘
                           │ StreamReader[*schema.Message]
                           ▼
              ┌────────────────────────────────────┐
              │   Branch (modelPostBranchCondition)  │
              │   NewStreamGraphBranch:              │
              │     - toolCallChecker(sr)            │
              │       (只看第 1 个 chunk 判断)        │
              │   →  nodeKeyTools  或  compose.END   │
              └────────┬───────────────────┬────────┘
                       │ 有 tool_calls    │ 无 tool_calls
                       ▼                   ▼
        ┌──────────────────────────┐    ┌───────────────────┐
        │  ToolsNode (nodeKeyTools) │    │   compose.END     │
        │  - StatePreHandler:       │    └───────────────────┘
        │      记 assistant 到 state
        │      检查 ToolReturnDirectly
        │  - 并发跑每个 tool_call    │
        │  - 输出 []*schema.Message │
        │    (tool result messages)  │
        └────────────┬─────────────┘
                     │  这些新 message 会在
                     │  下一轮 modelPreHandle
                     ▼  里再次进 state.Messages
              (回到 ChatModel node — 靠 Pregel AnyPredecessor 允许环)
```

**关键点**：
- **`compose.NodeTriggerMode = AnyPredecessor`（Pregel）** —— 才能有环
- **Branch 用 `NewStreamGraphBranch`** —— 分支条件函数拿的是 `StreamReader[*schema.Message]`，一看到第一个 tool_call chunk 就路由到 tools 节点，不等整个流拼完（首包低延迟）
- **State（`WithGenLocalState`）** —— 一个 `state` 结构体贯穿整个 run，累积 `state.Messages`；节点本身无状态，读写通过 PreHandler 完成

---

## 3. `AgentConfig` 全字段拆解

`flow/agent/react/react.go:136-190`：

```go
type AgentConfig struct {
    // 模型：优先用 ToolCallingModel（WithTools 不修改自己，并发安全）
    ToolCallingModel model.ToolCallingChatModel
    Model            model.ChatModel  // Deprecated
    
    // 工具集 + 中间件
    ToolsConfig compose.ToolsNodeConfig
    
    // 每次调 model 前对 messages 做一次改（临时 prompt 注入）
    MessageModifier MessageModifier
    
    // 类似但改 state.Messages 本体（跨多次 model 调用生效，用于长期改写 / 压缩）
    MessageRewriter MessageModifier
    
    // 循环最大步数（默认 12 = 节点数+10）
    MaxStep int
    
    // 某些工具是"终结型"：调用后直接结束，不再让 model 处理结果
    ToolReturnDirectly map[string]struct{}
    
    // 流式模式下检测第一个 chunk 是否是 tool_call —— Claude 的坑，见 §5
    StreamToolCallChecker func(ctx, *StreamReader[*Message]) (bool, error)
    
    // 图/节点显示名（观测用）
    GraphName     string
    ModelNodeName string
    ToolsNodeName string
}
```

### 3.1 `MessageModifier` vs `MessageRewriter`（易混）

| 项 | 作用位置 | 生效范围 | 何时用 |
|---|---|---|---|
| **MessageModifier** | 每次 model 调用**前**（modelPreHandle 末尾） | 只在**发给 model** 的临时副本上生效；state 里不变 | 加一次性 system prompt / few-shot |
| **MessageRewriter** | 每次 model 调用**前** state 修改（在 Modifier 之前跑） | **写回 state.Messages**，跨多轮生效 | 上下文压缩（超长会话截断历史）、消息清洗 |

先跑 Rewriter 改 state → 再跑 Modifier 生成本次入参。**MessageRewriter 是"长期改写"，Modifier 是"临时装饰"**。

### 3.2 `ToolReturnDirectly` + `SetReturnDirectly`

有些工具的输出本身就是最终答案，不需要 model 再套一层。两条路：

- **静态声明**：`ToolReturnDirectly: map[string]struct{}{"exit": {}, "final_answer": {}}` —— 这些工具一被调用，agent 就用 tool result 作为最终输出结束
- **动态在工具内**：`react.SetReturnDirectly(ctx)` —— 工具执行时判断"这次的输出可以当最终答案"，就调这个 signal（`SetReturnDirectly` 走的是 `compose.ProcessState` 修改 state）

多个 tool_call 都是 return-directly，"只有第一个生效"（原文 line 163）。

面试点：**这是"跳过 model 二次组织"的性能/成本优化位**。适合"文档搜索 → 直接给用户看结果"这类场景。

### 3.3 `MaxStep` 是循环上限

Pregel 的 SuperStep 数量。**默认 12 = 节点数(2) + 10**（保守估计能跑 10 轮 model+tool）。设太小早停、设太大失控烧 token。**本项目 `agent.go` 设了 12**。ADK 的 `ChatModelAgent` 里对应字段叫 `MaxIterations`，本项目设 50（deep_research）/50（job_search）/50（supervisor）。

---

## 4. 状态管理（`state`）

`flow/agent/react/react.go:56` 附近：

```go
type state struct {
    Messages                 []*schema.Message
    ReturnDirectlyToolCallID string
    // ...
}
```

**为什么需要 state 而不是让 messages 沿着 edge 流**：

- Graph 的边只传"本次 chunk 的产物"（model 输出 → tools 输出 → model 输入 …）
- 但 model 每次都需要看到**完整历史**（system + user + 累积的 assistant + tool messages）
- 用 state 累积历史；PreHandler 在每次 model 调用前把 state.Messages 拼出来喂进去

**没有 state 就没有多轮 ReAct**。

### 4.1 modelPreHandle 做的三件事

```go
modelPreHandle := func(ctx, input []*schema.Message, state *state) ([]*schema.Message, error) {
    state.Messages = append(state.Messages, input...)         // 1. 累积
    if config.MessageRewriter != nil {
        state.Messages = config.MessageRewriter(ctx, state.Messages)  // 2. 长期改写
    }
    if messageModifier == nil {
        return state.Messages, nil
    }
    modifiedInput := make([]*schema.Message, len(state.Messages))
    copy(modifiedInput, state.Messages)
    return messageModifier(ctx, modifiedInput), nil            // 3. 临时装饰
}
```

### 4.2 toolsNodePreHandle 的注释暗示 interrupt/resume

```go
toolsNodePreHandle := func(ctx, input *schema.Message, state *state) (*schema.Message, error) {
    if input == nil {
        return state.Messages[len(state.Messages)-1], nil // used for rerun interrupt resume
    }
    ...
}
```

**注释里那句 "used for rerun interrupt resume" 是关键**：中断恢复时，Graph 从上次停下的节点重跑，此时 upstream 不再产 input（因为流已经消费过），preHandle 就从 state 里取最后一条 assistant message 作为"输入"。**这是 v0.7+ HITL 能落到 flow/agent/react 上的实现细节**。

---

## 5. StreamToolCallChecker —— Claude 的坑

Branch 的条件函数是流式的：拿到 `StreamReader[*schema.Message]`，**只看第一个 chunk** 判断有没有 `tool_calls`。默认实现（`firstChunkStreamToolCallChecker`, react.go:218）：

```go
for {
    msg, err := sr.Recv()
    if err == io.EOF { return false, nil }
    if len(msg.ToolCalls) > 0 { return true, nil }   // 命中：走 tools 分支
    if len(msg.Content) == 0 { continue }             // 空 chunk 跳过
    return false, nil                                  // 有 Content 而无 ToolCalls：走 END 分支
}
```

**问题**：这个策略假设"tool_calls 会在流的前面出现"。OpenAI 这样。但 **Claude 是先输出一段自然语言（如 "Let me check…"）再输出 tool_use block**。默认 checker 见到第一个 Content chunk 就判定"无 tool_call → 结束"，**结果 tools 分支永远不进，ReAct 挂了**。

**修法**：自定义 `StreamToolCallChecker`，把整个流读完再判断：

```go
StreamToolCallChecker: func(ctx context.Context, sr *schema.StreamReader[*schema.Message]) (bool, error) {
    defer sr.Close()
    for {
        msg, err := sr.Recv()
        if errors.Is(err, io.EOF) { return false, nil }
        if err != nil { return false, err }
        if len(msg.ToolCalls) > 0 { return true, nil }  // 只要看到就 true
    }
},
```

**代价**：等整个流读完再决定分支 → 首包延迟变高（多等一整个模型响应）。**这是"用 Claude 走 flow/agent/react 老路线的必踩坑"**。

**本项目里怎么处理的**：`agent.go::NewReActAgent` 用 Claude 但**没设** StreamToolCallChecker —— 说明如果真跑起来会挂。但**主流程走 ADK 不走 react.NewAgent**，所以从没触发过。**这是"老路线残留代码可能会误用"的隐藏坑**。

面试聊到"用 Claude 遇到过什么陷阱"，这个可以讲：**Anthropic 的输出顺序和 OpenAI 不一致，直接用 Eino 默认 stream checker 会漏 tool_call**。

---

## 6. 本项目 `NewReActAgent`（老路线残留）

`internal/agent/agent.go:14`：

```go
func NewReActAgent(
    ctx context.Context,
    cm model.ToolCallingChatModel,
    tools []tool.BaseTool,
    systemPrompt string,
) (*react.Agent, error) {
    if cm == nil {
        return nil, fmt.Errorf("ToolCallingChatModel is nil")
    }
    if len(tools) == 0 {
        return nil, fmt.Errorf("at least one tool is required for a ReAct agent")
    }

    cfg := &react.AgentConfig{
        ToolCallingModel: cm,
        ToolsConfig:      compose.ToolsNodeConfig{Tools: tools},
        MaxStep:          12,
    }

    if systemPrompt != "" {
        cfg.MessageModifier = func(_ context.Context, input []*schema.Message) []*schema.Message {
            return append([]*schema.Message{schema.SystemMessage(systemPrompt)}, input...)
        }
    }

    return react.NewAgent(ctx, cfg)
}
```

**这个构造器现在没有调用方**（`grep -rn NewReActAgent internal/` 只有定义），是 ADK 迁移之前的主入口。**留着 = 面试聊架构演进的实物证据**。

三点值得说：

1. **`systemPrompt` 通过 `MessageModifier` 注入**（每次调 model 前 prepend）而不是塞进 state —— 因为如果 append 到 state 会在每次 modelPreHandle 里重复叠加
2. **`ToolsConfig.ToolCallMiddlewares` 没设**（现在项目走 ADK 时装了 `approval.Middleware` + `toolerr.Middleware`）—— 老路线未装 middleware，工具报错会 fatal 整个 run
3. **没设 `StreamToolCallChecker`** —— §5 讲过的 Claude 坑，如果真跑起来会挂

---

## 7. Runnable 的形状

`Agent` 结构（react.go:273）：

```go
type Agent struct {
    runnable         Runnable[[]*schema.Message, *schema.Message]  // Compile 的产物
    graph            *compose.Graph[[]*schema.Message, *schema.Message]
    graphAddNodeOpts []compose.GraphAddNodeOpt
}
```

用法（外部消费）：

```go
agent, _ := react.NewAgent(ctx, cfg)
// Runnable 四范式全都在（03-streaming.md 讲的自动补齐）：
out, err := agent.Generate(ctx, msgs)   // Invoke: [][]Msg → Msg
sr, err := agent.Stream(ctx, msgs)      // Stream:  [][]Msg → StreamReader[Msg]
```

**注意**：这是 **Runnable 派生的 API**，跟 ADK 的 `Agent.Run` 返回 `AsyncIterator[AgentEvent]` **完全不同**。

**返回形态差异（面试对比 ReAct 老路线 vs ADK 新路线的锚点）**：

| 项 | 老路线 `react.Agent` | 新路线 `adk.ChatModelAgent` |
|---|---|---|
| 消费入口 | `agent.Generate` / `agent.Stream` | `runner.Run(ctx, msgs).Next()` |
| 输出 | 完整 `*schema.Message` 或流 | `AsyncIterator[*AgentEvent]` 事件流 |
| 中间过程可见 | 只能靠 Callback 事后 | AgentEvent 本身就是过程 |
| Sub-agent 支持 | 无（要嵌套自己包） | AgentAsTool 一等公民 |
| Interrupt/Resume | v0.7 加，但生态弱 | ADK 默认支持 |
| 观测 | Callback | Callback + AgentEvent |
| 定位 | 类 LangChain agent | 类 LangGraph 但更 Agent-first |

**本项目从老 → 新的迁移动机**：三个 sub-agent + supervisor 的拓扑用 AgentAsTool 表达一次搞定；如果坚持老路线要自己手动嵌套 agent 或者把 sub-agent 包成 `tool.BaseTool` —— 麻烦且丢失 AgentEvent 的 root/sub 谱系信息。

---

## 8. HITL / Interrupt 在 ReAct 图里的落地位

（完整链路见 `08-multi-agent.md`，这里点位置）

老路线的图里，`ToolMiddleware` 是最自然的中断切点：

```
model → Branch → ToolsNode → (ToolCallMiddlewares chain) → tool.InvokableRun
                                       ↑
                                       │
                              approval.Middleware() 在这里返回
                              tool.Interrupt(ctx, info) sentinel
```

框架识别 sentinel → 生成 `Interrupted` action → 保存 checkpoint。恢复时：`toolsNodePreHandle` 里的 `if input == nil { return state.Messages[last] }` 那条路径 —— 让重新进入 ToolsNode 时不需要新的 assistant input，从 state 里取最后一条。middleware 用 `tool.GetResumeContext` 拿 decision 决定 `next(ctx, input)` 或返 denial。

**本项目就是这么做的**（虽然主流程走 ADK，但 ADK 内部也走 ToolsNode + ToolMiddleware）：`internal/approval/middleware.go` 抛 `tool.Interrupt` → `internal/service/chat.go::Resume` 拿到用户决策后 `runner.ResumeWithParams`。

---

## 9. 附：`newToolResultCollectorMiddleware` 是什么

`flow/agent/react/react.go:65` 框架内部一个默认 middleware：把 tool 输出**顺路**塞到 ctx 里，供 `ToolReturnDirectly` 判断"这个 tool 是不是返回后要直接结束"用。**这个 middleware 是自动加的**（react.go:320-323 unconditionally prepend），用户不需要关心。

---

## 10. 记忆锚点

- **两条路线**：`flow/agent/react.NewAgent` (老) vs `adk.NewChatModelAgent` (新)
- **老路线图**：START → ChatModel → Branch(has tool_calls?) → ToolsNode → (回环) or END，**Pregel 允许环**
- **Branch 是流式的**（`NewStreamGraphBranch`）—— 只看第一个 chunk 决策，Claude 的坑源头
- **State**：一个 `state.Messages` 累积历史，节点靠 `WithStatePreHandler` 读写
- **MessageRewriter 长期改写 state / MessageModifier 只装饰本次入参**（Rewriter 先跑）
- **MaxStep 默认 12** = 节点数(2) + 10
- **`ToolReturnDirectly` + `SetReturnDirectly`** —— 让"终结型"工具跳过 model 二次组织
- **Claude 坑**：默认 StreamToolCallChecker 只看第一个 chunk，Claude 先文本后 tool_call 会漏 → 自定义 checker 读完流再判断
- **Interrupt/Resume 落地位**：ToolMiddleware 抛 `tool.Interrupt` sentinel，框架保存 checkpoint；`toolsNodePreHandle` 的 `input == nil` 分支处理重入
- **本项目 `agent.go::NewReActAgent` 是老路线残留**（无调用方），主流程已迁移到 ADK
- **老 vs 新的核心返回形态差**：老返 `Runnable[[]Msg, Msg]`（纯数据），新返 `AsyncIterator[*AgentEvent]`（事件流，带过程）
- **新路线的细节** → [07b-adk-chatmodel-agent.md](07b-adk-chatmodel-agent.md)
