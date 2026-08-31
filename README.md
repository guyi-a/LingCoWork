# LingCoWork

**跟 AI 一起开工。** LingCoWork 是一个本地 Agent 工作站：以求职准备作为真实场景，用
Go + eino ADK 验证多 Agent 架构，并围绕模型补齐工具执行、审批恢复、上下文管理和桌面运行等
Agent Harness 能力。

> 仓库里 Go module 名仍是 `Interview-Agent`（历史遗留），产品在演进过程中扩展成了通用的 Co-work Agent，UI 侧统一叫 **LingCoWork**。

---

## 核心架构

### 六 Agent 拓扑

`supervisor` 负责直接执行、派发和汇总，五个子 Agent 通过 AgentAsTool 挂载，各自使用独立上下文：

```text
supervisor
├── deep_research       多步研究与结构化报告
├── job_search          招聘网站搜索与岗位整理
├── resume_analyzer     简历与 JD 匹配分析
├── question_planner    模拟面试题生成
└── explore             只读代码搜索与受限命令探测
```

eino ADK 提供 ChatModelAgent、Runner、AgentAsTool、事件迭代器和 interrupt 协议；本项目负责
拓扑设计、SQLite checkpoint、事件持久化、审批恢复和产品层展示。

### Agent Harness

- **26 个内置工具**：时间、RAG、人工提问、Plan/Todo、代码搜索、文件读写、目录操作、命令执行、文档解析、
  Workspace、浏览器、联网、Skill 加载和长期记忆。准确名称以
  `tools.BuiltinToolNames()` 为准。
- **六个运行时中间件**：Skills 索引、两级 Memory、Workspace、MCP 动态工具、Agent/Plan 模式和结构化任务状态均在
  每轮运行前刷新；安装 Skill 或完成 OAuth 后无需重启 Agent。
- **Effect 审批**：策略判断的是调用后果，而不是工具名。未知 effect 默认询问用户；
  pending approval 和 checkpoint 会落库，页面刷新或进程重启后仍可恢复。
- **流式与子 Agent 可观测性**：Runner 事件编码为 SSE，主回复、thinking、工具调用和
  sub-agent 内部事件按层级展示并持久化。
- **消息级增量持久化**：完整 assistant/tool 消息在边界立即幂等落库；Buffer 只保留尚未
  持久化的实时增量，取消或重启最多损失当前未完成边界，不丢整轮已完成步骤。
- **上下文压缩**：在轮次之间把旧历史折叠成摘要；原消息不删除，UI 仍展示完整记录。
- **结构化验证**：`run_command` 可声明 test/build/lint/typecheck/format，常见 Go、TypeScript、
  ESLint 诊断会持久化到 Problems，并可跳转到对应文件行。
- **原生图像理解**：默认使用 `deepseek-v4-flash-vision-exp`，用户主动附加的
  JPEG/PNG/GIF/WebP 会作为图片内容发送给 DeepSeek；关闭 `LLM_MULTIMODAL` 后改走本地 OCR。

```text
get_current_time  rag_search          ask_user             read_file
list_files        glob                grep                 file_info
extract_document_text write_file      apply_patch          write_file_chunked
mkdir             rm                  mv                   cp
run_command       browser_use         browser_bridge       browser_use_install
web_search        web_fetch           load_skill           remember
create_plan       todo_write
```

### 动态能力

- **Skills / SkillHub**：内置 `docx`、`pdf`、`pptx`、`bosszp`、`browser-bridge`、
  `browser-use`，也支持从 SkillHub 安装用户 Skill。
- **MCP**：支持 stdio、Streamable HTTP / SSE、动态增删、连接测试以及授权码 + PKCE +
  动态客户端注册。远端工具只注入 supervisor，并沿用同一套 effect 审批。
- **Memory**：用户级与项目级 Markdown 记忆，每轮注入；模型通过 `remember` 写入，
  覆盖式修改需要审批。
- **RAG 与联网搜索**：Hybrid Vector + BM25 检索；联网搜索支持 Tavily / Bocha 双源。

### Workspace 与桌面应用

- 项目或会话可以挂载独立 Workspace；所有文件工具都会做边界解析，越界读写进入审批或被拒绝。
- 右侧 Workspace 面板提供 Files / Diff / Problems / Terminal：Files 支持 Markdown、代码、CSV、PDF、
  docx、pptx、图片和音视频只读预览；Diff 区分本轮 Agent 与全部 Git 变更；Terminal
  通过 PTY 提供以 Workspace 为 cwd 的交互式登录 shell。HTML 默认按源码展示，不在应用本源中执行。
- Electron 开发态加载 Vite；打包态通过 `lingcowork://app` 加载包内前端，并托管
  `darwin/arm64` Go sidecar。后端就绪后才显示窗口，退出时按进程组清理子进程。

## 技术栈

| 层 | 技术 |
| --- | --- |
| Backend | Go 1.26 · Gin · GORM · SQLite (glebarez/sqlite) · Eino |
| LLM | DeepSeek（eino-ext OpenAI adapter） |
| Frontend | React 19 · Vite 7 · TypeScript 5.9 · TailwindCSS 4 · Zustand |
| Desktop | Electron 40 · Electron Builder |
| Storage | SQLite（对话、checkpoint、OAuth、向量）· Markdown（Memory） |

## 目录结构

```text
.
├── cmd/
│   ├── api/            # Gin 主服务，127.0.0.1:9001
│   ├── rag-index/      # RAG 离线索引
│   └── rag-search/     # RAG 命令行检索
├── internal/
│   ├── agent/          # 六 Agent、26 个工具、Skills、运行时中间件
│   ├── approval/       # 审批模式、分类器、pending 状态
│   ├── compaction/     # 跨轮上下文压缩
│   ├── effect/         # 工具后果描述与推导
│   ├── handler/        # HTTP / SSE / Workspace / MCP handlers
│   ├── instructions/   # 快捷指令文件存储
│   ├── mcp/            # MCP 生命周期、动态工具、OAuth PKCE
│   ├── memory/         # 两级长期记忆
│   ├── rag/            # chunker、embedding、indexer、retriever
│   ├── repository/     # SQLite / GORM 仓库
│   ├── workplan/       # Plan/Todo 持久化、编辑与状态校验
│   ├── service/        # 对话、项目、Workspace 领域服务
│   ├── skillhub/       # Skill 市场与安装
│   └── stream/         # SSE Buffer 与阶段追踪
├── web/                # React 前端与 Workspace 预览
├── electron/           # Electron Main / Preload / Builder
├── scripts/            # macOS 打包脚本
├── docs/               # 复习材料、算法、RAG 文档与简历
├── data/               # 开发态 SQLite 与用户 Skills
├── .workspace/         # 开发态项目 / 会话工作区
├── PACKAGING.md        # macOS 构建与验证说明
└── dev.sh              # 一键开发启动
```

## 快速开始

### 依赖

- Go **1.26+**
- Node.js **20+**、`pnpm`
- macOS / Linux（Electron 部分只在 macOS 上验证过）

### 首次跑

```bash
cp .env.example .env
```

至少填一把 `DEEPSEEK_API_KEY`。想启用 RAG 就填 `EMBEDDING_API_KEY`；想让 agent 联网就填 `TAVILY_API_KEY` 或 `BOCHA_API_KEY`（两个都填能走 `region=both` 并发合并）。

```bash
./dev.sh
```

脚本会依次拉起：
- **backend** — `go run ./cmd/api`，监听 `127.0.0.1:9001`
- **frontend** — `pnpm dev` in `web/`，固定 `:5173`
- **electron** — `pnpm start` in `electron/`，加载 `:5173`

使用弹出的 Electron 窗口。`--no-electron` 时也可打开
[http://localhost:5173](http://localhost:5173)，但原生文件选择和 `local-file://` 预览只在
Electron 中可用。

### 常用参数

```bash
./dev.sh --no-electron     # 只跑 backend + 浏览器前端
./dev.sh --no-frontend     # 只跑 backend (electron 会一起跳过)
./dev.sh --no-backend      # 前端 + electron，指向已跑的后端
./dev.sh --fresh           # 强制重新 go mod download / pnpm install
```

日志在 `logs/dev/{backend,frontend,electron}.log`。Ctrl-C 一次会清干净所有子进程。

### 分层构建

```bash
# backend
go build -o bin/api ./cmd/api

# frontend
cd web && pnpm build          # 产物在 web/dist/

# electron TypeScript
cd electron && pnpm build
```

### macOS 打包

首版只构建 Apple Silicon（arm64），输出未签名的 `.app` 和 `.dmg`：

```bash
./scripts/package-macos.sh
```

产物在 `release/`。首次启动会要求导入 `.env`；取消导入时，应用会在
`~/Library/Application Support/LingCoWork/.env` 创建模板并退出，填写后重新打开即可。
数据库、工作区、Skills、Memory、MCP 配置、附件和日志也都保存在同一个 Application
Support 目录，不会写进 `.app`。

未签名构建可能被 Gatekeeper 拦截。可在 Finder 中按住 Control 点击应用并选择“打开”；
不要为了测试关闭整个系统的 Gatekeeper。构建结构、验证步骤和首版限制见
[`PACKAGING.md`](PACKAGING.md)。

### 运行数据

- 开发态：仓库下的 `.env`、`data/`、`.workspace/` 和 `.lingcowork/`。
- 打包态：`~/Library/Application Support/LingCoWork/`。Electron 通过
  `LINGCOWORK_HOME` 把数据库、Workspace、Memory、Instructions、用户 Skills、MCP 配置、
  附件和日志统一放到这里。

`.lingcowork/mcp.json` 可能包含 API Key 或 Authorization Header，不应提交到 Git。
OAuth token 和动态注册得到的 client id 存在 SQLite 中，不写回 JSON。

## 环境变量

见 [`.env.example`](.env.example)。核心几组：

| 变量 | 作用 |
| --- | --- |
| `DEEPSEEK_API_KEY` / `LLM_BASE_URL` / `LLM_MODEL` | 主 LLM（DeepSeek OpenAI 兼容） |
| `LLM_ENABLE_THINKING` / `LLM_REASONING_EFFORT` | 是否启用思考 & effort（high/max） |
| `LLM_MULTIMODAL` | 是否将用户附图发送给视觉模型；关闭后使用本地 OCR 回退 |
| `APPROVAL_FAST_*` | 审批 auto 模式的快速分类器（共用 `DEEPSEEK_API_KEY`） |
| `COMPACTION_ENABLED` / `COMPACTION_MODEL` / `COMPACTION_WINDOW_TOKENS` 等 | 跨轮次上下文压缩（共用 `DEEPSEEK_API_KEY`），见下节 |
| `EMBEDDING_API_KEY` / `EMBEDDING_BASE_URL` / `EMBEDDING_MODEL` / `EMBEDDING_DIMENSIONS` | RAG 嵌入模型（默认 DashScope `text-embedding-v3`） |
| `RAG_DOCS_DIR` / `RAG_DB_PATH` / `RAG_CHUNK_SIZE` / `RAG_CHUNK_OVERLAP` | RAG 索引 & 检索参数 |
| `TAVILY_API_KEY` / `BOCHA_API_KEY` | 联网搜索。全空则 `web_search` 工具不注册 |

## RAG 离线索引

把要检索的文档丢进 `docs/rag_docs/`（可用 `RAG_DOCS_DIR` 改路径），然后：

```bash
go run ./cmd/rag-index                        # 全量索引
go run ./cmd/rag-search "query keywords"      # 命令行验证检索
```

服务运行时，agent 会通过 `rag_search` 工具走同一个 sqlite 向量库。

## 上下文压缩

聊得够久，历史迟早撑爆模型的上下文窗口。`internal/compaction` 在**每轮开始前**估一次 token，超过阈值就把已完成的历史折成一段五段式摘要（用户意图 / 任务进展 / 当前计划 / 关键技术上下文 / 交接说明）。

几个要点：

- **只追加，不删改**。压缩产生 `compactions` 表里的一行「截止到 seq N 的内容由这段摘要代表」，原始消息一条不动。装配 LLM 上下文时才做投影——折叠掉的行换成一条合成 user 消息；UI 那边照常渲染全量历史，只在折叠点画一条分隔线。
- **token 估算是混合式**。拿最近一条 assistant 行上记录的真实 usage 做基线（只记主 agent 的，子 agent 有自己独立的上下文），其后的增量才按字符粗估，误差不会一路累积。
- **只在轮次之间跑**。压缩发生在 `ChatService.Start` 里、agent 启动之前；HITL 恢复走 checkpoint，完全不碰这条路径。单轮内部的膨胀（比如一次 `deep_research` 连调几十个工具）不在覆盖范围内。
- **失败静默降级**。摘要超时或报错就跳过，这一轮照常用完整历史跑，压缩绝不阻断对话。

阈值 = `floor(WINDOW_TOKENS × USABLE_RATIO) − RESERVED_OUTPUT − BUFFER`，按 DeepSeek V4 的
1M 窗口默认在 `848000` tokens 时压缩。`RESERVED_OUTPUT` 为单次 32K 输出预留空间，
`BUFFER` 继续兜住 system prompt、工具 schema 和估算误差。全部参数见 [`.env.example`](.env.example)。

每轮回复末尾会显示 `43.2k / 96.8k · 3.4s`——左边是**上下文占用**，不是本轮开销。ReAct 循环里每次模型调用的 usage 都涵盖到那一刻为止的完整上下文，累加它等于把早期历史重复计好几遍；这里取的是最后一次调用的值，也正是压缩估算的锚点，所以看到的数和决定何时折叠的数始终是同一个。压缩关闭时分母消失，只剩裸 token 数。

## 架构备注

- **Workspace 隔离** — 会话可使用默认 Workspace，也可绑定项目 Workspace；相对路径统一在
  当前根目录解析，越界路径会被拒绝或进入审批。
- **流式与阶段追踪** — `internal/stream` 分阶段发 SSE，前端可以拿到 "thinking / tool_call / tool_result / text" 等阶段标签。
- **审批门槛** — 策略按调用的 **effect**（`internal/effect`）决策而非工具名：写入、移动、执行命令、以及读取工作区外的文件会挂起等审批；破坏性操作和无法识别 effect 的调用即使 `full_access` 也拦。会话选择 `auto` 模式时由快速分类器判断能否自动放行；`ask_user` 工具走同一套人工介入通道。
- **Skills 动态加载** — Skill 是方法论提示词和允许工具边界，通过 `load_skill` 按需进入
  上下文；Skills 索引每轮刷新。
- **Memory 分层** — 用户级记忆对所有项目生效，项目级记忆跟随 Workspace；两者均有大小限制
  和乐观锁。

## License

[MIT](LICENSE)
