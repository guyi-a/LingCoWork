package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/service"
)

func TestWorkspaceDiffHandlerValidationAndNonGitResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
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
		ID: "project", Name: "project", Workspace: t.TempDir(),
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
		ConversationID: "conversation", Role: "user", Content: "inspect",
	}); err != nil {
		t.Fatalf("append user: %v", err)
	}

	workspace := service.NewWorkspaceService(convRepo, projectRepo)
	diff := service.NewWorkspaceDiffService(workspace, messageRepo, changeRepo)
	router := gin.New()
	NewWorkspaceHandler(workspace, diff).Register(router)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/conversations/conversation/workspace/changes?scope=all&project_id=project",
		nil,
	)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("changes status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		GitRepository bool `json:"git_repository"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode changes: %v", err)
	}
	if body.GitRepository {
		t.Fatal("non-git workspace reported as Git")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodGet,
		"/conversations/conversation/workspace/changes?scope=invalid&project_id=project",
		nil,
	)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodGet,
		"/conversations/conversation/workspace/diff?scope=all&project_id=project",
		nil,
	)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing path status=%d body=%s", rec.Code, rec.Body.String())
	}
}
