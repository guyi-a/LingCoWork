package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/agent/multimodal"
	"github.com/guyi-a/Interview-Agent/internal/agent/toolerr"
	"github.com/guyi-a/Interview-Agent/internal/agentmode"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/compaction"
	"github.com/guyi-a/Interview-Agent/internal/hitl"
	"github.com/guyi-a/Interview-Agent/internal/instructions"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

var (
	ErrWorkspaceRequired       = errors.New("a workspace project is required")
	ErrProjectNotFound         = errors.New("workspace project not found")
	ErrProjectMismatch         = errors.New("conversation is already bound to another project")
	ErrRunActive               = errors.New("cannot change agent mode while a run or user interaction is active")
	ErrApprovalNotRememberable = errors.New("this approval cannot be remembered")
)

type ChatService struct {
	runner         *adk.Runner
	rootName       string
	manager        *stream.Manager
	convRepo       *repository.ConversationRepo
	msgRepo        *repository.MessageRepo
	projectRepo    *repository.ProjectRepo
	instructions   *instructions.Store
	pending        *approval.PendingStore
	approvalModes  *approval.ModeStore
	approvalMemory *approval.Memory
	multimodal     bool
	// compactor is nil when context compaction isn't configured; all of its
	// methods tolerate a nil receiver, so call sites don't branch.
	compactor *compaction.Compactor
}

func (s *ChatService) GetAgentMode(
	ctx context.Context,
	convID string,
) (agentmode.Mode, error) {
	conv, err := s.convRepo.Get(ctx, convID)
	if err != nil {
		return "", err
	}
	if conv == nil {
		return agentmode.Agent, nil
	}
	return agentmode.Parse(conv.ChatMode)
}

func (s *ChatService) HasPendingInterrupt(convID, interruptID string) bool {
	return s != nil && s.pending != nil && s.pending.Has(convID, interruptID)
}

func (s *ChatService) SetAgentMode(
	ctx context.Context,
	convID string,
	mode agentmode.Mode,
) error {
	parsed, err := agentmode.Parse(string(mode))
	if err != nil {
		return err
	}
	if (s.manager != nil && s.manager.IsStreaming(convID)) ||
		(s.pending != nil && s.pending.HasPending(convID)) {
		return ErrRunActive
	}
	return s.convRepo.SetChatMode(ctx, convID, string(parsed))
}

func NewChatService(
	runner *adk.Runner,
	rootName string,
	manager *stream.Manager,
	convRepo *repository.ConversationRepo,
	msgRepo *repository.MessageRepo,
	projectRepo *repository.ProjectRepo,
	instructionStore *instructions.Store,
	pending *approval.PendingStore,
	approvalModes *approval.ModeStore,
	approvalMemory *approval.Memory,
	multimodal bool,
	compactor *compaction.Compactor,
) *ChatService {
	return &ChatService{
		runner:         runner,
		rootName:       rootName,
		manager:        manager,
		convRepo:       convRepo,
		msgRepo:        msgRepo,
		projectRepo:    projectRepo,
		instructions:   instructionStore,
		pending:        pending,
		approvalModes:  approvalModes,
		approvalMemory: approvalMemory,
		multimodal:     multimodal,
		compactor:      compactor,
	}
}

func (s *ChatService) Get(id string) *stream.StreamBuffer {
	return s.manager.Get(id)
}

func (s *ChatService) IsStreaming(id string) bool {
	return s.manager.IsStreaming(id)
}

func (s *ChatService) ResumeStream(
	ctx context.Context,
	id string,
	afterSeq int,
) (<-chan []byte, stream.CursorStatus, int, error) {
	if buf := s.manager.Get(id); buf != nil {
		ch, status, durableSeq := buf.StreamFrom(ctx, afterSeq)
		if status != stream.CursorComplete {
			return ch, status, durableSeq, nil
		}
	}
	maxSeq, err := s.msgRepo.MaxSeq(ctx, id)
	if err != nil {
		return nil, stream.CursorComplete, 0, err
	}
	switch {
	case afterSeq < maxSeq:
		return nil, stream.CursorClientStale, maxSeq, nil
	case afterSeq > maxSeq:
		return nil, stream.CursorBufferBehind, maxSeq, nil
	default:
		return nil, stream.CursorComplete, maxSeq, nil
	}
}

func (s *ChatService) Cancel(id string) bool {
	buf := s.manager.Get(id)
	if buf == nil {
		return false
	}
	return buf.Cancel()
}

type preparedUserMessage struct {
	content     string
	titleSource string
	extra       string
}

func (s *ChatService) prepareUserMessage(userMsg, instructionName string) (preparedUserMessage, error) {
	prepared := preparedUserMessage{content: userMsg, titleSource: userMsg}
	if instructionName == "" {
		return prepared, nil
	}
	if s.instructions == nil {
		return prepared, fmt.Errorf("%w: %s", instructions.ErrNotFound, instructionName)
	}
	instruction, err := s.instructions.Get(instructionName)
	if err != nil {
		return prepared, err
	}
	prepared.content = instructions.Expand(instruction.Prompt, userMsg)
	if prepared.titleSource == "" {
		prepared.titleSource = instruction.Label
	}
	data, err := json.Marshal(instructions.MessageExtra{
		UserInstruction: &instructions.UserInstruction{
			Name:     instruction.Name,
			Label:    instruction.Label,
			RawInput: userMsg,
		},
	})
	if err != nil {
		return prepared, fmt.Errorf("marshal instruction metadata: %w", err)
	}
	prepared.extra = string(data)
	return prepared, nil
}

// Start begins (or continues) a chat turn:
//   - requires a valid project before creating a conversation
//   - binds new or legacy-unbound conversations to that project
//   - loads prior messages as context
//   - persists the new user message
//   - kicks off the ADK Runner in a goroutine, persists assistant reply when done
func (s *ChatService) Start(
	ctx context.Context,
	id, userMsg, instructionName, projectID, rawMode, rawApprovalMode string,
) (*stream.StreamBuffer, error) {
	mode, err := agentmode.Parse(rawMode)
	if err != nil {
		return nil, err
	}
	var requestedApprovalMode approval.Mode
	if rawApprovalMode != "" {
		requestedApprovalMode = approval.Mode(rawApprovalMode)
		if !approval.ValidMode(requestedApprovalMode) {
			return nil, fmt.Errorf("invalid approval mode %q", rawApprovalMode)
		}
	}
	prepared, err := s.prepareUserMessage(userMsg, instructionName)
	if err != nil {
		return nil, err
	}

	conv, err := s.convRepo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	boundProjectID := ""
	if conv != nil && conv.ProjectID != nil {
		boundProjectID = *conv.ProjectID
	}
	if boundProjectID == "" {
		boundProjectID = strings.TrimSpace(projectID)
		if boundProjectID == "" {
			return nil, ErrWorkspaceRequired
		}
	} else if projectID != "" && projectID != boundProjectID {
		return nil, ErrProjectMismatch
	}

	project, err := s.projectRepo.Get(ctx, boundProjectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, fmt.Errorf("%w: %s", ErrProjectNotFound, boundProjectID)
	}

	if err := s.convRepo.Upsert(ctx, id); err != nil {
		return nil, err
	}
	if err := s.convRepo.SetChatMode(ctx, id, string(mode)); err != nil {
		return nil, err
	}

	if conv == nil || conv.ProjectID == nil || *conv.ProjectID == "" {
		if err := s.convRepo.SetProjectID(ctx, id, boundProjectID); err != nil {
			return nil, err
		}
	}
	if requestedApprovalMode != "" {
		if err := s.approvalModes.Set(id, requestedApprovalMode); err != nil {
			return nil, err
		}
	}

	// Loaded before the new user row is written, so `prior` is exactly the
	// completed turns. Compaction folds only what's in here, which is why
	// the incoming message can never end up summarized away.
	prior, err := s.msgRepo.List(ctx, id)
	if err != nil {
		return nil, err
	}

	userRow := &model.Message{
		ConversationID: id,
		Role:           string(schema.User),
		Content:        prepared.content,
		Extra:          prepared.extra,
	}
	if err := s.msgRepo.Append(ctx, userRow); err != nil {
		return nil, err
	}

	if title := truncateForTitle(prepared.titleSource); title != "" {
		_ = s.convRepo.SetTitleIfEmpty(ctx, id, title)
	}

	workspaceCtx := s.workspaceContext(ctx, id)

	buf := s.manager.CreateAt(id, userRow.Seq)
	runCtx, cancel := context.WithCancel(context.Background())
	buf.SetCancel(cancel)

	// Any leftover pending approvals from a previous, discarded run would
	// mismatch the checkpoint eino is about to overwrite. Clear now so the
	// only pending items visible to the HTTP layer belong to this run.
	s.pending.Clear(id)

	// Compaction and history assembly happen inside the goroutine: a
	// summarization call can take tens of seconds, and blocking here would
	// hold POST /chat/:id open with nothing on screen. The agent still
	// starts strictly after compaction settles, so the two never race over
	// the context they share.
	go func() {
		active := s.compactor.MaybeCompact(runCtx, id, prior)
		if active != nil {
			buf.Append(stream.Encode(stream.Frame{
				Type:          "context_compacted",
				CompactionID:  active.ID,
				CompactedSeq:  active.ThroughSeq,
				ReplacedCount: active.ReplacedCount,
			}))
		} else {
			active = s.compactor.Active(runCtx, id)
		}

		imageBudget := multimodal.NewImageBudget()
		currentUserMessage := multimodal.BuildUserMessageWithBudget(
			prepared.content,
			s.multimodal,
			imageBudget,
		)
		history := toSchemaMessagesWithBudget(id, prior, s.multimodal, active, imageBudget)
		if workspaceCtx != "" {
			history = append([]*schema.Message{schema.SystemMessage(workspaceCtx)}, history...)
		}
		history = append(history, currentUserMessage)

		s.runAgent(runCtx, id, history, buf)
	}()

	return buf, nil
}

// Resume records one approval decision. A checkpoint can contain several
// parallel interrupts, so the run is resumed only after every sibling has an
// answer. The second return value tells the HTTP/frontend layer whether this
// decision completed the batch and created a fresh SSE stream.
func (s *ChatService) Resume(
	convID, interruptID string,
	dec approval.Decision,
	remember bool,
) (found, resumed bool, err error) {
	item, found := s.pending.Get(convID, interruptID)
	if !found {
		return false, false, nil
	}
	if dec.Approved {
		if e, ok := approval.ParseEffect(item.EffectJSON); ok {
			dec.EffectDigest = approval.EffectDigest(e)
			if remember {
				fingerprint, rememberable := approval.Fingerprint(e, item.Args)
				if !rememberable {
					return true, false, ErrApprovalNotRememberable
				}
				s.approvalMemory.Remember(convID, fingerprint)
			}
		} else if remember {
			return true, false, ErrApprovalNotRememberable
		}
	}
	checkpointID, targets, found, ready := s.pending.Resolve(convID, interruptID, dec)
	if !found {
		return false, false, nil
	}
	if !ready {
		s.applyWaitingStatus(convID, "running")
		return true, false, nil
	}

	if err := s.startResume(convID, checkpointID, targets); err != nil {
		return true, false, err
	}
	return true, true, nil
}

// ResumeQuestion 走跟 Resume 一样的 checkpoint 恢复通道，只是 Targets 里塞
// 的是 hitl.Answers（gob 已在 hitl 包 init 里注册），由 ask_user 工具体通过
// GetResumeContext[hitl.Answers] 取回。
func (s *ChatService) ResumeQuestion(
	convID, interruptID string,
	answers hitl.Answers,
) (found, resumed bool, err error) {
	checkpointID, targets, found, ready := s.pending.Resolve(convID, interruptID, answers)
	if !found {
		return false, false, nil
	}
	if !ready {
		s.applyWaitingStatus(convID, "running")
		return true, false, nil
	}

	if err := s.startResume(convID, checkpointID, targets); err != nil {
		return true, false, err
	}
	return true, true, nil
}

func (s *ChatService) ResumePlan(
	convID, interruptID string,
	decision hitl.PlanDecision,
) (found, resumed bool, err error) {
	checkpointID, targets, found, ready := s.pending.Resolve(convID, interruptID, decision)
	if !found {
		return false, false, nil
	}
	if !ready {
		s.applyWaitingStatus(convID, "running")
		return true, false, nil
	}
	if !decision.Cancelled {
		if err := s.convRepo.SetChatMode(context.Background(), convID, string(agentmode.Agent)); err != nil {
			return true, false, err
		}
	}
	if err := s.startResume(convID, checkpointID, targets); err != nil {
		return true, false, err
	}
	return true, true, nil
}

// startResume replaces the completed interrupt stream with one continuation
// stream and supplies all decisions from that checkpoint in a single target
// map. It must only be called by the goroutine that completed a pending batch.
func (s *ChatService) startResume(
	convID, checkpointID string,
	targets map[string]any,
) error {
	s.applyWaitingStatus(convID, "running")

	maxSeq, err := s.msgRepo.MaxSeq(context.Background(), convID)
	if err != nil {
		return err
	}
	buf := s.manager.CreateAt(convID, maxSeq)
	runCtx, cancel := context.WithCancel(context.Background())
	buf.SetCancel(cancel)

	go s.resumeAgent(runCtx, convID, checkpointID, targets, buf)
	return nil
}

// applyWaitingStatus 根据当前 pending 队列的队首 kind 更新会话状态：
//   - 队列空 → fallbackWhenEmpty（通常是 running / idle）
//   - 队首是 question → waiting_user（sidebar 展示"等待回复"）
//   - 队首是 approval → waiting_approval（sidebar 展示"等待审批"）
func (s *ChatService) applyWaitingStatus(convID, fallbackWhenEmpty string) {
	status := fallbackWhenEmpty
	if items := s.pending.List(convID); len(items) > 0 {
		switch items[0].Kind {
		case hitl.KindQuestion:
			status = "waiting_user"
		case hitl.KindPlan:
			status = "waiting_plan"
		default:
			status = "waiting_approval"
		}
	}
	_ = s.convRepo.SetAgentStatus(context.Background(), convID, status)
}

// PendingApprovals returns in-memory approval requests that are still waiting
// for a user decision. The ADK checkpoint itself lives in SQLite; this list is
// just the UI lookup metadata needed after a page refresh.
func (s *ChatService) PendingApprovals(convID string) []approval.PendingItem {
	if s.pending == nil {
		return nil
	}
	return s.pending.List(convID)
}

// GetApprovalMode returns the durable per-conversation approval mode,
// defaulting fail-safe to manual.
func (s *ChatService) GetApprovalMode(convID string) approval.Mode {
	return s.approvalModes.Get(convID)
}

// SetApprovalMode validates and stores the mode. Called from the HTTP handler.
func (s *ChatService) SetApprovalMode(convID string, m approval.Mode) error {
	if s.convRepo != nil {
		if err := s.convRepo.Upsert(context.Background(), convID); err != nil {
			return err
		}
	}
	return s.approvalModes.Set(convID, m)
}

func (s *ChatService) ApprovalMemoryCount(convID string) int {
	return s.approvalMemory.Count(convID)
}

func (s *ChatService) ClearApprovalMemory(convID string) {
	s.approvalMemory.Clear(convID)
}

func (s *ChatService) workspaceContext(ctx context.Context, convID string) string {
	if s.projectRepo == nil {
		return ""
	}
	conv, err := s.convRepo.Get(ctx, convID)
	if err != nil || conv == nil || conv.ProjectID == nil || *conv.ProjectID == "" {
		return "当前会话未绑定工作区。请用户先在 LingCoWork 中选择一个文件夹；不要自行创建或猜测路径。"
	}
	project, err := s.projectRepo.Get(ctx, *conv.ProjectID)
	if err != nil || project == nil {
		return "当前会话绑定的工作区记录不存在。用户要求读写文件时，先说明工作区不可用。"
	}
	return "当前会话已绑定工作区。project_id=" + project.ID + "，project_name=" + project.Name + "，workspace=" + project.Workspace + "。用户询问当前项目/工作区/文件时，直接调用 glob/grep/list_files/read_file/apply_patch/write_file/mkdir 等文件工具，不要先询问是否已挂载工作区。"
}

func (s *ChatService) runAgent(ctx context.Context, convID string, msgs []*schema.Message, buf *stream.StreamBuffer) {
	ctx = contextkey.WithConversationID(ctx, convID)
	ctx = approval.WithMemorySnapshot(ctx, s.approvalMemory.Allowed(convID))
	ctx = contextkey.WithBuffer(ctx, buf)
	ctx = toolerr.WithRegistry(ctx, toolerr.NewRegistry())

	collector := stream.NewRunCollector()
	journal := newMessageJournal(s.msgRepo, convID, uuid.NewString())
	sink := s.approvalSink(convID)

	_ = s.convRepo.SetAgentStatus(context.Background(), convID, "running")

	// Checkpoint id is stable per conversation — one active run at a time is
	// enforced by manager.Create above, so reusing convID as the eino
	// checkpoint id keeps resume lookups trivial.
	iter := s.runner.Run(ctx, msgs, adk.WithCheckPointID(convID))
	s.consumeAndPersist(ctx, convID, iter, sink, buf, collector, journal, nil)
}

// resumeAgent 恢复被中断的 run。targets 一次携带同一 checkpoint 的全部
// 中断答案；value 可以是 approval.Decision 或 hitl.Answers。
func (s *ChatService) resumeAgent(
	ctx context.Context,
	convID, checkpointID string,
	targets map[string]any,
	buf *stream.StreamBuffer,
) {
	ctx = contextkey.WithConversationID(ctx, convID)
	ctx = approval.WithMemorySnapshot(ctx, s.approvalMemory.Allowed(convID))
	ctx = contextkey.WithBuffer(ctx, buf)
	ctx = toolerr.WithRegistry(ctx, toolerr.NewRegistry())

	collector := stream.NewRunCollector()
	journal := newMessageJournal(s.msgRepo, convID, uuid.NewString())
	sink := s.approvalSink(convID)

	_ = s.convRepo.SetAgentStatus(context.Background(), convID, "running")

	// Rebuild the sub-agent router's open-parents map from persisted
	// history so events emitted during resume (e.g. a sub-agent's
	// interrupted write_file completing) can still be attributed to the
	// supervisor-level tool_call that spawned them before the interrupt.
	// See ConsumeADKEvents doc for the rationale.
	priorRows, err := s.msgRepo.List(context.Background(), convID)
	if err != nil {
		log.Printf("resume: load prior rows (conv=%s): %v", convID, err)
	}
	initialRouter := rebuildOpenToolCalls(priorRows)

	iter, err := s.runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{
		Targets: targets,
	})
	if err != nil {
		log.Printf("adk resume error (conv=%s): %v", convID, err)
		_ = s.convRepo.SetAgentStatus(context.Background(), convID, "idle")
		stream.FinalizeErr(buf, err)
		return
	}
	s.consumeAndPersist(ctx, convID, iter, sink, buf, collector, journal, initialRouter)
}

// rebuildOpenToolCalls walks the conversation's persisted messages and
// returns the sub-agent tool_calls that are still "open" — declared by an
// assistant row but with no matching Role=Tool result row afterwards. Used
// on resume to seed stream.subAgentRouter so mid-flight sub-agent events
// (e.g. a write_file tool_result arriving after the human approves)
// resolve to the correct parent tool_call_id from before the interrupt.
//
// Keyed by tool name because the router uses the sub-agent's AgentName as
// its lookup key (which equals the tool name for NewAgentTool-wrapped
// sub-agents like deep_research / job_search). If the same tool name is
// called twice in one run, the later id wins — matches the router's
// noteRootToolCall overwrite semantics.
func rebuildOpenToolCalls(rows []model.Message) map[string]string {
	open := map[string]string{} // tool name → tool_call_id, only kept while still un-resolved
	for _, r := range rows {
		if r.Role == string(schema.Assistant) && r.ToolCalls != "" {
			var tcs []stream.ToolCallRecord
			if err := json.Unmarshal([]byte(r.ToolCalls), &tcs); err == nil {
				for _, tc := range tcs {
					open[tc.Name] = tc.ID
				}
			}
			continue
		}
		if r.Role == string(schema.Tool) && r.ToolCallID != "" {
			// Drop any open entry whose id matches this tool_result.
			for name, id := range open {
				if id == r.ToolCallID {
					delete(open, name)
					break
				}
			}
		}
	}
	if len(open) == 0 {
		return nil
	}
	return open
}

// approvalSink wraps the pending store's sink with a side effect: whenever a
// run pauses, mark the conversation status so the sidebar pill lights up.
// The status depends on the payload type — approval → waiting_approval，
// question → waiting_user。End-of-run finalizer flips it back.
func (s *ChatService) approvalSink(convID string) stream.InterruptSink {
	inner := s.pending.Record(convID)
	return sinkFunc(func(checkpointID, interruptID string, info any) {
		inner.Record(checkpointID, interruptID, info)
		status := "waiting_approval"
		if _, ok := info.(*stream.QuestionInfo); ok {
			status = "waiting_user"
		} else if _, ok := info.(*stream.PlanInfo); ok {
			status = "waiting_plan"
		}
		_ = s.convRepo.SetAgentStatus(context.Background(), convID, status)
	})
}

type sinkFunc func(checkpointID, interruptID string, info any)

func (f sinkFunc) Record(checkpointID, interruptID string, info any) {
	f(checkpointID, interruptID, info)
}

// consumeAndPersist drives the iterator, persists the run's turns, and
// finalises the SSE buffer. Shared between the initial Run and post-approval
// Resume paths so both take the same code path.
func (s *ChatService) consumeAndPersist(
	ctx context.Context,
	convID string,
	iter *adk.AsyncIterator[*adk.AgentEvent],
	sink stream.InterruptSink,
	buf *stream.StreamBuffer,
	collector *stream.RunCollector,
	journal stream.MessageJournal,
	initialRouterState map[string]string,
) {
	if err := stream.ConsumeADKEvents(ctx, iter, s.rootName, convID, sink, buf, collector, journal, initialRouterState); err != nil {
		log.Printf("adk runner error: %v", err)
		if perr := s.persistInterruptState(convID, collector, journal, err); perr != nil {
			log.Printf("persist interrupt state: %v", perr)
		}
		s.updateJournalMetadata(convID, collector, journal)
		s.finalizeStatus(convID)
		stream.FinalizeErr(buf, err)
		return
	}

	interrupted := s.pending.HasPending(convID)
	if !interrupted {
		if err := s.persistInterruptState(convID, collector, journal, nil); err != nil {
			log.Printf("persist trailing state: %v", err)
		}
	}
	s.updateJournalMetadata(convID, collector, journal)
	_ = s.convRepo.Upsert(context.Background(), convID)
	s.finalizeStatus(convID)
	stream.FinalizeOK(buf)
}

func (s *ChatService) updateJournalMetadata(
	convID string,
	collector *stream.RunCollector,
	journal stream.MessageJournal,
) {
	if journal == nil || collector == nil {
		return
	}
	extra := ""
	subEvents, version, dirty := collector.SubEventsForFlush()
	if dirty && len(subEvents) > 0 {
		if data, err := json.Marshal(map[string]any{"sub_events": subEvents}); err == nil {
			extra = string(data)
		}
	}
	if err := journal.UpdateLastAssistant(
		context.Background(), collector.TotalTokens(), extra,
	); err != nil {
		log.Printf("update journal metadata (conv=%s): %v", convID, err)
		return
	}
	if dirty {
		collector.MarkSubEventsFlushed(version)
	}
}

func (s *ChatService) persistInterruptState(
	convID string,
	collector *stream.RunCollector,
	journal stream.MessageJournal,
	runErr error,
) error {
	if journal == nil || collector == nil {
		return nil
	}
	if err := journal.AppendPartialAssistant(
		context.Background(), collector.Content(), collector.Reasoning(),
	); err != nil {
		return err
	}
	pendingCalls := make(map[string]struct{})
	for _, item := range s.pending.List(convID) {
		if item.CallID != "" {
			pendingCalls[item.CallID] = struct{}{}
		}
	}
	open, err := s.msgRepo.OpenToolCalls(context.Background(), convID)
	if err != nil {
		return err
	}
	reason := "interrupted"
	if runErr != nil {
		reason = runErr.Error()
	}
	for _, call := range open {
		if _, waiting := pendingCalls[call.ID]; waiting {
			continue
		}
		if _, _, err := journal.AppendToolResult(context.Background(), stream.ToolResultRecord{
			CallID: call.ID, Name: call.Name, OK: false,
			Content: stream.CanceledPlaceholderPrefix + " tool did not run",
			Error:   reason, Cancelled: true,
		}); err != nil {
			return err
		}
	}
	return nil
}

// finalizeStatus 设定会话状态：无 pending → idle；有 pending 按队首 kind 区
// 分 waiting_approval / waiting_user。iterator 因中断自然结束时也走这里。
func (s *ChatService) finalizeStatus(convID string) {
	s.applyWaitingStatus(convID, "idle")
}

// toSchemaMessages hydrates DB rows into schema.Message with the full
// tool_use / tool_result structure so Claude sees the real prior tool
// invocations, not just the assistant's prose about them.
//
// Old-format rows (no ToolCalls column and no matching Role=tool rows —
// pre-fix data where tools lived only in Extra) fall back to Content-only
// hydration; the model won't see structured tool history for those turns,
// but the alternative — pretending the tool_use blocks existed by fabricating
// ids — would 400 on Anthropic replay.
//
// Orphan-tool_call defence (last line of protection against pairing bugs):
// if an assistant row declares a ToolCalls id with no matching Role=tool row
// in the same list, the whole ToolCalls field is stripped from that message
// and a warn is logged. Claude requires strict tool_use ↔ tool_result
// pairing; a stray tool_use with no tool_result would 400.
//
// When `active` is non-nil, the rows it has folded are dropped and a
// synthetic user message carrying the summary is prepended in their place.
// The drop happens BEFORE haveResult is built, so a surviving assistant row
// whose tool results were folded away is correctly seen as orphaned and
// stripped, rather than replayed against results the model can't see.
func toSchemaMessages(convID string, rows []model.Message, multimodalEnabled bool, active *model.Compaction) []*schema.Message {
	return toSchemaMessagesWithBudget(
		convID,
		rows,
		multimodalEnabled,
		active,
		multimodal.NewImageBudget(),
	)
}

func toSchemaMessagesWithBudget(
	convID string,
	rows []model.Message,
	multimodalEnabled bool,
	active *model.Compaction,
	imageBudget *multimodal.ImageBudget,
) []*schema.Message {
	rows = compaction.ActiveRows(rows, active)

	// Pass 1: collect all tool_call_ids we actually have tool_result rows for.
	haveResult := make(map[string]struct{})
	for _, r := range rows {
		if r.Role == string(schema.Tool) && r.ToolCallID != "" {
			haveResult[r.ToolCallID] = struct{}{}
		}
	}

	out := make([]*schema.Message, 0, len(rows)+1)
	if summary := compaction.SummaryMessage(active); summary != "" {
		out = append(out, schema.UserMessage(summary))
	}
	// Reserve the shared budget for recent history first. Start builds the
	// current user message before entering this function, so fresh attachments
	// always have priority over replayed images.
	builtUsers := make(map[int]*schema.Message)
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Role == string(schema.User) {
			builtUsers[i] = multimodal.BuildUserMessageWithBudget(
				rows[i].Content,
				multimodalEnabled,
				imageBudget,
			)
		}
	}
	for i, r := range rows {
		// User rows may carry [image: /abs/path] markers that need to be
		// expanded into multipart image blocks for the model. Delegate to
		// the same helper Start uses so the wire shape is identical
		// whether a message is being sent for the first time or replayed
		// from history.
		if r.Role == string(schema.User) {
			m := builtUsers[i]
			// preserve reasoning if any (rare for user rows but harmless)
			if r.ReasoningContent != "" {
				m.ReasoningContent = r.ReasoningContent
			}
			out = append(out, m)
			continue
		}
		m := &schema.Message{
			Role:             schema.RoleType(r.Role),
			Content:          r.Content,
			ReasoningContent: r.ReasoningContent,
			ToolCallID:       r.ToolCallID,
			ToolName:         r.ToolName,
		}
		if r.ToolCalls != "" {
			var recs []stream.ToolCallRecord
			if err := json.Unmarshal([]byte(r.ToolCalls), &recs); err != nil {
				log.Printf("toSchemaMessages: unmarshal ToolCalls (convID=%s seq=%d): %v",
					convID, r.Seq, err)
			} else if len(recs) > 0 {
				// Orphan defence: if ANY declared tool_call has no matching
				// tool_result row, drop the whole ToolCalls list. Splitting
				// the array would leave a partial tool_use that still 400s.
				orphaned := make([]string, 0)
				for _, rec := range recs {
					if _, ok := haveResult[rec.ID]; !ok {
						orphaned = append(orphaned, rec.ID)
					}
				}
				if len(orphaned) > 0 {
					log.Printf("toSchemaMessages: orphan tool_call detected, stripping ToolCalls "+
						"(convID=%s seq=%d orphan_ids=%v)", convID, r.Seq, orphaned)
				} else {
					tcs := make([]schema.ToolCall, 0, len(recs))
					for _, rec := range recs {
						tcs = append(tcs, schema.ToolCall{
							ID:   rec.ID,
							Type: "function",
							Function: schema.FunctionCall{
								Name:      rec.Name,
								Arguments: rec.ArgsJSON,
							},
						})
					}
					m.ToolCalls = tcs
				}
			}
		}
		// An assistant message only carries payload the API recognises if it
		// has content or tool_calls; reasoning_content does not count, and
		// sending a row with neither earns a 400. Rows like that are real:
		// a run cancelled mid-thinking persists its reasoning and nothing
		// else, and the orphan defence above can empty out a tool-call-only
		// row. They stay in the DB for the transcript, but never go on the
		// wire.
		if r.Role == string(schema.Assistant) && m.Content == "" && len(m.ToolCalls) == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}

// titleMaxRunes caps the conversation title. The source is a whole user
// message, which routinely runs to thousands of characters, and the column is
// varchar(255) — so the name of this function was a promise it did not keep.
const titleMaxRunes = 60

// truncateForTitle derives a conversation title from the first user message:
// its opening line, whitespace collapsed, cut to length.
//
// The cut counts runes rather than bytes. Titles here are usually Chinese, and
// slicing a UTF-8 string at an arbitrary byte offset lands in the middle of a
// multi-byte character and produces a replacement glyph at the end of every
// long title.
func truncateForTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// A prompt's first line is nearly always the request itself; the rest is
	// the supporting detail nobody wants in a sidebar entry.
	line := s
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.Join(strings.Fields(line), " ")

	runes := []rune(line)
	if len(runes) <= titleMaxRunes {
		return line
	}
	return strings.TrimRight(string(runes[:titleMaxRunes]), " ") + "…"
}
