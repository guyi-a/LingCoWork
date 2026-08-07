package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/instructions"
)

func TestInstructionHandlerCRUD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := instructions.NewStore(filepath.Join(t.TempDir(), ".lingcowork", "instructions"))
	router := gin.New()
	NewInstructionHandler(store).Register(router)

	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	body := `{"name":"summarize","label":"Summarize","description":"Make it shorter","prompt":"Summarize:\n\n{{input}}"}`
	if rec := request(http.MethodPost, "/instructions", body); rec.Code != http.StatusCreated {
		t.Fatalf("POST status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPost, "/instructions", body); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate POST status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec := request(http.MethodGet, "/instructions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list struct {
		Instructions []instructions.Instruction `json:"instructions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Instructions) != 1 {
		t.Fatalf("GET list = %+v, %v", list, err)
	}

	update := `{"label":"Short summary","description":"Updated","prompt":"Shorten {{input}}"}`
	if rec := request(http.MethodPut, "/instructions/summarize", update); rec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodGet, "/instructions/summarize", ""); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "Short summary") {
		t.Fatalf("GET item status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodDelete, "/instructions/summarize", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodGet, "/instructions/summarize", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET deleted status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestInstructionHandlerRejectsUnsafeName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewInstructionHandler(instructions.NewStore(t.TempDir())).Register(router)
	req := httptest.NewRequest(http.MethodGet, "/instructions/Bad_Name", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
