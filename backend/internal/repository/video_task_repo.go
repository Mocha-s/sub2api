package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/ent/videotask"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var errVideoTaskNotFound = infraerrors.NotFound("VIDEO_TASK_NOT_FOUND", "video task not found")

type videoTaskRepository struct {
	client *dbent.Client
	db     *sql.DB
}

func NewVideoTaskRepository(client *dbent.Client, db *sql.DB) service.VideoTaskRepository {
	return &videoTaskRepository{client: client, db: db}
}

func (r *videoTaskRepository) Create(ctx context.Context, input service.VideoTaskCreateInput) (*service.VideoTask, error) {
	client := clientFromContext(ctx, r.client)
	metadata := cloneAnyMap(input.Metadata)
	requestedModel := metadataString(metadata, "requested_model")
	if requestedModel == "" {
		requestedModel = input.Model
	}
	upstreamModel := metadataString(metadata, "upstream_model")
	if upstreamModel == "" {
		upstreamModel = requestedModel
	}
	billingModel := metadataString(metadata, "billing_model")
	if billingModel == "" {
		billingModel = upstreamModel
	}

	builder := client.VideoTask.Create().
		SetPublicTaskID(input.PublicTaskID).
		SetProvider(input.Provider).
		SetPlatform(input.Platform).
		SetUserID(input.UserID).
		SetAPIKeyID(input.APIKeyID).
		SetGroupID(input.GroupID).
		SetAccountID(input.AccountID).
		SetRequestedModel(requestedModel).
		SetUpstreamModel(upstreamModel).
		SetBillingModel(billingModel).
		SetStatus(string(service.VideoTaskStatusSubmitting)).
		SetPrompt(input.Prompt).
		SetRequestHash(input.RequestHash).
		SetRequestMetadata(metadata)

	if input.ChannelID > 0 {
		builder.SetChannelID(input.ChannelID)
	}
	if input.SubscriptionID != nil {
		builder.SetSubscriptionID(*input.SubscriptionID)
	}
	if input.PromptHash != "" {
		builder.SetPromptHash(input.PromptHash)
	}
	if len(input.RequestBody) > 0 {
		builder.SetRequestBody(append([]byte(nil), input.RequestBody...))
	}
	setCreateStringFromMetadata(builder, metadata)

	row, err := builder.Save(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, nil, nil)
	}
	return videoTaskEntToService(row), nil
}

func (r *videoTaskRepository) UpdateSettlementSummary(ctx context.Context, publicTaskID string, summary service.VideoTaskSettlementSummary) error {
	updater := clientFromContext(ctx, r.client).VideoTask.Update().Where(videotask.PublicTaskIDEQ(publicTaskID))
	if summary.SubscriptionID != nil {
		updater.SetSubscriptionID(*summary.SubscriptionID)
	}
	if summary.UsageLogID != nil {
		updater.SetUsageLogID(*summary.UsageLogID)
	} else if summary.ClearUsageLogID {
		updater.ClearUsageLogID()
	}
	if summary.UsageMetadata != nil {
		updater.SetUsageMetadata(cloneAnyMap(summary.UsageMetadata))
	}
	if summary.BilledUSD != nil {
		updater.SetBilledUsd(*summary.BilledUSD)
	}
	affected, err := updater.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, errVideoTaskNotFound, nil)
	}
	if affected == 0 {
		return errVideoTaskNotFound
	}
	return nil
}

func (r *videoTaskRepository) PersistUpstreamFallback(ctx context.Context, publicTaskID string, fallback service.VideoTaskUpstreamFallback) error {
	client := clientFromContext(ctx, r.client)
	row, err := client.VideoTask.Query().Where(videotask.PublicTaskIDEQ(publicTaskID)).Only(ctx)
	if err != nil {
		return translatePersistenceError(err, errVideoTaskNotFound, nil)
	}
	metadata := cloneAnyMap(row.RequestMetadata)
	metadata["reconciliation_error_code"] = "ATTACH_UPSTREAM_FAILED"
	metadata["reconciliation_error_message"] = "upstream task was created but could not be attached to the local task"
	metadata["reconciliation_upstream_task_id"] = fallback.Snapshot.ProviderTaskID
	metadata["reconciliation_accepted_snapshot"] = fallback.Snapshot
	if fallback.Snapshot.ProviderStatus != "" {
		metadata["reconciliation_provider_status"] = fallback.Snapshot.ProviderStatus
	}
	affected, err := client.VideoTask.Update().Where(videotask.PublicTaskIDEQ(publicTaskID), nonTerminalVideoTaskPredicate()).SetRequestMetadata(metadata).Save(ctx)
	if err != nil {
		return translatePersistenceError(err, errVideoTaskNotFound, nil)
	}
	if affected == 0 {
		return errors.New("video task upstream fallback was not persisted")
	}
	return nil
}

func (r *videoTaskRepository) AttachUpstream(ctx context.Context, publicTaskID string, update service.VideoTaskSubmitUpdate) error {
	client := clientFromContext(ctx, r.client)
	updater := client.VideoTask.Update().Where(videotask.PublicTaskIDEQ(publicTaskID))

	if update.ProviderTaskID != "" {
		updater.SetUpstreamTaskID(update.ProviderTaskID)
	}
	if update.Status != "" {
		updater.SetStatus(string(update.Status))
	}
	if update.ProviderStatus != "" {
		updater.SetProviderStatus(update.ProviderStatus)
	}
	if len(update.ResponseBody) > 0 {
		updater.SetUpstreamResponseBody(append([]byte(nil), update.ResponseBody...))
		if response := rawObject(update.ResponseBody); response != nil {
			updater.SetUpstreamResponse(response)
		}
	}
	if update.Metadata != nil {
		updater.SetResultMetadata(cloneAnyMap(update.Metadata))
	}
	if update.ErrorMessage != "" {
		updater.SetErrorMessage(update.ErrorMessage)
	}
	if update.SubmittedAt != nil {
		updater.SetSubmittedAt(*update.SubmittedAt)
	}
	if update.ExpiresAt != nil {
		updater.SetExpiresAt(*update.ExpiresAt)
	}
	if update.ClearNextPollAt || update.Status.Terminal() {
		updater.ClearNextPollAt()
	} else if update.NextPollAt != nil {
		updater.SetNextPollAt(*update.NextPollAt)
	}

	affected, err := updater.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, errVideoTaskNotFound, nil)
	}
	if affected == 0 {
		return errVideoTaskNotFound
	}
	return nil
}

func (r *videoTaskRepository) GetByPublicTaskID(ctx context.Context, publicTaskID string) (*service.VideoTask, error) {
	row, err := r.client.VideoTask.Query().
		Where(videotask.PublicTaskIDEQ(publicTaskID)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, errVideoTaskNotFound, nil)
	}
	return videoTaskEntToService(row), nil
}

func (r *videoTaskRepository) GetByPublicTaskIDForUser(ctx context.Context, publicTaskID string, userID int64) (*service.VideoTask, error) {
	row, err := r.client.VideoTask.Query().
		Where(videotask.PublicTaskIDEQ(publicTaskID), videotask.UserIDEQ(userID), videotask.UserDeletedAtIsNil()).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, errVideoTaskNotFound, nil)
	}
	return videoTaskEntToService(row), nil
}

func (r *videoTaskRepository) MarkUserDeleted(ctx context.Context, publicTaskID string, userID int64, deletedAt time.Time) error {
	affected, err := clientFromContext(ctx, r.client).VideoTask.Update().
		Where(
			videotask.PublicTaskIDEQ(publicTaskID),
			videotask.UserIDEQ(userID),
			videotask.UserDeletedAtIsNil(),
			videotask.StatusIn(
				string(service.VideoTaskStatusCompleted),
				string(service.VideoTaskStatusFailed),
				string(service.VideoTaskStatusCancelled),
				string(service.VideoTaskStatusExpired),
			),
		).
		SetUserDeletedAt(deletedAt).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, errVideoTaskNotFound, nil)
	}
	if affected == 0 {
		return errVideoTaskNotFound
	}
	return nil
}

func (r *videoTaskRepository) GetByProviderTaskID(ctx context.Context, provider, providerTaskID string) (*service.VideoTask, error) {
	row, err := r.client.VideoTask.Query().
		Where(videotask.ProviderEQ(provider), videotask.UpstreamTaskIDEQ(providerTaskID)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, errVideoTaskNotFound, nil)
	}
	return videoTaskEntToService(row), nil
}

func (r *videoTaskRepository) GetByIdempotencyKey(ctx context.Context, apiKeyID int64, idempotencyKey string) (*service.VideoTask, error) {
	row, err := r.client.VideoTask.Query().
		Where(videotask.APIKeyIDEQ(apiKeyID), videotask.IdempotencyKeyEQ(idempotencyKey)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, errVideoTaskNotFound, nil)
	}
	return videoTaskEntToService(row), nil
}

func (r *videoTaskRepository) ListForUser(ctx context.Context, params service.VideoTaskListParams) ([]*service.VideoTask, bool, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	predicates := []predicate.VideoTask{videotask.UserIDEQ(params.UserID), videotask.UserDeletedAtIsNil()}
	if params.Status != "" {
		predicates = append(predicates, videotask.StatusEQ(params.Status))
	}
	if params.Model != "" {
		predicates = append(predicates, videotask.RequestedModelEQ(params.Model))
	}
	if !params.After.IsZero() {
		predicates = append(predicates, videotask.CreatedAtGT(params.After))
	}
	if !params.Before.IsZero() {
		predicates = append(predicates, videotask.CreatedAtLT(params.Before))
	}
	rows, err := r.client.VideoTask.Query().
		Where(predicates...).
		Order(dbent.Desc(videotask.FieldCreatedAt), dbent.Desc(videotask.FieldID)).
		Limit(limit + 1).
		All(ctx)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]*service.VideoTask, 0, len(rows))
	for _, row := range rows {
		items = append(items, videoTaskEntToService(row))
	}
	return items, hasMore, nil
}

func (r *videoTaskRepository) UpdateSubmit(ctx context.Context, publicTaskID string, update service.VideoTaskSubmitUpdate) error {
	return r.AttachUpstream(ctx, publicTaskID, update)
}

func (r *videoTaskRepository) UpdateProvider(ctx context.Context, publicTaskID string, update service.VideoTaskProviderUpdate) error {
	_, err := r.UpdateFromProvider(ctx, publicTaskID, update)
	return err
}

func (r *videoTaskRepository) UpdateFromProvider(ctx context.Context, publicTaskID string, update service.VideoTaskProviderUpdate) (bool, error) {
	client := clientFromContext(ctx, r.client)
	updater := client.VideoTask.Update().Where(
		videotask.PublicTaskIDEQ(publicTaskID),
		nonTerminalVideoTaskPredicate(),
	)
	applyVideoTaskProviderUpdate(updater, update)
	if update.Status.Terminal() {
		updater.ClearLockedBy().ClearLockedUntil()
	}

	affected, err := updater.Save(ctx)
	if err != nil {
		return false, translatePersistenceError(err, errVideoTaskNotFound, nil)
	}
	return affected > 0, nil
}

func (r *videoTaskRepository) UpdateFromProviderWithPollLease(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time, update service.VideoTaskProviderUpdate) (bool, error) {
	client := clientFromContext(ctx, r.client)
	updater := client.VideoTask.Update().Where(
		videotask.PublicTaskIDEQ(publicTaskID),
		videotask.LockedByEQ(leaseToken),
		videotask.LockedUntilGT(validAt),
		nonTerminalVideoTaskPredicate(),
	)
	applyVideoTaskProviderUpdate(updater, update)

	affected, err := updater.Save(ctx)
	if err != nil {
		return false, translatePersistenceError(err, nil, nil)
	}
	return affected > 0, nil
}

func applyVideoTaskProviderUpdate(updater *dbent.VideoTaskUpdate, update service.VideoTaskProviderUpdate) {
	if update.Status != "" {
		updater.SetStatus(string(update.Status))
	}
	if update.ProviderStatus != "" {
		updater.SetProviderStatus(update.ProviderStatus)
	}
	if len(update.ResponseBody) > 0 {
		updater.SetUpstreamResponseBody(append([]byte(nil), update.ResponseBody...))
		if response := rawObject(update.ResponseBody); response != nil {
			updater.SetUpstreamResponse(response)
		}
	}
	if update.Metadata != nil {
		updater.SetResultMetadata(cloneAnyMap(update.Metadata))
	}
	if update.ErrorMessage != "" {
		updater.SetErrorMessage(update.ErrorMessage)
	}
	if update.CompletedAt != nil {
		updater.SetCompletedAt(*update.CompletedAt)
	} else if update.Status.Terminal() {
		updater.SetCompletedAt(time.Now())
	}
	if update.ExpiresAt != nil {
		updater.SetExpiresAt(*update.ExpiresAt)
	}
	if update.ClearNextPollAt || update.Status.Terminal() {
		updater.ClearNextPollAt()
	} else if update.NextPollAt != nil {
		updater.SetNextPollAt(*update.NextPollAt)
	}
}

func (r *videoTaskRepository) ClaimDuePollTasks(ctx context.Context, now time.Time, limit int, leaseToken string, lockTTL time.Duration) ([]*service.VideoTask, error) {
	if limit <= 0 {
		return nil, nil
	}
	if lockTTL <= 0 {
		lockTTL = time.Minute
	}
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return claimDuePollTasksWithClient(ctx, tx.Client(), now, limit, leaseToken, lockTTL)
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	claimed, err := claimDuePollTasksWithClient(txCtx, tx.Client(), now, limit, leaseToken, lockTTL)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (r *videoTaskRepository) RenewPollLock(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time, lockTTL time.Duration) (bool, error) {
	client := clientFromContext(ctx, r.client)
	affected, err := client.VideoTask.Update().
		Where(
			videotask.PublicTaskIDEQ(publicTaskID),
			videotask.LockedByEQ(leaseToken),
			videotask.LockedUntilGT(validAt),
			nonTerminalVideoTaskPredicate(),
		).
		SetLockedUntil(validAt.Add(lockTTL)).
		Save(ctx)
	if err != nil {
		return false, translatePersistenceError(err, nil, nil)
	}
	return affected > 0, nil
}

func claimDuePollTasksWithClient(ctx context.Context, client *dbent.Client, now time.Time, limit int, leaseToken string, lockTTL time.Duration) ([]*service.VideoTask, error) {
	candidates, err := client.VideoTask.Query().
		Where(duePollPredicates(now)...).
		Order(dbent.Asc(videotask.FieldNextPollAt), dbent.Asc(videotask.FieldID)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}

	claimed := make([]*service.VideoTask, 0, len(candidates))
	for _, candidate := range candidates {
		count, err := client.VideoTask.Update().
			Where(append(duePollPredicates(now), videotask.IDEQ(candidate.ID))...).
			SetLockedBy(leaseToken).
			SetLockedUntil(now.Add(lockTTL)).
			SetLastPolledAt(now).
			AddPollAttempts(1).
			Save(ctx)
		if err != nil {
			return nil, err
		}
		if count == 0 {
			continue
		}
		row, err := client.VideoTask.Get(ctx, candidate.ID)
		if err != nil {
			return nil, translatePersistenceError(err, errVideoTaskNotFound, nil)
		}
		claimed = append(claimed, videoTaskEntToService(row))
	}
	return claimed, nil
}

func (r *videoTaskRepository) ReleasePollLock(ctx context.Context, publicTaskID, leaseToken string) (bool, error) {
	client := clientFromContext(ctx, r.client)
	updater := client.VideoTask.Update().
		Where(videotask.PublicTaskIDEQ(publicTaskID), videotask.LockedByEQ(leaseToken)).
		ClearLockedBy().
		ClearLockedUntil()
	affected, err := updater.Save(ctx)
	if err != nil {
		return false, translatePersistenceError(err, nil, nil)
	}
	return affected > 0, nil
}

func duePollPredicates(now time.Time) []predicate.VideoTask {
	return []predicate.VideoTask{
		nonTerminalVideoTaskPredicate(),
		videotask.NextPollAtLTE(now),
		videotask.Or(
			videotask.LockedUntilIsNil(),
			videotask.LockedUntilLTE(now),
		),
	}
}

func nonTerminalVideoTaskPredicate() predicate.VideoTask {
	return videotask.StatusNotIn(
		string(service.VideoTaskStatusCompleted),
		string(service.VideoTaskStatusFailed),
		string(service.VideoTaskStatusCancelled),
		string(service.VideoTaskStatusExpired),
	)
}

func videoTaskEntToService(row *dbent.VideoTask) *service.VideoTask {
	if row == nil {
		return nil
	}
	metadata := cloneAnyMap(row.RequestMetadata)
	for key, value := range row.ResultMetadata {
		metadata[key] = value
	}
	return &service.VideoTask{
		ID:             row.ID,
		PublicTaskID:   row.PublicTaskID,
		ProviderTaskID: stringValue(row.UpstreamTaskID),
		Provider:       row.Provider,
		Platform:       row.Platform,
		UserID:         row.UserID,
		APIKeyID:       row.APIKeyID,
		GroupID:        row.GroupID,
		AccountID:      row.AccountID,
		ChannelID:      int64Value(row.ChannelID),
		SubscriptionID: cloneInt64(row.SubscriptionID),
		UsageLogID:     cloneInt64(row.UsageLogID),
		Model:          row.RequestedModel,
		Prompt:         row.Prompt,
		Status:         service.VideoTaskStatus(row.Status),
		ProviderStatus: stringValue(row.ProviderStatus),
		RequestHash:    row.RequestHash,
		PromptHash:     stringValue(row.PromptHash),
		RequestBody:    cloneBytes(row.RequestBody),
		ResponseBody:   cloneBytes(row.UpstreamResponseBody),
		Metadata:       metadata,
		UsageMetadata:  cloneAnyMap(row.UsageMetadata),
		BilledUSD:      row.BilledUsd,
		ErrorMessage:   stringValue(row.ErrorMessage),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		SubmittedAt:    cloneTime(row.SubmittedAt),
		StartedAt:      cloneTime(row.StartedAt),
		CompletedAt:    cloneTime(row.CompletedAt),
		ExpiresAt:      cloneTime(row.ExpiresAt),
		NextPollAt:     cloneTime(row.NextPollAt),
		LastPolledAt:   cloneTime(row.LastPolledAt),
		LockedBy:       cloneString(row.LockedBy),
		LockedUntil:    cloneTime(row.LockedUntil),
		PollAttempts:   row.PollAttempts,
		UserDeletedAt:  cloneTime(row.UserDeletedAt),
	}
}

func setCreateStringFromMetadata(builder *dbent.VideoTaskCreate, metadata map[string]any) {
	if v := metadataString(metadata, "model_mapping_chain"); v != "" {
		builder.SetModelMappingChain(v)
	}
	if v := metadataString(metadata, "upstream_base_url"); v != "" {
		builder.SetUpstreamBaseURL(v)
	}
	if v := metadataString(metadata, "idempotency_key"); v != "" {
		builder.SetIdempotencyKey(v)
	}
	if v := metadataString(metadata, "idempotency_key_hash"); v != "" {
		builder.SetIdempotencyKeyHash(v)
	}
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	v, _ := metadata[key].(string)
	return v
}

func rawObject(body []byte) map[string]any {
	if len(body) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func cloneString(s *string) *string {
	if s == nil {
		return nil
	}
	out := *s
	return &out
}

func int64Value(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func cloneInt64(n *int64) *int64 {
	if n == nil {
		return nil
	}
	out := *n
	return &out
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	out := *t
	return &out
}

func cloneBytes(b *[]byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), (*b)...)
}
