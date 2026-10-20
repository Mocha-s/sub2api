//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

type accountRepoStubForCompositeModelsList struct {
	accountRepoStub
	accounts []Account
}

func (s *accountRepoStubForCompositeModelsList) ListSchedulableByGroupID(_ context.Context, _ int64) ([]Account, error) {
	return s.accounts, nil
}

func TestAdminService_CreateCompositeGroupCopiesAccountsFromConcreteGroups(t *testing.T) {
	var copiedFrom []int64
	var boundGroupID int64
	var boundAccountIDs []int64
	groupRepo := &groupRepoStubForAdmin{
		createID: 99,
		getByIDByID: map[int64]*Group{
			10: {ID: 10, Platform: PlatformOpenAI},
			20: {ID: 20, Platform: PlatformGemini},
		},
		getAccountIDsByGroupIDsFn: func(groupIDs []int64) ([]int64, error) {
			copiedFrom = append([]int64{}, groupIDs...)
			return []int64{101, 202}, nil
		},
		bindAccountsToGroupFn: func(groupID int64, accountIDs []int64) error {
			boundGroupID = groupID
			boundAccountIDs = append([]int64{}, accountIDs...)
			return nil
		},
	}
	svc := &adminServiceImpl{groupRepo: groupRepo}

	group, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name:                        "Composite",
		Platform:                    PlatformComposite,
		RateMultiplier:              1,
		MaxReasoningEffort:          "medium",
		MaxReasoningEffortOverLimit: ReasoningEffortOverLimitDeny,
		ReasoningEffortMappings: []ReasoningEffortMapping{
			{From: "max", To: "xhigh"},
		},
		CopyAccountsFromGroupIDs: []int64{10, 20, 10},
	})

	require.NoError(t, err)
	require.Equal(t, PlatformComposite, groupRepo.created.Platform)
	require.Equal(t, "medium", groupRepo.created.MaxReasoningEffort)
	require.Equal(t, ReasoningEffortOverLimitDeny, groupRepo.created.MaxReasoningEffortOverLimit)
	require.Equal(t, []ReasoningEffortMapping{{From: "max", To: "xhigh"}}, groupRepo.created.ReasoningEffortMappings)
	require.Equal(t, int64(99), group.ID)
	require.Equal(t, int64(2), group.AccountCount)
	require.ElementsMatch(t, []int64{10, 20}, copiedFrom)
	require.Equal(t, int64(99), boundGroupID)
	require.ElementsMatch(t, []int64{101, 202}, boundAccountIDs)
}

func TestAdminService_CreateCompositeGroupAllowsVideoGeneration(t *testing.T) {
	groupRepo := &groupRepoStubForAdmin{createID: 99}
	svc := &adminServiceImpl{groupRepo: groupRepo}

	group, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name:                 "Composite Video",
		Platform:             PlatformComposite,
		RateMultiplier:       1,
		AllowVideoGeneration: true,
	})

	require.NoError(t, err)
	require.True(t, group.AllowVideoGeneration)
	require.NotNil(t, groupRepo.created)
	require.True(t, groupRepo.created.AllowVideoGeneration)
}

func TestAdminService_UpdateCompositeGroupCopiesAccountsFromConcreteGroups(t *testing.T) {
	var clearedGroupID int64
	var copiedFrom []int64
	var boundGroupID int64
	var boundAccountIDs []int64
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			10: {ID: 10, Platform: PlatformOpenAI},
			20: {ID: 20, Platform: PlatformGrok},
			99: {ID: 99, Platform: PlatformComposite, RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard},
		},
		deleteAccountGroupsByGroupIDFn: func(groupID int64) (int64, error) {
			clearedGroupID = groupID
			return 2, nil
		},
		getAccountIDsByGroupIDsFn: func(groupIDs []int64) ([]int64, error) {
			copiedFrom = append([]int64{}, groupIDs...)
			return []int64{301, 302}, nil
		},
		bindAccountsToGroupFn: func(groupID int64, accountIDs []int64) error {
			boundGroupID = groupID
			boundAccountIDs = append([]int64{}, accountIDs...)
			return nil
		},
	}
	svc := &adminServiceImpl{groupRepo: groupRepo}
	maxReasoningEffort := "low"
	maxReasoningEffortOverLimit := ReasoningEffortOverLimitDeny
	reasoningEffortMappings := []ReasoningEffortMapping{{From: "max", To: "high"}}

	group, err := svc.UpdateGroup(context.Background(), 99, &UpdateGroupInput{
		MaxReasoningEffort:          &maxReasoningEffort,
		MaxReasoningEffortOverLimit: &maxReasoningEffortOverLimit,
		ReasoningEffortMappings:     &reasoningEffortMappings,
		CopyAccountsFromGroupIDs:    []int64{10, 20},
	})

	require.NoError(t, err)
	require.Equal(t, PlatformComposite, group.Platform)
	require.Equal(t, "low", group.MaxReasoningEffort)
	require.Equal(t, ReasoningEffortOverLimitDeny, group.MaxReasoningEffortOverLimit)
	require.Equal(t, reasoningEffortMappings, group.ReasoningEffortMappings)
	require.Equal(t, int64(99), clearedGroupID)
	require.ElementsMatch(t, []int64{10, 20}, copiedFrom)
	require.Equal(t, int64(99), boundGroupID)
	require.ElementsMatch(t, []int64{301, 302}, boundAccountIDs)
}

func TestAdminService_UpdateCompositeGroupAllowsVideoGeneration(t *testing.T) {
	allowVideo := true
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			99: {ID: 99, Platform: PlatformComposite, RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard},
		},
	}
	svc := &adminServiceImpl{groupRepo: groupRepo}

	group, err := svc.UpdateGroup(context.Background(), 99, &UpdateGroupInput{
		AllowVideoGeneration: &allowVideo,
	})

	require.NoError(t, err)
	require.True(t, group.AllowVideoGeneration)
	require.NotNil(t, groupRepo.updated)
	require.True(t, groupRepo.updated.AllowVideoGeneration)
}

func TestAdminService_CreateAccountAllowsCompositeGroupAssignment(t *testing.T) {
	accountRepo := &accountRepoStubForBulkUpdate{createID: 7}
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			99: {ID: 99, Platform: PlatformComposite},
		},
	}
	svc := &adminServiceImpl{accountRepo: accountRepo, groupRepo: groupRepo}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                  "OpenAI account",
		Platform:              PlatformOpenAI,
		Type:                  AccountTypeAPIKey,
		Concurrency:           1,
		GroupIDs:              []int64{99},
		SkipDefaultGroupBind:  true,
		SkipMixedChannelCheck: true,
	})

	require.NoError(t, err)
	require.Equal(t, int64(7), account.ID)
	require.Equal(t, PlatformOpenAI, accountRepo.createAccount.Platform)
	require.ElementsMatch(t, []int64{99}, accountRepo.bindGroupsByAccount[7])
}

func TestAdminService_UpdateAccountAllowsCompositeGroupAssignment(t *testing.T) {
	accountRepo := &accountRepoStubForBulkUpdate{
		getByIDAccounts: map[int64]*Account{
			7: {ID: 7, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Extra: map[string]any{}},
		},
	}
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			99: {ID: 99, Platform: PlatformComposite},
		},
	}
	svc := &adminServiceImpl{accountRepo: accountRepo, groupRepo: groupRepo}
	groupIDs := []int64{99}

	account, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{
		GroupIDs:              &groupIDs,
		SkipMixedChannelCheck: true,
	})

	require.NoError(t, err)
	require.Equal(t, int64(7), account.ID)
	require.Len(t, accountRepo.updatedAccounts, 1)
	require.ElementsMatch(t, []int64{99}, accountRepo.bindGroupsByAccount[7])
}

func TestAdminService_CompositeModelsListCandidatesIncludeConcreteAccountMappings(t *testing.T) {
	accountRepo := &accountRepoStubForCompositeModelsList{
		accounts: []Account{
			{
				ID:       1,
				Platform: PlatformOpenAI,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gpt-custom": "gpt-5"},
				},
			},
			{
				ID:       2,
				Platform: PlatformGemini,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gemini-custom": "gemini-2.5-flash"},
				},
			},
			{
				ID:       3,
				Platform: PlatformKimi,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"kimi-custom": "kimi-k2"},
				},
			},
		},
	}
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			99: {ID: 99, Platform: PlatformComposite},
		},
	}
	svc := &adminServiceImpl{accountRepo: accountRepo, groupRepo: groupRepo}

	candidates, err := svc.GetGroupModelsListCandidates(context.Background(), 99, PlatformComposite)

	require.NoError(t, err)
	require.Contains(t, candidates, "gpt-custom")
	require.Contains(t, candidates, "gemini-custom")
	require.Contains(t, candidates, "kimi-custom")
	require.Contains(t, candidates, "gpt-5.5")
	require.Contains(t, candidates, "gemini-2.5-flash")
}

type compositeGroupTargetRepoStub struct {
	targets []CompositeGroupTarget
	created *CompositeGroupTarget
	updated *CompositeGroupTarget
	deleted []int64
}

func (r *compositeGroupTargetRepoStub) ListByCompositeGroup(_ context.Context, groupID int64, includeDisabled bool) ([]CompositeGroupTarget, error) {
	out := make([]CompositeGroupTarget, 0, len(r.targets))
	for _, target := range r.targets {
		if target.CompositeGroupID == groupID && (includeDisabled || target.Enabled) {
			out = append(out, target)
		}
	}
	return out, nil
}

func (r *compositeGroupTargetRepoStub) Create(_ context.Context, target *CompositeGroupTarget) error {
	r.created = target
	target.ID = 123
	return nil
}

func (r *compositeGroupTargetRepoStub) Update(_ context.Context, target *CompositeGroupTarget) error {
	r.updated = target
	return nil
}

func (r *compositeGroupTargetRepoStub) Delete(_ context.Context, groupID, targetID int64) error {
	r.deleted = append(r.deleted, targetID)
	return nil
}

type compositeTargetInvalidator struct{ groupIDs []int64 }

func (*compositeTargetInvalidator) InvalidateAuthCacheByKey(context.Context, string)   {}
func (*compositeTargetInvalidator) InvalidateAuthCacheByUserID(context.Context, int64) {}
func (i *compositeTargetInvalidator) InvalidateAuthCacheByGroupID(_ context.Context, id int64) {
	i.groupIDs = append(i.groupIDs, id)
}

func TestAdminService_CreateCompositeTargetValidatesAndInvalidatesSource(t *testing.T) {
	groupRepo := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{
		7:  {ID: 7, Platform: PlatformComposite, Status: StatusActive},
		20: {ID: 20, Platform: PlatformOpenAI, Status: StatusActive},
	}}
	targetRepo := &compositeGroupTargetRepoStub{}
	invalidator := &compositeTargetInvalidator{}
	svc := &adminServiceImpl{groupRepo: groupRepo, compositeTargetRepo: targetRepo, authCacheInvalidator: invalidator}

	target, err := svc.CreateCompositeTarget(context.Background(), 7, CompositeGroupTargetInput{
		TargetGroupID: 20,
		Priority:      10,
		Enabled:       true,
	})
	require.NoError(t, err)
	require.Equal(t, int64(123), target.ID)
	require.Equal(t, int64(7), target.CompositeGroupID)
	require.Equal(t, int64(20), target.TargetGroupID)
	require.Equal(t, 10, target.Priority)
	require.Equal(t, target, targetRepo.created)
	require.Equal(t, []int64{7}, invalidator.groupIDs)
}

func TestAdminService_CreateCompositeTargetRejectsNestedAndInactiveTargets(t *testing.T) {
	groupRepo := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{
		7:  {ID: 7, Platform: PlatformComposite, Status: StatusActive},
		20: {ID: 20, Platform: PlatformComposite, Status: StatusActive},
		30: {ID: 30, Platform: PlatformOpenAI, Status: StatusDisabled},
	}}
	targetRepo := &compositeGroupTargetRepoStub{}
	svc := &adminServiceImpl{groupRepo: groupRepo, compositeTargetRepo: targetRepo}

	_, err := svc.CreateCompositeTarget(context.Background(), 7, CompositeGroupTargetInput{TargetGroupID: 20, Enabled: true})
	require.Error(t, err)
	_, err = svc.CreateCompositeTarget(context.Background(), 7, CompositeGroupTargetInput{TargetGroupID: 30, Enabled: true})
	require.Error(t, err)
	require.Nil(t, targetRepo.created)
}

func TestAdminService_UpdateAndDeleteCompositeTargetRequireOwnership(t *testing.T) {
	groupRepo := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{
		7: {ID: 7, Platform: PlatformComposite, Status: StatusActive},
	}}
	targetRepo := &compositeGroupTargetRepoStub{targets: []CompositeGroupTarget{
		{ID: 11, CompositeGroupID: 7, TargetGroupID: 20, Priority: 100, Enabled: true},
		{ID: 12, CompositeGroupID: 8, TargetGroupID: 30, Priority: 100, Enabled: true},
	}}
	svc := &adminServiceImpl{groupRepo: groupRepo, compositeTargetRepo: targetRepo}

	updated, err := svc.UpdateCompositeTarget(context.Background(), 7, 11, CompositeGroupTargetInput{Priority: 2, Enabled: false})
	require.NoError(t, err)
	require.Equal(t, int64(20), updated.TargetGroupID)
	require.Equal(t, 2, updated.Priority)
	require.False(t, updated.Enabled)

	_, err = svc.UpdateCompositeTarget(context.Background(), 7, 12, CompositeGroupTargetInput{Priority: 2, Enabled: true})
	require.ErrorIs(t, err, ErrCompositeTargetNotFound)
	require.ErrorIs(t, svc.DeleteCompositeTarget(context.Background(), 7, 12), ErrCompositeTargetNotFound)
	require.NoError(t, svc.DeleteCompositeTarget(context.Background(), 7, 11))
	require.Equal(t, []int64{11}, targetRepo.deleted)
}

// 独立 CN 分组的模型列表候选沿用 default 分支的 Claude 默认列表；
// composite 支持不得改变独立分组的候选语义。
func TestAdminService_CNProviderModelsListCandidatesKeepClaudeDefaults(t *testing.T) {
	want := make([]string, 0, len(claude.DefaultModels))
	for _, model := range claude.DefaultModels {
		want = append(want, model.ID)
	}
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax} {
		require.Equal(t, want, defaultModelsListCandidateIDs(platform), "platform=%s", platform)
	}
}
