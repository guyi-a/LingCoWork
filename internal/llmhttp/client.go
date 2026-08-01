// Package llmhttp is a minimal OpenAI-compatible chat/completions client.
// Just enough for the non-conversational side-channel calls this codebase
// makes: one non-streaming request, plain JSON body in / string out.
//
// We hand-roll instead of pulling in eino's OpenAI adapter because (a) the
// adapter carries the ChatModel / ToolCallingChatModel machinery these call
// sites don't need, (b) they target their own endpoint with their own key,
// separate from the main model, and (c) 80 lines of net/http with no
// external dep beats another versioned dependency.
//
// Call sites: internal/approval (auto-mode classifier),
// internal/compaction (history summarizer).
package llmhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	apiKey  string
	baseURL string // e.g. https://api.deepseek.com  (no trailing slash)
	http    *http.Client
}

func New(apiKey, baseURL string, timeout time.Duration) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// Message matches the OpenAI wire format (role / content only — we don't
// send tool calls or images).
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// request omits fields we never set (top_p, stream, tools, ...).
type request struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	MaxTokens      int             `json:"max_tokens"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

// responseFormat forces the model to emit valid JSON. DeepSeek + OpenAI
// both honour {"type":"json_object"}.
type responseFormat struct {
	Type string `json:"type"`
}

type response struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Distinct error types let the caller distinguish "no key configured" (a
// deployment issue) from "endpoint timed out" (a runtime blip) from "the
// endpoint returned garbage" (probably a protocol version mismatch). The
// approval classifier maps each to a different reason string in its audit
// log.
var (
	ErrNoAPIKey    = errors.New("llmhttp: no api key configured")
	ErrTimeout     = errors.New("llmhttp: request timed out")
	ErrBadResponse = errors.New("llmhttp: malformed response")
)

// Chat runs a single non-streaming completion. Returns the assistant
// message content on success, or one of the sentinel errors above.
func (c *Client) Chat(ctx context.Context, model string, msgs []Message, maxTokens int, jsonMode bool) (string, error) {
	if c == nil || c.apiKey == "" {
		return "", ErrNoAPIKey
	}
	req := request{
		Model:       model,
		Messages:    msgs,
		MaxTokens:   maxTokens,
		Temperature: 0,
	}
	if jsonMode {
		req.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("llmhttp: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llmhttp: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isNetTimeout(err) {
			return "", ErrTimeout
		}
		return "", fmt.Errorf("llmhttp: http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("llmhttp: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("llmhttp: status %d: %s", resp.StatusCode, trimForLog(raw, 300))
	}
	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", ErrBadResponse
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("llmhttp: api error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", ErrBadResponse
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return "", ErrBadResponse
	}
	return content, nil
}

// isNetTimeout unwraps net-level timeout errors that don't wrap
// context.DeadlineExceeded (e.g. http.Client.Timeout expiration).
func isNetTimeout(err error) bool {
	type timeouter interface{ Timeout() bool }
	var t timeouter
	if errors.As(err, &t) && t.Timeout() {
		return true
	}
	return false
}

func trimForLog(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
