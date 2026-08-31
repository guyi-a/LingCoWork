package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/validation"
)

func TestProblemsHandlerCurrentAndInvalidScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	projects := repository.NewProjectRepo(db)
	convs := repository.NewConversationRepo(db)
	messages := repository.NewMessageRepo(db)
	runs := repository.NewValidationRepo(db)
	if err := projects.Create(t.Context(), &model.Project{
		ID: "project", Name: "project", Workspace: t.TempDir(),
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
	diagnostics, _ := json.Marshal([]validation.Diagnostic{{
		ID: "d1", Severity: "error", Message: "broken",
		Path: "main.go", Line: 4, Column: 2,
	}})
	if err := runs.Upsert(t.Context(), &model.ValidationRun{
		ProjectID: "project", ConversationID: "conv", UserMessageSeq: 1,
		ToolCallID: "call", Command: "go test ./...", Cwd: "/workspace",
		Kind: "test", ExitCode: 1, DiagnosticsJSON: string(diagnostics),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	service := validation.NewService(runs, messages, convs, projects)
	router := gin.New()
	NewProblemsHandler(service).Register(router)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet, "/conversations/conv/workspace/problems", nil,
	)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body validation.Problems
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ErrorCount != 1 || len(body.Runs) != 1 {
		t.Fatalf("body=%#v", body)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodGet, "/conversations/conv/workspace/problems?scope=nope", nil,
	)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope status=%d", rec.Code)
	}
}
