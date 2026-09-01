# Coding Agent 怎么设计才好

> 参考 Cursor、Claude Code、Codex 等；对照 LingCoWork 已做与缺口。  
> 实现细节见 `docs/review_2/`。

---

## 一、边界：Coding Agent 交付什么

| 用户目标 | 应交付 |
|----------|--------|
| 改 bug / 加功能 | **可合并的 diff**，不是长文 |
| 理解陌生仓库 | **准确定位** + 可读结论 |
| 长任务 | **可恢复、可审计、可打断** |
| 危险操作 | **可控**，不能静默删库 |

共识：**代码仓库是 ground truth**；Agent = 带工具的 ReAct 循环，不是全库向量问答。

---

## 二、业界参考（各强在哪）

### Cursor

- IDE 内嵌：Composer、diff 预览、接受/拒绝
- 上下文：`@file` / rules；**grep 搜代码**为主
- MCP 外挂；Cloud Agent 后台任务

**启示**：diff + 规则 + 精确检索，人机协作优先。

### Claude Code

- 终端优先；工具极简：Read / Edit / Grep / Glob / Bash / Task
- 权限分级；`CLAUDE.md`；子 agent 探索 + 主会话压缩

**启示**：工具少而强；长探索交给 sub-agent。

### Codex / 云端 Coding Agent

- 隔离沙箱；Git/PR/CI 闭环

**启示**：环境可复制 + 自动验证才敢放权。

---

## 三、推荐分层架构

```
┌─────────────────────────────────────────┐
│  UX：流式、diff、审批、checkpoint、恢复   │
├─────────────────────────────────────────┤
│  编排：ReAct + Supervisor / 子任务       │
├─────────────────────────────────────────┤
│  上下文：规则 + grep + 压缩 + 外置状态   │
├─────────────────────────────────────────┤
│  工具：fs / shell / grep / git / MCP     │
├─────────────────────────────────────────┤
│  安全：effect 分类 + 审批 + 硬阻断        │
├─────────────────────────────────────────┤
│  验证：test / lint / typecheck / CI      │
└─────────────────────────────────────────┘
```

---

## 四、各层设计要点

### 1. 上下文

**Do**

- 项目规则：`AGENTS.md`、`.cursor/rules`
- 按需检索：**glob → grep → read**；RAG 只服务文档/题库
- 双轨记忆：对话摘要（有损）+ 工作区文件（无损，`read_file`）
- **轮间 + 轮内**压缩：跨轮摘要；单轮 ReAct 在每次调模型前压（LingCoWork 轮间已有，轮内为缺口）

**Don't**

- 把整仓 embedding 当主检索路径
- 只靠 chat history 记结构

### 2. 工具

| 族 | 用途 |
|----|------|
| 读 | `read_file`（offset/limit） |
| 写 | 精确 patch，少整文件重写 |
| 搜 | grep（内容）+ glob（路径） |
| 执行 | shell（输出上限） |
| 扩展 | MCP |
| 委派 | sub-agent |

工具描述 = 路由表（见 supervisor 里 `job_search` 唯一入口）。

### 3. 安全

| 层级 | 做法 |
|------|------|
| 硬阻断 | `rm -rf`、force push 等 |
| 按 effect 审批 | 写盘、shell、网络 |
| interrupt + checkpoint | 等人决策后续跑 |
| 沙箱 | 容器 / preload 白名单 IPC |

### 4. 编排

| 场景 | 模式 |
|------|------|
| 小改 | 单 Agent ReAct |
| 专项长任务 | Supervisor + sub-agent as tool |
| 强质检长报告 | Plan → Execute → Evaluate |
| 跨组织 | A2A（少数） |

默认 **Supervisor**，不是群聊多 Agent。

### 5. 验证

```
改代码 → test / lint → 错误回 context → 再改
```

成功标准：**能 merge**，不只是读起来对。

### 6. UX

- 流式 SSE、工具卡、thinking
- Diff 预览（IDE 产品核心）
- 可中断/恢复（审批、checkpoint）
- tool error 进 observation，不整 run 崩

---

## 五、与 Chatbot 的差异

| | Chatbot | Coding Agent |
|---|---------|--------------|
| 真相 | 模型 + RAG | **Git 工作区** |
| 检索 | 向量为主 | **grep 为主** |
| 输出 | 自然语言 | **patch** |
| 风险 | 幻觉 | **删文件、恶意命令** |

---

## 六、LingCoWork 对照（简表）

| 能力 | 状态 | review_2 |
|------|------|----------|
| ReAct + 工具 | ✅ | `01-overview-architecture.md` |
| Supervisor + sub-agent | ✅ | `01-overview-architecture.md` |
| 审批 + effect | ✅ | `03-approval.md` |
| 轮间上下文压缩 | ✅ | `04-context-compression.md` |
| 轮内压缩 | ❌ 缺口 | `04` §十一 |
| Hybrid RAG（题库） | ✅ | `internal/rag/`（已从简历撤下） |
| IDE 级 diff UX | 弱（Electron 壳） | — |
| 沙箱容器 | 未做 | `05` §六 Electron |

---

## 七、MVP → 进阶路线

**MVP**：ReAct + read/grep/edit/shell + AGENTS.md + 审批 + 流式 + MaxStep

**进阶**：sub-agent、轮内压缩、MCP、git/PR、沙箱 + CI 闭环

---

## 八、30 秒口述版

> 好的 Coding Agent 以仓库为真相源，grep/glob 定位，少量工具 ReAct；规则用 AGENTS.md；长上下文靠摘要 + 文件外置；危险操作 effect 审批。编排默认单 Agent，长探索用 sub-agent。验证靠 test/CI 闭环；体验上流式、可审批、可恢复。Cursor 强 IDE diff，Claude Code 强终端与子任务，Codex 强沙箱与 PR——按场景取长，不堆功能。
