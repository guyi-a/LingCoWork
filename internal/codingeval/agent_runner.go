package codingeval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type AgentRunOptions struct {
	BaseURL    string
	LedgerPath string
	Keep       bool
	Timeout    time.Duration
}

var newConversationID = func() string { return "eval-" + uuid.NewString() }

// RunAgent executes a task through a running LingCoWork Agent Window. It
// creates an isolated git worktree and never points the Agent at the caller's
// current checkout. The caller starts the application; this harness only uses
// its public HTTP/SSE API.
func RunAgent(ctx context.Context, task Task, opts AgentRunOptions) (result RunResult, err error) {
	start := time.Now()
	result = RunResult{
		TaskID: task.ID, Title: task.Title, Driver: "lingcowork-agent",
		ApprovalMode: "auto", StartedAt: start, Status: "error",
	}
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
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if _, parseErr := url.ParseRequestURI(baseURL); baseURL == "" || parseErr != nil {
		err := fmt.Errorf("valid LingCoWork base URL is required")
		result.Error = err.Error()
		return result, err
	}

	root, err := os.MkdirTemp("", "coding-agent-eval-"+task.ID+"-")
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

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = time.Duration(task.TimeoutSeconds) * time.Second
		if timeout < 5*time.Minute {
			timeout = 5 * time.Minute
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{}
	projectID, err := createEvalProject(runCtx, client, baseURL, worktree, task.ID)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	defer deleteEvalProject(client, baseURL, projectID)

	conversationID := newConversationID()
	defer deleteEvalConversation(client, baseURL, conversationID)
	actionStart := time.Now()
	metrics, convergenceStopped, agentErr := runAgentConversation(
		runCtx, client, baseURL, projectID, conversationID, task.EffectivePrompt(),
	)
	result.Metrics = metrics
	result.ConvergenceStopped = convergenceStopped
	result.Action = CommandResult{
		Name: "agent-window", Command: task.EffectivePrompt(),
		DurationMS: time.Since(actionStart).Milliseconds(),
	}
	if agentErr != nil {
		result.Action.ExitCode = -1
		result.Action.Stderr = agentErr.Error()
		result.Action.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		result.Status, result.Error = "failed", agentErr.Error()
		result.Score, _ = ScoreWorktree(worktree, task.Scoring)
		result.BypassSuspected = hasForbiddenViolation(result.Score)
		return result, nil
	}
	result.Action.ExitCode = 0

	result.Status = "passed"
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
	if !result.Score.Passed {
		result.Status = "failed"
	}
	result.BypassSuspected = hasForbiddenViolation(result.Score)
	return result, nil
}

func createEvalProject(
	ctx context.Context,
	client *http.Client,
	baseURL, workspace, taskID string,
) (string, error) {
	var response struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := postJSON(ctx, client, baseURL+"/projects", map[string]any{
		"path": workspace, "name": "eval-" + taskID,
	}, &response); err != nil {
		return "", err
	}
	if response.Project.ID == "" {
		return "", fmt.Errorf("project API returned no id")
	}
	return response.Project.ID, nil
}

func runAgentConversation(
	ctx context.Context,
	client *http.Client,
	baseURL, projectID, conversationID, prompt string,
) (AgentMetrics, bool, error) {
	var metrics AgentMetrics
	convergenceStopped := false
	endpoint := fmt.Sprintf(
		"%s/chat/%s?project_id=%s",
		baseURL, url.PathEscape(conversationID), url.QueryEscape(projectID),
	)
	payload, _ := json.Marshal(map[string]any{
		"message": prompt, "mode": "agent", "approval_mode": "auto",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return metrics, convergenceStopped, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	res, err := client.Do(req)
	if err != nil {
		return metrics, convergenceStopped, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var body bytes.Buffer
		_, _ = body.ReadFrom(res.Body)
		return metrics, convergenceStopped, fmt.Errorf("chat API status %d: %s", res.StatusCode, strings.TrimSpace(body.String()))
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var frame struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &frame) != nil {
			continue
		}
		switch frame.Type {
		case "tool_call":
			metrics.ToolCalls++
			if frame.Name == "run_command" {
				metrics.ValidationCalls++
			}
			if frame.Name == "validation_gate" {
				metrics.CompletionGateRuns++
			}
		case "tool_result":
			if frame.Name == "validation_gate" && strings.Contains(frame.Content, `"release_after":true`) {
				convergenceStopped = true
			}
		case "done":
			return metrics, convergenceStopped, nil
		case "approval_required", "question_required", "plan_required":
			if frame.Type == "approval_required" {
				metrics.ApprovalInterrupts++
			}
			return metrics, convergenceStopped, fmt.Errorf("agent eval stopped for interactive input: %s", frame.Type)
		case "error":
			if frame.Message == "" {
				frame.Message = frame.Error
			}
			return metrics, convergenceStopped, fmt.Errorf("agent stream: %s", frame.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		return metrics, convergenceStopped, err
	}
	return metrics, convergenceStopped, fmt.Errorf("agent stream ended without done frame")
}

func hasForbiddenViolation(score Score) bool {
	for _, violation := range score.Violations {
		if strings.HasPrefix(violation, "forbidden path changed:") {
			return true
		}
	}
	return false
}

func postJSON(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	body any,
	out any,
) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var response bytes.Buffer
		_, _ = response.ReadFrom(res.Body)
		return fmt.Errorf("%s: status %d: %s", endpoint, res.StatusCode, strings.TrimSpace(response.String()))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func deleteEvalProject(client *http.Client, baseURL, projectID string) {
	deleteEvalResource(client, baseURL+"/projects/"+url.PathEscape(projectID))
}

func deleteEvalConversation(client *http.Client, baseURL, conversationID string) {
	deleteEvalResource(client, baseURL+"/conversations/"+url.PathEscape(conversationID))
}

func deleteEvalResource(client *http.Client, endpoint string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx, http.MethodDelete, endpoint, nil,
	)
	if err == nil {
		res, doErr := client.Do(req)
		if doErr == nil {
			_ = res.Body.Close()
		}
	}
}
