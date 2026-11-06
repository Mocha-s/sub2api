package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// platformFilteringAccountRepoStub 按生产 SQL 的语义过滤平台：platform IN (...)。
// 空字符串不等于任何真实平台，所以传空平台查不到任何账号。
type platformFilteringAccountRepoStub struct {
	AccountRepository
	byGroup map[int64][]Account
}

func (s *platformFilteringAccountRepoStub) filter(groupID int64, platforms []string) []Account {
	out := make([]Account, 0)
	for _, acc := range s.byGroup[groupID] {
		for _, p := range platforms {
			if acc.Platform == p {
				out = append(out, acc)
				break
			}
		}
	}
	return out
}

func (s *platformFilteringAccountRepoStub) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]Account, error) {
	return s.filter(groupID, []string{platform}), nil
}

func (s *platformFilteringAccountRepoStub) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]Account, error) {
	return s.filter(groupID, platforms), nil
}

// 生产回归：composite 目录以空平台调用 GetCompositeTargetModels。经过调度快照时，
// 空平台曾被当作字面量过滤，每个目标组都查不到账号，/v1/models 返回空列表。
func TestGetCompositeTargetModels_EmptyPlatformUsesEachTargetPlatformThroughSnapshot(t *testing.T) {
	compositeID := int64(4100)
	openAIID, anthropicID, inactiveID := int64(4101), int64(4102), int64(4103)
	repo := &platformFilteringAccountRepoStub{byGroup: map[int64][]Account{
		openAIID:    {{ID: 1, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"}}}},
		anthropicID: {{ID: 2, Platform: PlatformAnthropic, Credentials: map[string]any{"model_mapping": map[string]any{"claude-opus-5-5": "claude-opus-5-5"}}}},
		inactiveID:  {{ID: 3, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"must-not-list": "gpt-5"}}}},
	}}
	resolver := NewCompositeRouteResolver(nil)
	resolver.SetTargetRepository(compositeTargetRepoStub{targets: []CompositeGroupTarget{
		{CompositeGroupID: compositeID, TargetGroupID: openAIID, Priority: 1, Enabled: true, TargetGroup: &Group{ID: openAIID, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}},
		{CompositeGroupID: compositeID, TargetGroupID: anthropicID, Priority: 1, Enabled: true, TargetGroup: &Group{ID: anthropicID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}},
		{CompositeGroupID: compositeID, TargetGroupID: inactiveID, Priority: 1, Enabled: true, TargetGroup: &Group{ID: inactiveID, Platform: PlatformOpenAI, Status: StatusDisabled, Hydrated: true}},
	}})
	svc := &GatewayService{
		accountRepo:       repo,
		compositeResolver: resolver,
		schedulerSnapshot: NewSchedulerSnapshotService(nil, nil, repo, nil, nil),
	}

	models, configured, err := svc.GetCompositeTargetModels(context.Background(), compositeID, "")
	require.NoError(t, err)
	require.True(t, configured)
	require.Equal(t, []string{"claude-opus-5-5", "gpt-5.5"}, models)

	// 显式平台仍只列该平台的目标组。
	models, configured, err = svc.GetCompositeTargetModels(context.Background(), compositeID, PlatformOpenAI)
	require.NoError(t, err)
	require.True(t, configured)
	require.Equal(t, []string{"gpt-5.5"}, models)
}
