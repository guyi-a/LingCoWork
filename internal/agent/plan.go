package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/agentmode"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

func withoutRootStateTools(
	ctx context.Context,
	all []tool.BaseTool,
) ([]tool.BaseTool, error) {
	out := make([]tool.BaseTool, 0, len(all))
	for _, candidate := range all {
		info, err := candidate.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info.Name == "create_plan" || info.Name == "todo_write" {
			continue
		}
		out = append(out, candidate)
	}
	return out, nil
}

// planGuard is the enforcement layer behind the model-facing tool filter.
// It intentionally runs before approval: Plan mode is a capability boundary,
// not a request that auto mode may approve away.
func planGuard(convRepo *repository.ConversationRepo) compose.ToolMiddleware {
	validate := func(ctx context.Context, input *compose.ToolInput) error {
		inPlanMode := conversationInPlanMode(ctx, convRepo)
		if !inPlanMode {
			if input != nil && input.Name == "create_plan" {
				interrupted, _, _ := tool.GetInterruptState[any](ctx)
				if !interrupted {
					return fmt.Errorf("create_plan is only available in Plan mode")
				}
			}
			return nil
		}
		if input == nil {
			return fmt.Errorf("Plan mode received nil tool input")
		}
		if !agentmode.AllowsPlanTool(input.Name) {
			return fmt.Errorf("Plan mode cannot call %s", input.Name)
		}
		if input.Name != "run_command" {
			return nil
		}
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(input.Arguments), &args); err != nil {
			return fmt.Errorf("Plan command arguments: %w", err)
		}
		if ok, reason := approval.IsExploreProbeCommand(args.Command); !ok {
			return fmt.Errorf("Plan command rejected: %s", reason)
		}
		return nil
	}

	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				if err := validate(ctx, input); err != nil {
					return nil, err
				}
				return next(ctx, input)
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				if err := validate(ctx, input); err != nil {
					return nil, err
				}
				return next(ctx, input)
			}
		},
	}
}

func conversationInPlanMode(
	ctx context.Context,
	convRepo *repository.ConversationRepo,
) bool {
	if convRepo == nil {
		return false
	}
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return false
	}
	conv, err := convRepo.Get(ctx, convID)
	if err != nil || conv == nil {
		return false
	}
	mode, err := agentmode.Parse(conv.ChatMode)
	return err == nil && mode == agentmode.Plan
}
