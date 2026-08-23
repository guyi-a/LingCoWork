package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/memory"
	"github.com/guyi-a/Interview-Agent/internal/service"
)

type MemoryHandler struct {
	svc *service.MemoryService
}

func NewMemoryHandler(svc *service.MemoryService) *MemoryHandler {
	return &MemoryHandler{svc: svc}
}

// 项目级的 PUT 是唯一一个能写工作区文件的接口，而且只认 memory.md 这一个路径
// （路径由 memory.ProjectPath 算，前端传不了别的）。工作区其余文件在 UI 上仍然
// 只读 —— 开放通用编辑是另一个功能，不该顺着记忆一起放出去。
func (h *MemoryHandler) Register(r *gin.Engine) {
	r.GET("/memory/user", h.GetUser)
	r.PUT("/memory/user", h.PutUser)
	r.GET("/conversations/:id/memory", h.GetProject)
	r.PUT("/conversations/:id/memory", h.PutProject)
}

type memoryWriteRequest struct {
	Content string `json:"content"`
	// Hash 是读取时拿到的那个。缺省不代表"强制覆盖"：Store 会拿它跟磁盘现状
	// 比，空串只有在文件也为空时才对得上。
	Hash string `json:"hash"`
}

func (h *MemoryHandler) GetUser(c *gin.Context) {
	res, err := h.svc.ReadUser()
	if err != nil {
		writeMemoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *MemoryHandler) PutUser(c *gin.Context) {
	var req memoryWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := h.svc.WriteUser(req.Content, req.Hash)
	if err != nil {
		writeMemoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *MemoryHandler) GetProject(c *gin.Context) {
	res, err := h.svc.ReadProject(c.Request.Context(), c.Param("id"), c.Query("project_id"))
	if err != nil {
		writeMemoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *MemoryHandler) PutProject(c *gin.Context) {
	var req memoryWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := h.svc.WriteProject(c.Request.Context(), c.Param("id"), c.Query("project_id"), req.Content, req.Hash)
	if err != nil {
		writeMemoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func writeMemoryError(c *gin.Context, err error) {
	switch {
	// 409 而不是 400：内容本身没问题，是它基于的版本过期了，前端该重新加载再
	// 提交，而不是改内容重试。
	case errors.Is(err, memory.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "conflict"})
	case errors.Is(err, memory.ErrTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error(), "code": "too_large", "limit": memory.MaxBytes})
	case errors.Is(err, memory.ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrNoWorkspace):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error(), "code": "no_workspace"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
