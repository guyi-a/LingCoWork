package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

func newProjectTestRepos(t *testing.T) (*repository.ProjectRepo, *repository.ConversationRepo, *repository.MessageRepo) {
	t.Helper()
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	return repository.NewProjectRepo(db), repository.NewConversationRepo(db), repository.NewMessageRepo(db)
}

func TestOpenOrCreateFromPathCanonicalizesAndReuses(t *testing.T) {
	projectRepo, convRepo, _ := newProjectTestRepos(t)
	svc := NewProjectService(projectRepo, convRepo, nil, nil)
	ctx := context.Background()

	parent := t.TempDir()
	workspace := filepath.Join(parent, "repo")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "repo-link")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}

	createdProject, created, err := svc.OpenOrCreateFromPath(ctx, alias, "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first open did not create a project")
	}
	if _, err := uuid.Parse(createdProject.ID); err != nil {
		t.Fatalf("project id %q is not a UUID: %v", createdProject.ID, err)
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if createdProject.Workspace != canonical {
		t.Fatalf("workspace=%q, want canonical %q", createdProject.Workspace, canonical)
	}
	if createdProject.Name != filepath.Base(canonical) {
		t.Fatalf("name=%q, want basename %q", createdProject.Name, filepath.Base(canonical))
	}

	reused, created, err := svc.OpenOrCreateFromPath(ctx, workspace, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("same realpath created a duplicate project")
	}
	if reused.ID != createdProject.ID {
		t.Fatalf("reused id=%q, want %q", reused.ID, createdProject.ID)
	}
}

func TestOpenOrCreateFromPathReusesLegacyNonCanonicalRow(t *testing.T) {
	projectRepo, convRepo, _ := newProjectTestRepos(t)
	svc := NewProjectService(projectRepo, convRepo, nil, nil)
	ctx := context.Background()

	parent := t.TempDir()
	workspace := filepath.Join(parent, "repo")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "legacy-link")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	legacy := &model.Project{ID: "legacy-project", Name: "Legacy", Workspace: alias}
	if err := projectRepo.Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	got, created, err := svc.OpenOrCreateFromPath(ctx, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if created || got.ID != legacy.ID {
		t.Fatalf("got id=%q created=%v, want legacy row", got.ID, created)
	}
}

func TestOpenOrCreateFromPathRejectsInvalidPaths(t *testing.T) {
	projectRepo, convRepo, _ := newProjectTestRepos(t)
	svc := NewProjectService(projectRepo, convRepo, nil, nil)
	ctx := context.Background()

	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []string{
		"relative/path",
		filepath.Join(t.TempDir(), "missing"),
		file,
	}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			if _, _, err := svc.OpenOrCreateFromPath(ctx, path, ""); !errors.Is(err, ErrInvalidWorkspace) {
				t.Fatalf("error=%v, want ErrInvalidWorkspace", err)
			}
		})
	}
}

func TestDeleteProjectKeepsWorkspaceOnDisk(t *testing.T) {
	projectRepo, convRepo, _ := newProjectTestRepos(t)
	svc := NewProjectService(projectRepo, convRepo, nil, nil)
	ctx := context.Background()

	workspace := t.TempDir()
	marker := filepath.Join(workspace, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	project, _, err := svc.OpenOrCreateFromPath(ctx, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("workspace content was deleted: %v", err)
	}
	if got, err := projectRepo.Get(ctx, project.ID); err != nil || got != nil {
		t.Fatalf("project row after delete: project=%v err=%v", got, err)
	}
}

func TestChatStartRequiresWorkspaceBeforeCreatingConversation(t *testing.T) {
	projectRepo, convRepo, msgRepo := newProjectTestRepos(t)
	svc := &ChatService{
		convRepo:    convRepo,
		msgRepo:     msgRepo,
		projectRepo: projectRepo,
	}
	const conversationID = "conversation-without-workspace"
	if _, err := svc.Start(context.Background(), conversationID, "hello", "", "", "", ""); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("error=%v, want ErrWorkspaceRequired", err)
	}
	if got, err := convRepo.Get(context.Background(), conversationID); err != nil || got != nil {
		t.Fatalf("conversation was persisted without workspace: conversation=%v err=%v", got, err)
	}
}

func TestConversationListHidesLegacyUnboundRows(t *testing.T) {
	projectRepo, convRepo, _ := newProjectTestRepos(t)
	ctx := context.Background()
	if err := convRepo.Upsert(ctx, "legacy-unbound"); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	project := &model.Project{ID: "bound-project", Name: "Bound", Workspace: workspace}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := convRepo.Upsert(ctx, "bound-conversation"); err != nil {
		t.Fatal(err)
	}
	if err := convRepo.SetProjectID(ctx, "bound-conversation", project.ID); err != nil {
		t.Fatal(err)
	}

	items, err := convRepo.List(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "bound-conversation" {
		t.Fatalf("listed conversations=%v, want only bound conversation", items)
	}
}
