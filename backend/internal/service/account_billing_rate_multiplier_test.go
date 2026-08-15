package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccount_BillingRateMultiplier_DefaultsToOneWhenNil(t *testing.T) {
	var a Account
	require.NoError(t, json.Unmarshal([]byte(`{"id":1,"name":"acc","status":"active"}`), &a))
	require.Nil(t, a.RateMultiplier)
	require.Equal(t, 1.0, a.BillingRateMultiplier())
}

func TestAccount_BillingRateMultiplier_AllowsZero(t *testing.T) {
	v := 0.0
	a := Account{RateMultiplier: &v}
	require.Equal(t, 0.0, a.BillingRateMultiplier())
}

func TestAccount_BillingRateMultiplier_NegativeFallsBackToOne(t *testing.T) {
	v := -1.0
	a := Account{RateMultiplier: &v}
	require.Equal(t, 1.0, a.BillingRateMultiplier())
}

func TestAccount_ManagedPricingMarkupAffectsRequestRate(t *testing.T) {
	rate := 2.0
	a := Account{
		RateMultiplier: &rate,
		Credentials: map[string]any{
			"pricing_managed_by":    "api-pricing-sync",
			"pricing_markup_factor": 1.25,
		},
	}

	require.Equal(t, 2.0, a.BillingRateMultiplier())
	require.True(t, a.UsesManagedBillingRate())
	require.InDelta(t, 1.25, a.ManagedPricingMarkupFactor(), 1e-12)
	require.InDelta(t, 2.5, effectiveRequestRateMultiplier(&a, 9.9), 1e-12)

	a.RateMultiplier = nil
	require.Equal(t, 1.0, a.BillingRateMultiplier())
	require.InDelta(t, 1.25, effectiveRequestRateMultiplier(&a, 9.9), 1e-12)
}

func TestAccount_ManagedPricingMarkupRejectsInvalidFactors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		credentials map[string]any
	}{
		{name: "missing", credentials: map[string]any{}},
		{name: "below one", credentials: map[string]any{"pricing_markup_factor": 0.5}},
		{name: "malformed string", credentials: map[string]any{"pricing_markup_factor": "bad"}},
		{name: "nan string", credentials: map[string]any{"pricing_markup_factor": "NaN"}},
		{name: "infinite string", credentials: map[string]any{"pricing_markup_factor": "+Inf"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Account{Credentials: map[string]any{"pricing_managed_by": "api-pricing-sync"}}
			for k, v := range tc.credentials {
				a.Credentials[k] = v
			}

			require.Equal(t, 1.0, a.ManagedPricingMarkupFactor())
		})
	}
}
