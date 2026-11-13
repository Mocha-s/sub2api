package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMegaAiDurationDoesNotExcludeMatchingAccount(t *testing.T) {
	account := &Account{ID: 736, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"base_url": "https://newapi.megabyai.cc", "api_key": "test-key",
		"model_mapping": map[string]any{"T1-seedance-2.5": "T1-seedance-2.5"},
	}}
	body := []byte(`{"model":"T1-seedance-2.5","prompt":"waves","duration":15}`)
	provider := NewAccountVideoTaskProvider(nil).(*accountVideoTaskProvider)
	require.True(t, account.IsModelSupported("T1-seedance-2.5"))
	err := provider.ValidateCreate(context.Background(), account, body, "application/json", "T1-seedance-2.5")
	require.Error(t, err)
	require.True(t, isVideoTaskPreSubmitAdapterMismatch(err))
	account.Credentials[VideoAdapterMetadataKey] = VideoAdapterMegaAi
	require.NoError(t, provider.ValidateCreate(context.Background(), account, body, "application/json", "T1-seedance-2.5"))
	forwarded, err := megaAiCreateBody(body, "T1-seedance-2.5")
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(forwarded, &payload))
	require.Equal(t, float64(15), payload["duration"])
	require.NotContains(t, payload, "seconds")
}
