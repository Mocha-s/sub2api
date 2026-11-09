//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// settlementHandlerStub implements adminVideoTaskSettlementService for tests.
type settlementHandlerStub struct {
	snapshot  *service.VideoTaskSettlementSnapshot
	err       error
	refundOK  bool
	confirmOK bool
}

func (s *settlementHandlerStub) GetPersistedSettlement(_ context.Context, _ string) (*service.VideoTaskSettlementSnapshot, error) {
	return s.snapshot, s.err
}
func (s *settlementHandlerStub) GetSettlementByUsageLogID(_ context.Context, _ int64) (*service.VideoTaskSettlementSnapshot, error) {
	return s.snapshot, s.err
}
func (s *settlementHandlerStub) AdminRefundTask(_ context.Context, _ string, _ string) (*service.VideoTaskSettlementResult, error) {
	if !s.refundOK {
		return nil, service.ErrVideoTaskSettlementStateConflict
	}
	return &service.VideoTaskSettlementResult{Applied: true}, nil
}
func (s *settlementHandlerStub) AdminConfirmTask(_ context.Context, _ string) (*service.VideoTaskSettlementResult, error) {
	if !s.confirmOK {
		return nil, service.ErrVideoTaskSettlementStateConflict
	}
	return &service.VideoTaskSettlementResult{Applied: true}, nil
}

func newSettlementHandlerForTest(svc adminVideoTaskSettlementService) *VideoTaskSettlementHandler {
	return &VideoTaskSettlementHandler{svc: svc}
}

func TestVideoTaskSettlementHandler_GetByUsageLogID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	snap := &service.VideoTaskSettlementSnapshot{
		PublicTaskID:  "vtask_abc",
		State:         service.VideoTaskSettlementCharged,
		ActualCostUSD: 0.56,
	}
	h := newSettlementHandlerForTest(&settlementHandlerStub{snapshot: snap})

	r := gin.New()
	r.GET("/video-task-settlements/by-usage/:usage_log_id", h.GetByUsageLogID)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/video-task-settlements/by-usage/123", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	data := body["data"].(map[string]any)
	require.Equal(t, "vtask_abc", data["public_task_id"])
}

func TestVideoTaskSettlementHandler_Refund(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newSettlementHandlerForTest(&settlementHandlerStub{refundOK: true})

	r := gin.New()
	r.POST("/video-task-settlements/:task_id/refund", h.Refund)

	w := httptest.NewRecorder()
	body := strings.NewReader(`{"reason":"admin manual refund"}`)
	req, _ := http.NewRequest(http.MethodPost, "/video-task-settlements/vtask_abc/refund", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestVideoTaskSettlementHandler_Confirm(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newSettlementHandlerForTest(&settlementHandlerStub{confirmOK: true})

	r := gin.New()
	r.POST("/video-task-settlements/:task_id/confirm", h.Confirm)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/video-task-settlements/vtask_abc/confirm", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}
