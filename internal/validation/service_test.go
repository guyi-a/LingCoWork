package validation

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

func newValidationFixture(t *testing.T) (*Service, context.Context) {
	t.Helper()
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	projects := repository.NewProjectRepo(db)
	convs := repository.NewConversationRepo(db)
	messages := repository.NewMessageRepo(db)
	if err := projects.Create(t.Context(), &model.Project{
		ID: "project", Name: "project", Workspace: workspace,
	}); err != nil {
		t.Fatal(err)
	}
	if err := convs.Upsert(t.Context(), "conv"); err != nil {
		t.Fatal(err)
	}
	if err := convs.SetProjectID(t.Context(), "conv", "project"); err != nil {
		t.Fatal(err)
	}
	if err := messages.Append(t.Context(), &model.Message{
		ConversationID: "conv", Role: "user", Content: "fix",
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(
		repository.NewValidationRepo(db), messages, convs, projects,
	)
	return service, contextkey.WithConversationID(t.Context(), "conv")
}

func TestEnrichPersistsDeclaredValidationOnly(t *testing.T) {
	service, ctx := newValidationFixture(t)
	result := `{"exit_code":1,"duration_ms":12,"stdout":"","stderr":"src/a.ts(3,4): error TS1: bad","cwd":"WORKSPACE"}`
	var raw map[string]any
	_ = json.Unmarshal([]byte(result), &raw)
	conv, _ := service.convs.Get(ctx, "conv")
	project, _ := service.projects.Get(ctx, *conv.ProjectID)
	raw["cwd"] = project.Workspace
	encoded, _ := json.Marshal(raw)

	enriched := service.Enrich(ctx, &compose.ToolInput{
		CallID: "call-1", Name: "run_command",
		Arguments: `{"command":"tsc --pretty false","validation_kind":"typecheck"}`,
	}, string(encoded))
	var output commandResult
	if err := json.Unmarshal([]byte(enriched), &output); err != nil {
		t.Fatal(err)
	}
	if output.Validation == nil || output.Validation.ErrorCount != 1 {
		t.Fatalf("enriched output = %#v", output)
	}
	problems, err := service.ListProblems(ctx, "conv", "current")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems.Runs) != 1 || problems.ErrorCount != 1 {
		t.Fatalf("problems = %#v", problems)
	}

	ordinary := service.Enrich(ctx, &compose.ToolInput{
		CallID: "call-2", Name: "run_command",
		Arguments: `{"command":"pwd"}`,
	}, string(encoded))
	if ordinary != string(encoded) {
		t.Fatal("ordinary command output was changed")
	}
	service.Enrich(ctx, &compose.ToolInput{
		CallID: "call-1", Name: "run_command",
		Arguments: `{"command":"tsc --pretty false","validation_kind":"typecheck"}`,
	}, string(encoded))
	rows, err := service.runs.ListConversation(ctx, "conv", 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("idempotent rows=%d err=%v", len(rows), err)
	}
	if err := service.convs.Delete(ctx, "conv"); err != nil {
		t.Fatal(err)
	}
	rows, err = service.runs.ListConversation(ctx, "conv", 0)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows after conversation delete=%d err=%v", len(rows), err)
	}
}

func TestLatestEquivalentRunSupersedesOldFailure(t *testing.T) {
	service, ctx := newValidationFixture(t)
	conv, _ := service.convs.Get(ctx, "conv")
	project, _ := service.projects.Get(ctx, *conv.ProjectID)
	args := `{"command":"go test ./...","validation_kind":"test"}`
	service.Enrich(ctx, &compose.ToolInput{
		CallID: "failed", Name: "run_command", Arguments: args,
	}, `{"exit_code":1,"stderr":"foo.go:2: error: bad","cwd":"`+project.Workspace+`"}`)
	service.Enrich(ctx, &compose.ToolInput{
		CallID: "passed", Name: "run_command", Arguments: args,
	}, `{"exit_code":0,"stdout":"ok","stderr":"","cwd":"`+project.Workspace+`"}`)

	problems, err := service.ListProblems(ctx, "conv", "current")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems.Runs) != 1 || !problems.Runs[0].Summary.Passed ||
		problems.ErrorCount != 0 {
		t.Fatalf("problems = %#v", problems)
	}
}
