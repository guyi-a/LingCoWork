# 项目一 · 定位与多 agent 架构

> 对应简历：项目 intro（Agent Harness 定位）+ 第一条 bullet「多 agent 架构设计」。
>
> 这份文档是**知识梳理**，不是问答清单。目的是把每一层讲清楚：它是什么、为什么需要、
> 怎么实现的、有哪些取舍。读完能自己组织语言讲出来，比背答案有用。
>
> 代码位置：`/Users/guyi/LingCoWork`，主要在 `internal/agent/`。

---

## 一、这个项目在解决什么问题

### 1. 为什么要做 LingCoWork

真实原因有两个：**尝试 Agent 框架，以及解决自己的求职需求。**

我一直在尝试不同的 Agent 框架。单 Agent 的模型—工具循环大多都能实现，
但到了多 Agent，框架是否提供 AgentAsTool、独立上下文、事件冒泡和中断恢复等原语，
会直接决定系统需要自己补多少基础设施。在我尝试过的框架里，
eino ADK 对多 Agent 的支持最完整，也最符合我想验证的架构，所以我希望用一个完整项目
把它真正跑起来，而不是只写几个孤立示例。

同时，我自己正处在求职阶段。求职准备不是一次问答，而是一组持续任务：
模型要阅读本地简历和面试笔记、搜索招聘网站、整理资料、生成文档，
再根据前面的结果继续修改。自己是重度用户，需求是否真的有用，不需要靠猜。

这两个原因合在一起，就形成了 LingCoWork：用 eino ADK 搭建一个**本地 Agent 工作站**，
让用户挂载工作区后，由 Supervisor 把简历分析、招聘搜索、模拟面试和资料研究等任务
交给不同的领域 Agent；同时提供文件、命令、联网和浏览器等通用执行能力。

项目做深以后，需要解决的不只是多 Agent 拓扑，还有模型之外的整个 Agent Harness：

- 工具怎样注册、执行并把结果重新交给模型
- 危险操作如何审批，暂停后怎样继续
- 页面刷新、网络中断或进程重启后怎样恢复
- 长对话怎样压缩，避免撑爆上下文窗口
- 子 Agent 怎样分工，又怎样把过程展示给用户
- Skill、Memory 和 MCP 怎样动态进入模型上下文

这一层就是 **Agent Harness**：模型负责理解和决策，Harness 负责准备上下文、
执行工具、推进循环、反馈结果并限制行为边界。

所以 LingCoWork 的目标不是再做一个普通求职聊天机器人，而是：

> **以自己的求职需求作为真实场景，用 eino ADK 验证多 Agent 架构，
> 并围绕它补齐一套能够实际完成任务的本地 Agent Harness。**

### 2. 为什么选求职场景

自己就是重度用户，需求判断不需要猜。简历要反复修改，面试问题需要持续整理，
岗位信息又分散在招聘网站，这些都是真实而高频的需求。

同时，这个场景天然覆盖了 Harness 的主要难点：

- 招聘搜索需要操作浏览器，会遇到登录态、页面观察和危险操作审批
- 简历分析需要读取本地文件，会遇到工作区范围和文件安全边界
- 模拟面试会形成长对话，需要上下文压缩和长期记忆
- 资料调研和题目生成可能持续很久，需要子 Agent、流式进度和中断恢复
- 用户还会接入自己的工具，需要 Skill 与 MCP 的动态能力扩展

**场景是竖的，能力是横的。**选一个自己能判断需求的垂直场景，
但每个需求都逼着你去解一个通用问题——这样做出来的东西才不是玩具。

---

## 二、技术选型：框架给了什么，自己做了什么

### 为什么是 Go + eino

我尝试过多个 Agent 框架，最终选择 eino，不只是因为项目主语言是 Go，
更重要的是它的 **ADK（Agent Development Kit）**对多 Agent 提供了比较完整的组织原语：
把 Agent 包装成工具、独立上下文、事件流迭代器以及中断恢复钩子。
这些正好对应我想验证的问题，不需要先从零造一套多 Agent 调度框架。

### 边界要说清楚

面试里这个问题一定会被追问，含糊会显得是在蹭框架的功劳。

| 层 | eino 提供 | 本项目实现 |
|---|---|---|
| **agent 组织** | ChatModelAgent、Runner、AgentAsTool、事件迭代器 | 拓扑设计、职责划分、提示词 |
| **中断恢复** | interrupt 协议、checkpoint 接口 | SQLite 的 CheckPointStore、待审批队列持久化、恢复后的父子关系重建 |
| **工具执行** | ToolsNode、ToolMiddleware 接口 | 24 个工具、审批中间件、错误转结果中间件 |
| **审批** | — | 全部：影响推导、分级策略、模式管理、分类器 |
| **上下文管理** | 官方有 summarization 中间件 | 没用官方的，自己做了 turn 间的持久化压缩 |
| **技能 / MCP** | — | 全部 |
| **长期记忆** | — | 全部：两级文件存储、每轮注入、写入过审批 |
| **流式传输** | 事件流 | SSE 编码、缓冲、断线续接 |

一句话概括：**框架给的是「agent 怎么组织」，自己做的是「agent 怎么安全可靠地跑在产品形态里」。**

---

## 三、为什么是多 agent

### 单 agent 挂 24 个工具的三个问题

这不是拍脑袋决定的，是先做了单 agent 版本、撞到问题才拆的。

**提示词稀释**。24 个工具的描述，加上各领域的方法论——招聘搜索要教它登录检查和抓取节奏，
模拟出题要教它难度梯度和题型配比——全塞进一个系统提示词。这些知识彼此无关，
堆在一起互相干扰，模型在每个具体任务上的表现反而都变差。

**上下文污染**。深度研究一跑就是几十次工具调用，读文件、写文件、反复修改。
这些中间过程如果全留在主对话历史里，几轮之后主线对话的质量急剧下降——
用户问的是「帮我看看简历」，模型的上下文里塞满了上一个研究任务的文件内容。

**权限面**。浏览器操作、shell 执行这类高危工具，没有理由让每个任务都拿得到。

### 拆分的依据

按**领域知识 + 工具面**拆，不是按功能模块拆。

判断标准是：这两个任务需要的方法论知识是否互不相关？如果一个 agent 的提示词里
有两段互相看不懂的内容，那就该拆。

---

## 四、AgentAsTool：挂载方式的选择

eino ADK 提供两种挂 sub-agent 的方式，这个选择影响很大。

| | Transfer（`SubAgents` + `TransferToAgent`） | AgentAsTool（`NewAgentTool`） |
|---|---|---|
| 控制权 | 真的转移过去，sub-agent 接手对话 | 始终在 supervisor 手里 |
| 上下文 | 共享同一份消息历史 | sub-agent 有自己独立的上下文 |
| 返回 | sub-agent 直接对用户说话 | 结果作为 tool result 回给 supervisor |

选了 AgentAsTool，三个理由：

**上下文隔离**。sub-agent 干活产生的所有中间过程都在它自己的上下文里，
结束后整个丢弃，只有最终结果穿透回来。主线历史里 deep_research 那一轮
只占**一次 tool_call + 一次 tool_result** 的位置，不管它内部跑了多少步。
这直接解决了上面说的「上下文污染」。

**UI 有天然的锚点**。工具调用在前端本来就是一张卡片，sub-agent 的活动挂在这张卡片下面展开，
结构和用户心智一致。Transfer 模式下没有这个锚点，只能另想办法表达层级。

**拓扑是可组合的**。对 supervisor 来说 sub-agent 就是个普通工具，
今天四个明天五个，supervisor 只是多一个工具，别的什么都不用改。
理论上还能递归——sub-agent 自己也可以挂 sub-agent。

**代价**：supervisor 拿不到 sub-agent 的中间推理，只能看结果。
对这个场景可接受——supervisor 的职责是派发和汇总，不是复核。

---

## 五、五个 agent 的实际装配

```
Runner (EnableStreaming, CheckPointStore = SQLite)
└── supervisor (root)
    ├── baseTools × 24
    ├── deep_research      → 后台研究员
    ├── job_search         → 招聘搜索
    ├── resume_analyzer    → 简历自评
    ├── question_planner   → 模拟出题
    └── MCP 工具（每轮动态注入）
```

| agent | 构造方式 | 中间件 | 最大迭代 |
|---|---|---|---|
| `supervisor` | `NewChatModelAgent` | 技能索引 + 记忆 + 工作区 + 动态工具 | 50 |
| `deep_research` | `deep.New`（ADK 预制） | 技能索引 + 记忆 | 50 |
| `job_search` | `NewChatModelAgent` | 技能索引 + 记忆 | 50 |
| `resume_analyzer` | `NewChatModelAgent` | 技能索引 + 记忆 + 工作区 | 30 |
| `question_planner` | `NewChatModelAgent` | 技能索引 + 记忆 + 工作区 | 50 |

记忆挂在全部五个上，理由和技能索引一样：sub-agent 也该知道用户的偏好和这个工作区的
约定，否则同一件事主 agent 记住了、sub-agent 不知道。

### 为什么只有 deep_research 用预制模式

`deep.New` 是 ADK 自带的 DeepAgent 模式，内置了规划、todo 管理、文件后端这一套。
深度研究这个场景确实需要多步规划，用现成的省事。

但**关掉了它的两个默认能力**：

```130:131:internal/agent/adk_agent.go
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
```

`WithoutWriteTodos`：默认的 todos 中间件会强行注入一批工具和提示词，
和项目自己的工作区文件工具重复了。

`WithoutGeneralSubAgent`：不允许它再 spawn 子 agent。**不让套娃**——
拓扑深度不可控会让审批、可观测性、上下文预算全部失控。

另外也没给它 ADK 原生的 filesystem backend，继续用项目自己的工作区工具，
保证所有 agent 看到的是同一个文件视图。

---

## 六、委派的审批语义

这是个容易被忽略但很能体现思考深度的细节。

### 委派本身没有副作用

`supervisor` 调用 `deep_research` 这个动作，**自己不产生任何副作用**。
真正的副作用发生在 sub-agent 后续的每一次工具调用上，而那些调用会经过
sub-agent 自己挂的审批中间件，一次都不漏。

所以四个 sub-agent 的委派调用在影响注册表里显式登记为「委派」类，直接放行：

```92:100:internal/agent/adk_agent.go
	for _, name := range []string{
		DeepResearchAgentName, JobSearchAgentName,
		ResumeAnalyzerAgentName, QuestionPlannerAgentName,
	} {
		effects.Register(name, effect.Static(effect.Effect{
			Kind:  effect.KindDelegate,
			Agent: name,
		}))
	}
```

**不登记会怎样**：审批系统的兜底方向是「推导不出影响 → 问人」。
不登记的话每次委派都会弹一张审批卡，等于让用户为一个没有任何影响的动作点确认——
纯打扰，而且会加速审批疲劳，用户很快就会条件反射地点通过，安全机制反而失效。

**这体现了 fail-closed 设计的一个代价**：默认收紧是对的，
但正因为默认收紧，你必须把所有「确实无害」的情况显式登记出来，否则系统会吵得没法用。

### 五个 agent 共享同一个中间件实例

```102:106:internal/agent/adk_agent.go
	// One middleware value for every agent in the topology. Constructing it
	// per agent would work today and drift tomorrow: a change made at one of
	// five call sites would leave the other four judging calls by the old
	// rules, and the sub-agents are exactly where that would go unnoticed.
	approvalMW := approval.Middleware(approvalModes, classifier, effects)
```

每个 agent 各构造一份在今天也能跑对，但**明天会漂移**：
规则改了要五处同步，漏一处就是某个 sub-agent 按旧规则放行。
而 sub-agent 恰恰是最不容易被注意到的地方——它跑在折叠的卡片里，
出问题很久都没人发现。

技能索引和工作区中间件同理，都是无状态的共享实例。
这样还有个附带好处：**主 agent 和 sub-agent 看到的工作区视图必然一致**。

---

## 七、工具面的分配

### baseTools 所有 agent 共享

24 个内置工具，五个 agent 拿的是同一份。没有按 agent 裁剪。

这看起来和「权限面」那个拆分理由矛盾，其实不然——**权限面的收窄靠的是提示词和职责边界，
不是工具表的物理隔离**。真正的强制约束在审批层，而审批层对谁都一视同仁。

物理裁剪工具表的收益不大，代价却不小：每个 agent 一份工具表意味着五处维护，
而且一旦某个 agent 确实需要某个工具，改起来要动装配代码。

### MCP 只给 supervisor

```56:60:internal/agent/adk_agent.go
// Only the root agent, because a remote server's tool set is unbounded and
// unrelated to what a sub-agent was built to do — handing job_search a
// filesystem server would grow every sub-agent's prompt with tools none of
// them were designed around. The approval gate would still hold, so this is
// about exposure and context cost rather than a hole.
```

理由是**远程 server 的工具集无界**，而且和 sub-agent 的职责无关。
给 job_search 挂一个文件系统 server，只会让它的提示词膨胀一堆用不上的工具。

**注意这不是安全问题**——审批门对谁都拦着。这纯粹是暴露面和上下文成本的考虑。

把「这是成本问题不是安全问题」分清楚，比笼统说「为了安全」准确得多，
也更容易经得起追问。

### 传函数不传切片

```go
dynamicTools func(context.Context) []tool.BaseTool
```

这个类型选择是被框架约束逼出来的。MCP server 可能在应用启动**之后**才连上——
OAuth 授权本身就是交互式的，用户是在应用跑起来之后才点「授权」的。

而 eino 的 agent 在**首次运行时用 `sync.Once` 冻结工具表**。
构造时传进去的切片，此后就定型了，授权完成之后模型也永远看不到那些工具。

传函数则每轮被调用一次，拿到的是当下的工具集。

---

## 八、静态图与动态注入

上面那个问题不是孤例，它是**框架核心取舍的直接后果**，值得单独讲。

eino 的设计哲学是「编译期确定、运行时不可变」——图在 `Compile()` 阶段完成拓扑校验、
类型检查、并发计划，运行时不能改。换来的是类型安全和可推理性。

代价是：**所有运行期会变的东西，都要显式地绕一道。**

绕的办法是 `adk.ChatModelAgentMiddleware` 的 `BeforeAgent` 钩子。
只要 agent 挂了任意一个 handler，eino 每轮都会从冻结的基准工具表复制一份
`runCtx.Tools` 交给 `BeforeAgent` 改，改完重新生成 toolInfos 并覆盖派发表。

项目挂了四个：

| 中间件 | 每轮做什么 | 挂在哪 |
|---|---|---|
| 技能索引 | 重新扫描已安装技能，拼进指令 | 全部 5 个 |
| 记忆 | 读用户级 + 项目级记忆文件，拼进指令 | 全部 5 个 |
| 工作区 | 把当前会话的工作目录状态拼进指令 | supervisor + 简历 + 出题 |
| 动态工具 | 追加当前可用的 MCP 工具 | 仅 supervisor |

前三个往指令后面接文字，第四个往工具表里加工具。

### 挂载顺序是按变动频率排的

```
技能索引 → 记忆 → 工作区 → 动态工具
```

这个顺序是为了**提示词缓存**。缓存按前缀匹配，system prompt 在最前面，它一变，从那个
字节往后的整段缓存全部作废。所以把变动频率低的排前面，改动时被作废的前缀就更短。

三者的变动频率大致是：技能索引（装卸技能时变）≈ 记忆（写入时变）< 工作区状态（会话
中途绑定项目时变）。

顺带一个必须守住的约束：**这些拼进去的片段必须字节稳定**。同样的状态渲染两次必须完全
一致——不能放计数、不能放时间戳、遍历 map 要显式排序。否则每轮都是新的前缀，等于把
整个提示词缓存废掉。技能索引那边是靠显式 `sort.Strings` 保证的，记忆那边是靠「条目
保持文件原始行序、片段里不放额度和条数」。

### 为什么是 `BeforeAgent` 而不是更细的钩子

ADK 还提供 `BeforeModelRewriteState`，**每次模型调用前**都跑（一轮里可能几十次）。

选择依据是**这个状态在一轮之内会不会变**。工作区状态和技能索引在一轮之内不会变，
所以每轮算一次就够；选更细的钩子只会让同一轮里每次模型调用都重算，纯浪费。

有意思的是这和 eino 官方中间件的分配逻辑完全一致：
`skill`、`filesystem`、`plantask` 都挂 `BeforeAgent`，
而 `summarization`、`reduction`（历史随循环不断变化）挂 `BeforeModelRewriteState`。

### 技能索引：移植时踩到的坑

技能索引原本可以在构造时烤进指令，一次搞定。做成每轮注入是因为
**技能市场装完的技能要下一轮就能用**。

这正是从实习项目移植技能机制时撞到的问题：那边（TS 自研运行时）每轮 run
本来就重新扫描目录，装完自然生效；eino 这边指令在构造时固定，
必须显式补出「每轮重新拼接」这个语义。

**移植一个功能，难点常常不在功能本身，而在它隐含的架构前提。**

---

## 九、子过程的可观测性

deep_research 可能跑几分钟。用户不能接受「发出请求 → 几分钟黑盒 → 一大坨结果」。

### 事件冒泡

```245:247:internal/agent/adk_agent.go
			// Bubble up sub-agent (deep_research) internal events to the
			// Runner's iter so the UI can show real-time progress.
			EmitInternalEvents: true,
```

只在 supervisor 上开。sub-agent 的内部事件会通过 root Runner 的迭代器冒泡出来。

### 父子关联

光有事件不够，前端要知道这个事件该挂在哪张卡片下。

做法是维护 `sub-agent 名 → root tool_call id` 的映射：supervisor 说
「我要调 deep_research，call_id=X」时记下，此后所有来自 `deep_research` 的事件
发 SSE 时都带上 `parent_tool_call_id=X`。

落库时这些内部事件跟着消息一起存，也带着这个 id，**刷新页面后卡片能原样重建**。
中断恢复时这个映射要重建，否则 resume 之后的 sub-agent 事件会挂不上父卡片。

### 为什么不用 eino 的 callback

eino 有一套 `callbacks` 机制，项目里定义了但没用（`NewSSEHandler` 是死代码）。

原因很具体：**callback 拿不到 `AgentName`**，无法区分事件来自 root 还是某个 sub-agent。
而 `AgentEvent` 天然带这个字段。

这是 ADK 相对老路线（react agent + callback 观测）的一个实实在在的优势，
也是当初从 `flow/agent/react` 迁过来的动机之一。

---

## 十、与实习项目的对照

同一类系统在 TS 和 Go 两个技术栈下各做了一遍，这个对照是这份经历里最有价值的部分。

**骨架是同构的**：都是主控 + 领域专家，专家以工具形式被派发，
领域知识跟着专家走而不是堆在主控。

**差异全在框架给的自由度**：

| 问题 | klingwork-app / qai-sdk（TS 自研运行时） | LingCoWork（Go / eino） |
|---|---|---|
| 工具列表会变 | 每轮 run 重新组装工具集是默认行为 | 图冻结，靠 `BeforeAgent` 每轮重新注入 |
| 技能装完要生效 | 每轮重新扫描目录，天然生效 | 指令构造时固定，靠中间件每轮拼接 |
| sub-agent 挂载 | 内置专家 + 路由表，`agent_<id>` 派发工具 | `NewAgentTool` 包成工具 |
| 中断恢复 | 自己实现抢救 + 孤儿修复 | 框架提供 interrupt/checkpoint，应用层配合 |
| 审批 | ToolSpec 装饰器 | `compose.ToolMiddleware` |

两个观察值得说：

**审批的实现形态两边几乎一样**——都是「在工具执行外面包一层」。
说明这个模式和语言、框架无关，是问题本身的结构决定的。

**「运行时变化的东西怎么进入模型视野」差异最大**——一边是默认行为，
一边要显式绕道。这说明框架的核心取舍会渗透到应用的每个角落。

做完这两遍最大的体会是：**多 agent 的难点不在拓扑**，拓扑五分钟就画完了。
难在委派的审批语义、子过程的可观测性、动态能力怎么进一个静态图——这些才是花时间的地方。

---

## 十一、诚实的边界

主动说出来比被挖出来强。

**子 agent 派发是同步阻塞的**。supervisor 调 deep_research 就在那儿干等着，
几分钟内做不了别的。实习项目里见过更好的形态——任务表 + 唤醒机制，
提交后立刻返回，完成时事件驱动地叫醒。这是明确的下一步。

**上下文压缩只覆盖 turn 之间**。压缩跑在 `runner.Run` 之前，
一轮之内 agent 跑几十步把历史撑爆是不会触发的——而 `MaxIteration: 50` 的
deep_research 恰恰最容易撞上。框架的 `BeforeModelRewriteState` 就是为这个准备的，
补的路径是加一层而不是搬家。

**单机形态**。流缓冲和审批模式表都在进程内存里。多实例部署要把缓冲外置
（Redis Stream 之类）并做会话亲和。桌面交付形态下单机是合理选择，但这是个明确的边界。

**没接 tracing**。callback 层加一个 handler 就能拿到 token、延迟、trace，一直没做。

**没有真实用户**。定位是工程能力验证 + 自用，不装作有增长数据。

**关于「独立开发」被质疑用了 AI 编程**：大方承认深度使用了 AI 编程工具，
但架构决策和每一处设计取舍是自己的——这份笔记体系本身就是证明，
任何一个模块为什么这么做都能展开讲。

---

## 十二、串起来的主线

如果只能讲一条线，是这条：

**目标**是完整实现一遍 Agent Harness——模型之外的执行系统。
选求职场景，因为自己是用户，且这个场景天然覆盖 Harness 的全部难点。

**框架**选 eino ADK，它给的是 agent 的组织原语；
审批、持久化、上下文压缩、技能、MCP、长期记忆这些 Harness 核心件都是自己做的。

**拓扑**是 supervisor + 4 个领域 sub-agent，以工具形式挂载。
选 AgentAsTool 而非 Transfer，核心收益是上下文隔离——
sub-agent 跑几十步，在主线历史里只占一次工具调用的位置。

**委派本身不过审批**，因为它没有副作用；但必须显式登记，
否则 fail-closed 的兜底会让它弹一张毫无意义的审批卡。
五个 agent 共享同一个中间件实例，防的是规则漂移。

**动态能力进静态图**是这个框架下最核心的约束：eino 编译后不可变、
工具表首次运行就冻结，所以 MCP 工具、技能索引、长期记忆、工作区状态全靠
`BeforeAgent` 每轮重新注入，连传参类型都得是函数而不是切片。
四个中间件的挂载顺序按变动频率排，是为了少作废提示词缓存的前缀。

**子过程可观测**靠 `EmitInternalEvents` + 父子 id 映射，
这也是选 ADK 而不是 callback 的直接原因——callback 拿不到 agent 名。

最后落到**对照**：同一类系统在 TS 和 Go 下各做一遍，
审批的形态几乎一样（说明是问题结构决定的），
动态性的处理差异最大（说明是框架取舍决定的）。
