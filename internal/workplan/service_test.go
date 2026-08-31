package workplan

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/repository"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	return NewService(repository.NewWorkPlanRepo(db))
}

func TestDraftEditActivateAndConflict(t *testing.T) {
	svc := newTestService(t)
	ctx := t.Context()
	draft, err := svc.CreateDraft(ctx, "conv", 4, "overview", "body", []Item{
		{ID: "inspect", Content: "Inspect code", Status: ItemPending},
		{ID: "change", Content: "Make change", Status: ItemPending},
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Status != StatusAwaiting || draft.Revision != 1 {
		t.Fatalf("draft = %#v", draft)
	}

	edited, err := svc.EditDraft(ctx, "conv", draft.ID, draft.Revision, "edited", "new body", []Item{
		{ID: "change", Content: "Make the change", Status: ItemPending},
		{ID: "verify", Content: "Run tests", Status: ItemPending},
	})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision != 2 || edited.Items[0].ID != "change" {
		t.Fatalf("edited = %#v", edited)
	}
	if _, err := svc.EditDraft(
		ctx, "conv", draft.ID, draft.Revision, "stale", "stale", edited.Items,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit error = %v, want conflict", err)
	}

	active, err := svc.Activate(
		ctx, "conv", edited.ID, edited.Revision, edited.Overview, edited.BodyMD, edited.Items,
	)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != StatusActive || active.Revision != 3 {
		t.Fatalf("active = %#v", active)
	}
	if _, err := svc.EditDraft(
		ctx, "conv", active.ID, active.Revision, "x", "x", active.Items,
	); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("active edit error = %v, want not editable", err)
	}
}

func TestUpdateTodosMergeAndComplete(t *testing.T) {
	svc := newTestService(t)
	ctx := t.Context()
	board, err := svc.UpdateTodos(ctx, "conv", 1, "", false, []Item{
		{ID: "one", Content: "First", Status: ItemInProgress},
		{ID: "two", Content: "Second", Status: ItemPending},
	})
	if err != nil {
		t.Fatal(err)
	}
	if board.Origin != OriginAgent || board.Status != StatusActive {
		t.Fatalf("board = %#v", board)
	}

	board, err = svc.UpdateTodos(ctx, "conv", 1, board.ID, true, []Item{
		{ID: "one", Status: ItemCompleted},
		{ID: "two", Status: ItemInProgress},
	})
	if err != nil {
		t.Fatal(err)
	}
	if board.Items[0].Content != "First" || board.Items[1].Status != ItemInProgress {
		t.Fatalf("merged board = %#v", board)
	}

	board, err = svc.UpdateTodos(ctx, "conv", 1, board.ID, true, []Item{
		{ID: "two", Status: ItemCompleted},
	})
	if err != nil {
		t.Fatal(err)
	}
	if board.Status != StatusCompleted {
		t.Fatalf("completed board status = %q", board.Status)
	}
}

func TestRejectsMultipleInProgressItems(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.UpdateTodos(t.Context(), "conv", 1, "", false, []Item{
		{ID: "one", Content: "First", Status: ItemInProgress},
		{ID: "two", Content: "Second", Status: ItemInProgress},
	})
	if !errors.Is(err, ErrTooManyInProgress) {
		t.Fatalf("error = %v, want ErrTooManyInProgress", err)
	}
}
