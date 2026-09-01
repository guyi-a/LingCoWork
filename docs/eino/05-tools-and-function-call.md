# 05 · Tool 与 Function Call 全流程

Tool 是 ChatModel 落地能力的关键 —— 让语言模型能"操作外部世界"。这一章把 Tool 接口层次、`schema.ToolInfo`、三种构造方式、`ToolsNode` 编排、Middleware 拦截以及 function-calling 端到端流程全部串起来，并结合本项目所有工具都用 `utils.InferTool` 这一事实做对比。

---

## 1. Tool 接口层次（5 层）

源码 `components/tool/interface.go`。层层向上加能力：

```
                       ┌────────────────────────────┐
                       │       BaseTool             │  Info(ctx) → *ToolInfo
                       │       (只要元数据)           │  给 ChatModel 声明就够
                       └───────────┬────────────────┘
             ┌─────────────────────┼─────────────────────┐
             │                                          │
    ┌────────┴─────────┐                       ┌────────┴─────────┐
    │ InvokableTool     │                       │ StreamableTool    │
    │ InvokableRun      │                       │ StreamableRun     │
    │ 一次性返回 string   │                       │ 返 StreamReader   │
    └────────┬─────────┘                       └────────┬─────────┘
             │                                          │
    ┌────────┴───────────────┐               ┌──────────┴───────────────┐
    │ EnhancedInvokableTool   │               │ EnhancedStreamableTool    │
    │ 输入 ToolArgument       │               │ 流 *ToolResult             │
    │ 输出 *ToolResult        │               │ 多模态                     │
    │ 多模态                  │               │                            │
    └────────────────────────┘               └───────────────────────────┘
```

### 1.1 各层的确切签名

```go
// 只声明用（模型看得到，但没法真执行）
type BaseTool interface {
    Info(ctx context.Context) (*schema.ToolInfo, error)
}

// 一次性返回字符串结果
type InvokableTool interface {
    BaseTool
    InvokableRun(ctx context.Context, argumentsInJSON string, opts ...Option) (string, error)
}

// 流式返回字符串块
type StreamableTool interface {
    BaseTool
    StreamableRun(ctx context.Context, argumentsInJSON string, opts ...Option) (*schema.StreamReader[string], error)
}

// 结构化多模态输入 + 输出
type EnhancedInvokableTool interface {
    BaseTool
    InvokableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...Option) (*schema.ToolResult, error)
}
type EnhancedStreamableTool interface {
    BaseTool
    StreamableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...Option) (*schema.StreamReader[*schema.ToolResult], error)
}
```

### 1.2 选哪个的判断（面试点）

| 场景 | 用哪个 |
|---|---|
| 只把 tool schema 交给 ChatModel 声明，不在 Eino 侧执行（例如透传给外部编排） | `BaseTool` |
| 一次调用返回一个字符串/JSON（本项目所有工具） | `InvokableTool` |
| 结果又长又慢（例如子 agent 长文本、外部流式 API） | `StreamableTool` |
| 需要给模型返图片/音频/视频（多模态输出） | `EnhancedInvokableTool` |
| 多模态 + 流式（少见） | `EnhancedStreamableTool` |

**优先级**：当一个类型同时实现"标准"和"Enhanced"版本，ToolsNode 优先走 Enhanced。

**本项目**：`internal/agent/tools/*.go` 里所有工具最终返回的都是 `tool.BaseTool`（`tools.go::Builtin` 的返回类型 `[]tool.BaseTool`），实际实现的是 `InvokableTool`。没有用 Streamable 或 Enhanced —— 简单场景足够。

---

## 2. `schema.ToolInfo` 与 `ParamsOneOf`

Tool 给模型看的"名片"。源码 `schema/tool.go:128`：

```go
type ToolInfo struct {
    Name string       // 工具唯一名，模型会用这个 name 发起 tool_call
    Desc string       // 用途 + 何时用 + few-shot 示例（模型主要靠它决策）
    Extra map[string]any
    *ParamsOneOf     // 参数 schema（可为 nil，表示无参）
}

type ParamsOneOf struct { … }

// 两种造 ParamsOneOf 的方式：
func NewParamsOneOfByParams(params map[string]*ParameterInfo) *ParamsOneOf
func NewParamsOneOfByJSONSchema(s *jsonschema.Schema) *ParamsOneOf
```

- `NewParamsOneOfByParams` —— Eino 自定义的 `ParameterInfo` 结构（type + desc + required 等，简单场景够）
- `NewParamsOneOfByJSONSchema` —— 传标准 JSON Schema（复杂参数、嵌套结构、oneOf 等场景）

`Desc` 是最关键的字段 —— **模型是否会调你、参数怎么填、什么时候不调，全靠这段描述**。本项目在 `get_current_time` 的 description 里加"NEVER guess, and NEVER answer from memory"就是 prompt-engineering 干预模型行为的落地位。

---

## 3. 三种构造 Tool 的方式

### 3.1 `utils.InferTool[T, D]` —— 从 Go 结构体推 schema（**本项目全部用它**）

签名（`components/tool/utils/invokable_func.go:33-46`）：

```go
type InvokeFunc[T, D any] func(ctx context.Context, input T) (output D, err error)

func InferTool[T, D any](
    toolName, toolDesc string,
    i InvokeFunc[T, D],
    opts ...Option,
) (tool.InvokableTool, error)
```

**它做了三件事**：
1. 反射输入类型 `T`（用 [jsonschema 库](https://github.com/eino-contrib/jsonschema) 处理 `jsonschema:"..."` struct tag）→ 生成 JSON Schema
2. 生成的 schema + 你传的 name/desc 拼成 `*schema.ToolInfo`
3. 包成一个 `InvokableTool`，运行时自动 `json.Unmarshal` 参数字符串到 `T`、`json.Marshal(D)` 结果到字符串

### 3.2 InferTool 实战（本项目 `get_current_time`）

`internal/agent/tools/tools.go:32-40`：

```go
type currentTimeInput struct {
    Timezone string `json:"timezone" jsonschema:"description=IANA timezone name like 'Asia/Shanghai' or 'UTC'. Empty for system local time."`
}

type currentTimeOutput struct {
    Time     string `json:"time"`
    Timezone string `json:"timezone"`
}

func currentTime(_ context.Context, in *currentTimeInput) (*currentTimeOutput, error) {
    loc := time.Local
    if in.Timezone != "" {
        if l, err := time.LoadLocation(in.Timezone); err == nil {
            loc = l
        }
    }
    now := time.Now().In(loc)
    return &currentTimeOutput{
        Time:     now.Format("2006-01-02 15:04:05"),
        Timezone: loc.String(),
    }, nil
}

// 装配：
timeTool, err := utils.InferTool(
    "get_current_time",
    "Get the current wall-clock time. USE THIS whenever the user asks about now/today/current time or the answer depends on the current moment — NEVER guess, and NEVER answer from memory; the model's own knowledge of the current time is unreliable.",
    currentTime,
)
```

跑出来的 `ToolInfo` 大约是：

```json
{
  "name": "get_current_time",
  "description": "Get the current wall-clock time. USE THIS ...",
  "parameters": {
    "type": "object",
    "properties": {
      "timezone": {
        "type": "string",
        "description": "IANA timezone name like 'Asia/Shanghai' or 'UTC'. Empty for system local time."
      }
    }
  }
}
```

**优点**：类型安全（编译期）+ schema 自动同步（改结构体 tag 就够，不用双写 JSON schema）。

### 3.3 支持的 jsonschema tag 常用项

```go
type X struct {
    Name string  `json:"name"   jsonschema:"description=用户名,required"`
    Age  int     `json:"age"    jsonschema:"minimum=0,maximum=150"`
    Tags []string `json:"tags"  jsonschema:"description=用户标签"`
    Kind string  `json:"kind"   jsonschema:"enum=teacher,enum=student,enum=other"`
}
```

（本项目里大量用了 `description` tag，其他约束 tag 用得少。）

### 3.4 `utils.NewTool` —— 手写 ToolInfo + 提供函数

签名（`components/tool/utils/invokable_func.go:143`）：

```go
func NewTool[T, D any](desc *schema.ToolInfo, i InvokeFunc[T, D], opts ...Option) tool.InvokableTool
```

用途：当 InferTool 从结构体推不出你想要的 schema 时（例如需要复杂 oneOf、条件依赖），手写一份 `*schema.ToolInfo` 直接传。

### 3.5 `NewStreamTool` —— 流式版本

签名（`components/tool/utils/streamable_func.go:31,61`）：

```go
type StreamFunc[T, D any] func(ctx context.Context, input T) (output *schema.StreamReader[D], err error)

func NewStreamTool[T, D any](desc *schema.ToolInfo, s StreamFunc[T, D], opts ...Option) tool.StreamableTool
```

**本项目未用**。

### 3.6 直接手写实现接口

```go
type myTool struct{ … }

func (t *myTool) Info(_ context.Context) (*schema.ToolInfo, error) { … }
func (t *myTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) { … }
```

**本项目未用**（`ExitTool` 之类是 ADK 内部工具，不算业务侧）。

选型建议：**能用 InferTool 就用 InferTool**；只有涉及"多种参数形态互斥"或"schema 不能自动推导"时才降级到 NewTool；直接实现接口只在需要状态或复杂初始化时用。

---

## 4. `ToolsNode` —— 工具调度器

`compose/tool_node.go:79-193`。当你在 Graph 里用 `AddToolsNode`，或者 ADK 内部装载工具时，最终都落在 `ToolsNode`。

### 4.1 `ToolsNodeConfig`

```go
type ToolsNodeConfig struct {
    Tools                []tool.BaseTool                     // 必填：工具集
    ToolAliases          map[string]ToolAliasConfig          // 名字/参数别名（处理模型拼错）
    UnknownToolsHandler  func(ctx, name, input) (string, error)  // 处理 hallucinated tool call
    ExecuteSequentially  bool                                // false=并行（默认），true=串行
    ToolArgumentsHandler func(ctx, name, arguments) (string, error)  // 参数预处理
    ToolCallMiddlewares  []ToolMiddleware                    // 拦截器链
    // ...
}

func NewToolNode(ctx context.Context, conf *ToolsNodeConfig) (*ToolsNode, error)
```

### 4.2 三个容易忽视的能力

#### `UnknownToolsHandler` —— hallucination 兜底

模型有时候会发起 tool_call 到一个**根本不存在的工具**。默认 ToolsNode 会返回 error 让整个流程 fatal。装了 handler 后，你可以返一个"该工具不存在，请从下面工具选"式的字符串，让 ReAct 循环继续。

面试点：这是"模型犯错"的三种典型防御之一（另外两种是 orphan tool_call filter + toolerr middleware）。

#### `ToolAliases` —— 模型别名容错

```go
ToolAliases: map[string]ToolAliasConfig{
    "search": {
        NameAliases:      []string{"web_search", "google"},
        ArgumentsAliases: map[string][]string{
            "query": {"q", "search_term"},
        },
    },
}
```

**模型如果拼成 `web_search(q="...")` 也能路由到 `search(query="...")`**。上下文长了以后模型会漂，别名是很实际的鲁棒性提升。

#### `ExecuteSequentially`

模型可能一次 return 多个 tool_call（多工具并行）。默认 ToolsNode 会并发执行；`ExecuteSequentially=true` 时按顺序一个个跑。**当工具之间有隐含依赖（例如"先 create_workspace 再 write_file"）或副作用不可并发时**，需要打开这个开关。

**本项目**：三个 agent 都没设 `ExecuteSequentially`（默认并行）。看 `internal/agent/adk_agent.go:69-77`：

```go
ToolsConfig: adk.ToolsConfig{
    ToolsNodeConfig: compose.ToolsNodeConfig{
        Tools:               baseTools,
        ToolCallMiddlewares: []compose.ToolMiddleware{toolerr.Middleware()},
    },
    ...
},
```

只用了两个字段：Tools + ToolCallMiddlewares。其他能力（Aliases、UnknownHandler、Sequentially）没开 —— 当前场景下模型没漂，用不上。

---

## 5. Function Calling 端到端流程

这是模型侧 tool_call 从生成到返回结果的完整环路。**面试聊 "function calling 是怎么工作的" 时的标准答案**。

```
┌─────────────────────────────────────────────────────────────────────┐
│ 1. 装配阶段（服务启动时一次）                                          │
│    baseTools = [get_current_time, browser_bridge, load_skill, ...]  │
│    toolInfos = [t.Info() for t in baseTools]                        │
│    cm2 := cm.WithTools(toolInfos)  // ChatModel 拿到全部 schema      │
└─────────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 2. 用户提问，进入 ReAct 循环                                          │
│    history = [SystemMessage(instruction), UserMessage(query)]        │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 3. ChatModel.Stream(ctx, history)                                    │
│    - 模型看到所有 tool schema（第一步塞的 toolInfos）                  │
│    - 决定是否调工具                                                    │
│    - 如果调 → 输出 assistant.tool_calls = [{id, name, arguments}]     │
│    - 如果不调 → 输出普通 assistant.content                            │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
                        ┌──────┴───────┐
                        │              │
                        ▼              ▼
        ┌─────────────────────┐  ┌───────────────────────────┐
        │ 无 tool_calls        │  │ 有 tool_calls             │
        │ → 直接给用户答案      │  │ → ToolsNode 分派           │
        │ → ReAct 循环结束     │  │                            │
        └─────────────────────┘  └────────────┬──────────────┘
                                              │
                                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 4. ToolsNode.Run —— 对每个 tool_call 并发执行：                        │
│    - 按 name 查 tools[]（走 ToolAliases 别名 + UnknownToolsHandler）  │
│    - json.Unmarshal(arguments) → InvokableRun(ctx, argsJSON)          │
│    - 中间过 ToolCallMiddlewares 链（本项目：toolerr.Middleware）        │
│    - 得到 tool result string                                          │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 5. 生成 ToolMessage 追加到 history：                                  │
│    [SystemMessage, UserMessage, AssistantMessage(tool_calls),         │
│     ToolMessage(id, name, content) × N]                              │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
                               ▼
                     ─── 循环回到步骤 3 ───
                     直到模型决定不再调工具（无 tool_calls）
                     或达到 MaxIterations
```

**这条环就是 ReAct**（Reasoning + Acting）。ADK 的 `ChatModelAgent` 内部就是一个循环这套流程的 Graph（见 `07-react-agent.md`）。

### 5.1 tool_call ID 的严格配对

（`04-chatmodel-and-message.md` 讲过，这里再重申因为它是 tool 的核心正确性约束）

每个 assistant 的 `tool_calls[i].id` 必须由下一条 `ToolMessage` 的 `ToolCallID` 严格匹配（Anthropic 会 400，OpenAI 会警告或截断）。这条约束贯穿：
- **ADK 内部**：ToolsNode 一次跑完所有 tool_call 后按 id 生成 ToolMessage
- **本项目回放**：`toSchemaMessages` 里的 orphan filter 就是这条约束的最后一道防线

---

## 6. Middleware：`compose.ToolMiddleware`

（`compose/tool_node.go:130-169`）4 种 endpoint + 对应 4 种 middleware：

```go
type InvokableToolEndpoint  func(ctx, *ToolInput) (*ToolOutput, error)
type StreamableToolEndpoint func(ctx, *ToolInput) (*StreamToolOutput, error)
type EnhancedInvokableToolEndpoint  func(ctx, *ToolInput) (*EnhancedInvokableToolOutput, error)
type EnhancedStreamableToolEndpoint func(ctx, *ToolInput) (*EnhancedStreamableToolOutput, error)

type InvokableToolMiddleware  func(next InvokableToolEndpoint) InvokableToolEndpoint
// (以此类推)

type ToolMiddleware struct {
    Invokable          InvokableToolMiddleware
    Streamable         StreamableToolMiddleware
    EnhancedInvokable  EnhancedInvokableToolMiddleware
    EnhancedStreamable EnhancedStreamableToolMiddleware
}
```

**这是经典的 HTTP middleware 洋葱模型**，只是作用于工具调用而非请求。

### 6.1 本项目 `toolerr.Middleware` —— 错误转 tool result

`internal/agent/toolerr/middleware.go` 是**本项目 middleware 唯一实例**：把"工具报错"转成"看起来成功的 tool result"，让 ReAct 循环能继续走下去。核心逻辑：

```go
func Middleware() compose.ToolMiddleware {
    return compose.ToolMiddleware{
        Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
            return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
                out, err := next(ctx, input)
                if err == nil {
                    return out, nil
                }
                clean := stripFrameworkWrappers(err.Error())
                FromContext(ctx).Record(input.CallID, clean)  // 记到 registry
                return &compose.ToolOutput{
                    Result: formatToolErrMsg(input.Name, clean),
                }, nil
            }
        },
        Streamable: /* 同理 */ ,
    }
}
```

**为什么这么做**（面试高频）：

不装这层，工具报一次错（例如 `write_file` 遇到只读目录），错误会以 `AgentEvent.Err` 冒泡出来，**整个 Runner 的 AsyncIterator 直接 fatal**，用户对话流断掉，模型没机会看到错误、没机会重试。装上后：
1. Middleware 吞下 error
2. 把错误消息包成一个"看起来成功的 tool_result"
3. 模型下一轮 ReAct 循环拿到这个错误 message
4. 模型可以选择：重试（换参数）、放弃、告知用户

**这是所有生产级 ReAct 系统的必备封装**。ChatGPT / Claude Desktop / Cursor 都是这个模式。

### 6.2 SSE 层怎么显示"这是错误"

Middleware 把错误伪装成成功，但**前端还是要显示红色失败态**。项目里的解法：

- `toolerr.registry`（`context` 里）记录了"这个 CallID 实际上失败了 + 原始错误文本"
- SSE `emitToolResult` 消费 tool event 时，二次查 registry：如果命中，就发 `tool_result` 帧带 `ok=false + error=<原始文本>`
- **UI 拿 `ok=false`，模型拿"看起来成功的"字符串** —— 两边各取所需

这套"middleware 无侵入拦截 + 独立 registry 保真状态"的模式，是 Eino 相对 LangChain callback 系统的一个具体优势（详见 `06-callbacks.md`）。

### 6.3 你可能想加的其他 Middleware

- **Rate limit** —— 某个工具每分钟最多 10 次
- **Cache** —— 相同参数返上次的结果，避免重复调 API
- **Timeout wrapper** —— 单独给某工具设置 timeout
- **Audit log** —— 每次工具调用落日志（超过 CallOption 能力时）

Middleware 相对手动包一层 `tool.BaseTool` 的好处：**不用改工具本身，也不用改 ToolsNode**，切面式插入。

---

## 7. `AgenticToolsNode`（Beta）

`compose/agentic_tools_node.go`。用于新的 `AgenticMessage` 语义 —— 参数是结构化 `ToolArgument`、结果是 `ToolResult`（支持多模态）。**Beta，本项目未用**，只需要知道存在：面试如果问"Eino 未来的 tool 方向"，可以答 "AgenticToolsNode + AgenticMessage 是把多模态 I/O 纳入 tool 协议的抽象"。

---

## 8. 本项目 Tool 生态一览

一次性列出所有工具（`internal/agent/tools/tools.go::Builtin`）：

| 工具 | 文件 | 用途 | 特点 |
|---|---|---|---|
| `get_current_time` | `tools.go:32` | 拿当前墙钟时间 | 简单 InferTool，description 里加 "NEVER guess" |
| `create_workspace` | `workspace.go` | 为对话挂载 workspace 目录 | 手写 constructor（因为要闭包捕获 repos），内部用 InferTool |
| `list_files` / `read_file` / `write_file` / `write_file_chunked` / `edit_file` / `mkdir` | `fs.go` / `fs_chunked_write.go` | 工作区文件读写 | 全 InferTool，一堆 Input/Output 结构 |
| `browser_use_install` / `browser_use` | `browser_use.go` | 本地 headless 浏览器 | InferTool |
| `browser_bridge` | `browser_bridge.go` | 通过 Chrome extension 桥接用户浏览器 | InferTool，一个大 tool 里通过 `action` 参数分派多种子操作 |
| `load_skill` | `load_skill.go` | 加载 skill 手册（如 bosszp） | InferTool |

**装配位置**：`cmd/api/main.go:96-106`

```go
ts, err := tools.Builtin(ctx, tools.Deps{
    WorkspaceRoot:    absWorkspaceRoot,
    ProjectRepo:      projectRepo,
    ConversationRepo: convRepo,
    BrowserUseMgr:    browserMgr,
    BridgeService:    bridgeSvc,
    SkillLoader:      skillLoader,
})
// ts 是 []tool.BaseTool，接下来传给 NewInterviewADKAgent
```

**挂给三个 agent**（`internal/agent/adk_agent.go`）：
- Supervisor：`baseTools + [deepTool, jobTool]` （包括所有基础工具 + 两个 sub-agent 包装成 tool）
- deep_research：`baseTools`（不含 sub-agent，防止无限套娃）
- job_search：`baseTools`（同上）

**Sub-agent 也是 Tool**：`adk.NewAgentTool(ctx, subAgent)` 把 Agent 包成 `tool.BaseTool`，能挂给 parent。这是 ADK "AgentAsTool" 模式的实现基础，`08-multi-agent.md` 详解。

---

## 9. 记忆锚点

- **接口 5 层**：BaseTool → Invokable/Streamable → EnhancedInvokable/EnhancedStreamable
- **`schema.ToolInfo`**：Name / **Desc（最关键，模型靠它决策）** / ParamsOneOf（`ByParams` 或 `ByJSONSchema`）
- **构造三条路**：`utils.InferTool[T,D](name, desc, fn)` / `utils.NewTool(info, fn)` / 手写实现接口
- **InferTool 做的三件事**：反射 T → JSON Schema、拼 ToolInfo、包装 InvokableRun 并自动 JSON 编解码
- **`ToolsNodeConfig` 4 个进阶字段**：ToolAliases（模型拼错容错）、UnknownToolsHandler（hallucinated tool 兜底）、ExecuteSequentially（并行 vs 顺序）、ToolCallMiddlewares
- **Function calling 环**：装配 → model 决策 → tool_calls → ToolsNode 并发派发 → ToolMessage 回喂 → 循环
- **id 严格配对**：assistant.tool_calls[i].id ↔ ToolMessage.ToolCallID，Anthropic 400 触发条件
- **Middleware 洋葱**：`compose.ToolMiddleware` 4 种（Invokable/Streamable/Enhanced 两版）；本项目 `toolerr.Middleware` 把工具错误转成模型可读结果，是所有 ReAct 系统的必备封装
- **本项目**：所有 tool 走 InferTool + 挂 toolerr middleware；sub-agent 通过 `adk.NewAgentTool` 也变成 tool 挂给 supervisor
