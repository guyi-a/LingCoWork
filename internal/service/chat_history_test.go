package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/multimodal"
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

func TestToSchemaMessages_ReplaysExpandedInstructionSnapshot(t *testing.T) {
	rows := []model.Message{{
		Seq:     1,
		Role:    "user",
		Content: "Review carefully:\n\npackage main",
		Extra:   `{"user_instruction":{"name":"review","label":"Review","raw_input":"package main"}}`,
	}}

	out := toSchemaMessages("c1", rows, false, nil)
	if len(out) != 1 {
		t.Fatalf("len=%d want 1", len(out))
	}
	if out[0].Content != rows[0].Content {
		t.Fatalf("replay content=%q want expanded snapshot %q", out[0].Content, rows[0].Content)
	}
}

func TestToSchemaMessages_ImageBudgetPrefersRecentHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	rows := make([]model.Message, multimodal.MaxImagesPerRequest+1)
	for i := range rows {
		rows[i] = model.Message{
			Seq:     i + 1,
			Role:    "user",
			Content: fmt.Sprintf("[image: %s]\nturn %d", path, i),
		}
	}

	out := toSchemaMessagesWithBudget(
		"c1",
		rows,
		true,
		nil,
		multimodal.NewImageBudget(),
	)
	if len(out) != len(rows) {
		t.Fatalf("len=%d want %d", len(out), len(rows))
	}
	if len(out[0].UserInputMultiContent) != 0 ||
		!strings.Contains(out[0].Content, "request image count limit reached") {
		t.Fatalf("oldest image was not omitted: %#v", out[0])
	}
	if len(out[len(out)-1].UserInputMultiContent) == 0 {
		t.Fatal("newest image did not receive budget")
	}
}

func roleContents(msgs []*schema.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, string(m.Role)+":"+m.Content)
	}
	return out
}
