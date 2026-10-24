package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type mediaRefundSnapshotRow struct {
	id                 int64
	requestID          string
	apiKeyID           int64
	userID             int64
	accountID          int64
	groupID            sql.NullInt64
	subscriptionID     sql.NullInt64
	platform           string
	billingType        int8
	totalCost          float64
	actualCost         float64
	balanceCost        float64
	subscriptionCost   float64
	apiKeyQuotaCost    float64
	apiKeyRateCost     float64
	accountQuotaCost   float64
	platformQuotaCost  float64
	accountStatsCost   float64
	windowSnapshotJSON []byte
}

type mediaRefundUsageRow struct {
	id            int64
	refundedCost  float64
	refundedTotal float64
	refundStatus  string
}

func (r *usageBillingRepository) RefundMediaUsage(ctx context.Context, cmd *service.MediaUsageRefundCommand) (*service.MediaUsageRefundResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		result, err := r.refundMediaUsageOnce(ctx, cmd)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				attempt = 3
			case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
			}
		}
	}
	if cmd != nil && r != nil && r.db != nil {
		_, persistErr := r.db.ExecContext(context.Background(), `UPDATE media_usage_refund_events SET state='failed',attempts=attempts+1,last_error=$1,updated_at=NOW() WHERE request_id=$2 AND api_key_id=$3 AND scope=$4 AND state<>'refunded'`, errorString(lastErr), strings.TrimSpace(cmd.RequestID), cmd.APIKeyID, strings.TrimSpace(cmd.Scope))
		if persistErr != nil {
			return nil, errors.Join(lastErr, fmt.Errorf("persist media refund retry state: %w", persistErr))
		}
	}
	return nil, lastErr
}

func errorString(err error) string {
	if err == nil {
		return "unknown refund error"
	}
	return err.Error()
}

func (r *usageBillingRepository) refundMediaUsageOnce(ctx context.Context, cmd *service.MediaUsageRefundCommand) (*service.MediaUsageRefundResult, error) {
	if err := cmd.Normalize(); err != nil {
		return nil, err
	}
	missing, ratio, err := service.NormalizeMediaRefundUnits(cmd.RequestedUnits, cmd.DeliveredUnits)
	if err != nil {
		return nil, err
	}
	if missing == 0 {
		return &service.MediaUsageRefundResult{APIKeyID: cmd.APIKeyID}, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("media usage refund repository db is nil")
	}

	snapshot, err := r.ensureMediaRefundEvent(ctx, cmd, ratio)
	if err != nil {
		return nil, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var state string
	var refundActual, refundTotal float64
	var recordedRequested, recordedDelivered int
	var recordedRatio float64
	if err := tx.QueryRowContext(ctx, `SELECT state,refunded_cost,refunded_total_cost,requested_units,delivered_units,refund_ratio FROM media_usage_refund_events WHERE snapshot_id=$1 AND scope=$2 FOR UPDATE`, snapshot.id, cmd.Scope).Scan(&state, &refundActual, &refundTotal, &recordedRequested, &recordedDelivered, &recordedRatio); err != nil {
		return nil, err
	}
	if recordedRequested != cmd.RequestedUnits || recordedDelivered != cmd.DeliveredUnits || math.Abs(recordedRatio-ratio) > 1e-10 {
		return nil, service.ErrMediaRefundRequestConflict
	}
	if state == "refunded" {
		return mediaRefundResult(snapshot, false, refundActual, refundTotal), nil
	}

	if err := reverseMediaSnapshot(ctx, tx, snapshot, ratio); err != nil {
		return nil, err
	}
	refundActual = service.QuantizeUsageBillingAmount(snapshot.actualCost * ratio)
	refundTotal = service.QuantizeUsageBillingAmount(snapshot.totalCost * ratio)
	usage, err := findMediaRefundUsage(ctx, tx, snapshot.requestID, snapshot.apiKeyID)
	if err != nil {
		return nil, err
	}
	newRefundedActual := usage.refundedCost + refundActual
	newRefundedTotal := usage.refundedTotal + refundTotal
	if newRefundedActual > snapshot.actualCost+1e-8 || newRefundedTotal > snapshot.totalCost+1e-8 {
		return nil, errors.New("media refund exceeds original charge")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE usage_logs SET refunded_cost=$1,refunded_total_cost=$2,refund_reason=$3,refunded_at=NOW(),refund_status='refunded' WHERE id=$4`, newRefundedActual, newRefundedTotal, strings.TrimSpace(cmd.Reason), usage.id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_usage_refund_events SET state='refunded',refunded_cost=$1,refunded_total_cost=$2,last_error=NULL,completed_at=NOW(),updated_at=NOW() WHERE snapshot_id=$3 AND scope=$4`, refundActual, refundTotal, snapshot.id, cmd.Scope); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return mediaRefundResult(snapshot, true, refundActual, refundTotal), nil
}

func (r *usageBillingRepository) ensureMediaRefundEvent(ctx context.Context, cmd *service.MediaUsageRefundCommand, ratio float64) (*mediaRefundSnapshotRow, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot := &mediaRefundSnapshotRow{}
	if err := tx.QueryRowContext(ctx, `SELECT id,request_id,api_key_id,user_id,account_id,group_id,subscription_id,platform,billing_type,total_cost,actual_cost,balance_cost,subscription_cost,api_key_quota_cost,api_key_rate_limit_cost,account_quota_cost,platform_quota_cost,account_stats_cost,window_snapshot FROM media_usage_refund_snapshots WHERE request_id=$1 AND api_key_id=$2 FOR UPDATE`, cmd.RequestID, cmd.APIKeyID).Scan(&snapshot.id, &snapshot.requestID, &snapshot.apiKeyID, &snapshot.userID, &snapshot.accountID, &snapshot.groupID, &snapshot.subscriptionID, &snapshot.platform, &snapshot.billingType, &snapshot.totalCost, &snapshot.actualCost, &snapshot.balanceCost, &snapshot.subscriptionCost, &snapshot.apiKeyQuotaCost, &snapshot.apiKeyRateCost, &snapshot.accountQuotaCost, &snapshot.platformQuotaCost, &snapshot.accountStatsCost, &snapshot.windowSnapshotJSON); err != nil {
		return nil, err
	}
	usage, err := findMediaRefundUsage(ctx, tx, cmd.RequestID, cmd.APIKeyID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE usage_logs SET refund_status=CASE WHEN refund_status='refunded' THEN refund_status ELSE 'pending' END WHERE id=$1`, usage.id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_usage_refund_events (snapshot_id,request_id,api_key_id,scope,requested_units,delivered_units,refund_ratio,reason) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (request_id,api_key_id,scope) DO NOTHING`, snapshot.id, cmd.RequestID, cmd.APIKeyID, cmd.Scope, cmd.RequestedUnits, cmd.DeliveredUnits, ratio, strings.TrimSpace(cmd.Reason)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (r *usageBillingRepository) ClaimPendingMediaRefunds(ctx context.Context, limit int) ([]service.MediaUsageRefundCommand, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("media usage refund repository db is nil")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx, `SELECT request_id,api_key_id,scope,requested_units,delivered_units,reason FROM media_usage_refund_events WHERE state IN ('pending','failed') ORDER BY updated_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var commands []service.MediaUsageRefundCommand
	for rows.Next() {
		var cmd service.MediaUsageRefundCommand
		if err := rows.Scan(&cmd.RequestID, &cmd.APIKeyID, &cmd.Scope, &cmd.RequestedUnits, &cmd.DeliveredUnits, &cmd.Reason); err != nil {
			return nil, err
		}
		commands = append(commands, cmd)
	}
	return commands, rows.Err()
}

func findMediaRefundUsage(ctx context.Context, tx *sql.Tx, requestID string, apiKeyID int64) (*mediaRefundUsageRow, error) {
	usage := &mediaRefundUsageRow{}
	err := tx.QueryRowContext(ctx, `SELECT id,COALESCE(refunded_cost,0),COALESCE(refunded_total_cost,0),COALESCE(refund_status,'none') FROM usage_logs WHERE request_id=$1 AND api_key_id=$2 ORDER BY id DESC LIMIT 1 FOR UPDATE`, requestID, apiKeyID).Scan(&usage.id, &usage.refundedCost, &usage.refundedTotal, &usage.refundStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("media usage log not found: request_id=%s api_key_id=%d", requestID, apiKeyID)
	}
	return usage, err
}

func mediaRefundResult(s *mediaRefundSnapshotRow, applied bool, actual, total float64) *service.MediaUsageRefundResult {
	return &service.MediaUsageRefundResult{Applied: applied, UserID: s.userID, APIKeyID: s.apiKeyID, GroupID: mediaNullableInt64(s.groupID), SubscriptionID: mediaNullableInt64(s.subscriptionID), Platform: s.platform, RefundedCost: actual, RefundedTotalCost: total}
}

func mediaNullableInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}

func reverseMediaSnapshot(ctx context.Context, tx *sql.Tx, s *mediaRefundSnapshotRow, ratio float64) error {
	amount := func(v float64) float64 { return service.QuantizeUsageBillingAmount(v * ratio) }
	var windows map[string]string
	if err := json.Unmarshal(s.windowSnapshotJSON, &windows); err != nil {
		return err
	}
	if s.billingType == service.BillingTypeSubscription && s.subscriptionID.Valid {
		res, err := tx.ExecContext(ctx, `UPDATE user_subscriptions SET
			daily_usage_usd=CASE WHEN $2<>'' AND daily_window_start IS NOT DISTINCT FROM $2::timestamptz THEN GREATEST(daily_usage_usd-$1,0) ELSE daily_usage_usd END,
			weekly_usage_usd=CASE WHEN $3<>'' AND weekly_window_start IS NOT DISTINCT FROM $3::timestamptz THEN GREATEST(weekly_usage_usd-$1,0) ELSE weekly_usage_usd END,
			monthly_usage_usd=CASE WHEN $4<>'' AND monthly_window_start IS NOT DISTINCT FROM $4::timestamptz THEN GREATEST(monthly_usage_usd-$1,0) ELSE monthly_usage_usd END,
			updated_at=NOW() WHERE id=$5 AND deleted_at IS NULL`, amount(s.subscriptionCost), windows["subscription_daily"], windows["subscription_weekly"], windows["subscription_monthly"], s.subscriptionID.Int64)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "refund subscription usage"); err != nil {
			return err
		}
	} else if s.balanceCost > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance+$1,updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL`, amount(s.balanceCost), s.userID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "refund balance"); err != nil {
			return err
		}
	}
	if s.apiKeyQuotaCost > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE api_keys SET quota_used=GREATEST(quota_used-$1,0),status=CASE WHEN status=$3 AND quota>0 AND GREATEST(quota_used-$1,0)<quota AND (expires_at IS NULL OR expires_at>NOW()) THEN $4 ELSE status END,updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL`, amount(s.apiKeyQuotaCost), s.apiKeyID, service.StatusAPIKeyQuotaExhausted, service.StatusAPIKeyActive)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "refund api key quota"); err != nil {
			return err
		}
	}
	if s.apiKeyRateCost > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE api_keys SET usage_5h=CASE WHEN $2<>'' AND window_5h_start IS NOT DISTINCT FROM $2::timestamptz THEN GREATEST(usage_5h-$1,0) ELSE usage_5h END,usage_1d=CASE WHEN $3<>'' AND window_1d_start IS NOT DISTINCT FROM $3::timestamptz THEN GREATEST(usage_1d-$1,0) ELSE usage_1d END,usage_7d=CASE WHEN $4<>'' AND window_7d_start IS NOT DISTINCT FROM $4::timestamptz THEN GREATEST(usage_7d-$1,0) ELSE usage_7d END,updated_at=NOW() WHERE id=$5 AND deleted_at IS NULL`, amount(s.apiKeyRateCost), windows["api_key_5h"], windows["api_key_1d"], windows["api_key_7d"], s.apiKeyID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "refund api key rate limit"); err != nil {
			return err
		}
	}
	// Account/provider-side quota and cost are intentionally retained: only
	// customer-facing billing effects are compensated by this media refund.
	if s.platformQuotaCost > 0 && strings.TrimSpace(s.platform) != "" {
		res, err := tx.ExecContext(ctx, `UPDATE user_platform_quotas SET daily_usage_usd=CASE WHEN $2<>'' AND daily_window_start IS NOT DISTINCT FROM $2::timestamptz THEN GREATEST(daily_usage_usd-$1,0) ELSE daily_usage_usd END,weekly_usage_usd=CASE WHEN $3<>'' AND weekly_window_start IS NOT DISTINCT FROM $3::timestamptz THEN GREATEST(weekly_usage_usd-$1,0) ELSE weekly_usage_usd END,monthly_usage_usd=CASE WHEN $4<>'' AND monthly_window_start IS NOT DISTINCT FROM $4::timestamptz THEN GREATEST(monthly_usage_usd-$1,0) ELSE monthly_usage_usd END,revision=revision+1,updated_at=NOW() WHERE user_id=$5 AND platform=$6 AND deleted_at IS NULL`, amount(s.platformQuotaCost), windows["platform_daily"], windows["platform_weekly"], windows["platform_monthly"], s.userID, s.platform)
		if err != nil {
			return err
		}
		if affected, err := res.RowsAffected(); err != nil {
			return err
		} else if affected > 1 {
			return errors.New("refund platform quota updated multiple rows")
		}
	}
	return nil
}
