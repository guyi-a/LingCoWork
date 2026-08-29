package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/effect"
)

const exploreMaxIterations = 50

var exploreToolNames = map[string]struct{}{
	"glob":        {},
	"grep":        {},
	"read_file":   {},
	"list_files":  {},
	"file_info":   {},
	"run_command": {},
}

func selectExploreTools(
	ctx context.Context,
	all []tool.BaseTool,
) ([]tool.BaseTool, error) {
	selected := make([]tool.BaseTool, 0, len(exploreToolNames))
	found := make(map[string]struct{}, len(exploreToolNames))
	for _, candidate := range all {
		info, err := candidate.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("inspect tool for Explore: %w", err)
		}
		if _, ok := exploreToolNames[info.Name]; !ok {
			continue
		}
		selected = append(selected, candidate)
		found[info.Name] = struct{}{}
	}
	for name := range exploreToolNames {
		if _, ok := found[name]; !ok {
			return nil, fmt.Errorf("Explore requires missing tool %q", name)
		}
	}
	return selected, nil
}

func exploreGuard(registry *effect.Registry) compose.ToolMiddleware {
	validate := func(ctx context.Context, input *compose.ToolInput) error {
		if input == nil {
			return fmt.Errorf("Explore received nil tool input")
		}
		if _, ok := exploreToolNames[input.Name]; !ok {
			return fmt.Errorf("Explore cannot call %s", input.Name)
		}
		derived := registry.Derive(ctx, input.Name, input.Arguments)
		if input.Name == "run_command" {
			var args struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal([]byte(input.Arguments), &args); err != nil {
				return fmt.Errorf("Explore command arguments: %w", err)
			}
			if ok, reason := approval.IsExploreProbeCommand(args.Command); !ok {
				return fmt.Errorf("Explore command rejected: %s", reason)
			}
			if derived.Kind != effect.KindProcessExec ||
				derived.Scope != effect.ScopeWorkspace ||
				derived.Classification != effect.Harmless {
				return fmt.Errorf("Explore command is not a harmless Workspace probe")
			}
			return nil
		}
		if derived.Kind != effect.KindFileRead ||
			derived.Scope != effect.ScopeWorkspace {
			return fmt.Errorf("Explore may only read files inside the Workspace")
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
