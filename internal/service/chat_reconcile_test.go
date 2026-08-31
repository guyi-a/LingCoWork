package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

func TestReconcileChatStateClosesLostToolsAndRestoresWaiting(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	convs := repository.NewConversationRepo(db)
	messages := repository.NewMessageRepo(db)
	pending := repository.NewPendingApprovalRepo(db)
	checkpoints := repository.NewCheckpointRepo(db)

	if err := convs.Upsert(t.Context(), "lost"); err != nil {
		t.Fatal(err)
	}
	if err := convs.SetAgentStatus(t.Context(), "lost", "running"); err != nil {
		t.Fatal(err)
	}
	if err := messages.Append(t.Context(), &model.Message{
		ConversationID: "lost", Role: "assistant",
		ToolCalls: `[{"id":"call-lost","name":"run_command","args_json":"{}"}]`,
	}); err != nil {
		t.Fatal(err)
	}

	if err := convs.Upsert(t.Context(), "waiting"); err != nil {
		t.Fatal(err)
	}
	if err := checkpoints.Set(t.Context(), "waiting", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	if err := pending.Insert(t.Context(), &model.PendingApproval{
		ConversationID: "waiting", InterruptID: "interrupt",
		CallID: "call-waiting", Kind: "question",
	}); err != nil {
		t.Fatal(err)
	}
	if err := pending.Insert(t.Context(), &model.PendingApproval{
		ConversationID: "missing", InterruptID: "orphan", Kind: "approval",
	}); err != nil {
		t.Fatal(err)
	}

	valid, err := ReconcileChatState(
		t.Context(), convs, messages, pending, checkpoints,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 1 || valid[0].ConversationID != "waiting" {
		t.Fatalf("valid pending=%#v", valid)
	}
	lost, _ := convs.Get(t.Context(), "lost")
	if lost.AgentStatus != "idle" {
		t.Fatalf("lost status=%q", lost.AgentStatus)
	}
	open, err := messages.OpenToolCalls(t.Context(), "lost")
	if err != nil || len(open) != 0 {
		t.Fatalf("open calls=%#v err=%v", open, err)
	}
	waiting, _ := convs.Get(t.Context(), "waiting")
	if waiting.AgentStatus != "waiting_user" {
		t.Fatalf("waiting status=%q", waiting.AgentStatus)
	}
	rows, err := pending.ListAll(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending rows=%#v err=%v", rows, err)
	}
}

// TestReconcileKeepsOpenParentCallWhenSubAgentPending pins the conservative
// whole-conversation skip: a valid pending whose CallID belongs to a SUB-agent
// (it does not appear in the message table) must NOT close the open root
// PARENT tool call that launched that sub-agent. Closing it here would break
// the checkpoint resume chain. See ReconcileChatState's comment on this branch.
func TestReconcileKeepsOpenParentCallWhenSubAgentPending(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	convs := repository.NewConversationRepo(db)
	messages := repository.NewMessageRepo(db)
	pending := repository.NewPendingApprovalRepo(db)
	checkpoints := repository.NewCheckpointRepo(db)

	if err := convs.Upsert(t.Context(), "subagent"); err != nil {
		t.Fatal(err)
	}
	if err := convs.SetAgentStatus(t.Context(), "subagent", "running"); err != nil {
		t.Fatal(err)
	}
	if err := checkpoints.Set(t.Context(), "subagent", []byte("cp")); err != nil {
		t.Fatal(err)
	}
	// Root assistant emitted the PARENT tool that will launch the sub-agent;
	// it has no tool row yet, so it is "open".
	if err := messages.Append(t.Context(), &model.Message{
		ConversationID: "subagent", Role: "assistant",
		ToolCalls: `[{"id":"call-parent","name":"deep_research","args_json":"{}"}]`,
	}); err != nil {
		t.Fatal(err)
	}
	// Approved call is sub-agent-internal; it is NOT in the message table.
	if err := pending.Insert(t.Context(), &model.PendingApproval{
		ConversationID: "subagent", InterruptID: "int-sub",
		CallID: "call-child", Kind: "approval",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ReconcileChatState(
		t.Context(), convs, messages, pending, checkpoints,
	); err != nil {
		t.Fatal(err)
	}
	conv, _ := convs.Get(t.Context(), "subagent")
	if conv.AgentStatus != "waiting_approval" {
		t.Fatalf("status=%q, want waiting_approval", conv.AgentStatus)
	}
	// The parent call must remain open for resume.
	open, err := messages.OpenToolCalls(t.Context(), "subagent")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != "call-parent" {
		t.Fatalf("open calls=%#v, want the parent call to stay open", open)
	}
	// And no tool row may have been synthesized for the parent call.
	msgs, err := messages.List(t.Context(), "subagent")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Role != "assistant" {
		t.Fatalf("rows=%#v, want exactly the original assistant row", msgs)
	}
}

// TestReconcileRootParallelApprovalKeepsAllOpen pins that a pending matching
// one of several PARALLEL root tool calls still leaves the batch intact —
// nothing is closed, because the whole checkpoint resumes as one group.
func TestReconcileRootParallelApprovalKeepsAllOpen(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	convs := repository.NewConversationRepo(db)
	messages := repository.NewMessageRepo(db)
	pending := repository.NewPendingApprovalRepo(db)
	checkpoints := repository.NewCheckpointRepo(db)

	if err := convs.Upsert(t.Context(), "parallel"); err != nil {
		t.Fatal(err)
	}
	if err := convs.SetAgentStatus(t.Context(), "parallel", "running"); err != nil {
		t.Fatal(err)
	}
	if err := checkpoints.Set(t.Context(), "parallel", []byte("cp")); err != nil {
		t.Fatal(err)
	}
	if err := messages.Append(t.Context(), &model.Message{
		ConversationID: "parallel", Role: "assistant",
		ToolCalls: `[{"id":"call-a","name":"web_search","args_json":"{}"},{"id":"call-b","name":"read_file","args_json":"{}"}]`,
	}); err != nil {
		t.Fatal(err)
	}
	// Only call-a is pending; call-b is a sibling in the same checkpoint.
	if err := pending.Insert(t.Context(), &model.PendingApproval{
		ConversationID: "parallel", InterruptID: "int-a",
		CallID: "call-a", Kind: "approval",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ReconcileChatState(
		t.Context(), convs, messages, pending, checkpoints,
	); err != nil {
		t.Fatal(err)
	}
	open, err := messages.OpenToolCalls(t.Context(), "parallel")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 {
		t.Fatalf("open calls=%#v, want call-a and call-b both preserved", open)
	}
}

// TestReconcileMultiplePendingSameCheckpointPreserved verifies that several
// pending rows sharing one conversation survive as a group (none deleted as
// an orphan) and the conversation is parked on the FIRST one's kind.
func TestReconcileMultiplePendingSameCheckpointPreserved(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	convs := repository.NewConversationRepo(db)
	messages := repository.NewMessageRepo(db)
	pending := repository.NewPendingApprovalRepo(db)
	checkpoints := repository.NewCheckpointRepo(db)

	if err := convs.Upsert(t.Context(), "multi"); err != nil {
		t.Fatal(err)
	}
	if err := convs.SetAgentStatus(t.Context(), "multi", "running"); err != nil {
		t.Fatal(err)
	}
	if err := checkpoints.Set(t.Context(), "multi", []byte("cp")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	// Distinct interrupt ids, controlled CreatedAt so the first is deterministic
	// (ListAll orders by created_at ASC).
	if err := pending.Insert(t.Context(), &model.PendingApproval{
		ConversationID: "multi", InterruptID: "int-1",
		CallID: "call-x", Kind: "approval", CreatedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	if err := pending.Insert(t.Context(), &model.PendingApproval{
		ConversationID: "multi", InterruptID: "int-2",
		CallID: "call-y", Kind: "question", CreatedAt: start.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	valid, err := ReconcileChatState(
		t.Context(), convs, messages, pending, checkpoints,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 2 {
		t.Fatalf("valid=%#v, want both pending rows preserved", valid)
	}
	conv, _ := convs.Get(t.Context(), "multi")
	// First (earliest) pending is Kind=approval → waiting_approval.
	if conv.AgentStatus != "waiting_approval" {
		t.Fatalf("status=%q, want waiting_approval", conv.AgentStatus)
	}
	rows, err := pending.ListAll(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatalf("pending rows=%#v err=%v, want both intact", rows, err)
	}
}
