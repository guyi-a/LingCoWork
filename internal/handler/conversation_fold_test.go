package handler

import (
	"testing"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

func TestFoldMessages_SegmentsPreserveReActOrder(t *testing.T) {
	now := time.Now()
	msgs := []model.Message{
		{Seq: 1, Role: "user", Content: "search", CreatedAt: now},
		{
			Seq:              2,
			Role:             "assistant",
			Content:          "opening tab",
			ReasoningContent: "think1",
			ToolCalls:        `[{"id":"c1","name":"browser_bridge","args_json":"{}"}]`,
			CreatedAt:        now,
		},
		{Seq: 3, Role: "tool", ToolCallID: "c1", ToolName: "browser_bridge", Content: "ok", Extra: `{"ok":true}`, CreatedAt: now},
		{
			Seq:              4,
			Role:             "assistant",
			Content:          "here are results",
			ReasoningContent: "think2",
			CreatedAt:        now,
		},
	}

	out := foldMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("want 2 items (user+assistant), got %d", len(out))
	}
	asst := out[1]
	if asst.Role != "assistant" {
		t.Fatalf("role=%s", asst.Role)
	}
	if asst.ReasoningContent != "think1\n\nthink2" {
		t.Fatalf("reasoning=%q", asst.ReasoningContent)
	}
	if len(asst.Segments) != 2 {
		t.Fatalf("segments=%d want 2", len(asst.Segments))
	}
	if asst.Segments[0].Content != "opening tab" || len(asst.Segments[0].Tools) != 1 {
		t.Fatalf("seg0=%+v", asst.Segments[0])
	}
	if asst.Segments[0].Tools[0].ID != "c1" || asst.Segments[0].Tools[0].Status != "ok" {
		t.Fatalf("seg0 tool=%+v", asst.Segments[0].Tools[0])
	}
	if asst.Segments[1].Content != "here are results" || len(asst.Segments[1].Tools) != 0 {
		t.Fatalf("seg1=%+v", asst.Segments[1])
	}
	// Flat fields stay derived for compatibility.
	if asst.Content != "opening tab\n\nhere are results" {
		t.Fatalf("flat content=%q", asst.Content)
	}
	if len(asst.Tools) != 1 || asst.Tools[0].ID != "c1" {
		t.Fatalf("flat tools=%+v", asst.Tools)
	}
}

func TestFoldMessages_HITLEmptyAssistantDoesNotDuplicateTools(t *testing.T) {
	now := time.Now()
	msgs := []model.Message{
		{Seq: 1, Role: "user", Content: "search", CreatedAt: now},
		{
			Seq:       2,
			Role:      "assistant",
			ToolCalls: `[{"id":"call_js","name":"job_search","args_json":"{}"}]`,
			CreatedAt: now,
		},
		// Phantom empty assistant from pre-fix HITL resume persist.
		{Seq: 3, Role: "assistant", CreatedAt: now},
		{
			Seq:        4,
			Role:       "tool",
			ToolCallID: "call_js",
			ToolName:   "job_search",
			Content:    "results",
			Extra:      `{"ok":true}`,
			CreatedAt:  now,
		},
		{Seq: 5, Role: "assistant", Content: "here you go", CreatedAt: now},
	}
	out := foldMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("items=%d", len(out))
	}
	asst := out[1]
	n := 0
	for _, seg := range asst.Segments {
		n += len(seg.Tools)
		for _, tool := range seg.Tools {
			if tool.ID == "call_js" && tool.Status != "ok" {
				t.Fatalf("job_search status=%s want ok", tool.Status)
			}
		}
	}
	if n != 1 {
		t.Fatalf("tool cards=%d want 1 (got segments=%+v)", n, asst.Segments)
	}
}

// A denial returns successfully — the approval middleware answers its own
// payload — so `ok` alone can't distinguish it from a call that ran. Reload
// has to show the same "cancelled" the live stream did, not "done".
func TestFoldMessages_DenialFoldsToCancelled(t *testing.T) {
	now := time.Now()
	msgs := []model.Message{
		{Seq: 1, Role: "user", Content: "delete it", CreatedAt: now},
		{
			Seq:       2,
			Role:      "assistant",
			ToolCalls: `[{"id":"c1","name":"run_command","args_json":"{}"}]`,
			CreatedAt: now,
		},
		{
			Seq:        3,
			Role:       "tool",
			ToolCallID: "c1",
			ToolName:   "run_command",
			Content:    `{"canceled":true,"tool":"run_command"}`,
			Extra:      `{"ok":true,"cancelled":true}`,
			CreatedAt:  now,
		},
	}
	out := foldMessages(msgs)
	tools := out[1].Segments[0].Tools
	if len(tools) != 1 || tools[0].Status != "cancelled" {
		t.Fatalf("tools=%+v want one cancelled card", tools)
	}
}

// Rows written before the flag existed carry the cancellation only in their
// body, and still have to read as cancelled.
func TestFoldMessages_LegacyCancellationWithoutFlag(t *testing.T) {
	now := time.Now()
	msgs := []model.Message{
		{Seq: 1, Role: "user", Content: "delete it", CreatedAt: now},
		{
			Seq:       2,
			Role:      "assistant",
			ToolCalls: `[{"id":"c1","name":"run_command","args_json":"{}"},{"id":"c2","name":"web_search","args_json":"{}"}]`,
			CreatedAt: now,
		},
		{
			Seq:        3,
			Role:       "tool",
			ToolCallID: "c1",
			ToolName:   "run_command",
			Content:    "[canceled] tool did not run",
			Extra:      `{"ok":false,"error":"canceled"}`,
			CreatedAt:  now,
		},
		// The false positive the old substring rule produced: a call that ran
		// and returned prose about cancellation.
		{
			Seq:        4,
			Role:       "tool",
			ToolCallID: "c2",
			ToolName:   "web_search",
			Content:    "Upstream cancelled the first attempt; the retry returned 3 results.",
			Extra:      `{"ok":true}`,
			CreatedAt:  now,
		},
	}
	out := foldMessages(msgs)
	byID := map[string]string{}
	for _, seg := range out[1].Segments {
		for _, tool := range seg.Tools {
			byID[tool.ID] = tool.Status
		}
	}
	if byID["c1"] != "cancelled" {
		t.Errorf("c1 status=%q want cancelled", byID["c1"])
	}
	if byID["c2"] != "ok" {
		t.Errorf("c2 status=%q want ok", byID["c2"])
	}
}

func TestInsertCompactionMarker_LandsAtFoldPoint(t *testing.T) {
	now := time.Now()
	msgs := []model.Message{
		{Seq: 1, Role: "user", Content: "first", CreatedAt: now},
		{Seq: 2, Role: "assistant", Content: "reply", CreatedAt: now},
		{Seq: 3, Role: "user", Content: "second", CreatedAt: now},
		{Seq: 4, Role: "assistant", Content: "reply2", CreatedAt: now},
	}
	folded := foldMessages(msgs)
	out := insertCompactionMarker(folded, &model.Compaction{
		ID: 9, ThroughSeq: 2, ReplacedCount: 2, CreatedAt: now,
	})

	if len(out) != len(folded)+1 {
		t.Fatalf("len=%d want %d", len(out), len(folded)+1)
	}
	if out[2].Role != roleContextCompacted {
		t.Fatalf("marker at index 2 is %q; got roles %v", out[2].Role, roles(out))
	}
	if out[2].CompactionID != 9 || out[2].ReplacedCount != 2 {
		t.Fatalf("marker=%+v", out[2])
	}
	// Rows on either side keep their order.
	if out[0].Seq != 1 || out[1].Seq != 2 || out[3].Seq != 3 {
		t.Fatalf("ordering broken: %v", roles(out))
	}
	// The summary text is model-only and must never reach the client.
	if out[2].Content != "" {
		t.Fatalf("marker leaked content: %q", out[2].Content)
	}
}

func TestInsertCompactionMarker_NilIsPassThrough(t *testing.T) {
	items := []messageItem{{Seq: 1, Role: "user"}}
	if got := insertCompactionMarker(items, nil); len(got) != 1 {
		t.Fatalf("len=%d want 1", len(got))
	}
}

// An assistant turn folds to its LAST row's seq, so a turn that straddles the
// boundary sorts after the marker — the correct side for a partially-kept turn.
func TestInsertCompactionMarker_GoesFirstWhenNothingPrecedes(t *testing.T) {
	items := []messageItem{{Seq: 5, Role: "user"}, {Seq: 6, Role: "assistant"}}
	out := insertCompactionMarker(items, &model.Compaction{ID: 1, ThroughSeq: 2})
	if out[0].Role != roleContextCompacted {
		t.Fatalf("marker should lead; got %v", roles(out))
	}
}

func roles(items []messageItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Role
	}
	return out
}

// Usage lands on the run's final assistant row, but the UI sees one merged
// turn — the figure has to survive the merge, and it must be hoisted rather
// than summed: each ReAct step's count already covers the whole context.
func TestFoldMessages_HoistsTotalTokensOntoMergedTurn(t *testing.T) {
	now := time.Now()
	msgs := []model.Message{
		{Seq: 1, Role: "user", Content: "search", CreatedAt: now},
		{
			Seq:       2,
			Role:      "assistant",
			Content:   "looking",
			ToolCalls: `[{"id":"c1","name":"web_search","args_json":"{}"}]`,
			CreatedAt: now,
		},
		{Seq: 3, Role: "tool", ToolCallID: "c1", ToolName: "web_search", Content: "ok", Extra: `{"ok":true}`, CreatedAt: now},
		{Seq: 4, Role: "assistant", Content: "found it", TotalTokens: 42000, CreatedAt: now},
	}

	out := foldMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("len=%d want 2", len(out))
	}
	if out[1].TotalTokens != 42000 {
		t.Fatalf("total_tokens=%d want 42000", out[1].TotalTokens)
	}
	if out[0].TotalTokens != 0 {
		t.Fatalf("user row picked up tokens: %d", out[0].TotalTokens)
	}
}

// A turn whose last row reported nothing keeps the earlier row's figure
// rather than reverting to zero and blanking the readout.
func TestFoldMessages_KeepsEarlierTokensWhenLastRowHasNone(t *testing.T) {
	now := time.Now()
	msgs := []model.Message{
		{Seq: 1, Role: "user", Content: "hi", CreatedAt: now},
		{Seq: 2, Role: "assistant", Content: "part one", TotalTokens: 1200, CreatedAt: now},
		{Seq: 3, Role: "assistant", Content: "part two", CreatedAt: now},
	}

	out := foldMessages(msgs)
	if out[1].TotalTokens != 1200 {
		t.Fatalf("total_tokens=%d want 1200", out[1].TotalTokens)
	}
}

func TestFoldMessages_LegacySingleRow(t *testing.T) {
	now := time.Now()
	msgs := []model.Message{
		{Seq: 1, Role: "user", Content: "hi", CreatedAt: now},
		{
			Seq:              2,
			Role:             "assistant",
			Content:          "hello",
			ReasoningContent: "r",
			Extra:            `{"tools":[{"id":"t1","name":"fs","ok":true,"content":"done"}]}`,
			CreatedAt:        now,
		},
	}
	out := foldMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
	asst := out[1]
	if len(asst.Segments) != 1 {
		t.Fatalf("segments=%d", len(asst.Segments))
	}
	if len(asst.Segments[0].Tools) != 1 || asst.Segments[0].Tools[0].ID != "t1" {
		t.Fatalf("seg tools=%+v", asst.Segments[0].Tools)
	}
	if asst.Content != "hello" || len(asst.Tools) != 1 {
		t.Fatalf("flat=%q tools=%+v", asst.Content, asst.Tools)
	}
}

func TestFoldMessages_UserInstructionExposesRawInput(t *testing.T) {
	now := time.Now()
	msgs := []model.Message{{
		Seq:       1,
		Role:      "user",
		Content:   "Review this code:\n\npackage main",
		Extra:     `{"user_instruction":{"name":"review","label":"Code review","raw_input":"package main"}}`,
		CreatedAt: now,
	}}

	out := foldMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("len=%d want 1", len(out))
	}
	if out[0].Content != msgs[0].Content {
		t.Fatalf("expanded content changed: %q", out[0].Content)
	}
	if out[0].UserInstruction == nil ||
		out[0].UserInstruction.Name != "review" ||
		out[0].UserInstruction.Label != "Code review" ||
		out[0].UserInstruction.RawInput != "package main" {
		t.Fatalf("instruction metadata = %+v", out[0].UserInstruction)
	}
}

func TestFromModelMessage_AssistantExtraStillHydratesWithInstructionSupport(t *testing.T) {
	now := time.Now()
	item := fromModelMessage(model.Message{
		Role:      "assistant",
		Content:   "done",
		Extra:     `{"tools":[{"id":"t1","name":"fs","ok":true}],"sub_events":[{"seq":1,"agent":"worker","type":"message"}]}`,
		CreatedAt: now,
	})
	if len(item.Tools) != 1 || item.Tools[0].ID != "t1" {
		t.Fatalf("tools lost: %+v", item.Tools)
	}
	if len(item.SubEvents) != 1 || item.SubEvents[0].Agent != "worker" {
		t.Fatalf("sub-events lost: %+v", item.SubEvents)
	}
	if item.UserInstruction != nil {
		t.Fatalf("assistant received user instruction metadata: %+v", item.UserInstruction)
	}
}
