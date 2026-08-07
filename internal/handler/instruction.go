package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/instructions"
)

type InstructionHandler struct {
	store *instructions.Store
}

func NewInstructionHandler(store *instructions.Store) *InstructionHandler {
	return &InstructionHandler{store: store}
}

func (h *InstructionHandler) Register(r *gin.Engine) {
	r.GET("/instructions", h.List)
	r.GET("/instructions/:name", h.Get)
	r.POST("/instructions", h.Create)
	r.PUT("/instructions/:name", h.Update)
	r.DELETE("/instructions/:name", h.Delete)
}

func (h *InstructionHandler) List(c *gin.Context) {
	items, err := h.store.List()
	if err != nil {
		writeInstructionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"instructions": items})
}

func (h *InstructionHandler) Get(c *gin.Context) {
	item, err := h.store.Get(c.Param("name"))
	if err != nil {
		writeInstructionError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *InstructionHandler) Create(c *gin.Context) {
	var req instructions.Instruction
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.store.Create(req); err != nil {
		writeInstructionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, req)
}

func (h *InstructionHandler) Update(c *gin.Context) {
	name := c.Param("name")
	var req instructions.Instruction
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Name == "" {
		req.Name = name
	}
	if err := h.store.Update(name, req); err != nil {
		writeInstructionError(c, err)
		return
	}
	c.JSON(http.StatusOK, req)
}

func (h *InstructionHandler) Delete(c *gin.Context) {
	if err := h.store.Delete(c.Param("name")); err != nil {
		writeInstructionError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func writeInstructionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, instructions.ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, instructions.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, instructions.ErrAlreadyExists):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
