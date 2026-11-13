package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cancelAfterVideoTaskCreateRepository struct {
	VideoTaskRepository
	cancel context.CancelFunc
}

func (r *cancelAfterVideoTaskCreateRepository) Create(ctx context.Context, input VideoTaskCreateInput) (*VideoTask, error) {
	task, err := r.VideoTaskRepository.Create(ctx, input)
	if err == nil {
		r.cancel()
	}
	return task, err
}

type contextCheckingVideoAdmission struct {
	videoTaskSettlementOrchestrator
	reserveContext context.Context
	reserveCalls   int
}

func (s *contextCheckingVideoAdmission) ReservePrepared(ctx context.Context, cmd *VideoTaskSettlementReserveCommand) (*VideoTaskSubmissionClaim, error) {
	s.reserveContext = ctx
	s.reserveCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.videoTaskSettlementOrchestrator.ReservePrepared(ctx, cmd)
}

func TestVideoTaskAdmissionSurvivesDisconnectAfterTaskPersistence(t *testing.T) {
	svc, repo, provider, settlement := pricedVideoTaskServiceFixture(&videoTaskServiceTestEvents{})
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "request-trace"))
	defer cancel()
	svc.repo = &cancelAfterVideoTaskCreateRepository{VideoTaskRepository: repo, cancel: cancel}
	checker := &contextCheckingVideoAdmission{videoTaskSettlementOrchestrator: settlement}
	svc.settlement = checker
	params := VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"waves","seconds":5}`), ContentType: "application/json", IdempotencyKey: "disconnect-after-persist"}

	result, err := svc.Create(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "upstream", result.Task.ProviderTaskID)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, "request-trace", checker.reserveContext.Value(contextKey{}))
	deadline, ok := checker.reserveContext.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, time.Until(deadline), videoTaskPersistenceTimeout)
	require.ErrorIs(t, checker.reserveContext.Err(), context.Canceled)

	replay, err := svc.Create(context.Background(), params)
	require.NoError(t, err)
	require.Equal(t, result.Task.PublicTaskID, replay.Task.PublicTaskID)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, 1, checker.reserveCalls)
}
