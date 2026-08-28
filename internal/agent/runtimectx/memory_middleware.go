package runtimectx

import (
	"context"
	"log"

	"github.com/cloudwego/eino/adk"

	"github.com/guyi-a/Interview-Agent/internal/memory"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// MemoryMiddleware 每轮运行开始时把两级长期记忆拼进 instruction。
//
// 挂载顺序有意排在 SkillsIndexMiddleware 之后、WorkspaceMiddleware 之前：提示词
// 缓存按前缀匹配，变动点越靠后被作废的前缀越少，而三者的变动频率是"技能索引 ≈
// 记忆 < 工作区状态"。
//
// 与 WorkspaceMiddleware 同一个模式：嵌 BaseChatModelAgentMiddleware 复用其余
// hook 的 no-op，只重写 BeforeAgent；所有 agent 共用一个实例，主 agent 和
// sub-agent 看到的记忆才一致。
type MemoryMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	registry    *memory.Registry
	userPath    string
	convRepo    *repository.ConversationRepo
	projectRepo *repository.ProjectRepo
}

func NewMemoryMiddleware(
	registry *memory.Registry,
	userPath string,
	convRepo *repository.ConversationRepo,
	projectRepo *repository.ProjectRepo,
) *MemoryMiddleware {
	return &MemoryMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		registry:                     registry,
		userPath:                     userPath,
		convRepo:                     convRepo,
		projectRepo:                  projectRepo,
	}
}

func (m *MemoryMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if m.registry == nil {
		return ctx, runCtx, nil
	}

	userContent := m.read(m.userPath, "user")

	// 未绑定工作区时 projectPath 为空，片段会说明项目记忆不可用。
	var projectPath string
	if ws := LoadWorkspaceInfo(ctx, m.convRepo, m.projectRepo); ws != nil {
		projectPath = memory.ProjectPath(ws.AbsPath)
	}
	projectContent := m.read(projectPath, "project")

	snippet := memory.RenderSnippet(userContent, projectContent, projectPath)
	if snippet == "" {
		return ctx, runCtx, nil
	}
	if runCtx.Instruction != "" {
		runCtx.Instruction = runCtx.Instruction + "\n\n" + snippet
	} else {
		runCtx.Instruction = snippet
	}
	return ctx, runCtx, nil
}

// read 读一级记忆。读失败按"这一级为空"继续 —— 记忆是上下文增强，掐死整轮运行
// 是更坏的结果。文件不存在在 Store 那层就已经不是错误了，能走到这里的是权限、
// 符号链接之类真正异常的情况，值得留一行日志。
func (m *MemoryMiddleware) read(path, level string) string {
	store := m.registry.For(path)
	if store == nil {
		return ""
	}
	doc, err := store.Read()
	if err != nil {
		log.Printf("memory: read %s level failed, continuing without it: %v", level, err)
		return ""
	}
	return doc.Content
}
