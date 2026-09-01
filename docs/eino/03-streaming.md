# 03 · 流式：StreamReader / StreamWriter 与四种运行范式

**这一章是 Eino 相对同类框架的招牌**。理解了流式抽象和四方向范式，后面 ChatModel、Callback、ReAct、ADK Event 都会顺理成章。

---

## 1. 为什么 Eino 要把流做成一等公民

LLM 的输出天生是 token 流，SSE、WebSocket、gRPC streaming 都是常见的对外协议。如果框架层面没有流抽象，用户就得：

- 每次接 SDK 手动处理 chunk
- 拿到 chunk 后要么攒齐要么各种自定义回调
- 组件之间要"流上游 vs 非流下游"的适配代码手写一份

**Eino 的取向**：给流做一个统一的 `StreamReader[T]` / `StreamWriter[T]`，让所有组件用同一套接口收发流；然后规定"四种运行范式"让每个组件声明自己天然支持哪些；上下游不对齐的地方框架**自动做流<->值的转换**（Concat / Boxing）。

用户只关心业务，不用关心"上游是不是流"。这就是官方那句：

> "Whether a component can handle streams or will output streams becomes transparent to users."

---

## 2. `schema.StreamReader[T]` 与 `schema.StreamWriter[T]`

源码 `schema/stream.go`。

```go
type StreamReader[T any] struct { … }
type StreamWriter[T any] struct { … }

// 唯一的构造入口
func Pipe[T any](cap int) (*StreamReader[T], *StreamWriter[T])
```

### 2.1 StreamWriter 三方法

```go
func (sw *StreamWriter[T]) Send(chunk T, err error) (closed bool)
func (sw *StreamWriter[T]) Close()
```

- `Send(chunk, err)`：塞一个块；返回 `closed=true` 表示对端已关，你可以停了
- `Close()`：告诉读端"没有更多了"；读端 `Recv()` 会拿到 `io.EOF`
- **约定**：send 完必调 `Close()`

### 2.2 StreamReader 关键约束（面试题）

```go
func (sr *StreamReader[T]) Recv() (T, error)
func (sr *StreamReader[T]) Close()
func (sr *StreamReader[T]) Copy(n int) []*StreamReader[T]
func (sr *StreamReader[T]) SetAutomaticClose()
```

**核心约束（源码注释直译）**：

> "A StreamReader is read-once: only one goroutine should call Recv, and the reader must be closed exactly once (whether the loop finishes normally or exits early via break or return)."

翻译成规矩：
1. **一次性读**（read-once）：**同一个 sr 只能一个 goroutine 调 Recv**，不能扇出多消费者
2. **必须 Close 且只能 Close 一次**：不管是正常读完还是 break 提前退出，都要 Close，否则底层 channel/资源泄漏
3. **扇出用 `Copy(n)`**：一分 N 得到 N 个独立 reader，原 sr **不再可用**；场景是"同时给 callback 和下游节点"
4. **兜底 `SetAutomaticClose()`**：装了 finalizer，GC 时自动 Close，防止忘写 defer（不 100% 及时，只是保险）

标准消费姿势（源码文档里的示例）：

```go
defer sr.Close() // always close, even after io.EOF
for {
    chunk, err := sr.Recv()
    if errors.Is(err, io.EOF) {
        break
    }
    if err != nil {
        return err
    }
    process(chunk)
}
```

本项目 `internal/stream/adk_handler.go:161-215` 的 `drainAssistantStream` 就是这个姿势。

### 2.3 StreamReader 的五种内部形态（`readerType`）

一个 `StreamReader[T]` 内部可能是这五种之一：

| 类型 | 来源 | 用途 |
|---|---|---|
| `readerTypeStream` | `Pipe()` | 常规异步 producer/consumer |
| `readerTypeArray` | `StreamReaderFromArray(arr)` | 把切片当流，同步产 |
| `readerTypeMultiStream` | `MergeStreamReaders([]sr)` / `MergeNamedStreamReaders(map)` | N 合 1 |
| `readerTypeWithConvert` | `StreamReaderWithConvert[T, D](sr, convert)` | 元素类型转换 |
| `readerTypeChild` | `sr.Copy(n)` 的产物 | 一分 N |

对使用者来说都是同一个 `StreamReader[T]` 接口，看不出内部差异。**这就是"外部一个接口，内部多种实现"的经典 pattern**（类似 `io.Reader` 有 `*os.File` / `*bytes.Buffer` / `*bufio.Reader` 各种实现）。

### 2.4 Pipe：唯一的 Reader/Writer 出生方式

```go
sr, sw := schema.Pipe[string](3) // 缓冲 3

go func() {
    defer sw.Close()
    for i := 0; i < 10; i++ {
        sw.Send(strconv.Itoa(i), nil)
    }
}()

defer sr.Close()
for {
    chunk, err := sr.Recv()
    if errors.Is(err, io.EOF) { break }
    if err != nil { return err }
    fmt.Println(chunk)
}
```

背后是一个带缓冲的 `chan`。**cap 是背压的旋钮**：写快过读时，`Send` 会阻塞直到读端消费。

---

## 3. 四种运行范式

这是 Eino 里最重要的一组词，面试要能张口就来：

| 范式 | 输入 | 输出 | gRPC 类比 | Lambda 构造器 |
|---|---|---|---|---|
| **Invoke** | 单值 `I` | 单值 `O` | Unary（Ping-Pong） | `compose.InvokableLambda` |
| **Stream** | 单值 `I` | 流 `StreamReader[O]` | Server-Streaming | `compose.StreamableLambda` |
| **Collect** | 流 `StreamReader[I]` | 单值 `O` | Client-Streaming | `compose.CollectableLambda` |
| **Transform** | 流 `StreamReader[I]` | 流 `StreamReader[O]` | Bidirectional-Streaming | `compose.TransformableLambda` |

### 3.1 每种范式的典型场景

- **Invoke**：Retriever（查一次给一批文档）、Embedder、Indexer、ChatTemplate、Document Loader/Transformer —— 大多数"非模型"的组件天然只需要它
- **Stream**：ChatModel 生成回答、Tool 返回长文本（stdout 流）—— **产出天然分帧**的场景
- **Collect**：ReAct agent 的 branch 节点，只看流的第一个 chunk 就能决定"要不要调 tool"，不需要等全部到齐（省延迟）
- **Transform**：Chain 中间的加工节点、`StatePreHandler` / `StatePostHandler` 这种"流入流出"的切面

### 3.2 官方组件的流式支持矩阵

（**面试可能会问**：哪些组件天然是流？）

| 组件 | Invoke | Stream | Collect | Transform |
|---|:-:|:-:|:-:|:-:|
| ChatModel | ✅ | ✅ | ❌ | ❌ |
| Tool | ✅ | ✅ | ❌ | ❌ |
| ChatTemplate | ✅ | ❌ | ❌ | ❌ |
| Retriever | ✅ | ❌ | ❌ | ❌ |
| Indexer | ✅ | ❌ | ❌ | ❌ |
| Embedder | ✅ | ❌ | ❌ | ❌ |
| Document Loader | ✅ | ❌ | ❌ | ❌ |
| Document Transformer | ✅ | ❌ | ❌ | ❌ |

**Lambda / Branch / StateHandler**：Lambda 全支持；Branch 支持 Invoke 或 Collect（二选一）；StatePreHandler/StatePostHandler/Passthrough 支持 Invoke 和 Transform。

**核心思想**（官方原话）：

> "Eino believes that components should only need to implement streaming paradigms that are real in business scenarios."

组件只写"业务真的用得上"的范式，其他由框架桥接。

---

## 4. 桥接的两个原语：Concat 和 Boxing

框架自动补齐范式的所有花活，都建立在这两个原语上。

### 4.1 T → Stream[T]：Boxing（装箱成单帧流）

把一个完整值包成"只有一帧"的流。**签名不变，但没有低延迟收益**（官方叫 "fake stream"）。

场景：一个只实现了 Invoke 的组件，被下游要求 `Stream[T]` 时，框架直接把返回值塞进单帧流。

### 4.2 Stream[T] → T：Concat（拼接成完整值）

把流上所有 chunk 合并成一个 T。**这一步可能需要用户提供 Concat 方法**：

> "The process of framework automatically concatenating StreamReader[T] into T may require users to provide a Concat function."

`*schema.Message` 已经内置了 Concat（`schema.ConcatMessages`），所以 ChatModel 的流可以直接被拼回一条完整 Message。你自定义的类型如果要走 Concat 路线，需要实现相应约定（v0.9 前用 `Concat[T] func([]T) (T, error)`，v0.9 通过 gob/JSON 注册）。

**本项目实例**：`internal/stream/adk_handler.go:220`
```go
full, cErr := schema.ConcatMessages(chunks)
```
把消费到的所有 chunk 合并回一条完整 Message，从里面取 `ToolCalls` 和 `ResponseMeta.Usage`。

---

## 5. 编排层的整体调度规则

一个组件只实现了一种范式，如何在 Chain/Graph 里跑起来？框架有一套**统一的降级/升级规则**（这是 v0.4+ 以来的取向：**统一优于局部最优**）。

### 5.1 Graph 被整体 Invoke 调用时

内部所有组件都以 Invoke 范式跑。若组件没实现 Invoke，按下面优先级派生：

1. `Stream → Invoke`：跑组件的 Stream，然后 Concat 输出流
2. `Collect → Invoke`：把输入值装箱成单帧流，跑 Collect
3. `Transform → Invoke`：输入装箱，输出 Concat

### 5.2 Graph 被整体 Stream / Collect / Transform 调用时

内部所有组件都以 **Transform** 范式跑。若组件没实现 Transform，按下面优先级派生：

1. `Stream → Transform`：先 Concat 输入流成单值，跑 Stream
2. `Collect → Transform`：跑 Collect，把输出装箱成单帧流
3. `Invoke → Transform`：输入 Concat，输出装箱

**总结成两句话**（面试可背）：

> "Called overall with Invoke, internal components all called with Invoke, no streaming process exists."
>
> "Called overall with Stream/Collect/Transform, internal components all called with Transform."

### 5.3 为什么不追求"每对上下游选最优组合"

官方明确解释过：可以做，但**规则会变得复杂到讲不清**。用户要理解自己的 graph 会走哪条降级路径需要一个决策树。所以刻意选**一致但可能局部次优**的规则，保证心智模型简单：**"要流就一路 Transform，要不流就一路 Invoke"**。

---

## 6. 三个流操作：Merge / Copy / Convert

### 6.1 Merge —— N 个流合成 1 个

```go
func MergeStreamReaders[T any](srs []*StreamReader[T]) *StreamReader[T]
func MergeNamedStreamReaders[T any](srs map[string]*StreamReader[T]) *StreamReader[T]
```

前者不区分来源；后者每个 chunk 会带上来源 name（做 Parallel 合并时用）。

### 6.2 Copy —— 1 个流分成 N 个独立 reader

```go
func (sr *StreamReader[T]) Copy(n int) []*StreamReader[T]
```

**关键约束**：调 `Copy` 之后，**原 sr 不能再用**。N 个 copy 是"独立读的兄弟"，都能读到全量 chunk。

场景：给 Callback handler 和下游 node 同一份流；给主消费和"顺便存盘"分两路。

内部实现是一个 parent + N 个 child：parent 每次读一个 chunk 后广播给所有 child buffer；child 各自消费 buffer 里的 chunk，互不影响。**不是简单 fan-out**（那会漏 chunk），是带缓存的多路复制。

### 6.3 Convert —— 元素类型变换

```go
func StreamReaderWithConvert[T, D any](sr *StreamReader[T], convert func(T) (D, error)) *StreamReader[D]
```

在流上贴一层 map 函数，逐帧转类型。零拷贝、懒执行。

---

## 7. 消费流的标准姿势速查

**always defer Close，然后 Recv 直到 EOF**：

```go
defer sr.Close()
for {
    chunk, err := sr.Recv()
    if errors.Is(err, io.EOF) { break }
    if err != nil { return err }
    // use chunk
}
```

**中途要退出（提前 break/return）**：defer 兜底，不用手动 Close。

**要多个消费者**：先 `Copy(n)`，然后各自 defer + 各自 Recv。

**害怕忘 Close**：`sr.SetAutomaticClose()` 装 finalizer 兜底，但**别当主要手段**，标准 defer 才是。

---

## 8. Send 里的 err 参数怎么用

`Send(chunk T, err error)` 允许边发数据边报错：

```go
sw.Send(chunk, nil)         // 正常发一块
sw.Send(zeroValue, someErr) // 中途出错：读端下一次 Recv 会拿到这个 err（不是 EOF）
```

读端接到非 EOF 错误时应该视作"流坏了"，退出循环。**Send 一次 err 之后通常紧跟 Close**。

---

## 9. 本项目里的流

### 9.1 数据来源

`llm.NewChatModel(...)` 构造出 `ToolCallingChatModel`。它实现了 `Stream` 范式，能吐出 `*schema.StreamReader[*schema.Message]`。

### 9.2 ADK 内部

ChatModelAgent 的 ReAct 循环内部把这条流用 `Runnable.Transform` 编排。ADK 又把每个 chunk 包成一个 `AgentEvent`（`ev.Output.MessageOutput.MessageStream` 就是原始 `*StreamReader[*schema.Message]`），放进 `AsyncIterator[*AgentEvent]`。

### 9.3 我们的消费

`internal/stream/adk_handler.go` 的 `drainAssistantStream` 拿到 event 后：

```go
defer sr.Close()

var chunks []*schema.Message
for {
    chunk, err := sr.Recv()
    if err != nil {
        if isEOF(err) { break }
        return err
    }
    // 逐 chunk 发 SSE 帧（thinking / text）
    ...
    chunks = append(chunks, chunk)
}

// 流终了：Concat 成完整 Message
full, _ := schema.ConcatMessages(chunks)
// 从 full 里取 ToolCalls / Usage 发 tool_call / usage 帧
```

**这是标准的"Stream + Concat"混合消费模式**：
- **实时性**要求高的部分（token 文本）在流上逐 chunk 转发
- **需要完整数据才好处理**的部分（ToolCall 的完整 JSON、usage 汇总）用 Concat 拼回完整 Message 后一次性处理

**关键点**：我们没让框架自动 Concat，是因为我们**同时需要**"每个 chunk 立刻发前端"和"拿到完整 message 提取 tool_call"—— 只调 `Recv` 攒 chunks + 最后手动 `ConcatMessages` 是最直接的写法。

---

## 10. 记忆锚点

- **`Pipe[T](cap)`** 是唯一 Reader/Writer 出生入口；`cap` 是背压旋钮
- **一次性读、必须 Close 一次、扇出用 Copy** —— 三条铁律
- **四种范式**：Invoke（Unary）/ Stream（Server-Streaming）/ Collect（Client-Streaming）/ Transform（Bidirectional）
- **两个桥接原语**：Boxing（值→单帧流）、Concat（流→值，`schema.ConcatMessages` 用于 Message）
- **整图调度**：Invoke 模式全内部走 Invoke；Stream/Collect/Transform 模式全内部走 Transform（一致优于局部最优）
- **三种操作**：Merge（N→1）、Copy（1→N，原 sr 作废）、Convert（元素类型变换）
- **本项目消费姿势**：`defer Close` + `for Recv` + 攒 chunks + `ConcatMessages` 拼回完整 Message 取结构化字段
