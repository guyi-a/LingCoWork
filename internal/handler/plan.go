package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/hitl"
	"github.com/guyi-a/Interview-Agent/internal/service"
	"github.com/guyi-a/Interview-Agent/internal/workplan"
)

type PlanHandler struct {
	plans *workplan.Service
	chat  *service.ChatService
}

func NewPlanHandler(plans *workplan.Service, chat *service.ChatService) *PlanHandler {
	return &PlanHandler{plans: plans, chat: chat}
}

func (h *PlanHandler) Register(r *gin.Engine) {
	r.GET("/conversations/:id/plans", h.List)
	r.GET("/conversations/:id/plans/latest", h.Latest)
	r.PUT("/conversations/:id/plans/:plan_id", h.Edit)
	r.POST("/conversations/:id/plans/:plan_id/start", h.Start)
	r.POST("/conversations/:id/plans/:plan_id/cancel", h.Cancel)
}

func (h *PlanHandler) List(c *gin.Context) {
	plans, err := h.plans.List(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plans": plans})
}

func (h *PlanHandler) Latest(c *gin.Context) {
	plan, err := h.plans.Latest(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plan": plan})
}

type planEditRequest struct {
	Revision  int             `json:"revision" binding:"required"`
	Overview  string          `json:"overview"`
	BodyMD    string          `json:"body_md"`
	Items     []workplan.Item `json:"items" binding:"required"`
	Interrupt string          `json:"interrupt_id,omitempty"`
}

func (h *PlanHandler) Edit(c *gin.Context) {
	var req planEditRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	plan, err := h.plans.EditDraft(
		c.Request.Context(),
		c.Param("id"),
		c.Param("plan_id"),
		req.Revision,
		req.Overview,
		req.BodyMD,
		req.Items,
	)
	if h.writePlanError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"plan": plan})
}

func (h *PlanHandler) Start(c *gin.Context) {
	var req planEditRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Interrupt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "interrupt_id is required"})
		return
	}
	if !h.chat.HasPendingInterrupt(c.Param("id"), req.Interrupt) {
		c.JSON(http.StatusNotFound, gin.H{"error": "plan interrupt is no longer pending"})
		return
	}
	plan, err := h.plans.Activate(
		c.Request.Context(),
		c.Param("id"),
		c.Param("plan_id"),
		req.Revision,
		req.Overview,
		req.BodyMD,
		req.Items,
	)
	if h.writePlanError(c, err) {
		return
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	found, resumed, err := h.chat.ResumePlan(
		c.Param("id"),
		req.Interrupt,
		hitl.PlanDecision{PlanJSON: string(raw)},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "plan interrupt is no longer pending"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"plan": plan, "resumed": resumed})
}

type planCancelRequest struct {
	Revision  int    `json:"revision" binding:"required"`
	Interrupt string `json:"interrupt_id" binding:"required"`
}

func (h *PlanHandler) Cancel(c *gin.Context) {
	var req planCancelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !h.chat.HasPendingInterrupt(c.Param("id"), req.Interrupt) {
		c.JSON(http.StatusNotFound, gin.H{"error": "plan interrupt is no longer pending"})
		return
	}
	plan, err := h.plans.Cancel(
		c.Request.Context(), c.Param("id"), c.Param("plan_id"), req.Revision,
	)
	if h.writePlanError(c, err) {
		return
	}
	found, resumed, err := h.chat.ResumePlan(
		c.Param("id"), req.Interrupt, hitl.PlanDecision{Cancelled: true},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "plan interrupt is no longer pending"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"plan": plan, "resumed": resumed})
}

func (h *PlanHandler) writePlanError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, workplan.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, workplan.ErrConflict):
		latest, latestErr := h.plans.Get(c.Request.Context(), c.Param("id"), c.Param("plan_id"))
		if latestErr != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "plan": latest})
		}
	case errors.Is(err, workplan.ErrNotEditable),
		errors.Is(err, workplan.ErrTooManyInProgress):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
	return true
}
