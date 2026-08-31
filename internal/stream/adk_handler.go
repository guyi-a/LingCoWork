package stream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/toolerr"
)

// InterruptSink is called every time an ADK agent event carries a HITL
// interrupt (typically an approval request). The service layer implements it
// so it can remember the (checkpointID, interruptID, info) tuple and later
// call runner.ResumeWithParams with the user's decision.
type InterruptSink interface {
	Record(checkpointID, interruptID string, info any)
}

// ConsumeADKEvents drives an ADK Runner's AsyncIterator, translating every
// AgentEvent into SSE frames written to buf, and accumulating data into
// collector for later persistence.
//
// Routing rules (matching docs/adk-api-notes.md §8):
//   - ev.Err != nil                      → "error" frame, return that error
//   - ev.Action.Interrupted != nil       → one "approval_required" frame per
//     InterruptContext, sink.Record for
//     each, then keep draining (interrupt
//     ends the iter naturally after)
//   - ev.AgentName == rootName           → root path:
//     streaming/Assistant → per-chunk "thinking"/"text"; concat → "tool_call"
//     Tool                → "tool_result"
//     (writes to collector.content / reasoning / tools)
//   - ev.AgentName != rootName           → sub-agent path:
//     same shape of SSE frames, but each frame.agent = ev.AgentName,
//     writes go to collector.subEvents instead so the persisted root
//     message content stays clean. Sub-agent tool events are linked to
//     the root tool_call that triggered them via parent_tool_call_id.
//
// Returns nil on clean stream exhaustion (caller calls FinalizeOK then),
// or the first error encountered (caller calls FinalizeErr then).
// Does NOT emit "done" or finalize the buffer — caller's responsibility.
//
// initialRouterState pre-populates the sub-agent router's open-parents map
// (name → parent tool_call_id). Pass nil for a fresh Run; the resume path
// hydrates this from persisted history so a sub-agent event that arrives
// AFTER an interrupt/resume boundary still knows which supervisor-level
// tool_call it belongs to. Without it, resume-run sub-agent events end up
// with parentToolCallId="" and render as orphans in the transcript.
func ConsumeADKEvents(
	ctx context.Context,
	iter *adk.AsyncIterator[*adk.AgentEvent],
	rootName string,
	checkpointID string,
	sink InterruptSink,
	buf *StreamBuffer,
	collector *RunCollector,
	initialRouterState map[string]string,
) error {
	router := &subAgentRouter{rootName: rootName, active: map[string]string{}}
	for name, callID := range initialRouterState {
		router.active[name] = callID
	}

	for {
		// Honor ctx cancel — if the caller cancelled the run, stop draining.
		// The iterator itself doesn't take a ctx, so we check here.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		ev, ok := iter.Next()
		if !ok {
			return nil
		}

		if ev.Err != nil {
			// eino surfaces interrupts as errors on the iter when they
			// originate deep inside a sub-agent (adk.NewAgentTool wraps
			// them with tool.CompositeInterrupt, which bubbles up as an
			// error rather than as Action.Interrupted). Detect the
			// interrupt signal and route through the same approval frame
			// path — treating it as a hard failure would spray raw gob
			// bytes into the transcript.
			if info, ok := compose.ExtractInterruptInfo(ev.Err); ok {
				emitInterrupt(info.InterruptContexts, checkpointID, sink, buf)
				continue
			}
			var sig *adk.InterruptSignal
			if errors.As(ev.Err, &sig) {
				emitInterrupt(signalToContexts(sig), checkpointID, sink, buf)
				continue
			}
			// Diagnostic: if we get here on what looks like an interrupt
			// (error string prefixed "interrupt signal:"), we need to know
			// the concrete Go type so the check chain above can be extended.
			// Kept behind a log rather than a panic so the run still fails
			// loudly but predictably; delete once the wrapping is identified.
			log.Printf("[adk_handler] unrecognised interrupt-shaped err (type=%T, str=%q)", ev.Err, ev.Err.Error())
			return ev.Err
		}

		if ev.Action != nil && ev.Action.Interrupted != nil {
			emitInterrupt(ev.Action.Interrupted.InterruptContexts, checkpointID, sink, buf)
			// Fall through: eino may still enqueue trailing events, but the
			// iter's normal termination follows an interrupt.
			continue
		}

		if ev.Output == nil || ev.Output.MessageOutput == nil {
			// Action-only event (Exit / TransferToAgent) we don't render.
			continue
		}

		isRoot := ev.AgentName == rootName
		mv := ev.Output.MessageOutput

		switch {
		case mv.IsStreaming:
			if err := drainAssistantStream(isRoot, ev.AgentName, router, mv.MessageStream, buf, collector); err != nil {
				return err
			}

		case mv.Role == schema.Tool:
			if mv.Message == nil {
				continue
			}
			emitToolResult(ctx, isRoot, ev.AgentName, router, mv.ToolName, mv.Message, buf, collector)

		default:
			// Non-streaming assistant message (EnableStreaming=false path).
			// Our service runs with EnableStreaming=true, so this is mostly
			// defensive — but handle it so we don't silently swallow content.
			if mv.Message == nil {
				continue
			}
			emitNonStreamAssistant(isRoot, ev.AgentName, router, mv.Message, buf, collector)
		}
	}
}

// signalToContexts flattens a core.InterruptSignal tree into the same
// []*adk.InterruptCtx shape adk.InterruptInfo.InterruptContexts uses.
// Mirrors what core.ToInterruptContexts does internally: walk the tree,
// collect signals marked IsRootCause=true (i.e. the ORIGINAL interrupt
// site — our approval middleware inside a sub-agent), threading Parent
// pointers so the caller can still traverse the wrapper chain.
//
// We need this because sub-agent-as-tool interrupts arrive as raw
// *adk.InterruptSignal via ev.Err, and eino's public ExtractInterruptInfo
// only recognises compose-wrapped errors, not the raw signal.
func signalToContexts(is *adk.InterruptSignal) []*adk.InterruptCtx {
	if is == nil {
		return nil
	}
	var roots []*adk.InterruptCtx
	var walk func(*adk.InterruptSignal, *adk.InterruptCtx)
	walk = func(sig *adk.InterruptSignal, parent *adk.InterruptCtx) {
		cur := &adk.InterruptCtx{
			ID:          sig.ID,
			Address:     sig.Address,
			Info:        sig.InterruptInfo.Info,
			IsRootCause: sig.InterruptInfo.IsRootCause,
			Parent:      parent,
		}
		if cur.IsRootCause {
			roots = append(roots, cur)
		}
		for _, sub := range sig.Subs {
			walk(sub, cur)
		}
	}
	walk(is, nil)
	return roots
}

// emitInterrupt turns InterruptContexts into per-context SSE frames + sink
// notifications. The frame type depends on payload type:
//   - *ApprovalInfo → "approval_required"（工具调用等待批准 / 拒绝）
//   - *QuestionInfo → "question_required"（ask_user 等待用户回答）
//   - 其他类型 → 走 "approval_required" 兜底空 frame，UI 至少能显示"有 pending"
//     而不是崩掉
//
// Callers pass the flat contexts slice directly rather than a wrapper so
// this function serves both Action.Interrupted (top-level) and
// compose.ExtractInterruptInfo (sub-agent bubbled-as-error) paths.
func emitInterrupt(
	contexts []*adk.InterruptCtx,
	checkpointID string,
	sink InterruptSink,
	buf *StreamBuffer,
) {
	for _, ic := range contexts {
		if ic == nil {
			continue
		}
		frame := Frame{
			CheckpointID: checkpointID,
			InterruptID:  ic.ID,
		}
		switch info := ic.Info.(type) {
		case *ApprovalInfo:
			frame.Type = "approval_required"
			if info != nil {
				frame.ID = info.CallID
				frame.Name = info.Tool
				frame.ArgsJSON = info.Args
				frame.EffectJSON = info.EffectJSON
			}
		case *QuestionInfo:
			frame.Type = "question_required"
			if info != nil {
				frame.ID = info.CallID
				// Questions 通过 JSON 塞进扁平字段，前端拿到懒解析。
				if raw, err := json.Marshal(info.Questions); err == nil {
					frame.QuestionsJSON = string(raw)
				}
			}
		case *PlanInfo:
			frame.Type = "plan_required"
			if info != nil {
				frame.ID = info.CallID
				frame.PlanJSON = info.PlanJSON
			}
		default:
			// 未知 payload 类型：给个通用 approval_required 让 UI 不炸，
			// 但不带具体名字/参数，方便定位"哪种 interrupt 没接上"。
			frame.Type = "approval_required"
		}
		buf.Append(Encode(frame))
		if sink != nil {
			sink.Record(checkpointID, ic.ID, ic.Info)
		}
	}
}

// subAgentRouter tracks the linkage between root-level tool_calls and the
// sub-agent events that run inside them. When the root agent calls
// deep_research with id=toolu_X, every subsequent event from
// ev.AgentName="deep_research" is attributed to toolu_X via
// parent_tool_call_id, until the matching root-level tool_result arrives.
//
// Single-threaded (only ConsumeADKEvents touches it) so no lock needed.
type subAgentRouter struct {
	rootName string
	active   map[string]string // sub-agent name → root tool_call_id
}

func (r *subAgentRouter) noteRootToolCall(toolName, callID string) {
	if toolName == "" || callID == "" {
		return
	}
	r.active[toolName] = callID
}

func (r *subAgentRouter) noteRootToolResult(callID string) {
	for name, id := range r.active {
		if id == callID {
			delete(r.active, name)
			return
		}
	}
}

func (r *subAgentRouter) parentForAgent(agentName string) string {
	return r.active[agentName]
}

// agentFieldFor returns "" for root events (so the SSE frame omits the agent
// field and stays backward-compatible) and the sub-agent name otherwise.
func (r *subAgentRouter) agentFieldFor(isRoot bool, agentName string) string {
	if isRoot {
		return ""
	}
	return agentName
}

// drainAssistantStream consumes one streaming assistant event. Per chunk:
// emit text/thinking SSE frames; accumulate chunks for later concat.
// At stream end: ConcatMessages → emit tool_call frames for ToolCalls;
// emit usage frame if ResponseMeta carries token counts.
//
// Root-agent chunks feed collector.content/reasoning; sub-agent chunks
// feed collector.subEvents instead, keeping the root message's persisted
// content free of nested noise.
func drainAssistantStream(
	isRoot bool,
	agentName string,
	router *subAgentRouter,
	sr *schema.StreamReader[*schema.Message],
	buf *StreamBuffer,
	collector *RunCollector,
) error {
	if sr == nil {
		return nil
	}
	defer sr.Close()

	agentField := router.agentFieldFor(isRoot, agentName)
	parentID := ""
	if !isRoot {
		parentID = router.parentForAgent(agentName)
	}

	var chunks []*schema.Message
	for {
		chunk, err := sr.Recv()
		if err != nil {
			if isEOF(err) {
				break
			}
			return err
		}
		if chunk == nil {
			continue
		}
		chunks = append(chunks, chunk)

		if chunk.ReasoningContent != "" {
			buf.Append(Encode(Frame{
				Type:             "thinking",
				Agent:            agentField,
				ParentToolCallID: parentID,
				Content:          chunk.ReasoningContent,
			}))
			if collector != nil {
				if isRoot {
					collector.appendReasoning(chunk.ReasoningContent)
				} else {
					collector.AppendSubEvent(SubAgentEvent{
						Agent:            agentName,
						ParentToolCallID: parentID,
						Type:             "thinking",
						Content:          chunk.ReasoningContent,
					})
				}
			}
		}
		if chunk.Content != "" {
			buf.Append(Encode(Frame{
				Type:             "text",
				Agent:            agentField,
				ParentToolCallID: parentID,
				Content:          chunk.Content,
			}))
			if collector != nil {
				if isRoot {
					collector.appendContent(chunk.Content)
				} else {
					collector.AppendSubEvent(SubAgentEvent{
						Agent:            agentName,
						ParentToolCallID: parentID,
						Type:             "text",
						Content:          chunk.Content,
					})
				}
			}
		}
	}

	if len(chunks) == 0 {
		return nil
	}
	full, cErr := schema.ConcatMessages(chunks)
	if cErr != nil || full == nil {
		return nil
	}

	for _, tc := range full.ToolCalls {
		buf.Append(Encode(Frame{
			Type:             "tool_call",
			Agent:            agentField,
			ParentToolCallID: parentID,
			ID:               tc.ID,
			Name:             tc.Function.Name,
			ArgsJSON:         tc.Function.Arguments,
		}))
		if collector != nil {
			if isRoot {
				collector.startTool(tc.ID, tc.Function.Name, tc.Function.Arguments)
				// Remember which root tool_call started this sub-agent, so
				// the sub-agent's events can attribute themselves.
				router.noteRootToolCall(tc.Function.Name, tc.ID)
			} else {
				collector.AppendSubEvent(SubAgentEvent{
					Agent:            agentName,
					ParentToolCallID: parentID,
					Type:             "tool_call",
					ToolCallID:       tc.ID,
					Name:             tc.Function.Name,
					ArgsJSON:         tc.Function.Arguments,
				})
			}
		}
	}

	if full.ResponseMeta != nil && full.ResponseMeta.Usage != nil {
		u := full.ResponseMeta.Usage
		buf.Append(Encode(Frame{
			Type:             "usage",
			Agent:            agentField,
			ParentToolCallID: parentID,
			Prompt:           u.PromptTokens,
			Reply:            u.CompletionTokens,
			Total:            u.TotalTokens,
		}))
		if isRoot && collector != nil {
			collector.SetTotalTokens(u.TotalTokens)
		}
	}

	// Record this turn's structured shape for the service layer's raw-row
	// persistence. Only root events flow into turns — sub-agent internal
	// turns live in collector.subEvents instead.
	if isRoot && collector != nil {
		tcs := make([]ToolCallRecord, 0, len(full.ToolCalls))
		for _, tc := range full.ToolCalls {
			tcs = append(tcs, ToolCallRecord{
				ID:       tc.ID,
				Name:     tc.Function.Name,
				ArgsJSON: tc.Function.Arguments,
			})
		}
		collector.OpenTurn(full.Content, full.ReasoningContent, tcs)
	}
	return nil
}

func emitToolResult(
	ctx context.Context,
	isRoot bool,
	agentName string,
	router *subAgentRouter,
	toolName string,
	msg *schema.Message,
	buf *StreamBuffer,
	collector *RunCollector,
) {
	// Resolve the tool name with a three-tier fallback: ADK's MessageVariant
	// → provider's msg.Name → recorded tool_call by id (some provider SDKs
	// leave the result message's Name empty).
	name := toolName
	if name == "" {
		name = msg.Name
	}
	if name == "" && collector != nil {
		name = collector.ToolNameByID(msg.ToolCallID)
	}

	agentField := router.agentFieldFor(isRoot, agentName)
	parentID := ""
	if !isRoot {
		parentID = router.parentForAgent(agentName)
	}

	// Did toolerr.Middleware rescue this call from a failure? If yes, emit
	// ok=false with the original error message so the UI shows a real
	// failure state, even though ADK saw the rescued ToolOutput as success.
	if errMsg, failed := toolerr.FromContext(ctx).Lookup(msg.ToolCallID); failed {
		buf.Append(Encode(Frame{
			Type:             "tool_result",
			Agent:            agentField,
			ParentToolCallID: parentID,
			ID:               msg.ToolCallID,
			Name:             name,
			OK:               boolPtr(false),
			Error:            errMsg,
		}))
		if collector != nil {
			if isRoot {
				collector.finishTool(msg.ToolCallID, false, "", errMsg)
				collector.AttachToolResult(ToolResultRecord{
					CallID: msg.ToolCallID,
					Name:   name,
					OK:     false,
					Error:  errMsg,
				})
				router.noteRootToolResult(msg.ToolCallID)
			} else {
				collector.AppendSubEvent(SubAgentEvent{
					Agent:            agentName,
					ParentToolCallID: parentID,
					Type:             "tool_result",
					ToolCallID:       msg.ToolCallID,
					Name:             name,
					OK:               boolPtr(false),
					Error:            errMsg,
				})
			}
		}
		return
	}

	// A denial comes back through this path looking like any other success:
	// the approval middleware answers its own payload instead of calling the
	// tool. Classifying it here is what keeps the guesswork out of the UI.
	cancelled := IsCanceledResult(msg.Content)

	buf.Append(Encode(Frame{
		Type:             "tool_result",
		Agent:            agentField,
		ParentToolCallID: parentID,
		ID:               msg.ToolCallID,
		Name:             name,
		OK:               boolPtr(true),
		Content:          msg.Content,
		Cancelled:        cancelled,
	}))
	if collector != nil {
		if isRoot {
			collector.finishTool(msg.ToolCallID, true, msg.Content, "")
			collector.AttachToolResult(ToolResultRecord{
				CallID:    msg.ToolCallID,
				Name:      name,
				OK:        true,
				Content:   msg.Content,
				Cancelled: cancelled,
			})
			router.noteRootToolResult(msg.ToolCallID)
		} else {
			collector.AppendSubEvent(SubAgentEvent{
				Agent:            agentName,
				ParentToolCallID: parentID,
				Type:             "tool_result",
				ToolCallID:       msg.ToolCallID,
				Name:             name,
				OK:               boolPtr(true),
				Content:          msg.Content,
			})
		}
	}
}

func emitNonStreamAssistant(
	isRoot bool,
	agentName string,
	router *subAgentRouter,
	msg *schema.Message,
	buf *StreamBuffer,
	collector *RunCollector,
) {
	agentField := router.agentFieldFor(isRoot, agentName)
	parentID := ""
	if !isRoot {
		parentID = router.parentForAgent(agentName)
	}

	if msg.ReasoningContent != "" {
		buf.Append(Encode(Frame{Type: "thinking", Agent: agentField, ParentToolCallID: parentID, Content: msg.ReasoningContent}))
		if collector != nil {
			if isRoot {
				collector.appendReasoning(msg.ReasoningContent)
			} else {
				collector.AppendSubEvent(SubAgentEvent{
					Agent: agentName, ParentToolCallID: parentID,
					Type: "thinking", Content: msg.ReasoningContent,
				})
			}
		}
	}
	if msg.Content != "" {
		buf.Append(Encode(Frame{Type: "text", Agent: agentField, ParentToolCallID: parentID, Content: msg.Content}))
		if collector != nil {
			if isRoot {
				collector.appendContent(msg.Content)
			} else {
				collector.AppendSubEvent(SubAgentEvent{
					Agent: agentName, ParentToolCallID: parentID,
					Type: "text", Content: msg.Content,
				})
			}
		}
	}
	for _, tc := range msg.ToolCalls {
		buf.Append(Encode(Frame{
			Type:             "tool_call",
			Agent:            agentField,
			ParentToolCallID: parentID,
			ID:               tc.ID,
			Name:             tc.Function.Name,
			ArgsJSON:         tc.Function.Arguments,
		}))
		if collector != nil {
			if isRoot {
				collector.startTool(tc.ID, tc.Function.Name, tc.Function.Arguments)
				router.noteRootToolCall(tc.Function.Name, tc.ID)
			} else {
				collector.AppendSubEvent(SubAgentEvent{
					Agent: agentName, ParentToolCallID: parentID,
					Type: "tool_call", ToolCallID: tc.ID,
					Name: tc.Function.Name, ArgsJSON: tc.Function.Arguments,
				})
			}
		}
	}
	if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
		u := msg.ResponseMeta.Usage
		buf.Append(Encode(Frame{
			Type:             "usage",
			Agent:            agentField,
			ParentToolCallID: parentID,
			Prompt:           u.PromptTokens,
			Reply:            u.CompletionTokens,
			Total:            u.TotalTokens,
		}))
		if isRoot && collector != nil {
			collector.SetTotalTokens(u.TotalTokens)
		}
	}
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF)
}
