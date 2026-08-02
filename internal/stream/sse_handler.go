package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/hitl"
)

// Frame is the wire shape of one SSE event. Field omitempty so each frame
// type only carries what it needs. New frame types should add new fields
// here rather than overloading existing ones.
type Frame struct {
	Type string `json:"type"`

	// Agent identifies which ADK agent produced this frame. Empty (or equal
	// to the run's root agent name) means the supervisor itself. A non-root
	// value (e.g. "deep_research") tells the UI to render this event as a
	// nested sub-agent activity.
	Agent string `json:"agent,omitempty"`

	// ParentToolCallID links a sub-agent frame back to the root agent's
	// tool_call that triggered the sub-agent run. Only set when Agent is
	// non-empty. Mirrors the persisted SubAgentEvent.ParentToolCallID so
	// the live stream and the message-history replay carry the same shape.
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`

	Content string `json:"content,omitempty"` // text / thinking / tool_result(ok=true)
	Message string `json:"message,omitempty"` // error.message

	// Tool call / result fields
	ID       string `json:"id,omitempty"`        // tc-N, links tool_call ↔ tool_result
	Name     string `json:"name,omitempty"`      // tool name
	ArgsJSON string `json:"args_json,omitempty"` // tool_call arguments JSON
	OK       *bool  `json:"ok,omitempty"`        // tool_result success flag (pointer so we can emit even when false)
	Error    string `json:"error,omitempty"`     // tool_result(ok=false) error message

	// Project bound (emitted by create_workspace tool, not by this handler)
	ProjectID     string `json:"project_id,omitempty"`
	ProjectName   string `json:"project_name,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`

	// approval_required / question_required frame — the tool_call above is
	// paused waiting for the user. The frontend keys off (CheckpointID +
	// InterruptID) when POSTing the user's decision or answers.
	CheckpointID string `json:"checkpoint_id,omitempty"`
	InterruptID  string `json:"interrupt_id,omitempty"`

	// approval_required frame — the marshalled effect the approval decision
	// was made from. The card prefers it over re-reading ArgsJSON, because a
	// tool the frontend has never heard of (an MCP server's, say) still has a
	// describable effect. Empty for approvals checkpointed before this
	// existed, and the card falls back to its old argument summary.
	EffectJSON string `json:"effect_json,omitempty"`

	// question_required frame — JSON-encoded []hitl.Question that the user
	// should answer. Kept as string so the wire schema stays flat and the
	// frontend can lazily decode.
	QuestionsJSON string `json:"questions_json,omitempty"`

	// Usage
	Prompt int `json:"prompt,omitempty"`
	Reply  int `json:"completion,omitempty"`
	Total  int `json:"total,omitempty"`

	// context_compacted frame — history up to CompactedSeq was folded into a
	// summary before this run started. The UI draws a marker at that point;
	// the summary text itself is never shown.
	CompactionID  uint64 `json:"compaction_id,omitempty"`
	CompactedSeq  int    `json:"compacted_seq,omitempty"`
	ReplacedCount int    `json:"replaced_count,omitempty"`
}

// ApprovalInfo is what an approval middleware attaches to tool.Interrupt so
// downstream (adk_handler → SSE) can render an approval card. Living in the
// stream package keeps the type import graph one-way: approval imports stream,
// stream stays leaf-level.
type ApprovalInfo struct {
	// Tool is the tool name being called.
	Tool string
	// Args is the raw JSON arguments string as the model produced them.
	Args string
	// CallID is the eino tool call id (tc-N), so the UI can pin the
	// approval card visually next to the tool_call frame that spawned it.
	CallID string
	// EffectJSON is the marshalled effect.Effect the approval decision was
	// made from. Carried as a string rather than the struct so this package
	// stays leaf-level and doesn't import effect just to hold the payload.
	//
	// The card falls back to summarising Args when this is empty, which is
	// what happens for approvals checkpointed before the field existed.
	EffectJSON string
}

// QuestionInfo is what the ask_user tool attaches to tool.Interrupt so the
// UI can render a question card. Sibling of ApprovalInfo — both go through
// the same runner checkpoint / resume path, only the payload shape differs.
type QuestionInfo struct {
	// Questions carries the question list the tool asked. Stored on the
	// wire as JSON via Frame.QuestionsJSON.
	Questions []hitl.Question
	// CallID lets the frontend pin the question card visually next to the
	// tool_call frame that spawned it (mirrors ApprovalInfo.CallID).
	CallID string
}

// ToolCallRecord captures the shape of a single tool_call decided by an
// assistant turn. Persisted verbatim on the assistant message's ToolCalls
// column so history replay can reconstruct schema.Message.ToolCalls.
type ToolCallRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ArgsJSON string `json:"args_json,omitempty"`
}

// ToolResultRecord captures the outcome of one tool call. Persisted as its
// own Role=tool row so the OpenAI/Anthropic tool_use ↔ tool_result pairing
// survives across turns.
type ToolResultRecord struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

// AssistantTurnRecord is the persisted portion of a single root-agent
// assistant turn — its content/reasoning (already concatenated from the
// stream) plus any tool_calls it emitted.
type AssistantTurnRecord struct {
	Content          string           `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallRecord `json:"tool_calls,omitempty"`
}

// TurnRecord = one assistant message + the tool results that followed it.
// The service layer serialises these into DB rows (1 assistant + N tool) in
// order, preserving the tool_use / tool_result pairing Claude/OpenAI require.
type TurnRecord struct {
	Assistant   AssistantTurnRecord `json:"assistant"`
	ToolResults []ToolResultRecord  `json:"tool_results,omitempty"`
}

// ToolEventRecord is the persisted shape of one tool call within an agent run.
// Stored as a JSON array on message.Extra so the frontend can re-render tool
// cards when the conversation is reloaded from history.
type ToolEventRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ArgsJSON string `json:"args_json,omitempty"`
	OK       bool   `json:"ok"`
	Content  string `json:"content,omitempty"`
	Error    string `json:"error,omitempty"`
}

// SubAgentEvent is one persisted event from a sub-agent's internal run.
//
// Stored as an ordered array on message.Extra so the frontend can replay the
// nested deep_research / sub-agent timeline after the conversation reloads.
// `ParentToolCallID` links the sub-event back to the root agent's tool_call
// that triggered the sub-agent, which keeps the door open for future nested
// UI (collapse/expand under that tool card) without needing to re-derive
// attribution from timing.
type SubAgentEvent struct {
	Seq              int    `json:"seq"`
	Agent            string `json:"agent"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	Type             string `json:"type"` // thinking | text | tool_call | tool_result | error

	// Optional payload by Type
	Content    string `json:"content,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	ArgsJSON   string `json:"args_json,omitempty"`
	OK         *bool  `json:"ok,omitempty"`
	Error      string `json:"error,omitempty"`
}

// RunCollector accumulates the full record of a single agent run so the
// service layer can persist it. It captures:
//   - root agent's ChatModel content + reasoning (concatenated)
//   - root agent's tool call / result events in order
//   - sub-agent events kept separately so they don't pollute the root
//     agent's final message content
//
// Thread safety: a single mutex guards all fields. The internal WaitGroup
// tracks pending OnEndWithStreamOutputFn goroutines so callers can Wait()
// for the run to fully drain before persisting.
type RunCollector struct {
	mu        sync.Mutex
	wg        sync.WaitGroup
	content   strings.Builder
	reasoning strings.Builder
	tools     []ToolEventRecord
	subEvents []SubAgentEvent
	// turns is the turn-structured view of the same root-agent event stream.
	// Populated by OpenTurn / AttachToolResult in parallel with the flat
	// content/tools fields above. Used by the service layer to persist raw
	// per-message rows (assistant + tool) so Claude's strict tool_use ↔
	// tool_result pairing survives across conversation turns.
	turns []TurnRecord
	// totalTokens is the last provider-reported context size for the ROOT
	// agent. Sub-agent usage is never recorded here — each sub-agent runs
	// its own private context, so its totals say nothing about how full the
	// main thread's window is. The compaction estimator uses this as its
	// baseline, so mixing the two would badly skew the threshold.
	totalTokens int
}

func NewRunCollector() *RunCollector {
	return &RunCollector{}
}

func (c *RunCollector) Wait() {
	c.wg.Wait()
}

func (c *RunCollector) Content() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content.String()
}

func (c *RunCollector) Reasoning() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reasoning.String()
}

// SetTotalTokens records root-agent usage. Callers must gate on isRoot.
func (c *RunCollector) SetTotalTokens(n int) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalTokens = n
}

func (c *RunCollector) TotalTokens() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalTokens
}

func (c *RunCollector) Tools() []ToolEventRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tools) == 0 {
		return nil
	}
	out := make([]ToolEventRecord, len(c.tools))
	copy(out, c.tools)
	return out
}

// SubEvents returns the recorded sub-agent timeline in arrival order.
func (c *RunCollector) SubEvents() []SubAgentEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.subEvents) == 0 {
		return nil
	}
	out := make([]SubAgentEvent, len(c.subEvents))
	copy(out, c.subEvents)
	return out
}

// ToolNameByID looks up the tool name we previously recorded for the given
// tool_call ID. Used as a last-resort fallback when an event arrives with
// neither a MessageVariant.ToolName nor a populated msg.Name (some provider
// SDKs leave tool result names empty).
func (c *RunCollector) ToolNameByID(id string) string {
	if id == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.tools {
		if t.ID == id {
			return t.Name
		}
	}
	for _, e := range c.subEvents {
		if e.ToolCallID == id && e.Name != "" {
			return e.Name
		}
	}
	return ""
}

// AppendSubEvent records one sub-agent event. Seq is assigned by the
// collector based on the current length so order is monotonic. Callers
// fill in the rest of the fields.
//
// Consecutive thinking/text chunks from the same agent under the same
// parent tool_call are merged into the previous entry so the persisted
// sub_events array carries one prose block per agent turn instead of
// one entry per token — mirrors what the frontend does for the live
// SSE stream.
func (c *RunCollector) AppendSubEvent(ev SubAgentEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ev.Type == "thinking" || ev.Type == "text" {
		if n := len(c.subEvents); n > 0 {
			last := &c.subEvents[n-1]
			if last.Type == ev.Type &&
				last.Agent == ev.Agent &&
				last.ParentToolCallID == ev.ParentToolCallID {
				last.Content += ev.Content
				return
			}
		}
	}
	ev.Seq = len(c.subEvents) + 1
	c.subEvents = append(c.subEvents, ev)
}

func (c *RunCollector) appendContent(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.content.WriteString(s)
}

func (c *RunCollector) appendReasoning(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reasoning.WriteString(s)
}

func (c *RunCollector) startTool(id, name, args string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tools = append(c.tools, ToolEventRecord{ID: id, Name: name, ArgsJSON: args})
}

// OpenTurn records the end-of-stream state of one root-agent assistant turn.
// Called from drainAssistantStream once the message stream has been fully
// consumed and ConcatMessages has produced a `full` message. Subsequent
// AttachToolResult calls will bind to this turn until the next OpenTurn.
func (c *RunCollector) OpenTurn(content, reasoning string, toolCalls []ToolCallRecord) {
	// Skip empty phantom turns: some providers emit an empty streaming event
	// after tool results just to close the loop before the next real turn.
	if content == "" && reasoning == "" && len(toolCalls) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns = append(c.turns, TurnRecord{
		Assistant: AssistantTurnRecord{
			Content:          content,
			ReasoningContent: reasoning,
			ToolCalls:        toolCalls,
		},
	})
}

// AttachToolResult binds one root-agent tool result to the most recent turn.
// Only called for events with AgentName == rootName; sub-agent tool events
// live in subEvents instead.
func (c *RunCollector) AttachToolResult(r ToolResultRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.turns) == 0 {
		// Defensive: tool_result arrived before any OpenTurn. ADK's event
		// ordering shouldn't produce this, but don't drop it — synthesize a
		// placeholder turn so the result isn't orphaned.
		c.turns = append(c.turns, TurnRecord{})
	}
	last := &c.turns[len(c.turns)-1]
	last.ToolResults = append(last.ToolResults, r)
}

// Turns returns a defensive copy of the turn-structured record. Callers may
// mutate the returned slice freely; the collector's internal state is
// untouched.
func (c *RunCollector) Turns() []TurnRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.turns) == 0 {
		return nil
	}
	out := make([]TurnRecord, len(c.turns))
	for i, t := range c.turns {
		out[i] = TurnRecord{Assistant: t.Assistant}
		if len(t.Assistant.ToolCalls) > 0 {
			out[i].Assistant.ToolCalls = append([]ToolCallRecord(nil), t.Assistant.ToolCalls...)
		}
		if len(t.ToolResults) > 0 {
			out[i].ToolResults = append([]ToolResultRecord(nil), t.ToolResults...)
		}
	}
	return out
}

func (c *RunCollector) finishTool(id string, ok bool, content, errMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id == "" {
		return
	}
	for i := range c.tools {
		if c.tools[i].ID != id {
			continue
		}
		c.tools[i].OK = ok
		c.tools[i].Content = content
		c.tools[i].Error = errMsg
		return
	}
}

// Encode serializes a Frame into an SSE-formatted `data: ...\n\n` byte slice.
// Exported so other packages (tools) can push frames directly into a buffer.
func Encode(f Frame) []byte {
	data, _ := json.Marshal(f)
	out := make([]byte, 0, len(data)+8)
	out = append(out, []byte("data: ")...)
	out = append(out, data...)
	out = append(out, '\n', '\n')
	return out
}

// boolPtr is a tiny helper for the OK field above.
func boolPtr(b bool) *bool { return &b }

// NewSSEHandler builds an eino callback handler that translates model + tool
// events into SSE frames written to buf. Tool call IDs are allocated by a
// per-handler counter (one handler per agent run / per stream).
//
// If collector is non-nil, the same events are also accumulated into the
// collector so the caller can persist a full record of the run after
// collector.Wait() returns.
//
// Buf lifecycle (finish / error semantics) is managed by the caller; this
// handler only appends frames.
func NewSSEHandler(buf *StreamBuffer, collector *RunCollector) callbacks.Handler {
	var toolCounter int64
	var lastToolID atomic.Value // string — id of the in-flight tool call

	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info.Component != components.ComponentOfTool {
				return ctx
			}
			n := atomic.AddInt64(&toolCounter, 1)
			id := fmt.Sprintf("tc-%d", n)
			lastToolID.Store(id)
			args := ""
			if ti := tool.ConvCallbackInput(input); ti != nil {
				args = ti.ArgumentsInJSON
			}
			buf.Append(Encode(Frame{
				Type:     "tool_call",
				ID:       id,
				Name:     info.Name,
				ArgsJSON: args,
			}))
			if collector != nil {
				collector.startTool(id, info.Name, args)
			}
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info.Component != components.ComponentOfTool {
				return ctx
			}
			to := tool.ConvCallbackOutput(output)
			content := ""
			if to != nil {
				content = to.Response
			}
			id, _ := lastToolID.Load().(string)
			buf.Append(Encode(Frame{
				Type:    "tool_result",
				ID:      id,
				Name:    info.Name,
				OK:      boolPtr(true),
				Content: content,
			}))
			if collector != nil {
				collector.finishTool(id, true, content, "")
			}
			return ctx
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo, sr *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			if info.Component != components.ComponentOfChatModel {
				sr.Close()
				return ctx
			}
			if collector != nil {
				collector.wg.Add(1)
			}
			go func() {
				defer func() {
					if collector != nil {
						collector.wg.Done()
					}
				}()
				defer sr.Close()
				for {
					raw, err := sr.Recv()
					if errors.Is(err, io.EOF) {
						return
					}
					if err != nil {
						buf.Append(Encode(Frame{Type: "error", Message: err.Error()}))
						return
					}
					mo := model.ConvCallbackOutput(raw)
					if mo == nil || mo.Message == nil {
						continue
					}
					if mo.Message.ReasoningContent != "" {
						buf.Append(Encode(Frame{Type: "thinking", Content: mo.Message.ReasoningContent}))
						if collector != nil {
							collector.appendReasoning(mo.Message.ReasoningContent)
						}
					}
					if mo.Message.Content != "" {
						buf.Append(Encode(Frame{Type: "text", Content: mo.Message.Content}))
						if collector != nil {
							collector.appendContent(mo.Message.Content)
						}
					}
					if mo.TokenUsage != nil {
						buf.Append(Encode(Frame{
							Type:   "usage",
							Prompt: mo.TokenUsage.PromptTokens,
							Reply:  mo.TokenUsage.CompletionTokens,
							Total:  mo.TokenUsage.TotalTokens,
						}))
					}
				}
			}()
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			if info.Component == components.ComponentOfTool {
				id, _ := lastToolID.Load().(string)
				buf.Append(Encode(Frame{
					Type:  "tool_result",
					ID:    id,
					Name:  info.Name,
					OK:    boolPtr(false),
					Error: err.Error(),
				}))
				if collector != nil {
					collector.finishTool(id, false, "", err.Error())
				}
				return ctx
			}
			buf.Append(Encode(Frame{Type: "error", Message: err.Error()}))
			return ctx
		}).
		Build()
}

// FinalizeOK closes the stream successfully — appends a `done` frame
// and marks the buffer complete.
func FinalizeOK(buf *StreamBuffer) {
	buf.Append(Encode(Frame{Type: "done"}))
	buf.Finish()
}

// FinalizeErr closes the stream with an error.
func FinalizeErr(buf *StreamBuffer, err error) {
	buf.Append(Encode(Frame{Type: "error", Message: err.Error()}))
	buf.Finish()
}
