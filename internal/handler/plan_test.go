package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/workplan"
)

func TestPlanHandlerLoadsEditsAndRejectsStaleRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	plans := workplan.NewService(repository.NewWorkPlanRepo(db))
	draft, err := plans.CreateDraft(t.Context(), "conv", 1, "old", "body", []workplan.Item{
		{ID: "one", Content: "First", Status: workplan.ItemPending},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	NewPlanHandler(plans, nil).Register(router)

	load := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/conversations/conv/plans/latest", nil)
	router.ServeHTTP(load, req)
	if load.Code != http.StatusOK {
		t.Fatalf("load status = %d body=%s", load.Code, load.Body.String())
	}

	body, _ := json.Marshal(planEditRequest{
		Revision: draft.Revision,
		Overview: "new",
		BodyMD:   "new body",
		Items: []workplan.Item{
			{ID: "one", Content: "First edited", Status: workplan.ItemPending},
		},
	})
	edit := httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPut,
		"/conversations/conv/plans/"+draft.ID,
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(edit, req)
	if edit.Code != http.StatusOK {
		t.Fatalf("edit status = %d body=%s", edit.Code, edit.Body.String())
	}

	stale := httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPut,
		"/conversations/conv/plans/"+draft.ID,
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(stale, req)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale status = %d body=%s", stale.Code, stale.Body.String())
	}
}
