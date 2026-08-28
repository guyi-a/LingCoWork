package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/service"
)

func newProjectTestRouter(t *testing.T) (*gin.Engine, *repository.ProjectRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	projectRepo := repository.NewProjectRepo(db)
	projectService := service.NewProjectService(
		projectRepo,
		repository.NewConversationRepo(db),
		nil,
		nil,
	)
	router := gin.New()
	NewProjectHandler(projectService).Register(router)
	return router, projectRepo
}

func projectRequest(t *testing.T, router http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, target, &payload)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	return res
}

func TestProjectHandlerOpenReuseAndDeleteWithoutDiskRemoval(t *testing.T) {
	router, projectRepo := newProjectTestRouter(t)
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	first := projectRequest(t, router, http.MethodPost, "/projects", map[string]string{"path": workspace})
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var opened struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Created bool `json:"created"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.Created || opened.Project.ID == "" {
		t.Fatalf("unexpected create response: %s", first.Body.String())
	}

	second := projectRequest(t, router, http.MethodPost, "/projects", map[string]string{"path": workspace})
	if second.Code != http.StatusOK {
		t.Fatalf("reuse status=%d body=%s", second.Code, second.Body.String())
	}
	var reused struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Created bool `json:"created"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &reused); err != nil {
		t.Fatal(err)
	}
	if reused.Created || reused.Project.ID != opened.Project.ID {
		t.Fatalf("unexpected reuse response: %s", second.Body.String())
	}

	deleted := projectRequest(
		t,
		router,
		http.MethodDelete,
		"/projects/"+opened.Project.ID,
		nil,
	)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("workspace content was deleted: %v", err)
	}
	if project, err := projectRepo.Get(context.Background(), opened.Project.ID); err != nil || project != nil {
		t.Fatalf("project row after delete: project=%v err=%v", project, err)
	}
}

func TestProjectHandlerRejectsRelativePath(t *testing.T) {
	router, _ := newProjectTestRouter(t)
	res := projectRequest(t, router, http.MethodPost, "/projects", map[string]string{"path": "relative"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}
