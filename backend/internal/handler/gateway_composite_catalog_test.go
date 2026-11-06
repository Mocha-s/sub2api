package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type compositeCatalogTargetRepoStub struct {
	service.CompositeGroupTargetRepository
	targets []service.CompositeGroupTarget
}

func (s compositeCatalogTargetRepoStub) ListByCompositeGroup(_ context.Context, groupID int64, _ bool) ([]service.CompositeGroupTarget, error) {
	out := make([]service.CompositeGroupTarget, 0, len(s.targets))
	for _, target := range s.targets {
		if target.CompositeGroupID == groupID {
			out = append(out, target)
		}
	}
	return out, nil
}

// compositeCatalogAccountRepoStub 按生产 SQL 语义实现按平台查询：composite 组本身
// 没有账号，所以 Gemini 选号在 composite 组上必然查空。
type compositeCatalogAccountRepoStub struct {
	gatewayModelsAccountRepoStub
}

func (s *compositeCatalogAccountRepoStub) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]service.Account, error) {
	out := make([]service.Account, 0)
	for _, acc := range s.byGroup[groupID] {
		for _, p := range platforms {
			if acc.Platform == p {
				out = append(out, acc)
				break
			}
		}
	}
	return out, nil
}

// composite 分组自身不挂账号，模型都来自目标组。
func newCompositeCatalogHandlerForTest(t *testing.T) (*GatewayHandler, *service.APIKey) {
	t.Helper()
	const compositeID, geminiID, openAIID int64 = 9410, 9415, 9402
	accounts := &compositeCatalogAccountRepoStub{gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{
		geminiID: {{ID: 1, Platform: service.PlatformGemini, Status: service.StatusActive, Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"gemini-3.5-flash": "gemini-3.5-flash"}}}},
		openAIID: {{ID: 2, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"}}}},
	}}}
	resolver := service.NewCompositeRouteResolver(&gatewayModelsRouteRepoStub{})
	resolver.SetTargetRepository(compositeCatalogTargetRepoStub{targets: []service.CompositeGroupTarget{
		{CompositeGroupID: compositeID, TargetGroupID: geminiID, Priority: 1, Enabled: true,
			TargetGroup: &service.Group{ID: geminiID, Platform: service.PlatformGemini, Status: service.StatusActive, Hydrated: true}},
		{CompositeGroupID: compositeID, TargetGroupID: openAIID, Priority: 1, Enabled: true,
			TargetGroup: &service.Group{ID: openAIID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}},
	}})
	h := &GatewayHandler{
		gatewayService: service.NewGatewayService(
			accounts,
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, resolver, nil, nil,
		),
		// composite 组上没有 Gemini 账号：若仍走旧的选号路径会得到 503。
		geminiCompatService: service.NewGeminiMessagesCompatService(accounts, nil, nil, nil, nil, nil, nil, nil, nil),
	}
	groupID := compositeID
	return h, &service.APIKey{GroupID: &groupID, Group: &service.Group{ID: compositeID, Platform: service.PlatformComposite}}
}

func TestGeminiV1BetaListModels_CompositeListsGeminiTargetsOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, apiKey := newCompositeCatalogHandlerForTest(t)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	// 与 compositeGeminiTargetPlatformMiddleware 一致：列表请求不带模型名，只标记 gemini 平台。
	c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), service.PlatformGemini))
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	h.GeminiV1BetaListModels(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	names := make([]string, 0, len(got.Models))
	for _, model := range got.Models {
		names = append(names, model.Name)
	}
	require.Equal(t, []string{"models/gemini-3.5-flash"}, names)
}
