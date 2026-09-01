package validation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
)

const GateToolName = "validation_gate"

const (
	gateAttemptsKey    = "__lingcowork_validation_gate_attempts__"
	gateFingerprintKey = "__lingcowork_validation_gate_fingerprint__"
	gateRepeatKey      = "__lingcowork_validation_gate_repeats__"
	gateReleasedKey    = "__lingcowork_validation_gate_released__"
)

type Digest struct {
	Kind         Kind         `json:"kind"`
	Command      string       `json:"command"`
	Cwd          string       `json:"cwd,omitempty"`
	Passed       bool         `json:"passed"`
	ErrorCount   int          `json:"error_count"`
	WarningCount int          `json:"warning_count"`
	Diagnostics  []Diagnostic `json:"diagnostics,omitempty"`
	Truncated    bool         `json:"truncated,omitempty"`
	Fingerprint  string       `json:"fingerprint"`
}

func NewDigest(command, cwd string, summary Summary) Digest {
	diagnostics := append([]Diagnostic(nil), summary.Diagnostics...)
	sort.SliceStable(diagnostics, func(i, j int) bool {
		return diagnosticKey(diagnostics[i]) < diagnosticKey(diagnostics[j])
	})
	allDiagnostics := append([]Diagnostic(nil), diagnostics...)
	truncated := summary.Truncated || len(diagnostics) > 20
	if len(diagnostics) > 20 {
		diagnostics = diagnostics[:20]
	}
	fingerprintInput := struct {
		Kind        Kind
		Command     string
		Cwd         string
		Passed      bool
		Diagnostics []Diagnostic
	}{
		Kind: summary.Kind, Command: command, Cwd: cwd,
		Passed: summary.Passed, Diagnostics: allDiagnostics,
	}
	raw, _ := json.Marshal(fingerprintInput)
	sum := sha256.Sum256(raw)
	return Digest{
		Kind: summary.Kind, Command: command, Cwd: cwd,
		Passed: summary.Passed, ErrorCount: summary.ErrorCount,
		WarningCount: summary.WarningCount, Diagnostics: diagnostics,
		Truncated: truncated, Fingerprint: hex.EncodeToString(sum[:]),
	}
}

func diagnosticKey(d Diagnostic) string {
	return strings.Join([]string{
		d.Path, fmt.Sprintf("%09d", d.Line), fmt.Sprintf("%09d", d.Column),
		d.Severity, d.Code, d.ID, d.Message,
	}, "\x00")
}

type GateState string

const (
	GatePassed  GateState = "passed"
	GateMissing GateState = "missing"
	GateFailed  GateState = "failed"
)

type CompletionStatus struct {
	State          GateState `json:"state"`
	UserMessageSeq int       `json:"user_message_seq,omitempty"`
	LastChangeAt   string    `json:"last_change_at,omitempty"`
	Digests        []Digest  `json:"digests,omitempty"`
	Fingerprint    string    `json:"fingerprint,omitempty"`
	Instruction    string    `json:"instruction,omitempty"`
	ReleaseAfter   bool      `json:"release_after,omitempty"`
}

func (s *Service) EvaluateCompletion(ctx context.Context) CompletionStatus {
	status := CompletionStatus{State: GatePassed}
	if s == nil || s.changes == nil || s.runs == nil || s.messages == nil {
		return status
	}
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return status
	}
	userSeq, err := s.messages.LatestUserSeq(ctx, convID)
	if err != nil || userSeq <= 0 {
		return status
	}
	status.UserMessageSeq = userSeq
	events, err := s.changes.ListEvents(ctx, convID, userSeq)
	if err != nil {
		return status
	}
	var latestChange time.Time
	for _, event := range events {
		if !event.Succeeded || (event.Attribution != "" && event.Attribution != "agent") {
			continue
		}
		if !isCodePath(event.Path) && !isCodePath(event.OldPath) {
			continue
		}
		if event.CreatedAt.After(latestChange) {
			latestChange = event.CreatedAt
		}
	}
	if latestChange.IsZero() {
		return status
	}
	status.LastChangeAt = latestChange.Format(time.RFC3339Nano)

	runs, err := s.runs.ListCurrent(ctx, convID, userSeq)
	if err != nil {
		status.State = GateMissing
		status.Instruction = "代码已修改，但无法读取验证记录。请运行合适的 test、lint、build 或 typecheck。"
		status.Fingerprint = "missing"
		return status
	}
	runs = latestEquivalentRuns(runs)
	for _, run := range runs {
		if run.CreatedAt.Before(latestChange) {
			continue
		}
		var diagnostics []Diagnostic
		_ = json.Unmarshal([]byte(run.DiagnosticsJSON), &diagnostics)
		summary := Summary{
			Kind: Kind(run.Kind), Passed: run.Passed, Parser: run.Parser,
			ParseOK: run.ParseOK, Diagnostics: diagnostics,
			Truncated: run.DiagnosticsTruncated,
		}
		for _, d := range diagnostics {
			if d.Severity == "warning" {
				summary.WarningCount++
			} else if d.Severity != "info" {
				summary.ErrorCount++
			}
		}
		status.Digests = append(status.Digests, NewDigest(run.Command, run.Cwd, summary))
	}
	if len(status.Digests) == 0 {
		status.State = GateMissing
		status.Instruction = "代码已修改，但最后一次修改后还没有验证。请运行最相关的 test、lint、build 或 typecheck，并设置 validation_kind。"
		status.Fingerprint = "missing"
		return status
	}
	status.State = GatePassed
	var fingerprints []string
	for _, digest := range status.Digests {
		fingerprints = append(fingerprints, digest.Fingerprint)
		if !digest.Passed {
			status.State = GateFailed
		}
	}
	sort.Strings(fingerprints)
	raw := strings.Join(fingerprints, "\x00")
	sum := sha256.Sum256([]byte(raw))
	status.Fingerprint = hex.EncodeToString(sum[:])
	if status.State == GateFailed {
		status.Instruction = "验证仍未通过。请根据 validation_digest 修复后重新验证，不要直接结束任务。"
	}
	return status
}

func isCodePath(path string) bool {
	if path == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "dockerfile", "makefile", "go.mod", "go.sum", "package.json",
		"tsconfig.json", "cargo.toml", "pyproject.toml", "requirements.txt":
		return true
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".go", ".rs", ".py", ".js", ".jsx", ".ts", ".tsx", ".vue", ".svelte",
		".java", ".kt", ".kts", ".c", ".h", ".cc", ".cpp", ".cs", ".rb", ".php",
		".sh", ".bash", ".zsh", ".sql", ".proto":
		return true
	default:
		return false
	}
}

type gateToolInput struct {
	ReleaseAfter bool `json:"release_after,omitempty"`
}

func (s *Service) CompletionTool() (tool.BaseTool, error) {
	return utils.InferTool(
		GateToolName,
		"Internal completion gate. The runtime calls this automatically when code changes have not been validated; do not call it proactively.",
		func(ctx context.Context, in *gateToolInput) (*CompletionStatus, error) {
			status := s.EvaluateCompletion(ctx)
			status.ReleaseAfter = in != nil && in.ReleaseAfter
			if status.ReleaseAfter {
				status.Instruction = "验证未收敛或已达到自动续行上限。不要继续重试相同操作；在最终回答中明确说明未通过的验证和剩余 diagnostics。"
			}
			return &status, nil
		},
	)
}

type CompletionGateMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	service *Service
}

func (s *Service) CompletionMiddleware() adk.ChatModelAgentMiddleware {
	return &CompletionGateMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		service:                      s,
	}
}

func (m *CompletionGateMiddleware) WrapModel(
	ctx context.Context,
	inner model.BaseChatModel,
	_ *adk.ModelContext,
) (model.BaseChatModel, error) {
	if m == nil || m.service == nil {
		return inner, nil
	}
	return &completionGateModel{inner: inner, middleware: m}, nil
}

type completionGateModel struct {
	inner      model.BaseChatModel
	middleware *CompletionGateMiddleware
}

func (m *completionGateModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	message, err := m.inner.Generate(ctx, input, opts...)
	if err != nil || m.middleware.service.EvaluateCompletion(ctx).State == GatePassed {
		return message, err
	}
	return m.middleware.rewriteFinal(ctx, message), nil
}

func (m *completionGateModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if m.middleware.service.EvaluateCompletion(ctx).State == GatePassed {
		return m.inner.Stream(ctx, input, opts...)
	}
	reader, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var chunks []*schema.Message
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if chunk != nil {
			chunks = append(chunks, chunk)
		}
	}
	full, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{
		m.middleware.rewriteFinal(ctx, full),
	}), nil
}

func (m *CompletionGateMiddleware) rewriteFinal(
	ctx context.Context,
	last *schema.Message,
) *schema.Message {
	if last == nil || last.Role != schema.Assistant || len(last.ToolCalls) > 0 {
		return last
	}
	if released, _ := runLocalBool(ctx, gateReleasedKey); released {
		return last
	}
	status := m.service.EvaluateCompletion(ctx)
	if status.State == GatePassed {
		return last
	}

	attempts, _ := runLocalInt(ctx, gateAttemptsKey)
	previous, _, _ := adk.GetRunLocalValue(ctx, gateFingerprintKey)
	previousFingerprint, _ := previous.(string)
	repeats, _ := runLocalInt(ctx, gateRepeatKey)
	if previousFingerprint == status.Fingerprint {
		repeats++
	} else {
		repeats = 1
	}
	attempts++
	releaseAfter := attempts >= 3 || (status.State == GateFailed && repeats >= 2)
	_ = adk.SetRunLocalValue(ctx, gateAttemptsKey, attempts)
	_ = adk.SetRunLocalValue(ctx, gateFingerprintKey, status.Fingerprint)
	_ = adk.SetRunLocalValue(ctx, gateRepeatKey, repeats)
	if releaseAfter {
		_ = adk.SetRunLocalValue(ctx, gateReleasedKey, true)
	}

	args, _ := json.Marshal(gateToolInput{ReleaseAfter: releaseAfter})
	replacement := *last
	replacement.Content = ""
	replacement.ReasoningContent = ""
	replacement.ToolCalls = []schema.ToolCall{{
		ID:   fmt.Sprintf("validation-gate-%d", attempts),
		Type: "function",
		Function: schema.FunctionCall{
			Name: GateToolName, Arguments: string(args),
		},
	}}
	return &replacement
}

func runLocalInt(ctx context.Context, key string) (int, error) {
	value, found, err := adk.GetRunLocalValue(ctx, key)
	if err != nil || !found {
		return 0, err
	}
	number, _ := value.(int)
	return number, nil
}

func runLocalBool(ctx context.Context, key string) (bool, error) {
	value, found, err := adk.GetRunLocalValue(ctx, key)
	if err != nil || !found {
		return false, err
	}
	flag, _ := value.(bool)
	return flag, nil
}
