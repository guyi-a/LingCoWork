package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/service"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

func TestResumeEndpointRejectsStaleHistoryCursor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	messages := repository.NewMessageRepo(db)
	row := &model.Message{ConversationID: "conv", Role: "user", Content: "hi"}
	if err := messages.Append(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	manager := stream.NewManager()
	buf := manager.CreateAt("conv", row.Seq)
	chat := service.NewChatService(
		nil, "", manager, repository.NewConversationRepo(db), messages,
		repository.NewProjectRepo(db), nil, nil, nil, nil, false, nil,
	)
	router := gin.New()
	NewChatHandler(chat).Register(router)

	invalidMode := httptest.NewRecorder()
	invalidModeReq := httptest.NewRequest(
		http.MethodPost,
		"/chat/invalid-mode",
		bytes.NewBufferString(`{"message":"hi","approval_mode":"full_access"}`),
	)
	invalidModeReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(invalidMode, invalidModeReq)
	if invalidMode.Code != http.StatusBadRequest {
		t.Fatalf("invalid approval mode status=%d body=%s", invalidMode.Code, invalidMode.Body.String())
	}

	stale := httptest.NewRecorder()
	router.ServeHTTP(stale, httptest.NewRequest(
		http.MethodGet, "/chat/conv?after_seq=0", nil,
	))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		buf.Finish()
	}()
	equal := httptest.NewRecorder()
	router.ServeHTTP(equal, httptest.NewRequest(
		http.MethodGet, "/chat/conv?after_seq=1", nil,
	))
	if equal.Code != http.StatusOK {
		t.Fatalf("equal status=%d body=%s", equal.Code, equal.Body.String())
	}
}
