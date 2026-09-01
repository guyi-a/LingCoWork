# 04 · ChatModel 与 Message

Eino 里最常打交道的两个东西：**大模型接口** 和 **消息结构**。这一章把接口层次、Message 各字段、多模态、Concat 以及 Prompt 模板讲透。

---

## 1. ChatModel 的接口层次

源码 `components/model/interface.go`。整个接口体系有 4 层：

```
                    BaseModel[M messageType]      ← 泛型底座
                             △
             ┌───────────────┼────────────────┐
             │                                │
       BaseChatModel                     AgenticModel
      = BaseModel[*schema.Message]  = BaseModel[*schema.AgenticMessage]
             △
             │
       ┌─────┴──────┐
   ChatModel   ToolCallingChatModel   ← 上层：加"绑工具"能力
   (Deprecated)   (推荐)
```

### 1.1 BaseModel[M]：泛型底座

```go
type messageType interface {
    *schema.Message | *schema.AgenticMessage
}

type BaseModel[M messageType] interface {
    Generate(ctx context.Context, input []M, opts ...Option) (M, error)
    Stream(ctx context.Context, input []M, opts ...Option) (*schema.StreamReader[M], error)
}
```

两个方法对应 `01` 讲的 Invoke 和 Stream 范式。**ChatModel 不实现 Collect / Transform**，因为"给一批消息、返回结果"天然是"单值输入 → 单值 或 流"的形态。

### 1.2 BaseChatModel：Message 特化

```go
type BaseChatModel = BaseModel[*schema.Message]
```

**类型别名**（不是新类型），最常用的接口——市面上绝大多数 ChatModel 就是它。

### 1.3 ChatModel（Deprecated，别用）

```go
type ChatModel interface {
    BaseChatModel
    BindTools(tools []*schema.ToolInfo) error   // 就地修改，非并发安全
}
```

**为什么废弃**：`BindTools` 就地修改 receiver。同一个 model 实例被多 goroutine 共享时，一个 goroutine 的 tools 会覆盖另一个的。经典的可变共享状态陷阱。

### 1.4 ToolCallingChatModel（推荐）

```go
type ToolCallingChatModel interface {
    BaseChatModel
    WithTools(tools []*schema.ToolInfo) (ToolCallingChatModel, error)
}
```

`WithTools` **不修改自己**，**返回一个新实例**。可以安全并发：

```go
base, _ := claude.NewChatModel(ctx, cfg)        // 共享 base，无工具
withSearch, _ := base.WithTools([]*schema.ToolInfo{searchTool})
withCalc, _   := base.WithTools([]*schema.ToolInfo{calcTool})
```

本项目 `internal/agent/llm/llm.go:14` 就返回这个接口：

```go
func NewChatModel(ctx context.Context, cfg config.LLMConfig) (model.ToolCallingChatModel, error) {
    ...
    return claude.NewChatModel(ctx, cc)
}
```

**面试锚点**：能立刻讲出 "为什么用 `ToolCallingChatModel` 而不用 `ChatModel`" —— 并发安全的不可变模式 vs 就地修改的经典陷阱。

### 1.5 AgenticModel（Beta）

```go
type AgenticModel = BaseModel[*schema.AgenticMessage]
```

针对新的 `AgenticMessage` 消息形态（更适合 Agent 场景）的模型。**注意它没有 `WithTools`** —— 工具通过 `model.WithTools` 这个 CallOption 在请求时传，和 ChatModelAgent 绑工具的方式一致。**本项目没用它**（还在 Beta）。

---

## 2. `schema.Message` 结构详解

**Eino 里最重要的一个数据结构**（`schema/message.go:497`）。全字段：

```go
type Message struct {
    Role RoleType `json:"role"`

    // 主要文本内容（用户输入 or 模型文本输出）
    Content string `json:"content"`

    // 多模态：用户侧（Deprecated 老字段是 MultiContent）
    UserInputMultiContent []MessageInputPart `json:"user_input_multi_content,omitempty"`

    // 多模态：模型输出侧
    AssistantGenMultiContent []MessageOutputPart `json:"assistant_output_multi_content,omitempty"`

    Name string `json:"name,omitempty"`   // 参与者名字（很少用）

    // 只 AssistantMessage 用：模型要求调用工具
    ToolCalls []ToolCall `json:"tool_calls,omitempty"`

    // 只 ToolMessage 用：对应哪个 tool_call
    ToolCallID string `json:"tool_call_id,omitempty"`
    ToolName   string `json:"tool_name,omitempty"`

    // 响应元信息：FinishReason / Usage / LogProbs
    ResponseMeta *ResponseMeta `json:"response_meta,omitempty"`

    // 推理模型的思考链（Claude thinking / DeepSeek-R1 之类）
    ReasoningContent string `json:"reasoning_content,omitempty"`

    // 底层实现塞的杂项
    Extra map[string]any `json:"extra,omitempty"`
}
```

### 2.1 RoleType 四种（`message.go:108`）

```go
const (
    Assistant RoleType = "assistant"  // 模型回复
    User      RoleType = "user"       // 用户输入
    System    RoleType = "system"     // 系统 prompt / 指令
    Tool      RoleType = "tool"       // 工具执行结果
)
```

对应 OpenAI Chat Completions 的四种消息类型，Eino 直接沿用。

### 2.2 ToolCall 结构（`message.go:132`）

```go
type ToolCall struct {
    Index    *int   `json:"index,omitempty"`  // 流式 chunk 合并时用
    ID       string `json:"id"`
    Type     string `json:"type"`             // 一般 "function"
    Function FunctionCall `json:"function"`
    Extra    map[string]any `json:"extra,omitempty"`
}

type FunctionCall struct {
    Name      string `json:"name,omitempty"`
    Arguments string `json:"arguments,omitempty"`  // 是 JSON 字符串，不是对象
}
```

**注意**：`Arguments` 是**字符串**，不是已解析的对象。这是为了跟 OpenAI/Anthropic API 的原始返回对齐。你要用参数得 `json.Unmarshal(tc.Function.Arguments, &yourStruct)`。

本项目 `internal/stream/adk_handler.go:225-233` 就是这么把 tool_call 转成 SSE 帧的：

```go
for _, tc := range full.ToolCalls {
    buf.Append(Encode(Frame{
        Type:     "tool_call",
        ID:       tc.ID,
        Name:     tc.Function.Name,
        ArgsJSON: tc.Function.Arguments,  // 原样透传
    }))
    ...
}
```

前端拿到 `ArgsJSON` 字符串自己解析（因为不同工具的 args 结构不一样）。

### 2.3 ResponseMeta / TokenUsage（`message.go:447`）

```go
type ResponseMeta struct {
    FinishReason string       // "stop" / "length" / "tool_calls" / "content_filter" 等
    Usage        *TokenUsage
    LogProbs     *LogProbs
}

type TokenUsage struct {
    PromptTokens            int
    PromptTokenDetails      PromptTokenDetails       // CachedTokens
    CompletionTokens        int
    TotalTokens             int
    CompletionTokensDetails CompletionTokensDetails  // ReasoningTokens
}
```

本项目在 `drainAssistantStream` 里读这个：`full.ResponseMeta.Usage` → 发一个 "usage" 帧给前端展示消耗。

### 2.4 ReasoningContent —— 思考链

**Claude 的 "extended thinking"、DeepSeek-R1 的推理过程**这类"思维链"内容不放 `Content`，放 `ReasoningContent`。本项目 UI 里"思考"折叠区就是走的这个字段：

```go
// adk_handler.go:175-193
if chunk.ReasoningContent != "" {
    buf.Append(Encode(Frame{Type: "thinking", Content: chunk.ReasoningContent}))
    collector.appendReasoning(chunk.ReasoningContent)   // 单独存
}
if chunk.Content != "" {
    buf.Append(Encode(Frame{Type: "text", Content: chunk.Content}))
    collector.appendContent(chunk.Content)
}
```

数据库里 `messages.reasoning_content` 是独立字段：

```go
// service/chat.go:180-190
s.msgRepo.Append(ctx, &model.Message{
    ConversationID:   convID,
    Role:             string(schema.Assistant),
    Content:          collector.Content(),
    ReasoningContent: collector.Reasoning(),
    ...
})
```

`llm.NewChatModel` 里 `cfg.EnableThinking=true` 才开启 Claude thinking：

```go
if cfg.EnableThinking {
    cc.Thinking = &claude.Thinking{
        Enable:       true,
        BudgetTokens: cfg.ThinkingBudget,
    }
}
```

### 2.5 多模态字段

用户侧 `UserInputMultiContent []MessageInputPart` —— 每个 part 可以是：

- `text`
- `image_url`（→ `MessageInputImage`）
- `audio_url`（→ `MessageInputAudio`）
- `video_url`（→ `MessageInputVideo`）
- `file_url`（→ `MessageInputFile`）

每种媒体 part 有 `URL` 或 `Base64Data + MIMEType` 二选一（RFC-2397 data URL 也支持）。

模型侧 `AssistantGenMultiContent []MessageOutputPart` 差不多结构，另外还多一个 `Reasoning` type（在没走 `ReasoningContent` 字段而是走 part 化的模型上用）。

**本项目暂时不涉及多模态**（Interview-Agent 是纯文本对话 + 工具调用）。

---

## 3. Message 构造器（`message.go:1104-1154`）

四个便捷函数，覆盖 90% 使用场景：

```go
schema.SystemMessage("You are a helpful assistant.")
schema.UserMessage("Hello!")
schema.AssistantMessage("Hi there.", nil)                              // 无 tool_call
schema.AssistantMessage("", []schema.ToolCall{...})                    // 只 tool_call
schema.ToolMessage("workspace created", "toolu_X", schema.WithToolName("create_workspace"))
```

本项目使用位置：
- `internal/service/chat.go:94`：`schema.SystemMessage(workspaceContext)` —— 把 workspace 上下文注入历史开头
- `internal/service/chat.go:96`：`schema.UserMessage(userMsg)` —— 用户本轮输入
- `internal/agent/agent.go:35`：`schema.SystemMessage(systemPrompt)` —— ReAct agent 的 system prompt（在 `MessageModifier` 里拼）

---

## 4. `schema.ConcatMessages` —— 流拼接的核心

`message.go:1643`，签名：

```go
func ConcatMessages(msgs []*Message) (*Message, error)
```

把流上收到的一堆 chunk 合并成一条完整 Message：

- `Content` / `ReasoningContent` 字符串拼接
- `ToolCalls` 按 `Index` 分组，每组的 `Function.Arguments` 字符串拼接（tool_call 的 JSON args 分几次流下来）
- `ResponseMeta.Usage` 取最后一个非空（一般在流末尾）
- 各种字段的 nil 合并 / 冲突处理

**没有它，流式模型的 tool_call 根本没法用**（因为 args JSON 是一段一段流下来的，不拼是无法解析的 fragment）。本项目消费 chunk 时既转 SSE 又攒起来 `ConcatMessages`，就是为了拿完整 tool_call。

框架层"Stream → Invoke 自动降级"背后依赖的也就是这个函数（`03-streaming.md` 讲的 Concat 原语，对 Message 类型的具体实现）。

---

## 5. ChatTemplate 与 MessagesPlaceholder

如果你不用 Message 手拼、想走模板，Eino 有 `prompt.ChatTemplate` 和 `schema.MessagesPlaceholder`。

### 5.1 三种模板语法（`message.go:96-105`）

```go
const (
    FString    FormatType = 0   // Python 风格 {name}
    GoTemplate FormatType = 1   // Go text/template {{.Name}}
    Jinja2     FormatType = 2   // {{ name }} 与 {% for x in xs %}
)
```

选哪个纯偏好。Python 移植过来的 prompt 用 FString / Jinja2 顺手，Go 原生项目用 GoTemplate。

### 5.2 `MessagesTemplate` 接口

```go
type MessagesTemplate interface {
    Format(ctx context.Context, vs map[string]any, formatType FormatType) ([]*Message, error)
}
```

两种实现：
- `*schema.Message` 自身（用变量填充 Content）
- `schema.MessagesPlaceholder(key, optional)` —— 从 vs 里取 `[]*Message` 直接插入

### 5.3 典型用法

```go
tpl := prompt.FromMessages(schema.FString,
    schema.SystemMessage("you are eino helper for {{user_role}}"),
    schema.MessagesPlaceholder("history", false),
    schema.UserMessage("{query}"),
)

msgs, err := tpl.Format(ctx, map[string]any{
    "user_role": "senior engineer",
    "history":   priorMessages,
    "query":     "how do I use ADK?",
})
```

### 5.4 本项目为什么没用 ChatTemplate？

本项目的 prompts（`internal/agent/prompts/*.go`）是**大段 Go 常量字符串**，包含 markdown、examples、skill 索引等，动态填充不多。直接用 `schema.SystemMessage(promptString)` 更直白：

```go
// adk_agent.go:55
supervisorInstruction := prompts.WithSkillsIndex(prompts.Supervisor, skillLoader)

// 传给 ADK：
supervisor, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    ...
    Instruction: supervisorInstruction,   // ADK 内部会包成 SystemMessage
})
```

**如果需要**动态插变量（例如把用户名/岗位方向拼进 prompt），才值得引入 ChatTemplate。目前用 Go 字符串拼接 + `WithSkillsIndex` 追加的方式简单够用。

---

## 6. Message 在本项目的完整生命周期

```
┌───────────────────────────────────────────────────────────┐
│ 1. 数据库读取历史                                            │
│    prior := msgRepo.List(ctx, convID)   → []model.Message │
│    ↓                                                       │
│ 2. 转 schema.Message                                        │
│    toSchemaMessages(prior)  → []*schema.Message           │
│    ↓                                                       │
│ 3. 追加系统上下文（workspace）+ 用户输入                       │
│    history = SystemMessage(workspaceContext)              │
│           + prior                                          │
│           + UserMessage(userMsg)                          │
│    ↓                                                       │
│ 4. 交给 ADK Runner                                          │
│    iter := runner.Run(ctx, history)                       │
│    ↓                                                       │
│ 5. ADK 内部：ChatModelAgent 把 Instruction 拼成             │
│    SystemMessage，前置到 history，喂给 ChatModel.Stream    │
│    ↓                                                       │
│ 6. ChatModel 流式返回 *StreamReader[*schema.Message]        │
│    ↓                                                       │
│ 7. ADK 把每个 chunk 包成 AgentEvent 送入 AsyncIterator      │
│    ↓                                                       │
│ 8. 我们在 drainAssistantStream 里消费：                      │
│    - 逐 chunk 发 SSE "text"/"thinking"                    │
│    - 攒 chunks 到末尾 ConcatMessages → 完整 Message         │
│    - 从 full.ToolCalls 发 "tool_call" 帧                   │
│    - 从 full.ResponseMeta.Usage 发 "usage" 帧               │
│    ↓                                                       │
│ 9. 回合结束落库                                              │
│    msgRepo.Append(&model.Message{                         │
│        Content:          collector.Content(),             │
│        ReasoningContent: collector.Reasoning(),           │
│        Extra:            {tools, sub_events},             │
│    })                                                      │
└───────────────────────────────────────────────────────────┘
```

**关键映射**（`internal/service/chat.go:181-191`）：

```go
func toSchemaMessages(rows []model.Message) []*schema.Message {
    out := make([]*schema.Message, 0, len(rows))
    for _, r := range rows {
        out = append(out, &schema.Message{
            Role:             schema.RoleType(r.Role),
            Content:          r.Content,
            ReasoningContent: r.ReasoningContent,
        })
    }
    return out
}
```

**注意**：只搬了 3 个字段。**ToolCalls 和 ToolCallID 没搬**。这是设计选择 —— 上一轮的 tool call/tool result 已经在 assistant Content 里被前端渲染过了，模型再次看到时不需要 tool call 结构；只保留最终文本就够。**面试可以聊：这里其实是一个偷懒**，严格来说 tool call 应该完整还原，不然模型可能没法根据"上轮已经调过什么工具"决策。当前是够用状态。

---

## 7. 记忆锚点

- **接口 4 层**：`BaseModel[M]` → `BaseChatModel` / `AgenticModel`；`BaseChatModel` 之上有 **`ChatModel`（Deprecated，BindTools 就地改）** 和 **`ToolCallingChatModel`（推荐，WithTools 返回新实例）**
- **Message 字段速记**：Role / Content / **ReasoningContent** / ToolCalls / ToolCallID + ToolName / ResponseMeta（含 Usage）/ Multi 多模态字段 / Extra
- **4 个 RoleType**：System / User / Assistant / Tool
- **4 个构造器**：`SystemMessage` / `UserMessage` / `AssistantMessage(content, toolCalls)` / `ToolMessage(content, callID, WithToolName)`
- **`ConcatMessages`** 是流→完整 Message 的合并算法（`Content`/`ReasoningContent` 拼接、`ToolCalls` 按 Index 分组拼 Arguments）
- **ChatTemplate**：三种 FormatType（FString / GoTemplate / Jinja2）+ `MessagesPlaceholder("history", false)` 插历史
- **本项目**：`ToolCallingChatModel` 用 Claude ext；prompts 是纯字符串（没走模板）；chat.go 里手拼 `SystemMessage(workspaceContext) + history + UserMessage(userMsg)` 交给 ADK
