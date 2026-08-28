package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/service"
)

type ProjectHandler struct {
	svc *service.ProjectService
}

func NewProjectHandler(svc *service.ProjectService) *ProjectHandler {
	return &ProjectHandler{svc: svc}
}

func (h *ProjectHandler) Register(r *gin.Engine) {
	r.GET("/projects", h.List)
	r.POST("/projects", h.Create)
	r.GET("/projects/:id", h.Get)
	r.PATCH("/projects/:id", h.Update)
	r.DELETE("/projects/:id", h.Delete)
	r.POST("/projects/:id/open", h.Open)
}

type projectItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	UpdatedAt string `json:"updated_at"`
}

func (h *ProjectHandler) List(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]projectItem, 0, len(items))
	for _, p := range items {
		out = append(out, projectItem{
			ID:        p.ID,
			Name:      p.Name,
			Workspace: p.Workspace,
			UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"projects": out})
}

type createProjectRequest struct {
	Path string `json:"path" binding:"required"`
	Name string `json:"name"`
}

func (h *ProjectHandler) Create(c *gin.Context) {
	var req createProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	p, created, err := h.svc.OpenOrCreateFromPath(c.Request.Context(), req.Path, req.Name)
	if err != nil {
		if errors.Is(err, service.ErrInvalidWorkspace) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{
		"project": projectItem{
			ID:        p.ID,
			Name:      p.Name,
			Workspace: p.Workspace,
			UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
		},
		"created": created,
	})
}

func (h *ProjectHandler) Get(c *gin.Context) {
	id := c.Param("id")
	p, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if p == nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, projectItem{
		ID:        p.ID,
		Name:      p.Name,
		Workspace: p.Workspace,
		UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
	})
}

type updateProjectRequest struct {
	Name string `json:"name" binding:"required"`
}

func (h *ProjectHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req updateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.Rename(c.Request.Context(), id, req.Name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *ProjectHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *ProjectHandler) Open(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.OpenInFinder(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
