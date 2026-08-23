package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/memory"
	"github.com/guyi-a/Interview-Agent/internal/service"
)

func newMemoryRouter(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), ".lingcowork", "memory.md")
	router := gin.New()
	// WorkspaceService 只在项目级用到，用户级的路径不经过它。
	svc := service.NewMemoryService(memory.NewRegistry(), path, nil)
	NewMemoryHandler(svc).Register(router)
	return router, path
}

func TestUserMemoryReadWrite(t *testing.T) {
	router, _ := newMemoryRouter(t)

	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) service.MemoryResult {
		t.Helper()
		var out service.MemoryResult
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		return out
	}

	// 还没写过时 GET 也要成功，并且给出一个可用于首次 PUT 的哈希。
	rec := request(http.MethodGet, "/memory/user", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	empty := decode(rec)
	if empty.Content != "" || empty.Hash == "" {
		t.Fatalf("first GET = %+v, want empty content with a usable hash", empty)
	}
	if empty.Limit != memory.MaxBytes {
		t.Errorf("limit = %d, want %d", empty.Limit, memory.MaxBytes)
	}

	rec = request(http.MethodPut, "/memory/user",
		fmt.Sprintf(`{"content":"- [2026-08-23] 回答用中文\n","hash":%q}`, empty.Hash))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 存进去的是旧的逐行带日期写法，落盘时会迁移成按天分组 —— 内容自带日期，
	// 所以结果跟"今天"是哪天无关。
	saved := decode(rec)
	if saved.Content != "## 2026-08-23\n- 回答用中文\n" {
		t.Errorf("content = %q", saved.Content)
	}

	// 用过期的哈希再存一次 —— 这模拟用户编辑期间 Agent 写过一次的情形，必须
	// 拒绝，否则用户的版本会静默盖掉 Agent 那条。
	rec = request(http.MethodPut, "/memory/user",
		fmt.Sprintf(`{"content":"覆盖\n","hash":%q}`, empty.Hash))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale PUT status=%d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	// 冲突不能改动磁盘。
	if got := decode(request(http.MethodGet, "/memory/user", "")); got.Content != saved.Content {
		t.Errorf("content changed after a rejected PUT: %q", got.Content)
	}

	// 带上新哈希就能存进去。
	rec = request(http.MethodPut, "/memory/user",
		fmt.Sprintf(`{"content":"覆盖\n","hash":%q}`, saved.Hash))
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUserMemoryRejectsOversize(t *testing.T) {
	router, _ := newMemoryRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/memory/user", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var current service.MemoryResult
	if err := json.Unmarshal(rec.Body.Bytes(), &current); err != nil {
		t.Fatalf("decode: %v", err)
	}

	body := fmt.Sprintf(`{"content":%q,"hash":%q}`, strings.Repeat("x", memory.MaxBytes+1), current.Hash)
	req = httptest.NewRequest(http.MethodPut, "/memory/user", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize PUT status=%d, want 413; body=%s", rec.Code, rec.Body.String())
	}
}
