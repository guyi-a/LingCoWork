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

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/agent/multimodal"
	"github.com/guyi-a/Interview-Agent/internal/agent/toolerr"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/compaction"
	"github.com/guyi-a/Interview-Agent/internal/hitl"
	"github.com/guyi-a/Interview-Agent/internal/instructions"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

var (
	ErrWorkspaceRequired = errors.New("a workspace project is required")
	ErrProjectNotFound   = errors.New("workspace project not found")
	ErrProjectMismatch   = errors.New("conversation is already bound to another project")
)

type ChatService struct {
	runner        *adk.Runner
	rootName      string
	manager       *stream.Manager
	convRepo      *repository.ConversationRepo
	msgRepo       *repository.MessageRepo
	projectRepo   *repository.ProjectRepo
	instructions  *instructions.Store
	pending       *approval.PendingStore
	approvalModes *approval.ModeStore
	multimodal    bool
	// compactor is nil when context compaction isn't configured; all of its
	// methods tolerate a nil receiver, so call sites don't branch.
	compactor *compaction.Compactor
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
	multimodal bool,
	compactor *compaction.Compactor,
) *ChatService {
	return &ChatService{
		runner:        runner,
		rootName:      rootName,
		manager:       manager,
		convRepo:      convRepo,
		msgRepo:       msgRepo,
		projectRepo:   projectRepo,
		instructions:  instructionStore,
		pending:       pending,
		approvalModes: approvalModes,
		multimodal:    multimodal,
		compactor:     compactor,
	}
}

func (s *ChatService) Get(id string) *stream.StreamBuffer {
	return s.manager.Get(id)
}

func (s *ChatService) IsStreaming(id string) bool {
	return s.manager.IsStreaming(id)
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
func (s *ChatService) Start(ctx context.Context, id, userMsg, instructionName, projectID string) (*stream.StreamBuffer, error) {
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

	if conv == nil || conv.ProjectID == nil || *conv.ProjectID == "" {
		if err := s.convRepo.SetProjectID(ctx, id, boundProjectID); err != nil {
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

	if err := s.msgRepo.Append(ctx, &model.Message{
		ConversationID: id,
		Role:           string(schema.User),
		Content:        prepared.content,
		Extra:          prepared.extra,
	}); err != nil {
		return nil, err
	}

	if title := truncateForTitle(prepared.titleSource); title != "" {
		_ = s.convRepo.SetTitleIfEmpty(ctx, id, title)
	}

	workspaceCtx := s.workspaceContext(ctx, id)

	buf := s.manager.Create(id)
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

// Resume delivers the user's approval decision for an interrupted run and
// re-enters the same conversation's SSE stream with a fresh iterator. Returns
// (found, nil) on success, or (false, nil) if the interrupt id isn't known
// (already acted on, or stale). Errors from the ADK layer bubble up.
func (s *ChatService) Resume(convID, interruptID string, dec approval.Decision) (bool, error) {
	item, ok := s.pending.Take(convID, interruptID)
	if !ok {
		return false, nil
	}

	// 用户已经答了这条 pending。若队列里还有别的 pending，按当前队首 kind
	// 保留对应的 waiting_* 状态；否则视为 run 继续，切回 running。
	s.applyWaitingStatus(convID, "running")

	// The previous SSE buffer was Finish()ed when the interrupt drained the
	// iterator, so a resumed run can't Append into it. Replace with a fresh
	// buffer — the frontend will GET /chat/:id to reconnect and drain it.
	buf := s.manager.Create(convID)
	runCtx, cancel := context.WithCancel(context.Background())
	buf.SetCancel(cancel)

	go s.resumeAgent(runCtx, convID, item.CheckpointID, interruptID, dec, buf)
	return true, nil
}

// ResumeQuestion 走跟 Resume 一样的 checkpoint 恢复通道，只是 Targets 里塞
// 的是 hitl.Answers（gob 已在 hitl 包 init 里注册），由 ask_user 工具体通过
// GetResumeContext[hitl.Answers] 取回。
func (s *ChatService) ResumeQuestion(convID, interruptID string, answers hitl.Answers) (bool, error) {
	item, ok := s.pending.Take(convID, interruptID)
	if !ok {
		return false, nil
	}

	s.applyWaitingStatus(convID, "running")

	buf := s.manager.Create(convID)
	runCtx, cancel := context.WithCancel(context.Background())
	buf.SetCancel(cancel)

	go s.resumeAgent(runCtx, convID, item.CheckpointID, interruptID, answers, buf)
	return true, nil
}

// applyWaitingStatus 根据当前 pending 队列的队首 kind 更新会话状态：
//   - 队列空 → fallbackWhenEmpty（通常是 running / idle）
//   - 队首是 question → waiting_user（sidebar 展示"等待回复"）
//   - 队首是 approval → waiting_approval（sidebar 展示"等待审批"）
func (s *ChatService) applyWaitingStatus(convID, fallbackWhenEmpty string) {
	status := fallbackWhenEmpty
	if items := s.pending.List(convID); len(items) > 0 {
		if items[0].Kind == hitl.KindQuestion {
			status = "waiting_user"
		} else {
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

// GetApprovalMode returns the per-conversation approval mode, defaulting to
// approval.ModeDefault when the conversation has never explicitly set one
// (including after a server restart, which is intentional — see mode.go).
func (s *ChatService) GetApprovalMode(convID string) approval.Mode {
	return s.approvalModes.Get(convID)
}

// SetApprovalMode validates and stores the mode. Called from the HTTP handler.
func (s *ChatService) SetApprovalMode(convID string, m approval.Mode) error {
	return s.approvalModes.Set(convID, m)
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
	ctx = contextkey.WithBuffer(ctx, buf)
	ctx = toolerr.WithRegistry(ctx, toolerr.NewRegistry())

	collector := stream.NewRunCollector()
	sink := s.approvalSink(convID)

	_ = s.convRepo.SetAgentStatus(context.Background(), convID, "running")

	// Checkpoint id is stable per conversation — one active run at a time is
	// enforced by manager.Create above, so reusing convID as the eino
	// checkpoint id keeps resume lookups trivial.
	iter := s.runner.Run(ctx, msgs, adk.WithCheckPointID(convID))
	s.consumeAndPersist(ctx, convID, iter, sink, buf, collector, nil)
}

// resumeAgent 恢复被中断的 run。payload 会以 gob 塞进 ResumeParams.Targets
// 的 value —— 审批场景是 approval.Decision（gob 已在 approval 包 init 里注册），
// ask_user 场景是 hitl.Answers（gob 在 hitl 包 init 里注册）。类型分岔由
// 中断点自己在 GetResumeContext[T] 时按 T 做，service 层只负责传递。
func (s *ChatService) resumeAgent(
	ctx context.Context,
	convID, checkpointID, interruptID string,
	payload any,
	buf *stream.StreamBuffer,
) {
	ctx = contextkey.WithConversationID(ctx, convID)
	ctx = contextkey.WithBuffer(ctx, buf)
	ctx = toolerr.WithRegistry(ctx, toolerr.NewRegistry())

	collector := stream.NewRunCollector()
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
		Targets: map[string]any{interruptID: payload},
	})
	if err != nil {
		log.Printf("adk resume error (conv=%s): %v", convID, err)
		_ = s.convRepo.SetAgentStatus(context.Background(), convID, "idle")
		stream.FinalizeErr(buf, err)
		return
	}
	s.consumeAndPersist(ctx, convID, iter, sink, buf, collector, initialRouter)
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
	initialRouterState map[string]string,
) {
	if err := stream.ConsumeADKEvents(ctx, iter, s.rootName, convID, sink, buf, collector, initialRouterState); err != nil {
		log.Printf("adk runner error: %v", err)
		if perr := s.persistRun(convID, collector, false); perr != nil {
			log.Printf("persist run (on error path): %v", perr)
		}
		s.finalizeStatus(convID)
		stream.FinalizeErr(buf, err)
		return
	}

	interrupted := s.pending.HasPending(convID)
	if err := s.persistRun(convID, collector, interrupted); err != nil {
		log.Printf("persist run: %v", err)
	}
	_ = s.convRepo.Upsert(context.Background(), convID)
	s.finalizeStatus(convID)
	stream.FinalizeOK(buf)
}

// finalizeStatus 设定会话状态：无 pending → idle；有 pending 按队首 kind 区
// 分 waiting_approval / waiting_user。iterator 因中断自然结束时也走这里。
func (s *ChatService) finalizeStatus(convID string) {
	s.applyWaitingStatus(convID, "idle")
}

// persistRun serialises one completed run into raw per-message rows:
//
//	assistant (with ToolCalls JSON) → tool row × N → next assistant → ...
//
// Each turn's assistant ToolCalls are paired with their tool_result rows in
// the SAME batch, ensuring Claude's strict tool_use ↔ tool_result pairing
// survives on the next replay. If a turn is missing a tool_result (cancel /
// crash / early stream close), a placeholder "[canceled] tool did not run"
// row is inserted so the pairing stays intact.
//
// The entire batch is inserted in a single DB transaction via AppendMany;
// on failure nothing is committed, avoiding partial "half turn" state.
//
// Dual-write for UI compatibility: the last assistant row also carries a
// legacy Extra JSON blob (`tools[]` + `sub_events[]`) matching the pre-fix
// wire format, so the frontend's tool-card and sub-agent rendering paths
// keep working while the handler-side fold logic (see handler.fromModelMessage)
// is being validated.
func (s *ChatService) persistRun(convID string, collector *stream.RunCollector, skipMissingToolPadding bool) error {
	turns := collector.Turns()
	if len(turns) == 0 {
		// Nothing captured (e.g. an early ADK error before any event). Nothing
		// to persist — matches previous behaviour.
		return nil
	}
	subEvents := collector.SubEvents()
	legacyTools := collector.Tools()

	rows := make([]*model.Message, 0, 2*len(turns))
	// Extra dual-write lands on the last turn that actually emits an
	// assistant row (skip empty placeholder turns synthesized on resume).
	lastAssistantEmit := -1
	for i, t := range turns {
		if t.Assistant.Content != "" || t.Assistant.ReasoningContent != "" || len(t.Assistant.ToolCalls) > 0 {
			lastAssistantEmit = i
		}
	}

	for i, t := range turns {
		padded := t
		if !skipMissingToolPadding {
			padded = padMissingToolResults(t)
		}

		hasAssistant := padded.Assistant.Content != "" ||
			padded.Assistant.ReasoningContent != "" ||
			len(padded.Assistant.ToolCalls) > 0

		// Resume after HITL often delivers tool_result before any new
		// OpenTurn. AttachToolResult then synthesizes an empty TurnRecord
		// so the result isn't dropped. Emitting that empty assistant into
		// the DB would sit BETWEEN a prior run's tool_calls and this run's
		// tool row, which DeepSeek/OpenAI reject (400: tool_calls must be
		// followed by tool messages). Persist tool rows only in that case.
		if hasAssistant {
			assistantRow := &model.Message{
				ConversationID:   convID,
				Role:             string(schema.Assistant),
				Content:          padded.Assistant.Content,
				ReasoningContent: padded.Assistant.ReasoningContent,
			}
			if len(padded.Assistant.ToolCalls) > 0 {
				if b, err := json.Marshal(padded.Assistant.ToolCalls); err == nil {
					assistantRow.ToolCalls = string(b)
				} else {
					log.Printf("marshal ToolCalls (convID=%s): %v", convID, err)
				}
			}
			if i == lastAssistantEmit {
				// Anchor the compaction estimator on the provider's own·
				// count for the whole run rather than re-deriving it from
				// characters. Only the final row gets it: the intermediate
				// ReAct turns are prefixes of this same context.
				assistantRow.TotalTokens = collector.TotalTokens()
				payload := map[string]any{}
				if len(legacyTools) > 0 {
					payload["tools"] = legacyTools
				}
				if len(subEvents) > 0 {
					payload["sub_events"] = subEvents
				}
				if len(payload) > 0 {
					if data, jerr := json.Marshal(payload); jerr == nil {
						assistantRow.Extra = string(data)
					} else {
						log.Printf("marshal extra (convID=%s): %v", convID, jerr)
					}
				}
			}
			rows = append(rows, assistantRow)
		}

		for _, tr := range padded.ToolResults {
			// tool row Content is what the LLM sees on next replay — it must
			// carry enough info for the model to react (success text or error
			// description). We fold Error into Content for failures so the
			// model doesn't lose the reason on replay.
			content := tr.Content
			if !tr.OK {
				if content == "" {
					content = tr.Error
				} else if tr.Error != "" && !strings.Contains(content, tr.Error) {
					content = content + " (" + tr.Error + ")"
				}
				if content == "" {
					content = "[error] tool failed"
				}
			}
			toolRow := &model.Message{
				ConversationID: convID,
				Role:           string(schema.Tool),
				Content:        content,
				ToolCallID:     tr.CallID,
				ToolName:       tr.Name,
			}
			// Extra encodes ok/error/cancelled precisely for the UI fold path
			// so the frontend can render the right card without parsing
			// Content. Plain successes skip Extra entirely (nil ≡ ok:true
			// default in the handler-side fold).
			//
			// A denial is a success that still has to be written out: it
			// returns ok=true, so Cancelled is the only thing separating it
			// from a normal result.
			if !tr.OK || tr.Cancelled {
				payload := map[string]any{"ok": tr.OK}
				if tr.Error != "" {
					payload["error"] = tr.Error
				}
				if tr.Cancelled {
					payload["cancelled"] = true
				}
				if b, jerr := json.Marshal(payload); jerr == nil {
					toolRow.Extra = string(b)
				}
			}
			rows = append(rows, toolRow)
		}
	}

	return s.msgRepo.AppendMany(context.Background(), rows)
}

// padMissingToolResults ensures every ToolCall in the turn's assistant
// message has a matching ToolResult. Missing ones (cancel / crash mid-turn)
// get a placeholder result so the persisted history stays a valid
// tool_use ↔ tool_result pairing — otherwise the next replay would 400 from
// Claude with "tool_use ids without matching tool_result".
func padMissingToolResults(t stream.TurnRecord) stream.TurnRecord {
	if len(t.Assistant.ToolCalls) == 0 {
		return t
	}
	seen := make(map[string]bool, len(t.ToolResults))
	for _, r := range t.ToolResults {
		seen[r.CallID] = true
	}
	out := t
	for _, tc := range t.Assistant.ToolCalls {
		if !seen[tc.ID] {
			out.ToolResults = append(out.ToolResults, stream.ToolResultRecord{
				CallID:    tc.ID,
				Name:      tc.Name,
				OK:        false,
				Content:   stream.CanceledPlaceholderPrefix + " tool did not run",
				Error:     "canceled",
				Cancelled: true,
			})
		}
	}
	return out
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
