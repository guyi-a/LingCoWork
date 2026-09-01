package runtimectx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/agentsmd"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

func TestAgentsMDUsesConversationWorkspace(t *testing.T) {
	convRepo, projectRepo := newAgentsMDRepos(t)
	first := t.TempDir()
	second := t.TempDir()
	writeAgentsMD(t, first, "FIRST WORKSPACE")
	writeAgentsMD(t, second, "SECOND WORKSPACE")
	bindAgentsMDWorkspace(t, convRepo, projectRepo, "first", first)
	bindAgentsMDWorkspace(t, convRepo, projectRepo, "second", second)

	mw, err := NewAgentsMDMiddleware(t.Context(), convRepo, projectRepo)
	if err != nil {
		t.Fatal(err)
	}

	firstContent := injectAgentsMD(t, mw, contextkey.WithConversationID(t.Context(), "first"))
	if !strings.Contains(firstContent, "FIRST WORKSPACE") || strings.Contains(firstContent, "SECOND WORKSPACE") {
		t.Fatalf("first conversation received wrong AGENTS.md:\n%s", firstContent)
	}
	secondContent := injectAgentsMD(t, mw, contextkey.WithConversationID(t.Context(), "second"))
	if !strings.Contains(secondContent, "SECOND WORKSPACE") || strings.Contains(secondContent, "FIRST WORKSPACE") {
		t.Fatalf("second conversation received wrong AGENTS.md:\n%s", secondContent)
	}
}

func TestAgentsMDMissingRootIsSilent(t *testing.T) {
	convRepo, projectRepo := newAgentsMDRepos(t)
	bindAgentsMDWorkspace(t, convRepo, projectRepo, "missing", t.TempDir())
	mw, err := NewAgentsMDMiddleware(t.Context(), convRepo, projectRepo)
	if err != nil {
		t.Fatal(err)
	}

	ctx := contextkey.WithConversationID(t.Context(), "missing")
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{schema.UserMessage("hello")},
	}
	_, out, err := mw.BeforeModelRewriteState(ctx, state, nil)
	if err != nil {
		t.Fatalf("missing AGENTS.md must not fail a run: %v", err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Content != "hello" {
		t.Fatalf("missing AGENTS.md changed messages: %#v", out.Messages)
	}
}

func TestAgentsMDEnforcesBudgetAndRejectsImports(t *testing.T) {
	convRepo, projectRepo := newAgentsMDRepos(t)
	workspace := t.TempDir()
	bindAgentsMDWorkspace(t, convRepo, projectRepo, "conv", workspace)
	ctx := contextkey.WithConversationID(t.Context(), "conv")
	backend := &conversationAgentsMDBackend{convRepo: convRepo, projectRepo: projectRepo}

	t.Run("budget", func(t *testing.T) {
		content := strings.Repeat("a", agentsMDMaxBytes) + "MUST_NOT_BE_LOADED"
		writeAgentsMD(t, workspace, content)
		got, err := backend.Read(ctx, &agentsmd.ReadRequest{FilePath: agentsMDFileName})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Content) > agentsMDMaxBytes {
			t.Fatalf("loaded %d bytes, budget is %d", len(got.Content), agentsMDMaxBytes)
		}
		if strings.Contains(got.Content, "MUST_NOT_BE_LOADED") {
			t.Fatal("content beyond the AGENTS.md budget was loaded")
		}
	})

	t.Run("import", func(t *testing.T) {
		writeAgentsMD(t, workspace, "ROOT RULE\n@outside.md\n@.cursor/rules/hidden.md")
		if err := os.WriteFile(filepath.Join(workspace, "outside.md"), []byte("ESCAPED RULE"), 0o600); err != nil {
			t.Fatal(err)
		}
		mw, err := NewAgentsMDMiddleware(t.Context(), convRepo, projectRepo)
		if err != nil {
			t.Fatal(err)
		}
		content := injectAgentsMD(t, mw, ctx)
		if !strings.Contains(content, "ROOT RULE") {
			t.Fatalf("root AGENTS.md was not injected:\n%s", content)
		}
		if strings.Contains(content, "ESCAPED RULE") {
			t.Fatalf("@import escaped the root AGENTS.md:\n%s", content)
		}

		_, err = backend.Read(ctx, &agentsmd.ReadRequest{FilePath: "outside.md"})
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("non-root request error = %v, want os.ErrNotExist", err)
		}
		_, err = backend.Read(ctx, &agentsmd.ReadRequest{FilePath: ".cursor/rules/hidden.md"})
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf(".cursor/rules request error = %v, want os.ErrNotExist", err)
		}
	})
}

func TestAgentsMDRefreshesOnNextInvocation(t *testing.T) {
	convRepo, projectRepo := newAgentsMDRepos(t)
	workspace := t.TempDir()
	bindAgentsMDWorkspace(t, convRepo, projectRepo, "conv", workspace)
	ctx := contextkey.WithConversationID(t.Context(), "conv")
	mw, err := NewAgentsMDMiddleware(t.Context(), convRepo, projectRepo)
	if err != nil {
		t.Fatal(err)
	}

	writeAgentsMD(t, workspace, "OLD RULE")
	if got := injectAgentsMD(t, mw, ctx); !strings.Contains(got, "OLD RULE") {
		t.Fatalf("first invocation did not load old rule:\n%s", got)
	}
	writeAgentsMD(t, workspace, "NEW RULE")
	got := injectAgentsMD(t, mw, ctx)
	if !strings.Contains(got, "NEW RULE") || strings.Contains(got, "OLD RULE") {
		t.Fatalf("next invocation did not refresh AGENTS.md:\n%s", got)
	}
}

func newAgentsMDRepos(t *testing.T) (*repository.ConversationRepo, *repository.ProjectRepo) {
	t.Helper()
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "agentsmd.db"))
	if err != nil {
		t.Fatal(err)
	}
	return repository.NewConversationRepo(db), repository.NewProjectRepo(db)
}

func bindAgentsMDWorkspace(
	t *testing.T,
	convRepo *repository.ConversationRepo,
	projectRepo *repository.ProjectRepo,
	conversationID string,
	workspace string,
) {
	t.Helper()
	projectID := "project-" + conversationID
	if err := projectRepo.Create(t.Context(), &model.Project{
		ID:        projectID,
		Name:      projectID,
		Workspace: workspace,
	}); err != nil {
		t.Fatal(err)
	}
	if err := convRepo.Upsert(t.Context(), conversationID); err != nil {
		t.Fatal(err)
	}
	if err := convRepo.SetProjectID(t.Context(), conversationID, projectID); err != nil {
		t.Fatal(err)
	}
}

func writeAgentsMD(t *testing.T, workspace, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace, agentsMDFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func injectAgentsMD(t *testing.T, mw adk.ChatModelAgentMiddleware, ctx context.Context) string {
	t.Helper()
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{schema.UserMessage("hello")},
	}
	_, out, err := mw.BeforeModelRewriteState(ctx, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) < 2 {
		t.Fatalf("AGENTS.md was not injected: %#v", out.Messages)
	}
	return out.Messages[0].Content
}
