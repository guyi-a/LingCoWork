package runtimectx

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/workplan"
)

// WorkPlanMiddleware injects the canonical database snapshot on every model
// iteration. Structured state wins over prose remembered in chat/compaction.
type WorkPlanMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	plans *workplan.Service
}

func NewWorkPlanMiddleware(plans *workplan.Service) *WorkPlanMiddleware {
	return &WorkPlanMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		plans:                        plans,
	}
}

func (m *WorkPlanMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if m == nil || m.plans == nil {
		return ctx, runCtx, nil
	}
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return ctx, runCtx, nil
	}
	plan, err := m.plans.Latest(ctx, convID)
	if err != nil || plan == nil {
		return ctx, runCtx, nil
	}
	if plan.Status == workplan.StatusCompleted ||
		plan.Status == workplan.StatusCancelled {
		return ctx, runCtx, nil
	}
	snippet := renderWorkPlan(plan)
	if runCtx.Instruction != "" {
		runCtx.Instruction += "\n\n" + snippet
	} else {
		runCtx.Instruction = snippet
	}
	return ctx, runCtx, nil
}

func renderWorkPlan(plan *workplan.Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 当前结构化 Plan / Todo（数据库事实源）\n\nplan_id=%s，status=%s，revision=%d\n",
		plan.ID, plan.Status, plan.Revision)
	if plan.Overview != "" {
		fmt.Fprintf(&b, "\n概述：%s\n", plan.Overview)
	}
	if plan.BodyMD != "" {
		fmt.Fprintf(&b, "\n已确认计划：\n%s\n", plan.BodyMD)
	}
	if len(plan.Items) > 0 {
		b.WriteString("\n任务：\n")
		for _, item := range plan.Items {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", item.Status, item.ID, item.Content)
		}
	}
	b.WriteString("\n执行时只用 todo_write 更新上述结构化状态；不得用自然语言声称完成来替代状态更新。")
	return b.String()
}
