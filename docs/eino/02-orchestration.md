# 02 · 编排：Chain / Graph / Workflow

Eino 的 `compose` 包提供三套编排 API。**三者共享同一套底层**（Node、Edge、State、Callback、Stream、Interrupt、Checkpoint），差别在拓扑约束和 API 糖度。

---

## 1. 一句话对比

| API | 一句话 | 底层 | 拓扑约束 | 触发模式 |
|---|---|---|---|---|
| **Chain** | 线性追加的语法糖 | Graph 的包装 | 无分岔（除 Branch/Parallel 语法糖外） | Pregel |
| **Graph** | 完整的有向图，节点+边+分支+并行+状态 | 直接就是引擎 | 可有环、可有环无环任选 | 可配置（Pregel / DAG） |
| **Workflow** | 有向无环图 + 字段级数据映射 | Graph 的另一种约束 | 严格 DAG，禁止环 | 固定 DAG（`AllPredecessor`） |

> 官方原话："Chain can be considered a simplified wrapper over Graph"；Workflow 与 Graph 平级，都构建在 compose 引擎之上。

---

## 2. Chain —— 线性追加

用 `AppendXxx` 一路串起来即可，顺序即执行顺序。

```go
chain := compose.NewChain[map[string]any, *schema.Message]()

chain.
    AppendChatTemplate(promptTpl).
    AppendChatModel(chatModel).
    AppendLambda(compose.InvokableLambda(func(ctx context.Context, msg *schema.Message) (string, error) {
        return msg.Content, nil
    }))

runnable, err := chain.Compile(ctx)   // → Runnable[map[string]any, string]
```

### 2.1 Chain 支持的 Append 系列（源码 `compose/chain.go`）

- 组件类：`AppendChatModel` / `AppendAgenticModel` / `AppendChatTemplate` / `AppendAgenticChatTemplate` / `AppendToolsNode` / `AppendAgenticToolsNode` / `AppendRetriever` / `AppendIndexer` / `AppendEmbedding` / `AppendLoader` / `AppendDocumentTransformer`
- 通用：`AppendLambda(...)` / `AppendPassthrough(...)`
- 结构糖：`AppendBranch(*ChainBranch)` / `AppendParallel(*Parallel)` / `AppendGraph(AnyGraph)`

### 2.2 Chain 里的 Branch（选一）

```go
branchCond := func(ctx context.Context, in map[string]any) (string, error) {
    if rand.Intn(2) == 0 {
        return "b1", nil
    }
    return "b2", nil
}
chain.AppendBranch(
    compose.NewChainBranch(branchCond).
        AddLambda("b1", b1Lambda).
        AddLambda("b2", b2Lambda),
)
```

条件函数返回 key，框架跑对应 key 的节点。

### 2.3 Chain 里的 Parallel（同输入 → 多输出 → 合并 map）

```go
parallel := compose.NewParallel().
    AddLambda("role", compose.InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
        return "bird", nil
    })).
    AddLambda("input", compose.InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
        return "What does your call sound like?", nil
    }))

chain.AppendParallel(parallel)
```

输出永远是 `map[string]any`，key 就是 AddLambda 时给的 key。**Parallel 里嵌套 Branch/Parallel 不支持**。

---

## 3. Graph —— 完整拓扑

### 3.1 构建 API

```go
g := compose.NewGraph[map[string]any, *schema.Message]()

// 加节点：每种组件类型对应一个 AddXxxNode 方法
_ = g.AddChatTemplateNode("tpl",  promptTpl)
_ = g.AddChatModelNode("model", chatModel, compose.WithNodeName("ChatModel"))
_ = g.AddLambdaNode("post", myLambda)

// 加边：START/END 是保留节点
_ = g.AddEdge(compose.START, "tpl")
_ = g.AddEdge("tpl", "model")
_ = g.AddEdge("model", "post")
_ = g.AddEdge("post", compose.END)

runnable, err := g.Compile(ctx)   // → Runnable[map[string]any, *schema.Message]
```

`compose.START = "start"`、`compose.END = "end"`（`compose/graph.go:37,40`）。

### 3.2 支持的 AddNode（源码 `compose/graph.go`）

`AddChatModelNode` / `AddChatTemplateNode` / `AddToolsNode` / `AddLambdaNode` / `AddRetrieverNode` / `AddIndexerNode` / `AddEmbeddingNode` / `AddLoaderNode` / `AddDocumentTransformerNode` / `AddPassthroughNode` / `AddGraphNode`（把另一个图当节点）/ `AddAgenticModelNode` / `AddAgenticChatTemplateNode` / `AddAgenticToolsNode` / `AddBranch`

**注意**：Graph 没有专门的 `AddToolNode`（单个 Tool），Tool 都在 `ToolsNode` 里聚合调度（详见 `05-tools-and-function-call.md`）。

### 3.3 Branch（Graph 版）

```go
branch := compose.NewGraphBranch(
    func(ctx context.Context, in float64) (string, error) {
        if in > 5.0 { return compose.END, nil }
        return "next", nil
    },
    map[string]bool{compose.END: true, "next": true}, // 所有候选目标
)
_ = g.AddBranch("upstream", branch)
```

第二个参数列出所有可能的 target key，让框架在构图期做类型对齐检查。

### 3.4 State —— 图作用域的可变共享

节点本身应"无状态"，但整张图可以携带一个共享 State：

```go
type myState struct { Count int }

g := compose.NewGraph[string, string](
    compose.WithGenLocalState(func(ctx context.Context) *myState {
        return &myState{}
    }),
)

// 节点上挂 Pre/Post handler：
_ = g.AddLambdaNode("l1", l1,
    compose.WithStatePreHandler(func(ctx context.Context, in string, s *myState) (string, error) {
        s.Count++
        return in, nil
    }),
    compose.WithStatePostHandler(func(ctx context.Context, out string, s *myState) (string, error) {
        return out + fmt.Sprintf(" (count=%d)", s.Count), nil
    }),
)

// 节点内部要读写 State：
_ = compose.ProcessState[*myState](ctx, func(_ context.Context, s *myState) error {
    s.Count++
    return nil
})
```

**Read-Only 原则**（官方原文）：

> "Node, Branch, and Handler should not modify Input internally. If modification is needed, first Copy it yourself."

State Handler 位于节点外部，用来在节点入口/出口读写 state，**保持节点本身无状态**（官方原话："These state handlers are located outside the node, affecting the node through modifications to Input or Output, thus ensuring the node's 'state-agnostic' characteristic"）。

---

## 4. **执行引擎：Pregel vs DAG**（Eino 里被低估的知识点）

Graph 内部有两种执行引擎，通过 `NodeTriggerMode` 切换：

| 模式 | 常量 | 触发规则 | 允许环 | 谁用它 |
|---|---|---|---|---|
| **Pregel** | `AnyPredecessor` | 任一前驱完成即可触发；一批"当前活跃节点"的所有后继一起跑（SuperStep 概念） | ✅ | Chain、react-agent、Graph 默认 |
| **DAG** | `AllPredecessor` | 节点所有前驱都完成才触发；Branch 里未选中的分支被标记为 skip | ❌ | Workflow 强制、Graph 可选 |

### 4.1 Pregel（默认）

- 借鉴 Google Pregel 论文的图计算模型
- 一个 SuperStep 内并发跑所有可触发节点，跑完再进入下一步
- 支持环（因此可以表达 ReAct loop：`model → tool → model → tool → ...`）
- 灵活但认知成本略高（有时候需要手动加 Passthrough 节点让两条支路在同一个 SuperStep 落地）

### 4.2 DAG

- 直接的拓扑排序执行
- 无环，一个节点等所有前驱完成才动
- v0.4+ `AllPredecessor` 默认开启 **Eager Execution**：任何前驱就绪就立刻跑，不再攒 SuperStep
- 更简单、更贴近 Airflow / Dagster 那种数据流图心智模型

### 4.3 官方一句话结论

> "pregel mode is flexible and powerful but has additional cognitive burden, dag mode is clear and simple but limited in scenarios."

---

## 5. Workflow —— DAG + 字段级映射

Workflow 是 Graph 的一种"约束"变体，但 API 完全不同，长得更像 Airflow / Coze Studio 的可视化工作流。**Coze Studio 的开源工作流引擎就是基于 Eino Workflow 建的**。

### 5.1 三个和 Graph 的关键差异

1. **强制 DAG**（`NodeTriggerMode` 锁定 `AllPredecessor`），禁止环
2. **字段级 data mapping**：下游节点的输入可以从上游任意节点的输出**字段**拼装
3. **控制流和数据流可分离**：可以只依赖但不取数据，也可以取数据但不作为前驱

### 5.2 最简例子

```go
wf := compose.NewWorkflow[int, string]()

wf.AddLambdaNode("lambda", compose.InvokableLambda(
    func(ctx context.Context, in int) (string, error) {
        return strconv.Itoa(in), nil
    },
)).AddInput(compose.START)   // 输入全部来自 START

wf.End().AddInput("lambda")   // 输出即 lambda 的输出

run, err := wf.Compile(ctx)
```

### 5.3 字段映射示例（多来源拼输入）

```go
// message 结构体有 SubStr / Message.Content / Message.ReasoningContent
wf := compose.NewWorkflow[message, map[string]any]()

wf.AddLambdaNode("c1", compose.InvokableLambda(wordCounter)).
    AddInput(compose.START,
        compose.MapFields("SubStr", "SubStr"),
        compose.MapFieldPaths([]string{"Message", "Content"}, []string{"FullStr"}))

wf.AddLambdaNode("c2", compose.InvokableLambda(wordCounter)).
    AddInput(compose.START,
        compose.MapFields("SubStr", "SubStr"),
        compose.MapFieldPaths([]string{"Message", "ReasoningContent"}, []string{"FullStr"}))

wf.End().
    AddInput("c1", compose.ToField("content_count")).
    AddInput("c2", compose.ToField("reasoning_content_count"))
```

**字段映射解决的核心痛点**（原文提炼）：如果两个节点各有独立的 input/output struct，要么强行引入共享 struct（侵入），要么全用 `map[string]any`（失去类型安全）。Workflow 让每个节点保持"业务自然的 I/O 结构"，通过 field mapping 拼接。

### 5.4 控制/数据分离的三种写法

```go
// 只走数据、不作为控制前驱：
wf.AddLambdaNode("mul", compose.InvokableLambda(multiplier)).
    AddInput("adder", compose.ToField("A")).
    AddInputWithOptions(compose.START,
        []*compose.FieldMapping{compose.MapFields("Multiply", "B")},
        compose.WithNoDirectDependency())

// 只作为控制前驱、不取数据：
wf.AddLambdaNode("announcer", compose.InvokableLambda(announcer)).
    AddDependency("b1")

// 塞静态值（不来自任何节点）：
wf.AddLambdaNode("b1", compose.InvokableLambda(bidder)).
    AddInput(compose.START, compose.ToField("Price")).
    SetStaticValue([]string{"Budget"}, 3.0)
```

### 5.5 Workflow 的 Branch 有点不一样

Graph 的 Branch：条件出的 key 决定下游，**并且**上游的输出直接作为下游的输入（控制和数据绑在一起）。

Workflow 的 Branch：**只传控制，不传数据**。数据必须显式用 `AddInputWithOptions` 声明。这也是它把控制/数据分离贯彻到底的体现。

### 5.6 一些约束

- Map 的 key 必须是 `string` 或 `string` 别名
- Workflow 不支持 `WithNodeTriggerMode`、`WithMaxRunSteps` 编译选项
- 用于映射的 struct field 必须导出（反射依赖）
- 同一目标字段不能从多个来源映射
- 有 `WithCustomExtractor` 但会关掉编译期类型对齐检查

---

## 6. Compile —— 把图变成 Runnable

不管是 Chain、Graph 还是 Workflow，最后都要过一步：

```go
runnable, err := whatever.Compile(ctx, opts...)
```

`Compile` 干了这些事：

1. **拓扑校验**：起点/终点连通、无孤立节点、DAG 模式下无环等
2. **类型对齐检查**：见下面 §7
3. **并发计划**：Pregel 的 SuperStep 划分、DAG 的拓扑序 / eager 触发
4. **Callback 织入**：把默认注册的 Callback Handler 织到每个节点的入口/出口
5. **Runnable 生成**：返回 `Runnable[I, O]`，就是 `01` 讲的四方向执行契约

**Compile 后不能再改图**。这是 Eino 相对 LangGraph（可变 StateGraph）的一个刻意选择：换来构图期完整校验、静态可分析、可可视化（Eino Dev 插件依赖这个）。

---

## 7. 类型对齐检查发生在几个阶段

面试会考的一条：

| 阶段 | 抓什么错 |
|---|---|
| **`AddXxxNode` 时** | 节点自身的输入/输出类型和组件签名不匹配 |
| **`AddEdge(from, to)` 时** | 上游 out 类型和下游 in 类型不能对齐 |
| **`Compile()` 时** | Chain 因为没显式 AddEdge，类型错都在这一步集中报；还有 Graph 的整体连通性、终止性等 |
| **运行期** | 只有一种情况留到运行期：上游输出是 interface、下游是实现该 interface 的具体类型 —— 只能等运行时才能确认实际值 |

Eino 提供两个转换器缓解类型冲突：

- `compose.WithOutputKey("foo")` —— 把节点输出包一层 `map[string]any{"foo": out}`
- `compose.WithInputKey("foo")` —— 从上游 `map[string]any` 里取一个 key 喂给下游

---

## 8. 四条设计原则（官方，面试要能复述）

1. **基本假设**："The output value of the previous running node can serve as the input value for the next node."
2. **Read-Only 原则**：Node/Branch/Handler 不能修改 Input，要改先自己 Copy。
3. **流处理观**：组件只实现"业务场景真实需要的"流范式，其他框架自动降级。
4. **State Handler 外置**：State 读写通过节点外部的 Pre/Post Handler 进行，保证节点本身 state-agnostic。

---

## 9. 本项目落地映射

本项目**没有直接使用 Chain/Graph/Workflow API 编排**（走的是 ADK 路线），但仍在两个位置间接接触到编排层：

1. **`adk.ChatModelAgent` 内部**：ADK 层用 Graph 实现了 ReAct 循环（`model → tool → model → tool → ...`），是典型的 Pregel 模式（有环）。你在 `internal/agent/adk_agent.go` 里看到的 `compose.ToolsNodeConfig`、`compose.ToolMiddleware`、`adk.ToolsConfig{...}` 都是通过这一层间接暴露的。

2. **`compose.ToolsNodeConfig`**：Graph 的 `AddToolsNode` 需要 `*ToolsNode`，构造 `*ToolsNode` 需要 `ToolsNodeConfig{Tools: []tool.BaseTool, ToolCallMiddlewares: []ToolMiddleware}`。ADK 里的 `adk.ToolsConfig` 内嵌了它，所以本项目 3 个 Agent 都通过这个入口挂上工具集和 `toolerr.Middleware`。

**如果哪天要做"结构化子任务"**（例如"从简历里抽 JSON → 校验 → 保存"），会走 Workflow 路线（字段级映射刚好合适）；**当前对话式主流程走 ADK 更合适**（详见 `08-multi-agent.md`）。

---

## 10. 记忆锚点

- **Chain 是 Graph 的糖**；**Workflow 是 Graph 的严格 DAG 变体**；**Graph 支持 Pregel 和 DAG 两种引擎**。
- **START="start", END="end"** 是保留节点。
- **Compile 之后不能改图**（换来完整静态检查）。
- **Pregel 允许环**（ReAct loop 靠它）；**DAG 不允许环**（Workflow 靠它）。
- **类型对齐**在 AddNode / AddEdge / Compile 三阶段中尽可能早报错，只留 interface→concrete 的一种到运行期。
- **Workflow 独有**：字段级 mapping + 控制/数据流分离 + 静态值 + eager 触发。
- 本项目走的是 **ADK 路线**（详见 `07`、`08`），Chain/Graph/Workflow 是知识储备。
