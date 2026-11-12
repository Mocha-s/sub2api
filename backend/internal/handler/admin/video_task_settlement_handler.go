package admin

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// adminVideoTaskSettlementService is the subset of service.VideoTaskSettlementService used here.
type adminVideoTaskSettlementService interface {
	AdminRefundTask(ctx context.Context, publicTaskID string, reason string) (*service.VideoTaskSettlementResult, error)
	AdminConfirmTask(ctx context.Context, publicTaskID string) (*service.VideoTaskSettlementResult, error)
	AdminReleaseTask(ctx context.Context, publicTaskID string) (*service.VideoTaskSettlementResult, error)
}

// VideoTaskSettlementHandler handles admin settlement operations for video tasks.
type VideoTaskSettlementHandler struct {
	svc adminVideoTaskSettlementService
}

// NewVideoTaskSettlementHandler creates a VideoTaskSettlementHandler.
func NewVideoTaskSettlementHandler(svc *service.VideoTaskSettlementService) *VideoTaskSettlementHandler {
	return &VideoTaskSettlementHandler{svc: svc}
}

func settlementApplied(c *gin.Context, result *service.VideoTaskSettlementResult, err error) {
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"applied": result != nil && result.Applied})
}

// Refund refunds a charged settlement.
// POST /api/v1/admin/video-task-settlements/:task_id/refund
func (h *VideoTaskSettlementHandler) Refund(c *gin.Context) {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)
	result, err := h.svc.AdminRefundTask(c.Request.Context(), c.Param("task_id"), body.Reason)
	settlementApplied(c, result, err)
}

// Confirm confirms a charged settlement (repair + mark settled).
// POST /api/v1/admin/video-task-settlements/:task_id/confirm
func (h *VideoTaskSettlementHandler) Confirm(c *gin.Context) {
	result, err := h.svc.AdminConfirmTask(c.Request.Context(), c.Param("task_id"))
	settlementApplied(c, result, err)
}

// Release returns a still-reserved hold to the user.
// POST /api/v1/admin/video-task-settlements/:task_id/release
func (h *VideoTaskSettlementHandler) Release(c *gin.Context) {
	result, err := h.svc.AdminReleaseTask(c.Request.Context(), c.Param("task_id"))
	settlementApplied(c, result, err)
}
