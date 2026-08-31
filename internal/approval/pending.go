package approval

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"github.com/guyi-a/Interview-Agent/internal/hitl"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

// Pending item — one paused tool call awaiting user input (approval decision
// or answer to a question). Keyed externally by conversation id so the HTTP
// layer can look up the target without knowing checkpoint internals.
type PendingItem struct {
	// Kind 决定这条 pending 走哪种 UI + resume 路径。缺省视为审批，向后兼容
	// 已有落盘数据。
	Kind hitl.PendingKind
	// CheckpointID is the eino Runner checkpoint id (== conversation id in
	// our setup), passed to Runner.ResumeWithParams on user input.
	CheckpointID string
	// InterruptID is InterruptCtx.ID — the address eino uses to route the
	// resume payload to the exact middleware / tool invocation that paused.
	InterruptID string
	// CallID lets the frontend align the pending card with its tool_call
	// frame in the timeline.
	CallID string
	// Tool 仅审批场景下有意义（要审的工具名）；question 场景为空。
	Tool string
	// Args 是审批场景下的原始工具 JSON 参数；question 场景放 hitl.Question 数组
	// 的 JSON 序列化，前端拉 pending 时按 Kind 决定怎么解读。
	Args string
	// EffectJSON 是这次审批依据的 effect.Effect 序列化结果，仅 approval 场景
	// 有值。可能为空：老落盘数据没有这个字段，前端回退到按 Args 渲染。
	EffectJSON string
	// resolved/payload only live until every sibling interrupt from the same
	// checkpoint has received an answer. Keeping partial answers in memory
	// avoids resuming a parallel checkpoint more than once. If the process
	// restarts before the batch is complete, all cards are intentionally
	// presented again rather than guessing at lost user decisions.
	resolved bool
	payload  any
}

// PendingStore keeps in-flight approvals per conversation. In-memory index
// backed by a SQLite pending_approvals table so a server restart doesn't
// strand a conversation mid-approval: on boot the service calls Restore to
// hydrate the in-memory list from the DB.
//
// Scope: this type owns the pending list + its own DB rows only. It does NOT
// touch conversation.agent_status — that's ChatService's job, driven by
// HasPending in finalizeStatus.
type PendingStore struct {
	mu    sync.Mutex
	items map[string][]*PendingItem // convID -> ordered list
	repo  *repository.PendingApprovalRepo
}

func NewPendingStore(repo *repository.PendingApprovalRepo) *PendingStore {
	return &PendingStore{
		items: make(map[string][]*PendingItem),
		repo:  repo,
	}
}

// Record is called by the stream layer when an Interrupted event arrives.
// info is the *stream.ApprovalInfo the middleware attached; other types are
// stashed with empty display fields.
//
// DB write failures are logged with enough context to locate the row but do
// not abort the in-memory append — the user still needs the ApprovalBar to
// show on this live connection. Losing the row only costs recoverability on
// the next restart, which matches pre-persistence behaviour.
func (s *PendingStore) Record(convID string) stream.InterruptSink {
	return sinkFunc(func(checkpointID, interruptID string, info any) {
		item := &PendingItem{
			Kind:         hitl.KindApproval,
			CheckpointID: checkpointID,
			InterruptID:  interruptID,
		}
		switch v := info.(type) {
		case *stream.ApprovalInfo:
			if v != nil {
				item.Tool = v.Tool
				item.Args = v.Args
				item.CallID = v.CallID
				item.EffectJSON = v.EffectJSON
			}
		case *stream.QuestionInfo:
			item.Kind = hitl.KindQuestion
			if v != nil {
				item.CallID = v.CallID
				if raw, err := json.Marshal(v.Questions); err == nil {
					item.Args = string(raw)
				}
			}
		case *stream.PlanInfo:
			item.Kind = hitl.KindPlan
			if v != nil {
				item.CallID = v.CallID
				item.Tool = "create_plan"
				item.Args = v.PlanJSON
			}
		}

		s.mu.Lock()
		// 内存去重：SSE 重连 / 恢复回放场景下 Record 可能被多次调用；同一个
		// (conv, interrupt) 已在队列里就跳过 append 也不重复写 DB。
		duplicate := false
		for _, existing := range s.items[convID] {
			if existing != nil && existing.InterruptID == interruptID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			s.items[convID] = append(s.items[convID], item)
		}
		s.mu.Unlock()
		if duplicate {
			return
		}

		if s.repo != nil {
			row := &model.PendingApproval{
				ConversationID: convID,
				InterruptID:    interruptID,
				CallID:         item.CallID,
				Tool:           item.Tool,
				Args:           item.Args,
				Effect:         item.EffectJSON,
				Kind:           string(item.Kind),
			}
			if err := s.repo.Insert(context.Background(), row); err != nil {
				log.Printf("pending insert failed (conv=%s interrupt=%s kind=%s tool=%s): %v",
					convID, interruptID, item.Kind, item.Tool, err)
			}
		}
	})
}

// Resolve records one interrupt answer and returns a complete resume target
// batch once every sibling from the same checkpoint has been answered.
//
// Parallel tool calls are checkpointed together. Resuming after each click
// would replay the same checkpoint multiple times; additionally, middleware
// that sees a resume flow without its own payload may proceed implicitly.
// Holding the answers here ensures exactly one ResumeWithParams call receives
// every interrupt target.
func (s *PendingStore) Resolve(
	convID, interruptID string,
	payload any,
) (checkpointID string, targets map[string]any, found, ready bool) {
	s.mu.Lock()
	list := s.items[convID]
	var selected *PendingItem
	for _, item := range list {
		if item != nil && item.InterruptID == interruptID && !item.resolved {
			selected = item
			break
		}
	}
	if selected == nil {
		s.mu.Unlock()
		return "", nil, false, false
	}

	selected.resolved = true
	selected.payload = payload
	checkpointID = selected.CheckpointID

	for _, item := range list {
		if item != nil && item.CheckpointID == checkpointID && !item.resolved {
			s.mu.Unlock()
			return checkpointID, nil, true, false
		}
	}

	targets = make(map[string]any)
	remaining := make([]*PendingItem, 0, len(list))
	resolvedIDs := make([]string, 0, len(list))
	for _, item := range list {
		if item != nil && item.CheckpointID == checkpointID {
			targets[item.InterruptID] = item.payload
			resolvedIDs = append(resolvedIDs, item.InterruptID)
			continue
		}
		remaining = append(remaining, item)
	}
	if len(remaining) == 0 {
		delete(s.items, convID)
	} else {
		s.items[convID] = remaining
	}
	s.mu.Unlock()

	if s.repo != nil {
		for _, id := range resolvedIDs {
			if err := s.repo.DeleteByInterruptID(context.Background(), convID, id); err != nil {
				log.Printf("pending_approvals batch delete failed (conv=%s interrupt=%s): %v",
					convID, id, err)
			}
		}
	}
	return checkpointID, targets, true, true
}

// HasPending reports whether the conversation has anything awaiting approval.
// The service uses this to decide the conversation's agent_status.
func (s *PendingStore) HasPending(convID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items[convID]) > 0
}

func (s *PendingStore) Has(convID, interruptID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items[convID] {
		if item != nil && item.InterruptID == interruptID && !item.resolved {
			return true
		}
	}
	return false
}

// List returns a snapshot of pending approvals for a conversation in arrival
// order. The returned items are copies so callers cannot mutate store state.
func (s *PendingStore) List(convID string) []PendingItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.items[convID]
	if len(list) == 0 {
		return nil
	}
	out := make([]PendingItem, 0, len(list))
	for _, it := range list {
		if it == nil || it.resolved {
			continue
		}
		out = append(out, *it)
	}
	return out
}

// Clear drops every pending item for a conversation. Called on hard cancel
// or when a new run starts (which overwrites the checkpoint anyway).
func (s *PendingStore) Clear(convID string) {
	s.mu.Lock()
	delete(s.items, convID)
	s.mu.Unlock()

	if s.repo != nil {
		if err := s.repo.DeleteByConversationID(context.Background(), convID); err != nil {
			log.Printf("pending_approvals clear failed (conv=%s): %v", convID, err)
		}
	}
}

// Restore hydrates the in-memory store from persisted rows at startup. Rows
// are expected to arrive in arrival order (ORDER BY created_at ASC) so
// multi-pending conversations replay in the same sequence users saw before
// the crash. This does NOT re-write the rows back to the DB.
func (s *PendingStore) Restore(rows []model.PendingApproval) {
	if len(rows) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		kind := hitl.PendingKind(r.Kind)
		if kind == "" {
			// 老落盘行（未带 kind 字段）当作审批处理，向后兼容。
			kind = hitl.KindApproval
		}
		item := &PendingItem{
			Kind:         kind,
			CheckpointID: r.ConversationID,
			InterruptID:  r.InterruptID,
			CallID:       r.CallID,
			Tool:         r.Tool,
			Args:         r.Args,
			EffectJSON:   r.Effect,
		}
		s.items[r.ConversationID] = append(s.items[r.ConversationID], item)
	}
}

type sinkFunc func(checkpointID, interruptID string, info any)

func (f sinkFunc) Record(checkpointID, interruptID string, info any) {
	f(checkpointID, interruptID, info)
}
