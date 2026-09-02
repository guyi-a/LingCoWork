package codingeval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPersistsArtifactsBeforeWorkspaceCleanup(t *testing.T) {
	artifactDir := filepath.Join(t.TempDir(), "artifacts")
	result, err := Run(context.Background(), validTestTask(), RunOptions{
		ArtifactDir: artifactDir,
		Experiment: &ExperimentRun{
			Experiment: "experiment-1",
			Variant:    "baseline",
			Iteration:  2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifacts == nil || result.Artifacts.Result == "" || result.Artifacts.WorkspacePatch == "" {
		t.Fatalf("artifacts=%#v", result.Artifacts)
	}
	resultData, err := os.ReadFile(filepath.FromSlash(result.Artifacts.Result))
	if err != nil {
		t.Fatal(err)
	}
	var persisted RunResult
	if err := json.Unmarshal(resultData, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Experiment == nil || persisted.Experiment.Iteration != 2 || persisted.Status != "passed" {
		t.Fatalf("persisted=%#v", persisted)
	}
	patch, err := os.ReadFile(filepath.FromSlash(result.Artifacts.WorkspacePatch))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "+new") {
		t.Fatalf("patch did not contain final change:\n%s", patch)
	}
}

func TestPersistRunArtifactsRejectsUnsafeIdentity(t *testing.T) {
	result := RunResult{
		TaskID: "task",
		Experiment: &ExperimentRun{
			Experiment: "../escape",
			Variant:    "candidate",
			Iteration:  1,
		},
	}
	err := persistRunArtifacts(t.TempDir(), "", nil, &result)
	if err == nil || !strings.Contains(err.Error(), "unsafe character") {
		t.Fatalf("err=%v", err)
	}
}

func TestCreateWorkspacePatchIncludesUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@localhost")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "baseline")
	if err := os.WriteFile(filepath.Join(root, "created.txt"), []byte("created\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, err := createWorkspacePatch(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, "new file mode") || !strings.Contains(patch, "+created") {
		t.Fatalf("patch did not contain untracked file:\n%s", patch)
	}
}
