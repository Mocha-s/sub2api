//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeMediaRefundUnits(t *testing.T) {
	tests := []struct {
		name        string
		requested   int
		delivered   int
		wantMissing int
		wantRatio   float64
		wantError   bool
	}{
		{name: "all delivered", requested: 2, delivered: 2, wantMissing: 0, wantRatio: 0},
		{name: "partial delivery", requested: 2, delivered: 1, wantMissing: 1, wantRatio: 0.5},
		{name: "no delivery", requested: 2, delivered: 0, wantMissing: 2, wantRatio: 1},
		{name: "delivered capped", requested: 2, delivered: 3, wantMissing: 0, wantRatio: 0},
		{name: "invalid requested", requested: 0, delivered: 0, wantError: true},
		{name: "invalid delivered", requested: 2, delivered: -1, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			missing, ratio, err := NormalizeMediaRefundUnits(tt.requested, tt.delivered)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantMissing, missing)
			require.InDelta(t, tt.wantRatio, ratio, 1e-12)
		})
	}
}
