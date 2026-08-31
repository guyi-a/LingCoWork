package runtimectx

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/agent/prompts"
	"github.com/guyi-a/Interview-Agent/internal/agentmode"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// ModeMiddleware turns Plan into a real capability boundary at model-tool
// selection time. It runs after dynamic tools have been appended, so filtering
// applies to both builtins and MCP tools.
type ModeMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	convRepo *repository.ConversationRepo
}

func NewModeMiddleware(convRepo *repository.ConversationRepo) *ModeMiddleware {
	return &ModeMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		convRepo:                     convRepo,
	}
}

func (m *ModeMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if m.mode(ctx) != agentmode.Plan {
		return ctx, runCtx, nil
	}
	runCtx.Tools = filterTools(ctx, runCtx.Tools, agentmode.AllowsPlanTool)
	if runCtx.Instruction != "" {
		runCtx.Instruction += "\n\n" + prompts.Plan
	} else {
		runCtx.Instruction = prompts.Plan
	}
	return ctx, runCtx, nil
}

func filterTools(
	ctx context.Context,
	candidates []tool.BaseTool,
	keep func(name string) bool,
) []tool.BaseTool {
	filtered := make([]tool.BaseTool, 0, len(candidates))
	for _, candidate := range candidates {
		info, err := candidate.Info(ctx)
		if err != nil || info == nil || !keep(info.Name) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func (m *ModeMiddleware) mode(ctx context.Context) agentmode.Mode {
	if m == nil || m.convRepo == nil {
		return agentmode.Agent
	}
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return agentmode.Agent
	}
	conv, err := m.convRepo.Get(ctx, convID)
	if err != nil || conv == nil {
		return agentmode.Agent
	}
	mode, err := agentmode.Parse(conv.ChatMode)
	if err != nil {
		return agentmode.Agent
	}
	return mode
}
