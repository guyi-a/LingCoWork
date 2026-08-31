package service

import (
	"context"
	"log"

	"github.com/google/uuid"

	"github.com/guyi-a/Interview-Agent/internal/hitl"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

// ReconcileChatState repairs durable runtime metadata after process restart.
// It never re-executes a tool: unresolved non-HITL calls receive an
// interrupted result so model history remains valid.
func ReconcileChatState(
	ctx context.Context,
	conversations *repository.ConversationRepo,
	messages *repository.MessageRepo,
	pending *repository.PendingApprovalRepo,
	checkpoints *repository.CheckpointRepo,
) ([]model.PendingApproval, error) {
	rows, err := pending.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	valid := make([]model.PendingApproval, 0, len(rows))
	firstPending := make(map[string]model.PendingApproval)
	for _, row := range rows {
		conv, convErr := conversations.Get(ctx, row.ConversationID)
		_, checkpointOK, checkpointErr := checkpoints.Get(ctx, row.ConversationID)
		if convErr != nil || checkpointErr != nil {
			return nil, firstError(convErr, checkpointErr)
		}
		if conv == nil || !checkpointOK {
			if err := pending.DeleteByInterruptID(ctx, row.ConversationID, row.InterruptID); err != nil {
				return nil, err
			}
			continue
		}
		valid = append(valid, row)
		if _, exists := firstPending[row.ConversationID]; !exists {
			firstPending[row.ConversationID] = row
		}
	}
	for convID, row := range firstPending {
		if err := conversations.SetAgentStatus(ctx, convID, waitingStatus(row.Kind)); err != nil {
			return nil, err
		}
	}

	stale, err := conversations.ListByAgentStatuses(
		ctx, "running", "cancelling",
		"waiting_approval", "waiting_user", "waiting_plan",
	)
	if err != nil {
		return nil, err
	}
	for _, conv := range stale {
		if row, ok := firstPending[conv.ID]; ok {
			if err := conversations.SetAgentStatus(ctx, conv.ID, waitingStatus(row.Kind)); err != nil {
				return nil, err
			}
			// A valid pending means the run is parked on a live interrupt.
			// Deliberately leave EVERY open tool call in this conversation
			// untouched — closing them here would break the resume chain.
			//
			// Why the whole-conversation skip (not just the pending CallID):
			// the approved call may belong to a SUB-agent (its internal tool
			// calls live in extra.sub_events, not message rows), while the
			// open root call in the message table is the PARENT agent tool
			// (e.g. deep_research) that wrapped it. That parent call must
			// stay open until the checkpoint resumes and the child finishes;
			// injecting a "[canceled]" placeholder now would orphan the
			// sub-agent's interrupt and prevent a clean resume. Restoring the
			// waiting status is enough — the RUNNER handles the rest on resume.
			continue
		}
		journal := newMessageJournal(messages, conv.ID, "startup-"+uuid.NewString())
		open, err := messages.OpenToolCalls(ctx, conv.ID)
		if err != nil {
			return nil, err
		}
		for _, call := range open {
			if err := journal.AppendToolResult(ctx, stream.ToolResultRecord{
				CallID: call.ID, Name: call.Name, OK: false,
				Content:   stream.CanceledPlaceholderPrefix + " process restarted",
				Error:     "process restarted before the tool completed",
				Cancelled: true,
			}); err != nil {
				return nil, err
			}
		}
		if err := conversations.SetAgentStatus(ctx, conv.ID, "idle"); err != nil {
			return nil, err
		}
		if len(open) > 0 {
			log.Printf("startup reconcile: closed %d tool call(s) for conversation %s", len(open), conv.ID)
		}
	}
	return valid, nil
}

func waitingStatus(rawKind string) string {
	switch hitl.PendingKind(rawKind) {
	case hitl.KindQuestion:
		return "waiting_user"
	case hitl.KindPlan:
		return "waiting_plan"
	default:
		return "waiting_approval"
	}
}

func firstError(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}
