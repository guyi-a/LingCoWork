package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/validation"
)

type ProblemsHandler struct {
	validations *validation.Service
}

func NewProblemsHandler(validations *validation.Service) *ProblemsHandler {
	return &ProblemsHandler{validations: validations}
}

func (h *ProblemsHandler) Register(r *gin.Engine) {
	r.GET("/conversations/:id/workspace/problems", h.List)
}

func (h *ProblemsHandler) List(c *gin.Context) {
	result, err := h.validations.ListProblems(
		c.Request.Context(), c.Param("id"), c.Query("scope"),
	)
	if err != nil {
		if errors.Is(err, validation.ErrInvalidScope) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}
