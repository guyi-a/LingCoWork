package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/effect"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/service"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

func TestApprovalModePersistsAndSessionGrantIsExplicit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "approval.db"))
	if err != nil {
		t.Fatal(err)
	}
	convRepo := repository.NewConversationRepo(db)
	if err := convRepo.Upsert(t.Context(), "conv"); err != nil {
		t.Fatal(err)
	}
	modes := approval.NewModeStore(convRepo)
	memory := approval.NewMemory()
	pending := approval.NewPendingStore(nil)
	chat := service.NewChatService(
		nil, "", stream.NewManager(), convRepo, nil, nil, nil,
		pending, modes, memory, false, nil,
	)
	router := gin.New()
	NewApprovalHandler(chat).Register(router)

	modeBody := bytes.NewBufferString(`{"mode":"accept-write"}`)
	modeResp := httptest.NewRecorder()
	router.ServeHTTP(modeResp, httptest.NewRequest(
		http.MethodPost, "/conversations/conv/approval-mode", modeBody,
	))
	if modeResp.Code != http.StatusNoContent {
		t.Fatalf("set mode status=%d body=%s", modeResp.Code, modeResp.Body.String())
	}
	fresh := approval.NewModeStore(convRepo)
	if got := fresh.Get("conv"); got != approval.ModeAcceptWrite {
		t.Fatalf("persisted mode=%q", got)
	}

	e := effect.Effect{
		Kind: effect.KindFileRead, Scope: effect.ScopeExternal, Path: "/tmp/a",
	}
	record := pending.Record("conv")
	for _, item := range []struct{ id, call string }{{"first", "tc-1"}, {"second", "tc-2"}} {
		record.Record("conv", item.id, &stream.ApprovalInfo{
			Tool: "read_file", Args: `{"path":"/tmp/a"}`,
			CallID: item.call, EffectJSON: e.JSON(), Rememberable: true,
		})
	}
	pendingResp := httptest.NewRecorder()
	router.ServeHTTP(pendingResp, httptest.NewRequest(
		http.MethodGet, "/conversations/conv/approvals/pending", nil,
	))
	if pendingResp.Code != http.StatusOK ||
		!bytes.Contains(pendingResp.Body.Bytes(), []byte(`"rememberable":true`)) {
		t.Fatalf("pending status=%d body=%s", pendingResp.Code, pendingResp.Body.String())
	}
	approveResp := httptest.NewRecorder()
	router.ServeHTTP(approveResp, httptest.NewRequest(
		http.MethodPost,
		"/conversations/conv/approvals/first",
		bytes.NewBufferString(`{"decision":"approve","scope":"session"}`),
	))
	if approveResp.Code != http.StatusAccepted {
		t.Fatalf("approve status=%d body=%s", approveResp.Code, approveResp.Body.String())
	}
	var result struct {
		Resumed bool `json:"resumed"`
	}
	if err := json.Unmarshal(approveResp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("first sibling must not resume the checkpoint")
	}
	if memory.Count("conv") != 1 {
		t.Fatalf("remembered count=%d", memory.Count("conv"))
	}
}
