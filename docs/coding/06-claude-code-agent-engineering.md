# Claude Code Agent 工程设计：从 Loop 到后台任务

> 本文整理 Claude Code 暴露出来的 Coding Agent 工程思想，重点关注 Agent Loop、工具系统、
> 权限控制、上下文压缩、子 Agent 和后台任务。  
> 目标是理解可复用的设计原则，而不是复制某个版本的专有实现。

---

## 一、资料边界：能看到什么，不能把什么当官方事实

2026 年 3 月 31 日，`@anthropic-ai/claude-code@2.1.88` 发布包误带了约 60 MB 的
`cli.js.map`。Source Map 的 `sourcesContent` 可以还原大量 TypeScript 源文件，因此网上出现了
很多源码镜像和架构分析。

需要区分三类材料：

1. **Anthropic 官方文档**：可以确认产品契约和公开行为，但不等于完整内部实现。
2. **基于特定快照的架构分析**：适合理解设计，但可能包含误读，且功能开关和后续版本会变化。
3. **Clean-room 教学实现和开源 Harness**：不是 Claude Code 原码，但最适合运行、修改和学习。

这次事件不是模型或服务端训练代码泄露，也不是黑客攻入 Anthropic 服务端。暴露的主要是
Claude Code 客户端 Harness 快照；内部后端、完整构建系统以及被编译期裁剪的模块并不都在里面。

不建议下载或运行所谓“泄露源码完整版”的二进制压缩包：

- 代码仍属于 Anthropic 的专有知识产权，镜像可能被 DMCA 下架。
- 已有攻击者借热点传播恶意可执行文件。
- 单个历史快照也不能代表 Claude Code 当前实现。

本文只引用公开文档、架构分析和可合法阅读的独立实现，不提供泄露源码镜像。

---

## 二、总览：核心循环很小，外围工程才是主体

Coding Agent 最核心的逻辑可以压缩成一个循环：

```text
组装上下文
  → 调用模型
  → 模型返回文本或工具调用
  → 权限判断
  → 执行工具
  → 把结果写回上下文
  → 再次调用模型
  → 直到完成、暂停、失败或达到预算
```

真正困难的不是写出 `while`，而是保证循环能够：

- 连续运行而不撑爆上下文。
- 执行真实命令但不越过安全边界。
- 遇到工具失败、网络错误和进程退出后仍可恢复。
- 把长探索委派给子 Agent，而不污染主上下文。
- 让慢任务进入后台，同时保持结果可追踪。
- 将过程流式展示给用户，并允许暂停、审批和取消。

因此可以把 Coding Agent 看成：

```text
                 ┌────────────────────┐
                 │   Context Builder  │
                 └─────────┬──────────┘
                           ↓
User / UI → Session → Agent Loop → Model Adapter
                           ↓
                    Tool Dispatcher
                           ↓
            Permission / Hooks / Sandbox
                           ↓
                Files / Shell / MCP / Git
                           ↓
                  Observation / Events
```

核心 Loop 只负责协议编排，复杂能力应放在它周围的确定性子系统中。

---

## 三、Agent Loop：模型负责决策，Harness 负责状态机

### 1. 最小实现

一个简化的 Agent Loop 如下：

```python
while turns < max_turns:
    context = build_context(session, tools, rules)
    context = compact_if_needed(context)

    response = model.stream(context)
    session.append(response)

    if not response.tool_calls:
        return response.text

    for call in response.tool_calls:
        arguments = validate(call)
        decision = permission.check(call.name, arguments)

        if decision == "deny":
            result = tool_error("permission denied")
        elif decision == "ask":
            checkpoint_and_wait_for_user()
        else:
            result = tools.execute(call.name, arguments)

        session.append(tool_result(call.id, result))
```

代码不复杂，但有几个不能丢的协议约束：

- 每个工具调用都要有唯一 `tool_call_id`。
- 工具结果必须与原调用正确配对。
- 工具抛错应转换成模型可以观察的结果，不能让整个 Loop 无条件崩溃。
- 暂停审批时必须保存 checkpoint，恢复后不能重复执行已完成的副作用。
- 设置最大轮数、最大成本、超时和取消信号，防止死循环。
- 流式文本、工具开始、工具结果和最终完成应使用结构化事件表达。

### 2. 为什么使用异步生成器或事件流

模型响应本身是流式的，工具也可能运行很久。Loop 如果只返回最终字符串，UI 将无法展示：

- 文本增量。
- 工具调用开始和结束。
- 审批请求。
- 子 Agent 和后台任务状态。
- 重试、压缩和模型切换。

因此成熟实现通常让 Loop 产生事件：

```text
assistant.delta
tool.requested
permission.requested
tool.started
tool.completed
context.compacted
subagent.started
run.completed
```

事件流同时服务 UI、日志、审计和恢复，比在业务代码里拼接字符串更可靠。

### 3. 停止条件

Agent 不能只依赖“模型说自己完成了”。Harness 至少要处理：

- 模型没有继续请求工具，正常结束。
- 用户取消。
- 等待审批或补充信息。
- 达到最大 step、token、费用或运行时间。
- 连续重复同一个失败动作。
- 上下文无法继续压缩。
- 模型服务或本地执行环境发生不可恢复错误。

---

## 四、工具系统：工具不是函数列表，而是一条受控执行管线

### 1. 一个工具至少有四个面

```text
模型面：name、description、JSON Schema
路由面：注册、发现、启用范围、动态 MCP 工具
执行面：参数校验、handler、超时、取消、并发控制
安全面：effect、权限策略、审批、沙箱、审计
```

典型工具定义可以抽象为：

```go
type Tool struct {
    Name        string
    Description string
    InputSchema JSONSchema
    Effect      Effect
    Execute     func(ctx context.Context, input any) (Result, error)
}
```

其中 `Effect` 比工具名称更重要：

```text
read_file("/workspace/a.go")     → 工作区只读
write_file("/workspace/a.go")    → 工作区写入
shell("go test ./...")           → 本地进程
shell("rm -rf ...")              → 高风险破坏操作
http_request(...)                → 外部网络
```

同一个 Shell 工具既可能安全，也可能危险，所以权限不能只按工具名判断，还要分析参数和影响范围。

### 2. 工具执行管线

```text
模型选择工具
→ Schema 校验
→ 参数规范化
→ Effect 推导
→ Policy 判断：allow / ask / deny
→ Hook / Sandbox
→ 执行、超时与取消
→ 输出裁剪或外置
→ Tool Result 回填
```

应坚持两条边界：

1. **工具描述负责帮助模型选对工具，不负责安全。**
2. **权限和沙箱必须由确定性代码执行，不能只靠 Prompt 要求模型自律。**

### 3. 大工具结果不能直接塞满上下文

一次日志或文件读取可能产生数万行输出。如果原样回填，会同时损害：

- 上下文容量。
- 模型注意力。
- API 延迟和费用。
- Prompt Cache 命中。

常见处理顺序：

```text
执行前限制范围
→ 执行中限制字节数和行数
→ 完整结果写入外部文件或对象存储
→ 上下文只返回摘要、关键片段和引用
→ 模型需要时按 offset/limit 再读
```

这比“等上下文满了再总结整个对话”更早、更便宜。

---

## 五、权限控制：模型意图不能成为安全边界

### 1. 分层防护

```text
Prompt 引导
→ 工具参数 Schema
→ Policy / Effect 分类
→ 用户审批
→ 工作区路径限制
→ 进程或容器沙箱
→ 超时、资源配额和审计
```

各层职责不同：

- Prompt 降低模型主动犯错的概率。
- Schema 阻止畸形参数。
- Policy 决定允许、询问还是拒绝。
- 审批把高风险决策交给用户。
- 沙箱限制即使判断失误后真正能造成的影响。
- 审计帮助追责和复盘。

### 2. 权限决策应包含上下文

不能简单地规定“Bash 永远审批”或“Edit 永远允许”。更合理的输入包括：

- 工具与完整参数。
- 读写路径是否位于 Workspace。
- 是否访问网络、凭据或系统目录。
- 命令是否不可逆。
- 当前 Agent 身份，是主 Agent 还是受限子 Agent。
- 操作来源，是前台交互还是后台任务。
- 用户之前是否批准过相同作用域。

### 3. 审批不是一个弹窗，而是可恢复协议

正确流程是：

```text
运行到危险工具
→ 生成 pending approval
→ 持久化当前 checkpoint
→ 向 UI 推送审批事件
→ 当前执行暂停
→ 用户批准或拒绝
→ 恢复同一次 run
→ 只执行一次或返回拒绝结果
```

如果只把审批做成同步阻塞函数，断线、刷新页面或服务重启后就很难恢复。

### 4. 后台执行的特殊问题

后台子 Agent 无法直接占用主界面询问用户。常见策略是：

- 将审批请求上浮到主会话。
- 暂停后台任务并等待主会话批准。
- 对后台模式裁剪高风险工具。
- 非交互环境中默认拒绝需要询问的操作。

---

## 六、上下文压缩：不是一次总结，而是分级降噪

### 1. 上下文由什么组成

```text
System Prompt
+ 项目规则和 Memory
+ 工具定义
+ 对话历史
+ 工具调用与结果
+ 当前工作区状态
+ Todo / Plan / 后台任务通知
+ 压缩摘要
```

压缩目标不是单纯“变短”，而是在 token 预算内尽量保留：

- 当前任务和验收标准。
- 已作出的关键决策及原因。
- 修改过的文件和未完成事项。
- 最近几轮精确对话。
- 工具调用与结果的协议结构。
- 可以重新读取外部事实的引用。

### 2. 五类策略

基于公开架构分析，可以把 Claude Code 特定快照里的思路归纳成五类。部分机制曾受
Feature Flag 控制，名称和细节不应当作当前版本的稳定 API。

#### Budget Reduction

先减少动态附加内容或非关键预算，不触碰主要历史。

#### Snip

清除较旧、体积很大的工具结果正文，但保留工具调用与结果配对结构：

```text
[Old tool result content cleared]
```

#### Microcompact

针对日志、文件内容等局部大对象进行裁剪、摘要或外置，而不是总结整个会话。

#### Context Collapse

把较老的连续历史折叠成更紧凑的投影，同时保留近期上下文。

#### Auto Compact

接近窗口上限时，调用模型生成结构化摘要，用摘要替换较老历史。这是成本更高、信息损失也更大的
最后手段。

整体原则是：

```text
局部、便宜、确定性的压缩优先
→ 全局、有损、需要模型的总结最后使用
```

### 3. 轮间压缩和轮内压缩

- **轮间压缩**：用户下一次发送消息前，压缩过去的完整轮次。
- **轮内压缩**：同一轮 Agent 连续调用很多工具时，在下一次模型请求前压缩。

Coding Agent 更容易在单轮内被超大工具输出撑满，因此只做轮间压缩不够。

### 4. Prompt Cache 与压缩

稳定前缀越少变化，越容易命中 Prompt Cache。工程上应尽量：

- 保持稳定规则和工具定义的顺序、字节内容不变。
- 把易变状态放在后部。
- 不因一条通知重写整段 System Prompt。
- 优先清理旧工具结果，而不是频繁重写所有历史。

---

## 七、子 Agent：用独立上下文换取隔离和并行

### 1. 子 Agent 解决什么

主 Agent 如果亲自读取几十个文件，探索过程会永久占据主上下文。子 Agent 可以：

```text
主 Agent 给出清晰任务
→ 子 Agent 使用独立上下文探索
→ 只把结论、证据和文件位置返回
→ 主 Agent 继续决策
```

适合委派：

- 大范围代码搜索。
- 单个 CI 失败调查。
- 专项安全审查。
- 独立方案比较。
- 可并行、低耦合的子任务。

不适合委派：

- 读取一个明确文件。
- 修改一行代码。
- 强依赖主会话所有隐含上下文的任务。
- 多个 Agent 同时修改同一工作区而没有隔离。

### 2. 上下文与能力边界

子 Agent 不应无条件复制主 Agent 的全部状态。需要明确：

```text
共享：任务目标、必要文件、只读服务、统一规则
继承：部分模型配置、权限上限、项目约束
隔离：消息历史、临时推理、Todo、取消信号
限制：工具白名单、最大轮数、费用和运行时间
```

写任务最好使用独立 Git Worktree，避免并行编辑互相覆盖。

### 3. 返回值不是聊天全文

主 Agent 通常只需要：

- 结论。
- 证据和文件位置。
- 已执行的验证。
- 未解决风险。
- 如果发生写入，提供可审阅的 diff 或独立分支。

把子 Agent 全部中间过程回填主上下文，会失去隔离带来的主要收益。

---

## 八、后台任务：把“运行”与“等待”分离

后台任务和子 Agent 是两个维度：

- **后台 Shell**：确定性进程在运行，例如测试、构建、数据处理。
- **后台子 Agent**：另一个模型上下文在自主执行多步任务。

一个可靠的后台任务至少包含：

```text
Task ID
Owner Session / Agent
状态：queued / running / completed / failed / cancelled
启动时间和截止时间
进程或 Agent 标识
结构化结果
完整日志引用
取消方法
```

### 1. 不要让主 Loop 忙等

错误方式：

```text
启动任务 → Agent 每隔几秒查询一次 → 没结束继续查询
```

这会浪费模型调用和 token。更合理的是：

```text
启动后台任务
→ 主 Agent 继续其他工作或结束当前轮
→ 任务完成后产生通知事件
→ 下一次合适的模型调用把结果注入上下文
```

### 2. 生命周期问题

后台任务必须考虑：

- 主会话退出后任务是否继续。
- 服务重启后如何恢复任务状态。
- 如何避免孤儿进程。
- 日志如何限流和外置。
- 完成通知是否重复投递。
- 取消时如何终止整个进程树。
- 后台任务触发危险操作时由谁审批。

因此后台任务不是简单地在命令末尾加 `&`，而是一套任务状态和通知机制。

---

## 九、六个系统如何协作

完整链路可以描述为：

```text
1. Context Builder 读取规则、历史、Todo 和通知
2. Compactor 按预算裁剪旧内容和大工具结果
3. Agent Loop 调用模型
4. 模型选择工具或委派子 Agent
5. Tool Registry 校验并解析调用
6. Permission Pipeline 推导 Effect
7. 安全操作直接执行，高风险操作创建审批 checkpoint
8. 长操作进入后台任务，完成后发布通知
9. 工具结果作为 Observation 写入 Session
10. Loop 继续，直到完成或达到停止条件
```

任何一个模块单独做好都不够。例如：

- 有工具但没有权限，会把模型错误放大成真实破坏。
- 有压缩但没有工具结果外置，一次大日志仍能击穿窗口。
- 有子 Agent 但没有隔离，并行写文件会互相覆盖。
- 有后台进程但没有任务状态，断线后就无法追踪。
- 有审批但没有 checkpoint，批准后无法安全续跑。

---

## 十、Claude Code 与 DSH/Cordis 的侧重点

二者都在解决 Agent Harness 工程，但切入点不同。

### Claude Code 暴露出的重点

- 一个稳定的模型—工具主循环。
- Loop 外围有大量确定性基础设施。
- 对本地工具执行进行细粒度权限控制。
- 使用分级压缩支撑长会话。
- 使用子 Agent 隔离探索上下文。
- 使用后台任务避免阻塞主会话。

### DSH/Cordis 的重点

- Everything is a Plugin。
- 模型适配器、工具、Session、Agent Loop 都可替换。
- 时间可组合性：插件卸载时撤销被追踪的副作用。
- 空间可组合性：声明依赖，随 Provider 变化响应式启停。

可以概括为：

> Claude Code 的公开分析更适合学习“成熟 Coding Agent 需要哪些外围工程”；  
> DSH/Cordis 更适合学习“这些能力如何动态插件化并管理生命周期”。

二者存在相似问题域，不足以证明一方复制另一方。

---

## 十一、对 LingCoWork 的直接启发

结合当前项目已有的流式事件、工具、审批、上下文压缩和子 Agent，可以重点补强：

1. **轮内上下文预算**：每次模型调用前重新估算，而不是只在新用户轮次压缩。
2. **大工具结果外置**：完整内容持久化，上下文保留摘要、范围和引用。
3. **统一工具执行管线**：Schema、Effect、Policy、审批、执行和 Observation 不分散实现。
4. **可恢复审批**：保证暂停、刷新和服务重启后能够继续同一次 run。
5. **子 Agent 预算与隔离**：限制工具、step、token、时间以及写入范围。
6. **后台任务模型**：任务状态、日志引用、完成通知、取消和孤儿清理。
7. **Prompt Cache 纪律**：稳定前缀与动态提醒分离。
8. **结构化停止原因**：完成、等待用户、达到预算、取消和不可恢复失败要能区分。

优先级建议：

```text
工具结果限流与外置
→ 轮内压缩
→ 后台任务状态机
→ 子 Agent / Worktree 隔离
→ 更完整的恢复与成本观测
```

---

## 十二、推荐阅读与源码学习路线

### 官方资料

- [Claude Code：Subagents](https://docs.anthropic.com/en/docs/claude-code/sub-agents)
- [Claude Code：Hooks Guide](https://docs.anthropic.com/en/docs/claude-code/hooks-guide)
- [Claude Code：Memory](https://docs.anthropic.com/en/docs/claude-code/memory)
- [Claude Agent SDK](https://docs.anthropic.com/en/docs/claude-code/sdk)

### 架构分析

- [VILA-Lab：Dive into Claude Code](https://github.com/VILA-Lab/Dive-into-Claude-Code)
- [论文：Dive into Claude Code](https://arxiv.org/html/2604.14228v2)
- [Inside Claude Code：Context Compaction](https://y-agent.github.io/inside-claude-code/04-context-compaction.html)
- [decode-claude-code-analysis](https://github.com/0xE1337/decode-claude-code-analysis)

### 可运行的教学实现与开源 Harness

- [learn-claude-code](https://github.com/shareAI-lab/learn-claude-code)：由最小 Loop 逐步添加完整机制。
- [agent-harness-lab](https://github.com/mothieras/agent-harness-lab)：约 4000 行 TypeScript 教学 Harness。
- [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness)：基于 Cordis 的插件化 Harness。

建议阅读顺序：

```text
最小 Agent Loop
→ 工具协议与 Tool Registry
→ 权限与审批
→ 工具结果裁剪和上下文压缩
→ 子 Agent
→ 后台任务
→ Session 恢复
→ Cordis 插件生命周期
```

---

## 十三、面试时可以直接说

> 我看 Coding Agent 时不会只关注模型调用。它的核心 Agent Loop 本质上是组装上下文、调用模型、
> 执行工具、回填结果再继续的 ReAct 循环，真正复杂的是外围确定性系统。工具需要 Schema 校验、
> Effect 推导、权限审批、沙箱和结果限流；上下文需要从局部工具结果裁剪到全局摘要的分级压缩；
> 子 Agent 用独立上下文隔离长探索，后台任务用状态机和完成通知避免主 Agent 轮询；整个过程还要有
> 流式事件、checkpoint、取消和恢复。我的理解是模型负责判断下一步，Harness 负责确保每一步能够
> 安全、可控、可恢复地真正执行。
