package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/mcp"
)

func newMCPTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	mgr := mcp.New(&mcp.Config{Path: "mcp.json"}, nil,
		mcp.NewAuthorizer(nil, mcp.DefaultRedirectURI))
	r := gin.New()
	NewMCPHandler(mgr).Register(r)
	return r
}

func do(t *testing.T, r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

// A callback carrying a state nobody issued is either forged or stale. Either
// way it must not exchange a code.
func TestOAuthCallbackRejectsUnknownState(t *testing.T) {
	w := do(t, newMCPTestRouter(t), http.MethodGet,
		"/mcp/oauth/callback?code=abc&state=never-issued")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "授权失败") {
		t.Errorf("body does not report a failure: %q", w.Body.String())
	}
}

// The provider's own error text is shown, so it has to be escaped: it is
// attacker-influenced content rendered into a page.
func TestOAuthCallbackEscapesProviderError(t *testing.T) {
	w := do(t, newMCPTestRouter(t), http.MethodGet,
		"/mcp/oauth/callback?error=access_denied&error_description=%3Cscript%3Ealert(1)%3C/script%3E")

	body := w.Body.String()
	if strings.Contains(body, "<script>") {
		t.Fatalf("provider error was rendered unescaped: %q", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("escaped error text is missing: %q", body)
	}
}

func TestAuthorizeUnknownServerIs404(t *testing.T) {
	w := do(t, newMCPTestRouter(t), http.MethodPost, "/mcp/servers/nope/authorize")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDeleteUnknownServerIs404(t *testing.T) {
	w := do(t, newMCPTestRouter(t), http.MethodDelete, "/mcp/servers/nope")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDeleteRemovesTheEntryFromTheFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "mcp.json")
	body := `{"mcpServers": {
	  "kling":    {"url": "https://klingai.com/mcp"},
	  "deepwiki": {"url": "https://mcp.deepwiki.com/mcp"}
	}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_CONFIG_PATH", path)

	cfg, err := mcp.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	NewMCPHandler(mcp.New(cfg, nil, mcp.NewAuthorizer(nil, mcp.DefaultRedirectURI))).Register(r)

	if w := do(t, r, http.MethodDelete, "/mcp/servers/kling"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "kling") {
		t.Errorf("kling is still in the file: %s", raw)
	}
	if !strings.Contains(string(raw), "deepwiki") {
		t.Errorf("deepwiki went with it: %s", raw)
	}
}

// The status listing is what the settings page polls, including on first
// load before anything has connected.
func TestServersListsAnEmptyManager(t *testing.T) {
	w := do(t, newMCPTestRouter(t), http.MethodGet, "/mcp/servers")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"servers":[]`) {
		t.Fatalf("body = %q", w.Body.String())
	}
}
