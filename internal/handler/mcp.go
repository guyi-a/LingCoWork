package handler

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/mcp"
)

// MCPHandler backs the connectors settings page.
//
// Everything here takes effect immediately: saving a config reconciles the
// running servers, and authorizing one reconnects it. Nothing asks the user
// to restart, because a restart could not be the answer anyway — OAuth is
// interactive, so a server needing it is necessarily authorized while the
// app is already running.
type MCPHandler struct {
	mgr *mcp.Manager
}

func NewMCPHandler(mgr *mcp.Manager) *MCPHandler {
	return &MCPHandler{mgr: mgr}
}

func (h *MCPHandler) Register(r *gin.Engine) {
	r.GET("/mcp/servers", h.Servers)
	r.POST("/mcp/servers/:name/test", h.Test)
	r.POST("/mcp/servers/:name/authorize", h.Authorize)
	r.DELETE("/mcp/servers/:name", h.DeleteServer)
	// The browser navigates here directly from the authorization server, so
	// it is a GET returning HTML rather than JSON, and CORS does not apply.
	r.GET("/mcp/oauth/callback", h.OAuthCallback)
	r.GET("/mcp/config", h.GetConfig)
	// POST, not PUT: the CORS middleware allows GET/POST/DELETE only, and
	// every other mutation in this API is a POST anyway.
	r.POST("/mcp/config", h.SaveConfig)
}

// testTimeout bounds a connection test. Longer than a normal handshake budget
// because the first run of an `npx -y` server downloads the package, and a
// user watching a spinner would rather wait than see a false failure.
const testTimeout = 90 * time.Second

// authTimeout bounds building an authorization URL. Short: it is at most a
// metadata fetch and a registration call.
const authTimeout = 30 * time.Second

// maxConfigBytes caps what the settings page may upload. The config is a
// handful of server entries; anything approaching this is a mistake.
const maxConfigBytes = 1 << 20

func (h *MCPHandler) Servers(c *gin.Context) {
	// Servers carries no headers, env or tokens by construction — see
	// mcp.ServerStatus.
	c.JSON(http.StatusOK, gin.H{
		"servers":     h.mgr.Status(c.Request.Context()),
		"issues":      h.mgr.Issues(),
		"config_path": h.mgr.ConfigPath(),
	})
}

func (h *MCPHandler) Test(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), testTimeout)
	defer cancel()

	res, err := h.mgr.TestConnection(ctx, c.Param("name"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// Authorize returns the URL the user must visit. The frontend opens it in the
// system browser rather than an iframe or a webview: the flow depends on the
// provider's existing session cookies and often on a platform passkey prompt,
// and neither survives an embedded context.
func (h *MCPHandler) Authorize(c *gin.Context) {
	name := c.Param("name")
	srv, ok := h.mgr.ServerConfigFor(name)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "no server named " + name})
		return
	}
	auth := h.mgr.Authorizer()
	if auth == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "oauth is not available"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), authTimeout)
	defer cancel()

	url, err := auth.BuildAuthURL(ctx, srv)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"auth_url": url})
}

// OAuthCallback is where the authorization server sends the browser back.
//
// It answers with a page rather than a redirect into the app because the tab
// was opened by the system browser and has nowhere to go: the user's real
// window is elsewhere, already polling for the state change.
func (h *MCPHandler) OAuthCallback(c *gin.Context) {
	if desc := c.Query("error"); desc != "" {
		// The user declined, or the provider refused. Its own description is
		// the most useful thing we have, and it is escaped before display.
		detail := c.Query("error_description")
		if detail == "" {
			detail = desc
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", callbackPage(false, "授权未完成", detail))
		return
	}

	auth := h.mgr.Authorizer()
	if auth == nil {
		c.Data(http.StatusInternalServerError, "text/html; charset=utf-8",
			callbackPage(false, "授权失败", "oauth is not available"))
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), authTimeout)
	defer cancel()

	server, err := auth.CompleteAuth(ctx, c.Query("code"), c.Query("state"))
	if err != nil {
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8",
			callbackPage(false, "授权失败", err.Error()))
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8",
		callbackPage(true, "授权完成", "已连接 "+server+"，可以关闭此页面回到 LingCoWork。"))
}

// DeleteServer removes a server three ways at once: the entry leaves the
// config file, the connection is dropped, and any OAuth token is forgotten.
//
// Deleting the entry alone would leave the other two behind, and both are
// surprises — a stored token is a credential the user believes they are rid
// of, and a live connection is tools the model can still call for a server
// that no longer appears anywhere in the UI.
func (h *MCPHandler) DeleteServer(c *gin.Context) {
	name := c.Param("name")
	_, running := h.mgr.ServerConfigFor(name)

	removed, err := mcp.RemoveServer(h.mgr.ConfigPath(), name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !removed && !running {
		c.JSON(http.StatusNotFound, gin.H{"error": "no server named " + name})
		return
	}

	// Held rather than returned: the entry is already off disk, so failing
	// here and stopping would leave the connection up and describe an
	// outcome that did not happen.
	var credErr error
	if auth := h.mgr.Authorizer(); auth != nil {
		credErr = auth.Revoke(c.Request.Context(), name)
	}

	// Synchronous, unlike the one in SaveConfig: reconciling a deletion is a
	// teardown with nothing to wait on, so the status the page fetches next
	// is already correct.
	if cfg, err := mcp.LoadConfig(); err == nil {
		h.mgr.Apply(context.Background(), cfg)
	}

	if credErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "已从配置里删除，但清除授权失败：" + credErr.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// GetConfig returns the file verbatim, credentials included.
//
// This is the one endpoint that hands secrets back, and it is the deliberate
// one: the page cannot offer a JSON editor over a redacted document, because
// saving it would write the redaction markers back over the real keys.
// Everywhere else — status, errors, logs — is scrubbed.
func (h *MCPHandler) GetConfig(c *gin.Context) {
	path := h.mgr.ConfigPath()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		c.JSON(http.StatusOK, gin.H{"path": path, "exists": false, "content": defaultConfigTemplate})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": path, "exists": true, "content": string(raw)})
}

func (h *MCPHandler) SaveConfig(c *gin.Context) {
	var body struct {
		Content string `json:"content"`
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxConfigBytes))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body: " + err.Error()})
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request body: " + err.Error()})
		return
	}

	issues, err := mcp.ValidateRaw([]byte(body.Content))
	if err != nil {
		// A document that will not parse is rejected outright. Writing it
		// would leave the app with no MCP servers and no obvious reason why.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := mcp.WriteConfig(h.mgr.ConfigPath(), []byte(body.Content)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Reconcile in the background: a new stdio server may need to install
	// itself before it answers, and the save button should not hang on that.
	// Untouched servers are left connected — see Manager.Apply.
	if cfg, err := mcp.LoadConfig(); err == nil {
		go h.mgr.Apply(context.Background(), cfg)
	}

	// Per-entry problems are saved but reported: a half-finished entry is a
	// normal state to leave the editor in, and blocking the save would lose
	// the user's other edits.
	c.JSON(http.StatusOK, gin.H{"saved": true, "issues": issues})
}

const defaultConfigTemplate = "{\n  \"mcpServers\": {}\n}\n"

// callbackPage renders the standalone page the browser lands on. Written by
// hand rather than served from the frontend build because this tab is outside
// the app: it has no router, no bundle, and one job.
func callbackPage(ok bool, title, detail string) []byte {
	accent := "#b42318"
	if ok {
		accent = "#067647"
	}
	return []byte(`<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(title) + `</title>
<style>
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center;
         background:#faf9f7; color:#1a1a1a;
         font:15px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
  main { max-width:32rem; padding:2.5rem; text-align:center; }
  h1 { margin:0 0 .75rem; font-size:20px; font-weight:600; color:` + accent + `; }
  p { margin:0; color:#57534e; word-break:break-word; }
</style>
</head>
<body>
  <main>
    <h1>` + html.EscapeString(title) + `</h1>
    <p>` + html.EscapeString(detail) + `</p>
  </main>
</body>
</html>`)
}
