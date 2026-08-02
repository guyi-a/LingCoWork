package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestFlattenResultJoinsTextBlocks(t *testing.T) {
	res := &mcpgo.CallToolResult{
		Content: []mcpgo.Content{
			mcpgo.TextContent{Type: "text", Text: "first"},
			mcpgo.TextContent{Type: "text", Text: "second"},
		},
	}
	if got := flattenResult(res); got != "first\nsecond" {
		t.Fatalf("flattenResult = %q", got)
	}
}

// Inlining base64 would spend a large part of the context window on bytes the
// model cannot read.
func TestFlattenResultAnnouncesBinaryInsteadOfInlining(t *testing.T) {
	data := strings.Repeat("A", 4096)
	res := &mcpgo.CallToolResult{
		Content: []mcpgo.Content{
			mcpgo.ImageContent{Type: "image", MIMEType: "image/png", Data: data},
		},
	}
	got := flattenResult(res)
	if strings.Contains(got, data) {
		t.Fatal("base64 payload was inlined into the model's context")
	}
	if !strings.Contains(got, "image/png") {
		t.Fatalf("flattenResult = %q, want a mention of the media type", got)
	}
}

// The spec asks a server to send a text equivalent alongside structured
// output, but not every server does, and dropping the result entirely is the
// worst of the options.
func TestFlattenResultFallsBackToStructuredContent(t *testing.T) {
	res := &mcpgo.CallToolResult{
		StructuredContent: map[string]any{"ok": true},
	}
	got := flattenResult(res)
	if !strings.Contains(got, `"ok"`) {
		t.Fatalf("flattenResult = %q", got)
	}
}

func TestFlattenResultHandlesNil(t *testing.T) {
	if got := flattenResult(nil); got != "" {
		t.Fatalf("flattenResult(nil) = %q", got)
	}
}

// Some models emit "" for a tool that takes no arguments; "" is not valid
// JSON and the server would reject it.
func TestRawArgumentsTurnsEmptyIntoAnObject(t *testing.T) {
	got, ok := rawArguments("  ").(map[string]any)
	if !ok || len(got) != 0 {
		t.Fatalf("rawArguments(\"\") = %#v, want an empty object", got)
	}
	if _, ok := rawArguments(`{"a":1}`).(json.RawMessage); !ok {
		t.Fatal("real arguments were not passed through untouched")
	}
}

func TestConvertSchemaPrefersRawInputSchema(t *testing.T) {
	tool := mcpgo.Tool{
		Name:           "t",
		RawInputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
	}
	params, err := convertSchema(tool)
	if err != nil {
		t.Fatalf("convertSchema: %v", err)
	}
	if params == nil {
		t.Fatal("convertSchema returned nil params")
	}
}

func TestNewRemoteToolSubstitutesADescription(t *testing.T) {
	rt, err := newRemoteTool(nil, "wiki", "wiki__lookup", mcpgo.Tool{Name: "lookup"}, nil)
	if err != nil {
		t.Fatalf("newRemoteTool: %v", err)
	}
	info, _ := rt.Info(t.Context())
	if info.Name != "wiki__lookup" {
		t.Errorf("model-facing name = %q, want the prefixed one", info.Name)
	}
	if rt.remoteName != "lookup" {
		t.Errorf("wire name = %q, want the server's own", rt.remoteName)
	}
	if info.Desc == "" {
		t.Error("a tool with no description reached the model with none")
	}
}
