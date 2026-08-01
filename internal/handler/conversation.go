package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/service"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ConversationHandler struct {
	svc *service.ConversationService
	// contextLimit is the token count at which history gets folded, or 0 when
	// compaction is off. It rides along with the messages payload rather than
	// living on its own endpoint so the numerator and the denominator of the
	// occupancy readout always arrive together.
	contextLimit int
}

func NewConversationHandler(svc *service.ConversationService, contextLimit int) *ConversationHandler {
	return &ConversationHandler{svc: svc, contextLimit: contextLimit}
}

func (h *ConversationHandler) Register(r *gin.Engine) {
	r.GET("/conversations", h.List)
	r.GET("/conversations/:id/messages", h.Messages)
	r.DELETE("/conversations/:id", h.Delete)
}

type conversationListItem struct {
	ID          string  `json:"id"`
	ProjectID   *string `json:"project_id,omitempty"`
	Title       string  `json:"title"`
	AgentStatus string  `json:"agent_status,omitempty"`
	UpdatedAt   string  `json:"updated_at"`
}

func (h *ConversationHandler) List(c *gin.Context) {
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	items, err := h.svc.List(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]conversationListItem, 0, len(items))
	for _, it := range items {
		out = append(out, conversationListItem{
			ID:          it.ID,
			ProjectID:   it.ProjectID,
			Title:       it.Title,
			AgentStatus: it.AgentStatus,
			UpdatedAt:   it.UpdatedAt.Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"conversations": out})
}

type toolEventItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ArgsJSON string `json:"args_json,omitempty"`
	OK       *bool  `json:"ok,omitempty"`
	Status   string `json:"status,omitempty"`
	Content  string `json:"content,omitempty"`
	Error    string `json:"error,omitempty"`
}

type subAgentEventItem struct {
	Seq              int    `json:"seq"`
	Agent            string `json:"agent"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	Type             string `json:"type"`
	Content          string `json:"content,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	Name             string `json:"name,omitempty"`
	ArgsJSON         string `json:"args_json,omitempty"`
	OK               *bool  `json:"ok,omitempty"`
	Error            string `json:"error,omitempty"`
}

// segmentItem is one ReAct iteration inside an assistant turn: the
// assistant's visible text for that iteration plus the tools it invoked.
// Thought/reasoning stays on the parent messageItem (merged across segments).
type segmentItem struct {
	Content string          `json:"content,omitempty"`
	Tools   []toolEventItem `json:"tools,omitempty"`
}

type messageItem struct {
	Seq              int                 `json:"seq"`
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	Tools            []toolEventItem     `json:"tools,omitempty"`
	Segments         []segmentItem       `json:"segments,omitempty"`
	SubEvents        []subAgentEventItem `json:"sub_events,omitempty"`
	CreatedAt        string              `json:"created_at"`

	// TotalTokens is the provider's own count for the context this turn ran
	// against — the same number the compaction estimator anchors on, so the
	// occupancy the UI shows and the occupancy that decides when to fold are
	// never two different figures. Only the final assistant row of a run
	// carries it; foldMessages hoists it onto the merged turn.
	TotalTokens int `json:"total_tokens,omitempty"`

	// Set only on the synthetic role="context_compacted" marker.
	CompactionID  uint64 `json:"compaction_id,omitempty"`
	ReplacedCount int    `json:"replaced_count,omitempty"`
}

// roleContextCompacted marks where history was folded into a summary. It is
// not a stored row — the handler synthesizes it from the compaction record
// so the divider survives a reload the same way the live SSE frame draws it
// mid-run. The summary text is intentionally not sent: it is context for the
// model, not something the user asked to read.
const roleContextCompacted = "context_compacted"

func (h *ConversationHandler) Messages(c *gin.Context) {
	id := c.Param("id")
	msgs, err := h.svc.Messages(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := insertCompactionMarker(foldMessages(msgs), h.svc.ActiveCompaction(c.Request.Context(), id))
	c.JSON(http.StatusOK, gin.H{"messages": out, "context_limit": h.contextLimit})
}

// insertCompactionMarker splices the fold divider into the folded history.
//
// It lands after the last entry at or before ThroughSeq. foldMessages sets a
// merged assistant turn's Seq to its LAST row, so an entry compares <= only
// when the whole turn was folded — a turn straddling the boundary would sort
// after the marker, which is the correct side for a partially-kept turn.
func insertCompactionMarker(items []messageItem, active *model.Compaction) []messageItem {
	if active == nil {
		return items
	}
	marker := messageItem{
		Seq:           active.ThroughSeq,
		Role:          roleContextCompacted,
		CreatedAt:     active.CreatedAt.Format(time.RFC3339),
		CompactionID:  active.ID,
		ReplacedCount: active.ReplacedCount,
	}
	at := 0
	for at < len(items) && items[at].Seq <= active.ThroughSeq {
		at++
	}
	out := make([]messageItem, 0, len(items)+1)
	out = append(out, items[:at]...)
	out = append(out, marker)
	return append(out, items[at:]...)
}

func (h *ConversationHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

func fromModelMessage(m model.Message) messageItem {
	item := messageItem{
		Seq:              m.Seq,
		Role:             m.Role,
		Content:          m.Content,
		ReasoningContent: m.ReasoningContent,
		// Nano precision: the client derives a turn's wall-clock duration by
		// subtracting the user row's timestamp from the assistant row's, and
		// whole seconds would quantise every short turn to 0s or 1s.
		CreatedAt:   m.CreatedAt.Format(time.RFC3339Nano),
		TotalTokens: m.TotalTokens,
	}
	if m.Extra != "" {
		var payload struct {
			Tools     []stream.ToolEventRecord `json:"tools"`
			SubEvents []stream.SubAgentEvent   `json:"sub_events"`
		}
		if err := json.Unmarshal([]byte(m.Extra), &payload); err == nil {
			// Only hydrate Tools from Extra for LEGACY assistant rows (no
			// ToolCalls column). New rows have their tool structure in
			// ToolCalls + separate Role=tool rows, and foldMessages fills
			// item.Tools from those. Reading Extra.tools here would produce
			// duplicates.
			if m.ToolCalls == "" {
				for _, t := range payload.Tools {
					status := "error"
					if t.OK {
						status = "ok"
					}
					item.Tools = append(item.Tools, toolEventItem{
						ID:       t.ID,
						Name:     t.Name,
						ArgsJSON: t.ArgsJSON,
						OK:       boolPtr(t.OK),
						Status:   status,
						Content:  t.Content,
						Error:    t.Error,
					})
				}
			}
			for _, e := range payload.SubEvents {
				item.SubEvents = append(item.SubEvents, subAgentEventItem{
					Seq:              e.Seq,
					Agent:            e.Agent,
					ParentToolCallID: e.ParentToolCallID,
					Type:             e.Type,
					Content:          e.Content,
					ToolCallID:       e.ToolCallID,
					Name:             e.Name,
					ArgsJSON:         e.ArgsJSON,
					OK:               e.OK,
					Error:            e.Error,
				})
			}
		}
	}
	return item
}

// foldMessages transforms raw per-message DB rows into one assistant entry
// per user turn. Each ReAct iteration becomes a segment (content + tools);
// reasoning is merged across segments. Flat content/tools are derived from
// segments so older clients keep working.
//
//  1. assistant + subsequent tool rows: ToolCalls seed segment tool
//     placeholders; Role=tool rows fill ok/content/error by ToolCallID on
//     the latest segment.
//
//  2. Multiple assistant rows in the same user turn append segments (not
//     one giant tools[] blob) so the UI can interleave tools and text.
//
// The merge chain resets on any user / system row.
//
// Legacy rows (no ToolCalls, tools in Extra) become a single segment.
func foldMessages(msgs []model.Message) []messageItem {
	out := make([]messageItem, 0, len(msgs))
	lastAssistantIdx := -1

	for _, m := range msgs {
		switch m.Role {
		case "tool":
			if lastAssistantIdx < 0 {
				continue
			}
			ok := true
			errMsg := ""
			if m.Extra != "" {
				var p struct {
					OK    *bool  `json:"ok"`
					Error string `json:"error"`
				}
				if json.Unmarshal([]byte(m.Extra), &p) == nil {
					if p.OK != nil {
						ok = *p.OK
					}
					errMsg = p.Error
				}
			}
			prev := &out[lastAssistantIdx]
			if len(prev.Segments) == 0 {
				prev.Segments = []segmentItem{{}}
			}
			// Match across ALL segments — after HITL resume the tool_result
			// often lands after an empty phantom assistant segment; filling
			// only the last segment left the original PENDING card orphaned
			// and appended a second DONE card for the same id.
			merged := false
			for si := range prev.Segments {
				seg := &prev.Segments[si]
				for i := range seg.Tools {
					t := &seg.Tools[i]
					if t.ID != m.ToolCallID {
						continue
					}
					t.OK = boolPtr(ok)
					if ok {
						t.Status = "ok"
						t.Content = m.Content
					} else {
						t.Status = "error"
						t.Error = errMsg
						if t.Error == "" {
							t.Error = m.Content
						}
					}
					merged = true
					break
				}
				if merged {
					break
				}
			}
			if !merged {
				last := &prev.Segments[len(prev.Segments)-1]
				last.Tools = append(last.Tools, toolEventItem{
					ID:      m.ToolCallID,
					Name:    m.ToolName,
					OK:      boolPtr(ok),
					Status:  statusFromOK(ok),
					Content: m.Content,
					Error:   errMsg,
				})
			}
			rebuildFlatFromSegments(prev)
		case "assistant":
			item := fromModelMessage(m)
			if m.ToolCalls != "" {
				var recs []stream.ToolCallRecord
				if err := json.Unmarshal([]byte(m.ToolCalls), &recs); err == nil && len(recs) > 0 {
					placeholders := make([]toolEventItem, 0, len(recs))
					for _, rec := range recs {
						placeholders = append(placeholders, toolEventItem{
							ID:       rec.ID,
							Name:     rec.Name,
							ArgsJSON: rec.ArgsJSON,
							Status:   "pending",
						})
					}
					item.Tools = placeholders
				}
			}

			// Skip empty phantom assistants (HITL resume used to persist
			// these between tool_calls and tool results). Appending them as
			// segments causes tool_results to miss the PENDING placeholder
			// and render a duplicate DONE card.
			if item.Content == "" && item.ReasoningContent == "" &&
				len(item.Tools) == 0 && m.ToolCalls == "" {
				if lastAssistantIdx >= 0 {
					prev := &out[lastAssistantIdx]
					prev.SubEvents = append(prev.SubEvents, item.SubEvents...)
					prev.Seq = item.Seq
					prev.CreatedAt = item.CreatedAt
				}
				continue
			}

			seg := segmentItem{Content: item.Content, Tools: item.Tools}

			if lastAssistantIdx >= 0 {
				prev := &out[lastAssistantIdx]
				prev.ReasoningContent = joinAssistantContent(prev.ReasoningContent, item.ReasoningContent)
				// Dedupe tools already present in earlier segments (legacy
				// Extra.tools dual-write on the last assistant row).
				seen := segmentToolIDs(prev.Segments)
				filtered := make([]toolEventItem, 0, len(seg.Tools))
				for _, t := range seg.Tools {
					if t.ID != "" {
						if _, dup := seen[t.ID]; dup {
							continue
						}
						seen[t.ID] = struct{}{}
					}
					filtered = append(filtered, t)
				}
				seg.Tools = filtered
				prev.Segments = append(prev.Segments, seg)
				prev.SubEvents = append(prev.SubEvents, item.SubEvents...)
				prev.Seq = item.Seq
				prev.CreatedAt = item.CreatedAt
				// Only the run's final row carries usage, and it is the one
				// that measures the whole context — hoist it, never sum.
				if item.TotalTokens > 0 {
					prev.TotalTokens = item.TotalTokens
				}
				rebuildFlatFromSegments(prev)
				continue
			}

			item.Segments = []segmentItem{seg}
			rebuildFlatFromSegments(&item)
			out = append(out, item)
			lastAssistantIdx = len(out) - 1
		default:
			out = append(out, fromModelMessage(m))
			lastAssistantIdx = -1
		}
	}
	return out
}

func segmentToolIDs(segs []segmentItem) map[string]struct{} {
	seen := make(map[string]struct{})
	for _, seg := range segs {
		for _, t := range seg.Tools {
			if t.ID != "" {
				seen[t.ID] = struct{}{}
			}
		}
	}
	return seen
}

// rebuildFlatFromSegments keeps Content/Tools in sync with Segments for
// copy buttons and any client that ignores segments.
func rebuildFlatFromSegments(item *messageItem) {
	if item == nil {
		return
	}
	var content string
	tools := make([]toolEventItem, 0)
	seen := make(map[string]struct{})
	for _, seg := range item.Segments {
		content = joinAssistantContent(content, seg.Content)
		for _, t := range seg.Tools {
			if t.ID != "" {
				if _, dup := seen[t.ID]; dup {
					continue
				}
				seen[t.ID] = struct{}{}
			}
			tools = append(tools, t)
		}
	}
	item.Content = content
	item.Tools = tools
}

// joinAssistantContent concatenates two chunks of the same assistant turn's
// content/reasoning. A single blank line separator is inserted between
// non-empty halves so ReAct intermediate remarks ("Let me check…") stay
// visually distinct from the final answer.
func joinAssistantContent(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

func boolPtr(v bool) *bool {
	return &v
}

func statusFromOK(ok bool) string {
	if ok {
		return "ok"
	}
	return "error"
}
