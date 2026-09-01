# 09 · eino-ext 生态与扩展

Eino 分三个仓库：`eino`（核心）、`eino-ext`（实现/扩展）、`eino-examples`（示例）。**核心与实现分离**是关键设计选择 —— 每家 SDK（OpenAI / Claude / Milvus / Elasticsearch …）版本独立发布，任一 provider 升级不会牵连核心。

这一章拆 `eino-ext` 生态清单、按 component 分类看有哪些现成实现、并结合本项目 `go.mod` 里已经引入的 ext 包对号入座。

---

## 1. 仓库定位

```
github.com/cloudwego/eino            核心：接口、编排、类型、流、回调、ADK
github.com/cloudwego/eino-ext        扩展：各家 provider 实现 + Callback handler + DevOps
github.com/cloudwego/eino-examples   完整示例
```

**为什么切三个仓库**（面试考点 —— 已在 [00 §3](00-overview.md) 说过，这里再补一层理由）：

- **版本独立**：`eino v0.9.1` 稳定，`eino-ext/components/model/claude v0.1.20` 可以频繁 patch 跟进 Anthropic SDK 变化，两者互不影响
- **按需引入**：`go get` 只拉你实际用的 provider 包，不带整个生态的传递依赖
- **社区贡献友好**：加一个新 provider 只需要在 eino-ext 提 PR，不用碰核心

---

## 2. eino-ext 顶层目录

```
eino-ext/
  ├─ components/         各类 component 的官方实现
  │   ├─ model/           ChatModel providers
  │   ├─ tool/            Tool 实现
  │   ├─ retriever/       Retriever 实现
  │   ├─ indexer/         Indexer 实现
  │   ├─ embedding/       Embedding 实现
  │   ├─ document/        Loader / Parser / Transformer
  │   ├─ prompt/          ChatTemplate 实现
  │   └─ lambda/          常用 Lambda（如 JSONMessageParser）
  ├─ callbacks/           Callback handler 实现（观测/追踪）
  ├─ adk/backend/         ADK 相关的后端扩展（如 filesystem backend）
  ├─ acp/                 Agent Communication Protocol 集成
  ├─ devops/              Eino Dev IDE 插件（可视化编排、调试）
  ├─ skills/              技能包（本项目 skills 是自己实现的，不用这个）
  └─ libs/acl/            工具库（access-control 等）
```

---

## 3. Components 生态清单

按 component 类型汇总（截至 v0.9 生态，具体版本以仓库为准）：

### 3.1 ChatModel providers

| Provider | 说明 |
|---|---|
| `components/model/openai` | 官方 OpenAI 兼容协议（同时用于 Azure OpenAI、其他兼容 provider）。**本项目生产在用**：接 DeepSeek 走的就是这个兼容适配器 |
| `components/model/claude` | Anthropic Claude。**只在 `test/` 的集成测试里用**，生产链路没走 |
| `components/model/gemini` | Google Gemini |
| `components/model/ark` | 火山方舟（字节内部大模型平台） |
| `components/model/qwen` | 通义千问 |
| `components/model/ollama` | 本地 Ollama |
| `components/model/deepseek` | DeepSeek |

**用法一致**：`xxx.NewChatModel(ctx, &xxx.Config{...})` 返 `model.ToolCallingChatModel`。**换 provider 只改这一行**，业务代码不用动。

### 3.2 Tool 实现

| 目录 | 说明 |
|---|---|
| `components/tool/googlesearch` | Google 搜索 |
| `components/tool/duckduckgo` | DuckDuckGo 搜索 |
| `components/tool/commandline` | Shell 命令执行（**本项目自己实现了 `run_command`，没用这个**） |
| `components/tool/httprequest` | HTTP 请求 |
| `components/tool/bingsearch` | Bing 搜索 |

**注意**：这些是"通用工具"实现。业务侧 tool（如本项目的 `list_files` / `browser_bridge`）永远是自己写的 —— 生态里的 tool 只覆盖"跨项目复用"的场景。

### 3.3 Retriever / Indexer / Embedding（RAG 全套）

RAG 需要三件套：**Embedding**（文本 → 向量）、**Indexer**（向量入库）、**Retriever**（向量查询）。

| 类型 | 常见 provider |
|---|---|
| Embedding | `openai`、`ark`、`dashscope` |
| Indexer | `milvus`、`elasticsearch`、`volc_vikingdb`、`qdrant`、`redis` |
| Retriever | `milvus`、`elasticsearch`、`volc_vikingdb`、`qdrant`、`redis` |

> **本项目这三件套一个都没用，RAG 是完全自研的**（`internal/rag/` 下的
> `chunker` / `embedding` / `indexer` / `retriever` / `store` / `vector`）。
> 原因见 [10 §9](10-in-this-project.md)：检索链路的每一环都要能独立调试，
> 包进框架的执行模型里就得起一个图才能验一次；而且本地题库是有限静态语料，
> 暴力向量检索够用，引入 Milvus 是纯运维负担。
>
> 下面这段是**框架用法示意**，不是本项目代码。

**框架标准用法**：

```go
// 1. 建 Embedding
emb, _ := openai.NewEmbedder(ctx, &openai.EmbeddingConfig{
    APIKey: cfg.EmbeddingAPIKey,
    Model:  "text-embedding-3-small",
})

// 2. 建 Retriever（关联 milvus + 上一步的 embedder）
r, _ := milvus.NewRetriever(ctx, &milvus.RetrieverConfig{
    Client:     milvusClient,
    Collection: "interview_notes",
    Embedding:  emb,
    TopK:       5,
})

// 3. 用
docs, _ := r.Retrieve(ctx, "Go 泛型有什么坑")
```

**Embedding 注入 Retriever**：调 `Retrieve` 时框架自动用 `emb` 把 query 转向量再查。**这就是 [01 §3.5](01-core-abstractions.md) 讲的组件透明性**——同一个 `Retriever` 接口，不管背后是 milvus/es/redis，业务代码完全一致。

### 3.4 Document Loader / Parser / Transformer

**文本摄入 → 分片 → 索引** 的前置管线。

| 类型 | 常见 provider |
|---|---|
| Document Loader | `webload`（爬 URL）、`s3`、`local file`、`gcs` |
| Document Parser | `pdf`（**本项目用 pdfcpu + 自己写的 extract_document_text tool**）、`docx`、`html` |
| Document Transformer | `htmlsplitter`、`markdown_splitter`、`recursive_splitter`、`score_reranker` |

**本项目对文档抽取的选择**：没有走 eino-ext 的 loader/parser（因为要在**工具**层被 agent 主动调用，而不是"预先索引全部文档"），而是自己写了 `extract_document_text` tool（`internal/agent/tools/extract_document.go`），用第三方 Go 库分别处理 PDF/DOCX/PPTX/图片 OCR。这样 agent 才能"用户附件传来 → 现场读"。

### 3.5 ChatTemplate & Lambda

| | 说明 |
|---|---|
| `components/prompt/default_template` | 标准 ChatTemplate 实现（FString / Jinja2 / GoTemplate） |
| `components/lambda/json_message_parser` | 从 Message.Content 或 tool_call.arguments 解 JSON |

**本项目 prompt 是纯字符串常量**，没走模板；也没用 JSONMessageParser（因为工具的 args 我们直接从 `tool.Function.Arguments` 拿）。

---

## 4. Callback handler 生态

`eino-ext/callbacks/`：

| 包 | 用途 |
|---|---|
| `langfuse` | 把每个 component 调用发到 [Langfuse](https://langfuse.com/) 做 LLM 观测（token / cost / trace / eval） |
| `cozeloop` | 字节 CozeLoop 观测平台 |
| `apmplus` | 火山 APM+ 追踪 |

**装法一致**（呼应 [06 §6](06-callbacks.md)）：

```go
// main.go 启动时
tracer, _ := langfuse.NewHandler(&langfuse.Config{
    PublicKey:  os.Getenv("LANGFUSE_PK"),
    SecretKey:  os.Getenv("LANGFUSE_SK"),
    Host:       "https://cloud.langfuse.com",
})
callbacks.AppendGlobalHandlers(tracer)
```

一行注册，之后所有 component（ChatModel / Tool / Retriever）的 OnStart/OnEnd 都自动上报。**这就是 Callback 抽象最大的价值**：观测方案与业务代码完全解耦，换观测平台不用改一行业务逻辑。

**本项目暂未接入任何 tracing handler**（面试可以说"下一步准备接 Langfuse，callback 层已经准备好，只需要 AppendGlobalHandlers 一行"）。

---

## 5. `eino-ext/devops` —— Eino Dev IDE 插件

一个 JetBrains / VSCode 插件（`Eino Dev`），支持：

- **可视化编排**：拖拽拼 Graph，代码同步生成
- **可视化调试**：跑起来后每个节点的 input / output 实时展示
- **图渲染**：把 `compose.Graph` 的静态拓扑画出来（借助 Compile 阶段完整的类型信息）

**为什么这个能做**：呼应 [02 §6](02-orchestration.md) —— **Compile 后图是静态的**，静态就能被外部工具解析和可视化。LangGraph 因为图可以运行时改，可视化会难很多。

**本项目未接入**（走 ADK 路线，不是 Graph 手搭，可视化价值降低）。

---

## 6. `eino-ext/adk/backend` —— ADK 的后端扩展

主要是**给 ADK middleware 提供后端 impl**：

- `filesystem/local`：本地 fs backend，让 `middleware.NewFileSystemMiddleware(...)` 能操作真实磁盘
- `filesystem/ark_agentkit_sandbox`：火山 ARK Agentkit 沙箱 backend，让 agent 在隔离环境跑

**本项目 workspace 工具（`list_files` / `read_file` / `write_file` / …）是自己实现的 tool**，没用 filesystem middleware 那条路。区别：
- **eino-ext filesystem middleware**：ADK 自动挂载一组 fs 工具、自动做安全边界检查
- **本项目自实现**：直接给 agent 挂业务侧的 tool，边界规则自己写（workspace 内可写、绝对路径可读，见 `prompts/general.go` 的 workspace 规则）

选后者的理由：本项目要区分 workspace（写限制）vs 任意路径（读放开），这个策略比 eino filesystem middleware 的默认要精细，自己控更方便。

---

## 7. 本项目 `go.mod` 里的 eino-ext 依赖

现在的直接依赖清单：

```go
require (
    github.com/cloudwego/eino v0.9.1
    github.com/cloudwego/eino-ext/components/model/openai v0.1.13  // 生产：接 DeepSeek
    github.com/cloudwego/eino-ext/components/model/claude v0.1.20  // 仅 test/ 使用
)
```

RAG 相关的 eino-ext 包**一个都没引**——那条链路是自研的。

**只引 2-3 个包**，不带整个生态传递依赖 —— 这就是切三个仓库的实际收益。

面试点：讲到"依赖管理" 时可以点这个：**Eino 的模块化让 LLM 应用的依赖体积可控**，不像 Python 那样一个 LangChain 依赖一大堆。

---

## 8. `eino-ext/acp/` —— Agent Communication Protocol

新兴方向（v0.9 相关生态）：**agent 之间跨进程通信的标准协议**。让一个 eino agent 能作为 client 调用另一个远程 agent（无论对方是不是 Go 写的）。

**未成熟**，本项目未用。面试聊到"Multi-Agent 未来" 时可以带一句：Eino 在 acp 上下注了跨进程/跨语言的 agent 编排能力。

---

## 9. 迁移新 provider 的成本估算

举个例子：**把 Claude 换成 DeepSeek 需要改什么**：

```go
// 之前：
import "github.com/cloudwego/eino-ext/components/model/claude"

cc := &claude.Config{APIKey: ..., BaseURL: &baseURL, Model: ..., ...}
return claude.NewChatModel(ctx, cc)

// 之后：
import "github.com/cloudwego/eino-ext/components/model/deepseek"

cc := &deepseek.Config{APIKey: ..., BaseURL: ..., Model: ..., ...}
return deepseek.NewChatModel(ctx, cc)
```

> 本项目实际走的是**第三条路**：用 `components/model/openai` 这个
> OpenAI 兼容适配器去接 DeepSeek，而不是用 DeepSeek 专用适配器。
> 好处是同一份代码能接任何 OpenAI 兼容的服务，换 provider 只改配置不改代码；
> 代价是 DeepSeek 的私有能力（thinking 模式）要靠 `ExtraFields` 透传，
> 而不是用类型化的 config 字段——见 `internal/agent/llm/llm.go`。

**代码改动**：一个 import + 一个 config struct。**业务侧完全零改动**（因为返值都是 `model.ToolCallingChatModel`）。

**但有一个坑**：不同 provider 的 stream chunk 顺序不一样（[07 §5](07-react-agent.md) 讲的 Claude 先文本后 tool_call）。切 provider 时**默认 StreamToolCallChecker 可能要调**。ADK 内部封装得较好，一般不用管；如果走 flow/agent/react 就得注意。

---

## 10. 记忆锚点

- **三仓库分工**：`eino`（接口/编排/ADK）、`eino-ext`（实现）、`eino-examples`（示例）—— 版本独立、按需引入、社区友好
- **components 8 类**：ChatModel / Tool / Retriever / Indexer / Embedding / Document(Loader/Parser/Transformer) / ChatTemplate / Lambda
- **ChatModel 主流 provider**：OpenAI / Claude / Gemini / Ark / Qwen / Ollama / DeepSeek — 换 provider 只改 1-2 行
- **RAG 三件套**：Embedding + Indexer + Retriever（**本项目一个没用，全自研**）
- **Callback handler 生态**：Langfuse / CozeLoop / APM+ —— `AppendGlobalHandlers(h)` 一行注册
- **DevOps IDE 插件**：`eino-ext/devops`，靠 Compile 后静态图能力
- **本项目现役**：只有 `components/model/openai`（接 DeepSeek）；claude 仅测试用；RAG 链路完全自研，没引任何 eino-ext RAG 包
- **切 provider 的隐藏坑**：不同 provider 流式输出顺序不同，走 flow/agent/react 时 `StreamToolCallChecker` 要调；ADK 内部封装了
