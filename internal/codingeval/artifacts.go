package codingeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

type AgentEvent struct {
	ReceivedAt time.Time `json:"received_at"`
	Type       string    `json:"type"`
	Name       string    `json:"name,omitempty"`
	Content    string    `json:"content,omitempty"`
	Message    string    `json:"message,omitempty"`
	Error      string    `json:"error,omitempty"`
}

func cloneExperimentRun(value *ExperimentRun) *ExperimentRun {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (r ExperimentRun) Validate() error {
	if err := validateArtifactSegment("experiment", r.Experiment); err != nil {
		return err
	}
	if err := validateArtifactSegment("variant", r.Variant); err != nil {
		return err
	}
	if r.Iteration < 1 {
		return fmt.Errorf("iteration must be at least 1")
	}
	return nil
}

func validateArtifactSegment(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-", r) {
			continue
		}
		return fmt.Errorf("%s %q contains an unsafe character", name, value)
	}
	return nil
}

func persistRunArtifacts(
	artifactRoot, worktree string,
	events []AgentEvent,
	result *RunResult,
) error {
	if artifactRoot == "" || result.Experiment == nil {
		return nil
	}
	if err := result.Experiment.Validate(); err != nil {
		return err
	}
	if err := validateArtifactSegment("task id", result.TaskID); err != nil {
		return err
	}

	runDir := filepath.Join(
		artifactRoot,
		result.Experiment.Experiment,
		result.Experiment.Variant,
		result.TaskID,
		fmt.Sprintf("%03d", result.Experiment.Iteration),
	)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	refs := &ArtifactRefs{
		Result: filepath.ToSlash(filepath.Join(runDir, "result.json")),
	}
	result.Artifacts = refs

	var joined error
	if len(events) > 0 {
		refs.Events = filepath.ToSlash(filepath.Join(runDir, "events.jsonl"))
		if err := writeJSONLines(filepath.Join(runDir, "events.jsonl"), events); err != nil {
			joined = errors.Join(joined, fmt.Errorf("write events: %w", err))
		}
	}
	if worktree != "" {
		patch, err := createWorkspacePatch(worktree)
		if err != nil {
			joined = errors.Join(joined, fmt.Errorf("create workspace patch: %w", err))
		} else {
			refs.WorkspacePatch = filepath.ToSlash(filepath.Join(runDir, "workspace.patch"))
			if err := os.WriteFile(filepath.Join(runDir, "workspace.patch"), []byte(patch), 0o644); err != nil {
				joined = errors.Join(joined, fmt.Errorf("write workspace patch: %w", err))
			}
		}
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		joined = errors.Join(joined, fmt.Errorf("marshal result: %w", err))
	} else if err := os.WriteFile(filepath.Join(runDir, "result.json"), append(data, '\n'), 0o644); err != nil {
		joined = errors.Join(joined, fmt.Errorf("write result: %w", err))
	}
	return joined
}

func createWorkspacePatch(worktree string) (string, error) {
	indexPath := filepath.Join(os.TempDir(), fmt.Sprintf("coding-eval-index-%d", time.Now().UnixNano()))
	defer os.Remove(indexPath)
	runGit := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+indexPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
		return output, nil
	}
	if _, err := runGit("read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := runGit("add", "-A", "--"); err != nil {
		return "", err
	}
	output, err := runGit("diff", "--cached", "--binary", "HEAD", "--")
	return string(output), err
}

func writeJSONLines(path string, events []AgentEvent) error {
	var content strings.Builder
	encoder := json.NewEncoder(&content)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(content.String()), 0o644)
}

func errorFromString(message string) error {
	if message == "" {
		return nil
	}
	return errors.New(message)
}
