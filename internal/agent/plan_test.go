package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/agent/toolerr"
	"github.com/guyi-a/Interview-Agent/internal/agentmode"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

func TestPlanGuardBlocksWritesEvenWithoutApproval(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	convRepo := repository.NewConversationRepo(db)
	if err := convRepo.Upsert(t.Context(), "conv"); err != nil {
		t.Fatal(err)
	}
	if err := convRepo.SetChatMode(t.Context(), "conv", string(agentmode.Plan)); err != nil {
		t.Fatal(err)
	}
	ctx := contextkey.WithConversationID(context.Background(), "conv")
	called := 0
	endpoint := planGuard(convRepo).Invokable(func(
		_ context.Context,
		_ *compose.ToolInput,
	) (*compose.ToolOutput, error) {
		called++
		return &compose.ToolOutput{Result: "ok"}, nil
	})

	if _, err := endpoint(ctx, &compose.ToolInput{
		Name: "write_file", Arguments: `{"path":"x","content":"x"}`,
	}); err == nil {
		t.Fatal("Plan mode allowed write_file")
	}
	if _, err := endpoint(ctx, &compose.ToolInput{
		Name: "run_command", Arguments: `{"command":"git status --short"}`,
	}); err != nil {
		t.Fatalf("Plan mode rejected safe probe: %v", err)
	}
	if _, err := endpoint(ctx, &compose.ToolInput{
		Name: "run_command", Arguments: `{"command":"git add ."}`,
	}); err == nil {
		t.Fatal("Plan mode allowed mutating command")
	}
	if called != 1 {
		t.Fatalf("next called %d times, want 1", called)
	}
}

func TestPlanGuardRejectionBecomesRecoverableObservation(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	convRepo := repository.NewConversationRepo(db)
	if err := convRepo.Upsert(t.Context(), "conv"); err != nil {
		t.Fatal(err)
	}
	if err := convRepo.SetChatMode(t.Context(), "conv", string(agentmode.Plan)); err != nil {
		t.Fatal(err)
	}
	registry := toolerr.NewRegistry()
	ctx := toolerr.WithRegistry(
		contextkey.WithConversationID(context.Background(), "conv"),
		registry,
	)
	inner := planGuard(convRepo).Invokable(func(
		_ context.Context,
		_ *compose.ToolInput,
	) (*compose.ToolOutput, error) {
		t.Fatal("rejected command reached tool")
		return nil, nil
	})
	endpoint := toolerr.Middleware().Invokable(inner)
	out, err := endpoint(ctx, &compose.ToolInput{
		CallID:    "call-1",
		Name:      "run_command",
		Arguments: `{"command":"echo marker; head -1 go.mod"}`,
	})
	if err != nil {
		t.Fatalf("guard rejection aborted run: %v", err)
	}
	if out == nil || out.Result == "" {
		t.Fatal("guard rejection did not produce an observation")
	}
	if _, ok := registry.Lookup("call-1"); !ok {
		t.Fatal("guard rejection was not recorded as a failed tool result")
	}
}

func TestPlanGuardDoesNotRestrictAgentMode(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	convRepo := repository.NewConversationRepo(db)
	if err := convRepo.Upsert(t.Context(), "conv"); err != nil {
		t.Fatal(err)
	}
	ctx := contextkey.WithConversationID(context.Background(), "conv")
	called := false
	endpoint := planGuard(convRepo).Invokable(func(
		_ context.Context,
		_ *compose.ToolInput,
	) (*compose.ToolOutput, error) {
		called = true
		return &compose.ToolOutput{Result: "ok"}, nil
	})
	if _, err := endpoint(ctx, &compose.ToolInput{Name: "write_file"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Agent mode did not reach tool endpoint")
	}
	called = false
	if _, err := endpoint(ctx, &compose.ToolInput{Name: "create_plan"}); err == nil {
		t.Fatal("Agent mode allowed a fresh create_plan call")
	}
	if called {
		t.Fatal("fresh Agent create_plan reached tool endpoint")
	}
}
