package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

var ErrMediaRefundRequestRequired = errors.New("media refund request is required")
var ErrMediaRefundRequestIDRequired = errors.New("media refund request id is required")
var ErrMediaRefundUnitsInvalid = errors.New("media refund units are invalid")
var ErrMediaRefundRequestConflict = errors.New("media refund request conflicts with existing event")

type MediaUsageRefundCommand struct {
	RequestID      string
	APIKeyID       int64
	Scope          string
	RequestedUnits int
	DeliveredUnits int
	Reason         string
}

type MediaUsageRefundResult struct {
	Applied           bool
	UserID            int64
	APIKeyID          int64
	GroupID           *int64
	SubscriptionID    *int64
	Platform          string
	RefundedCost      float64
	RefundedTotalCost float64
}

type MediaUsageRefundRepository interface {
	RefundMediaUsage(ctx context.Context, cmd *MediaUsageRefundCommand) (*MediaUsageRefundResult, error)
	ClaimPendingMediaRefunds(ctx context.Context, limit int) ([]MediaUsageRefundCommand, error)
}

type MediaUsageRefundWorker struct {
	repo   MediaUsageRefundRepository
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
}

func NewMediaUsageRefundWorker(repo MediaUsageRefundRepository) *MediaUsageRefundWorker {
	return &MediaUsageRefundWorker{repo: repo}
}

func (w *MediaUsageRefundWorker) Start() {
	if w == nil || w.repo == nil {
		return
	}
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel, w.done = cancel, make(chan struct{})
	done := w.done
	w.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			w.process(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (w *MediaUsageRefundWorker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.cancel, w.done = nil, nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (w *MediaUsageRefundWorker) process(ctx context.Context) {
	commands, err := w.repo.ClaimPendingMediaRefunds(ctx, 20)
	if err != nil {
		slog.Warn("media_refund_reconcile_claim_failed", "error", err)
		return
	}
	for i := range commands {
		if _, err := w.repo.RefundMediaUsage(ctx, &commands[i]); err != nil {
			slog.Error("media_refund_reconcile_failed", "request_id", commands[i].RequestID, "api_key_id", commands[i].APIKeyID, "scope", commands[i].Scope, "error", err)
		}
	}
}

func invalidateMediaRefundCaches(ctx context.Context, cache *BillingCacheService, result *MediaUsageRefundResult) {
	if cache == nil || result == nil || !result.Applied {
		return
	}
	if result.UserID > 0 {
		_ = cache.InvalidateUserBalance(ctx, result.UserID)
	}
	if result.SubscriptionID != nil && result.GroupID != nil {
		_ = cache.InvalidateSubscription(ctx, result.UserID, *result.GroupID)
	}
	if result.APIKeyID > 0 {
		_ = cache.InvalidateAPIKeyRateLimit(ctx, result.APIKeyID)
	}
	if result.UserID > 0 && result.Platform != "" {
		_ = cache.InvalidateUserPlatformQuota(ctx, result.UserID, result.Platform)
	}
}

func (c *MediaUsageRefundCommand) Normalize() error {
	if c == nil {
		return ErrMediaRefundRequestRequired
	}
	c.RequestID = strings.TrimSpace(c.RequestID)
	c.Scope = strings.TrimSpace(c.Scope)
	c.Reason = strings.TrimSpace(c.Reason)
	if c.RequestID == "" {
		return ErrMediaRefundRequestIDRequired
	}
	if c.Scope == "" {
		return fmt.Errorf("%w: scope is required", ErrMediaRefundRequestRequired)
	}
	return nil
}

func NormalizeMediaRefundUnits(requested, delivered int) (missing int, ratio float64, err error) {
	if requested <= 0 || delivered < 0 {
		return 0, 0, ErrMediaRefundUnitsInvalid
	}
	if delivered > requested {
		delivered = requested
	}
	missing = requested - delivered
	if missing == 0 {
		return 0, 0, nil
	}
	return missing, float64(missing) / float64(requested), nil
}
