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
	refundOK       bool
	confirmOK      bool
	releaseApplied bool
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
func (s *settlementHandlerStub) AdminReleaseTask(_ context.Context, _ string) (*service.VideoTaskSettlementResult, error) {
	return &service.VideoTaskSettlementResult{Applied: s.releaseApplied}, nil
}

func settlementPost(t *testing.T, h *VideoTaskSettlementHandler, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/video-task-settlements/:task_id/refund", h.Refund)
	r.POST("/video-task-settlements/:task_id/confirm", h.Confirm)
	r.POST("/video-task-settlements/:task_id/release", h.Release)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/video-task-settlements/vtask_abc/"+action, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestVideoTaskSettlementHandler_Refund(t *testing.T) {
	w := settlementPost(t, &VideoTaskSettlementHandler{svc: &settlementHandlerStub{refundOK: true}}, "refund", `{"reason":"admin manual refund"}`)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestVideoTaskSettlementHandler_Confirm(t *testing.T) {
	w := settlementPost(t, &VideoTaskSettlementHandler{svc: &settlementHandlerStub{confirmOK: true}}, "confirm", "")
	require.Equal(t, http.StatusOK, w.Code)
}

func TestVideoTaskSettlementHandler_ReleaseReportsApplied(t *testing.T) {
	for _, applied := range []bool{true, false} {
		w := settlementPost(t, &VideoTaskSettlementHandler{svc: &settlementHandlerStub{releaseApplied: applied}}, "release", "")
		require.Equal(t, http.StatusOK, w.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, applied, body["data"].(map[string]any)["applied"])
	}
}
