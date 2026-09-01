// Package webfetch 是本项目的网页抓取能力，参考 krow-agent 的完整实现：
//
//   - 强 SSRF 保护：private IP / localhost / .local / .lan 直接挡；redirect
//     目标也检查
//   - HTML → 纯文本提取：去 <script>/<style>/<head>/<!-- -->/所有标签，
//     unescape entities，压缩空白
//   - <title> 提取（HTML 响应）
//   - 跨域重定向不自动跟进：返回 is_redirect + redirect_url，让 caller
//     决定是否新调一次
//   - 无缓存 / 无重试 / 无 per-request 状态 —— 缓存等 concern 由外层
//     包一层，见 cache.go
//
// 错误类型分层（typed errors）：ErrPrivateNetwork / ErrTimeout /
// ErrFetch，对不同错误上层可分开处理。
package webfetch

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultUserAgent = "Interview-Agent/0.1 (WebFetch)"
	DefaultAccept    = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	MaxContentLength = 5_000_000
	DefaultMaxBytes  = 2_000_000
	DefaultTimeout   = 30 * time.Second
	MaxTimeoutCap    = 120 * time.Second
	MaxRedirects     = 10
)

// Result 是一次 HTTP 抓取的结果。
//
// Text 是干净的纯文本（无 AI-friendly prefix，不含 [HTTP 404] / Title: 之类
// 的装饰）—— 装饰逻辑由 tool wrapper 加。
//
//   - HTML/XML: tags 剥掉、entities unescape、whitespace 压缩
//   - JSON / text/*: 原始 body
//   - 二进制: nil
//
// Title 只对 HTML 响应有值，其他为空。
//
// IsRedirect=true 表示这是一次**跨域**重定向，本函数拒绝跟进；同域
// redirect 会被自动跟进（up to MaxRedirects），最终结果的 IsRedirect=false。
type Result struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	StatusCode  int    `json:"status_code"`
	StatusText  string `json:"status_text"`
	ContentType string `json:"content_type,omitempty"`
	Encoding    string `json:"encoding,omitempty"`
	ByteCount   int    `json:"byte_count"`
	Text        string `json:"text,omitempty"`
	Title       string `json:"title,omitempty"`
	IsRedirect  bool   `json:"is_redirect,omitempty"`
	RedirectURL string `json:"redirect_url,omitempty"`
}

var (
	// ErrPrivateNetwork：URL 或 redirect 目标是 private / loopback / .local
	// 之类，且 AllowPrivateNetwork=false。
	ErrPrivateNetwork = errors.New("blocked: private/localhost URL")

	// ErrTimeout：连接 / 读取超时。
	ErrTimeout = errors.New("request timeout")
)

// FetchError 包裹其他失败情况（空 URL、非 http scheme、响应过大、
// redirect 太多、malformed redirect 等）—— 上层用 errors.As 拿细节。
type FetchError struct {
	Msg string
	Err error
}

func (e *FetchError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Msg, e.Err)
	}
	return e.Msg
}

func (e *FetchError) Unwrap() error { return e.Err }

// Options 是 FetchURL 的可选参数。
type Options struct {
	MaxBytes            int           // 最大响应字节；≤0 用 DefaultMaxBytes；上限 MaxContentLength
	Timeout             time.Duration // 单次请求超时；≤0 用 DefaultTimeout；上限 MaxTimeoutCap
	AllowPrivateNetwork bool          // false 时私网 URL / redirect 目标直接挡；true 时放行
	UserAgent           string        // 自定义 UA
	FollowRedirects     bool          // true=自动跟进同域 redirect；false=直接返 IsRedirect=true
	// OnPrivateRedirect 是"redirect 落到私网时"的回调；返回 error 就 abort
	// 抓取（典型用法：请求用户 approval）。回调不设时，走 AllowPrivateNetwork
	// 的兜底（false→抛 ErrPrivateNetwork；true→放行）。
	OnPrivateRedirect func(target string) error
}

var (
	whitespaceRe = regexp.MustCompile(`\s+`)
	// Go 的 RE2 不支持 backreference，只能拆成 4 个正则挨个替换。
	scriptRe   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	styleRe    = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	noscriptRe = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript>`)
	headRe     = regexp.MustCompile(`(?is)<head\b[^>]*>.*?</head>`)
	tagRe      = regexp.MustCompile(`(?is)<[^>]+>`)
	commentRe  = regexp.MustCompile(`(?s)<!--.*?-->`)
	titleRe    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

var privateHostnames = map[string]struct{}{
	"localhost":             {},
	"localhost.localdomain": {},
}

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	// IPv6 unique local (fc00::/7) 视作私网
	if ip.To4() == nil && len(ip) == 16 && (ip[0]&0xfe == 0xfc) {
		return true
	}
	return false
}

func isPrivateHostname(hostname string) bool {
	hn := strings.ToLower(strings.TrimSpace(hostname))
	hn = strings.TrimSuffix(hn, ".")
	if _, ok := privateHostnames[hn]; ok {
		return true
	}
	// 去掉 IPv6 括号
	hn = strings.TrimPrefix(hn, "[")
	hn = strings.TrimSuffix(hn, "]")
	if isPrivateIP(hn) {
		return true
	}
	for _, suffix := range []string{".local", ".internal", ".localhost", ".lan"} {
		if strings.HasSuffix(hn, suffix) {
			return true
		}
	}
	return false
}

// IsPrivateURL classifies literal private/local targets without performing
// DNS. FetchURL applies the same check at execution time.
func IsPrivateURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return false
	}
	return isPrivateHostname(parsed.Hostname())
}

func extractTextFromHTML(s string) string {
	s = commentRe.ReplaceAllString(s, " ")
	s = scriptRe.ReplaceAllString(s, " ")
	s = styleRe.ReplaceAllString(s, " ")
	s = noscriptRe.ReplaceAllString(s, " ")
	s = headRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = whitespaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func extractTitleFromHTML(s string) string {
	m := titleRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

var statusTexts = map[int]string{
	200: "OK", 201: "Created",
	301: "Moved Permanently", 302: "Found", 303: "See Other",
	304: "Not Modified", 307: "Temporary Redirect", 308: "Permanent Redirect",
	400: "Bad Request", 401: "Unauthorized", 403: "Forbidden", 404: "Not Found",
	500: "Internal Server Error", 502: "Bad Gateway", 503: "Service Unavailable",
}

func statusText(code int) string {
	if s, ok := statusTexts[code]; ok {
		return s
	}
	return "Unknown"
}

// FetchURL 抓一个 HTTP/HTTPS URL 并返回结果。
//
// 参数不合法（空 URL / 非 http scheme / 缺 host）→ *FetchError。
// 初始 host 是私网且 !AllowPrivateNetwork → ErrPrivateNetwork。
// Redirect 目标是私网：先走 OnPrivateRedirect 回调（返 error 就 abort）；
// 回调没设则看 AllowPrivateNetwork。
// 超时 → ErrTimeout。响应过大 / redirect 太多 → *FetchError。
func FetchURL(ctx context.Context, rawURL string, opts Options) (*Result, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, &FetchError{Msg: "URL is empty"}
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, &FetchError{Msg: "invalid URL", Err: err}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, &FetchError{Msg: "only http/https URLs are supported"}
	}
	if parsed.Host == "" {
		return nil, &FetchError{Msg: "invalid URL: missing host"}
	}

	hostname := parsed.Hostname()
	if isPrivateHostname(hostname) && !opts.AllowPrivateNetwork {
		return nil, fmt.Errorf("%w (%s)", ErrPrivateNetwork, hostname)
	}

	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxBytes > MaxContentLength {
		maxBytes = MaxContentLength
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeoutCap {
		timeout = MaxTimeoutCap
	}

	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}

	// 不用 http.Client 内置的 redirect follow —— 我们手工做，才能挡跨域 +
	// 私网 redirect + 超上限。
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	currentURL := rawURL
	redirectCount := 0
	originalHost := strings.ToLower(hostname)

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentURL, nil)
		if err != nil {
			return nil, &FetchError{Msg: "build request failed", Err: err}
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", DefaultAccept)

		resp, err := client.Do(req)
		if err != nil {
			// context 超时 → ErrTimeout
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, ErrTimeout
			}
			// url.Error 里可能包 net.Error 的 Timeout
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return nil, ErrTimeout
			}
			return nil, &FetchError{Msg: "request failed", Err: err}
		}

		// 3xx redirect：手工处理
		if resp.StatusCode >= 300 && resp.StatusCode < 400 &&
			resp.StatusCode != 304 {
			location := resp.Header.Get("Location")
			resp.Body.Close()

			if !opts.FollowRedirects {
				return &Result{
					URL:         rawURL,
					FinalURL:    currentURL,
					StatusCode:  resp.StatusCode,
					StatusText:  statusText(resp.StatusCode),
					IsRedirect:  true,
					RedirectURL: strEmpty(location),
				}, nil
			}
			if location == "" {
				return nil, &FetchError{Msg: "redirect response missing Location header"}
			}
			redirectCount++
			if redirectCount > MaxRedirects {
				return nil, &FetchError{Msg: fmt.Sprintf("too many redirects (>%d)", MaxRedirects)}
			}

			// 相对 URL → 拼绝对
			nextParsed, perr := url.Parse(location)
			if perr != nil {
				return nil, &FetchError{Msg: "invalid redirect Location", Err: perr}
			}
			if !nextParsed.IsAbs() {
				cur, _ := url.Parse(currentURL)
				nextParsed = cur.ResolveReference(nextParsed)
				location = nextParsed.String()
			}
			nextHost := strings.ToLower(nextParsed.Hostname())

			// 私网 redirect：走 callback 或 AllowPrivateNetwork
			if isPrivateHostname(nextHost) {
				if opts.OnPrivateRedirect != nil {
					if err := opts.OnPrivateRedirect(location); err != nil {
						return nil, err
					}
				} else if !opts.AllowPrivateNetwork {
					return nil, fmt.Errorf("%w: redirect to %s", ErrPrivateNetwork, nextHost)
				}
			}

			// 跨域 redirect：不跟进，返 IsRedirect
			if nextHost != originalHost {
				return &Result{
					URL:         rawURL,
					FinalURL:    currentURL,
					StatusCode:  resp.StatusCode,
					StatusText:  statusText(resp.StatusCode),
					IsRedirect:  true,
					RedirectURL: location,
				}, nil
			}

			currentURL = location
			continue
		}

		// 拉 body（限量）
		defer resp.Body.Close()
		limited := io.LimitReader(resp.Body, int64(maxBytes)+1)
		bodyBytes, err := io.ReadAll(limited)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, ErrTimeout
			}
			return nil, &FetchError{Msg: "read body failed", Err: err}
		}
		if len(bodyBytes) > maxBytes {
			return nil, &FetchError{Msg: fmt.Sprintf("response too large (>%d bytes)", maxBytes)}
		}

		contentType := resp.Header.Get("Content-Type")
		ctLower := strings.ToLower(contentType)
		bodyText := string(bodyBytes)

		var text, title string
		switch {
		case strings.Contains(ctLower, "html"), strings.Contains(ctLower, "xml"):
			text = extractTextFromHTML(bodyText)
			title = extractTitleFromHTML(bodyText)
		case strings.Contains(ctLower, "json"), strings.Contains(ctLower, "text"):
			text = bodyText
		default:
			// binary：text 留空
		}

		return &Result{
			URL:         rawURL,
			FinalURL:    currentURL,
			StatusCode:  resp.StatusCode,
			StatusText:  statusText(resp.StatusCode),
			ContentType: contentType,
			Encoding:    "utf-8",
			ByteCount:   len(bodyBytes),
			Text:        text,
			Title:       title,
		}, nil
	}
}

func strEmpty(s string) string {
	if s == "" {
		return ""
	}
	return s
}
