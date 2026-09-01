# 讨论 / 面试题流水账（Coding Agent）

> 只列题 + 指向展开；详细答法在本文或独立 `0x-*.md`。  
> 与 `docs/review_2/00-common-questions.md` 互补（那边偏 LingCoWork 项目清单）。

---

## 已讨论（可回溯聊天或下表）

| # | 题目 | 结论一句话 | 展开 |
|---|------|------------|------|
| — | grep/glob vs RAG | 代码用 grep，文档用 RAG | `review_2/05` §十三 |
| — | DeepWiki | 读的外脑，不是规范源 | `review_2/05` §十二 |
| — | 多 Agent：Supervisor vs A2A | 默认监管者；A2A 跨系统 | 本文档目录待补 `03-multi-agent.md` |
| — | Plan-Execute-Evaluate | 三角色：规划/执行/评估 | 同上 |
| — | Agent 模式除 ReAct | CoT、Reflection、ToT、RAG、HITL | `01-coding-agent-design.md` |
| — | 工具结果如何进上下文 | role=tool + tool_call_id；DB 投影 | 聊天整理 |
| — | 每次 tool 是否再打 LLM | 是，ReAct 每步一次请求 | MaxStep 限图步数 |
| — | 轮间 vs 轮内压缩 | 轮间治本，轮内救急单轮多步 | `review_2/04` §十一 |
| — | 压缩不动历史，摘要放哪 | compactions 表；合成 user 消息在投影最前 | `review_2/04` |
| — | 压缩阈值 | 默认估算 ≥ **96800** token | `review_2/04` §三 |
| — | 摘要也不行怎么办 | 外置+re-read、轮内压、拆会话 | 聊天整理 |
| — | KNN / ANN | 暴力 cosine topK vs 近似索引 | `review_2/03` |
| — | 知识库更新时机 | 源数据写入后异步增量 reindex | 聊天整理 |
| — | Coding Agent 整体设计 | 六层架构 + 业界对比 | [01-coding-agent-design.md](01-coding-agent-design.md) |
| — | LangChain / LangGraph / LlamaIndex 对照 | 高/低层编排 + 数据层；eino 三层都覆盖 | 聊天整理（JD 相关） |
| — | 沙箱方案全景 | 进程级（内核原语）vs 容器 vs 行为层策略 | `../review_2/10-sandbox.md` |
| — | Claude Code Agent 工程 | Loop 很小，复杂度集中在工具、权限、压缩、子 Agent 和后台任务 | [06-claude-code-agent-engineering.md](06-claude-code-agent-engineering.md) |

---

## 待写专篇（占位）

- `03-multi-agent.md` — Supervisor / A2A / Plan-Execute-Evaluate 选型
- `04-context-strategies.md` — 轮间/轮内/外置/RAG 统一图景
- `05-tooling-and-safety.md` — 工具面、effect、审批、沙箱对比 Cursor/CC
