package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

type videoTaskSettlementRepository struct{ db *sql.DB }

func NewVideoTaskSettlementRepository(_ *dbent.Client, db *sql.DB) service.VideoTaskSettlementRepository {
	return &videoTaskSettlementRepository{db: db}
}

type settlementTaskRow struct {
	id, userID, apiKeyID, groupID, accountID int64
	channelID, subscriptionID, usageLogID    sql.NullInt64
	publicID, platform, status               string
	providerEvidence                         bool
}

type settlementRow struct {
	id, userID, apiKeyID, groupID, accountID int64
	channelID, subscriptionID, usageLogID    sql.NullInt64
	chargeID, state                          string
	platform                                 string
	billingType                              int8
	gross, actual, accountCost, refunded     float64
	pricingJSON, effectsJSON, appliedJSON    []byte
	reservedAt, chargedAt                    sql.NullTime
	releasedAt, refundedAt                   sql.NullTime
	createdAt, updatedAt                     time.Time
}

func (r *videoTaskSettlementRepository) Reserve(ctx context.Context, cmd *service.VideoTaskSettlementReserveCommand) (*service.VideoTaskSettlementResult, error) {
	if cmd == nil {
		return nil, service.ErrVideoTaskSettlementCommandRequired
	}
	cmd.PublicTaskID = service.NormalizeVideoTaskPublicID(cmd.PublicTaskID)
	grossCost, err := service.NormalizeVideoTaskPricingAmount(cmd.GrossCostUSD)
	if err != nil {
		return nil, err
	}
	accountCost, err := service.NormalizeVideoTaskSettlementAmount(cmd.AccountCostUSD)
	if err != nil {
		return nil, err
	}
	effects, err := cmd.Effects.Normalize()
	if err != nil {
		return nil, err
	}
	actualCost, err := service.NormalizeVideoTaskSettlementAmount(cmd.ActualCostUSD)
	if err != nil {
		return nil, err
	}
	if cmd.Admission == nil && actualCost == 0 {
		actualCost, err = service.NormalizeVideoTaskSettlementAmount(grossCost)
		if err != nil {
			return nil, err
		}
	}
	cmd.GrossCostUSD, cmd.ActualCostUSD, cmd.AccountCostUSD, cmd.Effects = grossCost, actualCost, accountCost, effects
	if cmd.BillingType != service.BillingTypeBalance && cmd.BillingType != service.BillingTypeSubscription {
		return nil, fmt.Errorf("unsupported video task billing type %d", cmd.BillingType)
	}
	return r.inTx(ctx, func(tx *sql.Tx) (*service.VideoTaskSettlementResult, error) {
		task, err := lockSettlementTask(ctx, tx, cmd.PublicTaskID)
		if err != nil {
			return nil, err
		}
		existing, err := getSettlementForTask(ctx, tx, task.id, true)
		if err == nil {
			return resultFor(taskFromSettlement(task, existing), existing, false), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if task.status != string(service.VideoTaskStatusSubmitting) || task.providerEvidence {
			return nil, service.ErrVideoTaskSettlementStateConflict
		}
		if cmd.Admission != nil {
			if cmd.Admission.SubscriptionID != nil {
				task.subscriptionID = sql.NullInt64{Int64: *cmd.Admission.SubscriptionID, Valid: true}
			} else {
				task.subscriptionID = sql.NullInt64{}
			}
		}
		if err := lockSettlementFunding(ctx, tx, task, cmd.BillingType); err != nil {
			return nil, err
		}
		if err := lockSettlementAccountPlatform(ctx, tx, task); err != nil {
			return nil, err
		}
		if cmd.Admission != nil {
			if err := applySettlementAdmission(ctx, tx, task, cmd); err != nil {
				return nil, err
			}
		}
		if err := validateSettlementTaskRelations(ctx, tx, task); err != nil {
			return nil, err
		}

		effects := cmd.Effects
		effects.ChargedAt = time.Time{}
		effects.WindowSnapshot = cloneStringMap(effects.WindowSnapshot)
		if cmd.BillingType == service.BillingTypeBalance {
			if effects.BalanceCost == 0 {
				effects.BalanceCost = cmd.ActualCostUSD
			}
			if effects.BalanceCost != cmd.ActualCostUSD {
				return nil, errors.New("balance reservation effect must equal planned actual cost")
			}
		} else {
			if task.subscriptionID.Valid == false {
				return nil, service.ErrSubscriptionNotFound
			}
			if effects.SubscriptionCost == 0 {
				effects.SubscriptionCost = cmd.ActualCostUSD
			}
			if effects.SubscriptionCost != cmd.ActualCostUSD {
				return nil, errors.New("subscription reservation effect must equal planned actual cost")
			}
		}
		if err := reserveFunding(ctx, tx, task, cmd.BillingType, cmd.ActualCostUSD, &effects); err != nil {
			return nil, err
		}
		pricingJSON, err := json.Marshal(nonNilMap(cmd.PricingSnapshot))
		if err != nil {
			return nil, err
		}
		effectsJSON, err := json.Marshal(effects)
		if err != nil {
			return nil, err
		}
		row := &settlementRow{}
		err = tx.QueryRowContext(ctx, `
			INSERT INTO video_task_settlements
			(video_task_id,user_id,api_key_id,group_id,account_id,platform,channel_id,subscription_id,usage_log_id,
			 charge_request_id,state,billing_type,gross_cost_usd,actual_cost_usd,account_cost_usd,pricing_snapshot,effect_snapshot,reserved_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'reserved',$11,$12,$13,$14,$15,$16,NOW())
			RETURNING id,user_id,api_key_id,group_id,account_id,platform,channel_id,subscription_id,usage_log_id,
			 charge_request_id,state,billing_type,gross_cost_usd,actual_cost_usd,account_cost_usd,refunded_cost_usd,pricing_snapshot,effect_snapshot,applied_snapshot,
			 reserved_at,charged_at,released_at,refunded_at,created_at,updated_at`,
			task.id, task.userID, task.apiKeyID, task.groupID, task.accountID, task.platform, nullIntArg(task.channelID), nullIntArg(task.subscriptionID), nullIntArg(task.usageLogID),
			eventID(task.publicID, "charge"), cmd.BillingType, cmd.GrossCostUSD, cmd.ActualCostUSD, cmd.AccountCostUSD, pricingJSON, effectsJSON).Scan(settlementScanArgs(row)...)
		if err != nil {
			return nil, err
		}
		if err := insertReserveSettlementEvent(ctx, tx, row, task.publicID, pricingJSON, effectsJSON, cmd); err != nil {
			return nil, err
		}
		if err := enqueueCacheInvalidationJob(ctx, tx, row.id, "reserve"); err != nil {
			return nil, err
		}
		result := resultFor(task, row, true)
		if err := populatePostAmounts(ctx, tx, task, result, nil); err != nil {
			return nil, err
		}
		return result, nil
	})
}

func (r *videoTaskSettlementRepository) Capture(ctx context.Context, cmd *service.VideoTaskSettlementCaptureCommand) (*service.VideoTaskSettlementResult, error) {
	if cmd == nil {
		return nil, service.ErrVideoTaskSettlementCommandRequired
	}
	cmd.PublicTaskID = service.NormalizeVideoTaskPublicID(cmd.PublicTaskID)
	return r.inTx(ctx, func(tx *sql.Tx) (*service.VideoTaskSettlementResult, error) {
		task, row, effects, drift, err := lockTaskFundingSettlement(ctx, tx, cmd.PublicTaskID)
		if errors.Is(err, service.ErrVideoTaskSettlementNotFound) {
			return &service.VideoTaskSettlementResult{}, nil
		}
		if err != nil {
			return nil, err
		}
		if drift {
			return nil, service.ErrVideoTaskSettlementIntegrity
		}
		if err := validateCaptureSettlementIdentity(ctx, tx, task); err != nil {
			return nil, err
		}
		if row.state != string(service.VideoTaskSettlementReserved) {
			return resultFor(task, row, false), nil
		}
		switch service.VideoTaskStatus(task.status) {
		case service.VideoTaskStatusQueued, service.VideoTaskStatusInProgress, service.VideoTaskStatusCompleted:
		default:
			return nil, service.ErrVideoTaskSettlementStateConflict
		}
		actualCost := effects.BalanceCost
		if row.billingType == service.BillingTypeSubscription {
			actualCost = effects.SubscriptionCost
		}
		if err := service.ValidateVideoTaskSettlementAmount(actualCost); err != nil {
			return nil, service.ErrVideoTaskSettlementIntegrity
		}
		if cmd.ActualCostUSD != 0 {
			asserted, err := service.NormalizeVideoTaskSettlementAmount(cmd.ActualCostUSD)
			if err != nil || !decimal.NewFromFloat(asserted).Equal(decimal.NewFromFloat(actualCost)) {
				return nil, service.ErrVideoTaskSettlementIntegrity
			}
		}
		if ok, err := settlementEventExists(ctx, tx, row.id, "reserve"); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			return resultFor(task, row, false), nil
		}
		if err := validateSettlementUsageLog(ctx, tx, task, row); err != nil {
			return nil, err
		}
		if err := validateReserveLedger(ctx, tx, task, row, effects); err != nil {
			return nil, err
		}
		inserted, err := claimSettlementEvent(ctx, tx, row.id, "capture", actualCost, task.publicID)
		if err != nil {
			return nil, err
		}
		if !inserted {
			return resultFor(task, row, false), nil
		}
		if row.billingType == service.BillingTypeBalance {
			var balance, frozen float64
			err = tx.QueryRowContext(ctx, `UPDATE users SET frozen_balance=GREATEST(frozen_balance-$1,0), updated_at=NOW()
				WHERE id=$2 AND deleted_at IS NULL AND frozen_balance >= $1 RETURNING balance,frozen_balance`, actualCost, task.userID).Scan(&balance, &frozen)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errors.New("video task frozen balance is insufficient")
			}
			if err != nil {
				return nil, err
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT NOW()`).Scan(&effects.ChargedAt); err != nil {
			return nil, err
		}
		dimensions := captureAppliedDimensions{}
		if err := applyCaptureEffects(ctx, tx, task, row, &effects, &dimensions, actualCost); err != nil {
			return nil, err
		}
		if row.usageLogID.Valid {
			res, err := tx.ExecContext(ctx, `UPDATE usage_logs SET actual_cost=$1 WHERE id=$2`, actualCost, row.usageLogID.Int64)
			if err != nil {
				return nil, err
			}
			if err := requireOneRow(res, "capture video usage log"); err != nil {
				return nil, err
			}
		}
		identity, err := captureIdentityForAppliedEffects(ctx, tx, row, effects)
		if err != nil {
			return nil, err
		}
		appliedSnapshot := authoritativeCaptureSnapshot{CustomerCostUSD: actualCost, Effects: effects, Identity: identity, Dimensions: dimensions}
		appliedJSON, err := json.Marshal(appliedSnapshot)
		if err != nil {
			return nil, err
		}
		if !validAppliedWindows(effects, effects.WindowSnapshot, dimensions) {
			return nil, service.ErrVideoTaskSettlementIntegrity
		}
		if err := storeCaptureAppliedSnapshot(ctx, tx, row.id, appliedSnapshot); err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx, `UPDATE video_task_settlements SET state='charged',actual_cost_usd=$1,applied_snapshot=$2,charged_at=NOW(),updated_at=NOW() WHERE id=$3 AND state='reserved' AND applied_snapshot IS NULL`, actualCost, appliedJSON, row.id)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "capture settlement"); err != nil {
			return nil, err
		}
		res, err = tx.ExecContext(ctx, `UPDATE video_tasks SET billed_usd=$1,updated_at=NOW() WHERE id=$2`, actualCost, task.id)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "update video task charge"); err != nil {
			return nil, err
		}
		row, err = getSettlementForTask(ctx, tx, task.id, false)
		if err != nil {
			return nil, err
		}
		result := resultFor(task, row, true)
		if err := populatePostAmounts(ctx, tx, task, result, identity.PlatformQuotaID); err != nil {
			return nil, err
		}
		return result, nil
	})
}

func (r *videoTaskSettlementRepository) Release(ctx context.Context, cmd *service.VideoTaskSettlementReleaseCommand) (*service.VideoTaskSettlementResult, error) {
	return r.release(ctx, cmd, false)
}

func (r *videoTaskSettlementRepository) ReleaseFailed(ctx context.Context, cmd *service.VideoTaskSettlementReleaseCommand) (*service.VideoTaskSettlementResult, error) {
	return r.release(ctx, cmd, true)
}

func (r *videoTaskSettlementRepository) release(ctx context.Context, cmd *service.VideoTaskSettlementReleaseCommand, requireFailed bool) (*service.VideoTaskSettlementResult, error) {
	if cmd == nil {
		return nil, service.ErrVideoTaskSettlementCommandRequired
	}
	cmd.PublicTaskID = service.NormalizeVideoTaskPublicID(cmd.PublicTaskID)
	return r.inTx(ctx, func(tx *sql.Tx) (*service.VideoTaskSettlementResult, error) {
		task, row, effects, drift, err := lockTaskFundingSettlement(ctx, tx, cmd.PublicTaskID)
		if errors.Is(err, service.ErrVideoTaskSettlementNotFound) {
			return &service.VideoTaskSettlementResult{}, nil
		}
		if err != nil {
			return nil, err
		}
		if row.state != string(service.VideoTaskSettlementReserved) {
			return resultFor(task, row, false), nil
		}
		if requireFailed && task.status != string(service.VideoTaskStatusFailed) {
			return nil, service.ErrVideoTaskSettlementStateConflict
		}
		if ok, err := settlementEventExists(ctx, tx, row.id, "reserve"); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			return resultFor(task, row, false), nil
		}
		inserted, err := claimSettlementEvent(ctx, tx, row.id, "release", row.gross, task.publicID)
		if err != nil {
			return nil, err
		}
		if !inserted {
			return resultFor(task, row, false), nil
		}
		holdAmount, ledgerDrift, err := authoritativeReserveHold(ctx, tx, task, row, effects)
		if err != nil {
			return nil, err
		}
		if err := reverseReservation(ctx, tx, task, row, holdAmount, effects); err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx, `UPDATE video_task_settlements SET state='released',released_at=NOW(),updated_at=NOW() WHERE id=$1 AND state='reserved'`, row.id)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "release settlement"); err != nil {
			return nil, err
		}
		row, err = getSettlementForTask(ctx, tx, task.id, false)
		if err != nil {
			return nil, err
		}
		result := resultFor(task, row, true)
		result.IntegrityDrift = drift || ledgerDrift
		if err := populatePostAmounts(ctx, tx, task, result, nil); err != nil {
			return nil, err
		}
		return result, nil
	})
}

func (r *videoTaskSettlementRepository) FailSubmission(ctx context.Context, cmd *service.VideoTaskSettlementFailCommand) (*service.VideoTaskSettlementResult, error) {
	if cmd == nil {
		return nil, service.ErrVideoTaskSettlementCommandRequired
	}
	cmd.PublicTaskID = service.NormalizeVideoTaskPublicID(cmd.PublicTaskID)
	return r.inTx(ctx, func(tx *sql.Tx) (*service.VideoTaskSettlementResult, error) {
		task, row, effects, drift, err := lockTaskFundingSettlement(ctx, tx, cmd.PublicTaskID)
		if err != nil {
			return nil, err
		}
		if row.state != string(service.VideoTaskSettlementReserved) {
			return resultFor(task, row, false), nil
		}
		inserted, err := claimSettlementEvent(ctx, tx, row.id, "release", row.gross, task.publicID)
		if err != nil {
			return nil, err
		}
		if !inserted {
			return resultFor(task, row, false), nil
		}
		holdAmount, ledgerDrift, err := authoritativeReserveHold(ctx, tx, task, row, effects)
		if err != nil {
			return nil, err
		}
		if err := reverseReservation(ctx, tx, task, row, holdAmount, effects); err != nil {
			return nil, err
		}
		metadata, err := json.Marshal(nonNilMap(cmd.Metadata))
		if err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx, `UPDATE video_tasks SET status=$1,provider_status=$1,error_message=$2,result_metadata=COALESCE(result_metadata,'{}'::jsonb)||$3::jsonb,usage_log_id=NULL,completed_at=NOW(),next_poll_at=NULL,updated_at=NOW() WHERE id=$4`, string(service.VideoTaskStatusFailed), strings.TrimSpace(cmd.ErrorMessage), metadata, task.id)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "fail video task submission"); err != nil {
			return nil, err
		}
		if row.usageLogID.Valid {
			if _, err := tx.ExecContext(ctx, `WITH target AS (
				SELECT ul.id FROM usage_logs ul
				WHERE ul.id=$1 AND NOT EXISTS (
					SELECT 1 FROM video_task_refund_reporting_jobs j
					WHERE j.usage_log_id=ul.id AND j.completed_at IS NULL
				) FOR UPDATE OF ul
			), deleted_jobs AS (
				DELETE FROM video_task_refund_reporting_jobs j USING target
				WHERE j.usage_log_id=target.id AND j.completed_at IS NOT NULL
			)
			DELETE FROM usage_logs ul USING target WHERE ul.id=target.id`, row.usageLogID.Int64); err != nil {
				return nil, err
			}
			task.usageLogID = sql.NullInt64{}
		}
		res, err = tx.ExecContext(ctx, `UPDATE video_task_settlements SET state='released',released_at=NOW(),updated_at=NOW() WHERE id=$1 AND state='reserved'`, row.id)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "release failed submission settlement"); err != nil {
			return nil, err
		}
		row, err = getSettlementForTask(ctx, tx, task.id, false)
		if err != nil {
			return nil, err
		}
		result := resultFor(task, row, true)
		result.IntegrityDrift = drift || ledgerDrift
		if err := populatePostAmounts(ctx, tx, task, result, nil); err != nil {
			return nil, err
		}
		return result, nil
	})
}

func (r *videoTaskSettlementRepository) RefundFailed(ctx context.Context, cmd *service.VideoTaskSettlementRefundCommand) (*service.VideoTaskSettlementResult, error) {
	if cmd == nil {
		return nil, service.ErrVideoTaskSettlementCommandRequired
	}
	cmd.PublicTaskID = service.NormalizeVideoTaskPublicID(cmd.PublicTaskID)
	return r.inTx(ctx, func(tx *sql.Tx) (*service.VideoTaskSettlementResult, error) {
		task, row, effects, drift, err := lockTaskFundingSettlement(ctx, tx, cmd.PublicTaskID)
		if errors.Is(err, service.ErrVideoTaskSettlementNotFound) {
			return &service.VideoTaskSettlementResult{}, nil
		}
		if err != nil {
			return nil, err
		}
		if task.status != string(service.VideoTaskStatusFailed) || row.state != string(service.VideoTaskSettlementCharged) {
			return resultFor(task, row, false), nil
		}
		if ok, err := settlementEventExists(ctx, tx, row.id, "capture"); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			return resultFor(task, row, false), nil
		}
		applied, err := loadAuthoritativeCaptureSnapshot(ctx, tx, row, task.publicID, &effects)
		if err != nil {
			return nil, err
		}
		if err := validateSettlementUsageLog(ctx, tx, task, row); err != nil {
			return nil, err
		}
		inserted, err := claimSettlementEvent(ctx, tx, row.id, "refund", row.actual, task.publicID)
		if err != nil {
			return nil, err
		}
		if !inserted {
			return resultFor(task, row, false), nil
		}
		if err := reverseCapturedEffects(ctx, tx, task, row, effects, applied, strings.TrimSpace(cmd.Reason)); err != nil {
			return nil, err
		}
		var refundUsageCreatedAt time.Time
		if row.usageLogID.Valid {
			if err := tx.QueryRowContext(ctx, `SELECT created_at FROM usage_logs WHERE id=$1`, row.usageLogID.Int64).Scan(&refundUsageCreatedAt); err != nil {
				return nil, err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE video_task_settlements SET state='refunded',refunded_cost_usd=$1,refunded_at=NOW(),updated_at=NOW() WHERE id=$2 AND state='charged'`, row.actual, row.id)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "refund settlement"); err != nil {
			return nil, err
		}
		if row.usageLogID.Valid {
			res, err = tx.ExecContext(ctx, `INSERT INTO video_task_refund_reporting_jobs (settlement_id,usage_log_id,usage_created_at)
				VALUES ($1,$2,$3) ON CONFLICT (settlement_id) DO NOTHING`, row.id, row.usageLogID.Int64, refundUsageCreatedAt)
			if err != nil {
				return nil, err
			}
			if affected, affectedErr := res.RowsAffected(); affectedErr != nil || affected != 1 {
				if affectedErr != nil {
					return nil, affectedErr
				}
				return nil, service.ErrVideoTaskSettlementIntegrity
			}
		}
		row, err = getSettlementForTask(ctx, tx, task.id, false)
		if err != nil {
			return nil, err
		}
		result := resultFor(task, row, true)
		if !refundUsageCreatedAt.IsZero() {
			result.RefundUsageCreatedAt = &refundUsageCreatedAt
		}
		result.IntegrityDrift = drift
		if err := populatePostAmounts(ctx, tx, task, result, applied.Identity.PlatformQuotaID); err != nil {
			return nil, err
		}
		return result, nil
	})
}

func (r *videoTaskSettlementRepository) GetByPublicTaskID(ctx context.Context, publicID string) (*service.VideoTaskSettlementSnapshot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("video task settlement repository db is nil")
	}
	row := &settlementRow{}
	err := r.db.QueryRowContext(ctx, `SELECT s.id,s.user_id,s.api_key_id,s.group_id,s.account_id,s.platform,s.channel_id,s.subscription_id,s.usage_log_id,
		s.charge_request_id,s.state,s.billing_type,s.gross_cost_usd,s.actual_cost_usd,s.account_cost_usd,s.refunded_cost_usd,s.pricing_snapshot,s.effect_snapshot,s.applied_snapshot,
		s.reserved_at,s.charged_at,s.released_at,s.refunded_at,s.created_at,s.updated_at
		FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, service.NormalizeVideoTaskPublicID(publicID)).Scan(settlementScanArgs(row)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrVideoTaskSettlementNotFound
	}
	if err != nil {
		return nil, err
	}
	return snapshotFromRow(service.NormalizeVideoTaskPublicID(publicID), row)
}

func (r *videoTaskSettlementRepository) RepairChargedUsage(ctx context.Context, publicID string) (*service.VideoTaskSettlementResult, error) {
	publicID = service.NormalizeVideoTaskPublicID(publicID)
	return r.inTx(ctx, func(tx *sql.Tx) (*service.VideoTaskSettlementResult, error) {
		task, err := lockSettlementTask(ctx, tx, publicID)
		if err != nil {
			return nil, err
		}
		row, err := getSettlementForTask(ctx, tx, task.id, true)
		if err != nil {
			return nil, err
		}
		task.platform = row.platform
		if row.state != string(service.VideoTaskSettlementCharged) {
			return resultFor(task, row, false), nil
		}
		var effects service.VideoTaskBillingEffects
		if err := json.Unmarshal(row.effectsJSON, &effects); err != nil {
			return nil, service.ErrVideoTaskSettlementIntegrity
		}
		if err := validateReserveLedger(ctx, tx, task, row, effects); err != nil {
			return nil, err
		}
		var applied authoritativeCaptureSnapshot
		if err := json.Unmarshal(row.appliedJSON, &applied); err != nil {
			return nil, service.ErrVideoTaskSettlementIntegrity
		}
		validationRow := *row
		if applied.Identity.UsageLogID != nil {
			validationRow.usageLogID = sql.NullInt64{Int64: *applied.Identity.UsageLogID, Valid: true}
		} else {
			validationRow.usageLogID = sql.NullInt64{}
		}
		if _, err := loadAuthoritativeCaptureSnapshot(ctx, tx, &validationRow, publicID, &effects); err != nil {
			return nil, err
		}
		usage, usageMetadata, err := loadReserveAdmissionLedger(ctx, tx, row.id)
		if err != nil {
			return nil, err
		}
		usage.ID = 0
		usage.RequestID = row.chargeID
		usage.UserID, usage.APIKeyID, usage.AccountID = row.userID, row.apiKeyID, row.accountID
		usage.GroupID, usage.SubscriptionID, usage.ChannelID = int64Ptr(row.groupID), nullIntPtr(row.subscriptionID), nullIntPtr(row.channelID)
		usage.TotalCost, usage.ActualCost = row.gross, row.actual
		usage.AccountStatsCost = float64Ptr(effects.AccountStatsCost)
		correctedLinkedUsage := false
		if row.usageLogID.Valid && task.usageLogID.Valid && row.usageLogID.Int64 == task.usageLogID.Int64 {
			linkedExpected := *usage
			linkedExpected.ID = row.usageLogID.Int64
			var refundedCost, refundedTotalCost, refundedAccountCost float64
			var refundedAt sql.NullTime
			var refundReason sql.NullString
			refundErr := tx.QueryRowContext(ctx, `SELECT refunded_cost,refunded_total_cost,refunded_account_cost,refund_reason,refunded_at FROM usage_logs WHERE id=$1`, linkedExpected.ID).Scan(&refundedCost, &refundedTotalCost, &refundedAccountCost, &refundReason, &refundedAt)
			needsCorrection := validateAdoptedAdmissionUsage(ctx, tx, &linkedExpected) != nil || refundErr != nil || refundedCost != 0 || refundedTotalCost != 0 || refundedAccountCost != 0 || strings.TrimSpace(refundReason.String) != "" || refundedAt.Valid
			if needsCorrection {
				res, updateErr := tx.ExecContext(ctx, `UPDATE usage_logs SET
				user_id=$1,account_id=$2,model=$3,requested_model=$4,upstream_model=$5,group_id=$6,subscription_id=$7,channel_id=$8,
				total_cost=$9,actual_cost=$10,rate_multiplier=$11,account_rate_multiplier=$12,billing_type=$13,request_type=$14,
				video_count=$15,video_resolution=$16,video_duration_seconds=$17,billing_mode=$18,account_stats_cost=$19,inbound_endpoint=$20,upstream_endpoint=$21,
				refunded_cost=0,refunded_total_cost=0,refunded_account_cost=0,refund_reason=NULL,refunded_at=NULL
				WHERE id=$22 AND request_id=$23 AND api_key_id=$24`, usage.UserID, usage.AccountID, usage.Model, usage.RequestedModel, usage.UpstreamModel, usage.GroupID, usage.SubscriptionID, usage.ChannelID,
					usage.TotalCost, usage.ActualCost, usage.RateMultiplier, usage.AccountRateMultiplier, usage.BillingType, int16(usage.RequestType.Normalize()), usage.VideoCount, usage.VideoResolution, usage.VideoDurationSeconds, usage.BillingMode, usage.AccountStatsCost, usage.InboundEndpoint, usage.UpstreamEndpoint,
					row.usageLogID.Int64, row.chargeID, row.apiKeyID)
				if updateErr != nil {
					return nil, updateErr
				}
				if err := requireOneRow(res, "repair linked charged usage"); err != nil {
					return nil, service.ErrVideoTaskSettlementIntegrity
				}
				correctedLinkedUsage = true
			}
		}
		inserted, err := (&usageLogRepository{}).createSingle(ctx, tx, usage)
		if err != nil {
			return nil, err
		}
		if usage.ID <= 0 || validateAdoptedAdmissionUsage(ctx, tx, usage) != nil {
			return nil, service.ErrVideoTaskSettlementIntegrity
		}
		if row.usageLogID.Valid && row.usageLogID.Int64 != usage.ID || task.usageLogID.Valid && task.usageLogID.Int64 != usage.ID {
			return nil, service.ErrVideoTaskSettlementIntegrity
		}
		metadataJSON, err := json.Marshal(nonNilMap(usageMetadata))
		if err != nil {
			return nil, err
		}
		var currentSubscription sql.NullInt64
		var currentMetadata []byte
		var currentBilled float64
		if err := tx.QueryRowContext(ctx, `SELECT subscription_id,usage_metadata,billed_usd FROM video_tasks WHERE id=$1 FOR UPDATE`, task.id).Scan(&currentSubscription, &currentMetadata, &currentBilled); err != nil {
			return nil, err
		}
		alreadyLinked := row.usageLogID.Valid && task.usageLogID.Valid && nullIntEqual(currentSubscription, row.subscriptionID) && canonicalJSONEqual(currentMetadata, metadataJSON) && sameDecimal(row.actual, currentBilled)
		res, err := tx.ExecContext(ctx, `UPDATE video_task_settlements SET usage_log_id=$1,updated_at=NOW() WHERE id=$2`, usage.ID, row.id)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "repair settlement usage link"); err != nil {
			return nil, err
		}
		res, err = tx.ExecContext(ctx, `UPDATE video_tasks SET usage_log_id=$1,subscription_id=$2,usage_metadata=$3,billed_usd=$4,updated_at=NOW() WHERE id=$5`, usage.ID, nullIntArg(row.subscriptionID), metadataJSON, row.actual, task.id)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "repair task usage summary"); err != nil {
			return nil, err
		}
		row.usageLogID, task.usageLogID = sql.NullInt64{Int64: usage.ID, Valid: true}, sql.NullInt64{Int64: usage.ID, Valid: true}
		return resultFor(task, row, !alreadyLinked || inserted || correctedLinkedUsage), nil
	})
}

func loadReserveAdmissionLedger(ctx context.Context, tx *sql.Tx, settlementID int64) (*service.UsageLog, map[string]any, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT metadata->'admission' FROM video_task_settlement_events WHERE settlement_id=$1 AND event_type='reserve' FOR UPDATE`, settlementID).Scan(&raw); err != nil {
		return nil, nil, errors.Join(service.ErrVideoTaskSettlementIntegrity, err)
	}
	var admission service.VideoTaskSettlementAdmission
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &admission) != nil || admission.UsageLog == nil {
		return nil, nil, service.ErrVideoTaskSettlementIntegrity
	}
	return admission.UsageLog, admission.UsageMetadata, nil
}

func float64Ptr(value float64) *float64 { return &value }

func int64Ptr(value int64) *int64 { return &value }

func (r *videoTaskSettlementRepository) ClaimDueReconciliation(ctx context.Context, now time.Time, limit int, token string, ttl time.Duration) ([]service.VideoTaskSettlementReconcileClaim, error) {
	if limit <= 0 {
		return nil, nil
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `WITH due AS (
		SELECT s.id FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id
		LEFT JOIN usage_logs l ON l.id=s.usage_log_id
		LEFT JOIN video_task_settlement_events reserve_event ON reserve_event.settlement_id=s.id AND reserve_event.event_type='reserve'
		WHERE s.state IN ('reserved','charged') AND (s.next_reconcile_at IS NULL OR s.next_reconcile_at <= $1)
		AND (s.locked_until IS NULL OR s.locked_until <= $1)
		AND (s.state='reserved' OR NULLIF(BTRIM(COALESCE(s.last_error,'')),'') IS NOT NULL OR t.status='failed' OR s.usage_log_id IS NULL OR l.id IS NULL OR t.usage_log_id IS DISTINCT FROM s.usage_log_id OR t.subscription_id IS DISTINCT FROM s.subscription_id OR t.billed_usd IS DISTINCT FROM s.actual_cost_usd OR COALESCE(t.usage_metadata->>'request_id','') IS DISTINCT FROM s.charge_request_id
			OR l.user_id IS DISTINCT FROM s.user_id OR l.api_key_id IS DISTINCT FROM s.api_key_id OR l.account_id IS DISTINCT FROM s.account_id OR l.group_id IS DISTINCT FROM s.group_id OR l.subscription_id IS DISTINCT FROM s.subscription_id OR l.channel_id IS DISTINCT FROM s.channel_id OR l.request_id IS DISTINCT FROM s.charge_request_id
			OR l.model IS DISTINCT FROM reserve_event.metadata->'admission'->'UsageLog'->>'Model' OR l.requested_model IS DISTINCT FROM reserve_event.metadata->'admission'->'UsageLog'->>'RequestedModel' OR l.upstream_model IS DISTINCT FROM NULLIF(reserve_event.metadata->'admission'->'UsageLog'->>'UpstreamModel','')
			OR l.total_cost IS DISTINCT FROM s.gross_cost_usd OR l.actual_cost IS DISTINCT FROM s.actual_cost_usd OR l.account_stats_cost IS DISTINCT FROM (s.effect_snapshot->>'account_stats_cost')::numeric OR l.billing_type IS DISTINCT FROM s.billing_type OR l.billing_mode IS DISTINCT FROM COALESCE(NULLIF(regexp_replace(s.pricing_snapshot->>'billing_mode','^[[:space:]]+$',''),''),'video')
			OR l.request_type IS DISTINCT FROM (reserve_event.metadata->'admission'->'UsageLog'->>'RequestType')::smallint OR ROUND(l.rate_multiplier::numeric,4) IS DISTINCT FROM ROUND((reserve_event.metadata->'admission'->'UsageLog'->>'RateMultiplier')::numeric,4) OR ROUND(l.account_rate_multiplier::numeric,4) IS DISTINCT FROM ROUND((reserve_event.metadata->'admission'->'UsageLog'->>'AccountRateMultiplier')::numeric,4)
			OR l.inbound_endpoint IS DISTINCT FROM reserve_event.metadata->'admission'->'UsageLog'->>'InboundEndpoint' OR l.upstream_endpoint IS DISTINCT FROM reserve_event.metadata->'admission'->'UsageLog'->>'UpstreamEndpoint' OR l.video_count IS DISTINCT FROM (reserve_event.metadata->'admission'->'UsageLog'->>'VideoCount')::int OR l.video_resolution IS DISTINCT FROM NULLIF(reserve_event.metadata->'admission'->'UsageLog'->>'VideoResolution','') OR l.video_duration_seconds IS DISTINCT FROM (reserve_event.metadata->'admission'->'UsageLog'->>'VideoDurationSeconds')::int
			OR l.refunded_cost<>0 OR l.refunded_total_cost<>0 OR l.refunded_account_cost<>0 OR NULLIF(BTRIM(COALESCE(l.refund_reason,'')),'') IS NOT NULL OR l.refunded_at IS NOT NULL)
		ORDER BY COALESCE(s.next_reconcile_at,s.created_at),s.id FOR UPDATE OF s SKIP LOCKED LIMIT $2
	) UPDATE video_task_settlements s SET locked_by=$3,locked_until=$4,reconcile_attempts=s.reconcile_attempts+1,updated_at=NOW()
	FROM due,video_tasks t WHERE s.id=due.id AND t.id=s.video_task_id RETURNING t.public_task_id,s.reconcile_attempts`, now, limit, token, now.Add(ttl))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	claims := make([]service.VideoTaskSettlementReconcileClaim, 0, limit)
	for rows.Next() {
		var claim service.VideoTaskSettlementReconcileClaim
		if err := rows.Scan(&claim.PublicTaskID, &claim.Attempts); err != nil {
			return nil, err
		}
		claim.LeaseToken = token
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (r *videoTaskSettlementRepository) ClaimDueAdmissionOrphans(ctx context.Context, now time.Time, grace time.Duration, limit int, token string, ttl time.Duration) ([]service.VideoTaskAdmissionOrphanClaim, error) {
	if limit <= 0 {
		return nil, nil
	}
	if grace <= 0 {
		grace = 2 * time.Minute
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	rows, err := r.db.QueryContext(ctx, `WITH due AS (
		SELECT t.id FROM video_tasks t
		WHERE t.status='submitting' AND t.created_at <= $1::timestamptz-($2::bigint*INTERVAL '1 microsecond')
		AND (t.locked_until IS NULL OR t.locked_until <= $1::timestamptz)
		AND t.upstream_task_id IS NULL AND t.submitted_at IS NULL AND t.provider_status IS NULL
		AND t.next_poll_at IS NULL AND t.usage_log_id IS NULL AND t.upstream_response_body IS NULL
		AND COALESCE(t.upstream_response,'{}'::jsonb)='{}'::jsonb AND COALESCE(t.result_metadata,'{}'::jsonb)='{}'::jsonb
		AND t.billed_usd=0
		AND t.request_metadata->'request_metadata' ? 'video_pricing_snapshot'
		AND t.request_metadata->'request_metadata' ? 'video_settlement_admission'
		AND NOT t.request_metadata ? 'reconciliation_accepted_snapshot'
		AND NOT t.request_metadata ? 'reconciliation_upstream_task_id'
		AND NOT EXISTS (SELECT 1 FROM video_task_settlements s WHERE s.video_task_id=t.id)
		ORDER BY t.created_at,t.id FOR UPDATE OF t SKIP LOCKED LIMIT $3
	) UPDATE video_tasks t SET locked_by=$4,locked_until=$5,updated_at=NOW()
	FROM due WHERE t.id=due.id RETURNING t.public_task_id`, now, grace.Microseconds(), limit, token, now.Add(ttl))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	claims := make([]service.VideoTaskAdmissionOrphanClaim, 0, limit)
	for rows.Next() {
		var claim service.VideoTaskAdmissionOrphanClaim
		if err := rows.Scan(&claim.PublicTaskID); err != nil {
			return nil, err
		}
		claim.LeaseToken = token
		claims = append(claims, claim)
	}
	return claims, rows.Err()
}

func (r *videoTaskSettlementRepository) FailAdmissionOrphan(ctx context.Context, publicTaskID, token, code, message string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE video_tasks t SET
		status='failed',provider_status='failed',error_code=$3::text,error_message=$4::text,completed_at=NOW(),next_poll_at=NULL,
		result_metadata=COALESCE(t.result_metadata,'{}'::jsonb)||jsonb_build_object('reconciliation_error_code',$3::text,'reconciliation_error_message',$4::text,'retriable',true,'provider_called',false),
		locked_by=NULL,locked_until=NULL,updated_at=NOW()
		WHERE t.public_task_id=$1 AND t.locked_by=$2 AND t.locked_until>statement_timestamp()
		AND t.status='submitting' AND t.upstream_task_id IS NULL AND t.submitted_at IS NULL AND t.provider_status IS NULL
		AND t.next_poll_at IS NULL AND t.usage_log_id IS NULL AND t.upstream_response_body IS NULL
		AND COALESCE(t.upstream_response,'{}'::jsonb)='{}'::jsonb AND COALESCE(t.result_metadata,'{}'::jsonb)='{}'::jsonb
		AND t.billed_usd=0
		AND t.request_metadata->'request_metadata' ? 'video_pricing_snapshot'
		AND t.request_metadata->'request_metadata' ? 'video_settlement_admission'
		AND NOT t.request_metadata ? 'reconciliation_accepted_snapshot'
		AND NOT t.request_metadata ? 'reconciliation_upstream_task_id'
		AND NOT EXISTS (SELECT 1 FROM video_task_settlements s WHERE s.video_task_id=t.id)`, service.NormalizeVideoTaskPublicID(publicTaskID), token, strings.TrimSpace(code), strings.TrimSpace(message))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *videoTaskSettlementRepository) CompleteReconciliation(ctx context.Context, publicTaskID, token string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE video_task_settlements s SET last_error=NULL,next_reconcile_at=NULL,reconcile_attempts=0,locked_by=NULL,locked_until=NULL,updated_at=NOW()
		FROM video_tasks t WHERE s.video_task_id=t.id AND t.public_task_id=$1 AND s.locked_by=$2 AND s.locked_until>statement_timestamp()`, service.NormalizeVideoTaskPublicID(publicTaskID), token)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *videoTaskSettlementRepository) RenewSettlementClaim(ctx context.Context, publicTaskID, token string, ttl time.Duration) (time.Time, bool, error) {
	if ttl <= 0 {
		ttl = time.Minute
	}
	var lockedUntil time.Time
	err := r.db.QueryRowContext(ctx, `UPDATE video_task_settlements s SET locked_until=statement_timestamp()+($3::bigint*INTERVAL '1 microsecond'),updated_at=NOW()
		FROM video_tasks t WHERE s.video_task_id=t.id AND t.public_task_id=$1 AND s.locked_by=$2 AND s.locked_until>statement_timestamp() RETURNING s.locked_until`, service.NormalizeVideoTaskPublicID(publicTaskID), token, ttl.Microseconds()).Scan(&lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return lockedUntil, err == nil, err
}

func (r *videoTaskSettlementRepository) RetryReconciliation(ctx context.Context, publicTaskID, token, message string, next time.Time) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE video_task_settlements s SET last_error=$3,next_reconcile_at=$4,locked_by=NULL,locked_until=NULL,updated_at=NOW()
		FROM video_tasks t WHERE s.video_task_id=t.id AND t.public_task_id=$1 AND s.locked_by=$2 AND s.locked_until>statement_timestamp()`, service.NormalizeVideoTaskPublicID(publicTaskID), token, strings.TrimSpace(message), next)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *videoTaskSettlementRepository) ClaimDueRefundReporting(ctx context.Context, now time.Time, limit int, token string, ttl time.Duration) ([]service.VideoTaskRefundReportingClaim, error) {
	if limit <= 0 {
		return nil, nil
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	rows, err := r.db.QueryContext(ctx, `WITH due AS (
		SELECT id FROM video_task_refund_reporting_jobs
		WHERE completed_at IS NULL AND (next_attempt_at IS NULL OR next_attempt_at <= $1)
		AND (locked_until IS NULL OR locked_until <= $1)
		ORDER BY COALESCE(next_attempt_at,created_at),id FOR UPDATE SKIP LOCKED LIMIT $2
	) UPDATE video_task_refund_reporting_jobs j
	SET locked_by=$3,locked_until=$4,attempts=j.attempts+1,updated_at=NOW()
	FROM due WHERE j.id=due.id
	RETURNING j.id,j.settlement_id,j.usage_log_id,j.usage_created_at,j.attempts`, now, limit, token, now.Add(ttl))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	claims := make([]service.VideoTaskRefundReportingClaim, 0, limit)
	for rows.Next() {
		var claim service.VideoTaskRefundReportingClaim
		if err := rows.Scan(&claim.JobID, &claim.SettlementID, &claim.UsageLogID, &claim.UsageCreatedAt, &claim.Attempts); err != nil {
			return nil, err
		}
		claim.LeaseToken = token
		claims = append(claims, claim)
	}
	return claims, rows.Err()
}

func (r *videoTaskSettlementRepository) CompleteRefundReporting(ctx context.Context, jobID int64, token string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE video_task_refund_reporting_jobs
		SET completed_at=NOW(),last_error=NULL,next_attempt_at=NULL,locked_by=NULL,locked_until=NULL,updated_at=NOW()
		WHERE id=$1 AND completed_at IS NULL AND locked_by=$2 AND locked_until>statement_timestamp()`, jobID, token)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *videoTaskSettlementRepository) RetryRefundReporting(ctx context.Context, jobID int64, token, message string, next time.Time) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE video_task_refund_reporting_jobs
		SET last_error=$3,next_attempt_at=$4,locked_by=NULL,locked_until=NULL,updated_at=NOW()
		WHERE id=$1 AND completed_at IS NULL AND locked_by=$2 AND locked_until>statement_timestamp()`, jobID, token, strings.TrimSpace(message), next)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *videoTaskSettlementRepository) ClaimDueCacheInvalidation(ctx context.Context, limit int, token string, ttl time.Duration) ([]service.VideoTaskCacheInvalidationClaim, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `WITH due AS (
		SELECT id FROM video_task_cache_invalidation_jobs WHERE completed_at IS NULL AND failed_at IS NULL
		AND (next_attempt_at IS NULL OR next_attempt_at<=statement_timestamp())
		AND (locked_until IS NULL OR locked_until<=statement_timestamp())
		ORDER BY COALESCE(next_attempt_at,created_at),id FOR UPDATE SKIP LOCKED LIMIT $1
	) UPDATE video_task_cache_invalidation_jobs j SET locked_by=$2,locked_until=statement_timestamp()+make_interval(secs=>$3),
		attempts=j.attempts+1,updated_at=NOW() FROM due WHERE j.id=due.id
	RETURNING j.id,j.settlement_id,j.event_type,j.payload,j.attempts`, limit, token, ttl.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	claims := make([]service.VideoTaskCacheInvalidationClaim, 0, limit)
	for rows.Next() {
		var claim service.VideoTaskCacheInvalidationClaim
		if err := rows.Scan(&claim.JobID, &claim.SettlementID, &claim.EventType, &claim.Payload, &claim.Attempts); err != nil {
			return nil, err
		}
		var payload struct {
			Version                   int
			UserID, APIKeyID, GroupID int64
			Platform                  string
			BillingType               int8
			Effects                   service.VideoTaskBillingEffects
		}
		if json.Unmarshal(claim.Payload, &payload) == nil {
			claim.UserID = payload.UserID
			claim.APIKeyID = payload.APIKeyID
			claim.GroupID = payload.GroupID
			claim.Platform = payload.Platform
			claim.BillingType = payload.BillingType
			claim.Effects = payload.Effects
		}
		claim.LeaseToken = token
		claims = append(claims, claim)
	}
	return claims, rows.Err()
}

func (r *videoTaskSettlementRepository) CompleteCacheInvalidation(ctx context.Context, jobID int64, token string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE video_task_cache_invalidation_jobs SET completed_at=NOW(),last_error=NULL,next_attempt_at=NULL,locked_by=NULL,locked_until=NULL,updated_at=NOW() WHERE id=$1 AND completed_at IS NULL AND locked_by=$2 AND locked_until>statement_timestamp()`, jobID, token)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *videoTaskSettlementRepository) RetryCacheInvalidation(ctx context.Context, jobID int64, token, message string, next time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE video_task_cache_invalidation_jobs SET last_error=$3,next_attempt_at=$4,locked_by=NULL,locked_until=NULL,updated_at=NOW() WHERE id=$1 AND completed_at IS NULL AND locked_by=$2 AND locked_until>statement_timestamp()`, jobID, token, strings.TrimSpace(message), next)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *videoTaskSettlementRepository) DeadLetterCacheInvalidation(ctx context.Context, jobID int64, token, reason string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE video_task_cache_invalidation_jobs SET failed_at=NOW(),dead_letter_reason=$3,last_error=$3,locked_by=NULL,locked_until=NULL,updated_at=NOW() WHERE id=$1 AND completed_at IS NULL AND failed_at IS NULL AND locked_by=$2 AND locked_until>statement_timestamp()`, jobID, token, strings.TrimSpace(reason))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *videoTaskSettlementRepository) inTx(ctx context.Context, fn func(*sql.Tx) (*service.VideoTaskSettlementResult, error)) (_ *service.VideoTaskSettlementResult, err error) {
	if r == nil || r.db == nil {
		return nil, errors.New("video task settlement repository db is nil")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	result, err := fn(tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return result, nil
}

func lockSettlementTask(ctx context.Context, tx *sql.Tx, publicID string) (*settlementTaskRow, error) {
	t := &settlementTaskRow{}
	err := tx.QueryRowContext(ctx, `SELECT id,user_id,api_key_id,group_id,account_id,channel_id,subscription_id,usage_log_id,public_task_id,status,
		(upstream_task_id IS NOT NULL OR submitted_at IS NOT NULL OR provider_status IS NOT NULL OR next_poll_at IS NOT NULL
		 OR upstream_response_body IS NOT NULL OR COALESCE(upstream_response,'{}'::jsonb)<>'{}'::jsonb OR COALESCE(result_metadata,'{}'::jsonb)<>'{}'::jsonb
		 OR request_metadata ? 'reconciliation_accepted_snapshot' OR request_metadata ? 'reconciliation_upstream_task_id')
		FROM video_tasks WHERE public_task_id=$1 FOR UPDATE`, publicID).
		Scan(&t.id, &t.userID, &t.apiKeyID, &t.groupID, &t.accountID, &t.channelID, &t.subscriptionID, &t.usageLogID, &t.publicID, &t.status, &t.providerEvidence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrVideoTaskNotFound
	}
	return t, err
}

func applySettlementAdmission(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, cmd *service.VideoTaskSettlementReserveCommand) error {
	admission := cmd.Admission
	if admission == nil || admission.UsageLog == nil {
		return errors.New("video task settlement admission usage log is required")
	}
	usage := admission.UsageLog
	if usage.UserID != task.userID || usage.APIKeyID != task.apiKeyID || usage.AccountID != task.accountID || strings.TrimSpace(usage.RequestID) != eventID(task.publicID, "charge") {
		return service.ErrVideoTaskSettlementRelationInvalid
	}
	if admission.SubscriptionID != nil {
		usage.SubscriptionID = admission.SubscriptionID
		task.subscriptionID = sql.NullInt64{Int64: *admission.SubscriptionID, Valid: true}
	} else {
		usage.SubscriptionID = nil
		task.subscriptionID = sql.NullInt64{}
	}
	if task.channelID.Valid {
		usage.ChannelID = &task.channelID.Int64
	}
	inserted, err := (&usageLogRepository{}).createSingle(ctx, tx, usage)
	if err != nil {
		return err
	}
	if !inserted && usage.ID <= 0 {
		return errors.New("video task admission usage log was not linked")
	}
	if !inserted {
		if err := validateAdoptedAdmissionUsage(ctx, tx, usage); err != nil {
			return err
		}
	}
	task.usageLogID = sql.NullInt64{Int64: usage.ID, Valid: true}
	metadata, err := json.Marshal(nonNilMap(admission.UsageMetadata))
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE video_tasks SET subscription_id=$1,usage_log_id=$2,usage_metadata=$3,updated_at=NOW() WHERE id=$4`, nullIntArg(task.subscriptionID), usage.ID, metadata, task.id)
	if err != nil {
		return err
	}
	return requireOneRow(res, "update video task settlement admission")
}

func validateAdoptedAdmissionUsage(ctx context.Context, tx *sql.Tx, expected *service.UsageLog) error {
	var userID, apiKeyID, accountID int64
	var requestID, model, requestedModel string
	var upstreamModel, billingMode, resolution, inboundEndpoint, upstreamEndpoint sql.NullString
	var groupID, subscriptionID, channelID sql.NullInt64
	var totalCost, actualCost, rateMultiplier float64
	var accountRateMultiplier, accountStatsCost sql.NullFloat64
	var billingType int8
	var requestType int16
	var videoCount int
	var duration sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT user_id,api_key_id,account_id,request_id,model,requested_model,upstream_model,group_id,subscription_id,channel_id,total_cost,actual_cost,rate_multiplier,account_rate_multiplier,billing_type,request_type,video_count,video_resolution,video_duration_seconds,billing_mode,account_stats_cost,inbound_endpoint,upstream_endpoint FROM usage_logs WHERE id=$1 FOR UPDATE`, expected.ID).
		Scan(&userID, &apiKeyID, &accountID, &requestID, &model, &requestedModel, &upstreamModel, &groupID, &subscriptionID, &channelID, &totalCost, &actualCost, &rateMultiplier, &accountRateMultiplier, &billingType, &requestType, &videoCount, &resolution, &duration, &billingMode, &accountStatsCost, &inboundEndpoint, &upstreamEndpoint)
	if err != nil {
		return errors.Join(service.ErrVideoTaskSettlementIntegrity, err)
	}
	if userID != expected.UserID || apiKeyID != expected.APIKeyID || accountID != expected.AccountID || requestID != strings.TrimSpace(expected.RequestID) || model != expected.Model || requestedModel != expected.RequestedModel ||
		upstreamModel.String != stringPtrValue(expected.UpstreamModel) || groupID.Int64 != intPtrValue(expected.GroupID) || groupID.Valid != (expected.GroupID != nil) || subscriptionID.Int64 != intPtrValue(expected.SubscriptionID) || subscriptionID.Valid != (expected.SubscriptionID != nil) || channelID.Int64 != intPtrValue(expected.ChannelID) || channelID.Valid != (expected.ChannelID != nil) ||
		!sameDecimal(totalCost, expected.TotalCost) || !sameDecimal(actualCost, expected.ActualCost) || !sameDecimalAtScale(rateMultiplier, expected.RateMultiplier, usageLogMultiplierSchemaScale) || accountRateMultiplier.Valid != (expected.AccountRateMultiplier != nil) || (accountRateMultiplier.Valid && !sameDecimalAtScale(accountRateMultiplier.Float64, *expected.AccountRateMultiplier, usageLogMultiplierSchemaScale)) ||
		billingType != expected.BillingType || requestType != int16(expected.RequestType.Normalize()) || videoCount != expected.VideoCount || resolution.String != stringPtrValue(expected.VideoResolution) || duration.Int64 != int64PtrValue(expected.VideoDurationSeconds) || duration.Valid != (expected.VideoDurationSeconds != nil) || billingMode.String != stringPtrValue(expected.BillingMode) ||
		accountStatsCost.Valid != (expected.AccountStatsCost != nil) || (accountStatsCost.Valid && !sameDecimal(accountStatsCost.Float64, *expected.AccountStatsCost)) || inboundEndpoint.String != stringPtrValue(expected.InboundEndpoint) || upstreamEndpoint.String != stringPtrValue(expected.UpstreamEndpoint) {
		return service.ErrVideoTaskSettlementIntegrity
	}
	return nil
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intPtrValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func int64PtrValue(value *int) int64 {
	if value == nil {
		return 0
	}
	return int64(*value)
}

func validateSettlementTaskRelations(ctx context.Context, tx *sql.Tx, task *settlementTaskRow) error {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM api_keys k WHERE k.id=$1 AND k.user_id=$2 AND k.group_id=$3 AND k.deleted_at IS NULL)
		AND EXISTS(SELECT 1 FROM groups g WHERE g.id=$3 AND g.platform=$4 AND g.deleted_at IS NULL)
		AND EXISTS(SELECT 1 FROM accounts a JOIN account_groups ag ON ag.account_id=a.id WHERE a.id=$5 AND ag.group_id=$3 AND a.platform=$4 AND a.deleted_at IS NULL)
		AND ($6::bigint IS NULL OR EXISTS(SELECT 1 FROM channels c WHERE c.id=$6))`, task.apiKeyID, task.userID, task.groupID, task.platform, task.accountID, nullIntArg(task.channelID)).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return service.ErrVideoTaskSettlementRelationInvalid
	}
	return nil
}

func validateSettlementUsageLog(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, row *settlementRow) error {
	if !row.usageLogID.Valid {
		return nil
	}
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_logs l WHERE l.id=$1 AND l.user_id=$2 AND l.api_key_id=$3 AND l.account_id=$4 AND l.request_id IN ($5,$9)
		AND (l.group_id IS NULL OR l.group_id=$6) AND (l.subscription_id IS NULL OR l.subscription_id IS NOT DISTINCT FROM $7::bigint) AND (l.channel_id IS NULL OR l.channel_id IS NOT DISTINCT FROM $8::bigint))`,
		row.usageLogID.Int64, task.userID, task.apiKeyID, task.accountID, eventID(task.publicID, "charge"), task.groupID, nullIntArg(task.subscriptionID), nullIntArg(task.channelID), task.publicID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return service.ErrVideoTaskSettlementRelationInvalid
	}
	return nil
}

func lockSettlementFunding(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, billingType int8) error {
	var id int64
	if billingType == service.BillingTypeSubscription {
		if !task.subscriptionID.Valid {
			return service.ErrSubscriptionNotFound
		}
		err := tx.QueryRowContext(ctx, `SELECT id FROM user_subscriptions WHERE id=$1 AND user_id=$2 AND group_id=$3 AND deleted_at IS NULL FOR UPDATE`, task.subscriptionID.Int64, task.userID, task.groupID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrVideoTaskSettlementRelationInvalid
		}
		return err
	}
	err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, task.userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUserNotFound
	}
	return err
}

func lockSettlementAccountPlatform(ctx context.Context, tx *sql.Tx, task *settlementTaskRow) error {
	err := tx.QueryRowContext(ctx, `SELECT platform FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, task.accountID).Scan(&task.platform)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrVideoTaskSettlementRelationInvalid
	}
	return err
}

func lockOriginalSettlementFunding(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, billingType int8) error {
	var id int64
	if billingType == service.BillingTypeSubscription {
		if !task.subscriptionID.Valid {
			return service.ErrVideoTaskSettlementIntegrity
		}
		err := tx.QueryRowContext(ctx, `SELECT id FROM user_subscriptions WHERE id=$1 FOR UPDATE`, task.subscriptionID.Int64).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrVideoTaskSettlementIntegrity
		}
		return err
	}
	err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, task.userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrVideoTaskSettlementIntegrity
	}
	return err
}

func taskFromSettlement(current *settlementTaskRow, row *settlementRow) *settlementTaskRow {
	return &settlementTaskRow{id: current.id, userID: row.userID, apiKeyID: row.apiKeyID, groupID: row.groupID, accountID: row.accountID,
		channelID: row.channelID, subscriptionID: row.subscriptionID, usageLogID: row.usageLogID, publicID: current.publicID, platform: row.platform, status: current.status}
}

func taskIdentityDrifted(task *settlementTaskRow, row *settlementRow) bool {
	return task.userID != row.userID || task.apiKeyID != row.apiKeyID || task.groupID != row.groupID || task.accountID != row.accountID ||
		!nullIntEqual(task.channelID, row.channelID) || !nullIntEqual(task.subscriptionID, row.subscriptionID) || !nullIntEqual(task.usageLogID, row.usageLogID)
}

func settlementIdentityEqual(a, b *settlementRow) bool {
	return a.userID == b.userID && a.apiKeyID == b.apiKeyID && a.groupID == b.groupID && a.accountID == b.accountID && a.platform == b.platform &&
		nullIntEqual(a.channelID, b.channelID) && nullIntEqual(a.subscriptionID, b.subscriptionID) && nullIntEqual(a.usageLogID, b.usageLogID) && a.billingType == b.billingType
}

func nullIntEqual(a, b sql.NullInt64) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Int64 == b.Int64)
}

func validateCaptureSettlementIdentity(ctx context.Context, tx *sql.Tx, task *settlementTaskRow) error {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.deleted_at IS NULL)
		AND EXISTS(SELECT 1 FROM api_keys k WHERE k.id=$2 AND k.user_id=$1 AND k.group_id=$3 AND k.deleted_at IS NULL)
		AND EXISTS(SELECT 1 FROM groups g WHERE g.id=$3 AND g.deleted_at IS NULL)
		AND EXISTS(SELECT 1 FROM accounts a WHERE a.id=$4 AND a.deleted_at IS NULL)
		AND ($5::bigint IS NULL OR EXISTS(SELECT 1 FROM channels c WHERE c.id=$5))
		AND ($6::bigint IS NULL OR EXISTS(SELECT 1 FROM user_subscriptions s WHERE s.id=$6 AND s.user_id=$1 AND s.group_id=$3))`,
		task.userID, task.apiKeyID, task.groupID, task.accountID, nullIntArg(task.channelID), nullIntArg(task.subscriptionID)).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return service.ErrVideoTaskSettlementIntegrity
	}
	return nil
}

func lockTaskFundingSettlement(ctx context.Context, tx *sql.Tx, publicID string) (*settlementTaskRow, *settlementRow, service.VideoTaskBillingEffects, bool, error) {
	current, err := lockSettlementTask(ctx, tx, publicID)
	if err != nil {
		return nil, nil, service.VideoTaskBillingEffects{}, false, err
	}
	preRow, err := getSettlementForTask(ctx, tx, current.id, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, service.VideoTaskBillingEffects{}, false, service.ErrVideoTaskSettlementNotFound
	}
	if err != nil {
		return nil, nil, service.VideoTaskBillingEffects{}, false, err
	}
	original := taskFromSettlement(current, preRow)
	if err := lockOriginalSettlementFunding(ctx, tx, original, preRow.billingType); err != nil {
		return nil, nil, service.VideoTaskBillingEffects{}, false, err
	}
	row, err := getSettlementForTask(ctx, tx, current.id, true)
	if err != nil {
		return nil, nil, service.VideoTaskBillingEffects{}, false, err
	}
	if !settlementIdentityEqual(preRow, row) {
		return nil, nil, service.VideoTaskBillingEffects{}, false, service.ErrVideoTaskSettlementIntegrity
	}
	original = taskFromSettlement(current, row)
	drift := taskIdentityDrifted(current, row)
	var effects service.VideoTaskBillingEffects
	if err := json.Unmarshal(row.effectsJSON, &effects); err != nil {
		return nil, nil, effects, false, err
	}
	if effects.WindowSnapshot == nil {
		effects.WindowSnapshot = map[string]string{}
	}
	return original, row, effects, drift, nil
}

func getSettlementForTask(ctx context.Context, tx *sql.Tx, taskID int64, lock bool) (*settlementRow, error) {
	query := `SELECT id,user_id,api_key_id,group_id,account_id,platform,channel_id,subscription_id,usage_log_id,charge_request_id,state,billing_type,gross_cost_usd,actual_cost_usd,account_cost_usd,refunded_cost_usd,pricing_snapshot,effect_snapshot,applied_snapshot,reserved_at,charged_at,released_at,refunded_at,created_at,updated_at FROM video_task_settlements WHERE video_task_id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	row := &settlementRow{}
	err := tx.QueryRowContext(ctx, query, taskID).Scan(settlementScanArgs(row)...)
	return row, err
}

func settlementScanArgs(row *settlementRow) []any {
	return []any{&row.id, &row.userID, &row.apiKeyID, &row.groupID, &row.accountID, &row.platform, &row.channelID, &row.subscriptionID, &row.usageLogID, &row.chargeID, &row.state, &row.billingType, &row.gross, &row.actual, &row.accountCost, &row.refunded, &row.pricingJSON, &row.effectsJSON, &row.appliedJSON, &row.reservedAt, &row.chargedAt, &row.releasedAt, &row.refundedAt, &row.createdAt, &row.updatedAt}
}

func reserveFunding(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, billingType int8, amount float64, effects *service.VideoTaskBillingEffects) error {
	if billingType == service.BillingTypeBalance {
		res, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance-$1,frozen_balance=frozen_balance+$1,updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL AND balance >= $1`, amount, task.userID)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return service.ErrVideoTaskInsufficientBalance
		}
		return nil
	}
	now := time.Now().UTC()
	var d, w, m time.Time
	err := tx.QueryRowContext(ctx, `UPDATE user_subscriptions us SET
		daily_usage_usd=(CASE WHEN us.daily_window_start IS NULL OR (us.expires_at>us.starts_at+interval '1 day' AND us.daily_window_start+interval '24 hours'<=$3) THEN 0 ELSE us.daily_usage_usd END)+$1,
		weekly_usage_usd=(CASE WHEN us.weekly_window_start IS NULL OR us.weekly_window_start+interval '7 days'<=$3 THEN 0 ELSE us.weekly_usage_usd END)+$1,
		monthly_usage_usd=(CASE WHEN us.monthly_window_start IS NULL OR us.monthly_window_start+interval '30 days'<=$3 THEN 0 ELSE us.monthly_usage_usd END)+$1,
		daily_window_start=CASE WHEN us.daily_window_start IS NULL OR (us.expires_at>us.starts_at+interval '1 day' AND us.daily_window_start+interval '24 hours'<=$3) THEN $3 ELSE us.daily_window_start END,
		weekly_window_start=CASE WHEN us.weekly_window_start IS NULL OR us.weekly_window_start+interval '7 days'<=$3 THEN $3 ELSE us.weekly_window_start END,
		monthly_window_start=CASE WHEN us.monthly_window_start IS NULL OR us.monthly_window_start+interval '30 days'<=$3 THEN $3 ELSE us.monthly_window_start END,updated_at=NOW()
		FROM groups g WHERE us.id=$2 AND us.user_id=$4 AND us.group_id=$5 AND us.status=$6 AND us.starts_at<=$3 AND us.expires_at>$3 AND us.deleted_at IS NULL AND g.id=us.group_id AND g.deleted_at IS NULL
		AND (g.daily_limit_usd IS NULL OR g.daily_limit_usd<=0 OR (CASE WHEN us.daily_window_start IS NULL OR (us.expires_at>us.starts_at+interval '1 day' AND us.daily_window_start+interval '24 hours'<=$3) THEN 0 ELSE us.daily_usage_usd END)+$1<=g.daily_limit_usd)
		AND (g.weekly_limit_usd IS NULL OR g.weekly_limit_usd<=0 OR (CASE WHEN us.weekly_window_start IS NULL OR us.weekly_window_start+interval '7 days'<=$3 THEN 0 ELSE us.weekly_usage_usd END)+$1<=g.weekly_limit_usd)
		AND (g.monthly_limit_usd IS NULL OR g.monthly_limit_usd<=0 OR (CASE WHEN us.monthly_window_start IS NULL OR us.monthly_window_start+interval '30 days'<=$3 THEN 0 ELSE us.monthly_usage_usd END)+$1<=g.monthly_limit_usd)
		RETURNING us.daily_window_start,us.weekly_window_start,us.monthly_window_start`, amount, task.subscriptionID.Int64, now, task.userID, task.groupID, service.SubscriptionStatusActive).Scan(&d, &w, &m)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrVideoTaskSubscriptionIneligible
	}
	if err != nil {
		return err
	}
	effects.WindowSnapshot["subscription_daily"] = timeID(d)
	effects.WindowSnapshot["subscription_weekly"] = timeID(w)
	effects.WindowSnapshot["subscription_monthly"] = timeID(m)
	return nil
}

func applyCaptureEffects(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, row *settlementRow, effects *service.VideoTaskBillingEffects, dimensions *captureAppliedDimensions, actual float64) error {
	if effects.ChargedAt.IsZero() {
		effects.ChargedAt = time.Now().UTC()
	}
	if effects.APIKeyQuotaCost > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE api_keys SET quota_used=quota_used+$1,status=CASE WHEN quota>0 AND quota_used+$1>=quota AND status=$3 THEN $4 ELSE status END,updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL`, effects.APIKeyQuotaCost, task.apiKeyID, service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "capture api key quota"); err != nil {
			return err
		}
	}
	if effects.APIKeyRateLimitCost > 0 {
		var h, d, w time.Time
		err := tx.QueryRowContext(ctx, `UPDATE api_keys SET
			usage_5h=CASE WHEN window_5h_start IS NULL OR window_5h_start+interval '5 hours'<=$1 THEN $2 ELSE usage_5h+$2 END,
			usage_1d=CASE WHEN window_1d_start IS NULL OR window_1d_start+interval '24 hours'<=$1 THEN $2 ELSE usage_1d+$2 END,
			usage_7d=CASE WHEN window_7d_start IS NULL OR window_7d_start+interval '7 days'<=$1 THEN $2 ELSE usage_7d+$2 END,
			window_5h_start=CASE WHEN window_5h_start IS NULL OR window_5h_start+interval '5 hours'<=$1 THEN $1 ELSE window_5h_start END,
			window_1d_start=CASE WHEN window_1d_start IS NULL OR window_1d_start+interval '24 hours'<=$1 THEN date_trunc('day',$1) ELSE window_1d_start END,
			window_7d_start=CASE WHEN window_7d_start IS NULL OR window_7d_start+interval '7 days'<=$1 THEN date_trunc('day',$1) ELSE window_7d_start END,updated_at=NOW()
			WHERE id=$3 AND deleted_at IS NULL RETURNING window_5h_start,window_1d_start,window_7d_start`, effects.ChargedAt, effects.APIKeyRateLimitCost, task.apiKeyID).Scan(&h, &d, &w)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrAPIKeyNotFound
		}
		if err != nil {
			return err
		}
		effects.WindowSnapshot["api_key_5h"] = timeID(h)
		effects.WindowSnapshot["api_key_1d"] = timeID(d)
		effects.WindowSnapshot["api_key_7d"] = timeID(w)
	}
	if effects.AccountQuotaCost > 0 {
		delete(effects.WindowSnapshot, "account_daily")
		delete(effects.WindowSnapshot, "account_weekly")
		state, err := incrementUsageBillingAccountQuota(ctx, tx, task.accountID, effects.AccountQuotaCost)
		if err != nil {
			return err
		}
		var daily, weekly sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT extra->>'quota_daily_start',extra->>'quota_weekly_start' FROM accounts WHERE id=$1`, task.accountID).Scan(&daily, &weekly); err != nil {
			return err
		}
		if state.DailyLimit > 0 {
			if !daily.Valid {
				return service.ErrVideoTaskSettlementIntegrity
			}
			effects.WindowSnapshot["account_daily"] = daily.String
			dimensions.AccountDaily = true
		}
		if state.WeeklyLimit > 0 {
			if !weekly.Valid {
				return service.ErrVideoTaskSettlementIntegrity
			}
			effects.WindowSnapshot["account_weekly"] = weekly.String
			dimensions.AccountWeekly = true
		}
	}
	if effects.PlatformQuotaCost > 0 {
		if err := incrementPlatformQuota(ctx, tx, task, effects); err != nil {
			return err
		}
	}
	if row.usageLogID.Valid {
		res, err := tx.ExecContext(ctx, `UPDATE usage_logs SET total_cost=$1,actual_cost=$2,account_stats_cost=$3 WHERE id=$4`, row.gross, actual, effects.AccountStatsCost, row.usageLogID.Int64)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "capture usage log"); err != nil {
			return err
		}
	}
	return nil
}

func incrementPlatformQuota(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, effects *service.VideoTaskBillingEffects) error {
	now := effects.ChargedAt.UTC()
	daily := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	weekly := daily.AddDate(0, 0, -((int(daily.Weekday()) + 6) % 7))
	monthly := now
	var d, w, m time.Time
	err := tx.QueryRowContext(ctx, `INSERT INTO user_platform_quotas
		(user_id,platform,daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start,created_at,updated_at)
		VALUES ($1,$2,$3,$3,$3,$4,$5,$6,NOW(),NOW())
		ON CONFLICT (user_id,platform) WHERE deleted_at IS NULL DO UPDATE SET
		daily_usage_usd=CASE WHEN user_platform_quotas.daily_window_start IS DISTINCT FROM $4 THEN $3 ELSE user_platform_quotas.daily_usage_usd+$3 END,
		weekly_usage_usd=CASE WHEN user_platform_quotas.weekly_window_start IS DISTINCT FROM $5 THEN $3 ELSE user_platform_quotas.weekly_usage_usd+$3 END,
		monthly_usage_usd=CASE WHEN user_platform_quotas.monthly_window_start IS NULL OR user_platform_quotas.monthly_window_start+interval '30 days'<=$6 THEN $3 ELSE user_platform_quotas.monthly_usage_usd+$3 END,
		daily_window_start=$4,weekly_window_start=$5,monthly_window_start=CASE WHEN user_platform_quotas.monthly_window_start IS NULL OR user_platform_quotas.monthly_window_start+interval '30 days'<=$6 THEN $6 ELSE user_platform_quotas.monthly_window_start END,revision=user_platform_quotas.revision+1,updated_at=NOW()
		RETURNING daily_window_start,weekly_window_start,monthly_window_start`, task.userID, task.platform, effects.PlatformQuotaCost, daily, weekly, monthly).Scan(&d, &w, &m)
	if err != nil {
		return err
	}
	effects.WindowSnapshot["platform_daily"] = timeID(d)
	effects.WindowSnapshot["platform_weekly"] = timeID(w)
	effects.WindowSnapshot["platform_monthly"] = timeID(m)
	return nil
}

func authoritativeReserveHold(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, row *settlementRow, effects service.VideoTaskBillingEffects) (float64, bool, error) {
	hold, err := service.NormalizeVideoTaskSettlementAmount(row.actual)
	if err != nil {
		return 0, false, service.ErrVideoTaskSettlementIntegrity
	}
	var physicalID int64
	var tableEventID string
	var eventAmount float64
	err = tx.QueryRowContext(ctx, `SELECT id,event_id,amount_usd FROM video_task_settlement_events WHERE settlement_id=$1 AND event_type='reserve' FOR UPDATE`, row.id).Scan(&physicalID, &tableEventID, &eventAmount)
	if err != nil || physicalID <= 0 {
		return 0, false, errors.Join(service.ErrVideoTaskSettlementIntegrity, err)
	}
	effectAmount := effects.BalanceCost
	if row.billingType == service.BillingTypeSubscription {
		effectAmount = effects.SubscriptionCost
	}
	drift := !sameDecimal(eventAmount, hold) || tableEventID != eventID(task.publicID, "reserve") || !sameDecimal(effectAmount, hold)
	return hold, drift, nil
}

func reverseReservation(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, row *settlementRow, amount float64, effects service.VideoTaskBillingEffects) error {
	if row.billingType == service.BillingTypeBalance {
		res, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance+$1,frozen_balance=GREATEST(frozen_balance-$1,0),updated_at=NOW() WHERE id=$2 AND frozen_balance >= $1`, amount, task.userID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "release balance reservation")
	}
	return decrementSubscription(ctx, tx, task.subscriptionID.Int64, amount, effects.WindowSnapshot)
}

func reverseCapturedEffects(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, row *settlementRow, effects service.VideoTaskBillingEffects, applied *authoritativeCaptureSnapshot, reason string) error {
	if row.billingType == service.BillingTypeBalance && row.actual > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance+$1,updated_at=NOW() WHERE id=$2`, row.actual, task.userID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "refund balance"); err != nil {
			return err
		}
	} else if row.billingType == service.BillingTypeSubscription {
		if err := decrementSubscription(ctx, tx, task.subscriptionID.Int64, effects.SubscriptionCost, effects.WindowSnapshot); err != nil {
			return err
		}
	}
	if effects.APIKeyQuotaCost > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE api_keys SET quota_used=GREATEST(quota_used-$1,0),status=CASE WHEN status=$3 AND GREATEST(quota_used-$1,0)<quota AND (expires_at IS NULL OR expires_at>NOW()) THEN $4 ELSE status END,updated_at=NOW() WHERE id=$2`, effects.APIKeyQuotaCost, task.apiKeyID, service.StatusAPIKeyQuotaExhausted, service.StatusAPIKeyActive)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "refund api key quota"); err != nil {
			return err
		}
	}
	if effects.APIKeyRateLimitCost > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE api_keys SET
			usage_5h=CASE WHEN window_5h_start IS NOT DISTINCT FROM $2::timestamptz THEN GREATEST(usage_5h-$1,0) ELSE usage_5h END,
			usage_1d=CASE WHEN window_1d_start IS NOT DISTINCT FROM $3::timestamptz THEN GREATEST(usage_1d-$1,0) ELSE usage_1d END,
			usage_7d=CASE WHEN window_7d_start IS NOT DISTINCT FROM $4::timestamptz THEN GREATEST(usage_7d-$1,0) ELSE usage_7d END,updated_at=NOW()
			WHERE id=$5`, effects.APIKeyRateLimitCost, nullTimeString(effects.WindowSnapshot["api_key_5h"]), nullTimeString(effects.WindowSnapshot["api_key_1d"]), nullTimeString(effects.WindowSnapshot["api_key_7d"]), task.apiKeyID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "refund api key windows"); err != nil {
			return err
		}
	}
	if effects.AccountQuotaCost > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)
			||jsonb_build_object('quota_used',GREATEST(COALESCE((extra->>'quota_used')::numeric,0)-$1,0))
			||CASE WHEN $2 AND extra->>'quota_daily_start' IS NOT DISTINCT FROM $3 THEN jsonb_build_object('quota_daily_used',GREATEST(COALESCE((extra->>'quota_daily_used')::numeric,0)-$1,0)) ELSE '{}'::jsonb END
			||CASE WHEN $4 AND extra->>'quota_weekly_start' IS NOT DISTINCT FROM $5 THEN jsonb_build_object('quota_weekly_used',GREATEST(COALESCE((extra->>'quota_weekly_used')::numeric,0)-$1,0)) ELSE '{}'::jsonb END,updated_at=NOW()
			WHERE id=$6`, effects.AccountQuotaCost, applied.Dimensions.AccountDaily, nullableSnapshotString(effects.WindowSnapshot["account_daily"]), applied.Dimensions.AccountWeekly, nullableSnapshotString(effects.WindowSnapshot["account_weekly"]), task.accountID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "refund account quota"); err != nil {
			return err
		}
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &task.accountID, nil, nil); err != nil {
			return err
		}
	}
	if effects.PlatformQuotaCost > 0 {
		if applied == nil || applied.Identity.PlatformQuotaID == nil {
			return service.ErrVideoTaskSettlementIntegrity
		}
		res, err := tx.ExecContext(ctx, `UPDATE user_platform_quotas SET
			daily_usage_usd=CASE WHEN daily_window_start IS NOT DISTINCT FROM $2::timestamptz THEN GREATEST(daily_usage_usd-$1,0) ELSE daily_usage_usd END,
			weekly_usage_usd=CASE WHEN weekly_window_start IS NOT DISTINCT FROM $3::timestamptz THEN GREATEST(weekly_usage_usd-$1,0) ELSE weekly_usage_usd END,
			monthly_usage_usd=CASE WHEN monthly_window_start IS NOT DISTINCT FROM $4::timestamptz THEN GREATEST(monthly_usage_usd-$1,0) ELSE monthly_usage_usd END,revision=revision+1,updated_at=NOW()
			WHERE id=$5`, effects.PlatformQuotaCost, nullTimeString(effects.WindowSnapshot["platform_daily"]), nullTimeString(effects.WindowSnapshot["platform_weekly"]), nullTimeString(effects.WindowSnapshot["platform_monthly"]), *applied.Identity.PlatformQuotaID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "refund platform quota"); err != nil {
			return err
		}
	}
	if row.usageLogID.Valid {
		res, err := tx.ExecContext(ctx, `UPDATE usage_logs SET refunded_cost=$1,refunded_total_cost=$2,refunded_account_cost=$3,refund_reason=$4,refunded_at=NOW() WHERE id=$5`, applied.CustomerCostUSD, row.gross, applied.Effects.AccountStatsCost, reason, row.usageLogID.Int64)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "refund usage log"); err != nil {
			return err
		}
	}
	return nil
}

func decrementSubscription(ctx context.Context, tx *sql.Tx, id int64, amount float64, windows map[string]string) error {
	res, err := tx.ExecContext(ctx, `UPDATE user_subscriptions SET
		daily_usage_usd=CASE WHEN daily_window_start IS NOT DISTINCT FROM $2::timestamptz THEN GREATEST(daily_usage_usd-$1,0) ELSE daily_usage_usd END,
		weekly_usage_usd=CASE WHEN weekly_window_start IS NOT DISTINCT FROM $3::timestamptz THEN GREATEST(weekly_usage_usd-$1,0) ELSE weekly_usage_usd END,
		monthly_usage_usd=CASE WHEN monthly_window_start IS NOT DISTINCT FROM $4::timestamptz THEN GREATEST(monthly_usage_usd-$1,0) ELSE monthly_usage_usd END,updated_at=NOW()
		WHERE id=$5`, amount, nullTimeString(windows["subscription_daily"]), nullTimeString(windows["subscription_weekly"]), nullTimeString(windows["subscription_monthly"]), id)
	if err != nil {
		return err
	}
	return requireOneRow(res, "reverse subscription reservation")
}

func insertSettlementEvent(ctx context.Context, tx *sql.Tx, settlementID int64, action string, amount float64, publicID string) error {
	inserted, err := claimSettlementEvent(ctx, tx, settlementID, action, amount, publicID)
	if err != nil {
		return err
	}
	if !inserted {
		return fmt.Errorf("video task settlement %s event already exists", action)
	}
	return nil
}

func insertReserveSettlementEvent(ctx context.Context, tx *sql.Tx, row *settlementRow, publicID string, pricingJSON, effectsJSON []byte, cmd *service.VideoTaskSettlementReserveCommand) error {
	metadata, err := json.Marshal(map[string]any{
		"event_id": eventID(publicID, "reserve"), "public_task_id": publicID, "charge_request_id": eventID(publicID, "charge"),
		"billing_type": cmd.BillingType, "gross_cost_usd": cmd.GrossCostUSD, "actual_cost_usd": cmd.ActualCostUSD, "account_cost_usd": cmd.AccountCostUSD,
		"pricing_snapshot": json.RawMessage(pricingJSON), "effect_snapshot": json.RawMessage(effectsJSON),
		"admission": cmd.Admission,
	})
	if err != nil {
		return err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO video_task_settlement_events (settlement_id,event_id,event_type,amount_usd,metadata) VALUES ($1,$2,'reserve',$3,$4) RETURNING id`, row.id, eventID(publicID, "reserve"), cmd.ActualCostUSD, metadata).Scan(&id)
	if err != nil {
		return err
	}
	if id <= 0 {
		return service.ErrVideoTaskSettlementIntegrity
	}
	return nil
}

func validateReserveLedger(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, row *settlementRow, effects service.VideoTaskBillingEffects) error {
	var physicalID int64
	var tableEventID string
	var amount float64
	var metadataJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT id,event_id,amount_usd,metadata FROM video_task_settlement_events WHERE settlement_id=$1 AND event_type='reserve' FOR UPDATE`, row.id).Scan(&physicalID, &tableEventID, &amount, &metadataJSON)
	if err != nil || physicalID <= 0 {
		return errors.Join(service.ErrVideoTaskSettlementIntegrity, err)
	}
	planned := effects.BalanceCost
	if effects.Validate() != nil {
		return service.ErrVideoTaskSettlementIntegrity
	}
	if row.billingType == service.BillingTypeSubscription {
		if effects.BalanceCost != 0 || effects.SubscriptionCost < 0 {
			return service.ErrVideoTaskSettlementIntegrity
		}
		planned = effects.SubscriptionCost
	} else if effects.SubscriptionCost != 0 || effects.BalanceCost < 0 {
		return service.ErrVideoTaskSettlementIntegrity
	}
	planned, err = service.NormalizeVideoTaskSettlementAmount(planned)
	if err != nil {
		return service.ErrVideoTaskSettlementIntegrity
	}
	if !decimal.NewFromFloat(amount).Equal(decimal.NewFromFloat(planned)) || tableEventID != eventID(task.publicID, "reserve") {
		return service.ErrVideoTaskSettlementIntegrity
	}
	var metadata struct {
		EventID         string          `json:"event_id"`
		PublicTaskID    string          `json:"public_task_id"`
		ChargeRequestID string          `json:"charge_request_id"`
		BillingType     int8            `json:"billing_type"`
		GrossCostUSD    float64         `json:"gross_cost_usd"`
		ActualCostUSD   float64         `json:"actual_cost_usd"`
		AccountCostUSD  float64         `json:"account_cost_usd"`
		PricingSnapshot json.RawMessage `json:"pricing_snapshot"`
		EffectSnapshot  json.RawMessage `json:"effect_snapshot"`
	}
	if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
		return service.ErrVideoTaskSettlementIntegrity
	}
	if metadata.EventID != tableEventID || metadata.PublicTaskID != task.publicID || metadata.ChargeRequestID != row.chargeID || metadata.BillingType != row.billingType ||
		!sameDecimalAtScale(metadata.GrossCostUSD, row.gross, 10) || !sameDecimalAtScale(metadata.ActualCostUSD, planned, 8) || !sameDecimalAtScale(metadata.AccountCostUSD, row.accountCost, 8) ||
		!canonicalJSONEqual(metadata.PricingSnapshot, row.pricingJSON) || !canonicalJSONEqual(metadata.EffectSnapshot, row.effectsJSON) {
		return service.ErrVideoTaskSettlementIntegrity
	}
	var quote service.VideoTaskQuote
	if err := json.Unmarshal(row.pricingJSON, &quote); err == nil && quote.BillingMode == service.BillingModeVideo {
		if !sameDecimalAtScale(quote.GrossCostUSD, row.gross, 10) || !sameDecimalAtScale(quote.ActualCostUSD, planned, 8) || !sameDecimalAtScale(quote.AccountCostUSD, row.accountCost, 8) ||
			validateVideoTaskPricingAmount(quote.GrossCostUSD) != nil || service.ValidateVideoTaskSettlementAmount(quote.ActualCostUSD) != nil || service.ValidateVideoTaskSettlementAmount(quote.AccountCostUSD) != nil || quote.RateMultiplier < 0 || quote.AccountRateMultiplier < 0 {
			return service.ErrVideoTaskSettlementIntegrity
		}
		expectedGross := decimal.NewFromFloat(quote.UnitPriceUSD).Mul(decimal.NewFromInt(int64(quote.Effective.Seconds))).Mul(decimal.NewFromInt(int64(quote.Effective.VideoCount))).Round(10)
		expectedAccountBase := decimal.NewFromFloat(quote.AccountUnitPriceUSD).Mul(decimal.NewFromInt(int64(quote.Effective.Seconds))).Mul(decimal.NewFromInt(int64(quote.Effective.VideoCount))).Round(10)
		expectedActual := decimal.NewFromFloat(quote.GrossCostUSD).Mul(decimal.NewFromFloat(quote.RateMultiplier)).Round(8)
		expectedAccount := decimal.NewFromFloat(quote.AccountBaseCostUSD).Mul(decimal.NewFromFloat(quote.AccountRateMultiplier)).Round(8)
		if quote.Effective.Seconds <= 0 || quote.Effective.VideoCount <= 0 || quote.Effective.VideoCount > service.VideoTaskMaxOutputs || quote.UnitPriceUSD < 0 || quote.AccountUnitPriceUSD < 0 || quote.AccountBaseCostUSD < 0 || !decimal.NewFromFloat(quote.GrossCostUSD).Equal(expectedGross) || !decimal.NewFromFloat(quote.AccountBaseCostUSD).Equal(expectedAccountBase) || !decimal.NewFromFloat(quote.ActualCostUSD).Equal(expectedActual) || !decimal.NewFromFloat(quote.AccountCostUSD).Equal(expectedAccount) {
			return service.ErrVideoTaskSettlementIntegrity
		}
	}
	return nil
}

const usageLogMultiplierSchemaScale int32 = 4

func sameDecimal(a, b float64) bool {
	return decimal.NewFromFloat(a).Equal(decimal.NewFromFloat(b))
}

func sameDecimalAtScale(a, b float64, scale int32) bool {
	return decimal.NewFromFloat(a).Round(scale).Equal(decimal.NewFromFloat(b).Round(scale))
}

func validateVideoTaskPricingAmount(amount float64) error {
	_, err := service.NormalizeVideoTaskPricingAmount(amount)
	return err
}

func canonicalJSONEqual(a, b []byte) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	ac, errA := json.Marshal(av)
	bc, errB := json.Marshal(bv)
	return errA == nil && errB == nil && bytes.Equal(ac, bc)
}

func claimSettlementEvent(ctx context.Context, tx *sql.Tx, settlementID int64, action string, amount float64, publicID string) (bool, error) {
	var id int64
	deterministicID := eventID(publicID, action)
	err := tx.QueryRowContext(ctx, `INSERT INTO video_task_settlement_events (settlement_id,event_id,event_type,amount_usd,metadata) VALUES ($1,$2::varchar,$3,$4,jsonb_build_object('event_id',$2::varchar)) ON CONFLICT (settlement_id,event_type) DO NOTHING RETURNING id`, settlementID, deterministicID, action, amount).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := enqueueCacheInvalidationJob(ctx, tx, settlementID, action); err != nil {
		return false, err
	}
	return true, nil
}

func enqueueCacheInvalidationJob(ctx context.Context, tx *sql.Tx, settlementID int64, action string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO video_task_cache_invalidation_jobs(settlement_id,event_type,payload)
		SELECT id,$2,jsonb_build_object('Version',1,'UserID',user_id,'APIKeyID',api_key_id,'GroupID',group_id,'Platform',platform,'BillingType',billing_type,'Effects',effect_snapshot)
		FROM video_task_settlements WHERE id=$1 ON CONFLICT(settlement_id,event_type) DO NOTHING`, settlementID, action)
	return err
}

func settlementEventExists(ctx context.Context, tx *sql.Tx, settlementID int64, action string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM video_task_settlement_events WHERE settlement_id=$1 AND event_type=$2)`, settlementID, action).Scan(&exists)
	return exists, err
}

type captureSettlementIdentity struct {
	UserID          int64  `json:"user_id"`
	APIKeyID        int64  `json:"api_key_id"`
	GroupID         int64  `json:"group_id"`
	AccountID       int64  `json:"account_id"`
	Platform        string `json:"platform"`
	ChannelID       *int64 `json:"channel_id,omitempty"`
	SubscriptionID  *int64 `json:"subscription_id,omitempty"`
	UsageLogID      *int64 `json:"usage_log_id,omitempty"`
	PlatformQuotaID *int64 `json:"platform_quota_id,omitempty"`
}

type authoritativeCaptureSnapshot struct {
	CustomerCostUSD float64                         `json:"customer_cost_usd"`
	Effects         service.VideoTaskBillingEffects `json:"effects"`
	Identity        captureSettlementIdentity       `json:"identity"`
	Dimensions      captureAppliedDimensions        `json:"dimensions"`
}

type captureAppliedDimensions struct {
	AccountDaily  bool `json:"account_daily"`
	AccountWeekly bool `json:"account_weekly"`
}

func captureIdentityForAppliedEffects(ctx context.Context, tx *sql.Tx, row *settlementRow, effects service.VideoTaskBillingEffects) (captureSettlementIdentity, error) {
	identity := captureSettlementIdentity{UserID: row.userID, APIKeyID: row.apiKeyID, GroupID: row.groupID, AccountID: row.accountID, Platform: row.platform, ChannelID: nullIntPtr(row.channelID), SubscriptionID: nullIntPtr(row.subscriptionID), UsageLogID: nullIntPtr(row.usageLogID)}
	if effects.PlatformQuotaCost > 0 {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL FOR UPDATE`, row.userID, row.platform).Scan(&id); err != nil {
			return identity, err
		}
		identity.PlatformQuotaID = &id
	}
	return identity, nil
}

func storeCaptureAppliedSnapshot(ctx context.Context, tx *sql.Tx, settlementID int64, snapshot authoritativeCaptureSnapshot) error {
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	effectsJSON, err := json.Marshal(snapshot.Effects)
	if err != nil {
		return err
	}
	identityJSON, err := json.Marshal(snapshot.Identity)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE video_task_settlement_events SET metadata=metadata||jsonb_build_object('applied_snapshot',$1::jsonb,'applied_effects',$2::jsonb,'identity',$3::jsonb) WHERE settlement_id=$4 AND event_type='capture'`, snapshotJSON, effectsJSON, identityJSON, settlementID)
	if err != nil {
		return err
	}
	return requireOneRow(res, "store capture window snapshot")
}

func loadAuthoritativeCaptureSnapshot(ctx context.Context, tx *sql.Tx, row *settlementRow, publicID string, effects *service.VideoTaskBillingEffects) (*authoritativeCaptureSnapshot, error) {
	if len(row.appliedJSON) == 0 {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	var eventJSON, eventEffectsJSON, eventIdentityJSON []byte
	var tableEventID, metadataEventID string
	var eventAmount float64
	err := tx.QueryRowContext(ctx, `SELECT event_id,amount_usd,metadata->>'event_id',metadata->'applied_snapshot',metadata->'applied_effects',metadata->'identity' FROM video_task_settlement_events WHERE settlement_id=$1 AND event_type='capture'`, row.id).Scan(&tableEventID, &eventAmount, &metadataEventID, &eventJSON, &eventEffectsJSON, &eventIdentityJSON)
	if err != nil {
		return nil, err
	}
	var authoritative, eventCopy authoritativeCaptureSnapshot
	var eventEffects service.VideoTaskBillingEffects
	var eventIdentity captureSettlementIdentity
	if err := json.Unmarshal(row.appliedJSON, &authoritative); err != nil {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	if err := json.Unmarshal(eventJSON, &eventCopy); err != nil {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	if err := json.Unmarshal(eventEffectsJSON, &eventEffects); err != nil {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	if err := json.Unmarshal(eventIdentityJSON, &eventIdentity); err != nil {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	authoritativeCanonical, err := json.Marshal(authoritative)
	if err != nil {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	eventCanonical, err := json.Marshal(eventCopy)
	eventEffectsCanonical, _ := json.Marshal(eventEffects)
	eventIdentityCanonical, _ := json.Marshal(eventIdentity)
	authoritativeEffectsCanonical, _ := json.Marshal(authoritative.Effects)
	authoritativeIdentityCanonical, _ := json.Marshal(authoritative.Identity)
	if err != nil || !bytes.Equal(authoritativeCanonical, eventCanonical) || !bytes.Equal(eventEffectsCanonical, authoritativeEffectsCanonical) || !bytes.Equal(eventIdentityCanonical, authoritativeIdentityCanonical) {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	applied, identity := authoritative.Effects, authoritative.Identity
	normalizedCustomerCost, customerErr := service.NormalizeVideoTaskSettlementAmount(authoritative.CustomerCostUSD)
	normalizedEventAmount, eventAmountErr := service.NormalizeVideoTaskSettlementAmount(eventAmount)
	expectedEventID := eventID(publicID, "capture")
	if customerErr != nil || eventAmountErr != nil || !decimal.NewFromFloat(normalizedCustomerCost).Equal(decimal.NewFromFloat(row.actual)) || !decimal.NewFromFloat(normalizedEventAmount).Equal(decimal.NewFromFloat(row.actual)) || tableEventID != expectedEventID || metadataEventID != expectedEventID {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	normalized, err := applied.Normalize()
	if err != nil || !billingEffectsAmountsEqual(applied, normalized) || applied.ChargedAt.IsZero() || !row.chargedAt.Valid || !applied.ChargedAt.Equal(row.chargedAt.Time) {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	planned, err := effects.Normalize()
	if err != nil || !billingEffectsAmountsEqual(applied, planned) || !captureIdentityMatchesRow(identity, row) || !validAppliedWindows(applied, planned.WindowSnapshot, authoritative.Dimensions) {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	if row.billingType == service.BillingTypeBalance && !decimal.NewFromFloat(applied.BalanceCost).Equal(decimal.NewFromFloat(row.actual)) {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	if row.billingType == service.BillingTypeSubscription && !decimal.NewFromFloat(applied.SubscriptionCost).Equal(decimal.NewFromFloat(row.actual)) {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	if applied.PlatformQuotaCost > 0 && identity.PlatformQuotaID == nil {
		return nil, service.ErrVideoTaskSettlementIntegrity
	}
	*effects = applied
	return &authoritative, nil
}

func billingEffectsAmountsEqual(a, b service.VideoTaskBillingEffects) bool {
	av := []float64{a.BalanceCost, a.SubscriptionCost, a.APIKeyQuotaCost, a.APIKeyRateLimitCost, a.AccountQuotaCost, a.PlatformQuotaCost, a.AccountStatsCost}
	bv := []float64{b.BalanceCost, b.SubscriptionCost, b.APIKeyQuotaCost, b.APIKeyRateLimitCost, b.AccountQuotaCost, b.PlatformQuotaCost, b.AccountStatsCost}
	for i := range av {
		if !decimal.NewFromFloat(av[i]).Equal(decimal.NewFromFloat(bv[i])) {
			return false
		}
	}
	return true
}

func captureIdentityMatchesRow(i captureSettlementIdentity, row *settlementRow) bool {
	return i.UserID == row.userID && i.APIKeyID == row.apiKeyID && i.GroupID == row.groupID && i.AccountID == row.accountID && i.Platform == row.platform &&
		optionalIntMatches(i.ChannelID, row.channelID) && optionalIntMatches(i.SubscriptionID, row.subscriptionID) && optionalIntMatches(i.UsageLogID, row.usageLogID)
}

func optionalIntMatches(value *int64, row sql.NullInt64) bool {
	return (value == nil && !row.Valid) || (value != nil && row.Valid && *value == row.Int64)
}

func validAppliedWindows(applied service.VideoTaskBillingEffects, planned map[string]string, dimensions captureAppliedDimensions) bool {
	for key, value := range applied.WindowSnapshot {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return false
		}
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return false
		}
	}
	for key, value := range planned {
		if applied.WindowSnapshot[key] != value {
			return false
		}
	}
	required := []string{}
	if applied.SubscriptionCost > 0 {
		required = append(required, "subscription_daily", "subscription_weekly", "subscription_monthly")
	}
	if applied.APIKeyRateLimitCost > 0 {
		required = append(required, "api_key_5h", "api_key_1d", "api_key_7d")
	}
	if applied.PlatformQuotaCost > 0 {
		required = append(required, "platform_daily", "platform_weekly", "platform_monthly")
	}
	for _, key := range required {
		if applied.WindowSnapshot[key] == "" {
			return false
		}
	}
	for key, enabled := range map[string]bool{"account_daily": dimensions.AccountDaily, "account_weekly": dimensions.AccountWeekly} {
		_, present := applied.WindowSnapshot[key]
		if present != enabled {
			return false
		}
	}
	return true
}

func resultFor(task *settlementTaskRow, row *settlementRow, applied bool) *service.VideoTaskSettlementResult {
	result := &service.VideoTaskSettlementResult{Applied: applied, UserID: task.userID, APIKeyID: task.apiKeyID, AccountID: task.accountID, Platform: task.platform}
	result.Settlement, _ = snapshotFromRow(task.publicID, row)
	return result
}

func snapshotFromRow(publicID string, row *settlementRow) (*service.VideoTaskSettlementSnapshot, error) {
	var pricing map[string]any
	var effects service.VideoTaskBillingEffects
	if err := json.Unmarshal(row.pricingJSON, &pricing); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(row.effectsJSON, &effects); err != nil {
		return nil, err
	}
	return &service.VideoTaskSettlementSnapshot{ID: row.id, PublicTaskID: publicID, ChargeRequestID: row.chargeID, State: service.VideoTaskSettlementState(row.state), BillingType: row.billingType, UserID: row.userID, APIKeyID: row.apiKeyID, GroupID: row.groupID, AccountID: row.accountID, Platform: row.platform, ChannelID: nullIntPtr(row.channelID), SubscriptionID: nullIntPtr(row.subscriptionID), UsageLogID: nullIntPtr(row.usageLogID), GrossCostUSD: row.gross, ActualCostUSD: row.actual, AccountCostUSD: row.accountCost, RefundedCostUSD: row.refunded, PricingSnapshot: pricing, Effects: effects, ReservedAt: nullTimePtr(row.reservedAt), ChargedAt: nullTimePtr(row.chargedAt), ReleasedAt: nullTimePtr(row.releasedAt), RefundedAt: nullTimePtr(row.refundedAt), CreatedAt: row.createdAt, UpdatedAt: row.updatedAt}, nil
}

func populatePostAmounts(ctx context.Context, tx *sql.Tx, task *settlementTaskRow, result *service.VideoTaskSettlementResult, platformQuotaID *int64) error {
	if err := scanUserMoney(ctx, tx, task.userID, result); err != nil {
		return err
	}
	var key service.VideoTaskAPIKeyPostState
	var key5h, key1d, key7d sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT status,quota_used,usage_5h,usage_1d,usage_7d,window_5h_start,window_1d_start,window_7d_start FROM api_keys WHERE id=$1`, task.apiKeyID).Scan(&key.Status, &key.QuotaUsed, &key.Usage5h, &key.Usage1d, &key.Usage7d, &key5h, &key1d, &key7d); err != nil {
		return err
	}
	key.APIKeyID, key.Window5hStart, key.Window1dStart, key.Window7dStart = task.apiKeyID, nullTimePtr(key5h), nullTimePtr(key1d), nullTimePtr(key7d)
	result.PostState.APIKey = &key
	result.APIKeyQuotaUsed = &key.QuotaUsed

	var account service.VideoTaskAccountPostState
	var accountDaily, accountWeekly sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((extra->>'quota_used')::numeric,0),COALESCE((extra->>'quota_daily_used')::numeric,0),COALESCE((extra->>'quota_weekly_used')::numeric,0),extra->>'quota_daily_start',extra->>'quota_weekly_start' FROM accounts WHERE id=$1`, task.accountID).Scan(&account.TotalUsed, &account.DailyUsed, &account.WeeklyUsed, &accountDaily, &accountWeekly); err != nil {
		return err
	}
	account.AccountID, account.DailyPeriod, account.WeeklyPeriod = task.accountID, nullStringPtr(accountDaily), nullStringPtr(accountWeekly)
	result.PostState.Account = &account
	result.AccountQuotaUsed = &account.TotalUsed

	if task.subscriptionID.Valid {
		var subscription service.VideoTaskSubscriptionPostState
		var daily, weekly, monthly sql.NullTime
		err := tx.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start FROM user_subscriptions WHERE id=$1`, task.subscriptionID.Int64).Scan(&subscription.DailyUsage, &subscription.WeeklyUsage, &subscription.MonthlyUsage, &daily, &weekly, &monthly)
		if err != nil {
			return err
		}
		subscription.SubscriptionID, subscription.DailyPeriod, subscription.WeeklyPeriod, subscription.MonthlyPeriod = task.subscriptionID.Int64, nullTimePtr(daily), nullTimePtr(weekly), nullTimePtr(monthly)
		result.PostState.Subscription = &subscription
	}

	var platform service.VideoTaskPlatformPostState
	var platformDaily, platformWeekly, platformMonthly sql.NullTime
	var err error
	if platformQuotaID != nil {
		err = tx.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start FROM user_platform_quotas WHERE id=$1`, *platformQuotaID).Scan(&platform.DailyUsage, &platform.WeeklyUsage, &platform.MonthlyUsage, &platformDaily, &platformWeekly, &platformMonthly)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL`, task.userID, task.platform).Scan(&platform.DailyUsage, &platform.WeeklyUsage, &platform.MonthlyUsage, &platformDaily, &platformWeekly, &platformMonthly)
	}
	if err == nil {
		platform.UserID, platform.Platform, platform.DailyPeriod, platform.WeeklyPeriod, platform.MonthlyPeriod = task.userID, task.platform, nullTimePtr(platformDaily), nullTimePtr(platformWeekly), nullTimePtr(platformMonthly)
		result.PostState.Platform = &platform
		result.PlatformDailyUsage = &platform.DailyUsage
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if task.usageLogID.Valid {
		usage := &service.VideoTaskUsageLogPostState{UsageLogID: task.usageLogID.Int64}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(account_stats_cost,0),refunded_account_cost FROM usage_logs WHERE id=$1`, task.usageLogID.Int64).Scan(&usage.AccountStatsCost, &usage.RefundedAccountCost); err != nil {
			return err
		}
		result.PostState.UsageLog = usage
	}
	return nil
}

func scanUserMoney(ctx context.Context, tx *sql.Tx, userID int64, result *service.VideoTaskSettlementResult) error {
	var balance, frozen float64
	if err := tx.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, userID).Scan(&balance, &frozen); err != nil {
		return err
	}
	result.Balance, result.FrozenBalance = &balance, &frozen
	result.PostState.Balance = &service.VideoTaskBalancePostState{UserID: userID, Available: balance, Frozen: frozen}
	return nil
}

func requireOneRow(res sql.Result, operation string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%s affected %d rows", operation, affected)
	}
	return nil
}

func eventID(publicID, action string) string { return "video:" + publicID + ":" + action }
func timeID(t time.Time) string              { return t.UTC().Format(time.RFC3339Nano) }
func nullTimeString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
func nullableSnapshotString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullIntArg(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}
func nullIntPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
func nullTimePtr(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}
func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}
func nonNilMap(v map[string]any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	return v
}
func cloneStringMap(v map[string]string) map[string]string {
	out := make(map[string]string, len(v))
	for k, value := range v {
		out[k] = value
	}
	return out
}
