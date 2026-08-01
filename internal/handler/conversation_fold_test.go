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
