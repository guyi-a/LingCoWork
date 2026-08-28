package changes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/effect"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

func TestTrackerCapturesFirstStructuredWriteBaseline(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	tracker, changeRepo, ctx := newTrackerFixture(t, root)
	tracker.effects.Register("write_file", effect.Static(effect.Effect{
		Kind: effect.KindFileWrite, Scope: effect.ScopeWorkspace, Path: path,
	}))
	endpoint := tracker.Middleware().Invokable(func(
		_ context.Context,
		_ *compose.ToolInput,
	) (*compose.ToolOutput, error) {
		if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
			return nil, err
		}
		return &compose.ToolOutput{Result: `{"path":"main.go"}`}, nil
	})
	if _, err := endpoint(ctx, &compose.ToolInput{
		Name: "write_file", CallID: "call-1", Arguments: `{"path":"main.go"}`,
	}); err != nil {
		t.Fatalf("tracked write: %v", err)
	}

	baseline, err := changeRepo.GetBaseline(ctx, "conversation", 1, "main.go")
	if err != nil {
		t.Fatalf("get baseline: %v", err)
	}
	if baseline == nil || string(baseline.Content) != "old\n" {
		t.Fatalf("baseline = %#v", baseline)
	}
	events, err := changeRepo.ListEvents(ctx, "conversation", 1)
	if err != nil || len(events) != 1 || events[0].ToolName != "write_file" {
		t.Fatalf("events = %#v err=%v", events, err)
	}
}

func TestTrackerCapturesRunCommandNewDirtyFileFromHead(t *testing.T) {
	root := t.TempDir()
	runTrackerGit(t, root, "init")
	path := filepath.Join(root, "main.go")
	otherPath := filepath.Join(root, "user-dirty.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(otherPath, []byte("clean\n"), 0o644); err != nil {
		t.Fatalf("write second fixture: %v", err)
	}
	runTrackerGit(t, root, "add", ".")
	runTrackerGit(t, root,
		"-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-m", "initial",
	)
	if err := os.WriteFile(otherPath, []byte("user change\n"), 0o644); err != nil {
		t.Fatalf("make user change: %v", err)
	}

	tracker, changeRepo, ctx := newTrackerFixture(t, root)
	tracker.effects.Register("run_command", effect.Static(effect.Effect{
		Kind: effect.KindProcessExec, Scope: effect.ScopeWorkspace,
		Command: "replace", Cwd: root, Classification: effect.Normal,
	}))
	endpoint := tracker.Middleware().Invokable(func(
		_ context.Context,
		_ *compose.ToolInput,
	) (*compose.ToolOutput, error) {
		if err := os.WriteFile(path, []byte("after\n"), 0o644); err != nil {
			return nil, err
		}
		return &compose.ToolOutput{Result: `{"exit_code":0}`}, nil
	})
	if _, err := endpoint(ctx, &compose.ToolInput{
		Name: "run_command", CallID: "call-shell", Arguments: `{"command":"replace"}`,
	}); err != nil {
		t.Fatalf("tracked command: %v", err)
	}

	baseline, err := changeRepo.GetBaseline(ctx, "conversation", 1, "main.go")
	if err != nil {
		t.Fatalf("get baseline: %v", err)
	}
	if baseline == nil || string(baseline.Content) != "before\n" {
		t.Fatalf("baseline = %#v", baseline)
	}
	untouched, err := changeRepo.GetBaseline(ctx, "conversation", 1, "user-dirty.txt")
	if err != nil {
		t.Fatalf("get untouched baseline: %v", err)
	}
	if untouched != nil {
		t.Fatalf("unchanged user-dirty file was captured: %#v", untouched)
	}
	events, err := changeRepo.ListEvents(ctx, "conversation", 1)
	if err != nil || len(events) != 1 || events[0].Operation != "shell" {
		t.Fatalf("events = %#v err=%v", events, err)
	}
}

func newTrackerFixture(
	t *testing.T,
	root string,
) (*Tracker, *repository.WorkspaceChangeRepo, context.Context) {
	t.Helper()
	ctx := contextkey.WithConversationID(context.Background(), "conversation")
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	projectRepo := repository.NewProjectRepo(db)
	convRepo := repository.NewConversationRepo(db)
	messageRepo := repository.NewMessageRepo(db)
	changeRepo := repository.NewWorkspaceChangeRepo(db)
	if err := projectRepo.Create(ctx, &model.Project{
		ID: "project", Name: "project", Workspace: root,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := convRepo.Upsert(ctx, "conversation"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := convRepo.SetProjectID(ctx, "conversation", "project"); err != nil {
		t.Fatalf("bind project: %v", err)
	}
	if err := messageRepo.Append(ctx, &model.Message{
		ConversationID: "conversation", Role: "user", Content: "change it",
	}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	registry := effect.NewRegistry()
	return NewTracker(changeRepo, messageRepo, convRepo, projectRepo, registry),
		changeRepo, ctx
}

func runTrackerGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
