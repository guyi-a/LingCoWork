package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

func TestWorkspaceDiffServiceGitAllAndAgentScopes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init")
	writeWorkspaceDiffFixture(t, root, "main.go", "package main\n\nconst value = 1\n")
	runGit(t, root, "add", ".")
	runGit(t, root,
		"-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-m", "initial",
	)

	diffSvc, changeRepo := newWorkspaceDiffFixture(t, root)
	original := []byte("package main\n\nconst value = 1\n")
	if _, err := changeRepo.CreateBaselineIfAbsent(ctx, &model.WorkspaceFileBaseline{
		ProjectID: "project", ConversationID: "conversation", UserMessageSeq: 1,
		Path: "main.go", Existed: true, Content: original, SHA256: hashBytes(original),
	}); err != nil {
		t.Fatalf("create baseline: %v", err)
	}
	if err := changeRepo.CreateEvent(ctx, &model.WorkspaceChangeEvent{
		ProjectID: "project", ConversationID: "conversation", UserMessageSeq: 1,
		ToolCallID: "call-1", ToolName: "apply_patch", Operation: "patch",
		Path: "main.go", Attribution: "agent", Succeeded: true,
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	writeWorkspaceDiffFixture(t, root, "main.go", "package main\n\nconst value = 2\n")
	writeWorkspaceDiffFixture(t, root, "new.txt", "new\n")

	all, err := diffSvc.Changes(ctx, "conversation", "project", "all")
	if err != nil {
		t.Fatalf("all changes: %v", err)
	}
	if !all.GitRepository || len(all.Files) != 2 {
		t.Fatalf("all changes = %#v", all)
	}
	agent, err := diffSvc.Changes(ctx, "conversation", "project", "agent")
	if err != nil {
		t.Fatalf("agent changes: %v", err)
	}
	if len(agent.Files) != 1 || agent.Files[0].Path != "main.go" ||
		agent.Files[0].Additions != 1 || agent.Files[0].Deletions != 1 {
		t.Fatalf("agent changes = %#v", agent.Files)
	}
	if len(agent.Files[0].Tools) != 1 || agent.Files[0].Tools[0] != "apply_patch" {
		t.Fatalf("agent tools = %#v", agent.Files[0].Tools)
	}

	diff, err := diffSvc.Diff(ctx, "conversation", "project", "agent", "main.go")
	if err != nil {
		t.Fatalf("agent diff: %v", err)
	}
	if !strings.Contains(diff.Patch, "-const value = 1") ||
		!strings.Contains(diff.Patch, "+const value = 2") {
		t.Fatalf("unexpected agent diff:\n%s", diff.Patch)
	}
}

func TestWorkspaceDiffServiceNonGitAndSensitiveBaseline(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	diffSvc, changeRepo := newWorkspaceDiffFixture(t, root)

	all, err := diffSvc.Changes(ctx, "conversation", "project", "all")
	if err != nil {
		t.Fatalf("non-git changes: %v", err)
	}
	if all.GitRepository || len(all.Files) != 0 {
		t.Fatalf("non-git result = %#v", all)
	}

	if _, err := changeRepo.CreateBaselineIfAbsent(ctx, &model.WorkspaceFileBaseline{
		ProjectID: "project", ConversationID: "conversation", UserMessageSeq: 1,
		Path: ".npmrc", Existed: true, Sensitive: true,
	}); err != nil {
		t.Fatalf("create sensitive baseline: %v", err)
	}
	writeWorkspaceDiffFixture(t, root, ".npmrc", "TOKEN=secret\n")
	agent, err := diffSvc.Changes(ctx, "conversation", "project", "agent")
	if err != nil {
		t.Fatalf("agent changes: %v", err)
	}
	if len(agent.Files) != 1 || !agent.Files[0].Sensitive {
		t.Fatalf("sensitive result = %#v", agent.Files)
	}
	diff, err := diffSvc.Diff(ctx, "conversation", "project", "agent", ".npmrc")
	if err != nil {
		t.Fatalf("sensitive diff: %v", err)
	}
	if diff.Patch != "" {
		t.Fatal("sensitive file leaked patch content")
	}
}

func newWorkspaceDiffFixture(
	t *testing.T,
	root string,
) (*WorkspaceDiffService, *repository.WorkspaceChangeRepo) {
	t.Helper()
	ctx := context.Background()
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
		t.Fatalf("upsert conversation: %v", err)
	}
	if err := convRepo.SetProjectID(ctx, "conversation", "project"); err != nil {
		t.Fatalf("bind project: %v", err)
	}
	if err := messageRepo.Append(ctx, &model.Message{
		ConversationID: "conversation", Role: "user", Content: "change it",
	}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	workspace := NewWorkspaceService(convRepo, projectRepo)
	return NewWorkspaceDiffService(workspace, messageRepo, changeRepo), changeRepo
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeWorkspaceDiffFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
