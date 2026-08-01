package compaction

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/config"
	"github.com/guyi-a/Interview-Agent/internal/llmhttp"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

// Summarizer turns a slice of history rows into the five-section summary.
type Summarizer struct {
	client *llmhttp.Client
	cfg    config.CompactionConfig
}

func NewSummarizer(cfg config.CompactionConfig) *Summarizer {
	if !cfg.Enabled() {
		return nil
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &Summarizer{
		client: llmhttp.New(cfg.APIKey, cfg.BaseURL, timeout),
		cfg:    cfg,
	}
}

// Summarize runs one non-streaming completion over the folded history.
//
// The request is deliberately plain chat/completions with no tools attached:
// the model has no way to act even if the prompt fails to dissuade it.
func (s *Summarizer) Summarize(ctx context.Context, folded []model.Message, priorSummary string) (string, error) {
	msgs := s.buildMessages(folded, priorSummary)
	if len(msgs) == 0 {
		return "", fmt.Errorf("compaction: nothing to summarize")
	}
	return s.client.Chat(ctx, s.cfg.Model, msgs, s.cfg.MaxTokens, /*jsonMode=*/ false)
}

// buildMessages flattens the history into the summarizer request:
// [<prior-summary>?] + history + <compact-control>.
//
// Everything collapses to plain user/assistant text. Tool activity is
// rendered as readable pseudo-messages rather than real tool_calls/tool
// roles, because a summarization request replays a partial history whose
// pairing the API would reject — and the summarizer only needs to read what
// happened, not to be able to continue it.
func (s *Summarizer) buildMessages(folded []model.Message, priorSummary string) []llmhttp.Message {
	out := make([]llmhttp.Message, 0, len(folded)+2)
	if strings.TrimSpace(priorSummary) != "" {
		out = append(out, llmhttp.Message{Role: "user", Content: wrapPriorSummary(priorSummary)})
	}

	for _, r := range folded {
		switch r.Role {
		case "user":
			if c := strings.TrimSpace(r.Content); c != "" {
				out = append(out, llmhttp.Message{Role: "user", Content: c})
			}
		case "assistant":
			var b strings.Builder
			b.WriteString(r.Content)
			if r.ToolCalls != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString("[tool calls] ")
				b.WriteString(truncate(r.ToolCalls, s.cfg))
			}
			if c := strings.TrimSpace(b.String()); c != "" {
				out = append(out, llmhttp.Message{Role: "assistant", Content: c})
			}
		case "tool":
			name := r.ToolName
			if name == "" {
				name = "tool"
			}
			out = append(out, llmhttp.Message{
				Role:    "user",
				Content: "[tool result: " + name + "]\n" + truncate(r.Content, s.cfg),
			})
		}
	}

	if len(out) == 0 {
		return nil
	}
	return append(out, llmhttp.Message{Role: "user", Content: triggerPrompt})
}

// truncate head/tail trims oversized tool output so one giant result (a full
// file dump, a long stdout) cannot by itself overflow the summarizer's own
// context window. This only ever touches the copy sent to the summarizer;
// stored rows and normal agent replay are untouched.
func truncate(v string, cfg config.CompactionConfig) string {
	limit := cfg.ToolResultTruncateThresholdChars
	keep := cfg.ToolResultTruncateKeepChars
	if limit <= 0 || keep <= 0 || len(v) <= limit || keep*2 >= len(v) {
		return v
	}
	omitted := len(v) - keep*2
	return v[:keep] +
		fmt.Sprintf("\n\n[... %d chars truncated for compaction ...]\n\n", omitted) +
		v[len(v)-keep:]
}
