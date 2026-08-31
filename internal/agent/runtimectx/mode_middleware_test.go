package runtimectx

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/agentmode"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

func TestModeMiddlewareFiltersPlanAndAgentTools(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	convRepo := repository.NewConversationRepo(db)
	if err := convRepo.Upsert(t.Context(), "conv"); err != nil {
		t.Fatal(err)
	}
	mw := NewModeMiddleware(convRepo)
	ctx := contextkey.WithConversationID(context.Background(), "conv")
	all := []tool.BaseTool{
		namedTestTool(t, "read_file"),
		namedTestTool(t, "write_file"),
		namedTestTool(t, "create_plan"),
		namedTestTool(t, "todo_write"),
		namedTestTool(t, "remote__publish"),
	}

	if err := convRepo.SetChatMode(ctx, "conv", string(agentmode.Plan)); err != nil {
		t.Fatal(err)
	}
	_, planRun, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext{
		Instruction: "base",
		Tools:       all,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertToolNames(t, planRun.Tools, []string{"read_file", "create_plan"})
	if planRun.Instruction == "base" {
		t.Fatal("Plan prompt was not appended")
	}

	if err := convRepo.SetChatMode(ctx, "conv", string(agentmode.Agent)); err != nil {
		t.Fatal(err)
	}
	_, agentRun, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext{Tools: all})
	if err != nil {
		t.Fatal(err)
	}
	assertToolNames(t, agentRun.Tools, []string{
		"read_file", "write_file", "create_plan", "todo_write", "remote__publish",
	})
}

func namedTestTool(t *testing.T, name string) tool.BaseTool {
	t.Helper()
	out, err := utils.InferTool(name, name, func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertToolNames(t *testing.T, tools []tool.BaseTool, want []string) {
	t.Helper()
	got := make([]string, 0, len(tools))
	for _, candidate := range tools {
		info, err := candidate.Info(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, info.Name)
	}
	if len(got) != len(want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool names = %v, want %v", got, want)
		}
	}
}
