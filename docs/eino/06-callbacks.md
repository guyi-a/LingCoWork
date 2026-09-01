# 06 · Callback / Handler 与切面机制

Callback 是 Eino 里做 tracing、日志、metrics 的标准接口。这一章讲透 5 种 timing hook、`RunInfo` 元数据、三种挂载路径、流式 hook 的隐藏坑，并把它跟前面讲的 `ToolMiddleware`、ADK 的 `AgentEvent` 摆在一起对比清楚。

**顺便一个考古发现**：本项目 `internal/stream/sse_handler.go:362` 有一个完整的 `NewSSEHandler`，但**没有 wire**（`grep -r NewSSEHandler` 只有定义处）—— 是 ADK 之前老路线的残留代码。这本身就是一个很好的"两条路线对比"素材。

---

## 1. Callback 存在的动机

Chain / Graph / ADK 编排在跑的时候，你会想在**每个节点前后**做点事：

- 打日志：这个 ChatModel 拿到什么消息、输出了什么、消耗几个 token
- Tracing：给 OpenTelemetry span 打 attribute（component 名字、耗时）
- 指标：Prometheus 里 tool 调用次数、失败率
- 缓存：命中检查、写缓存
- 数据审计：入库全部 assistant 消息用于合规

如果每个 Component 内部各自打点，就爆炸重复；如果绕开框架自己包一层，就得逐个包组件。**Callback 的答案是：框架自动把 hook 织进每个 component 的入口/出口**，用户只要注册 Handler 就行。

一句话定位：**Eino 的 Callback 是 component-level 的切面钩子，主要用于观测（不改变执行结果）**。

---

## 2. 五种 Callback Timing

`callbacks/interface.go:114-134`：

```go
const (
    TimingOnStart                  CallbackTiming = iota  // 组件开始处理前
    TimingOnEnd                                             // 组件成功返回后
    TimingOnError                                           // 组件返回非 nil error
    TimingOnStartWithStreamInput                            // 组件接收流式输入前（Collect/Transform 范式）
    TimingOnEndWithStreamOutput                             // 组件返回流式输出后（Stream/Transform 范式）
)
```

**为什么是 5 个不是 3 个**：因为流式输入/输出各有独立的 hook。非流式 In → Out 只需要 OnStart+OnEnd；但流式的时候 `input` 和 `output` 都是 `*StreamReader[T]`，框架要"复制一份流"给 handler，语义和普通值传递不同，必须独立 hook。

**注意**：`TimingOnError` 只在**同步返回错误**时触发。**流中间的错误**（Stream reader 在读到某个 chunk 时 err）**不走 OnError**，而是包在 stream 里被消费者拿到。这个坑上过多次。

---

## 3. `Handler` 接口和 5 个方法

```go
type Handler interface {
    OnStart(ctx context.Context, info *RunInfo, input CallbackInput)   context.Context
    OnEnd(ctx context.Context, info *RunInfo, output CallbackOutput)   context.Context
    OnError(ctx context.Context, info *RunInfo, err error)              context.Context
    OnStartWithStreamInput(ctx, info, input  *StreamReader[CallbackInput])   context.Context
    OnEndWithStreamOutput (ctx, info, output *StreamReader[CallbackOutput]) context.Context
}
```

**每个方法返回一个新 context**。这有两个作用：
1. **同一 handler 内部**：OnStart 里塞进 ctx 的 value，OnEnd 能读到（例如记录起始时间戳，OnEnd 算 latency）
2. **不同 handler 之间**：context 不会在多个 handler 之间串（原文："the context chain does NOT flow from one handler to the next"），也没有确定顺序 —— **handler 必须无序独立**

### 3.1 `CallbackInput` / `CallbackOutput` 是"多态 any"

框架层不知道每个 component 具体传什么，所以 `CallbackInput` / `CallbackOutput` 就是 `any`。**具体类型由 component 定义**：

- ChatModel 传 `*model.CallbackInput{Messages []*Message}` 和 `*model.CallbackOutput{Message, TokenUsage}`
- Tool 传 `*tool.CallbackInput{ArgumentsInJSON}` 和 `*tool.CallbackOutput{Response}`
- Retriever 传 `*retriever.CallbackInput{Query}` 和 `*retriever.CallbackOutput{Docs}`

Handler 里用**类型断言 helper** 安全转换：

```go
if mi := model.ConvCallbackInput(input); mi != nil {
    log.Printf("prompt has %d messages", len(mi.Messages))
}
```

`ConvXxxCallbackInput` 类型不匹配时返回 nil（不 panic），让你用一个 handler 处理多种 component 类型，各自 nil-check。

---

## 4. `RunInfo` —— 告诉 handler "谁在触发"

`callbacks/interface.go:41`：

```go
type RunInfo struct {
    Name      string  // 用户给的名字（例如 compose.WithNodeName("chat_model") 里那个）
    Type      string  // 实现身份，比如 "OpenAI" / "Claude" / "MyCustomTool"
    Component components.Component  // 组件类别常量，例如 ChatModel / Tool / Retriever / Lambda
}
```

Handler 通常先按 `Component` 分流，再按 `Name` / `Type` 精确匹配：

```go
OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
    if info.Component != components.ComponentOfTool {
        return ctx  // 不是工具调用，跳过
    }
    log.Printf("[tool=%s type=%s] starting", info.Name, info.Type)
    return ctx
})
```

**面试点**：为什么 Handler 无序不保证执行顺序？—— 因为 RunInfo 已经给了足够的过滤信号，多个 handler 只要各自 nil-check + component 判断就能独立工作，强制排序反而增加耦合。

---

## 5. `HandlerBuilder` —— 只填你关心的 timing

不用手动实现 5 个方法。用 `NewHandlerBuilder()` 链式配：

```go
handler := callbacks.NewHandlerBuilder().
    OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
        // ...
        return ctx
    }).
    OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
        // ...
        return ctx
    }).
    Build()
```

对应 5 个 setter：`OnStartFn` / `OnEndFn` / `OnErrorFn` / `OnStartWithStreamInputFn` / `OnEndWithStreamOutputFn`。

### 5.1 `TimingChecker` 自动实现

`HandlerBuilder` 生成的 handler 会自动实现 `TimingChecker` 接口的 `Needed(...)` 方法：**你没设的 timing，框架不会为你复制流、不启 goroutine**。这是重要的性能优化 —— 每个 component 调用都要过 handler 链，如果每个都强制走 5 个 hook 会浪费很多。

手写 Handler 的话记得也实现 `Needed` 显式声明你要哪些 timing。

---

## 6. 挂载 Handler 的三条路径

### 6.1 全局：`AppendGlobalHandlers`

```go
func main() {
    callbacks.AppendGlobalHandlers(myTracer)  // 一次，程序生命周期
    // ...
}
```

**在所有 graph / agent 执行前调用一次**。之后每个组件的每次调用都会经过 `myTracer`。适合 tracing、metrics、审计日志这种"必须观测所有调用"的场景。

⚠️ **不是并发安全的**，只能启动时设置。运行时改会 data race。

### 6.2 编译期：`compose.WithCallbacks` 编译选项

```go
r, err := chain.Compile(ctx, compose.WithCallbacks(handler1, handler2))
```

Handler 织进这个 Runnable，之后每次 Invoke/Stream 都会跑。适合"某个特定 pipeline 需要额外观测"的场景。

### 6.3 运行期：`compose.WithCallbacks` CallOption

```go
runnable.Invoke(ctx, input, compose.WithCallbacks(oneOffHandler))
```

**只作用于本次调用**。适合调试、trace 特定 request、A/B 观测。

### 6.4 优先级 & 执行顺序

- 全局 Handler 先跑（"they run before per-invocation handlers"）
- 编译期 handler 次之
- 运行期（CallOption）handler 最后

但**同层内不同 handler 之间无序**（前面强调过），别依赖。

---

## 7. 流式 Hook 的两个必知细节

### 7.1 handler 拿到的是"复制的流"，必须 Close

原文警告（`callbacks/interface.go:77-81`）：

> Stream handlers receive a `*schema.StreamReader` that has already been copied; they MUST close their copy after reading. If any handler's copy is not closed, the original stream cannot be freed, causing a goroutine/memory leak for the entire pipeline.

框架在触发 `OnEndWithStreamOutput` 时，会 `sr.Copy(N)` 给每个注册了这个 timing 的 handler 各一份独立 reader。你可以自由消费，但**必须 Close**，否则原流引用永远不释放，pipeline 挂着 goroutine 泄漏。

标准写法：

```go
OnEndWithStreamOutputFn(func(ctx context.Context, info *RunInfo, sr *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
    go func() {
        defer sr.Close()   // ← 必须
        for {
            v, err := sr.Recv()
            if errors.Is(err, io.EOF) { return }
            if err != nil { return }
            // consume v
        }
    }()
    return ctx
})
```

### 7.2 不要 mutate `input` / `output`

原文：

> Do NOT mutate the Input or Output values. All downstream nodes and handlers share the same pointer (direct assignment, not a deep copy). Mutations cause data races in concurrent graph execution.

Handler 只读，改了下游拿到的会是脏数据。**要 mutate 请自己 deep copy 一份**。

---

## 8. 本项目 `NewSSEHandler` —— 老路线的完整参照

`internal/stream/sse_handler.go:362` 有一个功能完整的 Handler 实现，注册了 4 种 timing：

```go
func NewSSEHandler(buf *StreamBuffer, collector *RunCollector) callbacks.Handler {
    var toolCounter int64
    var lastToolID atomic.Value

    return callbacks.NewHandlerBuilder().
        OnStartFn(func(ctx, info, input) context.Context {
            if info.Component != components.ComponentOfTool { return ctx }
            // 从 tool.ConvCallbackInput 拿参数，发 "tool_call" SSE 帧
            ...
        }).
        OnEndFn(func(ctx, info, output) context.Context {
            if info.Component != components.ComponentOfTool { return ctx }
            // 从 tool.ConvCallbackOutput 拿结果，发 "tool_result" 帧
            ...
        }).
        OnEndWithStreamOutputFn(func(ctx, info, sr) context.Context {
            if info.Component != components.ComponentOfChatModel {
                sr.Close(); return ctx
            }
            go func() {
                defer sr.Close()  // ← 遵守规则
                for {
                    raw, err := sr.Recv()
                    if errors.Is(err, io.EOF) { return }
                    // 逐 chunk 发 "thinking" / "text" 帧
                    ...
                }
            }()
            return ctx
        }).
        OnErrorFn(...).
        Build()
}
```

**关键**：`grep -rn NewSSEHandler` 只匹配到定义位置，**没有任何调用点**。这段代码是**老路线遗留**：ADK 引入之前，本项目通过 `flow/agent/react.NewAgent` 跑 ReAct，用这个 callback handler 把 ChatModel/Tool 事件翻译成 SSE 帧。切到 ADK 后主流程改成消费 `AsyncIterator[*AgentEvent]`（`internal/stream/adk_handler.go`），callback handler 变成孤儿代码但没删。

**这个对比是面试聊 Eino 观测机制的黄金材料**（下一节展开）。

---

## 9. Callback vs Middleware vs AgentEvent —— 三种切面的分层

本文档里已经出现过 **3 种切面/观测**机制，容易混。定位差异：

| 机制 | 作用于 | 位置 | 语义 | 能改结果吗 | 典型用法 |
|---|---|---|---|---|---|
| **Callback**（`callbacks/`） | 每个 Component + Graph 本身 | 每个组件的入口/出口 | 观测（通知） | ❌ 只读 | Trace、日志、metrics、缓存副作用 |
| **ToolMiddleware**（`compose/tool_node`） | 只 Tool | Tool 调用洋葱 | 拦截+改写 | ✅ 可吞 error、可改 result | 错误恢复、rate limit、cache、audit |
| **ADK AgentEvent**（`adk/`） | 整个 Agent | Agent.Run 的 AsyncIterator | 事件流 | ❌ 只读 | 前端实时展示 agent 进度、sub-agent 冒泡 |

一张分层图：

```
┌────────────────────────────────────────────────────┐
│  Agent 层                                            │
│    观察方式：消费 Runner.Run → AsyncIterator[Event]   │  ← ADK AgentEvent
└─────────────────────┬──────────────────────────────┘
                      │
┌─────────────────────┴──────────────────────────────┐
│  Graph / Chain / Workflow 层                        │
│    观察方式：Callback Handler on every component     │  ← callbacks
│    改写方式：ToolMiddleware（只对 Tool 有效）          │  ← ToolMiddleware
└────────────────────────────────────────────────────┘
```

### 9.1 为什么本项目从 Callback 迁到 AgentEvent

放老路线 `NewSSEHandler` + Callback 时，能拿到 ChatModel 的流 + Tool 的调用/返回。但 ADK 引入后：

- **需要区分"root agent 的 tool 调用"和"sub-agent 内部的 tool 调用"** —— Callback 只知道"某个 tool 被调了"，但**不知道谁调的**（root supervisor 还是嵌套的 deep_research？）。AgentEvent 有 `AgentName` 字段直接解决这个问题。
- **需要给 sub-agent 挂 `parent_tool_call_id`**（"deep_research 的活挂在 root 的 deep_research 卡片下面"）—— Callback 拿不到这条 agent-level 的谱系。
- **AgentEvent 的粒度更粗**（一个事件 = 某 agent 某时刻的一次输出），比 Callback 的 component 粒度更适合 UI 展示。

**结论**：Callback 适合"跨 pipeline 的观测层"（trace / metrics）；AgentEvent 适合"面向用户的进度呈现"。**本项目 UI 层用 AgentEvent 是对的选择**，Callback handler 可以删（但因为不影响正确性，也可以留作 fallback 参考）。

### 9.2 什么时候仍应该考虑 Callback

- 加 **OpenTelemetry tracing**（给每个 component 打 span） —— 用全局 handler
- 加 **Prometheus metrics**（token 消耗、tool 调用次数） —— 用全局 handler
- 需要观察**普通 Graph pipeline**（不是 ADK Agent）时 —— AgentEvent 只在 ADK 里存在，Graph 直接跑没有这一层

---

## 10. 完整最简例子

一个"打印所有 ChatModel 请求/响应"的调试 handler：

```go
import (
    "context"
    "log"

    "github.com/cloudwego/eino/callbacks"
    "github.com/cloudwego/eino/components"
    "github.com/cloudwego/eino/components/model"
)

func NewDebugHandler() callbacks.Handler {
    return callbacks.NewHandlerBuilder().
        OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
            if info.Component != components.ComponentOfChatModel {
                return ctx
            }
            mi := model.ConvCallbackInput(input)
            if mi == nil {
                return ctx
            }
            log.Printf("[model=%s] request: %d messages", info.Name, len(mi.Messages))
            // 塞开始时间戳到 ctx，OnEnd 里能读
            return context.WithValue(ctx, "start_time", time.Now())
        }).
        OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
            if info.Component != components.ComponentOfChatModel {
                return ctx
            }
            mo := model.ConvCallbackOutput(output)
            if mo == nil || mo.Message == nil {
                return ctx
            }
            elapsed := time.Since(ctx.Value("start_time").(time.Time))
            tokens := 0
            if mo.Message.ResponseMeta != nil && mo.Message.ResponseMeta.Usage != nil {
                tokens = mo.Message.ResponseMeta.Usage.TotalTokens
            }
            log.Printf("[model=%s] done in %v, %d tokens", info.Name, elapsed, tokens)
            return ctx
        }).
        Build()
}

// main.go:
callbacks.AppendGlobalHandlers(NewDebugHandler())
```

---

## 11. 记忆锚点

- **5 个 timing**：OnStart / OnEnd / OnError / OnStartWithStreamInput / OnEndWithStreamOutput —— 后两个是流式版本
- **RunInfo 三字段**：Name（用户命名）/ Type（实现类型）/ Component（类别常量），Handler 靠这仨过滤
- **`CallbackInput/Output` 是 any**，具体类型由 component 决定，用 `xxx.ConvCallbackInput(in)` 类型断言（nil-safe）
- **`HandlerBuilder` 只填关心的 timing**，自动实现 `TimingChecker.Needed` 让框架跳过未注册 timing 的开销
- **挂载 3 条路径**：`AppendGlobalHandlers`（启动时）/ `compose.WithCallbacks` 编译期 / `compose.WithCallbacks` CallOption 运行期
- **流式 hook 两个铁律**：（1）handler 拿到的 stream 是 Copy，**必须 Close** 否则 pipeline 泄漏；（2）**不要 mutate** input/output，downstream 共享指针
- **不同 handler 之间**无序、context 不串；同一 handler 的 OnStart→OnEnd 通过 ctx 传状态
- **Callback ≠ Middleware ≠ AgentEvent**：Callback 观测 component（只读）；Middleware 拦截 Tool（可改）；AgentEvent 观测 Agent（只读，粒度更粗）
- **本项目**：`NewSSEHandler` 是 ADK 之前老路线的完整 callback 实现，切到 ADK 后未再 wire；主流程改走 `AsyncIterator[*AgentEvent]` 因为 AgentEvent 天然带 agent 谱系信息（root vs sub），callback 层拿不到
