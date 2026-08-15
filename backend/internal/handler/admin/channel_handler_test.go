//go:build unit

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func float64Ptr(v float64) *float64 { return &v }
func intPtr(v int) *int             { return &v }

// ---------------------------------------------------------------------------
// 1. channelToResponse
// ---------------------------------------------------------------------------

func TestChannelToResponse_NilInput(t *testing.T) {
	require.Nil(t, channelToResponse(nil))
}

func TestChannelToResponse_FullChannel(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	ch := &service.Channel{
		ID:                 42,
		Name:               "test-channel",
		Description:        "desc",
		Status:             "active",
		BillingModelSource: "upstream",
		RestrictModels:     true,
		CreatedAt:          now,
		UpdatedAt:          now.Add(time.Hour),
		GroupIDs:           []int64{1, 2, 3},
		ModelPricing: []service.ChannelModelPricing{
			{
				ID:              10,
				Platform:        "openai",
				Models:          []string{"gpt-4"},
				BillingMode:     service.BillingModeToken,
				InputPrice:      float64Ptr(0.01),
				OutputPrice:     float64Ptr(0.03),
				CacheWritePrice: float64Ptr(0.005),
				CacheReadPrice:  float64Ptr(0.002),
				PerRequestPrice: float64Ptr(0.5),
			},
		},
		ModelMapping: map[string]map[string]string{
			"anthropic": {"claude-3-haiku": "claude-haiku-3"},
		},
	}

	resp := channelToResponse(ch)
	require.NotNil(t, resp)
	require.Equal(t, int64(42), resp.ID)
	require.Equal(t, "test-channel", resp.Name)
	require.Equal(t, "desc", resp.Description)
	require.Equal(t, "active", resp.Status)
	require.Equal(t, "upstream", resp.BillingModelSource)
	require.True(t, resp.RestrictModels)
	require.Equal(t, []int64{1, 2, 3}, resp.GroupIDs)
	require.Equal(t, "2025-06-01T12:00:00Z", resp.CreatedAt)
	require.Equal(t, "2025-06-01T13:00:00Z", resp.UpdatedAt)

	// model mapping
	require.Len(t, resp.ModelMapping, 1)
	require.Equal(t, "claude-haiku-3", resp.ModelMapping["anthropic"]["claude-3-haiku"])

	// pricing
	require.Len(t, resp.ModelPricing, 1)
	p := resp.ModelPricing[0]
	require.Equal(t, int64(10), p.ID)
	require.Equal(t, "openai", p.Platform)
	require.Equal(t, []string{"gpt-4"}, p.Models)
	require.Equal(t, "token", p.BillingMode)
	require.Equal(t, float64Ptr(0.01), p.InputPrice)
	require.Equal(t, float64Ptr(0.03), p.OutputPrice)
	require.Equal(t, float64Ptr(0.005), p.CacheWritePrice)
	require.Equal(t, float64Ptr(0.002), p.CacheReadPrice)
	require.Equal(t, float64Ptr(0.5), p.PerRequestPrice)
	require.Empty(t, p.Intervals)
}

func TestChannelToResponse_EmptyDefaults(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ch := &service.Channel{
		ID:                 1,
		Name:               "ch",
		BillingModelSource: service.BillingModelSourceChannelMapped,
		CreatedAt:          now,
		UpdatedAt:          now,
		GroupIDs:           nil,
		ModelMapping:       nil,
		ModelPricing: []service.ChannelModelPricing{
			{
				Platform:    "",
				BillingMode: "",
				Models:      []string{"m1"},
			},
		},
	}

	// handler 层 channelToResponse 现在是纯透传：BillingModelSource 的空值兜底
	// 已下放到 service 层（Create/GetByID/List/Update/ListAvailable 出口统一处理），
	// 因此这里构造 fixture 时直接传入归一化后的值。
	resp := channelToResponse(ch)
	require.Equal(t, "channel_mapped", resp.BillingModelSource)
	require.NotNil(t, resp.GroupIDs)
	require.Empty(t, resp.GroupIDs)
	require.NotNil(t, resp.ModelMapping)
	require.Empty(t, resp.ModelMapping)

	require.Len(t, resp.ModelPricing, 1)
	require.Equal(t, "anthropic", resp.ModelPricing[0].Platform)
	require.Equal(t, "token", resp.ModelPricing[0].BillingMode)
}

func TestChannelToResponse_BillingModelSourcePassthrough(t *testing.T) {
	// handler 不再兜底 BillingModelSource：空值应原样透传（由 service 层负责默认回填）。
	ch := &service.Channel{
		ID:                 1,
		Name:               "ch",
		BillingModelSource: "",
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}
	resp := channelToResponse(ch)
	require.Equal(t, "", resp.BillingModelSource, "handler 应纯透传，默认值由 service.normalizeBillingModelSource 负责")
}

func TestChannelToResponse_NilModels(t *testing.T) {
	now := time.Now()
	ch := &service.Channel{
		ID:        1,
		Name:      "ch",
		CreatedAt: now,
		UpdatedAt: now,
		ModelPricing: []service.ChannelModelPricing{
			{
				Models: nil,
			},
		},
	}

	resp := channelToResponse(ch)
	require.Len(t, resp.ModelPricing, 1)
	require.NotNil(t, resp.ModelPricing[0].Models)
	require.Empty(t, resp.ModelPricing[0].Models)
}

func TestChannelToResponse_WithIntervals(t *testing.T) {
	now := time.Now()
	ch := &service.Channel{
		ID:        1,
		Name:      "ch",
		CreatedAt: now,
		UpdatedAt: now,
		ModelPricing: []service.ChannelModelPricing{
			{
				Models:      []string{"m1"},
				BillingMode: service.BillingModePerRequest,
				Intervals: []service.PricingInterval{
					{
						ID:              100,
						MinTokens:       0,
						MaxTokens:       intPtr(1000),
						TierLabel:       "1K",
						InputPrice:      float64Ptr(0.01),
						OutputPrice:     float64Ptr(0.02),
						CacheWritePrice: float64Ptr(0.003),
						CacheReadPrice:  float64Ptr(0.001),
						PerRequestPrice: float64Ptr(0.1),
						SortOrder:       1,
					},
					{
						ID:        101,
						MinTokens: 1000,
						MaxTokens: nil,
						TierLabel: "unlimited",
						SortOrder: 2,
					},
				},
			},
		},
	}

	resp := channelToResponse(ch)
	require.Len(t, resp.ModelPricing, 1)
	intervals := resp.ModelPricing[0].Intervals
	require.Len(t, intervals, 2)

	iv0 := intervals[0]
	require.Equal(t, int64(100), iv0.ID)
	require.Equal(t, 0, iv0.MinTokens)
	require.Equal(t, intPtr(1000), iv0.MaxTokens)
	require.Equal(t, "1K", iv0.TierLabel)
	require.Equal(t, float64Ptr(0.01), iv0.InputPrice)
	require.Equal(t, float64Ptr(0.02), iv0.OutputPrice)
	require.Equal(t, float64Ptr(0.003), iv0.CacheWritePrice)
	require.Equal(t, float64Ptr(0.001), iv0.CacheReadPrice)
	require.Equal(t, float64Ptr(0.1), iv0.PerRequestPrice)
	require.Equal(t, 1, iv0.SortOrder)

	iv1 := intervals[1]
	require.Equal(t, int64(101), iv1.ID)
	require.Equal(t, 1000, iv1.MinTokens)
	require.Nil(t, iv1.MaxTokens)
	require.Equal(t, "unlimited", iv1.TierLabel)
	require.Equal(t, 2, iv1.SortOrder)
}

func TestChannelToResponse_MultipleEntries(t *testing.T) {
	now := time.Now()
	ch := &service.Channel{
		ID:        1,
		Name:      "multi",
		CreatedAt: now,
		UpdatedAt: now,
		ModelPricing: []service.ChannelModelPricing{
			{
				ID:          1,
				Platform:    "anthropic",
				Models:      []string{"claude-sonnet-4"},
				BillingMode: service.BillingModeToken,
				InputPrice:  float64Ptr(0.003),
				OutputPrice: float64Ptr(0.015),
			},
			{
				ID:              2,
				Platform:        "openai",
				Models:          []string{"gpt-4", "gpt-4o"},
				BillingMode:     service.BillingModePerRequest,
				PerRequestPrice: float64Ptr(1.0),
			},
			{
				ID:               3,
				Platform:         "gemini",
				Models:           []string{"gemini-2.5-pro"},
				BillingMode:      service.BillingModeImage,
				ImageOutputPrice: float64Ptr(0.05),
				PerRequestPrice:  float64Ptr(0.2),
			},
		},
	}

	resp := channelToResponse(ch)
	require.Len(t, resp.ModelPricing, 3)

	require.Equal(t, int64(1), resp.ModelPricing[0].ID)
	require.Equal(t, "anthropic", resp.ModelPricing[0].Platform)
	require.Equal(t, []string{"claude-sonnet-4"}, resp.ModelPricing[0].Models)
	require.Equal(t, "token", resp.ModelPricing[0].BillingMode)

	require.Equal(t, int64(2), resp.ModelPricing[1].ID)
	require.Equal(t, "openai", resp.ModelPricing[1].Platform)
	require.Equal(t, []string{"gpt-4", "gpt-4o"}, resp.ModelPricing[1].Models)
	require.Equal(t, "per_request", resp.ModelPricing[1].BillingMode)

	require.Equal(t, int64(3), resp.ModelPricing[2].ID)
	require.Equal(t, "gemini", resp.ModelPricing[2].Platform)
	require.Equal(t, []string{"gemini-2.5-pro"}, resp.ModelPricing[2].Models)
	require.Equal(t, "image", resp.ModelPricing[2].BillingMode)
	require.Equal(t, float64Ptr(0.05), resp.ModelPricing[2].ImageOutputPrice)
}

// ---------------------------------------------------------------------------
// 2. pricingRequestToService
// ---------------------------------------------------------------------------

func TestChannelModelPricingRequestDescriptionValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		description string
		wantErr     bool
	}{
		{
			name:        "500 characters accepted",
			description: strings.Repeat("a", 500),
			wantErr:     false,
		},
		{
			name:        "501 characters rejected",
			description: strings.Repeat("a", 501),
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"name": "ch",
				"model_pricing": []map[string]any{
					{
						"models":      []string{"m1"},
						"description": tt.description,
					},
				},
			})
			require.NoError(t, err)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/channels", strings.NewReader(string(payload)))
			c.Request.Header.Set("Content-Type", "application/json")

			var req createChannelRequest
			err = c.ShouldBindJSON(&req)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.description, req.ModelPricing[0].Description)
		})
	}
}

func TestAccountStatsPricingNestedModelsValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		payload      map[string]any
		bindAndCheck func(t *testing.T, payload map[string]any)
	}{
		{
			name: "create rejects missing models",
			payload: map[string]any{
				"name": "ch",
				"account_stats_pricing_rules": []map[string]any{{
					"name":      "stats",
					"group_ids": []int64{1},
					"pricing": []map[string]any{{
						"billing_mode": "token",
					}},
				}},
			},
			bindAndCheck: func(t *testing.T, payload map[string]any) {
				var req createChannelRequest
				require.Error(t, bindAdminJSON(t, http.MethodPost, payload, &req))
			},
		},
		{
			name: "create rejects empty models",
			payload: map[string]any{
				"name": "ch",
				"account_stats_pricing_rules": []map[string]any{{
					"name":      "stats",
					"group_ids": []int64{1},
					"pricing": []map[string]any{{
						"models":       []string{},
						"billing_mode": "token",
					}},
				}},
			},
			bindAndCheck: func(t *testing.T, payload map[string]any) {
				var req createChannelRequest
				require.Error(t, bindAdminJSON(t, http.MethodPost, payload, &req))
			},
		},
		{
			name: "update rejects missing models",
			payload: map[string]any{
				"account_stats_pricing_rules": []map[string]any{{
					"name":      "stats",
					"group_ids": []int64{1},
					"pricing": []map[string]any{{
						"billing_mode": "token",
					}},
				}},
			},
			bindAndCheck: func(t *testing.T, payload map[string]any) {
				var req updateChannelRequest
				require.Error(t, bindAdminJSON(t, http.MethodPut, payload, &req))
			},
		},
		{
			name: "update rejects empty models",
			payload: map[string]any{
				"account_stats_pricing_rules": []map[string]any{{
					"name":      "stats",
					"group_ids": []int64{1},
					"pricing": []map[string]any{{
						"models":       []string{},
						"billing_mode": "token",
					}},
				}},
			},
			bindAndCheck: func(t *testing.T, payload map[string]any) {
				var req updateChannelRequest
				require.Error(t, bindAdminJSON(t, http.MethodPut, payload, &req))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.bindAndCheck(t, tt.payload)
		})
	}
}

func bindAdminJSON(t *testing.T, method string, payload map[string]any, out any) error {
	t.Helper()

	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/channels", strings.NewReader(string(raw)))
	c.Request.Header.Set("Content-Type", "application/json")

	return c.ShouldBindJSON(out)
}

func TestPricingRequestToService_DescriptionScope(t *testing.T) {
	reqs := []channelModelPricingRequest{
		{
			Models:      []string{"m1"},
			Description: " \nFirst line\nSecond line\t ",
		},
	}

	primary := pricingRequestToService(reqs, pricingScopePrimary)
	require.Len(t, primary, 1)
	require.Equal(t, "First line\nSecond line", primary[0].Description)

	accountStats := pricingRequestToService(reqs, pricingScopeAccountStats)
	require.Len(t, accountStats, 1)
	require.Empty(t, accountStats[0].Description)
}

func TestChannelToResponse_DescriptionScope(t *testing.T) {
	now := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	ch := &service.Channel{
		ID:        1,
		Name:      "ch",
		CreatedAt: now,
		UpdatedAt: now,
		ModelPricing: []service.ChannelModelPricing{
			{Models: []string{"primary-described"}, Description: "visible"},
			{Models: []string{"primary-empty"}, Description: ""},
		},
		AccountStatsPricingRules: []service.AccountStatsPricingRule{
			{
				ID:   10,
				Name: "stats",
				Pricing: []service.ChannelModelPricing{
					{Models: []string{"stats-described"}, Description: "hidden"},
				},
			},
		},
	}

	resp := channelToResponse(ch)
	require.Len(t, resp.ModelPricing, 2)
	require.NotNil(t, resp.ModelPricing[0].Description)
	require.Equal(t, "visible", *resp.ModelPricing[0].Description)
	require.NotNil(t, resp.ModelPricing[1].Description)
	require.Empty(t, *resp.ModelPricing[1].Description)

	require.Len(t, resp.AccountStatsPricingRules, 1)
	require.Len(t, resp.AccountStatsPricingRules[0].Pricing, 1)
	require.Nil(t, resp.AccountStatsPricingRules[0].Pricing[0].Description)
}

func TestPricingRequestToService_Defaults(t *testing.T) {
	tests := []struct {
		name      string
		req       channelModelPricingRequest
		wantField string // which default field to check
		wantValue string
	}{
		{
			name: "empty billing mode defaults to token",
			req: channelModelPricingRequest{
				Models:      []string{"m1"},
				BillingMode: "",
			},
			wantField: "BillingMode",
			wantValue: string(service.BillingModeToken),
		},
		{
			name: "empty platform stays empty",
			req: channelModelPricingRequest{
				Models:   []string{"m1"},
				Platform: "",
			},
			wantField: "Platform",
			wantValue: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pricingRequestToService([]channelModelPricingRequest{tt.req}, pricingScopePrimary)
			require.Len(t, result, 1)
			switch tt.wantField {
			case "BillingMode":
				require.Equal(t, service.BillingMode(tt.wantValue), result[0].BillingMode)
			case "Platform":
				require.Equal(t, tt.wantValue, result[0].Platform)
			}
		})
	}
}

func TestPricingRequestToService_WithAllFields(t *testing.T) {
	reqs := []channelModelPricingRequest{
		{
			Platform:         "openai",
			Models:           []string{"gpt-4", "gpt-4o"},
			BillingMode:      "per_request",
			InputPrice:       float64Ptr(0.01),
			OutputPrice:      float64Ptr(0.03),
			CacheWritePrice:  float64Ptr(0.005),
			CacheReadPrice:   float64Ptr(0.002),
			ImageOutputPrice: float64Ptr(0.04),
			PerRequestPrice:  float64Ptr(0.5),
		},
	}

	result := pricingRequestToService(reqs, pricingScopePrimary)
	require.Len(t, result, 1)
	r := result[0]
	require.Equal(t, "openai", r.Platform)
	require.Equal(t, []string{"gpt-4", "gpt-4o"}, r.Models)
	require.Equal(t, service.BillingModePerRequest, r.BillingMode)
	require.Equal(t, float64Ptr(0.01), r.InputPrice)
	require.Equal(t, float64Ptr(0.03), r.OutputPrice)
	require.Equal(t, float64Ptr(0.005), r.CacheWritePrice)
	require.Equal(t, float64Ptr(0.002), r.CacheReadPrice)
	require.Equal(t, float64Ptr(0.04), r.ImageOutputPrice)
	require.Equal(t, float64Ptr(0.5), r.PerRequestPrice)
}

func TestPricingRequestToService_WithIntervals(t *testing.T) {
	reqs := []channelModelPricingRequest{
		{
			Models:      []string{"m1"},
			BillingMode: "per_request",
			Intervals: []pricingIntervalRequest{
				{
					MinTokens:       0,
					MaxTokens:       intPtr(2000),
					TierLabel:       "small",
					InputPrice:      float64Ptr(0.01),
					OutputPrice:     float64Ptr(0.02),
					CacheWritePrice: float64Ptr(0.003),
					CacheReadPrice:  float64Ptr(0.001),
					PerRequestPrice: float64Ptr(0.1),
					SortOrder:       1,
				},
				{
					MinTokens: 2000,
					MaxTokens: nil,
					TierLabel: "large",
					SortOrder: 2,
				},
			},
		},
	}

	result := pricingRequestToService(reqs, pricingScopePrimary)
	require.Len(t, result, 1)
	require.Len(t, result[0].Intervals, 2)

	iv0 := result[0].Intervals[0]
	require.Equal(t, 0, iv0.MinTokens)
	require.Equal(t, intPtr(2000), iv0.MaxTokens)
	require.Equal(t, "small", iv0.TierLabel)
	require.Equal(t, float64Ptr(0.01), iv0.InputPrice)
	require.Equal(t, float64Ptr(0.02), iv0.OutputPrice)
	require.Equal(t, float64Ptr(0.003), iv0.CacheWritePrice)
	require.Equal(t, float64Ptr(0.001), iv0.CacheReadPrice)
	require.Equal(t, float64Ptr(0.1), iv0.PerRequestPrice)
	require.Equal(t, 1, iv0.SortOrder)

	iv1 := result[0].Intervals[1]
	require.Equal(t, 2000, iv1.MinTokens)
	require.Nil(t, iv1.MaxTokens)
	require.Equal(t, "large", iv1.TierLabel)
	require.Equal(t, 2, iv1.SortOrder)
}

func TestPricingRequestToService_EmptySlice(t *testing.T) {
	result := pricingRequestToService([]channelModelPricingRequest{}, pricingScopePrimary)
	require.NotNil(t, result)
	require.Empty(t, result)
}

func TestPricingRequestToService_NilPriceFields(t *testing.T) {
	reqs := []channelModelPricingRequest{
		{
			Models:      []string{"m1"},
			BillingMode: "token",
			// all price fields are nil by default
		},
	}

	result := pricingRequestToService(reqs, pricingScopePrimary)
	require.Len(t, result, 1)
	r := result[0]
	require.Nil(t, r.InputPrice)
	require.Nil(t, r.OutputPrice)
	require.Nil(t, r.CacheWritePrice)
	require.Nil(t, r.CacheReadPrice)
	require.Nil(t, r.ImageOutputPrice)
	require.Nil(t, r.PerRequestPrice)
}

// ---------------------------------------------------------------------------
// 3. SyncPricingModels handler
// ---------------------------------------------------------------------------

func setupSyncPricingModelsRouter(pricingSvc *service.PricingService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &ChannelHandler{pricingService: pricingSvc}
	router.GET("/channels/pricing/sync-models", h.SyncPricingModels)
	return router
}

func TestSyncPricingModels_MissingPlatform(t *testing.T) {
	svc := service.NewPricingService(nil, nil)
	router := setupSyncPricingModelsRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/channels/pricing/sync-models", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSyncPricingModels_UnsupportedPlatform(t *testing.T) {
	svc := service.NewPricingService(nil, nil)
	router := setupSyncPricingModelsRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/channels/pricing/sync-models?platform=unknown", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSyncPricingModels_ValidPlatform_EmptyService(t *testing.T) {
	svc := service.NewPricingService(nil, nil)
	router := setupSyncPricingModelsRouter(svc)

	for _, platform := range []string{"anthropic", "openai", "gemini", "antigravity"} {
		req := httptest.NewRequest(http.MethodGet, "/channels/pricing/sync-models?platform="+platform, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, "platform=%s", platform)

		var body struct {
			Data struct {
				Models []string `json:"models"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.NotNil(t, body.Data.Models, "models must not be null for platform=%s", platform)
	}
}
