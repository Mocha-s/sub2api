package admin

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// adminVideoTaskSettlementService is the subset of service.VideoTaskSettlementService used here.
type adminVideoTaskSettlementService interface {
	GetPersistedSettlement(ctx context.Context, publicTaskID string) (*service.VideoTaskSettlementSnapshot, error)
	GetSettlementByUsageLogID(ctx context.Context, usageLogID int64) (*service.VideoTaskSettlementSnapshot, error)
	AdminRefundTask(ctx context.Context, publicTaskID string, reason string) (*service.VideoTaskSettlementResult, error)
	AdminConfirmTask(ctx context.Context, publicTaskID string) (*service.VideoTaskSettlementResult, error)
}

// VideoTaskSettlementHandler handles admin settlement operations for video tasks.
type VideoTaskSettlementHandler struct {
	svc adminVideoTaskSettlementService
}

// NewVideoTaskSettlementHandler creates a VideoTaskSettlementHandler.
// svc satisfies adminVideoTaskSettlementService (implemented by *service.VideoTaskSettlementService).
func NewVideoTaskSettlementHandler(svc *service.VideoTaskSettlementService) *VideoTaskSettlementHandler {
	return &VideoTaskSettlementHandler{svc: svc}
}

// videoTaskSettlementResponse is the JSON shape returned to the frontend.
type videoTaskSettlementResponse struct {
	PublicTaskID    string  `json:"public_task_id"`
	State           string  `json:"state"`
	ActualCostUSD   float64 `json:"actual_cost_usd"`
	RefundedCostUSD float64 `json:"refunded_cost_usd"`
	ChargedAt       *string `json:"charged_at,omitempty"`
	RefundedAt      *string `json:"refunded_at,omitempty"`
}

func toSettlementResponse(s *service.VideoTaskSettlementSnapshot) videoTaskSettlementResponse {
	r := videoTaskSettlementResponse{
		PublicTaskID:    s.PublicTaskID,
		State:           string(s.State),
		ActualCostUSD:   s.ActualCostUSD,
		RefundedCostUSD: s.RefundedCostUSD,
	}
	if s.ChargedAt != nil {
		v := s.ChargedAt.UTC().Format("2006-01-02T15:04:05Z")
		r.ChargedAt = &v
	}
	if s.RefundedAt != nil {
		v := s.RefundedAt.UTC().Format("2006-01-02T15:04:05Z")
		r.RefundedAt = &v
	}
	return r
}

// GetByUsageLogID looks up the settlement for the video task linked to a usage log.
// GET /api/v1/admin/video-task-settlements/by-usage/:usage_log_id
//
// The frontend calls this lazily (on button click) so that the hot usage-log
// query path does not need a JOIN to video_task_settlements.
func (h *VideoTaskSettlementHandler) GetByUsageLogID(c *gin.Context) {
	usageLogID, err := strconv.ParseInt(c.Param("usage_log_id"), 10, 64)
	if err != nil || usageLogID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid usage_log_id"})
		return
	}
	snap, err := h.svc.GetSettlementByUsageLogID(c.Request.Context(), usageLogID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if snap == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "settlement not found"})
		return
	}
	response.Success(c, toSettlementResponse(snap))
}

// Refund triggers an admin refund for a charged video task settlement.
// POST /api/v1/admin/video-task-settlements/:task_id/refund
func (h *VideoTaskSettlementHandler) Refund(c *gin.Context) {
	taskID := c.Param("task_id")
	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)
	result, err := h.svc.AdminRefundTask(c.Request.Context(), taskID, body.Reason)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"applied": result != nil && result.Applied})
}

// Confirm confirms a charged video task settlement (repair + mark settled).
// POST /api/v1/admin/video-task-settlements/:task_id/confirm
func (h *VideoTaskSettlementHandler) Confirm(c *gin.Context) {
	taskID := c.Param("task_id")
	result, err := h.svc.AdminConfirmTask(c.Request.Context(), taskID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"applied": result != nil && result.Applied})
}
