package service

import (
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

// A run cancelled while the model was still thinking persists an assistant row
// carrying reasoning and nothing else. The API counts only content and
// tool_calls as payload, so replaying such a row 400s the whole conversation
// from then on — every later turn resends it.
func TestToSchemaMessages_DropsReasoningOnlyAssistant(t *testing.T) {
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: "hi"},
		{Seq: 2, Role: "assistant", ReasoningContent: "thinking, then killed"},
		{Seq: 3, Role: "user", Content: "still there?"},
	}

	out := toSchemaMessages("c1", rows, false, nil)

	if len(out) != 2 {
		t.Fatalf("len=%d want 2: %v", len(out), roleContents(out))
	}
	for _, m := range out {
		if m.Role == schema.Assistant {
			t.Fatalf("reasoning-only assistant survived: %+v", m)
		}
	}
}

// Reasoning riding along with real content is normal and must be preserved.
func TestToSchemaMessages_KeepsAssistantWithContent(t *testing.T) {
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: "hi"},
		{Seq: 2, Role: "assistant", Content: "hello", ReasoningContent: "thought"},
	}

	out := toSchemaMessages("c1", rows, false, nil)

	if len(out) != 2 {
		t.Fatalf("len=%d want 2: %v", len(out), roleContents(out))
	}
	if out[1].Content != "hello" || out[1].ReasoningContent != "thought" {
		t.Fatalf("assistant mangled: %+v", out[1])
	}
}

// Orphan defence strips the tool_calls list; if the row had no text of its own
// that leaves an empty assistant message, which is just as invalid.
func TestToSchemaMessages_DropsAssistantEmptiedByOrphanStrip(t *testing.T) {
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: "search"},
		{
			Seq:       2,
			Role:      "assistant",
			ToolCalls: `[{"id":"call_1","name":"web_search","args_json":"{}"}]`,
		},
		// No Role=tool row for call_1: the run died before the result landed.
	}

	out := toSchemaMessages("c1", rows, false, nil)

	if len(out) != 1 {
		t.Fatalf("len=%d want 1: %v", len(out), roleContents(out))
	}
}

// A paired tool_call keeps its list and its tool result.
func TestToSchemaMessages_KeepsPairedToolCall(t *testing.T) {
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: "search"},
		{
			Seq:       2,
			Role:      "assistant",
			ToolCalls: `[{"id":"call_1","name":"web_search","args_json":"{}"}]`,
		},
		{Seq: 3, Role: "tool", Content: "results", ToolCallID: "call_1", ToolName: "web_search"},
	}

	out := toSchemaMessages("c1", rows, false, nil)

	if len(out) != 3 {
		t.Fatalf("len=%d want 3: %v", len(out), roleContents(out))
	}
	if len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool_calls lost: %+v", out[1])
	}
}

func roleContents(msgs []*schema.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, string(m.Role)+":"+m.Content)
	}
	return out
}
