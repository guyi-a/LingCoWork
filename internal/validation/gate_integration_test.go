package validation

import (
	"context"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	repomodel "github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type scriptedGateModel struct {
	mu    sync.Mutex
	calls int
}

func (m *scriptedGateModel) next() *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls < 4 {
		return schema.AssistantMessage("premature final", nil)
	}
	return schema.AssistantMessage("validation did not converge", nil)
}

func (m *scriptedGateModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return m.next(), nil
}

func (m *scriptedGateModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next()}), nil
}

func (m *scriptedGateModel) WithTools(
	[]*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestCompletionGateSuppressesPrematureStreamingFinals(t *testing.T) {
	service, ctx := newValidationFixture(t)
	if err := service.changes.CreateEvent(ctx, &repomodel.WorkspaceChangeEvent{
		ProjectID: "project", ConversationID: "conv", UserMessageSeq: 1,
		ToolCallID: "write-1", ToolName: "apply_patch", Operation: "write",
		Path: "internal/a.go", Attribution: "agent", Succeeded: true,
	}); err != nil {
		t.Fatal(err)
	}
	gateTool, err := service.CompletionTool()
	if err != nil {
		t.Fatal(err)
	}
	scripted := &scriptedGateModel{}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: "gate-test", Description: "gate test", Instruction: "test",
		Model: scripted,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{gateTool},
		}},
		Handlers:      []adk.ChatModelAgentMiddleware{service.CompletionMiddleware()},
		MaxIterations: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx = contextkey.WithConversationID(ctx, "conv")
	iter := agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("fix")},
	})
	var visible []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, getErr := event.Output.MessageOutput.GetMessage()
		if getErr == nil && message != nil && message.Content != "" {
			visible = append(visible, message.Content)
		}
	}
	if scripted.calls != 4 {
		t.Fatalf("model calls=%d, want 4", scripted.calls)
	}
	for _, content := range visible {
		if content == "premature final" {
			t.Fatalf("premature final escaped middleware: %v", visible)
		}
	}
	if len(visible) == 0 || visible[len(visible)-1] != "validation did not converge" {
		t.Fatalf("visible outputs=%v", visible)
	}
}
