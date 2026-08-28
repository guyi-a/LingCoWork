package scope

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAllowsPathsInsideCanonicalWorkspace(t *testing.T) {
	root := t.TempDir()
	canonical, err := canonicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(canonical, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(root, "src/new.go")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonical, "src", "new.go")
	if got != want {
		t.Fatalf("Resolve=%q, want %q", got, want)
	}
}

func TestResolveRejectsLexicalEscapesAndPrefixCollisions(t *testing.T) {
	root := t.TempDir()
	canonical, err := canonicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, "../outside"); err == nil {
		t.Fatal(".. traversal was accepted")
	}

	sibling := canonical + "-backup"
	if err := os.Mkdir(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })
	if _, err := Resolve(root, filepath.Join(sibling, "file")); err == nil {
		t.Fatal("directory prefix collision was accepted")
	}
}

func TestResolveRejectsExistingSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "secret-link")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatal(err)
	}

	if _, err := Resolve(root, "secret-link"); err == nil {
		t.Fatal("existing symlink to an external file was accepted")
	}
}

func TestResolveRejectsNewFileThroughExternalSymlinkParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "external-dir")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if _, err := Resolve(root, filepath.Join("external-dir", "nested", "new.txt")); err == nil {
		t.Fatal("new file through an external symlink parent was accepted")
	}
}

func TestResolveAllowsNewFileThroughInternalSymlinkParent(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(root, "internal-dir")); err != nil {
		t.Fatal(err)
	}

	if _, err := Resolve(root, filepath.Join("internal-dir", "new.txt")); err != nil {
		t.Fatalf("internal symlink was rejected: %v", err)
	}
}
