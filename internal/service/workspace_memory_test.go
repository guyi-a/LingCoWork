package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/memory"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

// 项目记忆有自己的编辑入口，所以它不该出现在文件树里。但只有工作区根下那一个
// 算项目记忆 —— 子目录里的同名文件是用户的普通文件，一起藏掉就是让文件凭空
// 消失。
func TestTreeHidesOnlyTheProjectMemoryFile(t *testing.T) {
	ctx := context.Background()
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	projectRepo := repository.NewProjectRepo(db)
	convRepo := repository.NewConversationRepo(db)

	root := t.TempDir()
	for _, rel := range []string{
		memory.FileName,
		"notes.md",
		filepath.Join("reports", memory.FileName),
	} {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := projectRepo.Create(ctx, &model.Project{
		ID:        "proj",
		Name:      "proj",
		Workspace: root,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := convRepo.Upsert(ctx, "conv"); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}
	if err := convRepo.SetProjectID(ctx, "conv", "proj"); err != nil {
		t.Fatalf("bind project: %v", err)
	}

	res, err := NewWorkspaceService(convRepo, projectRepo).Tree(ctx, "conv")
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}

	got := make(map[string]bool, len(res.Entries))
	for _, e := range res.Entries {
		got[e.Path] = true
	}
	if got[memory.FileName] {
		t.Errorf("%s at the workspace root should be hidden; entries: %v", memory.FileName, got)
	}
	for _, want := range []string{"notes.md", "reports/" + memory.FileName} {
		if !got[want] {
			t.Errorf("%s should stay visible; entries: %v", want, got)
		}
	}
}
