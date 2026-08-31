package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/hitl"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/stream"
	"github.com/guyi-a/Interview-Agent/internal/workplan"
)

type createPlanInput struct {
	Overview string          `json:"overview" jsonschema:"description=Short user-facing summary of the implementation plan."`
	BodyMD   string          `json:"body_md" jsonschema:"description=Detailed Markdown plan covering current behavior\\, implementation steps\\, risks\\, and validation."`
	Todos    []planItemInput `json:"todos" jsonschema:"description=Ordered implementation tasks with stable ids and pending status."`
}

type todoWriteInput struct {
	PlanID string          `json:"plan_id,omitempty" jsonschema:"description=Existing active plan id. Omit to use or create the board for the current user turn."`
	Merge  bool            `json:"merge" jsonschema:"description=True to patch items by stable id; false to replace the whole list."`
	Todos  []planItemInput `json:"todos" jsonschema:"description=Todo items with id\\, content\\, and pending/in_progress/completed/cancelled status."`
}

type planItemInput struct {
	ID      string `json:"id"`
	Content string `json:"content,omitempty"`
	Status  string `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=completed,enum=cancelled"`
}

func newPlanTools(
	plans *workplan.Service,
	messages *repository.MessageRepo,
) ([]tool.BaseTool, error) {
	create, err := utils.InferTool(
		"create_plan",
		"Publish the researched implementation plan for inline user review. Use exactly once after Plan-mode investigation is complete. This interrupts execution until the user edits and starts implementation; do not implement before it resumes.",
		func(ctx context.Context, in *createPlanInput) (string, error) {
			wasInterrupted, _, _ := tool.GetInterruptState[any](ctx)
			if wasInterrupted {
				_, hasDecision, decision := tool.GetResumeContext[hitl.PlanDecision](ctx)
				if !hasDecision || decision.Cancelled {
					return `{"cancelled":true,"instruction":"The user cancelled the plan. Do not implement it."}`, nil
				}
				if !json.Valid([]byte(decision.PlanJSON)) {
					return "", errors.New("create_plan resumed without a valid plan snapshot")
				}
				return `{"approved":true,"instruction":"Implement this exact user-edited plan and keep its todos updated with todo_write.","plan":` +
					decision.PlanJSON + "}", nil
			}

			convID := contextkey.ConversationID(ctx)
			if convID == "" {
				return "", errors.New("create_plan requires a conversation")
			}
			userSeq, err := messages.LatestUserSeq(ctx, convID)
			if err != nil {
				return "", fmt.Errorf("create_plan user message: %w", err)
			}
			snapshot, err := plans.CreateDraft(
				ctx, convID, userSeq, in.Overview, in.BodyMD, workPlanItems(in.Todos),
			)
			if err != nil {
				return "", err
			}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				return "", err
			}
			if buf := contextkey.Buffer(ctx); buf != nil {
				buf.Append(stream.Encode(stream.Frame{
					Type:     "plan_update",
					PlanJSON: string(raw),
				}))
			}
			return "", tool.Interrupt(ctx, &stream.PlanInfo{
				PlanID:   snapshot.ID,
				PlanJSON: string(raw),
				CallID:   compose.GetToolCallID(ctx),
			})
		},
	)
	if err != nil {
		return nil, err
	}

	todos, err := utils.InferTool(
		"todo_write",
		"Create or update the structured execution todo list. Use for complex work, not trivial one-step tasks. Keep stable ids, at most one in_progress item, mark an item completed only after its work and verification are actually done, and use cancelled when the scope is dropped.",
		func(ctx context.Context, in *todoWriteInput) (*workplan.Snapshot, error) {
			convID := contextkey.ConversationID(ctx)
			if convID == "" {
				return nil, errors.New("todo_write requires a conversation")
			}
			userSeq, err := messages.LatestUserSeq(ctx, convID)
			if err != nil {
				return nil, fmt.Errorf("todo_write user message: %w", err)
			}
			snapshot, err := plans.UpdateTodos(
				ctx, convID, userSeq, in.PlanID, in.Merge, workPlanItems(in.Todos),
			)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				return nil, err
			}
			if buf := contextkey.Buffer(ctx); buf != nil {
				buf.Append(stream.Encode(stream.Frame{
					Type:     "todo_update",
					PlanJSON: string(raw),
				}))
			}
			return snapshot, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{create, todos}, nil
}

func workPlanItems(input []planItemInput) []workplan.Item {
	out := make([]workplan.Item, len(input))
	for i, item := range input {
		out[i] = workplan.Item{
			ID:      item.ID,
			Content: item.Content,
			Status:  item.Status,
		}
	}
	return out
}
