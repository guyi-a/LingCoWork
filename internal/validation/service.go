package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

var ErrInvalidScope = errors.New("invalid problems scope")

type Service struct {
	runs     *repository.ValidationRepo
	messages *repository.MessageRepo
	convs    *repository.ConversationRepo
	projects *repository.ProjectRepo
}

func NewService(
	runs *repository.ValidationRepo,
	messages *repository.MessageRepo,
	convs *repository.ConversationRepo,
	projects *repository.ProjectRepo,
) *Service {
	return &Service{runs: runs, messages: messages, convs: convs, projects: projects}
}

type commandArgs struct {
	Command        string `json:"command"`
	ValidationKind string `json:"validation_kind"`
}

type commandResult struct {
	ExitCode        int      `json:"exit_code"`
	DurationMs      int64    `json:"duration_ms"`
	Stdout          string   `json:"stdout"`
	Stderr          string   `json:"stderr"`
	StdoutTruncated bool     `json:"stdout_truncated,omitempty"`
	StderrTruncated bool     `json:"stderr_truncated,omitempty"`
	TimedOut        bool     `json:"timed_out,omitempty"`
	Cwd             string   `json:"cwd"`
	Validation      *Summary `json:"validation,omitempty"`
}

type Run struct {
	ToolCallID     string  `json:"tool_call_id"`
	UserMessageSeq int     `json:"user_message_seq"`
	Command        string  `json:"command"`
	Cwd            string  `json:"cwd"`
	ExitCode       int     `json:"exit_code"`
	DurationMs     int64   `json:"duration_ms"`
	TimedOut       bool    `json:"timed_out,omitempty"`
	Summary        Summary `json:"validation"`
	CreatedAt      string  `json:"created_at"`
}

type Problems struct {
	Scope          string `json:"scope"`
	UserMessageSeq int    `json:"user_message_seq,omitempty"`
	Runs           []Run  `json:"runs"`
	ErrorCount     int    `json:"error_count"`
	WarningCount   int    `json:"warning_count"`
}

// Enrich parses and persists one completed run_command result. Any parser or
// persistence failure degrades to a parse_ok=false summary; it never turns a
// command that already ran into a fatal tool error.
func (s *Service) Enrich(
	ctx context.Context,
	input *compose.ToolInput,
	resultJSON string,
) string {
	if s == nil || input == nil || input.Name != "run_command" {
		return resultJSON
	}
	var args commandArgs
	if json.Unmarshal([]byte(input.Arguments), &args) != nil {
		return resultJSON
	}
	kind, ok := ParseKind(args.ValidationKind)
	if !ok {
		return resultJSON
	}
	var result commandResult
	if json.Unmarshal([]byte(resultJSON), &result) != nil {
		return resultJSON
	}
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return resultJSON
	}
	conv, err := s.convs.Get(ctx, convID)
	if err != nil || conv == nil || conv.ProjectID == nil {
		return resultJSON
	}
	project, err := s.projects.Get(ctx, *conv.ProjectID)
	if err != nil || project == nil {
		return resultJSON
	}
	summary := Parse(
		kind, args.Command, project.Workspace, result.Cwd,
		result.Stdout, result.Stderr, result.ExitCode,
	)
	result.Validation = &summary
	enriched, err := json.Marshal(result)
	if err != nil {
		return resultJSON
	}
	userSeq, err := s.messages.LatestUserSeq(ctx, convID)
	if err == nil && input.CallID != "" {
		diagnosticsJSON, marshalErr := json.Marshal(summary.Diagnostics)
		if marshalErr == nil {
			_ = s.runs.Upsert(context.Background(), &model.ValidationRun{
				ProjectID:            project.ID,
				ConversationID:       convID,
				UserMessageSeq:       userSeq,
				ToolCallID:           input.CallID,
				Command:              args.Command,
				Cwd:                  result.Cwd,
				Kind:                 string(kind),
				ExitCode:             result.ExitCode,
				DurationMs:           result.DurationMs,
				Passed:               summary.Passed,
				Parser:               summary.Parser,
				ParseOK:              summary.ParseOK,
				DiagnosticsJSON:      string(diagnosticsJSON),
				DiagnosticsTruncated: summary.Truncated,
				StdoutTruncated:      result.StdoutTruncated,
				StderrTruncated:      result.StderrTruncated,
				TimedOut:             result.TimedOut,
				CreatedAt:            time.Now(),
			})
		}
	}
	return string(enriched)
}

func (s *Service) ListProblems(
	ctx context.Context,
	conversationID, scope string,
) (*Problems, error) {
	if scope == "" {
		scope = "current"
	}
	var (
		rows    []model.ValidationRun
		userSeq int
		err     error
	)
	switch scope {
	case "current":
		userSeq, err = s.messages.LatestUserSeq(ctx, conversationID)
		if err == nil {
			rows, err = s.runs.ListCurrent(ctx, conversationID, userSeq)
		}
	case "conversation":
		rows, err = s.runs.ListConversation(ctx, conversationID, 200)
	default:
		return nil, ErrInvalidScope
	}
	if err != nil {
		return nil, err
	}
	rows = latestEquivalentRuns(rows)
	result := &Problems{
		Scope: scope, UserMessageSeq: userSeq, Runs: make([]Run, 0, len(rows)),
	}
	for _, row := range rows {
		var diagnostics []Diagnostic
		if err := json.Unmarshal([]byte(row.DiagnosticsJSON), &diagnostics); err != nil {
			diagnostics = []Diagnostic{}
		}
		summary := Summary{
			Kind:        Kind(row.Kind),
			Passed:      row.Passed,
			Parser:      row.Parser,
			ParseOK:     row.ParseOK,
			Diagnostics: diagnostics,
			Truncated:   row.DiagnosticsTruncated,
		}
		for _, item := range diagnostics {
			switch item.Severity {
			case "warning":
				summary.WarningCount++
				result.WarningCount++
			case "info":
				// Visible, but not counted as a problem.
			default:
				summary.ErrorCount++
				result.ErrorCount++
			}
		}
		result.Runs = append(result.Runs, Run{
			ToolCallID: row.ToolCallID, UserMessageSeq: row.UserMessageSeq,
			Command: row.Command, Cwd: row.Cwd, ExitCode: row.ExitCode,
			DurationMs: row.DurationMs, TimedOut: row.TimedOut,
			Summary: summary, CreatedAt: row.CreatedAt.Format(time.RFC3339Nano),
		})
	}
	return result, nil
}

func latestEquivalentRuns(rows []model.ValidationRun) []model.ValidationRun {
	seen := make(map[string]struct{}, len(rows))
	out := make([]model.ValidationRun, 0, len(rows))
	for _, row := range rows {
		key := strings.Join([]string{row.Kind, row.Command, row.Cwd}, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func ValidateKind(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if _, ok := ParseKind(raw); !ok {
		return fmt.Errorf(
			"validation_kind must be test, build, lint, typecheck, or format",
		)
	}
	return nil
}
