package codingeval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunFixtureAndLedger(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "ledger.jsonl")
	result, err := Run(context.Background(), validTestTask(), RunOptions{LedgerPath: ledger})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" || !result.Score.Passed {
		t.Fatalf("result = %#v", result)
	}
	if result.Score.Diff.ChangedFiles != 1 || result.Score.Diff.AddedLines != 1 || result.Score.Diff.DeletedLines != 1 {
		t.Fatalf("diff = %#v", result.Score.Diff)
	}
	summary, err := SummarizeLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 1 || summary.Passed != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestRunTimeout(t *testing.T) {
	task := validTestTask()
	task.TimeoutSeconds = 1
	task.Fixture.Command = "sleep 5"
	start := time.Now()
	result, err := Run(context.Background(), task, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !result.Action.TimedOut {
		t.Fatalf("result = %#v", result)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not stop command promptly")
	}
}

func TestScoreForbiddenPathAndFileLimit(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@localhost")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "safe.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private", "secret.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "baseline")
	if err := os.WriteFile(filepath.Join(root, "safe.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private", "secret.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	score, err := ScoreWorktree(root, ScoringPolicy{
		ForbiddenPaths: []string{"private/**"}, MaxChangedFiles: 1, MaxAddedLines: 10, MaxDeletedLines: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if score.Passed || len(score.Violations) != 2 {
		t.Fatalf("score = %#v", score)
	}
}

func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	if _, err := gitOutput(cwd, args...); err != nil {
		t.Fatal(err)
	}
}
