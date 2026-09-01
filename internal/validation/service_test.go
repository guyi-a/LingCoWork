package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

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
	changes := repository.NewWorkspaceChangeRepo(db)
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
		repository.NewValidationRepo(db), messages, convs, projects, changes,
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
	if output.ValidationDigest == nil || output.ValidationDigest.Fingerprint == "" ||
		len(output.ValidationDigest.Diagnostics) != 1 {
		t.Fatalf("validation digest = %#v", output.ValidationDigest)
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

func TestCompletionStatusTracksChangesAndLatestValidation(t *testing.T) {
	service, ctx := newValidationFixture(t)
	conv, _ := service.convs.Get(ctx, "conv")
	project, _ := service.projects.Get(ctx, *conv.ProjectID)
	if err := service.changes.CreateEvent(ctx, &model.WorkspaceChangeEvent{
		ProjectID: "project", ConversationID: "conv", UserMessageSeq: 1,
		ToolCallID: "write-1", ToolName: "apply_patch", Operation: "write",
		Path: "internal/a.go", Attribution: "agent", Succeeded: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := service.EvaluateCompletion(ctx); got.State != GateMissing {
		t.Fatalf("after change state=%s", got.State)
	}

	args := `{"command":"go test ./internal/...","validation_kind":"test"}`
	service.Enrich(ctx, &compose.ToolInput{
		CallID: "test-failed", Name: "run_command", Arguments: args,
	}, `{"exit_code":1,"stderr":"internal/a.go:2: error: bad","cwd":"`+project.Workspace+`"}`)
	failed := service.EvaluateCompletion(ctx)
	if failed.State != GateFailed || len(failed.Digests) != 1 ||
		failed.Fingerprint == "" {
		t.Fatalf("failed status=%#v", failed)
	}

	service.Enrich(ctx, &compose.ToolInput{
		CallID: "test-passed", Name: "run_command", Arguments: args,
	}, `{"exit_code":0,"stdout":"ok","stderr":"","cwd":"`+project.Workspace+`"}`)
	if got := service.EvaluateCompletion(ctx); got.State != GatePassed {
		t.Fatalf("after passing validation state=%s digests=%#v", got.State, got.Digests)
	}
}

func TestCompletionGateRewritesPrematureFinalAnswer(t *testing.T) {
	service, ctx := newValidationFixture(t)
	if err := service.changes.CreateEvent(ctx, &model.WorkspaceChangeEvent{
		ProjectID: "project", ConversationID: "conv", UserMessageSeq: 1,
		ToolCallID: "write-1", ToolName: "write_file", Operation: "write",
		Path: "src/a.ts", Attribution: "agent", Succeeded: true,
	}); err != nil {
		t.Fatal(err)
	}
	middleware := service.CompletionMiddleware().(*CompletionGateMiddleware)
	last := middleware.rewriteFinal(ctx, schema.AssistantMessage("done", nil))
	if last.Content != "" || len(last.ToolCalls) != 1 ||
		last.ToolCalls[0].Function.Name != GateToolName {
		t.Fatalf("rewritten final=%#v", last)
	}
}

func TestCompletionGateIgnoresDocumentationOnlyChanges(t *testing.T) {
	service, ctx := newValidationFixture(t)
	if err := service.changes.CreateEvent(ctx, &model.WorkspaceChangeEvent{
		ProjectID: "project", ConversationID: "conv", UserMessageSeq: 1,
		ToolCallID: "write-doc", ToolName: "write_file", Operation: "write",
		Path: "README.md", Attribution: "agent", Succeeded: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := service.EvaluateCompletion(ctx); got.State != GatePassed {
		t.Fatalf("documentation-only change state=%s", got.State)
	}
}

func TestValidationDigestIsBoundedAndStable(t *testing.T) {
	diagnostics := make([]Diagnostic, 0, 25)
	for i := 24; i >= 0; i-- {
		diagnostics = append(diagnostics, Diagnostic{
			ID: fmt.Sprintf("d-%02d", i), Severity: "error",
			Path: "a.go", Line: i + 1, Message: fmt.Sprintf("error %d", i),
		})
	}
	summary := Summary{
		Kind: KindTest, Passed: false, Diagnostics: diagnostics, ErrorCount: 25,
	}
	first := NewDigest("go test ./...", "/ws", summary)
	second := NewDigest("go test ./...", "/ws", summary)
	if len(first.Diagnostics) != 20 || !first.Truncated {
		t.Fatalf("digest size=%d truncated=%v", len(first.Diagnostics), first.Truncated)
	}
	if first.Fingerprint == "" || first.Fingerprint != second.Fingerprint {
		t.Fatalf("unstable fingerprints %q %q", first.Fingerprint, second.Fingerprint)
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
