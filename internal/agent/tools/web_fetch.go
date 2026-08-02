package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/webfetch"
)

type webFetchInput struct {
	URL             string `json:"url" jsonschema:"description=Target URL to fetch (http/https only). Required."`
	Prompt          string `json:"prompt,omitempty" jsonschema:"description=What you're looking for in the page (helps you focus when reading the returned text). Examples: 'Find the pricing table'\\, 'Extract the API endpoint list'. Optional; just prefixes the returned text."`
	MaxBytes        int    `json:"max_bytes,omitempty" jsonschema:"description=Max bytes to download. Default 2MB\\, max 5MB. Bump if the page is heavy and you're missing content."`
	TimeoutSec      int    `json:"timeout_sec,omitempty" jsonschema:"description=Request timeout in seconds. Default 30\\, max 120."`
	FollowRedirects bool   `json:"follow_redirects,omitempty" jsonschema:"description=Follow HTTP 3xx redirects. Default true. Same-host redirects are followed up to 10 hops; cross-host redirects always return is_redirect=true (call web_fetch again with the new URL if you want them)."`
	UseCache        *bool  `json:"use_cache,omitempty" jsonschema:"description=Use LRU cache (15min TTL). Default true. Set false when you need a fresh fetch (e.g. checking a rapidly-changing status page)."`
}

type webFetchOutput struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	StatusCode  int    `json:"status_code"`
	StatusText  string `json:"status_text"`
	ContentType string `json:"content_type,omitempty"`
	ByteCount   int    `json:"byte_count"`
	Text        string `json:"text,omitempty"` // 已装饰：加了 Title / [HTTP xxx] / [Extraction focus] 前缀
	IsRedirect  bool   `json:"is_redirect,omitempty"`
	RedirectURL string `json:"redirect_url,omitempty"`
	Cached      bool   `json:"cached,omitempty"`
}

// newWebFetchTool 构造 web_fetch。cache 全局共享（跟 krow 一样），
// key 是 (url, max_bytes)，命中同一份 result。
func newWebFetchTool() (tool.BaseTool, error) {
	cache := webfetch.NewCache(0, 0, 0) // 全走默认：100 entries / 50MB / 15min

	fn := func(ctx context.Context, in *webFetchInput) (*webFetchOutput, error) {
		if in == nil || strings.TrimSpace(in.URL) == "" {
			return nil, errors.New("url is required")
		}
		targetURL := strings.TrimSpace(in.URL)

		// 前端校验一遍：非 http/https 直接拒
		parsed, err := url.Parse(targetURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("only http/https URLs are supported (got %q)", targetURL)
		}

		maxBytes := in.MaxBytes
		if maxBytes <= 0 {
			maxBytes = webfetch.DefaultMaxBytes
		}

		useCache := true
		if in.UseCache != nil {
			useCache = *in.UseCache
		}
		if useCache {
			if cached := cache.Get(targetURL, maxBytes); cached != nil {
				return decorateResult(cached, in.Prompt, in.FollowRedirects, true), nil
			}
		}

		timeout := time.Duration(in.TimeoutSec) * time.Second
		result, err := webfetch.FetchURL(ctx, targetURL, webfetch.Options{
			MaxBytes:        maxBytes,
			Timeout:         timeout,
			FollowRedirects: in.FollowRedirects || in.UseCache == nil, // 默认 true
		})
		if err != nil {
			if errors.Is(err, webfetch.ErrTimeout) {
				return &webFetchOutput{
					URL: targetURL, FinalURL: targetURL,
					StatusCode: 0, StatusText: "Timeout",
					Text: fmt.Sprintf("[Request timeout after %d seconds]", in.TimeoutSec),
				}, nil
			}
			if errors.Is(err, webfetch.ErrPrivateNetwork) {
				return nil, fmt.Errorf("blocked: private/localhost URL not allowed. Original error: %v", err)
			}
			var fe *webfetch.FetchError
			if errors.As(err, &fe) {
				return &webFetchOutput{
					URL: targetURL, FinalURL: targetURL,
					StatusCode: 0, StatusText: "Fetch Error",
					Text: fmt.Sprintf("[%s]", fe.Error()),
				}, nil
			}
			return nil, fmt.Errorf("fetch failed: %w", err)
		}

		if useCache && result.StatusCode >= 200 && result.StatusCode < 300 {
			cache.Set(targetURL, maxBytes, result)
		}
		return decorateResult(result, in.Prompt, in.FollowRedirects, false), nil
	}

	desc := "Fetch a web page and return its cleaned plain text (not raw HTML). " +
		"Use after web_search to read a specific hit's full content, or when the user pastes a URL. " +
		"IMPORTANT: This tool WILL FAIL for authenticated / private URLs (Google Docs, Confluence, Jira, private GitHub, ...) — for those, tell the user you can't fetch them. " +
		"Text extraction strips <script>/<style>/<head> and all tags; HTML entities are decoded and whitespace collapsed. " +
		"HTML pages get their <title> prepended; non-2xx responses get a [HTTP NNN] prefix; your `prompt` param (if set) is prepended as [Extraction focus: ...] to remind you what to look for. " +
		"Cross-host redirects are NOT followed automatically — you'll get is_redirect=true + redirect_url, call web_fetch again to follow. " +
		"Results are LRU-cached 15 min (key=url+max_bytes) — set use_cache=false to force fresh fetch."

	return utils.InferTool("web_fetch", desc, fn)
}

func decorateResult(r *webfetch.Result, prompt string, followRedirects, cached bool) *webFetchOutput {
	text := r.Text

	if r.IsRedirect && r.RedirectURL != "" {
		if followRedirects {
			text = fmt.Sprintf(
				"REDIRECT DETECTED: URL redirects to different host.\n\n"+
					"Original: %s\nRedirects to: %s\n\n"+
					"To fetch the redirected content, call web_fetch again with:\n  url=%q",
				r.URL, r.RedirectURL, r.RedirectURL)
		} else {
			text = "Redirect to: " + r.RedirectURL
		}
	} else if text == "" && r.ContentType != "" {
		text = fmt.Sprintf("[Binary content: %s, %d bytes]", r.ContentType, r.ByteCount)
	}

	if r.Title != "" {
		text = "Title: " + r.Title + "\n\n" + text
	}
	if r.StatusCode >= 400 {
		text = fmt.Sprintf("[HTTP %d %s]\n\n%s", r.StatusCode, r.StatusText, text)
	}
	if prompt != "" {
		text = "[Extraction focus: " + prompt + "]\n\n" + text
	}

	return &webFetchOutput{
		URL:         r.URL,
		FinalURL:    r.FinalURL,
		StatusCode:  r.StatusCode,
		StatusText:  r.StatusText,
		ContentType: r.ContentType,
		ByteCount:   r.ByteCount,
		Text:        text,
		IsRedirect:  r.IsRedirect,
		RedirectURL: r.RedirectURL,
		Cached:      cached,
	}
}
