# 01 · 核心抽象：Component、Runnable、Lambda、类型系统

Eino 的所有花活都建在这四层抽象之上。理解了这一章，后面 Chain/Graph/ADK 都只是"用法糖"。

---

## 1. 三层结构总览

```
                ┌─────────────────────────┐
                │        compose          │  编排层
                │  Chain / Graph / Workflow│  Runnable[I,O]、Lambda、Branch、Parallel、State
                └───────────▲─────────────┘
                            │ 用（组合）
                ┌───────────┴─────────────┐
                │       components        │  能力抽象层
                │  ChatModel / Tool /      │  BaseTool、Retriever、Embedding、Indexer、
                │  Retriever / Embedding … │  ChatTemplate、Document.Loader/Parser/Transformer
                └───────────▲─────────────┘
                            │ 用（数据）
                ┌───────────┴─────────────┐
                │         schema          │  基础类型层
                │  Message / StreamReader  │  Message、RoleType、ToolCall、
                │  / Document              │  StreamReader/Writer、Document、Format
                └─────────────────────────┘
```

设计意图：**基础类型是纯数据；组件是纯能力；编排是纯拓扑**。三者不互相依赖对方的实现细节，靠接口连接。这是 Eino 能做到"组件可替换、编排可静态分析"的根本。

---

## 2. Runnable[I, O] —— 全框架的执行契约

Runnable 是 Eino 里**最重要的一个接口**，`Chain.Compile()` / `Graph.Compile()` / Lambda 最终都归约成它。

源码位置：`compose/runnable.go:32`

```go
// Runnable is the interface for an executable object. Graph, Chain can be compiled into Runnable.
// runnable is the core conception of eino, we do downgrade compatibility for four data flow patterns,
// and can automatically connect components that only implement one or more methods.
// eg, if a component only implements Stream() method, you can still call Invoke() to convert stream output to invoke output.
type Runnable[I, O any] interface {
    Invoke(ctx context.Context, input I, opts ...Option) (output O, err error)
    Stream(ctx context.Context, input I, opts ...Option) (output *schema.StreamReader[O], err error)
    Collect(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output O, err error)
    Transform(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output *schema.StreamReader[O], err error)
}
```

### 2.1 四个方法对应四种运行范式

以"流"为轴，输入/输出各二态，共四种：

| 方法 | 输入 | 输出 | 典型场景 |
|---|---|---|---|
| `Invoke` | 单个值 `I` | 单个值 `O` | 一次性调用一个非流组件 |
| `Stream` | 单个值 `I` | `StreamReader[O]` | 组件"一次输入产流"（如 ChatModel 流式回复） |
| `Collect` | `StreamReader[I]` | 单个值 `O` | 上游流的下游要"攒齐"（如把 chunk 拼成一条 Message） |
| `Transform` | `StreamReader[I]` | `StreamReader[O]` | 流转流（Lambda 加工 token 流） |

### 2.2 关键设计：自动降级 / 自动升级（Framework Bridging）

**这是 Eino 相对 LangChain 最独到的抽象**，也是面试挂在嘴边可以背的一句话：

> "we do downgrade compatibility for four data flow patterns, and can automatically connect components that only implement one or more methods"

翻译过来：一个组件只要实现其中一种范式（比如只写了 `Invoke`），框架就能替它自动派生出另外三种。规则：

- 只有 `Invoke` → 框架在需要 `Stream` 的地方，用 `Invoke` 得到一个值后包成"单元素流"
- 只有 `Stream` → 框架在需要 `Invoke` 的地方，先跑 `Stream`，然后 **Concat**（自动拼接）流上所有块得到一个完整值
- 只有 `Transform` → 派生出 `Stream`（把单值包成流入）、`Invoke`（Concat 流出）、`Collect`（Concat 流出）
- 类推

对使用者的意义：**你写业务组件时，选一种最自然的形态实现即可**。上下游谁需要流谁不需要流，框架桥接。这也是 Eino 官方说"流处理对用户透明"的实现基础。

### 2.3 `Option` —— 运行期切面/参数注入通道

`Invoke(ctx, in, opts ...Option)` 中的 `opts` 是 Eino 的 **CallOption**（编排层称之为 `compose.Option`）。它承担两件事：

1. **给单个节点透传运行期参数**（例如给某个 ChatModel 节点单独设置 `Temperature`）
2. **挂载 Callback Handler**（例如加一次性 tracing）

面试点：CallOption 是"跨编排层传参"的机制。你 `runnable.Invoke(ctx, in, opts...)` 时，框架会按 `WithNodeName("...")` 之类的选择器把 option 定向送到目标节点，避免全局重配组件。

---

## 3. Component —— 能力抽象层的设计模式

Eino 官方对 Component 的定义：

> "the capability providers for LLM applications, serving as the bricks and mortar in the construction process of LLM applications."

设计原则：**模块化 + 标准化、可扩展、可复用**。

### 3.1 统一的组件契约

每个组件类型都遵循同一份形状（面试可背）：

```go
// 大致长这样（每个 component 具体签名略有变化）
type Xxx interface {
    // 主动作方法，签名是 (ctx, input, opts...) → (output, error)
    Method(ctx context.Context, input InType, opts ...XxxOption) (OutType, error)
}
```

对应地，每个组件类型的包会提供：
1. **接口** `interface Xxx`（在 `components/xxx/interface.go`）
2. **`XxxOption` 类型** + `WithFoo(v)` 系列 helper（用 functional options 模式）
3. **`Config` 结构体**（构造实现时的配置项）
4. **默认/常见实现**（在 `eino-ext/components/xxx/<provider>`，如 `eino-ext/components/model/claude`）

### 3.2 内建 Component 分类

按用途分四组（官方 `components/` 目录）：

| 组 | 组件 | 作用 |
|---|---|---|
| 对话处理 | `ChatTemplate`、`AgenticChatTemplate` | Prompt 模板 |
| 对话处理 | `ChatModel`、`ToolCallingChatModel`、`AgenticModel` | 大模型调用 |
| 语义处理 | `Document.Loader / Parser / Transformer` | 文档摄入与转换 |
| 语义处理 | `Embedding` | 文本 → 向量 |
| 语义处理 | `Indexer` | 向量入库 |
| 语义处理 | `Retriever` | 向量检索 |
| 决策与执行 | `ToolsNode`、`AgenticToolsNode`、`Tool`（`BaseTool` / `InvokableTool` / `StreamableTool`） | 工具调用 |
| 自定义 | `Lambda` | 自定义 Go 函数 |

### 3.3 举一个真实的接口签名：ChatModel

源码 `components/model/interface.go`：

```go
// 泛型化的模型基础接口
type BaseModel[M messageType] interface { … }

// 常用别名
type BaseChatModel = BaseModel[*schema.Message]

// 具备完整对话能力的 ChatModel
type ChatModel interface { … }

// 支持 function call 的 ChatModel（本项目 llm.NewChatModel 返回的就是它）
type ToolCallingChatModel interface { … }

// Beta：AgenticMessage 语义的模型
type AgenticModel = BaseModel[*schema.AgenticMessage]
```

面试点：`BaseModel[M messageType]` 用 Go 泛型统一了不同的消息形态（普通 `*schema.Message` vs `AgenticMessage`），让 SDK 层不用为两种 message 各写一套接口。

### 3.4 举一个真实的接口签名：Tool

源码 `components/tool/interface.go`：

```go
type BaseTool interface { … }                // 只需要能给出 Info（描述与 JSON Schema）
type InvokableTool interface { BaseTool; … } // 支持一次性调用
type StreamableTool interface { BaseTool; … }// 支持流式返回结果
type EnhancedInvokableTool interface { … }   // 加强版（带 raw response / metadata）
type EnhancedStreamableTool interface { … }
```

层级从"最小契约"往上加强，符合 Go 的接口段（interface segregation）习惯。**本项目里所有工具最终都实现 `BaseTool`**（`internal/agent/tools/tools.go::Builtin` 返回 `[]tool.BaseTool`），具体是 Invokable 还是 Streamable 由 `utils.InferTool` 或手写决定。

---

## 4. Lambda —— 自定义逻辑的逃生舱

不是每一段业务代码都值得抽成 Component。Eino 给出 **Lambda**：把一个普通 Go 函数直接塞进 Chain/Graph 当节点用。

官方一句话：**"the most basic component type in Eino"**。

### 4.1 四种 Lambda 类型（和 Runnable 的四种范式对齐）

```go
type Invoke[I, O, TOption any]    func(ctx context.Context, input I,                          opts ...TOption) (output O, err error)
type Stream[I, O, TOption any]    func(ctx context.Context, input I,                          opts ...TOption) (output *schema.StreamReader[O], err error)
type Collect[I, O, TOption any]   func(ctx context.Context, input *schema.StreamReader[I],   opts ...TOption) (output O, err error)
type Transform[I, O, TOption any] func(ctx context.Context, input *schema.StreamReader[I],   opts ...TOption) (output *schema.StreamReader[O], err error)
```

对应四个构造 helper：

```go
compose.InvokableLambda(fn)
compose.StreamableLambda(fn)
compose.CollectableLambda(fn)
compose.TransformableLambda(fn)
```

### 4.2 塞进 Chain / Graph

```go
// Graph
graph := compose.NewGraph[string, *MyStruct]()
graph.AddLambdaNode(
    "node1",
    compose.InvokableLambda(func(ctx context.Context, in string) (*MyStruct, error) {
        return &MyStruct{...}, nil
    }),
)

// Chain
chain := compose.NewChain[string, string]()
chain.AppendLambda(compose.InvokableLambda(func(ctx context.Context, in string) (string, error) {
    return strings.ToUpper(in), nil
}))
```

### 4.3 AnyLambda —— 一次性提供多种范式

如果你希望同一个 Lambda 在被下游要求 `Stream` 时用你手写的流式版本、被要求 `Invoke` 时用你的非流版本（而不是让框架自动 Concat），用 `AnyLambda` 同时给出多种实现：

```go
lambda, err := compose.AnyLambda(
    func(ctx context.Context, in string, opts ...MyOption) (string, error)                                 { … },
    func(ctx context.Context, in string, opts ...MyOption) (*schema.StreamReader[string], error)           { … },
    func(ctx context.Context, in *schema.StreamReader[string], opts ...MyOption) (string, error)           { … },
    func(ctx context.Context, in *schema.StreamReader[string], opts ...MyOption) (*schema.StreamReader[string], error){ … },
)
```

### 4.4 Lambda vs Component 什么时候用哪个

- **写成 Component**：这段能力有可能被复用、有清晰的输入输出契约、可能被第三方替换（例如另一家 ChatModel）
- **写成 Lambda**：只在这一条链路里出现一次的胶水逻辑（提取字段、做个 if/else、拼字符串、映射结构）

### 4.5 两个官方内置 Lambda

- `ToList` —— 把单个 `*schema.Message` 包成 `[]*schema.Message`（下游要求切片时常用）
- `MessageParser` —— 把 Message 的 content 或 tool_call.arguments 用 JSON 解到结构体，做意图识别常用

---

## 5. 类型系统：Go 泛型如何贯穿

Eino 全框架的类型安全建立在 **Go 1.18+ 泛型** 上。三个关键泛型位点：

1. **`Runnable[I, O any]`** —— 编排的输入/输出类型
2. **`Lambda 四种签名 [I, O, TOption any]`** —— 用户函数的输入/输出/自定义 option
3. **`compose.NewGraph[I, O]()` / `compose.NewChain[I, O]()`** —— 建图时锁定端到端类型
4. **`schema.StreamReader[T]` / `schema.StreamWriter[T]`** —— 流内容的元素类型
5. **`BaseModel[M messageType]`** —— 模型消息形态的泛化

### 5.1 编译期检查的实际效果

```go
chain := compose.NewChain[string, *schema.Message]()
chain.AppendChatTemplate(tpl)     // 期望上游给 map[string]any → 编译期失败
```

比 LangChain 的 `Any` 传参思路早一个阶段发现错。**这是面试聊 Eino 独特性时的强论据**。

### 5.2 编译期 vs 构图期

不是所有类型错误都能编译期抓到。有些拓扑相关的类型不匹配（比如某个 Node 输出 `X`、下游期望 `Y`）要等 `Compile(ctx)` 才报错。Eino 的取向是：**构图期能报的绝不留到运行期**。

---

## 6. Option 与 Middleware：切面机制两件套

### 6.1 CallOption

已在 §2.3 提过。它是"从 Runnable.Invoke 传参一直渗透到叶子组件"的机制，允许：

- `compose.WithNodeName("chat")` —— 定向某个节点
- `compose.WithCallbacks(handler)` —— 一次性挂 Callback
- 各组件包自己的 `WithXxx` —— 例如 `model.WithTemperature(0.2)`

### 6.2 ToolMiddleware（本项目在用）

`compose.ToolMiddleware` 是编排层给 **Tool 调用**准备的切面结构。签名（简化）：

```go
type ToolMiddleware struct {
    Invokable  func(next InvokableToolEndpoint)  InvokableToolEndpoint
    Streamable func(next StreamableToolEndpoint) StreamableToolEndpoint
}
```

其中：

```go
type InvokableToolEndpoint  func(ctx context.Context, input *ToolInput) (*ToolOutput, error)
type StreamableToolEndpoint func(ctx context.Context, input *ToolInput) (*StreamToolOutput, error)
```

**本项目实例 `internal/agent/toolerr/middleware.go:29`**：把 tool 报错转成"给模型看的错误 Message"，避免整个 AgentEvent 流 fatal 掉。核心逻辑：

```go
func Middleware() compose.ToolMiddleware {
    return compose.ToolMiddleware{
        Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
            return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
                out, err := next(ctx, input)
                if err == nil { return out, nil }
                clean := stripFrameworkWrappers(err.Error())
                FromContext(ctx).Record(input.CallID, clean)
                // 把 error 转成正常输出，让 ReAct 循环把它当作工具的返回信息喂给模型
                return &compose.ToolOutput{Result: formatToolErrMsg(input.Name, clean)}, nil
            }
        },
        Streamable: /* 同理，包装 StreamReader 版本 */ ,
    }
}
```

**为什么要这么写**：不装这层中间件的话，工具报错（比如"workspace 已存在"）会以 `AgentEvent.Err` 冒泡出来终止整个 Run；装上之后，错误变成一句普通的工具返回，模型下一轮 ReAct 就有机会看到并重试或换策略。**这个模式是所有 ReAct-based agent 都要做的经典封装**，面试聊"你怎么让 agent 更鲁棒"可以直接搬。

安装位置见 `internal/agent/adk_agent.go:75`：

```go
ToolsConfig: adk.ToolsConfig{
    ToolsNodeConfig: compose.ToolsNodeConfig{
        Tools:               baseTools,
        ToolCallMiddlewares: []compose.ToolMiddleware{toolerr.Middleware()},
    },
},
```

---

## 7. 本项目落地映射

| 抽象 | 本项目里在哪 |
|---|---|
| `Runnable[I, O]` | 主流程没直接用（走 ADK 路线）；但 `adk.ChatModelAgent` 内部把 tools + model 编到 graph 里，最终 compile 出来的就是 Runnable |
| Component - `ToolCallingChatModel` | `cm, err := llm.NewChatModel(ctx, cfg.LLM)` — `cmd/api/main.go:78` |
| Component - `tool.BaseTool` | `internal/agent/tools/tools.go::Builtin` 返回的整个切片 |
| Component - `utils.InferTool` | `tools.go:32` —— 从 Go 结构体 tag 自动推 tool JSON Schema，省掉手写 schema |
| Lambda | 本项目未直接用（业务逻辑够用 Tool 表达） |
| CallOption | 隐式：ADK Runner 内部生成 |
| ToolMiddleware | `internal/agent/toolerr/middleware.go` + 在 3 个 agent 上都装了（adk_agent.go 的 3 个 ToolsConfig） |

---

## 8. 记忆锚点

- **一句话记 Runnable**：一个可编排对象的四方向执行契约（Invoke / Stream / Collect / Transform），实现其一，另外三种由框架自动桥接。
- **一句话记 Component**：能力抽象层，每个组件 = 接口 + Config + Options + 若干实现。
- **一句话记 Lambda**：把 Go 函数直接当节点用的逃生舱，四种签名对应四种运行范式。
- **一句话记类型系统**：`compose.NewGraph[I, O]()`、`Runnable[I, O]`、`StreamReader[T]` 全部泛型，端到端类型在构图期落定。
- **一句话记 Middleware**：`compose.ToolMiddleware` 让你无侵入地包住 tool 调用做统一处理（本项目用它把 error 转成模型可读的 Message）。
