// Package runtimectx 每轮 agent 运行前动态注入的"运行时上下文"。
//
// 目的：明确告诉 LLM 当前会话绑定的是用户选择的哪个文件夹。选择目录是 UI 行为，
// Agent 不会自行创建或切换 workspace。
//
// 当前只注入 workspace 信息；其他运行时事实（时间、附件历史、embedding 模型等）
// 按需再加。
package runtimectx

import (
	"context"
	"fmt"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// WorkspaceInfo 是当前会话绑定的 workspace 元信息。nil 表示未绑定。
type WorkspaceInfo struct {
	ProjectID string // 数据库 project id
	Name      string // 人类可读名
	AbsPath   string // 磁盘绝对路径
}

// LoadWorkspaceInfo 从 ctx 里拿 conversation id，查 DB 得到当前 workspace 状态。
// 未绑定 / 查询失败 / 找不到 project 都返回 nil，让调用方走"未绑定"分支。
// 出错不 panic 不 log，因为这只是"增强上下文"，失败降级不阻塞主流程。
func LoadWorkspaceInfo(
	ctx context.Context,
	convRepo *repository.ConversationRepo,
	projectRepo *repository.ProjectRepo,
) *WorkspaceInfo {
	if convRepo == nil || projectRepo == nil {
		return nil
	}
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return nil
	}
	conv, err := convRepo.Get(ctx, convID)
	if err != nil || conv == nil {
		return nil
	}
	if conv.ProjectID == nil || *conv.ProjectID == "" {
		return nil
	}
	project, err := projectRepo.Get(ctx, *conv.ProjectID)
	if err != nil || project == nil {
		return nil
	}
	return &WorkspaceInfo{
		ProjectID: project.ID,
		Name:      project.Name,
		AbsPath:   project.Workspace,
	}
}

// RenderSnippet 生成拼进 instruction 的 markdown 片段。
// 无论绑定与否都会返回非空字符串。
func RenderSnippet(ws *WorkspaceInfo) string {
	if ws == nil {
		return unboundSnippet
	}
	return fmt.Sprintf(boundTemplate, ws.Name, ws.ProjectID, ws.AbsPath)
}

const boundTemplate = `## 运行时上下文

当前会话已绑定工作区：
- 名称：%s
- Project ID：%s
- 路径：%s

规则：
- 文件读写优先使用相对路径，例如 reports/resume_report.md。
- 写入/修改工具会作用于上述工作区。`

const unboundSnippet = `## 运行时上下文

当前会话未绑定工作区。

规则：
- 不要自行选择、创建或猜测工作区路径。
- 请用户先在 LingCoWork 中选择一个文件夹，再执行文件写入或命令。`
