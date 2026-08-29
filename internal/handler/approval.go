package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/hitl"
	"github.com/guyi-a/Interview-Agent/internal/service"
)

// ApprovalHandler mediates one HTTP round-trip: the frontend POSTs the user's
// approve/deny decision for a paused tool call, we hand it to ChatService
// which fires runner.ResumeWithParams — no response body beyond OK/Not-Found;
// the actual continuation streams over the existing SSE connection the
// frontend is already reading.
type ApprovalHandler struct {
	chat *service.ChatService
}

func NewApprovalHandler(chat *service.ChatService) *ApprovalHandler {
	return &ApprovalHandler{chat: chat}
}

func (h *ApprovalHandler) Register(r *gin.Engine) {
	r.GET("/conversations/:id/approvals/pending", h.Pending)
	r.POST("/conversations/:id/approvals/:interrupt_id", h.Decide)
	// ask_user 走独立 POST，因为 body 结构（answers）跟 approval 的 decision
	// 不同，分开更稳；GET pending 端点合并输出两类（每项带 kind 字段）。
	r.POST("/conversations/:id/questions/:interrupt_id", h.AnswerQuestion)
	// Mode routes deliberately live OUTSIDE /approvals/ to avoid the
	// POST /approvals/:interrupt_id catch-all — otherwise POST .../mode
	// would be routed to Decide with interrupt_id="mode".
	r.GET("/conversations/:id/approval-mode", h.GetMode)
	r.POST("/conversations/:id/approval-mode", h.SetMode)
}

type pendingApprovalItem struct {
	Kind          string `json:"kind"` // approval | question
	InterruptID   string `json:"interrupt_id"`
	CallID        string `json:"call_id,omitempty"`
	Tool          string `json:"tool,omitempty"`           // 仅 kind=approval
	ArgsJSON      string `json:"args_json,omitempty"`      // kind=approval：工具参数 JSON
	EffectJSON    string `json:"effect_json,omitempty"`    // kind=approval：审批依据的 effect，老数据为空
	QuestionsJSON string `json:"questions_json,omitempty"` // kind=question：[]hitl.Question 的 JSON
}

func (h *ApprovalHandler) Pending(c *gin.Context) {
	convID := c.Param("id")
	items := h.chat.PendingApprovals(convID)
	out := make([]pendingApprovalItem, 0, len(items))
	for _, it := range items {
		row := pendingApprovalItem{
			Kind:        string(it.Kind),
			InterruptID: it.InterruptID,
			CallID:      it.CallID,
		}
		if row.Kind == "" {
			row.Kind = string(hitl.KindApproval)
		}
		switch hitl.PendingKind(row.Kind) {
		case hitl.KindQuestion:
			row.QuestionsJSON = it.Args
		default:
			row.Tool = it.Tool
			row.ArgsJSON = it.Args
			row.EffectJSON = it.EffectJSON
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"approvals": out})
}

type approvalRequest struct {
	// Decision is "approve" or "deny". Any other value is a 400.
	Decision string `json:"decision" binding:"required"`
	// Reason is optional; only meaningful when Decision == "deny". Surfaced
	// back to the model so it can adjust rather than silently retry.
	Reason string `json:"reason"`
}

func (h *ApprovalHandler) Decide(c *gin.Context) {
	convID := c.Param("id")
	interruptID := c.Param("interrupt_id")

	var req approvalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	dec := approval.Decision{}
	switch req.Decision {
	case "approve":
		dec.Approved = true
	case "deny":
		dec.Approved = false
		dec.Reason = req.Reason
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": `decision must be "approve" or "deny"`})
		return
	}

	found, resumed, err := h.chat.Resume(convID, interruptID, dec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		// Stale approval — either the user clicked twice or the run was
		// cancelled before we got here. 404 lets the frontend clear its
		// stored pending card without treating this as a real error.
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"resumed": resumed})
}

// answerQuestionRequest 是 ask_user 恢复时的 body 契约。cancelled=true 时
// answers 允许为空，服务端会给工具体一个 Cancelled 标记的 Answers；否则
// answers 里每条按 question_id 关联 UI 侧的答复。
type answerQuestionRequest struct {
	Cancelled bool `json:"cancelled"`
	Answers   []struct {
		QuestionID string   `json:"question_id"`
		Selected   []string `json:"selected,omitempty"`
		Custom     string   `json:"custom,omitempty"`
	} `json:"answers,omitempty"`
}

// AnswerQuestion 接收 ask_user 的用户回复，走跟 approval 一样的 checkpoint
// resume 通道恢复 run，但 payload 是 hitl.Answers。
func (h *ApprovalHandler) AnswerQuestion(c *gin.Context) {
	convID := c.Param("id")
	interruptID := c.Param("interrupt_id")

	var req answerQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	payload := hitl.Answers{Cancelled: req.Cancelled}
	if !req.Cancelled {
		payload.Items = make([]hitl.Answer, 0, len(req.Answers))
		for _, a := range req.Answers {
			payload.Items = append(payload.Items, hitl.Answer{
				QuestionID: a.QuestionID,
				Selected:   a.Selected,
				Custom:     a.Custom,
			})
		}
	}

	found, resumed, err := h.chat.ResumeQuestion(convID, interruptID, payload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		// 已被消费 / run 被 cancel —— 前端可据此清 pending 卡。
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"resumed": resumed})
}

// GetMode returns the current approval mode for a conversation. Fresh
// conversations (and conversations that outlived a server restart) report
// "default" — the mode store is in-memory by design.
func (h *ApprovalHandler) GetMode(c *gin.Context) {
	convID := c.Param("id")
	m := h.chat.GetApprovalMode(convID)
	c.JSON(http.StatusOK, gin.H{"mode": string(m)})
}

type approvalModeRequest struct {
	Mode string `json:"mode" binding:"required"`
}

// SetMode changes the approval mode. Any unknown mode returns 400 without
// touching store state — the mode set is closed (default / auto / full_access).
// The change takes effect immediately for subsequent tool calls; pending
// approvals already awaiting a user decision are NOT auto-approved by a
// switch to full_access — the user still has to answer them.
func (h *ApprovalHandler) SetMode(c *gin.Context) {
	convID := c.Param("id")
	var req approvalModeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.chat.SetApprovalMode(convID, approval.Mode(req.Mode)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
