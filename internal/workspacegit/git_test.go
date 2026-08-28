package workspacegit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusNumStatAndDiff(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "modified.txt", "before\n")
	writeFile(t, root, "deleted.txt", "delete me\n")
	writeFile(t, root, "old-name.txt", "rename me\n")
	git(t, root, "add", ".")
	git(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-m", "initial")

	writeFile(t, root, "modified.txt", "after\nextra\n")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	git(t, root, "mv", "old-name.txt", "new-name.txt")
	writeFile(t, root, "untracked.txt", "new\n")

	status, err := Status(context.Background(), root)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	statuses := make(map[string]string)
	for _, entry := range status {
		statuses[entry.Path] = string([]byte{entry.IndexStatus, entry.WorkStatus})
		if entry.Path == "new-name.txt" && entry.OldPath != "old-name.txt" {
			t.Fatalf("rename old path = %q", entry.OldPath)
		}
	}
	for _, path := range []string{
		"modified.txt", "deleted.txt", "new-name.txt", "untracked.txt",
	} {
		if _, ok := statuses[path]; !ok {
			t.Errorf("status missing %s: %#v", path, status)
		}
	}

	stats, err := NumStat(context.Background(), root)
	if err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	if len(stats) < 3 {
		t.Fatalf("numstat too short: %#v", stats)
	}

	patch, err := Diff(context.Background(), root, "modified.txt")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	text := string(patch)
	if !strings.Contains(text, "-before") ||
		!strings.Contains(text, "+after") ||
		!strings.Contains(text, "+extra") {
		t.Fatalf("unexpected patch:\n%s", text)
	}
}

func TestNoIndexDiffUsesWorkspaceLabels(t *testing.T) {
	patch, err := NoIndexDiff(
		context.Background(),
		"src/file.go",
		[]byte("old\n"),
		[]byte("new\n"),
		true,
		true,
	)
	if err != nil {
		t.Fatalf("NoIndexDiff: %v", err)
	}
	text := string(patch)
	if !strings.HasPrefix(text, "--- a/src/file.go\n+++ b/src/file.go\n@@") {
		t.Fatalf("unexpected labels:\n%s", text)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init")
	return root
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
