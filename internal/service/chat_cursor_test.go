package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

func TestResumeStreamCoordinatesHistoryAndBufferCursor(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	messages := repository.NewMessageRepo(db)
	user := &model.Message{ConversationID: "conv", Role: "user", Content: "hi"}
	if err := messages.Append(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	manager := stream.NewManager()
	buf := manager.CreateAt("conv", user.Seq)
	buf.Append([]byte("partial"))
	svc := &ChatService{manager: manager, msgRepo: messages}

	ch, status, durable, err := svc.ResumeStream(t.Context(), "conv", user.Seq)
	if err != nil || status != stream.CursorEqual || durable != user.Seq || ch == nil {
		t.Fatalf("equal ch=%v status=%s durable=%d err=%v", ch, status, durable, err)
	}
	if got := string(receiveCursor(t, ch)); got != "partial" {
		t.Fatalf("partial=%q", got)
	}
	if _, status, _, _ := svc.ResumeStream(t.Context(), "conv", 0); status != stream.CursorClientStale {
		t.Fatalf("stale status=%s", status)
	}

	assistant := &model.Message{ConversationID: "conv", Role: "assistant", Content: "done"}
	if err := messages.Append(t.Context(), assistant); err != nil {
		t.Fatal(err)
	}
	if _, status, _, _ := svc.ResumeStream(t.Context(), "conv", assistant.Seq); status != stream.CursorBufferBehind {
		t.Fatalf("DB-ahead status=%s", status)
	}
	buf.CommitBoundary(assistant.Seq)
	if _, status, _, _ := svc.ResumeStream(t.Context(), "conv", user.Seq); status != stream.CursorClientStale {
		t.Fatalf("post-commit stale status=%s", status)
	}
	buf.Finish()
	if _, status, durable, err := svc.ResumeStream(t.Context(), "conv", assistant.Seq); err != nil ||
		status != stream.CursorComplete || durable != assistant.Seq {
		t.Fatalf("complete status=%s durable=%d err=%v", status, durable, err)
	}
}

func receiveCursor(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream")
		return nil
	}
}
