package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/skillhub"
)

// SkillHubHandler backs the skill marketplace page.
//
// The registry sends no CORS headers, so the browser cannot fetch it
// directly — every read is forwarded through here (the same reason klingwork
// routes these calls through its main process). This is not an open proxy:
// only the fixed set of registry endpoints is reachable, and slugs are
// validated before they touch a request path.
//
// fullSlug 可以含 "/"（"@scope/slug"），所以走 query 参数而不是路径参数；
// 卸载用 POST + body 同理。
type SkillHubHandler struct {
	svc *skillhub.Service
	// cats 可为 nil：分类是可选增强，分类器不可用时页面隐藏分类栏。
	cats *skillhub.Categories
}

func NewSkillHubHandler(svc *skillhub.Service, cats *skillhub.Categories) *SkillHubHandler {
	return &SkillHubHandler{svc: svc, cats: cats}
}

func (h *SkillHubHandler) Register(r *gin.Engine) {
	r.GET("/skillhub/skills", h.List)
	r.GET("/skillhub/categories", h.CategoriesList)
	r.GET("/skillhub/skill/readme", h.Readme)
	r.GET("/skillhub/skill/files", h.Files)
	r.GET("/skillhub/skill/versions", h.Versions)
	r.GET("/skillhub/skill/content", h.FileContent)
	r.GET("/skillhub/installed", h.Installed)
	r.POST("/skillhub/install", h.Install)
	r.POST("/skillhub/uninstall", h.Uninstall)
}

// installTimeout bounds download + unzip + swap. The bundle download alone
// may take up to a minute on a bad day; the rest is local disk work.
const installTimeout = 90 * time.Second

func pageParams(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ = strconv.Atoi(c.Query("pageSize"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	return page, pageSize
}

// slugParam validates the ?slug= query or aborts with a 400.
func slugParam(c *gin.Context) (string, bool) {
	slug := c.Query("slug")
	if !skillhub.ValidFullSlug(slug) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的技能 slug"})
		return "", false
	}
	return slug, true
}

func (h *SkillHubHandler) List(c *gin.Context) {
	page, pageSize := pageParams(c)
	q := c.Query("q")

	if category := c.Query("category"); category != "" {
		if h.cats == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分类不可用"})
			return
		}
		out, err := h.cats.ListByCategory(c.Request.Context(), category, q, page, pageSize)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, out)
		return
	}

	out, err := h.svc.Registry.Catalog(c.Request.Context(), q, page, pageSize)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, out)
}

// CategoriesList 返回各分类的技能数。分类器不可用时报错，前端据此隐藏分类栏，
// 浏览与搜索不受影响。
func (h *SkillHubHandler) CategoriesList(c *gin.Context) {
	if h.cats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "分类不可用"})
		return
	}
	counts, err := h.cats.Counts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"categories": counts})
}

func (h *SkillHubHandler) Readme(c *gin.Context) {
	slug, ok := slugParam(c)
	if !ok {
		return
	}
	content, err := h.svc.Registry.Readme(c.Request.Context(), slug)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"content": content})
}

func (h *SkillHubHandler) Files(c *gin.Context) {
	slug, ok := slugParam(c)
	if !ok {
		return
	}
	files, err := h.svc.Registry.FileList(c.Request.Context(), slug)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, files)
}

func (h *SkillHubHandler) Versions(c *gin.Context) {
	slug, ok := slugParam(c)
	if !ok {
		return
	}
	versions, err := h.svc.Registry.Versions(c.Request.Context(), slug)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"versions": versions})
}

func (h *SkillHubHandler) FileContent(c *gin.Context) {
	slug, ok := slugParam(c)
	if !ok {
		return
	}
	path := c.Query("path")
	// path 只作为 query 参数转发给注册中心（会被编码），无路径注入风险；
	// 长度上限只是挡异常输入。
	if path == "" || len(path) > 512 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的文件路径"})
		return
	}
	content, err := h.svc.Registry.FileContent(c.Request.Context(), slug, path)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"content": content})
}

func (h *SkillHubHandler) Installed(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"installed": h.svc.ListInstalled()})
}

func (h *SkillHubHandler) Install(c *gin.Context) {
	var req struct {
		FullSlug string `json:"fullSlug"`
		Version  string `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !skillhub.ValidFullSlug(req.FullSlug) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的技能 slug"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), installTimeout)
	defer cancel()
	installed, err := h.svc.Install(ctx, req.FullSlug, req.Version)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, installed)
}

func (h *SkillHubHandler) Uninstall(c *gin.Context) {
	var req struct {
		FullSlug string `json:"fullSlug"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !skillhub.ValidFullSlug(req.FullSlug) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的技能 slug"})
		return
	}
	if err := h.svc.Uninstall(req.FullSlug); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"uninstalled": true})
}
