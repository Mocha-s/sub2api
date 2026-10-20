package routes

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type compositeRouteRepoStub struct {
	routes []service.CompositeModelRoute
}

type compositeTargetRepoStub struct {
	targets []service.CompositeGroupTarget
}

func (s compositeTargetRepoStub) ListByCompositeGroup(_ context.Context, groupID int64, _ bool) ([]service.CompositeGroupTarget, error) {
	out := make([]service.CompositeGroupTarget, 0, len(s.targets))
	for _, target := range s.targets {
		if target.CompositeGroupID == groupID {
			out = append(out, target)
		}
	}
	return out, nil
}

func (compositeTargetRepoStub) Create(context.Context, *service.CompositeGroupTarget) error {
	return nil
}
func (compositeTargetRepoStub) Update(context.Context, *service.CompositeGroupTarget) error {
	return nil
}
func (compositeTargetRepoStub) Delete(context.Context, int64, int64) error { return nil }

func (s compositeRouteRepoStub) ListByGroup(ctx context.Context, groupID int64, includeDisabled bool) ([]service.CompositeModelRoute, error) {
	routes := make([]service.CompositeModelRoute, 0, len(s.routes))
	for _, route := range s.routes {
		if route.GroupID != groupID {
			continue
		}
		if !includeDisabled && !route.Enabled {
			continue
		}
		routes = append(routes, route)
	}
	return routes, nil
}

func (s compositeRouteRepoStub) Create(ctx context.Context, route *service.CompositeModelRoute) error {
	return nil
}

func (s compositeRouteRepoStub) Update(ctx context.Context, route *service.CompositeModelRoute) error {
	return nil
}

func (s compositeRouteRepoStub) Delete(ctx context.Context, id int64) error {
	return nil
}

func (s compositeRouteRepoStub) DeleteByGroup(ctx context.Context, groupID int64) error {
	return nil
}

func TestCompositeTargetPlatformMiddlewareResolvesModelAndRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		groupID := int64(1)
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{Platform: service.PlatformComposite},
		})
		c.Next()
	})))
	router.Use(compositeTargetPlatformMiddleware(nil))
	router.POST("/", func(c *gin.Context) {
		platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, service.PlatformOpenAI, platform)

		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"gpt-5"}`, string(body))
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"model":"gpt-5"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestCompositeTargetPlatformMiddlewareUsesExplicitRouteAndRewritesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{
		routes: []service.CompositeModelRoute{
			{
				ID:             1,
				GroupID:        1,
				PublicModel:    "openrouter/gpt-5",
				MatchType:      service.CompositeRouteMatchExact,
				TargetPlatform: service.PlatformOpenAI,
				UpstreamModel:  "gpt-5",
				Endpoint:       service.CompositeRouteEndpointAny,
				Priority:       100,
				Enabled:        true,
			},
		},
	})
	router.Use(gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		groupID := int64(1)
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformComposite},
		})
		c.Next()
	})))
	router.Use(compositeTargetPlatformMiddleware(resolver))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, service.PlatformOpenAI, platform)

		upstreamModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, "gpt-5", upstreamModel)

		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"gpt-5","messages":[]}`, string(body))
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openrouter/gpt-5","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestCompositeTargetPlatformMiddlewareInstallsEffectiveTargetAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID, targetID := int64(1), int64(20)
	resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{routes: []service.CompositeModelRoute{{
		ID: 1, GroupID: groupID, PublicModel: "public-gpt", MatchType: service.CompositeRouteMatchExact,
		TargetPlatform: service.PlatformOpenAI, TargetGroupID: &targetID, Endpoint: service.CompositeRouteEndpointResponses, Enabled: true,
	}}})
	resolver.SetTargetRepository(compositeTargetRepoStub{targets: []service.CompositeGroupTarget{{
		ID: 10, CompositeGroupID: groupID, TargetGroupID: targetID, Priority: 1, Enabled: true,
		TargetGroup: &service.Group{ID: targetID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true},
	}}})
	router := gin.New()
	router.Use(gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformComposite, Status: service.StatusActive, Hydrated: true}})
		c.Next()
	})))
	router.Use(compositeTargetPlatformMiddleware(resolver))
	router.POST("/v1/responses", func(c *gin.Context) {
		apiKey, ok := servermiddleware.GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.Equal(t, int64(99), apiKey.ID)
		require.Equal(t, targetID, *apiKey.GroupID)
		require.Equal(t, service.PlatformOpenAI, apiKey.Group.Platform)
		billingGroup, ok := service.CompositeBillingGroupFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, groupID, billingGroup.ID)
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"public-gpt"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestCompositeTargetPlatformMiddlewareRewritesNestedLiveModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{
		routes: []service.CompositeModelRoute{
			{
				ID:             1,
				GroupID:        1,
				PublicModel:    "live-alias",
				MatchType:      service.CompositeRouteMatchExact,
				TargetPlatform: service.PlatformOpenAI,
				UpstreamModel:  "gpt-live",
				Endpoint:       service.CompositeRouteEndpointAny,
				Priority:       100,
				Enabled:        true,
			},
		},
	})
	router.Use(gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		groupID := int64(1)
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformComposite},
		})
		c.Next()
	})))
	router.Use(compositeTargetPlatformMiddleware(resolver))
	router.POST("/backend-api/codex/realtime/calls", func(c *gin.Context) {
		platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, service.PlatformOpenAI, platform)

		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"session":{"model":"gpt-live"},"sdp":"v=0"}`, string(body))
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(
		http.MethodPost,
		"/backend-api/codex/realtime/calls",
		strings.NewReader(`{"session":{"model":"live-alias"},"sdp":"v=0"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
}

// Scenario: 回归 —— openai OAuth 与 deepseek API Key 同时映射 gpt-6-astra 时，
// 请求必须在路由层被拒绝，不能落到默认 Anthropic handler 后再按模型名猜平台。
// 仅 API Key 的跨平台歧义保持原行为（放行且不解析平台）。
func TestCompositeTargetPlatformMiddlewareRejectsOnlyOAuthAmbiguousOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name         string
		oauthClaimed bool
		wantStatus   int
	}{
		{name: "oauth claimant", oauthClaimed: true, wantStatus: http.StatusBadRequest},
		{name: "apikey only", oauthClaimed: false, wantStatus: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := service.NewCompositeRouteResolver(nil)
			resolver.SetModelOwnershipResolver(func(context.Context, int64, string) (service.CompositeModelOwnership, error) {
				return service.CompositeModelOwnership{Ambiguous: true, OpenAIGatewayOAuthClaimed: tc.oauthClaimed}, nil
			})
			router := gin.New()
			router.Use(gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
				groupID := int64(8)
				c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
					GroupID: &groupID,
					Group:   &service.Group{ID: groupID, Platform: service.PlatformComposite},
				})
				c.Next()
			})))
			router.Use(compositeTargetPlatformMiddleware(resolver))
			router.POST("/v1/responses", func(c *gin.Context) {
				_, resolved := service.ResolvedTargetPlatformFromContext(c.Request.Context())
				require.False(t, resolved)
				c.Status(http.StatusNoContent)
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-6-astra"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, tc.wantStatus, w.Code)
			if tc.oauthClaimed {
				require.Contains(t, w.Body.String(), "explicit composite route")
			}
		})
	}
}

func TestCompositeRequestModelFromMultipartLiveSession(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("sdp", "v=0"))
	require.NoError(t, writer.WriteField("session", `{"model":"live-alias"}`))
	require.NoError(t, writer.Close())

	require.Equal(t, "live-alias", requestmodel.FromBody(writer.FormDataContentType(), body.Bytes()))
}

func TestCompositeCodexControlPathsUseResponsesRoutes(t *testing.T) {
	for _, path := range []string{
		"/v1/alpha/search",
		"/backend-api/codex/alpha/search",
		"/v1/live",
		"/backend-api/codex/realtime/calls",
	} {
		require.Equal(t, service.CompositeRouteEndpointResponses, compositeRouteEndpointForPath(path), "path=%s", path)
	}
}

func TestCompositeTargetPlatformMiddlewareUsesExplicitRouteForMultipartImages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{
		routes: []service.CompositeModelRoute{
			{
				ID:             1,
				GroupID:        1,
				PublicModel:    "image-alias",
				MatchType:      service.CompositeRouteMatchExact,
				TargetPlatform: service.PlatformOpenAI,
				UpstreamModel:  "gpt-image-1",
				Endpoint:       service.CompositeRouteEndpointImages,
				Priority:       100,
				Enabled:        true,
			},
		},
	})
	router.Use(gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		groupID := int64(1)
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformComposite},
		})
		c.Next()
	})))
	router.Use(compositeTargetPlatformMiddleware(resolver))
	router.POST("/v1/images/edits", func(c *gin.Context) {
		platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, service.PlatformOpenAI, platform)

		upstreamModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, "gpt-image-1", upstreamModel)

		publicModel, ok := service.RequestedPublicModelFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, "image-alias", publicModel)

		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Contains(t, string(body), "image-alias")
		c.Status(http.StatusNoContent)
	})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "image-alias"))
	require.NoError(t, writer.WriteField("prompt", "draw"))
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestCompositeTargetPlatformMiddlewareUsesExplicitVideoRouteAndRewritesBody(t *testing.T) {
	for _, path := range []string{"/v1/videos", "/v1/video/generations"} {
		t.Run(path, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{
				routes: []service.CompositeModelRoute{
					{
						ID:             1,
						GroupID:        1,
						PublicModel:    "video-alias",
						MatchType:      service.CompositeRouteMatchExact,
						TargetPlatform: service.PlatformAnthropic,
						UpstreamModel:  "claude-wrong",
						Endpoint:       service.CompositeRouteEndpointAny,
						Priority:       1,
						Enabled:        true,
					},
					{
						ID:             2,
						GroupID:        1,
						PublicModel:    "video-alias",
						MatchType:      service.CompositeRouteMatchExact,
						TargetPlatform: service.PlatformOpenAI,
						UpstreamModel:  "sora-upstream",
						Endpoint:       "video",
						Priority:       100,
						Enabled:        true,
					},
				},
			})
			router.Use(gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
				groupID := int64(1)
				c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
					GroupID: &groupID,
					Group:   &service.Group{ID: groupID, Platform: service.PlatformComposite},
				})
				c.Next()
			})))
			router.Use(compositeTargetPlatformMiddleware(resolver))
			router.POST(path, func(c *gin.Context) {
				platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
				require.True(t, ok)
				require.Equal(t, service.PlatformOpenAI, platform)

				upstreamModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context())
				require.True(t, ok)
				require.Equal(t, "sora-upstream", upstreamModel)

				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.JSONEq(t, `{"model":"sora-upstream","prompt":"waves"}`, string(body))
				c.Status(http.StatusNoContent)
			})

			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"video-alias","prompt":"waves"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusNoContent, w.Code)
		})
	}
}

func TestCompositeRouteEndpointForPathMapsDurableVideoPaths(t *testing.T) {
	for _, path := range []string{
		"/v1/videos",
		"/videos",
		"/v1/videos/estimate",
		"/videos/estimate",
		"/v1/videos/references",
		"/videos/references",
		"/v1/videos/material-assets",
		"/videos/material-assets",
		"/v1/videos/task_123/refresh",
		"/videos/task_123/refresh",
		"/v1/videos/task_123/cancel",
		"/videos/task_123/cancel",
		"/v1/videos/task_123",
		"/videos/task_123",
		"/v1/videos/task_123/content",
		"/videos/task_123/content",
		"/v1/videos/generations/task_123",
		"/videos/generations/task_123",
		"/v1/videos/generations/task_123/content",
		"/videos/generations/task_123/content",
		"/v1/video/generations",
		"/video/generations",
		"/v1/video/generations/estimate",
		"/video/generations/estimate",
		"/v1/video/generations/references",
		"/video/generations/references",
		"/v1/video/generations/material-assets",
		"/video/generations/material-assets",
		"/v1/video/generations/task_123/refresh",
		"/video/generations/task_123/refresh",
		"/v1/video/generations/task_123/cancel",
		"/video/generations/task_123/cancel",
		"/v1/video/generations/task_123",
		"/video/generations/task_123",
		"/v1/video/generations/task_123/content",
		"/video/generations/task_123/content",
	} {
		require.Equal(t, "video", compositeRouteEndpointForPath(path), "path=%s", path)
	}

	for _, path := range []string{
		"/v1/videos/generations",
		"/videos/generations",
		"/v1/videos/edits",
		"/videos/edits",
		"/v1/videos/extensions",
		"/videos/extensions",
	} {
		require.Equal(t, service.CompositeRouteEndpointAny, compositeRouteEndpointForPath(path), "path=%s", path)
	}
}

func TestCompositeGeminiTargetPlatformMiddlewareUsesPathRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{
		routes: []service.CompositeModelRoute{
			{
				ID:             1,
				GroupID:        1,
				PublicModel:    "openrouter/gemini-pro",
				MatchType:      service.CompositeRouteMatchExact,
				TargetPlatform: service.PlatformGemini,
				UpstreamModel:  "gemini-2.5-pro",
				Endpoint:       service.CompositeRouteEndpointGemini,
				Priority:       100,
				Enabled:        true,
			},
		},
	})
	router.Use(gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		groupID := int64(1)
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformComposite},
		})
		c.Next()
	})))
	router.Use(compositeGeminiTargetPlatformMiddleware(resolver))
	router.POST("/v1beta/models/*modelAction", func(c *gin.Context) {
		platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, service.PlatformGemini, platform)

		upstreamModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, "gemini-2.5-pro", upstreamModel)
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/openrouter/gemini-pro:generateContent", strings.NewReader(`{"contents":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
}

// Live 入口顶层 model 与 session.model 不一致时，合成路由必须按 session.model
// 分发与改写（与白名单准入、Live handler 一致），不得命中顶层别名的映射并把
// session 模型覆盖为别名上游。
func TestCompositeLiveRouteDispatchesBySessionModelNotTopLevelAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{
		routes: []service.CompositeModelRoute{
			{
				ID:             1,
				GroupID:        1,
				PublicModel:    "live-alias",
				MatchType:      service.CompositeRouteMatchExact,
				TargetPlatform: service.PlatformOpenAI,
				UpstreamModel:  "gpt-alias-upstream",
				Endpoint:       service.CompositeRouteEndpointAny,
				Priority:       100,
				Enabled:        true,
			},
		},
	})
	router.Use(gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		groupID := int64(1)
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformComposite},
		})
		c.Next()
	})))
	router.Use(compositeTargetPlatformMiddleware(resolver))
	router.POST("/backend-api/codex/realtime/calls", func(c *gin.Context) {
		upstream, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, "gpt-realtime-1", upstream,
			"Live dispatch must resolve the session model, never the top-level alias upstream")
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"live-alias","session":{"model":"gpt-realtime-1"},"sdp":"v=0"}`, string(body),
			"session.model must stay untouched when dispatch resolves by session model")
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(
		http.MethodPost,
		"/backend-api/codex/realtime/calls",
		strings.NewReader(`{"model":"live-alias","session":{"model":"gpt-realtime-1"},"sdp":"v=0"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
}
