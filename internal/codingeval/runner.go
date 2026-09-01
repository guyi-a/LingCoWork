package codingeval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const maxCommandOutput = 64 * 1024

type RunOptions struct {
	LedgerPath string
	Keep       bool
}

type CommandResult struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
}

type DiffStat struct {
	ChangedFiles int      `json:"changed_files"`
	AddedLines   int      `json:"added_lines"`
	DeletedLines int      `json:"deleted_lines"`
	Paths        []string `json:"paths"`
}

type Score struct {
	Passed     bool     `json:"passed"`
	Violations []string `json:"violations,omitempty"`
	Diff       DiffStat `json:"diff"`
}

type AgentMetrics struct {
	ToolCalls          int `json:"tool_calls"`
	ValidationCalls    int `json:"validation_calls"`
	CompletionGateRuns int `json:"completion_gate_runs"`
	ApprovalInterrupts int `json:"approval_interrupts"`
}

type RunResult struct {
	TaskID             string          `json:"task_id"`
	Title              string          `json:"title"`
	Driver             string          `json:"driver"`
	ApprovalMode       string          `json:"approval_mode,omitempty"`
	StartedAt          time.Time       `json:"started_at"`
	DurationMS         int64           `json:"duration_ms"`
	Status             string          `json:"status"`
	Error              string          `json:"error,omitempty"`
	Worktree           string          `json:"worktree,omitempty"`
	Action             CommandResult   `json:"action"`
	Verification       []CommandResult `json:"verification"`
	Score              Score           `json:"score"`
	Metrics            AgentMetrics    `json:"metrics,omitempty"`
	ConvergenceStopped bool            `json:"convergence_stopped,omitempty"`
	BypassSuspected    bool            `json:"bypass_suspected,omitempty"`
}

func FindTask(c Catalog, id string) (Task, bool) {
	for _, task := range c.Tasks {
		if task.ID == id {
			return task, true
		}
	}
	return Task{}, false
}

func Run(ctx context.Context, task Task, opts RunOptions) (result RunResult, err error) {
	start := time.Now()
	result = RunResult{TaskID: task.ID, Title: task.Title, Driver: "reference-command", StartedAt: start, Status: "error"}
	defer func() {
		result.DurationMS = time.Since(start).Milliseconds()
		if opts.LedgerPath != "" {
			if ledgerErr := AppendLedger(opts.LedgerPath, result); ledgerErr != nil {
				err = errors.Join(err, fmt.Errorf("append ledger: %w", ledgerErr))
			}
		}
	}()

	if err := task.Validate(); err != nil {
		result.Error = err.Error()
		return result, err
	}
	if !task.Enabled {
		err := fmt.Errorf("task %s is disabled: %s", task.ID, task.DisabledReason)
		result.Status, result.Error = "skipped", err.Error()
		return result, err
	}

	root, err := os.MkdirTemp("", "coding-eval-"+task.ID+"-")
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	source := filepath.Join(root, "source")
	worktree := filepath.Join(root, "worktree")
	if !opts.Keep {
		defer os.RemoveAll(root)
	} else {
		result.Worktree = worktree
	}

	if err = createFixture(ctx, source, worktree, task.Fixture.Files); err != nil {
		result.Error = err.Error()
		return result, err
	}
	if !opts.Keep {
		defer exec.Command("git", "-C", source, "worktree", "remove", "--force", worktree).Run()
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(task.TimeoutSeconds)*time.Second)
	defer cancel()
	result.Action = runShell(runCtx, "action", task.Fixture.Command, worktree)
	if result.Action.ExitCode != 0 {
		result.Status = "failed"
		result.Error = "fixture action failed"
		result.Score, _ = ScoreWorktree(worktree, task.Scoring)
		return result, nil
	}

	for _, verify := range task.Verify {
		commandResult := runShell(runCtx, verify.Name, verify.Command, worktree)
		result.Verification = append(result.Verification, commandResult)
		if commandResult.ExitCode != 0 {
			result.Status = "failed"
		}
	}
	result.Score, err = ScoreWorktree(worktree, task.Scoring)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	if result.Status != "failed" && result.Score.Passed {
		result.Status = "passed"
	} else {
		result.Status = "failed"
	}
	return result, nil
}

func createFixture(ctx context.Context, source, worktree string, files map[string]string) error {
	if err := os.MkdirAll(source, 0o755); err != nil {
		return err
	}
	for name, content := range files {
		target := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return err
		}
	}
	commands := [][]string{
		{"init", "-q"},
		{"config", "user.email", "coding-eval@localhost"},
		{"config", "user.name", "Coding Eval"},
		{"add", "."},
		{"commit", "-qm", "fixture baseline"},
		{"worktree", "add", "--detach", worktree, "HEAD"},
	}
	for _, args := range commands {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", source}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func runShell(ctx context.Context, name, command, cwd string) CommandResult {
	result := CommandResult{Name: name, Command: command, ExitCode: -1}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		result.Stderr = err.Error()
		return result
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		result.ExitCode = exitCode(err)
	case <-ctx.Done():
		result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	result.DurationMS = time.Since(start).Milliseconds()
	result.Stdout, result.Stderr = stdout.String(), stderr.String()
	return result
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	if remaining := maxCommandOutput - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return original, nil
}

func ScoreWorktree(worktree string, policy ScoringPolicy) (Score, error) {
	paths, err := changedPaths(worktree)
	if err != nil {
		return Score{}, err
	}
	diff := DiffStat{ChangedFiles: len(paths), Paths: paths}
	tracked, err := gitOutput(worktree, "diff", "--numstat", "HEAD", "--")
	if err != nil {
		return Score{}, err
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(tracked), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		added, _ := strconv.Atoi(fields[0])
		deleted, _ := strconv.Atoi(fields[1])
		diff.AddedLines += added
		diff.DeletedLines += deleted
		seen[filepath.ToSlash(fields[2])] = true
	}
	for _, name := range paths {
		if seen[name] {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(name)))
		if readErr == nil {
			diff.AddedLines += lineCount(data)
		}
	}

	var violations []string
	for _, name := range paths {
		for _, pattern := range policy.ForbiddenPaths {
			matched, matchErr := doublestar.Match(pattern, name)
			if matchErr != nil {
				return Score{}, fmt.Errorf("forbidden pattern %q: %w", pattern, matchErr)
			}
			if matched {
				violations = append(violations, fmt.Sprintf("forbidden path changed: %s", name))
			}
		}
	}
	if diff.ChangedFiles > policy.MaxChangedFiles {
		violations = append(violations, fmt.Sprintf("changed files %d exceeds %d", diff.ChangedFiles, policy.MaxChangedFiles))
	}
	if diff.AddedLines > policy.MaxAddedLines {
		violations = append(violations, fmt.Sprintf("added lines %d exceeds %d", diff.AddedLines, policy.MaxAddedLines))
	}
	if diff.DeletedLines > policy.MaxDeletedLines {
		violations = append(violations, fmt.Sprintf("deleted lines %d exceeds %d", diff.DeletedLines, policy.MaxDeletedLines))
	}
	return Score{Passed: len(violations) == 0, Violations: violations, Diff: diff}, nil
}

func changedPaths(worktree string) ([]string, error) {
	output, err := gitOutput(worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	records := strings.Split(output, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			continue
		}
		name := filepath.ToSlash(record[3:])
		if record[0] == 'R' || record[1] == 'R' {
			i++
			if i < len(records) && records[i] != "" {
				name = filepath.ToSlash(records[i])
			}
		}
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths, nil
}

func gitOutput(worktree string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func lineCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	count := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		count++
	}
	return count
}

func MarshalResult(result RunResult) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}
