//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestValidateReserveLedgerIndependentBilledSeconds(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		mode                  service.BillingMode
		billed, accountBilled int
		gross, accountCost    float64
		valid                 bool
	}{
		{"ordinary video with account surcharge", service.BillingModeVideo, 0, 8, 1, 0.8, true},
		{"independent reference surcharges", service.BillingModeVideoReference, 7, 8, 1.4, 0.8, true},
		{"historical snapshot", service.BillingModeVideoReference, 0, 0, 1, 0.5, true},
		{"tampered account duration", service.BillingModeVideoReference, 7, 7, 1.4, 0.8, false},
		{"tampered customer duration", service.BillingModeVideoReference, 6, 8, 1.4, 0.8, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			mock.ExpectBegin()
			tx, err := db.Begin()
			require.NoError(t, err)
			quote := service.VideoTaskQuote{
				BillingMode: tt.mode, Effective: service.VideoTaskEffectiveParams{Seconds: 5, Resolution: "720p", VideoCount: 1},
				BillingSeconds: tt.billed, AccountBillingSeconds: tt.accountBilled,
				UnitPriceUSD: 0.2, GrossCostUSD: tt.gross, ActualCostUSD: tt.gross, RateMultiplier: 1,
				AccountUnitPriceUSD: 0.1, AccountBaseCostUSD: tt.accountCost, AccountCostUSD: tt.accountCost, AccountRateMultiplier: 1,
			}
			pricingJSON, err := json.Marshal(quote)
			require.NoError(t, err)
			effects := service.VideoTaskBillingEffects{BalanceCost: tt.gross, AccountStatsCost: tt.accountCost}
			effectsJSON, err := json.Marshal(effects)
			require.NoError(t, err)
			task := &settlementTaskRow{publicID: "extra_seconds"}
			row := &settlementRow{id: 1, chargeID: eventID(task.publicID, "charge"), billingType: service.BillingTypeBalance,
				gross: tt.gross, actual: tt.gross, accountCost: tt.accountCost, pricingJSON: pricingJSON, effectsJSON: effectsJSON}
			metadataJSON, err := json.Marshal(map[string]any{
				"event_id": eventID(task.publicID, "reserve"), "public_task_id": task.publicID, "charge_request_id": row.chargeID,
				"billing_type": row.billingType, "gross_cost_usd": tt.gross, "actual_cost_usd": tt.gross, "account_cost_usd": tt.accountCost,
				"pricing_snapshot": quote, "effect_snapshot": effects,
			})
			require.NoError(t, err)
			mock.ExpectQuery(`SELECT id,event_id,amount_usd,metadata FROM video_task_settlement_events`).
				WithArgs(int64(1)).
				WillReturnRows(sqlmock.NewRows([]string{"id", "event_id", "amount_usd", "metadata"}).
					AddRow(int64(2), eventID(task.publicID, "reserve"), tt.gross, metadataJSON))
			err = validateReserveLedger(context.Background(), tx, task, row, effects)
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
			}
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
