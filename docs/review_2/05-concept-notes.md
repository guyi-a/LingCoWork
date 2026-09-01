# 概念辨析与疑难解答

> 放那些**容易被问法带偏**、或者自己一时想不通的概念问题。
> 题目本身记在 [00-common-questions.md](00-common-questions.md)，这里写来龙去脉。

---

## 一、什么是 Agent：LLM + Harness

**结论先行**：

> Agent 不是一个更强的 LLM，而是 **LLM + Harness**。
> LLM 负责理解、推理和决定下一步；Harness 是模型外面的运行系统，
> 负责准备上下文、提供执行能力、推动任务循环、接收反馈并约束行为。

单独的 LLM 本质上只是一个「根据上下文预测下一个 token」的模型。
它不会自己读文件、调用接口，也不会天然记住上一轮发生了什么。
真正把模型变成一个能持续完成任务的 Agent，是外面的 Harness。

### 1. Harness 的五个部分

| 部分 | 解决的问题 | 常见实现 |
|---|---|---|
| **上下文架构** | 这一次应该让模型看到什么 | system prompt、对话历史、工作区信息、Skill、Memory、RAG、上下文压缩 |
| **执行能力** | 模型决定做事以后，谁真正执行 | 文件、Shell、浏览器、MCP、HTTP、数据库等工具 |
| **任务编排** | 多步骤任务如何不断向前推进 | ReAct 循环、任务拆分、子 Agent 委派、并行工具、后台任务 |
| **反馈机制** | 执行结果怎样回到模型和用户 | Tool Result 回填上下文、AgentEvent、流式进度、错误反馈、审批后续跑 |
| **安全与护栏** | 模型最多能做什么，何时必须停下来问人 | 权限范围、参数校验、审批、超时、沙箱、资源限制、审计 |

这五部分不是并列堆功能，而是组成一个闭环：

```text
Harness 组装上下文
  → LLM 判断下一步
  → Harness 执行工具
  → 把结果作为 Observation 交还 LLM
  → LLM 再判断下一步
  → 直到任务完成
```

所以 Agent 的关键不是「模型能调用工具」这一点，而是：
**模型的决策和外部环境的执行结果能够反复闭环。**

### 2. 五部分分别怎么理解

**上下文架构**决定模型的认知边界。上下文窗口有限，不是资料塞得越多越好；
Harness 要选择这一轮真正相关的历史、规则、工具和外部知识，过长时还要压缩。

**执行能力**把模型输出的意图变成真实动作。LLM 只会生成
「调用 `read_file`」这样的结构化请求，真正打开文件、捕获错误和返回结果的是 Harness。

**任务编排**负责多步骤过程。简单 Agent 可以只做
「模型判断 → 调工具 → 回填结果」的 ReAct 循环；复杂系统还会加入子 Agent、
并行执行和后台任务。哪些步骤由开发者固定，哪些交给模型临场决定，也是编排的一部分。

**反馈机制**有两条：一条面向模型，把工具结果和错误作为下一次推理的依据；
另一条面向用户，把思考、工具执行、审批和最终回答实时展示出来。
没有反馈，工具调用只是一次性 RPC，构不成 Agent 闭环。

**安全与护栏**不能只写在提示词里。提示词只能引导模型，
真正的边界要由 Harness 确定性执行：越界路径直接拒绝、危险操作进入审批、
命令设置超时，条件允许时再用进程沙箱限制文件和网络权限。

### 3. 放到 LingCoWork 里对照

| Harness 部分 | 项目里的对应实现 |
|---|---|
| 上下文架构 | 对话历史、Workspace / Skill / Memory 中间件、RAG、上下文压缩 |
| 执行能力 | 文件、Shell、搜索、浏览器、MCP、文档生成等工具 |
| 任务编排 | Supervisor + 领域子 Agent、模型驱动的工具循环 |
| 反馈机制 | AgentEvent → chunk → Frame → SSE；工具结果回填；审批 checkpoint 续跑 |
| 安全与护栏 | Effect 推导、多级审批、工作区路径约束、参数与超时限制 |

### 4. 面试时可以直接说

> 我理解 Agent 在工程上可以概括为 LLM 加 Harness。LLM 提供理解、推理和决策能力，
> Harness 则把它变成一个可以真正完成任务的系统，主要包含五部分：
> 第一是上下文架构，决定每次给模型哪些历史、规则和知识；
> 第二是执行能力，通过工具连接文件、浏览器和外部服务；
> 第三是任务编排，用模型—工具循环、子 Agent 或后台任务推进复杂任务；
> 第四是反馈机制，把工具结果重新交给模型，同时把执行过程流式反馈给用户；
> 第五是安全与护栏，通过权限、审批和沙箱限制行为边界。
> 所以 Agent 的核心不是单次生成，而是「决策—执行—反馈」的持续闭环。

---

## 二、流式输出全链路：AgentEvent → chunk → Frame → SSE → 前端渲染

> 关联 [02-streaming.md](02-streaming.md) 第二至四节（连接 / StreamBuffer 那部分）。
> 这一节补的是 02 文档没细讲的两段：**产出侧从 ADK 事件到 SSE 帧的展开过程**，
> 以及**前端拿到内容之后怎么渲染成 markdown**。也是真实面试问到的问题。

### 1. 先厘清一个常被搞混的层级：AgentEvent ≠ SSE frame ≠ token

三者是三个不同粒度的概念，经常被混着问：

| 概念 | 产生者 | 粒度 |
|---|---|---|
| **token** | 模型的 tokenizer，自回归解码 | 模型内部生成单元，我们看不到 |
| **chunk** | DeepSeek 服务端的 SSE flush 策略 | 传输层单元，一个 chunk 可能是 0/1/多个 token 的文本 |
| **AgentEvent** | eino ADK Runner 的迭代器 | agent 编排层事件，一个流式 AgentEvent 内部还套一层 chunk 流 |
| **SSE Frame** | 我们自己的 `stream.Frame` | 一个 chunk 编码成一条，推给前端 |

**token 是模型词表里的最小单位**，由训练时 BPE 统计合并出来的子词片段；
高频词整个是一个 token，罕见词被拆成几段，永远不会有「完全不认识」的输入。
**chunk 是我们能实际观测到的传输单元**，边界由服务端决定，不保证等于一个 token。

### 2. 实际接进生产的是 `ConsumeADKEvents`，不是 eino 的 `callbacks.Handler`

`internal/stream/sse_handler.go` 里的 `NewSSEHandler`（基于 eino 的
`callbacks.Handler`，OnStart/OnEnd 那种钩子）**是没被调用的旧实现**，
真正接进 `main.go`/`service/chat.go` 的是 `adk_handler.go` 的 `ConsumeADKEvents`，
驱动 `*adk.AsyncIterator[*adk.AgentEvent]`。踩过一次坑，记录一下别再讲错。

### 3. AgentEvent 内部还套了一层流

```
Runner.Run() → iter (AsyncIterator[*AgentEvent])
  → 每次 iter.Next() 拿到 1 个 AgentEvent（ev.AgentName / ev.Output.MessageOutput / ev.Action）
    → 如果 mv.IsStreaming：mv.MessageStream 是另一个更细的 StreamReader[*schema.Message]
      → 每次 sr.Recv() 拿到 1 个 chunk
        → chunk.Content / chunk.ReasoningContent 就是这次的增量文本
          → 包成 Frame{Type:"text"/"thinking", Content: chunk.Content}
            → buf.Append(Encode(frame)) 写进 StreamBuffer
```

一个「模型在说话」的宏观 `AgentEvent`，展开成几十上百个 chunk，每个单独编成一条
SSE frame 立即推流——**这才是打字机效果真正的源头**，不是前端做了什么动画。
非流式的事件（工具结果、usage）走的是另一条路径，一次性编一条 frame，不会像文本那样一小段一小段冒出来。

### 4. SSE frame 里到底装了什么

`stream.Frame` 是一个扁平 struct，`Type` 判别字段，常见类型：`text`/`thinking`
（`Content` 是这次的增量）、`tool_call`/`tool_result`（`ID`/`Name`/`ArgsJSON`/`OK`）、
`usage`（token 用量）、`approval_required`/`question_required`（HITL 中断）、`done`/`error`。

### 5. StreamBuffer 的作用：生产者和消费者之间的解耦层

不是「模型吐一点就直接写 HTTP 响应」。`StreamBuffer` 同时做两件事：
`chunks` 切片留底（支持断线重连后补发历史）、`subscribers` 支持多路订阅
（同一个 buffer 可以有多个 channel 在监听）。`Append` 非阻塞广播
（`select-default`，某个订阅者慢就丢给它这一条，不会拖累模型消费循环）。

详细的并发设计（`StreamAll` 快照+订阅必须原子、204 判断等）已经在
[02-streaming.md](02-streaming.md) 写过，这里不重复。

### 6. 前端渲染：不判断是不是 markdown，无条件解析

**没有格式检测这一步**。每一段正文都无条件丢给 `Streamdown`（`web/src/features/chat/MessageBody.tsx`）
去解析——没有 markdown 语法的纯文本解析完还是普通段落，效果等同直接显示，
所以不需要先判断「这是不是 markdown」。

喂给解析器的是**从头累积到当前的完整字符串**（`last.content = last.content + f.content`），
不是孤立的一小段 chunk；每次新 chunk 到达都用当前累积的（依然不完整的）全部内容重新整体解析一遍，
不是等模型说完整段话才开始解析。

### 7. 真正的难点：字符串末尾未闭合的语法

`**加粗`、`` `代码 ``、`[链接](url` 这类**配对语法**，末尾出现开始符号但配对符号还没到达时，
存在真实的歧义。`streamdown` 依赖 `remend` 库解决——**不是让解析器变智能，
是在喂给 remark 解析之前，先对原始字符串做预处理，把末尾疑似未闭合的标记自动补上闭合符号**：

```
remend("This is **incomplete bold")  →  "This is **incomplete bold**"
```

规则：只检测字符串末尾、数开合符号个数（排除转义）、不确定就不补（防御性）、
尊重代码块/数学块上下文。链接类的不完整会替换成占位 URL
（`[text](streamdown:incomplete-link)`）而不是猜测补全，图片类不完整直接整段删除。

**标题（`#`/`##`）不走这套机制**，因为它不是配对语法，是单侧行首前缀标记——
`"##"` 单独出现，CommonMark 就已经认为是合法的（内容为空的）二级标题，
不存在「未闭合」问题。实测验证：

```
"##"        → heading(depth=2, text="")   已经是合法标题
"## 面试"    → heading(depth=2, text="面试")
"#标签"      → paragraph(text="#标签")     没有空格，直接是普通文字
```

唯一真实存在、没被专门修复的小瑕疵：`#` 后面到底会不会跟空格，
只收到孤立的 `#` 那一刻是判断不出来的，可能先渲染成空标题、下一个 chunk
才发现其实是字面文字，出现一次短暂跳变。`remend` 的补全列表里没有标题这一项，
这是个诚实的边界，被问到直接承认。

---

## 三、Electron 渲染进程为什么不能直接读本地磁盘文件

> 关联 `electron/src/main/main.ts`。跟「dev 环境 vs 打包环境」无关——
> 这是渲染进程**安全隔离**的问题，不管 dev 还是打包都一样。

**结论先行**：不是 http 页面跨 scheme 读 `file://` 的问题，是
`nodeIntegration: false` + `contextIsolation: true` 把渲染进程隔成了一个
**没有任何 Node.js API 的纯浏览器环境**，天然不具备读磁盘的能力。

### 1. 和普通浏览器对比：限制的性质完全不同

| | 普通浏览器网页 | Electron 渲染进程 |
|---|---|---|
| 底层运行时 | 只有浏览器引擎，从没绑定过文件系统 API | 绑定了完整 Node.js（Electron 本身就是 Chromium + Node） |
| 能不能读任意本地文件 | **不能，且无法通过任何设置开启**——Web 平台设计上从没实现这条路，否则任意网站都能偷用户文件 | **技术上可以**——只是安全实践上主动关掉 |
| 关闭方式 | 没有"关闭"这一说，本来就没有 | `nodeIntegration: false` 主动不把 Node 全局对象注入页面 |
| 唯一合法入口 | `<input type="file">` 原生选择框 / 拖拽——用户主动选中的那一个文件，生成 `blob:` URL | preload 精确暴露的接口 / 自定义 privileged 协议 |

**普通浏览器是"天生没有"，Electron 是"本来有、主动关掉"**。早期 Electron 默认
`nodeIntegration: true`，直接把 `require`/`fs`/`process` 注入页面 `window`，
这是普通网页永远做不到的事。现在的安全实践是显式关闭，让渲染进程退回到
"跟普通网页一样什么都摸不到"的状态。

### 2. 项目里的具体设置

```182:187:electron/src/main/main.ts
webPreferences: {
  contextIsolation: true,
  nodeIntegration: false,
  preload: preloadPath,
  devTools: true,
},
```

`nodeIntegration: false`——不注入 Node 全局对象。`contextIsolation: true`——
再加一层，preload 脚本的 JS 世界和页面自己的 JS 世界隔开，页面碰不到 preload
拿到的任何东西，只能通过 `contextBridge.exposeInMainWorld` 显式暴露的接口
（这个项目里只有 `pickFiles`/`savePastedImage` 两个）。

### 3. 具体需求 vs 两种解法

需求：渲染进程要用 `<img src="...">` 显示本地图片（粘贴/拖拽的图、文件选择器选中文件的缩略图），
但它自己读不了任意绝对路径的文件字节。

**已有的显式 IPC 模式**（`pick-files`/`save-pasted-image`）：
`ipcRenderer.invoke` → 主进程处理 → 返回结果，一次请求一次往返，适合「拿一次结果」的场景。

**图片展示走的是另一条路——自定义协议**，原因是要复用浏览器原生的 `<img>`
加载/缓存机制，不想每张图都手动发 IPC、拿字节、拼 blob URL：

```40:45:electron/src/main/main.ts
protocol.registerSchemesAsPrivileged([
  { scheme: 'local-file', privileges: { standard: true, secure: true, supportFetchAPI: true, stream: true } },
]);
```

```161:171:electron/src/main/main.ts
function registerLocalFileProtocol(): void {
  protocol.handle('local-file', (req) => {
    // URL 形状：local-file://l/Users/guyi/Downloads/x.png
    //   host === 'l'（占位，自定义协议 URL 按标准也得有 authority 段）
    //   pathname === '/Users/guyi/Downloads/x.png'
    const url = new URL(req.url);
    const abs = decodeURIComponent(url.pathname);
    return net.fetch(pathToFileURL(abs).toString());
  });
}
```

渲染进程写 `<img src="local-file://l/绝对路径">`，Chromium 正常的资源加载流程去请求这个
URL；因为 `local-file` 是注册过的特权协议（必须在 `app.whenReady` 之前注册），
Electron 把请求**跨进程转发到主进程**（走的是 Electron 底层的 IPC 通道，不是开发者
手写的 `ipcMain.handle`，而是框架内置在「协议拦截」机制里的），交给 `protocol.handle`
的回调处理——回调跑在主进程，有完整文件系统权限，读完用 `net.fetch(pathToFileURL(abs))`
转成响应流回给渲染进程，渲染进程感觉就像请求了一个普通网络资源。

### 4. 一句话答法

> 普通浏览器网页从没有文件系统访问能力，这是 Web 平台设计上就没实现的，
> 不存在"关闭"这回事，唯一入口是用户主动选择文件的原生交互。Electron 渲染进程
> 因为绑定了 Node.js，技术上本可以拥有完整文件系统权限，但安全实践是用
> `nodeIntegration: false` + `contextIsolation: true` 主动关掉，让渲染进程表现得
> 跟普通网页一样。我们需要按绝对路径展示本地图片时，没有走显式 IPC 一次次搬字节，
> 而是注册了一个自定义特权协议 `local-file`，让 `<img>` 的原生加载机制透明地把请求
> 转发到有文件系统权限的主进程处理，这一层跟 dev/打包环境无关，是同一套代码。

---

## 四、浏览器缓存：304 是怎么判断出来的

> 通用 HTTP 知识，跟本项目的关联点是 `writeSSE` 里显式禁用缓存的那一行
> （见 [02-streaming.md](02-streaming.md)）——理解了这一节，
> 才知道"为什么要显式禁用"是在对抗什么默认行为。

**结论先行**：304 出现的前提是**浏览器已经发了一个请求出去**，这跟"强缓存命中、
根本不发请求"是两种不同程度的省流量，容易被问法带着混在一起。

### 1. 两层缓存策略，先分清

| | 强缓存 | 协商缓存 |
|---|---|---|
| 判断依据 | `Cache-Control: max-age=N` / `Expires` | `ETag` / `Last-Modified` |
| 命中时 | **浏览器不发请求**，直接用本地缓存 | **发了请求**，服务端判断后返回 304（无 body）或 200（带新内容） |

第一次响应会同时带这两组头：`Cache-Control` 告诉浏览器「这段时间内不用问我」；
`ETag`/`Last-Modified` 是给浏览器的一个「凭证」，等强缓存过期后带着这个凭证来验证。
**304 只发生在强缓存过期之后的那次验证请求里。**

### 2. 服务端判断资源变没变，两种验证器（validator）

**`Last-Modified` / `If-Modified-Since`（基于时间）**：服务端带上资源最后修改时间，
浏览器下次请求原样带回来，服务端拿这个时间跟资源**当前**的 mtime 比——没变就 304。
局限：精度只到秒，一秒内改两次会漏判；内容重新生成但字节没变，mtime 照样会变，误判成「变了」。

**`ETag` / `If-None-Match`（基于内容指纹）**：服务端算一个当前内容的指纹（hash /
版本号），浏览器带回来跟**重新算一遍**的当前指纹比——一样就 304。更准，因为只要内容
字节相同就一定算出同一个指纹，不受时钟精度和多机时钟不同步影响；代价是要花计算量
（大文件算 hash 有成本，弱校验 `W/"..."` 就是为了省这个）。

两者可以同时带，`ETag` 通常优先，`Last-Modified` 当退路（比如某些代理不透传 ETag 时兜底）。

### 3. 完整链路

```
第一次:            GET /app.js
                   ← 200, body, Cache-Control: max-age=3600, ETag: "v3-abc"

强缓存期内再访问:    浏览器直接用本地缓存，不发请求

3600 秒后再访问:    GET /app.js
                   If-None-Match: "v3-abc"
                   ← 服务端重新算一遍当前内容的 ETag
                     没变 → 304 Not Modified（无 body）
                     变了 → 200, 新 body, ETag: "v4-def"
```

### 4. 本项目里的反向应用：显式禁用缓存

```103:106:internal/handler/chat.go
c.Header("Content-Type", "text/event-stream")
c.Header("Cache-Control", "no-cache")
c.Header("Connection", "keep-alive")
c.Header("X-Accel-Buffering", "no")
```

SSE 流每次都是全新的实时内容，不该被浏览器当成「可复用的静态资源」，所以显式用
`Cache-Control: no-cache` 关掉缓存协商这条路——**这是同一套机制的另一个极端用法**：
知道浏览器默认会怎么缓存、才知道要显式声明「别缓存」来对抗这个默认行为。

---

## 五、公共 skill 安装怎么保证安全性

> 对应 [00 §21](00-common-questions.md)，来源：百度二面 · 实习项目 klingwork-app 方向二。
> 详细梳理见 [docs/review/03-skillhub.md](../review/03-skillhub.md) 第六节。

**结论先行**：公共 skill 来自完全开放生态，**没有人替用户 vet 包**。
安全模型是「**安装前让用户知情并确认 + 安装链路多层卫生检查 + 装完以后脚本执行仍走原有审批**」，
不是「装进来就安全了」。

### 1. 核心认知：装 skill = 接受陌生人给模型写指令

公共 skill 是 **agent 调 `skill_install` 发起的**（不是用户在 UI 里点的），
所以和内网 SkillHub「用户点击 = 授权」不同——**模型替用户做决策，必须单独设防**。

安装后 skill 会进入 `~/.klingwork/skills/`，下一轮 run 扫描发现，
`load_skill` 会把正文注入模型上下文——本质是 **prompt 注入面的扩大**。

### 2. 流程四步

```
search（只读）→ preview（只读，必做）→ install（写盘，强制审批）→ 下轮 run 生效
```

- **preview 只拉 SKILL.md**：不必整包下载；带 GitHub 源链接，模型/用户可核对
- **install 工具描述明确要求**：先 preview、向用户说明来源；陌生 owner 要直说没人 vet
- **装完不能立刻用**：discovery 是每轮 run 开始时扫的，当前 turn 装完下轮才可见

### 3. 真正防线：强制审批（任何模式）

```36:44:/Users/guyi/klingwork-app/packages/agent-core/src/approval/policy.ts
  if (effect.kind === "skill-admin") {
    return effect.action === "install" || effect.action === "uninstall";
  }
```

`skill_install` / `skill_uninstall` 在 **auto / semi-auto / manual 全部要用户确认**。
理由和 `mcp_add_server` 同级：都是 **能力跃迁**——下一轮模型会多一块陌生人写的指令。

search / preview 是只读 fetch，走常规模式规则（auto 下不拦）。

### 4. 安装器卫生措施（比内网市场更严）

| 措施 | 作用 |
|------|------|
| **体积上限** | 单文件 2MB、总量 8MB；`content-length` 不可信，按实际字节累计 |
| **路径校验** | 拒绝 `..`、反斜杠、控制字符、过长路径 → 防 zip-slip 类逃逸 |
| **扩展名黑名单** | 拦 `.exe/.so/.dll/.dmg` 等——**卫生信号**，不是主防线（见下） |
| **大小写碰撞检查** | macOS 不区分大小写，`SKILL.md` vs `skill.md` 会互盖 → 整包拒绝 |
| **metadata 校验** | 与 discovery 同一套规则：`name` 必须等于目录名、description 必填 |
| **staging + 原子 rename** | 要么完整出现要么没有，不会读到半个技能 |
| **撞名拒绝** | 目录已存在 / 与内置 skill 同名 → 拒绝（不能覆盖内置） |
| **marker 隔离** | `.public-skill.json` 标记来源；卸载只删有 marker 的目录 |
| **contentHash** | sha256 记录内容指纹——**检测本地后续被改**，不是签名验真 |

**扩展名黑名单的诚实说法**（代码注释原话）：

> 这些东西不会自己运行，拦下来的安全收益接近零——真正的风险是 SKILL.md 指挥 Agent 去跑脚本，
> 那只能靠安装前的人工审批兜住。

黑名单的真实价值：**包里有二进制 = 强烈质量信号 + 不想当恶意软件投递落地点**。
能分清「真防线 vs 卫生措施」比一律说「为了安全」清醒。

### 5. 信任链：内容从哪来

- 文件清单走 **jsDelivr**（绕 GitHub API 60次/h 共享限流）
- **文件内容仍从 `raw.githubusercontent.com` 取**——清单错了最多装不上，内容不能换第三方 CDN

### 6. 装完以后：脚本不是免审批

`skill_install` 工具描述写死：

> Installing is not permission to run anything: scripts shipped inside a skill still go through
> the normal command approval when you run them.

skill 里的 `.sh/.py` 躺在磁盘上不会自跑；模型若按 SKILL.md 去执行，仍走 `run_command` + 审批链。

### 7. 和用户装 skill 不能 self-pin 的关系

discovery 里 `source === "user"` 的技能 **不能** 靠 frontmatter `metadata.always` 自我 pin 进系统提示词。
公共 skill 装完是 user source，只能被 `load_skill` 按需加载，不能偷偷常驻索引。

### 8. 诚实边界（面试必说）

| 项 | 状态 |
|---|---|
| 包签名 / 官方 vet | **没有**，skills.sh 开放生态无人背书 |
| 静态安全审计 | **公共链路未接入**；内网有 kwai-skill-vetter，规则非本人编写 |
| 技能沙箱 | **没有**，脚本走普通命令审批 |
| 自动更新 | **没有**，装完就固定，除非用户 uninstall + reinstall |

### 9. 30 秒口头版

> 公共 skill 的安全核心是：模型发起安装，所以 install/uninstall 任何模式都强制用户审批；
> 安装前要求 preview 并告知来源。安装器做体积、路径、扩展名、metadata、撞名、staging 原子落盘等卫生检查，
> 但不能证明包无害——装 skill 等于让陌生人写模型指令。装完以后脚本执行仍走原有命令审批；
> 用户装的 skill 也不能自我 pin 进系统提示词。没有签名和官方 vet，hash 只是记录不是验真。

---

## 六、多标签页视频互斥播放：播一个、其他暂停

> 对应 [00 §22](00-common-questions.md)，来源：百度二面 · 前端。  
> 三件套总览见 [06-frontend-basics.md](06-frontend-basics.md)。

**结论先行**：先分清是 **同一页多个 `<video>`** 还是 **多个浏览器标签页**。
前者页内事件总线即可；后者需要 **跨标签通信**——首选 `BroadcastChannel`，降级 `localStorage` + `storage` 事件。

### 1. 同一页面内多个播放器

维护一个 **MediaManager**（模块级单例或 React context）：

```typescript
const players = new Set<HTMLVideoElement>();

export function registerVideo(el: HTMLVideoElement) {
  players.add(el);
  el.addEventListener("play", () => {
    for (const other of players) {
      if (other !== el && !other.paused) other.pause();
    }
    // 若还要通知其他标签页，在这里 broadcast（见下）
    broadcastPlaying(el.dataset.videoId ?? "");
  });
  return () => players.delete(el);
}
```

- 监听 **`play` 事件**（不是 `click`）——用户拖动进度条恢复播放也会触发
- 当前正在播的 element 跳过，其余 `pause()`
- 组件 unmount 时从 Set 移除，防泄漏

### 2. 多个浏览器标签页（面试官真正想问的）

标签页之间 **不共享 JS 内存**，必须走浏览器提供的跨上下文通道。

#### 方案 A：`BroadcastChannel`（推荐，现代浏览器）

```typescript
const CHANNEL = "app:video-playback";
const tabId = crypto.randomUUID();
const channel = new BroadcastChannel(CHANNEL);

channel.onmessage = (event) => {
  const { tabId: from, videoId } = event.data as {
    tabId: string;
    videoId: string;
  };
  if (from === tabId) return; // 忽略自己发的
  document.querySelectorAll("video").forEach((v) => v.pause());
};

function broadcastPlaying(videoId: string) {
  channel.postMessage({ tabId, videoId });
}

// 本 tab 某个 video 开始 play 时：
video.addEventListener("play", () => broadcastPlaying(video.id));
```

特点：
- **同源**多个 tab/window/iframe 都能收到
- API 干净，比 localStorage  hack 更语义化
- Safari 15.4+、Chrome/Firefox 均支持；极老环境需降级

#### 方案 B：`localStorage` + `storage` 事件（兼容降级）

```typescript
const KEY = "active-video";

localStorage.setItem(
  KEY,
  JSON.stringify({ tabId, videoId, ts: Date.now() }),
);

window.addEventListener("storage", (e) => {
  if (e.key !== KEY || !e.newValue) return;
  const { tabId: from } = JSON.parse(e.newValue);
  if (from === tabId) return;
  document.querySelectorAll("video").forEach((v) => v.pause());
});
```

注意：`storage` 事件 **只在其他标签页触发**，写 localStorage 的那个 tab 自己收不到——正好符合「发消息的不暂停自己」。

### 3. 推荐组合

```
页内 play → 先 pause 本页其他 video → BroadcastChannel 通知其他 tab → 其他 tab 全部 pause
```

页内 + 跨 tab 两层都做，体验才完整。

### 4. 常搭配的补充（不是主答案，加分项）

| 机制 | 作用 |
|------|------|
| **Page Visibility API** | 标签页切到后台时自动 pause（`document.hidden`）——省资源，和互斥播放互补 |
| **`navigator.mediaSession`** | 系统媒体键/锁屏控件；mobile 上多个 WebView 场景有用 |
| **Autoplay policy** | 带声音的 autoplay 常被拦；互斥逻辑应绑在 user gesture 后的 `play()` 上 |

### 5. 边界情况

- **Race**：两个 tab 几乎同时 play → 「后发的赢」即可，一般可接受；要严格可用 `ts` 比大小
- **Tab 关闭**：无需专门清理，其他 tab 只是少一个竞争者
- **不同源**：BroadcastChannel / localStorage 都仅限 **同源**，跨域 iframe 要 `postMessage` + 约定 origin
- **同一页 + 多 tab 同时开**：页内 Manager 管页内，Channel 管跨 tab，两层叠加

### 6. 30 秒口头版

> 同一页多个 video 用 Set 注册所有播放器，某个触发 play 时 pause 其余。多个标签页因为 JS 不共享内存，用 BroadcastChannel 广播「我在播」——其他 tab 收到后 pause 自己页内所有 video；老浏览器降级 localStorage 的 storage 事件。页内和跨 tab 两层配合。另外 Visibility API 可以在 tab 进后台时自动 pause，和互斥播放互补。

---

## 七、设计稿落地：Figma → AI 写码 → 仍不一致怎么收口

> 对应 [00 §23](00-common-questions.md)，来源：百度二面。

**结论先行**：Figma MCP **降低输入误差**，不能保证 **输出像素级一致**。
面试官追问的核心是：**你有没有「验收标准 + 分层修正 + 知道何时停」的工程流程**，不是有没有接上 MCP。

### 1. 面试官在探什么

| 你当时的答法 | 面试官听到的 | 他真正想问的 |
|---|---|---|
| 接 Figma MCP，拿标准样式 | 你会用工具减误差 | MCP 之后还是不像，**谁负责收口、怎么收口、何时算 done** |

MCP 解决的是「模型瞎猜颜色/间距」；解决不了：

- 设计稿 **Auto Layout → CSS flex** 的语义损失
- 字体渲染、行高、抗锯齿等 **浏览器 vs Figma 差异**
- 响应式、滚动、动态内容（聊天流）**稿里没画的状态**
- AI **结构搭错**（层级、组件拆分不对），改 class 修不好

### 2. 推荐工作流（四层）

```
设计稿（Figma）
    ↓
① 结构化输入（Figma MCP / Dev Mode 标注：色板、字号、间距 token、组件 spec）
    ↓
② AI 首稿（React + Tailwind，按 token 写，别散写 magic number）
    ↓
③ 视觉 QA（浏览器实跑 + 与设计并排 / 截图对比 / DevTools 量间距）
    ↓
④ 分层修正（小改手调，大改带 diff 再 prompt AI）→ 设计走查 sign-off
```

### 3. 「自己调」还是「继续让 AI 调」——决策表

| 情况 | 做法 | 原因 |
|------|------|------|
| 1~2 处间距/颜色不对 | **自己改 Tailwind class** | 改 `gap-3`→`gap-4` 比 re-prompt 快 |
| 对齐、圆角、阴影微调 | **自己调** | 局部、确定性高 |
| 组件结构错（该 flex 写成 block、缺一层 wrapper） | **带具体 diff 再让 AI 改** | 牵涉 DOM 结构，手改易漏 |
| 同一错误在多个文件重复 | **让 AI 批量改 + 抽组件** | 人修五遍不如改一处 pattern |
| 稿与实现 **语义不一致**（设计没画空态/loading） | **先问设计/产品** | 不是调 CSS 能解决的 |

**经验法则**：

- **微调（< 5 行 CSS）→ 人**
- **结构/模式问题 → AI + 明确约束**
- **需求模糊 → 人（沟通）**

### 4. Figma MCP 之后仍不一致——标准答法

> MCP 把 Figma 的 **design token**（色值、字号、spacing、圆角）结构化喂给模型，减少「猜」；
> 但 **还原度不是 100%**，所以我会设 **验收标准**，而不是追求无限像素对齐：
>
> 1. **Token 级一致**：颜色、字号、间距阶梯跟设计系统一致（Tailwind config / CSS 变量对齐 Figma Variables）
> 2. **关键屏一致**：主流程页面（列表、详情、表单）布局和设计稿一致
> 3. **已知差异可接受**：字体渲染、1px 抗锯齿、动态内容高度——走查时和设计说明
>
> 收口流程：浏览器实跑 → 并排对比或截图 → 列 **差异清单**（间距 12→16、标题字重 medium→semibold）→
> 小差异手改 → 大差异把 **清单 + 当前组件路径** 再喂 AI → **设计同学视觉走查** sign-off。
>
> 如果多轮仍差一点：不会无限 re-prompt，**最后 5% 人工 polish 是正常成本**；
> 长期会把高频偏差沉淀成 **组件库 / Tailwind preset**，下次 AI 直接复用。

### 5. 加分：预防比反复修便宜

- **Design token 进代码**：Figma Variables ↔ Tailwind theme，AI 只许用 `text-sm`、`gap-4`，不许 `#3B82F6`
- **组件化**：Button、Card、Input 先对齐设计系统，页面级 AI 只拼组件
- **视觉回归**（团队有的话）：Playwright 截图 diff；没有就设计走查
- **动态页说明**：聊天/流式 UI 设计稿常是静态帧，要和对齐 **状态**（loading、空、错误）

### 6. 和本项目 PPT skill「视觉 QA」的类比（可选提）

项目里 pptx skill 强调 **生成后必须 OCR/视觉审查、修一轮就停、纯视觉问题交付用户确认**——
同一思路：**工具有帮助，但要有验收清单和 stop condition**，不能假设 AI 一次到位。

### 7. 30 秒口头版（比只答 Figma MCP 完整）

> 我会用 Figma MCP 把 token 和组件 spec 结构化给 AI，减少猜样式；但 MCP 不能保证像素级一致。
> 流程是 AI 出首稿 → 浏览器实跑和设计稿并排走查 → 列差异清单。
> 小改比如间距颜色我自己改 Tailwind；结构错了带 diff 再让 AI 改。
> 验收看 token 和关键屏，不无限 re-prompt，最后几个点人工 polish 正常。
> 长期把 token 和组件沉淀进设计系统，偏差会越来越小。

---

## 八、测试流程：边写边测 + 分层怎么讲

> 对应 [00 §24](00-common-questions.md)，来源：百度二面。

**结论先行**：你答的「单元 → 冒烟 → 联调」**方向对**，但面试里要补三件事：
**① 不是严格串行，是测试金字塔；② 每层测什么、用什么工具；③ Agent 项目特有的 mock/集成点**。

### 1. 你当时答法的问题与改进

| 你答的 | 问题 | 改进 |
|--------|------|------|
| 边写边测 | 太笼统 | 说 **TDD 或「改逻辑先补/改测试」**，并举例 |
| 单元 → 冒烟 → 联调 | 像流水线，缺层次定义 | 改成 **金字塔：多单元、少集成、更少 E2E/手工** |
| 没提 Agent 特殊性 | 面试官可能觉得通用背稿 | 补 **mock LLM / 假 MCP / 审批事件投影** |

### 2. 推荐分层（结合两个项目）

```
        ┌─────────────┐
        │ 手工冒烟/E2E │  少：主流程走一遍（启动→对话→调工具→审批）
        └──────┬──────┘
       ┌───────┴───────┐
       │ 集成/联调      │  中：handler↔service、IPC、SSE 流、MCP pool
       └───────┬───────┘
  ┌───────────┴───────────┐
  │ 单元测试（最多）        │  纯函数、policy、parser、installer 安全逻辑
  └─────────────────────────┘
```

### 3. 各层做什么（可举真实文件）

#### 单元测试（开发时边写边补）

**测什么**：无外部依赖、输入输出确定的逻辑。

| 项目 | 例子 |
|------|------|
| **klingwork-app** | `approval/policy.test.ts` 审批策略；`mcp/client-pool.test.ts` 连接池 refCount/manifest；`public-skills/installer.test.ts` zip-slip/体积上限；`skill-install` 备份互换 |
| **LingCoWork** | `approval/destructive_test.go` destructive 墙；`approval/policy_test.go`；`compaction/compaction_test.go`；`skillhub/skillhub_test.go` |

**习惯**：改 policy、installer、parser 这类 **安全/规则代码** 时，**先写或同步改测试**，回归成本低。

#### 集成测试（模块边界，可不启真实 LLM）

**测什么**：多个模块拼在一起，外部依赖 **mock/fake**。

| 项目 | 例子 |
|------|------|
| **klingwork-app** | `agent/run.test.ts`、`service.test.ts` mock 模型；`app-skill-hub-ipc.test.ts` IPC 安装全流程；`app-runtime.test.ts` 公共 skill 安装 |
| **LingCoWork** | `handler/mcp_test.go`、`service/chat_history_test.go`；`test/adk_*_test.go` 工具事件（可能打真实 API，偏集成） |

#### 冒烟测试（smoke）

说清楚 **你指的冒烟是什么**——面试里这个词歧义大：

- **自动化 smoke**：`pnpm test` / `go test ./...` 全绿 + 关键 package 必跑
- **手工 smoke**：发版前 **5~10 分钟主路径**：App 能启、能对话、能装 skill、审批弹窗正常、流式不断

Agent 项目 smoke 重点：**流式 SSE 不断流、审批续跑、MCP 懒连接**——这些单元测不好覆盖。

#### 前后端联调

**测什么**：真实 HTTP/SSE/IPC，真实（或 staging）模型可选。

| 场景 | 做法 |
|------|------|
| LingCoWork `web` ↔ Go API | 浏览器 + `dev.sh`，看 Network 里 SSE event、审批接口 |
| klingwork Electron | renderer ↔ main IPC，`app-agent-ipc.test.ts` 自动化一部分；复杂路径手工点 |

联调 **放在单元/集成绿了之后**，否则难定位是协议问题还是逻辑 bug。

### 4. 「边写边测」怎么说才像工程习惯

不要只 say 边写边测，选一种讲清楚：

> **规则/安全/解析类代码**：倾向同步写单测（如审批 policy、skill 安装器路径校验）。
> **UI/交互**：先手工 smoke 主路径，稳定后再补集成测。
> **Agent 行为**：单测 mock LLM 输出固定 tool call，断言工具链路和事件投影；真实模型效果单独做 **人工 eval**，不塞进 CI。

### 5. Agent 项目额外一层（加分）

| 测什么 | 怎么测 |
|--------|--------|
| 工具审批 | effect 推导 + `NeedsApproval` / `MustAsk` 表驱动 |
| 流式事件 | projector 单测：chunk → AgentEvent 序列 |
| MCP 连接池 | fake client，测 borrow/release/idle/manifest 生命周期 |
| LLM 质量 | **不自动化进 CI**（ flaky + 贵），用固定 prompt 集人工回归 |

### 6. 30 秒口头版（升级版）

> 我习惯边写边测，但是分层的。底层最多是单元测试：审批 policy、安装器安全逻辑、连接池这类纯规则代码，改就同步改 test——klingwork 用 vitest，LingCoWork 用 go test。
> 上一层是集成：mock 模型或 fake MCP，跑 agent run、IPC、SSE 投影，不依赖真 LLM。
> 全绿后再做冒烟：CI 跑全量 test，发版前手工过主路径——对话、流式、审批续跑、装 skill。
> 最后才是前后端联调，浏览器或 Electron 对着真实 API 验协议和 UI。
> Agent 的模型效果本身不做硬断言，单独人工 eval，和确定性逻辑分开。

### 7. 如果被追问「测试覆盖率多少」

诚实答：**没有追求数字 KPI**；**安全边界、审批、安装器、连接池** 等高后果路径优先有测试；UI 和 LLM 效果靠 smoke + 人工。比编一个覆盖率百分比安全。

---

## 九、多人协作不乱 + DeepWiki 类文档的利弊

> 对应 [00 §25](00-common-questions.md)，来源：百度二面。

**结论先行**：多人 + 人人用 Agent 协作，要靠 **「人读的规范 + 机读的约束 + 自动化门禁」** 三层；
DeepWiki 是 **降低读代码成本** 的外脑，不能替代 **写进 repo 的规范** 和 **CI/review**。

### 1. 你答的 AGENTS.md —— 对，但要展开成体系

klingwork-app 的 `AGENTS.md` 就是典型 **Agent 可读的项目宪法**：

- Package 职责与依赖方向（`apps/desktop → app-runtime → agent-core → contracts`）
- 不可破坏的不变量（`AgentEvent` 是事实源、approval 按 effect 不按 tool 名）
- 新能力落位 checklist（contracts → agent-core → database → …）
- 验证命令（`pnpm typecheck`、`pnpm test`）

**作用**：约束 **Agent 输出**——少生成放错层、重复造轮子、违反事件协议的代码。

但面试里只答 AGENTS.md 会显得单薄，应补 **人 + 机 + 流程**：

| 层 | 手段 | 防什么 |
|----|------|--------|
| **机读约束** | `AGENTS.md`、`.cursor/rules/`、README、架构 doc | Agent 乱改、放错目录 |
| **代码结构** | monorepo 分包、单向依赖、contracts/schema 共享 | 接口漂移、隐式耦合 |
| **流程** | PR review、分支策略、ownership | 人 merge 乱代码 |
| **自动化** | typecheck、lint、test、CI | 回归、类型错误 |
| **确定性边界** | Zod schema / Go struct 作 IPC 与 API 契约 | 前后端各写各的 |

LingCoWork 例：`.cursor/rules/project-context.mdc` 持久注入项目背景；`mcp.example.json` 里配 DeepWiki MCP。

**30 秒版（多人协作）**：

> 多人协作我会分层约束：代码结构上 monorepo 分包、依赖单向、contracts 定接口；
> 流程上 PR review；自动化上 CI 跑 test/typecheck。
> 现在大家都有 Agent，额外加 AGENTS.md 和 Cursor rules，把架构不变量和落位规则写进去，
> 让 Agent 生成代码前先对齐项目宪法，减少「每人 prompt 一套、改法打架」。

### 2. DeepWiki 是什么（面试官说的 deepviki）

**DeepWiki**（Cognition/Devin 出品，`deepwiki.com` / `mcp.deepwiki.com`）：

- 对 **公开 GitHub 仓库** 自动生成 **AI /wiki 式文档**，可问答
- 提供 **MCP 服务**（LingCoWork 的 `mcp.example.json` 里有示例）：
  - `read_wiki_structure` — 文档目录
  - `read_wiki_contents` — 读 wiki 正文
  - `ask_question` — 对仓库提问

本质：**把「读代码理解项目」外包给索引好的 AI 文档**，Agent 通过 MCP 查，而不是每次全仓 grep。

### 3. DeepWiki / 自动产文档的 **好处**

| 好处 | 说明 |
|------|------|
| **降低 onboarding 成本** | 新人/新 Agent 先读 wiki 再问代码，比盲 grep 快 |
| **统一「项目叙事」** | 架构、模块关系有一份 generated overview，减少每人理解不一致 |
| **Agent 上下文更准** | MCP 拉结构化文档进 context，少 hallucinate 目录结构 |
| **公开库即插即用** | 开源依赖可先 DeepWiki 理解再集成（Karpathy 等提过的用法） |
| **文档随代码索引更新** | 比没人维护的 README 更接近「有东西」——**前提是索引刷新及时** |

### 4. DeepWiki / 自动产文档的 **坏处**（面试重点）

| 坏处 | 说明 |
|------|------|
| **不等于 ground truth** | AI 生成的 wiki **可能错、可能旧**；代码才是最终真相 |
| **私有/内网 repo 受限** | 公开免费；私有要 Devin 账号，内网项目未必能索引 |
| **和本仓库 drift** | 本地分支未 push、未 re-index 时，wiki 描述 **main 旧状态** |
| **替代不了 AGENTS.md** | DeepWiki 偏 **「是什么」**；AGENTS.md 偏 **「该怎么改、不能做什么」** |
| **过度依赖 → 思考外包** | 团队少读代码、review 变浅；出 bug 不知道真实现 |
| **安全/合规** | 把私有代码送外部索引有泄露风险；公开 repo 则无所谓 |
| **幻觉传导** | Agent 信错 wiki → 生成错代码；需要 **关键路径仍对代码 + 测试** |

**诚实收口句**：

> DeepWiki 我当作 **检索增强的外脑**，不当作规范源；**AGENTS.md + review + CI** 才是硬约束。

### 5. AGENTS.md vs DeepWiki：分工

```
DeepWiki     →  「这个项目/modules 大致怎么工作的？」（读）
AGENTS.md    →  「改的时候必须遵守什么？放哪？」（写）
CI / test    →  「改完有没有破坏不变量？」（验）
PR review    →  「人是否认可这次改法？」（审）
```

两者 **互补**：DeepWiki 减认知负载；AGENTS.md 减 **错误改法**；都不能省 review。

### 6. 若追问「自动文档会不会和代码不一致怎么办」

> 把 wiki 当 **参考层**，关键决策以 **代码 + 测试 + 人写的不变量文档** 为准；
> 发现 wiki 和实现不符，以代码为准并 optionally 更新 AGENTS.md / 架构 note；
> 对 Agent 会 prompt：**「DeepWiki/文档仅供参考，冲突时 read 源码确认」**。

### 7. 30 秒口头版（整题）

> 多人协作靠分包和 contracts 定边界、CI 和 review 守门；Agent 时代额外用 AGENTS.md 和 Cursor rules 约束生成代码的落位和不变量。
> DeepWiki 这类工具是给 repo 自动生成可读文档、通过 MCP 让 Agent 查架构，好处是 onboarding 快、上下文统一；
> 坏处是可能过时或幻觉、不能替代规范，私有库还有索引和合规问题。
> 我的用法是 DeepWiki 负责「读懂」，AGENTS.md 负责「改对」，测试和 review 负责「验对」。

---

## 十、Coding Agent 里 grep/glob vs RAG：为什么代码多用前者

> 对应 [00 §26](00-common-questions.md)。  
> 关联本项目的题库检索（向量 + BM25 + RRF）：`internal/rag/`；`rag_search` 工具边界见 `internal/agent/tools/rag_search.go`。

**结论先行**：**源码是结构化、精确、高频变更的 ground truth**——找定义、找引用、改 bug 需要 **exact match + 可验证路径**，grep/glob/read 更合适。RAG 适合 **非代码语料**（文档、题库、wiki）或 **语义模糊的自然语言检索**，不适合替代「在仓库里定位第 42 行那个函数」。

### 1. 两类任务，两种工具

| 任务 | 典型 query | 更合适的手段 |
|------|------------|--------------|
| **在代码库里定位** | `AbortController` 在哪定义？谁调用了 `setTurns`？ | **grep / glob → read_file** |
| **在文档库里理解** | 「缓存穿透怎么答？」「Go GMP 面试题」 | **RAG（向量 + BM25）** |
| **改代码** | 修这个 bug、加这个 API | **glob 缩小范围 → read → edit** |
| **读架构叙事** | 这个项目模块怎么划分？ | **DeepWiki / README / AGENTS.md**（文档层，仍可能需 grep 验真） |

Coding Agent（Cursor、Claude Code、LingCoWork）的主路径是 **filesystem as source of truth**，不是 **vector index as source of truth**。

### 2. 为什么代码场景更偏 grep/glob

**① 代码检索本质是「精确符号匹配」**

- 函数名、类名、import 路径、报错字符串、配置 key —— 都是 **字面量**
- `grep "useChatStream"` 命中就是命中；向量检索可能召回「也在讲流式聊天」的无关文件
- BM25 对 `GMP`、`MVCC` 这类术语还行，但对 **`handleApprovalRequired`** 这种长 camelCase，grep 仍然更稳

**② 需要行号 + 完整上下文才能改**

- Agent 工作流：`grep` 得文件+行号 → `read_file` 看前后文 → `edit_file` 精确替换
- RAG chunk 是 **切出来的片段**（定长或按标题切），容易：
  - 切断函数 / 类 / 代码块
  - 丢掉 import、类型定义、调用链
  - 返回「相关段落」但不是可编辑的完整单元

**③ 代码变太快，索引容易 stale**

- 每 commit、每 Agent 改文件，RAG 索引若不同步 → 召回 **旧实现**
- grep 直接扫 **当前工作区**，无索引滞后（代价是每次现扫，但 ripgrep 对百万行仍够快）

**④ 可验证、可迭代**

- grep 结果 deterministic：同样 pattern 同样命中
- Agent 可以多轮：**glob 缩目录 → grep 找 symbol → read 跟引用 → 再 grep**
- RAG 是概率召回，topK 漏了关键文件就要换 query 重搜，且难以解释「为什么没召到」

**⑤ 成本和工程复杂度**

- grep/glob：本地、无 embedding API、无切分/入库 pipeline
- 全仓库 RAG：切分策略、增量索引、embedding 费用、向量库运维；代码 repo 越大越重
- 对 **coding** 主路径，ROI 往往不如「让模型自己 grep」

### 3. RAG 仍然有价值的场景（不是不用，是 **分工**）

| 场景 | 为什么 RAG 合适 |
|------|----------------|
| **面试题库 / 内部知识库** | 自然语言问句、概念改写；语料是 markdown Q&A，不是 AST |
| **文档 / wiki / 设计说明** | 没有稳定 symbol；用户问「架构怎么设计的」 |
| **超大 monorepo 冷启动** | 先 RAG/DeepWiki 缩小 **模块级** 范围，再 grep 精定位（粗筛 + 精查） |
| **跨 repo 经验检索** | 「别人怎么实现 SSE 的」——若索引的是文章/笔记而非当前代码 |

本项目 **`rag_search` 的定位**（工具描述里写死）：

- **用**：本地面试题库、给候选人出题、引用权威答案
- **不用**：写代码、debug、code review、用户已加载文件 —— 走 `read_file` / `extract_document_text`

这和 Cursor 等产品 **代码用 search/grep、文档用 index** 的分工一致。

### 4. grep/glob 的局限（答题要诚实）

| 局限 | 说明 |
|------|------|
| **不知道搜什么** | 用户只说「登录慢」，没有 symbol → 要先语义理解再猜 keyword，或 RAG 文档 |
| **语义改写** | 题库写「四种隔离级别」，用户问「事务隔离机制」——grep 字面可能对不上 |
| **超大结果集** | 泛词 `error` 命中过多 → 要加 glob 范围、正则、二次过滤 |
| **二进制 / 生成物** | 不应扫 `node_modules`、构建产物；需要 ignore 规则（Cursor 的 `.cursorignore`、ripgrep glob） |

所以 **不是 RAG 全面输给 grep**，而是：**代码主路径靠精确检索；语义检索补文档和概念层**。

### 5. 和 Hybrid RAG 的关系（串项目）

LingCoWork 的 Hybrid RAG（向量 + BM25 + RRF）解决的是 **题库 markdown** 的召回：

- 术语类 → BM25 硬命中
- 改写类 → 向量兜底

这套设计 **刻意不拿去索引整个 `internal/` 源码**——代码走 agent 内置 fs/shell 工具链。  
被问「你们项目为什么还做 RAG」：答 **「RAG 服务的是题库语料，不是替代代码搜索」**。

### 6. 30 秒口头版

> Coding Agent 找代码更像 IDE 的 **Find References**，需要精确 symbol 和最新文件内容，所以主流用 **glob 缩范围 + grep/ripgrep 定位 + read 上下文**，结果可验证、能直接改。
> RAG 擅长 **自然语言 + 非结构化文档**，比如面试题库、wiki；但代码一切 chunk 就丢结构、索引还会过期。
> 所以是 **分工**：代码 ground truth 在文件系统；RAG 补语义检索。我们项目里 `rag_search` 只搜题库，写代码走 read/grep/shell，工具描述里明确写了边界。

### 7. 若追问「将来会不会全用 RAG 索引代码库」

> 可能有 **混合** 趋势：embedding 做模块级「从哪下手」，grep 做行级精确定位；或 AST/LS LSP 结构化索引（比纯向量更贴代码）。
> 但 **改代码** 仍要落到具体文件行，RAG chunk 很难单独承担 edit 闭环。
> 关键不变量：**代码以仓库为准，索引只是辅助，冲突时 read 源码**——和 DeepWiki 那题同一口径。

---

## 十一、代码索引怎么做？和 DeepWiki 什么关系？

> 对应 [00 §27](00-common-questions.md)。  
> 关联 §九（DeepWiki）、§十（grep vs RAG）；Comate 产品侧称「代码索引」，本质是 **本地源码向量检索**。

**结论先行**：代码索引和 DeepWiki **同属「检索增强」大类**（query → 召回 → 塞进 context），但 **索引对象完全不同**——前者索引 **你工作区里的源码切片**，后者索引 **公开仓库的 AI 生成 wiki**。二者 **不是上下游，也不是替代关系**；可以 **互补**（wiki 懂架构叙事，索引给代码片段），但 **都会过时**，冲突时都以 **当前源码 + grep/read** 为准。

### 1. 代码索引在干什么（一句话）

给当前 workspace 的代码建 **本地向量库**，Agent 需要时用自然语言 **语义搜索** 相关文件片段（如 Comate 的 `codebase_search`），减少盲 grep。

### 2. 怎么做（通用管线，以 Comate ContextEngine 为例）

```
离线 / 后台（构建索引）
  扫描仓库文件（常要求 Git/SVN，有文件数上限）
    → 按函数/块切 chunk（code_context_items：path、行号、content）
    → 调 embedding API 得到向量
    → 写入本地 SQLite（embeddings 表，如 vec_distance_cosine 检索）
    → versioned_files 记 contentHash，文件变更则增量重 embed

在线（Agent 用时）
  用户/模型发起语义 query
    → query 也 embed
    → 向量近邻检索 topK chunk（可叠加路径关键词过滤）
    → 把「文件路径 + 代码片段」交给模型
    → 模型再 read_file / grep 精确定位、改代码
```

**和本项目 Hybrid RAG 的相似点**：都是 **切 chunk → embedding → 检索 → 增强生成**。  
**不同点**：语料是 **源码 AST/文本块**，不是 markdown 题库；存储在 **本机**（`~/.comate-engine/` 一类路径），不是云端 wiki。

**LingCoWork 现状**：**没有**给仓库源码建向量索引；`internal/rag/` 只服务 **面试题库 markdown**（`rag_search`）。代码走 `read_file` / shell grep。

### 3. DeepWiki 在干什么（对比用）

```
离线（Devin/Cognition 侧，针对 GitHub 公开 repo）
  读完整仓库 → LLM 归纳 → 生成结构化 wiki 页面

在线（MCP）
  read_wiki_structure / read_wiki_contents / ask_question
    → 返回 **架构叙事、模块说明**（不是逐行源码）
```

### 4. 二者关系：一张表说清

| 维度 | 代码索引（Comate 等） | DeepWiki |
|------|----------------------|----------|
| **索引什么** | 本地 **源码 chunk** | 远端 **AI 写的 wiki 文档** |
| **存哪** | 本机 SQLite + blob | 云端服务 |
| **召回形态** | `path` + 行范围 + 代码片段 | 章节标题 + 说明文字 |
| **典型问题** | 「事务提交在哪实现的？」 | 「这个项目模块怎么划分的？」 |
| **和 ground truth** | 较近（仍是源码），但 **有索引滞后** | 较远（二次归纳），**更易过时/幻觉** |
| **覆盖** | 当前 workspace；常要 Git |  mainly 公开 GitHub repo |
| **Agent 入口** | 内置工具 `codebase_search` | MCP 外挂（LingCoWork `mcp.example.json` 有示例） |
| **关系** | **并列两种 RAG**，不是谁生成谁 | 同上 |

**关系一句话**：

> 都是帮 Agent **少盲搜**；代码索引对准 **「改哪段代码」**，DeepWiki 对准 **「先搞懂项目」**。可以先用 DeepWiki/wiki 建立地图，再用代码索引或 grep 落到行；没有必然依赖。

### 5. 共同问题：过时

| 原因 | 代码索引 | DeepWiki |
|------|----------|----------|
| 代码 merge 了索引未 rebuild | ✔ | ✔（更严重） |
| 未保存 buffer / 未 push 分支 | ✔ | ✔（看不到本地改动） |
| AI 归纳错误 | 较少（直接嵌源码） | ✔ |

**工程缓解（代码索引）**：contentHash 增量更新、保存后触发 rebuild、**索引只作候选、改前必须 read**、符号级仍 grep。

**口径（和 §八、§九 一致）**：

> 索引 / wiki 都是 **检索入口**，不是规范；**冲突时 read 源码**。

### 6. 和 grep、本项目分工（串起来答）

| 手段 | 何时用 |
|------|--------|
| **grep/glob** | 已知 symbol、报错串、精确引用 |
| **代码索引** | 概念问法、不知关键词、大仓冷启动粗定位 |
| **DeepWiki / README / AGENTS.md** | 架构叙事、模块关系、改码规范 |
| **`rag_search`（本项目）** | **仅题库**，不索引 `internal/` 源码 |

### 7. 30 秒口头版

> 代码索引本质是给工作区源码做本地向量 RAG：扫文件、切 chunk、embed、存 SQLite，Agent 用 `codebase_search` 做语义召回，返回路径和片段，再 read 改代码。DeepWiki 也是检索增强，但索引的是公开 repo 的 AI wiki，返回的是架构说明不是源码行。它俩是 **并列关系**，一个偏精确定位代码、一个偏快速理解项目，都会过时，所以都要以当前文件为准。我们项目源码不用向量索引，grep/read 写代码，RAG 只做题库。

---

## 十二、大模型为什么在编程领域进步这么快？

> 对应 [00 §35](00-common-questions.md)。  
> 问题不是只让背 GPT 发布时间线，而是考察：**模型能力从哪里来、代码为什么适合训练和验证，以及 Coding Agent 的进步有多少属于模型、有多少属于系统工程。**

### 1. 结论先行

编程能力的进步不是某一个算法突然解决的，而是四层能力叠加：

1. **基础模型**：Transformer、规模化预训练和高质量代码数据，让模型学会语法、API 和常见程序结构。
2. **后训练**：指令微调、偏好对齐以及可验证奖励，让模型从「续写代码」变成「按要求解决问题」。
3. **推理阶段**：更长上下文、更多推理计算、规划和自我修正，让模型能处理跨文件、长链路任务。
4. **Agent Harness**：搜索代码、读写文件、运行命令、编译测试、查看报错，再把结果交回模型，形成可验证闭环。

所以今天 Coding Agent 看起来比早期代码补全强很多，**既有模型权重里的能力提升，也有模型外执行系统的提升**。不能把两者都算成模型本身的能力。

### 2. 简化的发展时间线

#### 第一阶段：RNN / LSTM，擅长局部序列

早期语言模型主要使用 RNN、LSTM。它们能按顺序处理文本，但长距离依赖难学、训练难并行，
面对一个长函数或跨文件依赖时容易忘记前面的内容。

#### 第二阶段：Transformer，让规模化训练成为可能

2017 年 Transformer 使用 Self-Attention，让每个 token 可以直接关注上下文中的其他 token，
同时训练过程能够大规模并行。它不只适合自然语言，也很适合代码：

- 变量定义和使用可能相隔很远
- 函数调用要联系函数签名
- 括号、缩进和控制流存在结构依赖
- 一个文件需要引用另一个文件中的类型和接口

Transformer 不是天然理解 AST，但注意力机制比传统序列模型更容易学习这些远距离关系。

#### 第三阶段：大规模预训练与 Scaling

GPT、BERT 之后，行业验证了「更大模型 + 更多数据 + 更多算力」可以持续提升泛化能力。
自回归模型通过预测下一个 token，在海量文本和代码中学习：

- 语言语法和代码语法
- 常见算法与设计模式
- API 的典型使用方式
- 注释、需求和代码之间的对应关系
- issue、补丁、测试与修复之间的关系

这时模型已经能做代码补全，但更像「根据前文续写」，不一定真正服从用户指令。

#### 第四阶段：代码专用训练与指令对齐

Codex、Code Llama 等代码模型开始使用更高比例的代码语料，并针对代码补全、函数生成和
Fill-in-the-Middle 等任务训练。随后指令微调与 RLHF 把能力进一步变成对话式使用：

```text
原来：给一段前缀，预测后面的代码
后来：读懂自然语言要求，解释、修改或生成指定代码
```

这一步使模型从「IDE 自动补全」走向「可以交流的编程助手」。

#### 第五阶段：推理模型与 Coding Agent

近年的重点不再只是一次生成正确答案，而是让模型使用更多推理计算，并接入真实开发环境：

```text
理解任务
→ 搜索仓库
→ 读取相关文件
→ 制定修改方案
→ 编辑代码
→ 编译 / 测试
→ 根据报错修正
→ 输出最终结果
```

模型开始从生成一个函数，发展到处理多文件修改、依赖安装、测试失败和较长时间运行的任务。
这也是 Coding Agent 和普通 ChatBot 的核心差别。

### 3. 为什么代码领域尤其适合快速进步？

#### 原因一：代码数据规模大，而且结构密度高

公开代码仓库包含源代码、README、注释、issue、commit diff 和测试。自然语言需求与代码实现
经常同时出现，模型可以从中学习「问题—修改—结果」关系。

但这里也有两个限制：公开代码存在许可证问题；训练集和 benchmark 可能重复，所以榜单提升
不能全部等同于真实泛化能力。

#### 原因二：代码具有明确规则

自然语言答案可能有多种说法，很难自动判断哪一种最好；代码则有较明确的反馈：

- 能不能解析
- 能不能编译
- 类型是否正确
- 单元测试是否通过
- 输出是否符合预期

这种反馈既能清洗训练数据，也能作为后训练和推理阶段的 verifier。

#### 原因三：代码可以形成自动闭环

模型生成代码后，可以立即交给编译器、测试框架和静态检查器执行，再根据错误继续修改。

```text
生成 → 执行 → 获得客观反馈 → 修正
```

写文章没有一个通用的 `go test` 判断内容是否正确，而代码有。因此代码任务特别适合
ReAct、强化学习和 Agent 循环。

#### 原因四：工具可以补偿模型记忆

模型不需要在参数里记住整个仓库。它可以通过 grep、glob、LSP、代码索引和 `read_file`
按需获取最新代码，再调用编译器和测试验证。

这使能力不再完全受限于「模型背过多少代码」：

```text
模型负责判断下一步做什么
工具负责提供事实和执行结果
```

#### 原因五：商业价值明确，投入集中

编程任务高频、结果可量化，而且可以直接节省开发时间。自动补全采纳率、测试通过率、
任务完成率都能形成产品反馈，因此企业愿意持续投入数据、算力和工程资源。

### 4. 主要进步体现在哪些方面？

#### 4.1 从补全到理解指令

早期主要补全当前行；现在可以根据自然语言生成函数、解释旧代码、修复 bug 和完成重构。

#### 4.2 从代码片段到仓库级任务

上下文窗口变长只是基础，更关键的是 Agent 会主动搜索和分批读取仓库。它开始处理：

- 跨文件类型与调用关系
- 依赖和配置修改
- 数据库迁移
- 测试与构建脚本
- 遵循仓库自己的规则

#### 4.3 从一次生成到执行反馈

以前模型输出代码就结束；现在会运行测试、读取报错、定位失败原因并再次编辑。
编译器和测试相当于给模型增加了外部校验器。

#### 4.4 从固定模型知识到动态上下文

RAG、代码搜索、LSP、MCP 和浏览器工具把最新文档、当前代码和外部系统状态动态放进上下文，
减少只凭训练记忆回答造成的过时和幻觉。

#### 4.5 从单轮助手到长任务 Agent

Agent 可以维护任务状态、拆分步骤、调用子 Agent、处理中断审批并在连接恢复后继续执行。
不过长任务中的错误会累积，因此仍需要 checkpoint、权限边界和人工确认。

### 5. 哪些是模型能力，哪些是 Harness 能力？

**模型本身的进步**：

- 更准确地理解需求和代码
- 更强的推理与规划
- 更好的跨语言迁移
- 更稳定的结构化输出和工具选择
- 更大的有效上下文范围

**模型外系统的进步**：

- grep、代码索引和 LSP 提供仓库事实
- 文件与 Shell 工具执行修改
- 编译器、测试和 lint 提供反馈
- 上下文压缩与 Memory 管理长任务
- checkpoint、中断恢复和审批保证可靠性
- sandbox 和 effect 系统限制危险操作

以 LingCoWork 为例，eino 负责组织 Agent 和工具循环，但 Workspace、审批、SSE 恢复、Memory、
Skill、MCP 和上下文压缩都是 Harness。**模型变聪明决定能力上限，Harness 决定它能不能安全、
稳定地完成真实任务。**

### 6. 当前仍没有解决的问题

- 代码能编译不代表业务语义正确，测试本身也可能不完整
- 大仓库的有效上下文仍有限，检索可能漏掉关键实现
- 长链任务容易早期判断错误、后面不断放大
- 模型可能为了通过测试而过拟合可见用例
- 依赖、权限、生产数据和环境差异无法仅靠生成代码解决
- benchmark 可能受训练数据污染，不能只看分数判断真实能力

因此更准确的判断不是「AI 已经能替代程序员」，而是：

> AI 已经从代码补全发展成能够操作开发环境的协作者，但复杂需求定义、架构取舍、
> 结果验收和生产责任仍需要人承担。

### 7. 30 秒口头版

> 大模型编程能力的进步主要来自四层：Transformer 和规模化预训练让模型学会代码结构；
> 代码专用训练与指令对齐让它从续写变成按需求开发；推理模型通过更多测试时计算提升规划和
> 修正能力；Coding Agent 再把搜索、文件、Shell、编译和测试接进来，形成执行反馈闭环。
> 代码领域进步特别快，是因为公开数据多、规则明确，而且编译和测试能提供自动、客观的奖励。
> 所以现在的进展不只是模型更聪明，也包括 Harness 更完整——模型决定上限，Harness 决定它
> 能否在真实仓库里稳定完成任务。

---

## 三十六、DDD：领域怎么划、领域怎么交互、和 Spring 贫血模型差在哪

> 对应 [00-common-questions.md](00-common-questions.md) 第 36–38 题。
> Java 后端面试高频。LingCoWork / klingwork **不是按 DDD 落地的项目**，
> 不要硬说「我们用了 DDD」。讲清概念，再用「按业务能力拆模块」做类比即可。

**结论先行**：

> DDD 首先是按**业务边界**拆系统，不是按 Controller / Service / DAO 拆层。
> 拆完以后，领域内部用充血模型和聚合保护不变量；领域之间不互相调内部对象，
> 只通过应用服务编排、领域事件或防腐层交互。
> 相对 Spring 里常见的贫血模型，最大优势是**业务规则内聚、边界清晰**，
> 复杂业务不会全部堆进上帝 Service。

### 1. 先把几个词说清

| 概念 | 一句话 |
|---|---|
| **限界上下文（Bounded Context）** | 一块业务里，同一套术语、模型和规则成立的范围。划领域首先划这个。 |
| **通用语言（Ubiquitous Language）** | 产品和研发共用的词。同一个词在不同上下文可以有不同含义，不要硬统一成一张大表。 |
| **聚合（Aggregate）** | 一组必须一起改、一起校验的对象；通过聚合根进出，保证不变量。 |
| **领域服务** | 规则不属于单个实体、但属于这个领域时，放领域服务，而不是塞进某个随机 Service。 |
| **应用服务** | 用例入口：开事务、调仓储、编排多个领域对象。本身尽量不写业务规则。 |
| **贫血模型** | 对象只有字段和 getter/setter，规则全在 Service。Spring 项目最常见。 |
| **充血模型** | 对象自己执行业务行为并保护不变量，Service 变薄，只做编排。 |

划领域看的是**业务能力是否独立、语言是否一致、数据是否能单独演进**，
不是看包名、表数量或团队人数。
「用户下单」和「库存扣减」经常分成两个上下文：Order 里的 SKU 和 Inventory 里的 SKU
含义不同，不要共用一张实体到处 import。

### 2. 领域之间怎么交互（第 36 题）

原则：**领域不直接持有另一个领域的实体或仓储。**
跨上下文只走明确契约，常见三种：

| 方式 | 何时用 | 要点 |
|---|---|---|
| **应用层编排（同步）** | 同一请求必须立刻拿到结果，如「下单时校验库存是否够」 | 应用服务调本领域 + 对方面向接口的能力；本领域模型不出现对方类 |
| **领域事件（异步）** | 对方是后果，不是当前用例的一部分，如「支付成功 → 发积分 / 发货」 | 发布领域事件，对方订阅；最终一致，要考虑幂等和失败补偿 |
| **防腐层 ACL** | 对方模型脏、旧、或语言不一致，如对接老系统 / 外部中台 | 翻译成自己的模型，隔离腐化，避免对方字段渗进自己的核心 |

面试里再补一句 **上下文映射**（Context Mapping），说明你不是只会 CRUD：

- **开放主机 / 发布语言**：提供稳定 API 或事件协议，对方按这份契约来
- **客户—供应商**：下游有需求，上游承诺兼容
- **遵奉者（Conformist）**：对方太强，只能按对方模型用
- **共享内核**：极少数必须共享的模型和表，越少越好
- **分离方式（Separate Ways）**：两边独立，不强行集成

落地时还要注意：

- 不要跨上下文直接连对方数据库、共用一张表
- 聚合之间不要互相持有对方引用，用 ID 关联
- 同步调用会把两个上下文绑成一次事务，边界会变糊；能异步就异步
- 事件要有业务主键、版本和幂等键，订阅方不能假定「只收到一次」

**30 秒口头版：**

> 领域按限界上下文拆。上下文内部通过聚合根改数据，规则留在模型里。
> 跨上下文有三条路：要同步结果就由应用服务编排，只依赖对方的接口，不依赖对方的实体；
> 只是通知后果就发领域事件，最终一致；对方模型不干净就加防腐层翻译。
> 核心是保护自己的模型和语言，不要让另一个上下文的表结构和字段漏进来。

### 3. 相对 Spring 贫血模型，DDD 最大优势（第 37 题）

Spring 常见写法：

```text
Controller → Service → Mapper/DAO
Entity / DO 只有字段
规则全在 XxxServiceImpl：校验、改状态、发消息、写多张表
```

这就是**贫血模型**：对象像数据结构，Service 像事务脚本。
CRUD、流程短、规则少时，这套又快又好维护。

充血 / DDD 的差别不是「对象里多几个方法」，而是**规则放在哪、不变量谁来保证**：

| | 贫血（Spring 常见） | 充血（DDD） |
|---|---|---|
| 业务规则 | 散落在多个 Service、工具类、甚至 Controller | 聚合根 / 领域服务内部 |
| 典型风险 | 同一个「已支付不能再取消」写了三遍，漏改一次就出 bug | 状态流转只有一处入口，非法调用直接拒绝 |
| Service 职责 | 越来越胖，变成上帝类 | 变薄，只做事务与编排 |
| 适用 | 后台管理、简单 CRUD | 规则多、状态机复杂、长期演进的核心域 |

最大优势可以收成一句：

> **把复杂业务的正确性收口到领域模型，而不是收口到程序员记不记得在每个 Service 里 if 一遍。**

面试别把 DDD 吹成银弹。对方如果追问缺点，主动说：

- 简单业务上 DDD 是过度设计，Spring 贫血模型开发更快
- 充血模型对团队要求高，语言和边界要持续对齐
- 和 MyBatis / JPA 懒加载、事务边界会别扭，要在仓储里做装配，不要让持久化模型直接当领域模型

**30 秒口头版：**

> Spring 项目里实体经常是贫血的，规则都在 Service。业务一复杂，Service 会变成上帝类，
> 同一条规则出现在好几个地方。DDD 用充血模型和聚合，让「能不能改、改完还合不合法」
> 由领域对象自己保证。最大优势是复杂规则内聚、边界清楚，而不是多写了几个 domain 包。
> 简单 CRUD 我不会上 DDD。

### 4. 前后端分离时，DDD 对团队协作的优势（第 38 题）

前后端分离解决的是**技术切分**：页面一套仓库，接口一套仓库。
它不自动解决业务切分。没有领域边界时，前端仍要理解一整张大接口文档，
后端仍是一个巨石 Service，联调按页面撕接口，而不是按业务能力交付。

DDD 的协作优势来自**限界上下文 ≈ 可独立交付的业务切片**：

- **语言对齐**：产品、后端、前端在同一个上下文里用同一套词（订单状态、审批节点），减少「这个 status=3 是什么意思」
- **契约稳定**：每个上下文对外只暴露应用服务 / API / 事件（发布语言），前端对接的是用例，不是数据库字段
- **团队按业务拆，而不是只按技术拆**：订单组、支付组可以各自前后端一起迭代；不是所有前端等同一个后端中台排期
- **改动隔离**：库存规则变了，不应该逼着交易页和用户中心一起改模型

注意：DDD 不是替代前后端分离，而是**在分离之后再按业务切开**。
实践上常见 BFF / 网关按上下文聚合数据，避免前端为了一个页面去打五个领域的内部接口。

**30 秒口头版：**

> 前后端分离只是把 UI 和接口拆开，如果后端仍是一张大模型，协作还是围着页面改接口。
> DDD 按限界上下文拆，一个上下文有自己的语言、模型和对外契约，团队可以按业务垂直交付。
> 前端对接的是稳定用例 API，而不是对方表结构。这样沟通成本和改动范围都会变小。

### 5. 和自己项目怎么挂钩（别说成「我们做了 DDD」）

更稳妥的说法：

> 我做的 Agent 系统不是按 DDD 落地的。分层上更接近应用服务编排：Handler 接协议，
> Service 开用例，Agent / 工具执行领域动作，Repository 落库。
> 但拆模块时用了类似限界上下文的思路——对话、工作区、审批、Skill、Memory 各自有模型和接口，
> 跨模块走事件或应用层编排，而不是互相引用内部结构。
> 核心域如果规则变复杂，会把不变量收回聚合或领域服务，而不是继续往 Service 里堆 if。

LingCoWork 里可类比、但不要划等号的点：

- Supervisor + 领域子 Agent ≈ 按能力拆上下文，主 Agent 做编排而不是深入对方内部
- AgentEvent / 审批 checkpoint ≈ 跨模块用明确事件和契约，而不是直接改对方状态
- 工作区 git、对话、校验各有自己的模型，避免共用一个大 struct 打天下

如果面试官要你画分层，用教科书这一套就够：

```text
用户接口（HTTP / 消息 / 前端）
        ↓
应用层（用例编排、事务、权限）
        ↓
领域层（实体、聚合、领域服务、领域事件）  ← 核心，不依赖框架
        ↓
基础设施（DB、MQ、外部 API、ACL）
```

领域层不 import Spring / Gin，不直接写 SQL。这是和「Service 里到处 @Autowired Mapper」最大的结构差别。

### 7. 30 秒口头版

> 大模型编程能力的进步主要来自四层：Transformer 和规模化预训练让模型学会代码结构；
> 代码专用训练与指令对齐让它从续写变成按需求开发；推理模型通过更多测试时计算提升规划和
> 修正能力；Coding Agent 再把搜索、文件、Shell、编译和测试接进来，形成执行反馈闭环。
> 代码领域进步特别快，是因为公开数据多、规则明确，而且编译和测试能提供自动、客观的奖励。
> 所以现在的进展不只是模型更聪明，也包括 Harness 更完整——模型决定上限，Harness 决定它
> 能否在真实仓库里稳定完成任务。
