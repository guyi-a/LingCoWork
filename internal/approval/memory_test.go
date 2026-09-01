package approval

import (
	"path/filepath"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/effect"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

func TestFingerprintIsExactAndSafetyWallCannotBeRemembered(t *testing.T) {
	first, ok := Fingerprint(effect.Effect{
		Kind: effect.KindFileWrite, Scope: effect.ScopeExternal, Path: "/tmp/a.txt",
	}, `{"content":"hello"}`)
	if !ok {
		t.Fatal("ordinary external write should be rememberable")
	}
	second, ok := Fingerprint(effect.Effect{
		Kind: effect.KindFileWrite, Scope: effect.ScopeExternal, Path: "/tmp/b.txt",
	}, `{"content":"hello"}`)
	if !ok || first == second {
		t.Fatal("different files must have different fingerprints")
	}
	if _, ok := Fingerprint(effect.Effect{
		Kind: effect.KindFileWrite, Path: "/tmp/a", Destructive: true,
	}, ""); ok {
		t.Fatal("destructive effects must not be rememberable")
	}
	if _, ok := Fingerprint(effect.Effect{
		Kind: effect.KindMemoryWrite,
	}, ""); ok {
		t.Fatal("memory writes must not be rememberable")
	}
	if _, ok := Fingerprint(effect.Effect{
		Kind: effect.KindFileWrite, Path: "/ws/credentials.json",
	}, `{"content":"{}"}`); ok {
		t.Fatal("protected writes must not be rememberable")
	}
}

func TestMemoryIsConversationScopedAndSnapshotIsStable(t *testing.T) {
	m := NewMemory()
	m.Remember("a", "one")
	snapshot := m.Allowed("a")
	m.Remember("a", "two")
	m.Remember("b", "one")
	if len(snapshot) != 1 {
		t.Fatalf("snapshot changed after remember: %v", snapshot)
	}
	if m.Count("a") != 2 || m.Count("b") != 1 {
		t.Fatalf("unexpected counts a=%d b=%d", m.Count("a"), m.Count("b"))
	}
	m.Clear("a")
	if m.Count("a") != 0 || m.Count("b") != 1 {
		t.Fatal("clear leaked across conversations")
	}
}

func TestModeStorePersistsAcrossInstances(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "mode.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewConversationRepo(db)
	if err := repo.Upsert(t.Context(), "conv"); err != nil {
		t.Fatal(err)
	}
	first := NewModeStore(repo)
	if got := first.Get("conv"); got != ModeManual {
		t.Fatalf("default=%q", got)
	}
	if err := first.Set("conv", ModeAcceptWrite); err != nil {
		t.Fatal(err)
	}
	second := NewModeStore(repo)
	if got := second.Get("conv"); got != ModeAcceptWrite {
		t.Fatalf("persisted=%q", got)
	}
}
