# LingCoWork 向 Coding 场景演进路线

> 状态：Phase 0–2 已形成最小 Coding 闭环；2026-09-01 完成 Coding Alpha 阶段一的
> `AGENTS.md`、显式上下文、验证闸门与 Eval Harness。  
> 原则：先在开发版中持续自用，形成可靠闭环后再发布新版本；各阶段仅在通过验收后标记为已支持。

## 一、目标

让 LingCoWork 不只在应用创建的 `.workspace/` 中生成文档，还能由用户选择一个已有代码仓库，
把它作为当前会话的工作区，由主 Agent 直接完成：

```text
打开仓库
→ 理解需求
→ 搜索和阅读代码
→ 修改文件
→ 查看 Diff
→ 运行格式化、测试和构建
→ 根据错误继续修复
→ 用户审阅最终变更
```

首个真实目标就是自举：

> 用稳定版 LingCoWork 打开 `/Users/guyi/LingCoWork`，让它在安全边界内修改自己的源码，
> 并通过测试和 Diff 验证结果。

Coding 作为主 Agent 的横向能力，不新增一个负责完整任务的“Coding Agent”。小中型任务仍由
主 Agent 通过 ReAct 完成搜索、修改和验证；同时提供一个**受限探测型 Explore 子 Agent**，专门
承担大范围代码库探索。Explore 可以读取 Workspace 并运行受限只读探测命令，只把结构化结论返回
主 Agent；最终修改权、测试与任务责任仍留在主 Agent。

## 二、Workspace 机制调整

### 1. 统一模型：Project 就是用户选中的文件夹

不再把 Workspace 分成“Agent 创建的工作区”和“用户打开的外部工作区”两套产品机制。
统一语义为：

> 用户先选择一个文件夹作为 Project Workspace，Conversation 绑定 Project，所有文件与命令工具
> 都以这个文件夹为根目录。

当前 `.workspace/<slug>/` 下已经存在的几个 Project 不需要迁移，也不会失效。它们在语义上
等同于用户以前已经选中了这些文件夹；数据库中现有的 `projects.workspace` 本来保存的就是
绝对路径。

新建项目时必须通过系统目录选择器选定文件夹。用户如果需要一个空工作区，可以在系统目录
选择器中创建新文件夹后再选中，不再让 Agent 在对话执行到一半时调用 `create_workspace`
决定磁盘位置。

`create_workspace` 后续从主 Agent 工具集中退出，相关提示词改为“未选择 Workspace 时引导
用户先选择文件夹”。为兼容已有会话和数据，旧 Project 记录继续正常读取。

### 2. 参考 klingwork-app 的选择与绑定链路

klingwork-app 已经采用“目录路径就是 Workspace”的模型：

```text
Renderer 点击选择目录
→ Preload 调用专用 pick-directory IPC
→ Electron showOpenDialog(openDirectory/createDirectory)
→ Runtime 对路径做 realpath、绝对路径和目录校验
→ SQLite 持久化 workspace.rootPath
→ Thread 通过 workspaceId 绑定目录
→ 文件工具从服务端按 threadId 解析权威根路径
```

LingCoWork 不照搬它的 Thread 导航，而是把同一思路接到现有 Project 模型：

```text
用户点击打开文件夹
→ Electron 弹出系统目录选择器
→ 后端校验目录存在、可访问并解析真实路径
→ 按规范化路径查找或创建 Project
→ 会话绑定该 Project
→ 主 Agent 每轮收到仓库路径、Git 状态和项目规则
```

这里 Electron 只负责调用系统选择器，路径校验、去重、持久化和工具边界必须由 Go 后端负责。
Renderer 后续只传 `project_id`，不能在每次文件请求中自行指定根路径。

可参考 klingwork-app：

- `apps/desktop/src/windows/app/workspace-api.ts`：专用目录选择 IPC。
- `apps/desktop/src/main/index.ts`：`showOpenDialog`。
- `packages/app-runtime/src/app-runtime.ts`：路径规范化和 Workspace provision。
- `packages/database/src/thread-persistence.ts`：按规范化路径去重并持久化绑定。

建议同时支持：

- 最近打开的仓库
- 在 Finder 中显示
- 新建会话时选择已有 Project
- 当前会话需要换目录时，明确切换 Project，而不是静默修改路径
- 在项目标题中展示目录名、绝对路径和 Git 分支
- 非 Electron 浏览器模式下手动输入路径，但必须明确提示这是服务端机器上的路径

### 3. 数据与删除语义

- 所有 Project 都统一保存规范化后的绝对路径。
- 同一真实路径只对应一个 Project，多个 Conversation 可以共享。
- 删除 Conversation 只删除会话，不影响 Project 和磁盘目录。
- 删除 Project 只删除 LingCoWork 中的记录和会话，**不删除用户选中的文件夹**。
- 现有 `.workspace/<slug>/` 也按“已选中的文件夹”处理，不因删除 Project 自动删除磁盘内容。
- 解析符号链接后的写入目标仍必须位于仓库根目录，防止通过 symlink 越界。

## 三、主 Agent 的 Coding 工具面

原有 `read_file`、`run_command` 提供了基础能力，但距离稳定的 Coding 闭环还缺少搜索与
上下文 Patch 等工具。

### 1. 代码发现与检索

- `glob`：按路径模式查找文件，例如 `**/*.go`、`web/**/*.tsx`。
- `grep`：按文本或正则搜索代码，返回路径、行号和少量上下文。
- 搜索默认遵守 `.gitignore`，跳过 `.git`、`node_modules`、构建产物和二进制文件。
- 设置结果数、文件大小、耗时和目录遍历预算，避免一次搜索灌爆上下文或扫描超大目录。
- 后续可增加符号、定义和引用查找；第一版不依赖 LSP，也不把全仓向量检索当主路径。

推荐搜索顺序：

```text
glob 找候选文件 → grep 定位符号/文本 → read_file 分段读取
```

### 2. 精确编辑

- 新增 `apply_patch`，用带上下文的 Diff 表达新增、删除和修改，支持一个调用中修改多个位置，
  并返回每个 hunk 的应用结果。
- 修改前校验旧内容，文件已变化时拒绝盲目覆盖。
- 新建文件继续使用 `write_file`，避免整文件重写已有源码。
- 格式化由仓库自己的 formatter 完成，不在编辑工具中偷偷改变其他内容。

编辑工具分工：

```text
修改已有文本         → apply_patch
创建新文件或完整重写 → write_file
```

`apply_patch` 是 Coding 场景的主要编辑表达。`edit_file` 和 `edit_file_lines` 已在
2026-08-28 下线，避免模型在三种重叠编辑工具之间做无意义选择。

### 3. Git 上下文与操作

第一阶段不新增 `git_status`、`git_diff`、`git_log` 等专用 Agent 工具，避免和现有
`run_command` 重复。主 Agent 直接执行只读 Git 命令：

```text
git status --short
git diff -- <path>
git log --oneline -10
```

分支、上游分支、脏状态和 Changes/Diff 页面不依赖模型调用工具，由 Go 后端独立读取 Git 状态并
向前端提供结构化数据。这样 Agent 使用 Terminal 完成推理，页面使用稳定接口完成展示，职责分开。

`run_command` 当前对 stdout、stderr 各有 64 KiB 上限。第一版通过路径过滤和按需读取控制 Diff
大小；只有出现稳定的结构化、分页或超大 Diff 需求后，再考虑专用 Git 查询接口或工具。

`git add`、`git commit`、新建分支、worktree 和 PR 等写操作同样先复用 `run_command`。提交、
推送等外部写操作必须由用户明确要求，不能因为“任务完成”就自动执行。强制推送、
`reset --hard`、`clean -fd` 等继续硬阻断。

### 4. 执行与验证

现有 `run_command` 可以执行测试，但 Coding 体验还需要：

- 明确返回 exit code、耗时、stdout 和 stderr 是否被截断。
- 识别常见构建、测试、Lint 和类型检查结果。
- 将 `file:line:column` 错误变成可点击的诊断项。
- 支持格式化、单测、全量测试、构建四类验证状态。
- 长时间命令后续支持后台运行、取消、日志续读和进程退出通知。
- 不自动猜测危险的“修复命令”，安装依赖和执行项目脚本继续经过审批。

第一版不需要先做统一测试框架，只要能够可靠执行仓库已有命令，并把结果重新交给模型修复即可。

## 四、核心 Coding 提示词、Plan 模式与任务编排

### 1. 核心 Coding 提示词

Coding 已成为主 Agent 的核心横向能力，不能依赖模型先发现并加载 Skill。以下稳定纪律直接写入
General 系统提示词，领域专用流程才继续使用 Skill：

1. 修改前先查看 Git 状态，不能覆盖用户已有改动。
2. 小范围任务由主 Agent 搜索；位置未知或跨模块探索时委派 Explore。
3. 修改前重新读取相关实现、调用方与测试。
4. 局部修改优先 patch，不整文件重写，不处理需求之外的清理。
5. 修改后运行与风险相称的 formatter、test、lint 或 build。
6. 最后检查 Diff，确认没有生成物和无关文件。
7. 未经明确要求不提交、不推送、不创建 PR。

General 负责身份、通用工具纪律、Workspace 与 Coding Loop；Supervisor 只负责直接执行或选择
Explore、Deep Research、招聘、简历和出题等子 Agent。

### 2. TodoWrite

新增 `todo_write` 工具维护复杂任务的执行清单。每项至少包含：

- 稳定的 `id`
- `content`
- `status`：`pending`、`in_progress`、`completed`、`cancelled`
- 工具级 `merge`：增量合并现有列表或整体替换

同一时间最多一个任务处于 `in_progress`。Todo 不是模型写给用户看的临时 Markdown，而是可被
前端渲染、会话恢复和后续轮次继续更新的结构化状态。简单的一两步任务不强制创建 Todo，跨文件、
多阶段或需要多次验证的任务才使用。

Eino ADK 的 Deep Agent 已经带有 WriteTodos 相关能力，但当前通过 `WithoutWriteTodos` 关闭。
实施前先评估能否复用其数据结构和中间件；如果不能满足前端展示、持久化和主 Agent 调用要求，
再实现 LingCoWork 自己的 `todo_write`。

### 3. Plan 模式

参考 Cursor 增加显式 Plan 模式，但它不是一个独立子 Agent，而是主 Agent 的受限运行模式：

- 允许搜索、读取、只读 Git 查询和向用户提问。
- 禁止写文件以及有副作用的命令。
- 对需求、相关代码、风险和验证方式形成可执行计划。
- 用户点击“开始实施”后，在同一会话切换到 Agent 模式。
- 将已确认计划转换成 Todo，继续由主 Agent 实施。

Plan 模式解决“先想清楚再改”的权限和交互问题，TodoWrite 解决实施过程中的进度跟踪，二者不能
互相替代。第一版先支持手动选择模式，不依赖模型自动判断和切换。

### 4. Explore 子 Agent

面向 Coding 的 Explore 子 Agent 用于：

- 不确定实现位置时的大范围目录和关键词探索。
- 梳理跨文件调用链、模块边界和测试位置。
- 将关键文件、符号、调用关系、风险点和建议阅读顺序返回主 Agent。

边界：

- 开放 `glob`、`grep`、`read_file`、`list_files`、`file_info` 和受限 `run_command`。
- `run_command` 仅允许 Workspace 内的 Git/文件/版本探测；禁止重定向、外部路径、测试、构建和脚本。
- 不开放 `write_file`、`apply_patch`、浏览器、网络、MCP 和 Memory。
- 不接管完整任务，不直接向用户宣称修改完成。
- 主 Agent 根据 Explore 结论重新读取关键文件，再决定修改。
- 设置迭代、搜索和输出预算，避免子 Agent 反而放大上下文与资源消耗。

小任务由主 Agent 直接搜索，只有探索范围较大、位置未知或可并行调查时才委派，避免所有 Coding
请求都机械地增加一次子 Agent 调用。

### 5. 项目上下文

每轮可动态注入的项目上下文：

- Workspace 根路径
- Git 仓库根和当前分支
- 当前未提交变更摘要
- 工作区根目录 `AGENTS.md`（最多 16 KiB；不加载 `.cursor/rules`）
- 常用构建和测试命令
- 项目级 Memory 中记录的技术约定

代码以磁盘和 Git 状态为事实源。历史摘要只能帮助回忆任务，不能替代重新读取当前文件。

## 五、页面与交互

### 1. 打开项目

首页通过输入框工具栏提供“选择工作区”入口；已打开项目统一放在侧栏，不在首页重复展示
“最近的工作区”。进入会话后，顶部展示：

```text
项目名 / 当前分支 / 修改文件数 / 测试状态
```

用户应始终知道 Agent 当前操作的是哪个真实目录，避免把命令跑错仓库。

### 2. Workspace 面板升级

现有右侧 Workspace 面板从单一文件树扩展为：

- **Files**：仓库文件树和文件预览
- **Changes**：本轮或当前 Git 工作区的变更列表
- **Problems**：测试、编译、Lint、类型检查诊断
- **Terminal**：后续阶段展示命令和持续输出

大型仓库的文件树应：

- 遵守 `.gitignore`
- 默认折叠 `.git`、依赖和构建目录
- 按需加载子目录，不一次遍历全仓
- 支持按文件名快速过滤

### 3. Diff 体验

Diff 是 Coding 版本最重要的页面能力之一：

- 展示新增、删除和修改文件。
- 支持 unified 与左右分栏两种视图。
- 显示行号、增加/删除统计和语法高亮。
- Agent 工具卡中的文件路径可以直接跳转到对应 Diff。
- 支持按本轮变更与全部未提交变更切换，避免把用户原有修改误算成 Agent 产生的修改。

必须区分两个阶段：

#### 第一版：写入后审阅

Agent 直接修改真实工作区，页面读取 Git Diff 展示结果。用户可以继续让 Agent 修正，但暂不提供
“拒绝单个 hunk”，因为安全回滚需要知道哪些修改属于 Agent，不能粗暴覆盖用户原有改动。

#### 后续：写入前暂存

写工具先生成 patch，用户可以接受或拒绝文件/hunk，接受后再落盘。这需要新增 patch staging、
基线快照和冲突检测，不能只在前端放两个按钮假装支持。

### 4. 工具卡和审批

- 搜索工具卡展示查询词、命中文件数和截断信息。
- 编辑工具卡展示目标文件、hunk 数，并提供“查看 Diff”。
- 命令工具卡展示命令、工作目录、耗时、退出码和日志。
- 审批卡不仅显示工具名，还要显示真实 effect、路径和命令风险。
- 测试失败属于 observation，应回到 Agent 继续修复，而不是让整轮对话崩溃。

## 六、安全模型

选择真实仓库后，Agent 的写入影响从“应用生成目录”升级为“用户源码”，需要更严格的边界。

### 1. 文件边界

- 所有相对路径以选中的仓库根解析。
- 写入、移动和删除必须限制在仓库根内。
- 解析 symlink 后再次检查边界。
- `.git` 内部对象、系统目录和应用运行数据默认禁止直接修改。
- `.env`、证书、私钥、凭证配置默认视为敏感文件，读取和修改提高审批等级。

### 2. 命令边界

- `cwd` 必须位于当前仓库。
- 保留 effect 审批与破坏性命令硬阻断。
- 下载并执行脚本、安装依赖、网络请求、修改 Git 历史分别展示风险。
- 搜索和测试设置时间、输出、进程数量及目录遍历上限。
- Agent 停止、超时或应用退出时继续清理整个进程组。

### 3. 用户已有修改

打开仓库时记录初始 Git 状态：

- 不覆盖未知的未提交修改。
- 修改同一文件前重新读取，避免基于旧版本应用 patch。
- 记录 Agent 首次修改每个文件前的内容，形成“本轮变更基线”；第一版先用于区分和审阅，后续再
  发展为可恢复的 Checkpoint。
- 总结时区分“进入项目前已有的变更”和“本轮 Agent 产生的变更”。
- 未来使用 worktree 隔离长任务，但第一版不强制要求干净工作区。

## 七、长任务与上下文

Coding 任务容易在单轮内连续搜索、读取、修改和测试，现有轮间压缩不足以处理单轮上下文膨胀。
后续需要：

- 工具输出裁剪：搜索只保留关键命中，构建日志只保留错误附近内容。
- 将大日志写入文件，模型按需分段读取。
- 单轮内压缩或状态外置，保留目标、已改文件、验证结果和未完成项。
- 大范围探索交给只读 Explore 子 Agent，主 Agent 只接收结构化结论。
- checkpoint 中保留当前计划和验证状态，应用重启后不重复执行有副作用的步骤。

LingCoWork 当前已经具备前端停止按钮、后端 Run 取消以及 `run_command` 进程组清理，不需要重新
发明基础取消链路。Coding 阶段需要补的是：

- 明确 `Stop` 只停止后续执行，不自动回滚已经写入磁盘的修改；文件恢复由 Checkpoint 或 Git
  负责。
- 审计 MCP、浏览器和新增工具是否都响应 `ctx.Done()`。
- 停止时统一清理待审批、待提问和工具卡状态。
- 后台命令和未来并行子 Agent 支持独立取消。
- 页面刷新或应用重启后，能够区分“已取消”“执行失败”和“进程失联”。

## 八、实施阶段

### Phase 0：统一的文件夹 Workspace

**状态：已完成。**

- 新增 Electron 专用目录选择 IPC。
- Go 后端完成路径规范化、去重并注册 Project。
- 新建项目必须先选择文件夹，Conversation 再绑定 `project_id`。
- 下线主 Agent 的 `create_workspace` 工具及相关自动创建提示。
- 兼容现有 `.workspace/<slug>` Project，不要求重新选择。
- 删除 Project 改为只删除应用记录，不删除磁盘目录。
- 完成路径规范化、symlink 和目录安全测试。

验收：能够安全打开 LingCoWork 仓库，所有文件工具和 `run_command` 均以仓库为根。

### Phase 1：最小 Coding 闭环

**状态：已完成最小闭环；真实 Agent 任务通过率仍需持续积累。**

- [x] `glob`、`grep`
- [x] `apply_patch`
- 通过 `run_command` 完成 Git 状态、Diff 和历史查询
- [x] 主 Agent 核心 Coding Loop 系统提示词
- [x] 结构化 `todo_write`
- [x] 受限探测型 Explore 子 Agent
- [x] 本轮 Agent 变更基线
- [x] 顶部展示真实路径、Git 分支和脏状态
- [x] 修改后格式化、测试、构建的基本循环
- [x] 工具卡展示搜索/Patch 结果并跳转到文件；`grep` 可定位和高亮命中行
- [x] Composer 通过 `@` 选择 Workspace 文件/文件夹，并复用附件 marker 管线
- [x] 根目录 `AGENTS.md` 按会话 Workspace 注入

验收：主 Agent 能独立完成一个小 Bug 修复，并给出范围正确、测试通过的 Diff。

### Phase 2：Changes 与 Diff 页面

**状态：进行中。Files / Diff、Git 全部变更、本轮 Agent 基线与交互式 PTY Terminal 已完成。**

- [x] Workspace 的 Files / Diff / Terminal 标签
- [x] 单文件和多文件 Diff
- [x] 本轮 Agent 变更与全部 Git 变更区分
- [x] 测试结果和诊断跳转
- [x] 编辑、命令工具卡与 Diff / Terminal 联动
- [x] Todo 进度面板

验收：用户不看终端也能理解 Agent 改了哪些文件、为什么改、验证是否通过。

### Phase 3：可靠执行

- [x] Plan 模式以及“开始实施”交接（手动选择、内联编辑、同 checkpoint 恢复）
- [x] assistant/tool 消息边界增量落库与启动状态 reconciliation
- 长命令后台运行与日志续读
- [x] Problems 面板
- [x] 代码修改后的 validation completion gate：未验证/失败时继续 ReAct，相同失败限次停转
- [x] Coding Eval Harness：临时 worktree、10 项 smoke baseline、30 项任务 catalog 与 JSONL 台账
- Checkpoint 预览与恢复
- 轮内上下文治理
- 全工具取消审计、并行任务取消和重启恢复
- 更细的敏感文件与命令策略

验收：中等规模跨文件任务不会因为日志过长、页面刷新或应用重启而失控。

### Phase 4：隔离与协作

- Git worktree / 临时分支
- patch staging 与文件/hunk 接受、拒绝
- 结构化 LSP diagnostics
- commit、push、PR、CI 闭环
- 可选沙箱或受限执行环境

这部分不是首版 Coding 能力的前置条件，只有自用证明需要后再实施。

## 九、新版本发布门槛

先在开发模式下持续使用，不因完成某个页面就立即发布。满足以下条件后再打 Coding Preview：

1. 可以从系统目录选择器打开已有 Git 仓库。
2. reference driver 的 10 项 smoke 全绿；真实 Agent driver 至少完成 10 个小型开发任务，
   未发生工作区外误写。
3. 能稳定执行“搜索、阅读、修改、测试、Diff、总结”完整闭环。
4. 用户已有未提交修改不会被覆盖或错误回滚。
5. Diff 页面能准确展示 Agent 产生的变化。
6. 写文件、执行命令和敏感文件访问的审批信息清楚。
7. 页面刷新和应用重启后项目绑定仍然存在。
8. 使用 LingCoWork 修改自身代码并通过 Go、Web、Electron 的相关构建测试。
9. 打包应用完成一次独立冷启动和自举冒烟验证。

发布时建议先叫 **Coding Preview**，不承诺完整 IDE 替代。连续自用稳定后，再作为正式 Coding
能力写入 README 和简历。

## 十、第一批建议任务

按以下顺序逐步在开发版实现：

1. ~~统一 Workspace 语义并完成“选择文件夹 → 注册 Project → 绑定会话”链路。~~（已完成）
2. ~~仓库路径、分支和脏状态展示。~~（已完成）
3. ~~`glob`、`grep` 两个只读工具。~~（已完成）
4. ~~`apply_patch`，并下线重叠的 `edit_file`、`edit_file_lines`。~~（已完成）
5. ~~为编辑工具增加本轮变更基线，区分用户原有变更和 Agent 修改。~~（已完成）
6. ~~主 Agent 核心 Coding 提示词与最小“搜索 → 修改 → `run_command` 验证和检查 Git Diff”循环。~~（已完成）
7. ~~结构化 `todo_write` 及前端进度展示。~~（已完成）
8. ~~受限探测型 Explore 子 Agent。~~（已完成）
9. 使用 LingCoWork 自身完成至少一个真实 Bug 修复，并记录到 Coding Eval 台账。
10. ~~单独完成 Coding UI 详细设计，再实现 Changes 列表与 Diff 预览。~~（已完成）
11. ~~Plan 模式和“开始实施”交接。~~（已完成）
12. ~~测试结果结构化、Problems 与 validation completion gate。~~（已完成；Checkpoint 页面待做）

每完成一项都用 LingCoWork 仓库自身做真实任务验证。只有上一层形成闭环，再继续叠加下一层，
避免一次性把它改造成一个功能很多、但不敢真的让它写代码的“伪 IDE”。
