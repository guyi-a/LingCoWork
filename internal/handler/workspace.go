package handler

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/service"
	"github.com/guyi-a/Interview-Agent/internal/workspacegit"
)

// inlineMimeByExt whitelists extensions that may be served inline (i.e.
// with Content-Disposition: inline so the browser embeds them). Anything
// outside this set is rejected — inline serving of arbitrary content
// (especially html/svg/js) is a security hazard.
//
// docx / pptx 走这里是给前端的 docx-preview / pptx-renderer 拉 ArrayBuffer
// 用的，虽然是 OOXML 二进制包（ZIP）但内容都是被前端解析，X-Content-Type-Options:
// nosniff 挡住浏览器把它当 html 猜。
var inlineMimeByExt = map[string]string{
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".webm": "video/webm",
	".ogv":  "video/ogg",
	".mov":  "video/quicktime",
	".mkv":  "video/x-matroska",
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".wav":  "audio/wav",
	".ogg":  "audio/ogg",
	".flac": "audio/flac",
	".aac":  "audio/aac",
}

type WorkspaceHandler struct {
	svc     *service.WorkspaceService
	diffSvc *service.WorkspaceDiffService
}

func NewWorkspaceHandler(
	svc *service.WorkspaceService,
	diffSvc ...*service.WorkspaceDiffService,
) *WorkspaceHandler {
	h := &WorkspaceHandler{svc: svc}
	if len(diffSvc) > 0 {
		h.diffSvc = diffSvc[0]
	}
	return h
}

func (h *WorkspaceHandler) Register(r *gin.Engine) {
	r.GET("/conversations/:id/workspace/tree", h.Tree)
	r.GET("/conversations/:id/workspace/file", h.File)
	r.GET("/conversations/:id/workspace/download", h.Download)
	r.GET("/conversations/:id/workspace/inline", h.Inline)
	r.GET("/conversations/:id/workspace/changes", h.Changes)
	r.GET("/conversations/:id/workspace/diff", h.Diff)
	r.GET("/conversations/:id/workspace/status", h.Status)
	r.GET("/conversations/:id/workspace/terminal", h.Terminal)
}

func (h *WorkspaceHandler) Status(c *gin.Context) {
	if h.diffSvc == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "workspace status unavailable"})
		return
	}
	result, err := h.diffSvc.RepositoryStatus(
		c.Request.Context(),
		c.Param("id"),
		c.Query("project_id"),
	)
	if err != nil {
		writeWorkspaceDiffError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *WorkspaceHandler) Changes(c *gin.Context) {
	if h.diffSvc == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "workspace diff unavailable"})
		return
	}
	result, err := h.diffSvc.Changes(
		c.Request.Context(),
		c.Param("id"),
		c.Query("project_id"),
		c.Query("scope"),
	)
	if err != nil {
		writeWorkspaceDiffError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *WorkspaceHandler) Diff(c *gin.Context) {
	if h.diffSvc == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "workspace diff unavailable"})
		return
	}
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path query param required"})
		return
	}
	result, err := h.diffSvc.Diff(
		c.Request.Context(),
		c.Param("id"),
		c.Query("project_id"),
		c.Query("scope"),
		path,
	)
	if err != nil {
		writeWorkspaceDiffError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func writeWorkspaceDiffError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidChangeScope):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrChangeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, workspacegit.ErrNotRepository),
		errors.Is(err, workspacegit.ErrNotRepositoryRoot):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		writeWorkspaceError(c, err)
	}
}

type treeEntry struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size,omitempty"`
	ModifiedAt string `json:"modified_at"`
}

type treeResponse struct {
	Workspace service.WorkspaceMeta `json:"workspace"`
	Entries   []treeEntry           `json:"entries"`
	Truncated bool                  `json:"truncated,omitempty"`
}

func (h *WorkspaceHandler) Tree(c *gin.Context) {
	id := c.Param("id")
	result, err := h.svc.TreeFor(c.Request.Context(), id, c.Query("project_id"))
	if err != nil {
		writeWorkspaceError(c, err)
		return
	}
	entries := make([]treeEntry, 0, len(result.Entries))
	for _, e := range result.Entries {
		entries = append(entries, treeEntry{
			Path:       e.Path,
			Name:       e.Name,
			IsDir:      e.IsDir,
			Size:       e.Size,
			ModifiedAt: e.ModifiedAt.Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, treeResponse{
		Workspace: result.Workspace,
		Entries:   entries,
		Truncated: result.Truncated,
	})
}

func (h *WorkspaceHandler) File(c *gin.Context) {
	id := c.Param("id")
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path query param required"})
		return
	}
	result, err := h.svc.FileFor(c.Request.Context(), id, c.Query("project_id"), path)
	if err != nil {
		writeWorkspaceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *WorkspaceHandler) Download(c *gin.Context) {
	id := c.Param("id")
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path query param required"})
		return
	}
	abs, name, err := h.svc.OpenForDownloadFor(c.Request.Context(), id, c.Query("project_id"), path)
	if err != nil {
		writeWorkspaceError(c, err)
		return
	}
	c.FileAttachment(abs, name)
}

// Inline serves a file with `Content-Disposition: inline` so browsers embed
// it (PDF via <iframe>, media via <video>/<audio>). Extension must be in the
// whitelist above — never sniff, never let arbitrary content be inline
// (html/svg/js could execute in-origin).
func (h *WorkspaceHandler) Inline(c *gin.Context) {
	id := c.Param("id")
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path query param required"})
		return
	}
	abs, name, err := h.svc.OpenForDownloadFor(c.Request.Context(), id, c.Query("project_id"), path)
	if err != nil {
		writeWorkspaceError(c, err)
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	mime, ok := inlineMimeByExt[ext]
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "inline preview not allowed for this file type"})
		return
	}
	c.Header("Content-Type", mime)
	c.Header("Content-Disposition", "inline; filename=\""+name+"\"")
	c.Header("X-Content-Type-Options", "nosniff")
	c.File(abs)
}

func writeWorkspaceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNoWorkspace):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrPathOutsideWorkspace):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrFileNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrPathIsDirectory):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
