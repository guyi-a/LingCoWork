# Coding Agent 设计讨论

> 与 `docs/review_2/`（LingCoWork 项目实现复盘）并列，本目录聚焦 **Coding Agent 产品与架构设计**——  
> 参考 Cursor、Claude Code、Codex 等，结合 LingCoWork / klingwork 实践，逐题展开。

## 和 review_2 的分工

| 目录 | 侧重 |
|------|------|
| **`docs/review_2/`** | 本仓库 **已实现** 的架构、代码路径、面试追问与项目 bullet 对齐 |
| **`docs/coding/`** | **通用设计**、业界对比、取舍讨论；可引用 review_2，但不重复贴实现细节 |

## 文档清单

| # | 文件 | 内容 |
|---|------|------|
| 00 | [00-index.md](00-index.md) | 本页：目录与用法 |
| 01 | [01-coding-agent-design.md](01-coding-agent-design.md) | 分层架构：上下文 / 工具 / 安全 / 编排 / 验证 / UX |
| 02 | [02-interview-qa-log.md](02-interview-qa-log.md) | 面试/讨论题流水账（grep vs RAG、多 Agent、上下文压缩等） |
| 03 | [03-lingcowork-coding-roadmap.md](03-lingcowork-coding-roadmap.md) | LingCoWork 转向 Coding 场景的 Workspace、工具、Diff、页面与分阶段发布路线 |
| 04 | [04-cursor-coding-agent.md](04-cursor-coding-agent.md) | Cursor 公开的 Agent、Plan、Todo、Explore、Diff、安全与界面机制，以及 LingCoWork 可借鉴边界 |
| 06 | [06-claude-code-agent-engineering.md](06-claude-code-agent-engineering.md) | Claude Code 的 Agent Loop、工具、权限、上下文压缩、子 Agent、后台任务与开源学习路线 |

## 怎么用

1. 新话题：在 `02-interview-qa-log.md` 记题号 + 一句话结论，展开写新 `0x-*.md` 或并入已有篇  
2. 和 LingCoWork 实现对齐：链到 `docs/review_2/` 对应章节（如 `02-streaming.md`、`03-approval.md`、`04-context-compression.md`）  
3. 实习项目对比：链到 `docs/review/` 与 `/Users/guyi/klingwork-app`

## 关联

- LingCoWork 实现笔记：`docs/review_2/`
- klingwork 实习笔记：`docs/review/`
- 面试题清单：`docs/review_2/00-common-questions.md`
