package service

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

func TestMessageJournalPersistsBoundariesIdempotently(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewMessageRepo(db)
	if err := repo.Append(t.Context(), &model.Message{
		ConversationID: "conv", Role: "user", Content: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	journal := newMessageJournal(repo, "conv", "run-1")
	firstAssistant := stream.AssistantTurnRecord{
		Content: "checking",
		ToolCalls: []stream.ToolCallRecord{
			{ID: "call-1", Name: "read_file", ArgsJSON: `{"path":"a"}`},
		},
	}
	if _, _, err := journal.AppendAssistant(t.Context(), firstAssistant); err != nil {
		t.Fatal(err)
	}
	if seq, created, err := journal.AppendAssistant(t.Context(), firstAssistant); err != nil {
		t.Fatal(err)
	} else if created || seq != 2 {
		t.Fatalf("duplicate assistant seq=%d created=%v", seq, created)
	}
	result := stream.ToolResultRecord{
		CallID: "call-1", Name: "read_file", OK: true, Content: "content",
	}
	if _, _, err := journal.AppendToolResult(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	if seq, created, err := journal.AppendToolResult(t.Context(), result); err != nil {
		t.Fatal(err)
	} else if created || seq != 3 {
		t.Fatalf("duplicate tool seq=%d created=%v", seq, created)
	}
	if _, _, err := journal.AppendAssistant(t.Context(), stream.AssistantTurnRecord{
		Content: "done",
	}); err != nil {
		t.Fatal(err)
	}
	if err := journal.UpdateLastAssistant(t.Context(), 123, `{"sub_events":[]}`); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.List(t.Context(), "conv")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows=%d, want user + 2 assistant + 1 tool", len(rows))
	}
	for i, row := range rows {
		if row.Seq != i+1 {
			t.Fatalf("row %d seq=%d", i, row.Seq)
		}
	}
	if rows[1].EventKey == nil || *rows[1].EventKey != "assistant:run-1:1" {
		t.Fatalf("assistant event key=%v", rows[1].EventKey)
	}
	if rows[2].EventKey == nil || *rows[2].EventKey != "tool:call-1" {
		t.Fatalf("tool event key=%v", rows[2].EventKey)
	}
	if rows[3].TotalTokens != 123 {
		t.Fatalf("total tokens=%d", rows[3].TotalTokens)
	}
	modelMessages := toSchemaMessages("conv", rows, false, nil)
	if len(modelMessages) != 4 ||
		len(modelMessages[1].ToolCalls) != 1 ||
		modelMessages[2].ToolCallID != "call-1" {
		t.Fatalf("model replay=%#v", modelMessages)
	}
}

func TestMessageJournalSerializesParallelToolResults(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewMessageRepo(db)
	journal := newMessageJournal(repo, "conv", "run")
	if _, _, err := journal.AppendAssistant(t.Context(), stream.AssistantTurnRecord{
		ToolCalls: []stream.ToolCallRecord{
			{ID: "a", Name: "read_file"},
			{ID: "b", Name: "read_file"},
			{ID: "c", Name: "read_file"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(callID string) {
			defer wg.Done()
			if _, _, err := journal.AppendToolResult(t.Context(), stream.ToolResultRecord{
				CallID: callID, Name: "read_file", OK: true, Content: callID,
			}); err != nil {
				t.Errorf("append %s: %v", callID, err)
			}
		}(id)
	}
	wg.Wait()
	rows, err := repo.List(t.Context(), "conv")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows=%d", len(rows))
	}
	seenSeq := map[int]bool{}
	for _, row := range rows {
		if seenSeq[row.Seq] {
			t.Fatalf("duplicate seq %d", row.Seq)
		}
		seenSeq[row.Seq] = true
	}
}

func TestMessageJournalFailureContentSurvivesReplay(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewMessageRepo(db)
	journal := newMessageJournal(repo, "conv", "run")
	if _, _, err := journal.AppendToolResult(t.Context(), stream.ToolResultRecord{
		CallID: "failed", Name: "run_command", OK: false, Error: "exit failed",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.List(t.Context(), "conv")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Content != "exit failed" {
		t.Fatalf("rows=%#v", rows)
	}
}
