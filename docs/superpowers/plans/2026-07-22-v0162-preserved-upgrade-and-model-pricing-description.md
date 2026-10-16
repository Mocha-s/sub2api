# Sub2API v0.1.162 Preserved Upgrade and Model Pricing Description Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate canonical Sub2API `v0.1.162` into the customized production lineage, retain every local production behavior, add primary model-pricing descriptions, and deploy the verified result with complete application rollback coverage.

**Architecture:** Preserve the customized branch as the first parent of one explicit upstream merge, resolving dependency injection and gateway routes as functional unions. Add the description only to handwritten primary-pricing SQL and explicitly scope shared admin/frontend converters so account-statistics pricing cannot expose or persist it. Build from a clean Git archive, rehearse additive migrations 183-185 against a production database copy, then recreate only the production application container.

**Tech Stack:** Go 1.26.5, Gin, PostgreSQL 18, Redis 8, Google Wire, Vue 3, TypeScript, Vitest, pnpm 9, Docker BuildKit/Compose.

---

## Execution Constraints

- Run source commands from `/root/sub2api` unless a step names another working directory.
- Execute Tasks 1-3 in the current worktree because the production-deployed Gemini hotfix exists only as tracked working-tree changes until Task 2 commits it. Do not create an isolated worktree before that commit.
- Keep `deploy/sub2api.dump` untracked. Never run `git add -A`, `git add .`, `git stash -u`, `git stash --all`, or destructive cleanup.
- Do not fetch canonical `main`; fetch only the exact annotated `v0.1.162` tag.
- Do not edit generated `backend/cmd/server/wire_gen.go` by hand. Resolve `wire.go`, then regenerate it.
- Do not regenerate Ent for migration 185; channel pricing uses handwritten SQL and has no Ent schema.
- Treat pricing descriptions as plain text only. Do not add Markdown/HTML rendering, search indexing, billing logic, scheduling logic, or route-selection behavior for this field.
- Use `pnpm`, never `npm`.
- Build release images from `git archive HEAD`, not from the dirty workspace, because `.dockerignore` does not exclude `deploy/sub2api.dump`.
- Never print production `.env`, `config.yaml`, Docker inspect environment values, API keys, account credentials, proxy credentials, or the SSH password.
- An ordinary rollback restores the old application image and Compose file but leaves additive migrations 183-185 in place.
- Stop immediately on an unexpected migration checksum, dependency-container recreation, production state loss, settlement anomaly, or missing required worker.

## File Map

### Upstream Integration

- Modify: `backend/cmd/server/wire.go` - functional union of official and local long-running service cleanup.
- Regenerate: `backend/cmd/server/wire_gen.go` - Wire output from the resolved source.
- Modify: `backend/cmd/server/wire_gen_test.go` - exercise official auth/ops dependencies and local video workers in one cleanup signature.
- Modify: `backend/internal/server/routes/gateway.go` - one route registration per path with OpenAI durable-video and official Grok dispatch.
- Modify: `backend/internal/server/routes/gateway_test.go` - retain local route aliases and official Grok content/count-token/body-limit contracts.
- Review: `backend/internal/repository/api_key_repo.go`, `backend/internal/repository/http_upstream.go`, `backend/internal/service/account.go`, `backend/internal/service/api_key_service.go`, `backend/internal/service/api_key_service_cache.go`, `backend/internal/web/embed_on.go`, `backend/internal/web/embed_off.go`, and all merged provider-set files.

### Model-Pricing Description Backend

- Create: `backend/migrations/185_add_channel_model_pricing_description.sql` - additive primary-pricing column.
- Create: `backend/migrations/model_pricing_description_migration_test.go` - static migration scope/shape regression.
- Modify: `backend/internal/service/channel.go` - `ChannelModelPricing.Description`.
- Modify: `backend/internal/repository/channel_repo_pricing.go` - primary select/scan/insert/update/replace persistence.
- Modify: `backend/internal/repository/channel_repo_video_pricing_test.go` - SQL column and argument ordering.
- Create: `backend/internal/repository/channel_repo_pricing_integration_test.go` - create/update/replace round trip.
- Modify: `backend/internal/repository/migrations_schema_integration_test.go` - schema/default/account-stat exclusion/checksum assertions.
- Modify: `backend/internal/handler/admin/channel_handler.go` - scoped request/response conversion, trim, and validation.
- Modify: `backend/internal/handler/admin/channel_handler_test.go` - primary/account-stat conversion and response scope tests.
- Modify: `backend/internal/handler/admin/channel_video_pricing_test.go` - pass explicit conversion scope.
- Modify: `backend/internal/handler/available_channel_handler.go` - user DTO whitelist.
- Modify: `backend/internal/handler/available_channel_handler_test.go` - user DTO description regression.
- Modify: `backend/internal/service/channel_available.go` - preserve descriptions through LiteLLM display fallback.
- Modify: `backend/internal/service/channel_available_test.go` - fallback eligibility and preservation tests.
- Modify: `backend/internal/service/channel_test.go` - shared row description reaches every bound model.

### Model-Pricing Description Frontend

- Modify: `frontend/src/api/admin/channels.ts` - separate primary and account-stat pricing DTO shapes.
- Modify: `frontend/src/api/channels.ts` - user pricing description field.
- Modify: `frontend/src/components/admin/channel/types.ts` - form field and scoped serializers.
- Modify: `frontend/src/components/admin/channel/__tests__/types.spec.ts` - trim and omission tests.
- Modify: `frontend/src/components/admin/channel/PricingEntryCard.vue` - conditional textarea and live count.
- Create: `frontend/src/components/admin/channel/__tests__/PricingEntryCard.description.spec.ts` - editor behavior.
- Modify: `frontend/src/components/admin/channel/__tests__/PricingEntryCard.video.spec.ts` - satisfy the new required prop/field.
- Modify: `frontend/src/views/admin/ChannelsView.vue` - defaults, hydration, scoped serialization, and card props.
- Create: `frontend/src/views/admin/__tests__/ChannelsView.modelPricingDescription.spec.ts` - primary/account-stat form boundary.
- Modify: `frontend/src/views/admin/__tests__/ChannelsView.videoPricing.spec.ts` - typed fixture field updates.
- Modify: `frontend/src/i18n/locales/en/admin/channels.ts` - English editor copy.
- Modify: `frontend/src/i18n/locales/zh/admin/channels.ts` - Chinese editor copy.
- Create: `frontend/src/i18n/__tests__/modelPricingDescriptionLocales.spec.ts` - locale key coverage.
- Modify: `frontend/src/components/channels/SupportedModelChip.vue` - escaped multiline popover text.
- Modify: `frontend/src/components/channels/__tests__/SupportedModelChip.spec.ts` - content, escaping, ordering, and empty state.
- Create: `frontend/src/views/user/__tests__/AvailableChannelsView.modelPricingDescription.spec.ts` - prove search excludes pricing descriptions.

### Release Artifacts

- Create: `deploy/release/v0162_state_snapshot.sql` - stable, non-secret production state projection reused before and after rollout.
- Create outside Git: `/root/deploy-artifacts/$RELEASE_TAG/` - image, frontend, migration, source, and Compose manifests.
- Create outside Git: `/root/deploy-artifacts/sub2api-$RELEASE_TAG.tar.gz` - uniquely tagged image archive.
- Create on production: `/root/sub2api/deploy/backups/$RELEASE_TAG/` - immediate pre-deploy recovery point and state snapshots.

## Task 1: Capture the Pre-Upgrade Source Recovery Point

**Files:**
- Read: `backend/internal/service/gateway_usage_billing.go`
- Read: `backend/internal/service/gateway_record_usage_test.go`
- Copy outside Git: `deploy/sub2api.dump`

- [ ] **Step 1: Assert the exact starting worktree**

Run:

```bash
set -euo pipefail
cd /root/sub2api
test "$(git rev-parse HEAD^)" = 2efc227ad420b719e239f778a3a72d420056c37f
test "$(git diff-tree --no-commit-id --name-only -r HEAD)" = docs/superpowers/plans/2026-07-22-v0162-preserved-upgrade-and-model-pricing-description.md
test -z "$(git diff --cached --name-only)"
expected=$'backend/internal/service/gateway_record_usage_test.go\nbackend/internal/service/gateway_usage_billing.go'
test "$(git diff --name-only)" = "$expected"
test "$(git ls-files --others --exclude-standard -- deploy/sub2api.dump)" = deploy/sub2api.dump
! git ls-files --error-unmatch -- deploy/sub2api.dump >/dev/null 2>&1
```

Expected: every assertion exits zero; the index is empty and the dump is untracked.

- [ ] **Step 2: Create a root-only branch, bundle, patch, and dump backup**

Run:

```bash
set -euo pipefail
cd /root/sub2api
umask 077
SOURCE_BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)"
SOURCE_BACKUP_ROOT=/root/sub2api-upgrade-backups
SOURCE_BACKUP_DIR="$SOURCE_BACKUP_ROOT/source-pre-v0162-$SOURCE_BACKUP_TS"
SOURCE_BACKUP_REF="backup/pre-v0162-$SOURCE_BACKUP_TS"
test -d /root
install -d -m 0700 "$SOURCE_BACKUP_ROOT" "$SOURCE_BACKUP_DIR"
git branch "$SOURCE_BACKUP_REF" HEAD
git show-ref >"$SOURCE_BACKUP_DIR/show-ref.txt"
git log --graph --decorate --oneline --all -100 >"$SOURCE_BACKUP_DIR/history.txt"
git status --porcelain=v2 --branch --untracked-files=all >"$SOURCE_BACKUP_DIR/status.txt"
git diff --binary --full-index --cached >"$SOURCE_BACKUP_DIR/staged.patch"
git diff --binary --full-index >"$SOURCE_BACKUP_DIR/unstaged.patch"
git ls-files --others --exclude-standard -z >"$SOURCE_BACKUP_DIR/untracked.nul"
git ls-files --others --ignored --exclude-standard -z >"$SOURCE_BACKUP_DIR/ignored.nul"
install -m 0600 deploy/sub2api.dump "$SOURCE_BACKUP_DIR/deploy-sub2api.dump"
cmp -- deploy/sub2api.dump "$SOURCE_BACKUP_DIR/deploy-sub2api.dump"
git bundle create "$SOURCE_BACKUP_DIR/sub2api-all-refs.bundle" --all
git bundle verify "$SOURCE_BACKUP_DIR/sub2api-all-refs.bundle" >"$SOURCE_BACKUP_DIR/bundle-verify.txt" 2>&1
git bundle list-heads "$SOURCE_BACKUP_DIR/sub2api-all-refs.bundle" >"$SOURCE_BACKUP_DIR/bundle-heads.txt"
sha256sum "$SOURCE_BACKUP_DIR"/* >"$SOURCE_BACKUP_DIR/SHA256SUMS"
chmod -R go-rwx "$SOURCE_BACKUP_DIR"
printf '%s\n' "$SOURCE_BACKUP_DIR"
```

Expected: the command prints one root-only backup directory; `bundle-verify.txt` records a valid bundle and `cmp` succeeds.

- [ ] **Step 3: Recheck that backup creation did not stage the dump**

Run:

```bash
cd /root/sub2api
test -z "$(git diff --cached --name-only)"
test "$(git ls-files --others --exclude-standard -- deploy/sub2api.dump)" = deploy/sub2api.dump
git status --short
```

Expected: only the two tracked hotfix files and `?? deploy/sub2api.dump` are shown.

## Task 2: Formalize the Deployed Gemini Image-Count Hotfix

**Files:**
- Modify: `backend/internal/service/gateway_usage_billing.go:771`
- Test: `backend/internal/service/gateway_record_usage_test.go`

- [ ] **Step 1: Review the hotfix diff and its billing guard**

Run:

```bash
cd /root/sub2api
git diff --check -- backend/internal/service/gateway_usage_billing.go backend/internal/service/gateway_record_usage_test.go
git diff -- backend/internal/service/gateway_usage_billing.go backend/internal/service/gateway_record_usage_test.go
```

Expected: the implementation defaults `ImageCount` to one only when resolved channel pricing uses `BillingModeImage`, then recalculates image billing normalization; the test targets the Gemini image alias with no detected count.

- [ ] **Step 2: Run the focused regression test**

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go test -tags=unit ./internal/service -run '^TestGatewayServiceRecordUsage_ChannelImageAliasWithoutDetectedImageCountBillsOneImageRequest$' -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit only the two hotfix files**

Run:

```bash
cd /root/sub2api
expected=$'backend/internal/service/gateway_record_usage_test.go\nbackend/internal/service/gateway_usage_billing.go'
test "$(git diff --name-only)" = "$expected"
git add -- backend/internal/service/gateway_record_usage_test.go backend/internal/service/gateway_usage_billing.go
test "$(git diff --cached --name-only)" = "$expected"
git diff --cached --check
git commit -m "fix(billing): count Gemini image alias requests"
test "$(git ls-files --others --exclude-standard -- deploy/sub2api.dump)" = deploy/sub2api.dump
```

Expected: one dedicated hotfix commit; the dump remains untracked.

## Task 3: Fetch and Identify Canonical v0.1.162

**Files:**
- No worktree files change.
- Create refs: `refs/tags/v0.1.162`, `refs/heads/integration/v0.1.162-preserved`

- [ ] **Step 1: Fetch the exact canonical tag into a temporary ref**

Run:

```bash
set -euo pipefail
cd /root/sub2api
CANONICAL_URL=https://github.com/Wei-Shaw/sub2api.git
VERIFY_REF=refs/canonical-verify/v0.1.162
! git show-ref --verify --quiet "$VERIFY_REF"
git fetch --no-tags "$CANONICAL_URL" "refs/tags/v0.1.162:$VERIFY_REF"
```

Expected: only the requested tag object and its reachable history are fetched.

- [ ] **Step 2: Verify the pinned tag, commit, tree, ancestry, and delta**

Run:

```bash
set -euo pipefail
cd /root/sub2api
VERIFY_REF=refs/canonical-verify/v0.1.162
TAG_OID="$(git rev-parse "$VERIFY_REF")"
TARGET_COMMIT="$(git rev-parse "$VERIFY_REF^{}")"
TARGET_TREE="$(git rev-parse "$VERIFY_REF^{tree}")"
test "$(git cat-file -t "$VERIFY_REF")" = tag
test "$TAG_OID" = 34b7a5ad70b4b9b9bb96955562fe632ad625d783
test "$TARGET_COMMIT" = 27f094e0960ebd8e52de7ff7e763c6fec2ff4057
test "$TARGET_TREE" = d5efb21746149a2eca1938d9df51f3d8c89f13d9
git fsck --strict --connectivity-only "$TAG_OID"
git merge-base --is-ancestor refs/tags/v0.1.160 "$TARGET_COMMIT"
test "$(git rev-list --count "refs/tags/v0.1.160..$TARGET_COMMIT")" -eq 176
test "$(git diff --name-only refs/tags/v0.1.160 "$TARGET_COMMIT" | wc -l)" -eq 377
```

Expected: every pinned identity and range assertion succeeds. The annotated tag is unsigned, so these exact object IDs over canonical HTTPS are the release identity gate.

- [ ] **Step 3: Install the verified tag and create the integration branch**

Run:

```bash
set -euo pipefail
cd /root/sub2api
VERIFY_REF=refs/canonical-verify/v0.1.162
TAG_OID="$(git rev-parse "$VERIFY_REF")"
git update-ref refs/tags/v0.1.162 "$TAG_OID"
git update-ref -d "$VERIFY_REF" "$TAG_OID"
git switch -c integration/v0.1.162-preserved
test "$(git status --porcelain=v1 --untracked-files=all)" = "?? deploy/sub2api.dump"
```

Expected: the integration branch starts after the docs and Gemini hotfix commits with no tracked changes.

## Task 4: Merge v0.1.162 as a Functional Union

**Files:**
- Modify: `backend/cmd/server/wire.go`
- Regenerate: `backend/cmd/server/wire_gen.go`
- Modify: `backend/cmd/server/wire_gen_test.go`
- Modify: `backend/internal/server/routes/gateway.go`
- Modify: `backend/internal/server/routes/gateway_test.go`
- Review all 23 semantically overlapping paths reported by the merge audit.

- [ ] **Step 1: Start the explicit no-fast-forward merge without committing**

Run:

```bash
set -euo pipefail
cd /root/sub2api
PREMERGE_COMMIT="$(git rev-parse HEAD)"
TARGET_COMMIT=27f094e0960ebd8e52de7ff7e763c6fec2ff4057
printf '%s\n' "$PREMERGE_COMMIT" >/tmp/opencode/sub2api-v0162-premerge-commit
set +e
git merge --no-ff --no-commit refs/tags/v0.1.162
MERGE_RC=$?
set -e
test "$(git rev-parse MERGE_HEAD)" = "$TARGET_COMMIT"
if test "$MERGE_RC" -ne 0; then
  git diff --name-only --diff-filter=U
fi
```

Expected: Git enters a merge. The known direct conflicts are `backend/cmd/server/wire_gen.go` and `backend/internal/server/routes/gateway.go`; inspect any additional unmerged path before changing it and preserve both feature sets.

- [ ] **Step 2: Resolve gateway helper dispatch in `gateway.go`**

Retain official `textBodyLimit`, local OpenAI durable-video helpers, and these shared dispatchers:

```go
countTokensHandler := func(c *gin.Context) {
	switch getGroupPlatform(c) {
	case service.PlatformOpenAI:
		h.OpenAIGateway.CountTokens(c)
	case service.PlatformGrok:
		h.OpenAIGateway.GrokCountTokens(c)
	default:
		h.Gateway.CountTokens(c)
	}
}

videoStatusHandler := func(c *gin.Context) {
	switch getGroupPlatform(c) {
	case service.PlatformGrok:
		h.OpenAIGateway.GrokVideoStatus(c)
	case service.PlatformOpenAI:
		h.VideoTask.Fetch(c)
	default:
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{"type": "not_found_error", "message": "Videos API is not supported for this platform"},
		})
	}
}

videoContentHandler := func(c *gin.Context) {
	switch getGroupPlatform(c) {
	case service.PlatformGrok:
		h.OpenAIGateway.GrokVideoContent(c)
	case service.PlatformOpenAI:
		h.VideoTask.Content(c)
	default:
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{"type": "not_found_error", "message": "Videos API is not supported for this platform"},
		})
	}
}
```

Use `countTokensHandler` for both `/v1/messages/count_tokens` and `/messages/count_tokens`. Use `textBodyLimit` on all official alpha-search and embeddings registrations.

- [ ] **Step 3: Resolve gateway route registrations as one path per method**

The `/v1` group must contain this union without duplicate Gin registrations:

```go
gateway.POST("/video/generations", openAIVideoTaskHandler(h.VideoTask.CreateGenerationsCompat))
gateway.GET("/video/generations", openAIVideoTaskHandler(h.VideoTask.List))
gateway.POST("/video/generations/estimate", openAIVideoTaskHandler(h.VideoTask.EstimateGenerationsCompat))
gateway.POST("/video/generations/references", openAIVideoTaskHandler(h.VideoTask.ReferencesGenerationsCompat))
gateway.POST("/video/generations/material-assets", openAIVideoTaskHandler(h.VideoTask.MaterialAssetsGenerationsCompat))
gateway.POST("/video/generations/:request_id/refresh", openAIVideoTaskHandler(h.VideoTask.Refresh))
gateway.POST("/video/generations/:request_id/cancel", openAIVideoTaskHandler(h.VideoTask.Cancel))
gateway.GET("/video/generations/:request_id", openAIVideoTaskHandler(h.VideoTask.Fetch))
gateway.GET("/video/generations/:request_id/content", openAIVideoTaskHandler(h.VideoTask.Content))

gateway.POST("/videos", openAIVideoTaskHandler(h.VideoTask.Create))
gateway.GET("/videos", openAIVideoTaskHandler(h.VideoTask.List))
gateway.POST("/videos/estimate", openAIVideoTaskHandler(h.VideoTask.Estimate))
gateway.POST("/videos/references", openAIVideoTaskHandler(h.VideoTask.References))
gateway.POST("/videos/material-assets", openAIVideoTaskHandler(h.VideoTask.MaterialAssets))
gateway.POST("/videos/generations", videoGenerationHandler)
gateway.POST("/videos/edits", videoEditHandler)
gateway.POST("/videos/extensions", videoExtensionHandler)
gateway.GET("/videos/generations/:request_id", openAIVideoTaskHandler(h.VideoTask.Fetch))
gateway.GET("/videos/generations/:request_id/content", openAIVideoTaskHandler(h.VideoTask.Content))
gateway.POST("/videos/:request_id/refresh", openAIVideoTaskHandler(h.VideoTask.Refresh))
gateway.POST("/videos/:request_id/cancel", openAIVideoTaskHandler(h.VideoTask.Cancel))
gateway.GET("/videos/:request_id", videoStatusHandler)
gateway.GET("/videos/:request_id/content", videoContentHandler)
```

Register this exact no-prefix union with the existing middleware chain on every route:

```go
r.POST("/messages/count_tokens", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, countTokensHandler)
r.POST("/video/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.CreateGenerationsCompat))
r.GET("/video/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.List))
r.POST("/video/generations/estimate", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.EstimateGenerationsCompat))
r.POST("/video/generations/references", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.ReferencesGenerationsCompat))
r.POST("/video/generations/material-assets", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.MaterialAssetsGenerationsCompat))
r.POST("/video/generations/:request_id/refresh", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.Refresh))
r.POST("/video/generations/:request_id/cancel", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.Cancel))
r.GET("/video/generations/:request_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.Fetch))
r.GET("/video/generations/:request_id/content", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.Content))
r.POST("/videos", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.Create))
r.GET("/videos", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.List))
r.POST("/videos/estimate", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.Estimate))
r.POST("/videos/references", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.References))
r.POST("/videos/material-assets", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.MaterialAssets))
r.POST("/videos/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, videoGenerationHandler)
r.GET("/videos/generations/:request_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.Fetch))
r.GET("/videos/generations/:request_id/content", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.Content))
r.POST("/videos/:request_id/refresh", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.Refresh))
r.POST("/videos/:request_id/cancel", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, openAIVideoTaskHandler(h.VideoTask.Cancel))
r.POST("/videos/edits", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, videoEditHandler)
r.POST("/videos/extensions", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, videoExtensionHandler)
r.GET("/videos/:request_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, videoStatusHandler)
r.GET("/videos/:request_id/content", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, videoContentHandler)
```

- [ ] **Step 4: Merge gateway route tests before running the implementation**

Keep `TestGatewayRoutesOpenAIVideoTaskPathsAreRegistered`, and make the Grok test cover content:

```go
for _, path := range []string{
	"/v1/videos/request-123",
	"/videos/request-123",
	"/v1/videos/request-123/content",
	"/videos/request-123/content",
} {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit Grok video handler", path)
	require.NotContains(t, w.Body.String(), "not supported for this platform")
}
```

Change the Grok count-token assertion to the official local estimator and cover both aliases:

```go
countTokensRouter := newGatewayRoutesTestRouterWithConfig(&config.Config{
	Gateway: config.GatewayConfig{MaxBodySize: 1024 * 1024},
}, service.PlatformGrok)
for _, path := range []string{"/v1/messages/count_tokens", "/messages/count_tokens"} {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"grok","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	countTokensRouter.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "path=%s", path)
	var response struct {
		InputTokens int `json:"input_tokens"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response), "path=%s", path)
	require.Positive(t, response.InputTokens, "path=%s", path)
}
```

The non-video-platform test must use Anthropic as the rejected platform because OpenAI now legitimately owns all durable-video aliases.

- [ ] **Step 5: Resolve `wire.go` with official and local lifecycle dependencies**

Use this complete `provideCleanup` argument list, which includes official auth/ops dependencies and both local video workers:

```go
	func provideCleanup(
	entClient *ent.Client,
	rdb *redis.Client,
	opsMetricsCollector *service.OpsMetricsCollector,
	opsAggregation *service.OpsAggregationService,
	opsAlertEvaluator *service.OpsAlertEvaluatorService,
	opsCleanup *service.OpsCleanupService,
	opsScheduledReport *service.OpsScheduledReportService,
	opsSystemLogSink *service.OpsSystemLogSink,
	opsService *service.OpsService,
	opsIngressReject *service.OpsIngressRejectAggregator,
	apiKeyService *service.APIKeyService,
	authCacheInvalidationWorker *service.AuthCacheInvalidationWorker,
	schedulerSnapshot *service.SchedulerSnapshotService,
	tokenRefresh *service.TokenRefreshService,
	accountExpiry *service.AccountExpiryService,
	proxyExpiry *service.ProxyExpiryService,
	subscriptionExpiry *service.SubscriptionExpiryService,
	usageCleanup *service.UsageCleanupService,
	idempotencyCleanup *service.IdempotencyCleanupService,
	batchImageCleanup *service.BatchImageCleanupService,
	batchImageWorker *service.BatchImageWorkerRuntime,
	pricing *service.PricingService,
	emailQueue *service.EmailQueueService,
	billingCache *service.BillingCacheService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	subscriptionService *service.SubscriptionService,
	oauth *service.OAuthService,
	openaiOAuth *service.OpenAIOAuthService,
	geminiOAuth *service.GeminiOAuthService,
	antigravityOAuth *service.AntigravityOAuthService,
	grokOAuth *service.GrokOAuthService,
	videoTaskPoller *service.VideoTaskPoller,
	videoTaskSettlementReconciler *service.VideoTaskSettlementReconciler,
	openAIGateway *service.OpenAIGatewayService,
	scheduledTestRunner *service.ScheduledTestRunnerService,
	backupSvc *service.BackupService,
	paymentOrderExpiry *service.PaymentOrderExpiryService,
	channelMonitorRunner *service.ChannelMonitorRunner,
	quotaFlusher *service.UserPlatformQuotaUsageFlusher,
	upstreamBillingProbe *service.UpstreamBillingProbeService,
	auditLog *service.AuditLogService,
	promptAudit *securityaudit.PromptService,
) func() {
```

Add these exact official cleanup steps near the front of `parallelSteps` and retain the existing local `VideoTaskPoller` and `VideoTaskSettlementReconciler` steps:

```go
{"OpsIngressRejectAggregator", func() error {
	if opsIngressReject != nil {
		opsIngressReject.Stop()
	}
	return nil
}},
{"AuthCacheInvalidationWorker", func() error {
	if authCacheInvalidationWorker != nil {
		authCacheInvalidationWorker.Stop()
	}
	return nil
}},
{"AuthCacheInvalidationSubscriber", func() error {
	if apiKeyService != nil {
		apiKeyService.StopAuthCacheInvalidationSubscriber()
	}
	return nil
}},
{"OpsRuntimeSettingsRefresh", func() error {
	if opsService != nil {
		opsService.StopRuntimeSettingsRefresh()
	}
	return nil
}},
```

- [ ] **Step 6: Update the cleanup test signature and regenerate Wire twice**

In `wire_gen_test.go`, pass these official nil dependencies before `schedulerSnapshotSvc`, while keeping the local video-worker nils before `openAIGateway`:

```go
		nil, // opsService
		nil, // opsIngressRejectAggregator
		nil, // apiKeyService
		nil, // authCacheInvalidationWorker
		schedulerSnapshotSvc,
```

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go generate ./cmd/server
git diff --check -- cmd/server/wire.go cmd/server/wire_gen.go cmd/server/wire_gen_test.go
git add -- \
  cmd/server/wire.go \
  cmd/server/wire_gen.go \
  cmd/server/wire_gen_test.go \
  internal/server/routes/gateway.go \
  internal/server/routes/gateway_test.go
test -z "$(git diff --name-only --diff-filter=U)"
GOTOOLCHAIN=go1.26.5 go generate ./cmd/server
git diff --exit-code -- cmd/server/wire_gen.go
```

Expected: the second generation is stable.

- [ ] **Step 7: Review mechanically clean semantic overlaps**

Run:

```bash
cd /root/sub2api
PREMERGE_COMMIT="$(</tmp/opencode/sub2api-v0162-premerge-commit)"
git diff --stat "$PREMERGE_COMMIT"
git diff "$PREMERGE_COMMIT" -- \
  backend/internal/repository/api_key_repo.go \
  backend/internal/repository/http_upstream.go \
  backend/internal/repository/http_upstream_test.go \
  backend/internal/repository/migrations_schema_integration_test.go \
  backend/internal/server/api_contract_test.go \
  backend/internal/service/account.go \
  backend/internal/service/api_key_service.go \
  backend/internal/service/api_key_service_cache.go \
  backend/internal/web/embed_on.go \
  backend/internal/web/embed_off.go \
  frontend/src/types/index.ts
```

Confirm in the diff that local managed-pricing markers and markup multiplication remain, official auth-cache invalidation hooks are present, OpenAI/Codex failover fixes remain, embedded frontend bypass behavior is preserved, and all official provider-set additions survived.

- [ ] **Step 8: Run focused integration-union tests before committing**

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go test ./cmd/server ./internal/server/routes -count=1
GOTOOLCHAIN=go1.26.5 go test -tags=unit ./internal/service ./internal/repository ./internal/handler ./internal/server/routes \
  -run 'ChannelImageAliasWithoutDetectedImageCount|ManagedPricing|PricingMarker|SchedulerMetadata|VideoTask|GrokVideo|SameAccountRetry|AuthCacheInvalidation|OpsIngressReject|ForwardedClientIP|ImageStorage' \
  -count=1
```

Expected: PASS, including router construction without a duplicate-route panic.

- [ ] **Step 9: Commit the merge and verify parent order**

Run:

```bash
set -euo pipefail
cd /root/sub2api
PREMERGE_COMMIT="$(</tmp/opencode/sub2api-v0162-premerge-commit)"
TARGET_COMMIT=27f094e0960ebd8e52de7ff7e763c6fec2ff4057
test -z "$(git diff --name-only --diff-filter=U)"
git diff --cached --check
git commit -m "merge: integrate canonical v0.1.162 preserving local behavior"
test "$(git rev-parse HEAD^1)" = "$PREMERGE_COMMIT"
test "$(git rev-parse HEAD^2)" = "$TARGET_COMMIT"
git merge-base --is-ancestor "$TARGET_COMMIT" HEAD
test "$(git ls-files --others --exclude-standard -- deploy/sub2api.dump)" = deploy/sub2api.dump
```

Expected: one merge commit with the customized lineage as first parent and canonical `v0.1.162` as second parent.

## Task 5: Add Migration 185 and the Domain Field

**Files:**
- Create: `backend/migrations/185_add_channel_model_pricing_description.sql`
- Create: `backend/migrations/model_pricing_description_migration_test.go`
- Modify: `backend/internal/repository/migrations_schema_integration_test.go`
- Modify: `backend/internal/service/channel.go:86-105`

- [ ] **Step 1: Write the failing static migration test**

Create `backend/migrations/model_pricing_description_migration_test.go`:

```go
package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration185AddsPrimaryModelPricingDescriptionOnly(t *testing.T) {
	content, err := FS.ReadFile("185_add_channel_model_pricing_description.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Equal(t,
		"ALTER TABLE channel_model_pricing ADD COLUMN IF NOT EXISTS description VARCHAR(500) NOT NULL DEFAULT '';",
		sql,
	)
	require.NotContains(t, sql, "channel_account_stats_model_pricing")
}
```

- [ ] **Step 2: Run the test to verify the migration is absent**

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go test ./migrations -run '^TestMigration185AddsPrimaryModelPricingDescriptionOnly$' -count=1
```

Expected: FAIL because `185_add_channel_model_pricing_description.sql` does not exist.

- [ ] **Step 3: Add schema and checksum assertions to the integration migration test**

Add these imports to `migrations_schema_integration_test.go`:

```go
"crypto/sha256"
"encoding/hex"
"strings"

"github.com/Wei-Shaw/sub2api/migrations"
```

After the primary video-pricing column assertions, add:

```go
	requireColumn(t, tx, "channel_model_pricing", "description", "character varying", 500, false)
	requireColumnDefaultContains(t, tx, "channel_model_pricing", "description", "''::character varying")

	var accountStatsDescriptionColumns int
	require.NoError(t, tx.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = 'channel_account_stats_model_pricing'
  AND column_name = 'description'
`).Scan(&accountStatsDescriptionColumns))
	require.Zero(t, accountStatsDescriptionColumns)

	content, err := migrations.FS.ReadFile("185_add_channel_model_pricing_description.sql")
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(content))))
	var appliedChecksum string
	require.NoError(t, tx.QueryRowContext(
		context.Background(),
		"SELECT checksum FROM schema_migrations WHERE filename = $1",
		"185_add_channel_model_pricing_description.sql",
	).Scan(&appliedChecksum))
	require.Equal(t, hex.EncodeToString(sum[:]), appliedChecksum)
```

- [ ] **Step 4: Add migration 185 and the service field**

Create `backend/migrations/185_add_channel_model_pricing_description.sql`:

```sql
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS description VARCHAR(500) NOT NULL DEFAULT '';
```

Add the field immediately after `Models` in `service.ChannelModelPricing`:

```go
	Models              []string          // bound model names
	Description         string            // shared plain-text description for every model in this row
	BillingMode         BillingMode
```

`Clone()` needs no new branch because `cp := p` already copies strings.

- [ ] **Step 5: Run migration unit and integration tests**

Run:

```bash
cd /root/sub2api/backend
gofmt -w internal/service/channel.go internal/repository/migrations_schema_integration_test.go migrations/model_pricing_description_migration_test.go
GOTOOLCHAIN=go1.26.5 go test ./migrations -run '^TestMigration185AddsPrimaryModelPricingDescriptionOnly$' -count=1
GOTOOLCHAIN=go1.26.5 go test -tags=integration ./internal/repository -run '^TestMigrationsRunner_IsIdempotent_AndSchemaIsUpToDate$' -count=1 -timeout=15m
```

Expected: PASS; migration 185 is embedded automatically by `//go:embed *.sql`.

- [ ] **Step 6: Commit the schema boundary**

Run:

```bash
cd /root/sub2api
git add -- \
  backend/migrations/185_add_channel_model_pricing_description.sql \
  backend/migrations/model_pricing_description_migration_test.go \
  backend/internal/repository/migrations_schema_integration_test.go \
  backend/internal/service/channel.go
git diff --cached --check
git commit -m "feat(pricing): add model description schema"
```

## Task 6: Persist Primary Pricing Descriptions

**Files:**
- Modify: `backend/internal/repository/channel_repo_video_pricing_test.go`
- Create: `backend/internal/repository/channel_repo_pricing_integration_test.go`
- Modify: `backend/internal/repository/channel_repo_pricing.go`
- Verify unchanged: `backend/internal/repository/channel_repo_account_stats_pricing.go`

- [ ] **Step 1: Update SQL-mock tests to require the description column**

In the list test, add `description` before timestamps in the expected SQL and row, then assert it:

```go
mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, channel_id, platform, models, billing_mode, input_price, output_price, cache_write_price, cache_read_price, image_input_price, image_output_price, per_request_price, video_price_per_second, video_default_seconds, video_allowed_seconds, description, created_at, updated_at
		 FROM channel_model_pricing WHERE channel_id = $1 ORDER BY id`)).
	WithArgs(int64(7)).
	WillReturnRows(sqlmock.NewRows([]string{
		"id", "channel_id", "platform", "models", "billing_mode", "input_price", "output_price",
		"cache_write_price", "cache_read_price", "image_input_price", "image_output_price", "per_request_price",
		"video_price_per_second", "video_default_seconds", "video_allowed_seconds", "description", "created_at", "updated_at",
	}).AddRow(int64(11), int64(7), "openai", []byte(`["sora-2"]`), "video", nil, nil, nil, nil, nil, nil, nil, 0.03, 10, []byte(`[5,10]`), "Fast video\nShared by aliases", now, now))
```

Add:

```go
require.Equal(t, "Fast video\nShared by aliases", got[0].Description)
```

Apply the same `description` column position to the batch-load expected query and empty row schema.

- [ ] **Step 2: Update insert/update SQL-mock expectations and account-stat exclusion**

Set `Description: "Fast video"` on the primary pricing fixture and require:

```go
mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO channel_model_pricing (channel_id, platform, models, billing_mode, input_price, output_price, cache_write_price, cache_read_price, image_input_price, image_output_price, per_request_price, video_price_per_second, video_default_seconds, video_allowed_seconds, description)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id, created_at, updated_at`)).
	WithArgs(int64(7), "openai", []byte(`["sora-2"]`), service.BillingModeVideo, nil, nil, nil, nil, nil, nil, nil, price, seconds, []byte(`[5,10]`), "Fast video")

mock.ExpectExec(regexp.QuoteMeta(`UPDATE channel_model_pricing
		 SET models = $1, billing_mode = $2, input_price = $3, output_price = $4, cache_write_price = $5, cache_read_price = $6, image_input_price = $7, image_output_price = $8, per_request_price = $9, video_price_per_second = $10, video_default_seconds = $11, video_allowed_seconds = $12, platform = $13, description = $14, updated_at = NOW()
		 WHERE id = $15`)).
	WithArgs([]byte(`["sora-2"]`), service.BillingModeVideo, nil, nil, nil, nil, nil, nil, nil, price, seconds, []byte(`[5,10]`), "openai", "Fast video", int64(11))
```

Set `Description: "must not persist"` on the account-stat write fixture but leave its existing 14-column SQL and argument expectation unchanged. Rename that test to `TestAccountStatsPricingWritesVideoColumnsAndIgnoresDescription`.

- [ ] **Step 3: Write the primary repository round-trip integration test**

Create `backend/internal/repository/channel_repo_pricing_integration_test.go`:

```go
//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestChannelRepository_ModelPricingDescriptionRoundTrip(t *testing.T) {
	ctx := context.Background()
	var channelID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO channels (name, description, status)
VALUES ($1, '', 'active')
RETURNING id
`, fmt.Sprintf("pricing-description-%d", time.Now().UnixNano())).Scan(&channelID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM channels WHERE id = $1", channelID)
	})

	repo := &channelRepository{db: integrationDB}
	pricing := service.ChannelModelPricing{
		ChannelID:   channelID,
		Platform:    service.PlatformOpenAI,
		Models:      []string{"gpt-test", "gpt-test-alias"},
		Description: "First line\nSecond line",
		BillingMode: service.BillingModeToken,
	}
	require.NoError(t, repo.CreateModelPricing(ctx, &pricing))

	listed, err := repo.ListModelPricing(ctx, channelID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "First line\nSecond line", listed[0].Description)

	pricing.Description = "Updated description"
	require.NoError(t, repo.UpdateModelPricing(ctx, &pricing))
	listed, err = repo.ListModelPricing(ctx, channelID)
	require.NoError(t, err)
	require.Equal(t, "Updated description", listed[0].Description)

	require.NoError(t, repo.ReplaceModelPricing(ctx, channelID, []service.ChannelModelPricing{
		{Platform: service.PlatformOpenAI, Models: []string{"one"}, Description: "One", BillingMode: service.BillingModeToken},
		{Platform: service.PlatformOpenAI, Models: []string{"two"}, Description: "Two", BillingMode: service.BillingModePerRequest},
	}))
	listed, err = repo.ListModelPricing(ctx, channelID)
	require.NoError(t, err)
	require.Equal(t, []string{"One", "Two"}, []string{listed[0].Description, listed[1].Description})
}
```

- [ ] **Step 4: Run tests to verify primary SQL persistence is still missing**

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go test -tags=unit ./internal/repository \
  -run 'Test(ListModelPricingScansVideoColumnsInOrder|BatchLoadModelPricingSelectsVideoColumnsInOrder|MainPricingWritesVideoColumnsAndArgumentsInOrder|AccountStatsPricingWritesVideoColumnsAndIgnoresDescription)$' \
  -count=1
```

Expected: FAIL on primary query/argument mismatches while account-stat SQL remains description-free.

- [ ] **Step 5: Add description to every primary SQL path**

Update both primary SELECT lists to place `description` after `video_allowed_seconds`. Update the scan suffix to:

```go
&videoAllowedSecondsJSON, &p.Description, &p.CreatedAt, &p.UpdatedAt,
```

Update the INSERT to 15 columns/parameters and end its arguments with:

```go
pricing.VideoDefaultSeconds, videoAllowedSecondsJSON, pricing.Description,
```

Update the UPDATE statement to:

```sql
UPDATE channel_model_pricing
SET models = $1, billing_mode = $2, input_price = $3, output_price = $4,
    cache_write_price = $5, cache_read_price = $6, image_input_price = $7,
    image_output_price = $8, per_request_price = $9, video_price_per_second = $10,
    video_default_seconds = $11, video_allowed_seconds = $12, platform = $13,
    description = $14, updated_at = NOW()
WHERE id = $15
```

End the arguments with:

```go
pricing.VideoDefaultSeconds, videoAllowedSecondsJSON, pricing.Platform, pricing.Description, pricing.ID,
```

Do not modify `channel_repo_account_stats_pricing.go`.

- [ ] **Step 6: Run unit and integration persistence tests**

Run:

```bash
cd /root/sub2api/backend
gofmt -w internal/repository/channel_repo_pricing.go internal/repository/channel_repo_video_pricing_test.go internal/repository/channel_repo_pricing_integration_test.go
GOTOOLCHAIN=go1.26.5 go test -tags=unit ./internal/repository \
  -run 'Test(ListModelPricingScansVideoColumnsInOrder|BatchLoadModelPricingSelectsVideoColumnsInOrder|MainPricingWritesVideoColumnsAndArgumentsInOrder|AccountStatsPricingWritesVideoColumnsAndIgnoresDescription)$' \
  -count=1
GOTOOLCHAIN=go1.26.5 go test -tags=integration ./internal/repository \
  -run 'Test(ChannelRepository_ModelPricingDescriptionRoundTrip|MigrationsRunner_IsIdempotent_AndSchemaIsUpToDate)$' \
  -count=1 -timeout=15m
```

Expected: PASS.

- [ ] **Step 7: Commit repository persistence**

Run:

```bash
cd /root/sub2api
git add -- \
  backend/internal/repository/channel_repo_pricing.go \
  backend/internal/repository/channel_repo_video_pricing_test.go \
  backend/internal/repository/channel_repo_pricing_integration_test.go
git diff --cached --check
git commit -m "feat(pricing): persist model descriptions"
```

## Task 7: Scope Admin Description Input and Output

**Files:**
- Modify: `backend/internal/handler/admin/channel_handler.go`
- Modify: `backend/internal/handler/admin/channel_handler_test.go`
- Modify: `backend/internal/handler/admin/channel_video_pricing_test.go`

- [ ] **Step 1: Write failing validation and scope tests**

Add `strings` and `github.com/gin-gonic/gin/binding` to `channel_handler_test.go`, then add:

```go
func TestChannelModelPricingRequestDescriptionValidation(t *testing.T) {
	valid := createChannelRequest{
		Name: "valid",
		ModelPricing: []channelModelPricingRequest{{
			Models:      []string{"model"},
			Description: strings.Repeat("界", 500),
		}},
	}
	require.NoError(t, binding.Validator.ValidateStruct(valid))

	invalid := valid
	invalid.ModelPricing = []channelModelPricingRequest{{
		Models:      []string{"model"},
		Description: strings.Repeat("界", 501),
	}}
	require.Error(t, binding.Validator.ValidateStruct(invalid))
}

func TestPricingRequestToService_DescriptionScope(t *testing.T) {
	reqs := []channelModelPricingRequest{{
		Models:      []string{"model"},
		Description: "  First line\nSecond line  ",
	}}
	primary := pricingRequestToService(reqs, pricingScopePrimary)
	require.Equal(t, "First line\nSecond line", primary[0].Description)

	accountStats := pricingRequestToService(reqs, pricingScopeAccountStats)
	require.Empty(t, accountStats[0].Description)
}

func TestChannelToResponse_DescriptionScope(t *testing.T) {
	channel := &service.Channel{
		Name:      "described",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		ModelPricing: []service.ChannelModelPricing{{
			Models:      []string{"primary"},
			Description: "Primary description",
		}},
		AccountStatsPricingRules: []service.AccountStatsPricingRule{{
			Pricing: []service.ChannelModelPricing{{
				Models:      []string{"stats"},
				Description: "must not be exposed",
			}},
		}},
	}

	raw, err := json.Marshal(channelToResponse(channel))
	require.NoError(t, err)
	var decoded struct {
		ModelPricing []map[string]any `json:"model_pricing"`
		AccountStatsPricingRules []struct {
			Pricing []map[string]any `json:"pricing"`
		} `json:"account_stats_pricing_rules"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, "Primary description", decoded.ModelPricing[0]["description"])
	_, exposed := decoded.AccountStatsPricingRules[0].Pricing[0]["description"]
	require.False(t, exposed)
}
```

- [ ] **Step 2: Run tests to verify the scope API does not exist**

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go test -tags=unit ./internal/handler/admin \
  -run 'Test(ChannelModelPricingRequestDescriptionValidation|PricingRequestToService_DescriptionScope|ChannelToResponse_DescriptionScope)$' \
  -count=1
```

Expected: FAIL to compile because description fields and scope arguments are absent.

- [ ] **Step 3: Add explicit pricing scopes and DTO fields**

Add:

```go
type pricingScope uint8

const (
	pricingScopePrimary pricingScope = iota
	pricingScopeAccountStats
)
```

Add to the request:

```go
Description string `json:"description" binding:"max=500"`
```

Add to the response:

```go
Description *string `json:"description,omitempty"`
```

Change the converter signatures to:

```go
func pricingToResponse(p *service.ChannelModelPricing, scope pricingScope) channelModelPricingResponse
func pricingRequestToService(reqs []channelModelPricingRequest, scope pricingScope) []service.ChannelModelPricing
```

In `pricingToResponse`, construct the existing response, then expose a pointer only for primary pricing:

```go
	resp := channelModelPricingResponse{
		ID:                  p.ID,
		Platform:            platform,
		Models:              models,
		BillingMode:         billingMode,
		InputPrice:          p.InputPrice,
		OutputPrice:         p.OutputPrice,
		CacheWritePrice:     p.CacheWritePrice,
		CacheReadPrice:      p.CacheReadPrice,
		ImageInputPrice:     p.ImageInputPrice,
		ImageOutputPrice:    p.ImageOutputPrice,
		PerRequestPrice:     p.PerRequestPrice,
		VideoPricePerSecond: p.VideoPricePerSecond,
		VideoDefaultSeconds: p.VideoDefaultSeconds,
		VideoAllowedSeconds: p.VideoAllowedSeconds,
		Intervals:           intervals,
	}
	if scope == pricingScopePrimary {
		description := p.Description
		resp.Description = &description
	}
	return resp
```

In `pricingRequestToService`, set the service field only for primary pricing:

```go
	description := ""
	if scope == pricingScopePrimary {
		description = strings.TrimSpace(r.Description)
	}

	result = append(result, service.ChannelModelPricing{
		Platform:            platform,
		Models:              r.Models,
		Description:         description,
		BillingMode:         billingMode,
		InputPrice:          r.InputPrice,
		OutputPrice:         r.OutputPrice,
		CacheWritePrice:     r.CacheWritePrice,
		CacheReadPrice:      r.CacheReadPrice,
		ImageInputPrice:     r.ImageInputPrice,
		ImageOutputPrice:    r.ImageOutputPrice,
		PerRequestPrice:     r.PerRequestPrice,
		VideoPricePerSecond: r.VideoPricePerSecond,
		VideoDefaultSeconds: r.VideoDefaultSeconds,
		VideoAllowedSeconds: r.VideoAllowedSeconds,
		Intervals:           intervals,
	})
```

- [ ] **Step 4: Update every converter call site with the correct scope**

Use `pricingScopePrimary` for:

```go
pricingToResponse(&p, pricingScopePrimary)
pricingRequestToService(req.ModelPricing, pricingScopePrimary)
pricingRequestToService(*req.ModelPricing, pricingScopePrimary)
```

Use `pricingScopeAccountStats` for:

```go
pricingToResponse(&rule.Pricing[i], pricingScopeAccountStats)
pricingRequestToService(r.Pricing, pricingScopeAccountStats)
```

Update existing tests in `channel_handler_test.go` and `channel_video_pricing_test.go` to pass `pricingScopePrimary` unless the test explicitly exercises account-statistics behavior.

- [ ] **Step 5: Run admin handler tests**

Run:

```bash
cd /root/sub2api/backend
gofmt -w internal/handler/admin/channel_handler.go internal/handler/admin/channel_handler_test.go internal/handler/admin/channel_video_pricing_test.go
GOTOOLCHAIN=go1.26.5 go test -tags=unit ./internal/handler/admin -count=1
```

Expected: PASS, with primary empty descriptions serialized as `"description":""` and nested account-stat descriptions omitted.

- [ ] **Step 6: Commit the scoped admin API**

Run:

```bash
cd /root/sub2api
git add -- \
  backend/internal/handler/admin/channel_handler.go \
  backend/internal/handler/admin/channel_handler_test.go \
  backend/internal/handler/admin/channel_video_pricing_test.go
git diff --cached --check
git commit -m "feat(pricing): expose scoped model descriptions"
```

## Task 8: Expose Descriptions to Users and Preserve LiteLLM Fallback

**Files:**
- Modify: `backend/internal/handler/available_channel_handler.go`
- Modify: `backend/internal/handler/available_channel_handler_test.go`
- Modify: `backend/internal/service/channel_available.go`
- Modify: `backend/internal/service/channel_available_test.go`
- Modify: `backend/internal/service/channel_test.go`

- [ ] **Step 1: Write the failing user DTO test**

Add to `available_channel_handler_test.go`:

```go
func TestToUserPricingExposesDescription(t *testing.T) {
	pricing := toUserPricing(&service.ChannelModelPricing{
		Description: "First line\nSecond line",
		BillingMode: service.BillingModeToken,
	})
	require.NotNil(t, pricing)
	require.Equal(t, "First line\nSecond line", pricing.Description)

	raw, err := json.Marshal(pricing)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, "First line\nSecond line", decoded["description"])
}
```

- [ ] **Step 2: Write failing fallback eligibility and preservation tests**

Add `description only` to `TestPricingNeedsFallback`:

```go
{"description only", &ChannelModelPricing{Description: "Display copy", BillingMode: BillingModeToken}, true},
```

Add:

```go
func TestSynthesizePricingFromLiteLLM_PreservesDescription(t *testing.T) {
	tests := []struct {
		name string
		mode BillingMode
		liteLLMMode string
	}{
		{name: "token", mode: BillingModeToken, liteLLMMode: "chat"},
		{name: "image", mode: BillingModeImage, liteLLMMode: "image_generation"},
		{name: "per request", mode: BillingModePerRequest, liteLLMMode: "chat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existing := &ChannelModelPricing{
				BillingMode: tt.mode,
				Description: "Authored\nDescription",
			}
			got := synthesizePricingFromLiteLLM(&LiteLLMModelPricing{
				Mode:                    tt.liteLLMMode,
				InputCostPerToken:       0.000001,
				OutputCostPerImage:      0.04,
				OutputCostPerImageToken: 0.000002,
			}, existing)
			require.NotNil(t, got)
			require.Equal(t, "Authored\nDescription", got.Description)
		})
	}
}
```

Add to `channel_test.go`:

```go
func TestSupportedModels_SharedDescriptionForEveryBoundModel(t *testing.T) {
	channel := &Channel{ModelPricing: []ChannelModelPricing{{
		Platform:    PlatformOpenAI,
		Models:      []string{"gpt-primary", "gpt-alias"},
		Description: "Shared\nDescription",
		BillingMode: BillingModeToken,
	}}}

	models := channel.SupportedModels()
	require.Len(t, models, 2)
	require.Equal(t, []string{"gpt-alias", "gpt-primary"}, []string{models[0].Name, models[1].Name})
	require.Equal(t, "Shared\nDescription", models[0].Pricing.Description)
	require.Equal(t, "Shared\nDescription", models[1].Pricing.Description)
}
```

- [ ] **Step 3: Run focused tests to verify fields are not copied**

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go test -tags=unit ./internal/handler ./internal/service \
  -run 'Test(ToUserPricingExposesDescription|PricingNeedsFallback|SynthesizePricingFromLiteLLM_PreservesDescription|SupportedModels_SharedDescriptionForEveryBoundModel)$' \
  -count=1
```

Expected: FAIL because the user DTO and synthesized pricing omit the description.

- [ ] **Step 4: Implement the user DTO and fallback copy**

Add to `userSupportedModelPricing`:

```go
Description string `json:"description"`
```

Set it in `toUserPricing`:

```go
Description: p.Description,
```

At the start of `synthesizePricingFromLiteLLM`, after the nil LiteLLM guard, derive:

```go
description := ""
if existing != nil {
	description = existing.Description
}
```

Set `Description: description` in both newly synthesized `ChannelModelPricing` literals. Do not add `Description` to `pricingNeedsFallback`; description-only rows must remain eligible for display-price fallback.

- [ ] **Step 5: Run handler/service tests and commit**

Run:

```bash
cd /root/sub2api/backend
gofmt -w internal/handler/available_channel_handler.go internal/handler/available_channel_handler_test.go internal/service/channel_available.go internal/service/channel_available_test.go internal/service/channel_test.go
GOTOOLCHAIN=go1.26.5 go test -tags=unit ./internal/handler ./internal/service \
  -run 'Test(ToUserPricingExposesDescription|PricingNeedsFallback|SynthesizePricingFromLiteLLM|FillGlobalPricingFallback)' \
  -count=1
cd /root/sub2api
git add -- \
  backend/internal/handler/available_channel_handler.go \
  backend/internal/handler/available_channel_handler_test.go \
  backend/internal/service/channel_available.go \
  backend/internal/service/channel_available_test.go \
  backend/internal/service/channel_test.go
git diff --cached --check
git commit -m "feat(pricing): publish model descriptions"
```

Expected: PASS and one focused backend API/fallback commit.

## Task 9: Add Frontend DTOs, Form State, and Scoped Serialization

**Files:**
- Modify: `frontend/src/api/admin/channels.ts`
- Modify: `frontend/src/api/channels.ts`
- Modify: `frontend/src/components/admin/channel/types.ts`
- Modify: `frontend/src/components/admin/channel/__tests__/types.spec.ts`
- Modify: `frontend/src/views/admin/ChannelsView.vue`
- Create: `frontend/src/views/admin/__tests__/ChannelsView.modelPricingDescription.spec.ts`
- Modify: `frontend/src/views/admin/__tests__/ChannelsView.videoPricing.spec.ts`

- [ ] **Step 1: Write failing serializer tests**

Add `formAccountStatsPricingToAPI` to the imports in `types.spec.ts`, add `description: ''` to `makeVideoPricing`, and add:

```ts
describe('model pricing descriptions', () => {
  it('trims surrounding whitespace while preserving internal line breaks', () => {
    const entry = makeVideoPricing({
      description: '  First line\nSecond line  ',
    })

    expect(formPricingToAPI(entry, 'openai').description).toBe('First line\nSecond line')
  })

  it('omits injected descriptions from account-statistics serialization', () => {
    const entry = makeVideoPricing({ description: 'must not leave the form' })
    const serialized = formAccountStatsPricingToAPI(entry, 'openai')

    expect(serialized).not.toHaveProperty('description')
  })
})
```

- [ ] **Step 2: Run the serializer tests to verify the new contract is absent**

Run:

```bash
cd /root/sub2api
pnpm --dir frontend exec vitest run src/components/admin/channel/__tests__/types.spec.ts
```

Expected: FAIL because the field and account-stat serializer do not exist.

- [ ] **Step 3: Split primary and account-stat API types**

In `frontend/src/api/admin/channels.ts`, add `description` to the primary type and define the account-stat shape:

```ts
export interface ChannelModelPricing {
  id?: number
  platform: string
  models: string[]
  description: string
  billing_mode: BillingMode
  input_price: number | null
  output_price: number | null
  cache_write_price: number | null
  cache_read_price: number | null
  image_input_price: number | null
  image_output_price: number | null
  per_request_price: number | null
  video_price_per_second: number | null
  video_default_seconds: number | null
  video_allowed_seconds: number[]
  intervals: PricingInterval[]
}

export type AccountStatsModelPricing = Omit<ChannelModelPricing, 'description'>

export interface AccountStatsPricingRule {
  id?: number
  name: string
  group_ids: number[]
  account_ids: number[]
  pricing: AccountStatsModelPricing[]
}
```

In `frontend/src/api/channels.ts`, add:

```ts
description: string
```

to `UserSupportedModelPricing`.

- [ ] **Step 4: Implement one shared field serializer with scoped public functions**

Import `AccountStatsModelPricing`, add `description: string` to `PricingFormEntry`, and replace `formPricingToAPI` with this structure while retaining its existing mode-isolation body:

```ts
function formPricingFieldsToAPI(
  entry: PricingFormEntry,
  platform: string,
): AccountStatsModelPricing {
  const tokenMode = entry.billing_mode === 'token'
  const requestMode = entry.billing_mode === 'image' || entry.billing_mode === 'per_request'
  const videoMode = entry.billing_mode === 'video'
  const video = videoMode
    ? formVideoPricingToAPI(entry)
    : { video_price_per_second: null, video_default_seconds: null, video_allowed_seconds: [] }

  return {
    platform,
    models: [...entry.models],
    billing_mode: entry.billing_mode,
    input_price: tokenMode ? mTokToPerToken(entry.input_price) : null,
    output_price: tokenMode ? mTokToPerToken(entry.output_price) : null,
    cache_write_price: tokenMode ? mTokToPerToken(entry.cache_write_price) : null,
    cache_read_price: tokenMode ? mTokToPerToken(entry.cache_read_price) : null,
    image_input_price: tokenMode ? mTokToPerToken(entry.image_input_price) : null,
    image_output_price: tokenMode ? mTokToPerToken(entry.image_output_price) : null,
    per_request_price: requestMode ? toNullableNumber(entry.per_request_price) : null,
    ...video,
    intervals: formIntervalsForMode(entry.intervals || [], entry.billing_mode),
  }
}

export function formPricingToAPI(entry: PricingFormEntry, platform: string): ChannelModelPricing {
  return {
    ...formPricingFieldsToAPI(entry, platform),
    description: entry.description.trim(),
  }
}

export function formAccountStatsPricingToAPI(
  entry: PricingFormEntry,
  platform: string,
): AccountStatsModelPricing {
  return formPricingFieldsToAPI(entry, platform)
}
```

- [ ] **Step 5: Update ChannelsView defaults, hydration, and serializers**

Import `formAccountStatsPricingToAPI`. Add `description: ''` to `createPricingEntry()`.

Primary hydration must use:

```ts
description: p.description || '',
```

Account-stat hydration must explicitly use:

```ts
description: '',
```

Account-stat request serialization must use:

```ts
pricing: rule.pricing
  .filter(p => p.models.length > 0)
  .map(p => formAccountStatsPricingToAPI(p, section.platform))
```

Primary serialization remains `formPricingToAPI(entry, section.platform)`.

- [ ] **Step 6: Add the ChannelsView boundary test**

Create `frontend/src/views/admin/__tests__/ChannelsView.modelPricingDescription.spec.ts` with the complete content in the following block.

```ts
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, nextTick } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AccountStatsModelPricing, Channel, ChannelModelPricing } from '@/api/admin/channels'
import ChannelsView from '../ChannelsView.vue'

const { getAllGroups, listChannels, updateChannel, showError } = vi.hoisted(() => ({
  getAllGroups: vi.fn(),
  listChannels: vi.fn(),
  updateChannel: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channels: {
      list: listChannels,
      update: updateChannel,
      create: vi.fn(),
      delete: vi.fn(),
      syncPricingModels: vi.fn(),
    },
    groups: { getAll: getAllGroups },
    accounts: { list: vi.fn(), getById: vi.fn() },
    settings: { getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false }) },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess: vi.fn() }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const PricingEntryCardStub = defineComponent({
  name: 'PricingEntryCard',
  props: {
    entry: { type: Object, required: true },
    platform: { type: String, default: '' },
    inputIdPrefix: { type: String, required: true },
    showDescription: { type: Boolean, required: true },
  },
  template: '<div data-test="pricing-entry-card" />',
})

function makePrimaryPricing(description = ''): ChannelModelPricing {
  return {
    platform: 'openai',
    models: ['gpt-test'],
    description,
    billing_mode: 'token',
    input_price: 0.000001,
    output_price: 0.000002,
    cache_write_price: null,
    cache_read_price: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    video_price_per_second: null,
    video_default_seconds: null,
    video_allowed_seconds: [],
    intervals: [],
  }
}

function makeAccountStatsPricing(): AccountStatsModelPricing {
  return {
    platform: 'openai',
    models: ['gpt-stats'],
    billing_mode: 'token',
    input_price: 0.000001,
    output_price: 0.000002,
    cache_write_price: null,
    cache_read_price: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    video_price_per_second: null,
    video_default_seconds: null,
    video_allowed_seconds: [],
    intervals: [],
  }
}

function makeChannel(): Channel {
  return {
    id: 7,
    name: 'Described channel',
    description: '',
    status: 'active',
    billing_model_source: 'channel_mapped',
    restrict_models: false,
    group_ids: [3],
    model_pricing: [makePrimaryPricing('Stored\nDescription')],
    model_mapping: {},
    apply_pricing_to_account_stats: true,
    account_stats_pricing_rules: [{
      name: 'Stats',
      group_ids: [3],
      account_ids: [],
      pricing: [makeAccountStatsPricing()],
    }],
    created_at: '2026-07-01T00:00:00Z',
    updated_at: '2026-07-01T00:00:00Z',
  }
}

function mountChannelsView() {
  return mount(ChannelsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: true,
        DataTable: true,
        Pagination: true,
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        Toggle: true,
        PricingEntryCard: PricingEntryCardStub,
      },
    },
  })
}

describe('ChannelsView model pricing descriptions', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getAllGroups.mockResolvedValue([{ id: 3, name: 'OpenAI', platform: 'openai' }])
    listChannels.mockResolvedValue({ items: [], total: 0 })
    updateChannel.mockResolvedValue(makeChannel())
  })

  it('hydrates and submits descriptions only for primary pricing', async () => {
    const channel = makeChannel()
    channel.account_stats_pricing_rules[0].pricing[0] = {
      ...channel.account_stats_pricing_rules[0].pricing[0],
      description: 'injected account-stat description',
    } as unknown as AccountStatsModelPricing

    const wrapper = mountChannelsView()
    await flushPromises()
    const vm = wrapper.vm as unknown as {
      openEditDialog: (value: Channel) => Promise<void>
      handleSubmit: () => Promise<void>
      form: {
        platforms: Array<{
          model_pricing: Array<{ description: string }>
          account_stats_pricing_rules: Array<{ pricing: Array<{ description: string }> }>
        }>
      }
    }

    await vm.openEditDialog(channel)
    await nextTick()
    expect(vm.form.platforms[0].model_pricing[0].description).toBe('Stored\nDescription')
    expect(vm.form.platforms[0].account_stats_pricing_rules[0].pricing[0].description).toBe('')
    expect(wrapper.findAllComponents(PricingEntryCardStub).map(card => card.props('showDescription'))).toEqual([true, false])

    vm.form.platforms[0].model_pricing[0].description = '  Submitted\nDescription  '
    await vm.handleSubmit()

    const request = updateChannel.mock.calls[0][1] as {
      model_pricing: ChannelModelPricing[]
      account_stats_pricing_rules: Array<{ pricing: AccountStatsModelPricing[] }>
    }
    expect(request.model_pricing[0].description).toBe('Submitted\nDescription')
    expect(request.account_stats_pricing_rules[0].pricing[0]).not.toHaveProperty('description')
    expect(showError).not.toHaveBeenCalled()
  })

  it('hydrates a missing runtime primary description as empty', async () => {
    const channel = makeChannel()
    delete (channel.model_pricing[0] as Partial<ChannelModelPricing>).description
    const wrapper = mountChannelsView()
    await flushPromises()
    const vm = wrapper.vm as unknown as {
      openEditDialog: (value: Channel) => Promise<void>
      form: { platforms: Array<{ model_pricing: Array<{ description: string }> }> }
    }

    await vm.openEditDialog(channel)
    expect(vm.form.platforms[0].model_pricing[0].description).toBe('')
  })
})
```

In `ChannelsView.videoPricing.spec.ts`, import `AccountStatsModelPricing` and `ChannelModelPricing`, type `videoPricing` as `ChannelModelPricing`, and add `description: ''` plus `image_input_price: null`. Add this converter and use its result for the account-stat fixture:

```ts
function withoutDescription(pricing: ChannelModelPricing): AccountStatsModelPricing {
  const { description, ...accountStatsPricing } = pricing
  void description
  return accountStatsPricing
}

pricing: [{ ...withoutDescription(videoPricing), models: ['sora-rule'] }],
```

- [ ] **Step 7: Run focused form/view tests and typecheck**

Run:

```bash
cd /root/sub2api
pnpm --dir frontend exec vitest run \
  src/components/admin/channel/__tests__/types.spec.ts \
  src/views/admin/__tests__/ChannelsView.modelPricingDescription.spec.ts \
  src/views/admin/__tests__/ChannelsView.videoPricing.spec.ts
pnpm --dir frontend run typecheck
```

Expected: PASS.

- [ ] **Step 8: Commit form and DTO behavior**

Run:

```bash
cd /root/sub2api
git add -- \
  frontend/src/api/admin/channels.ts \
  frontend/src/api/channels.ts \
  frontend/src/components/admin/channel/types.ts \
  frontend/src/components/admin/channel/__tests__/types.spec.ts \
  frontend/src/views/admin/ChannelsView.vue \
  frontend/src/views/admin/__tests__/ChannelsView.modelPricingDescription.spec.ts \
  frontend/src/views/admin/__tests__/ChannelsView.videoPricing.spec.ts
git diff --cached --check
git commit -m "feat(pricing): serialize model descriptions"
```

## Task 10: Add the Primary-Only Description Editor

**Files:**
- Modify: `frontend/src/components/admin/channel/PricingEntryCard.vue`
- Create: `frontend/src/components/admin/channel/__tests__/PricingEntryCard.description.spec.ts`
- Modify: `frontend/src/components/admin/channel/__tests__/PricingEntryCard.video.spec.ts`
- Modify: `frontend/src/views/admin/ChannelsView.vue`
- Modify: `frontend/src/i18n/locales/en/admin/channels.ts`
- Modify: `frontend/src/i18n/locales/zh/admin/channels.ts`
- Create: `frontend/src/i18n/__tests__/modelPricingDescriptionLocales.spec.ts`

- [ ] **Step 1: Write the failing editor tests**

Create `PricingEntryCard.description.spec.ts`:

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import PricingEntryCard from '../PricingEntryCard.vue'
import type { PricingFormEntry } from '../types'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

function makeEntry(overrides: Partial<PricingFormEntry> = {}): PricingFormEntry {
  return {
    models: ['gpt-test'],
    description: 'First line',
    billing_mode: 'token',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    video_price_per_second: null,
    video_default_seconds: null,
    video_allowed_seconds: [],
    intervals: [],
    ...overrides,
  }
}

function mountCard(options: { entry?: PricingFormEntry, showDescription: boolean }) {
  return mount(PricingEntryCard, {
    props: {
      entry: options.entry ?? makeEntry(),
      platform: 'openai',
      inputIdPrefix: 'description-test',
      showDescription: options.showDescription,
    },
    global: {
      stubs: {
        Icon: true,
        ModelTagInput: true,
        Select: true,
        IntervalRow: true,
      },
    },
  })
}

describe('PricingEntryCard model description', () => {
  it('renders the primary description editor with maxlength and live count', () => {
    const wrapper = mountCard({ showDescription: true })
    const textarea = wrapper.get('[data-test="pricing-description"]')

    expect(textarea.attributes('maxlength')).toBe('500')
    expect(textarea.attributes('rows')).toBe('3')
    expect(wrapper.get('[data-test="pricing-description-count"]').text()).toBe('10/500')
  })

  it('emits an immutable description update', async () => {
    const entry = makeEntry({ description: 'Original' })
    const wrapper = mountCard({ entry, showDescription: true })

    await wrapper.get('[data-test="pricing-description"]').setValue('Updated\nCopy')

    expect(entry.description).toBe('Original')
    expect(wrapper.emitted('update')?.[0]?.[0]).toMatchObject({ description: 'Updated\nCopy' })
  })

  it('does not render description controls for account-statistics pricing', () => {
    const wrapper = mountCard({ showDescription: false })
    expect(wrapper.find('[data-test="pricing-description"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="pricing-description-count"]').exists()).toBe(false)
  })
})
```

- [ ] **Step 2: Run the editor test to verify the prop/control is absent**

Run:

```bash
cd /root/sub2api
pnpm --dir frontend exec vitest run src/components/admin/channel/__tests__/PricingEntryCard.description.spec.ts
```

Expected: FAIL.

- [ ] **Step 3: Add the required prop and editor UI**

Add `showDescription: boolean` to `defineProps`. Add:

```ts
const descriptionId = computed(() => `${props.inputIdPrefix}-description`)
const descriptionLength = computed(() => Array.from(props.entry.description).length)
```

Immediately below the models/billing-mode row and above mode-specific prices, add:

```vue
<div v-if="props.showDescription" class="mt-3">
  <div class="flex items-center justify-between gap-2">
    <label :for="descriptionId" class="text-xs font-medium text-gray-500 dark:text-gray-400">
      {{ t('admin.channels.form.pricingDescription') }}
    </label>
    <span data-test="pricing-description-count" class="text-xs text-gray-400">
      {{ descriptionLength }}/500
    </span>
  </div>
  <textarea
    :id="descriptionId"
    data-test="pricing-description"
    :value="entry.description"
    rows="3"
    maxlength="500"
    class="input mt-1 resize-y text-sm"
    :placeholder="t('admin.channels.form.pricingDescriptionPlaceholder')"
    @input="emit('update', { ...entry, description: ($event.target as HTMLTextAreaElement).value })"
  />
</div>
```

Do not alter the collapsed summary.

- [ ] **Step 4: Pass the prop at every production and test call site**

Primary card:

```vue
<PricingEntryCard
  :show-description="true"
  ...
/>
```

Account-stat card:

```vue
<PricingEntryCard
  :show-description="false"
  ...
/>
```

Update the video component test harness to pass `:show-description="true"` and add `description: ''` to its fixture.

- [ ] **Step 5: Add localized editor copy and locale tests**

Add under `admin.channels.form`:

```ts
// English
pricingDescription: 'Model description',
pricingDescriptionPlaceholder: 'Optional model description',

// Chinese
pricingDescription: '模型说明',
pricingDescriptionPlaceholder: '可选的模型说明',
```

Create `modelPricingDescriptionLocales.spec.ts`:

```ts
import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en/admin/channels'
import zh from '@/i18n/locales/zh/admin/channels'

describe('model pricing description locales', () => {
  it('defines editor copy in English and Chinese', () => {
    expect(en.channels.form.pricingDescription).toBe('Model description')
    expect(en.channels.form.pricingDescriptionPlaceholder).toBe('Optional model description')
    expect(zh.channels.form.pricingDescription).toBe('模型说明')
    expect(zh.channels.form.pricingDescriptionPlaceholder).toBe('可选的模型说明')
  })
})
```

- [ ] **Step 6: Run editor, locale, view, lint, and type tests**

Run:

```bash
cd /root/sub2api
pnpm --dir frontend exec vitest run \
  src/components/admin/channel/__tests__/PricingEntryCard.description.spec.ts \
  src/components/admin/channel/__tests__/PricingEntryCard.video.spec.ts \
  src/views/admin/__tests__/ChannelsView.modelPricingDescription.spec.ts \
  src/i18n/__tests__/modelPricingDescriptionLocales.spec.ts
pnpm --dir frontend run typecheck
pnpm --dir frontend run lint:check
```

Expected: PASS.

- [ ] **Step 7: Commit the editor**

Run:

```bash
cd /root/sub2api
git add -- \
  frontend/src/components/admin/channel/PricingEntryCard.vue \
  frontend/src/components/admin/channel/__tests__/PricingEntryCard.description.spec.ts \
  frontend/src/components/admin/channel/__tests__/PricingEntryCard.video.spec.ts \
  frontend/src/views/admin/ChannelsView.vue \
  frontend/src/i18n/locales/en/admin/channels.ts \
  frontend/src/i18n/locales/zh/admin/channels.ts \
  frontend/src/i18n/__tests__/modelPricingDescriptionLocales.spec.ts
git diff --cached --check
git commit -m "feat(pricing): edit primary model descriptions"
```

## Task 11: Render Descriptions in Available Channels Without Searching Them

**Files:**
- Modify: `frontend/src/components/channels/SupportedModelChip.vue`
- Modify: `frontend/src/components/channels/__tests__/SupportedModelChip.spec.ts`
- Create: `frontend/src/views/user/__tests__/AvailableChannelsView.modelPricingDescription.spec.ts`
- Verify unchanged search logic: `frontend/src/views/user/AvailableChannelsView.vue:85-103`

- [ ] **Step 1: Write failing popover tests**

Add `description: ''` and the currently required `image_input_price` field to `makeModel()`'s pricing fixture. Add:

```ts
it('renders escaped multiline descriptions before billing details', async () => {
  const wrapper = mountModel(makeModel({
    pricing: {
      ...makeModel().pricing!,
      description: 'First line\n<script data-injected>bad()</script>',
    },
  }))
  await openOnMouseenter(wrapper)

  const description = tooltip().querySelector<HTMLElement>('[data-test="model-pricing-description"]')
  const billingMode = tooltip().querySelector<HTMLElement>('[data-test="model-pricing-billing-mode"]')
  expect(description?.textContent).toBe('First line\n<script data-injected>bad()</script>')
  expect(tooltip().querySelector('script[data-injected]')).toBeNull()
  expect(description?.classList.contains('whitespace-pre-wrap')).toBe(true)
  expect(description?.classList.contains('break-words')).toBe(true)
  expect(description?.compareDocumentPosition(billingMode!)).toBe(Node.DOCUMENT_POSITION_FOLLOWING)
})

it('renders no description node for an empty description', async () => {
  const wrapper = mountModel(makeModel())
  await openOnMouseenter(wrapper)
  expect(tooltip().querySelector('[data-test="model-pricing-description"]')).toBeNull()
})
```

Add `data-test="model-pricing-billing-mode"` to the existing billing-mode row so ordering is asserted without depending on translated text.

- [ ] **Step 2: Run the popover tests to verify no description node exists**

Run:

```bash
cd /root/sub2api
pnpm --dir frontend exec vitest run src/components/channels/__tests__/SupportedModelChip.spec.ts
```

Expected: FAIL on the new description assertions.

- [ ] **Step 3: Render escaped multiline text above billing details**

As the first child of the priced detail block, add:

```vue
<p
  v-if="model.pricing.description"
  data-test="model-pricing-description"
  class="whitespace-pre-wrap break-words text-gray-500 dark:text-gray-400"
>{{ model.pricing.description }}</p>
```

Vue interpolation provides escaping. Keep the existing Teleport, platform colors, hover/focus behavior, width, header, and pricing rows unchanged.

- [ ] **Step 4: Write the failing search-exclusion test**

Create `AvailableChannelsView.modelPricingDescription.spec.ts`:

```ts
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { UserAvailableChannel } from '@/api/channels'
import AvailableChannelsView from '../AvailableChannelsView.vue'

const { getAvailable, getUserGroupRates } = vi.hoisted(() => ({
  getAvailable: vi.fn(),
  getUserGroupRates: vi.fn(),
}))

vi.mock('@/api/channels', () => ({
  default: { getAvailable },
}))

vi.mock('@/api/groups', () => ({
  default: { getUserGroupRates },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn() }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const AvailableChannelsTableStub = defineComponent({
  name: 'AvailableChannelsTable',
  props: {
    rows: { type: Array, required: true },
  },
  template: '<div data-test="available-channels-table" />',
})

function makeChannels(): UserAvailableChannel[] {
  return [{
    name: 'Channel',
    description: '',
    platforms: [{
      platform: 'openai',
      groups: [{
        id: 3,
        name: 'OpenAI group',
        platform: 'openai',
        subscription_type: 'standard',
        rate_multiplier: 1,
        peak_rate_enabled: false,
        peak_start: '',
        peak_end: '',
        peak_rate_multiplier: 1,
        is_exclusive: false,
      }],
      supported_models: [{
        name: 'visible-model',
        platform: 'openai',
        pricing: {
          description: 'internal-only-description',
          billing_mode: 'token',
          input_price: null,
          output_price: null,
          cache_write_price: null,
          cache_read_price: null,
          image_input_price: null,
          image_output_price: null,
          per_request_price: null,
          video_price_per_second: null,
          video_default_seconds: null,
          video_allowed_seconds: [],
          intervals: [],
        },
      }],
    }],
  }]
}

function mountAvailableChannelsView() {
  return mount(AvailableChannelsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /></div>',
        },
        Icon: true,
        AvailableChannelsTable: AvailableChannelsTableStub,
      },
    },
  })
}

describe('AvailableChannelsView model pricing description search', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getAvailable.mockResolvedValue(makeChannels())
    getUserGroupRates.mockResolvedValue({})
  })

  it('does not include model pricing descriptions in search', async () => {
    const wrapper = mountAvailableChannelsView()
    await flushPromises()
    const table = wrapper.getComponent(AvailableChannelsTableStub)

    await wrapper.get('input[type="text"]').setValue('internal-only-description')
    expect(table.props('rows')).toEqual([])

    await wrapper.get('input[type="text"]').setValue('visible-model')
    expect(table.props('rows')).toHaveLength(1)
  })
})
```

- [ ] **Step 5: Run display and search tests**

Run:

```bash
cd /root/sub2api
pnpm --dir frontend exec vitest run \
  src/components/channels/__tests__/SupportedModelChip.spec.ts \
  src/views/user/__tests__/AvailableChannelsView.modelPricingDescription.spec.ts
pnpm --dir frontend run typecheck
```

Expected: PASS. No production search-code change is needed because the existing filter only reads channel name/description, platform, group name, and model name.

- [ ] **Step 6: Commit Available Channels display behavior**

Run:

```bash
cd /root/sub2api
git add -- \
  frontend/src/components/channels/SupportedModelChip.vue \
  frontend/src/components/channels/__tests__/SupportedModelChip.spec.ts \
  frontend/src/views/user/__tests__/AvailableChannelsView.modelPricingDescription.spec.ts
git diff --cached --check
git commit -m "feat(channels): show model pricing descriptions"
```

## Task 12: Add the State Snapshot Query and Run Complete Source Gates

**Files:**
- Create: `deploy/release/v0162_state_snapshot.sql`
- Verify all tracked source and generated files.
- Build output: `backend/internal/web/dist/` from the frontend build.

- [ ] **Step 1: Create the stable non-secret production state query**

Create `deploy/release/v0162_state_snapshot.sql`:

```sql
SELECT 'setting', key, value
FROM settings
WHERE key = 'allow_user_view_error_requests'
ORDER BY key;

SELECT 'channel', id, name, description, status, billing_model_source,
       restrict_models, features, features_config::text,
       apply_pricing_to_account_stats, model_mapping::text
FROM channels
ORDER BY id;

SELECT 'pricing', channel_id, platform, models::text, billing_mode,
       input_price, output_price, cache_write_price, cache_read_price,
       image_input_price, image_output_price, per_request_price,
       video_price_per_second, video_default_seconds, video_allowed_seconds::text
FROM channel_model_pricing
ORDER BY channel_id, platform, models::text, billing_mode;

SELECT 'interval', p.channel_id, p.platform, p.models::text, i.tier_label,
       i.min_tokens, i.max_tokens, i.input_price, i.output_price,
       i.cache_write_price, i.cache_read_price, i.per_request_price,
       i.video_price_per_second, i.sort_order
FROM channel_pricing_intervals i
JOIN channel_model_pricing p ON p.id = i.pricing_id
ORDER BY p.channel_id, p.platform, p.models::text, i.sort_order, i.id;

SELECT 'stats_rule', channel_id, name, group_ids::text, account_ids::text, sort_order
FROM channel_account_stats_pricing_rules
ORDER BY channel_id, sort_order, name;

SELECT 'stats_pricing', r.channel_id, r.name, p.platform, p.models::text,
       p.billing_mode, p.input_price, p.output_price, p.cache_write_price,
       p.cache_read_price, p.image_input_price, p.image_output_price,
       p.per_request_price, p.video_price_per_second,
       p.video_default_seconds, p.video_allowed_seconds::text
FROM channel_account_stats_model_pricing p
JOIN channel_account_stats_pricing_rules r ON r.id = p.rule_id
ORDER BY r.channel_id, r.sort_order, r.name, p.platform, p.models::text, p.billing_mode;

SELECT 'stats_interval', r.channel_id, r.name, p.platform, p.models::text,
       i.tier_label, i.min_tokens, i.max_tokens, i.input_price, i.output_price,
       i.cache_write_price, i.cache_read_price, i.per_request_price,
       i.video_price_per_second, i.sort_order
FROM channel_account_stats_pricing_intervals i
JOIN channel_account_stats_model_pricing p ON p.id = i.pricing_id
JOIN channel_account_stats_pricing_rules r ON r.id = p.rule_id
ORDER BY r.channel_id, r.sort_order, r.name, p.platform, p.models::text, i.sort_order, i.id;

SELECT 'proxy', id, name, protocol, host, port, status, expires_at,
       fallback_mode, backup_proxy_id
FROM proxies
ORDER BY id;

SELECT 'account', id, name, platform, type, status, schedulable,
       rate_multiplier, proxy_id, expires_at,
       credentials->>'pricing_managed_by',
       credentials->>'pricing_markup_factor',
       (deleted_at IS NOT NULL)
FROM accounts
ORDER BY id;

SELECT 'group', id, name, platform, status, rate_multiplier,
       allow_image_generation, allow_video_generation,
       image_rate_independent, image_rate_multiplier,
       video_rate_independent, video_rate_multiplier,
       (deleted_at IS NOT NULL)
FROM groups
ORDER BY id;

SELECT 'plan', id, account_id, model_id, cron_expression, enabled,
       max_results, auto_recover
FROM scheduled_test_plans
ORDER BY id;

SELECT 'account_group', account_id, group_id, priority
FROM account_groups
ORDER BY account_id, group_id, priority;

SELECT 'channel_group', channel_id, group_id
FROM channel_groups
ORDER BY channel_id, group_id;
```

- [ ] **Step 2: Verify and commit the state-query boundary**

Run:

```bash
cd /root/sub2api
grep -F "credentials->>'pricing_managed_by'" deploy/release/v0162_state_snapshot.sql
grep -F "credentials->>'pricing_markup_factor'" deploy/release/v0162_state_snapshot.sql
if grep -Ei 'password|secret|api[_ ]?key|credentials::text|credentials[[:space:]]*$' deploy/release/v0162_state_snapshot.sql; then
  exit 1
fi
git add -- deploy/release/v0162_state_snapshot.sql
git diff --cached --check
git commit -m "ops(release): add production state snapshot query"
```

Expected: the query includes only the two approved pricing marker projections from `credentials` and contains no secrets or API-key material.

- [ ] **Step 3: Establish CI-equivalent toolchains**

Run:

```bash
set -euo pipefail
cd /root/sub2api
GOTOOLCHAIN=go1.26.5 go version | grep -F 'go1.26.5'
source /root/.nvm/nvm.sh
nvm install 20
nvm use 20
corepack enable
corepack prepare pnpm@9 --activate
node --version
pnpm --version
pnpm --dir frontend install --frozen-lockfile
```

Expected: Go 1.26.5, Node 20, pnpm 9, and a frozen-lockfile install.

- [ ] **Step 4: Prove Wire generation is stable**

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go generate ./cmd/server
git diff --exit-code -- cmd/server/wire_gen.go
GOTOOLCHAIN=go1.26.5 go generate ./cmd/server
git diff --exit-code -- cmd/server/wire_gen.go
```

Expected: both diff checks are empty.

- [ ] **Step 5: Run focused customized and official backend regressions**

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go test -tags=unit \
  ./internal/repository ./internal/handler ./internal/handler/admin ./internal/server/routes ./internal/service \
  -run 'Description|PricingMarker|ManagedPricing|ManagedAccountRateAppliesMarkup|SchedulerMetadata|ChannelImageAliasWithoutDetectedImageCount|VideoTask|Settlement|Refund|Reconcile|AuthCacheInvalidation|OpsIngressReject|GrokMediaContent|GrokVideo|ForwardedClientIP|TrustedProx|ImageStorage|SameAccountRetry' \
  -count=1 -timeout=20m
```

Expected: PASS.

- [ ] **Step 6: Run complete backend suites and lint**

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go test ./... -count=1 -timeout=20m
GOTOOLCHAIN=go1.26.5 go test -tags=unit ./... -count=1 -timeout=30m
GOTOOLCHAIN=go1.26.5 go test -tags=integration ./... -count=1 -timeout=45m
GOTOOLCHAIN=go1.26.5 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.9.0 run --timeout=30m ./...
```

Expected: every suite and lint command passes. Integration tests require Docker and provision isolated PostgreSQL/Redis containers.

- [ ] **Step 7: Run complete frontend gates**

Run:

```bash
cd /root/sub2api
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
make test-frontend-critical
pnpm --dir frontend run test:run
pnpm --dir frontend run build
```

Expected: PASS, then fresh assets in `backend/internal/web/dist/`.

- [ ] **Step 8: Verify embedded frontend behavior after the frontend build**

Run:

```bash
cd /root/sub2api/backend
GOTOOLCHAIN=go1.26.5 go test -tags='unit embed' ./internal/web ./internal/server/routes -count=1 -timeout=15m
CGO_ENABLED=0 GOTOOLCHAIN=go1.26.5 go build -tags embed -trimpath -o /tmp/opencode/sub2api-v0162-verification ./cmd/server
/tmp/opencode/sub2api-v0162-verification -version
rm -f /tmp/opencode/sub2api-v0162-verification
```

Expected: embed tests pass and the binary identifies the integrated build.

- [ ] **Step 9: Run diff and secret gates without scanning the untracked dump as content**

Run:

```bash
cd /root/sub2api
git diff --check
docker run --rm -v "$PWD:/repo:ro" -w /repo zricethezav/gitleaks:v8.24.2 \
  git --redact --no-banner --log-opts='e56abf53e58ffe8574e6b40b1afaf58a02bdc219..HEAD'
test "$(git status --porcelain=v1 --untracked-files=all)" = "?? deploy/sub2api.dump"
git log --oneline --decorate -12
```

Expected: no whitespace or secret findings and only the dump remains untracked. Do not commit generated `dist` changes unless the repository already tracks those files after the upstream merge; the release image will be built from a clean archive and rebuild the frontend itself.

## Task 13: Build the Unique Release Image and Manifests

**Files:**
- Create outside Git: `/root/deploy-artifacts/$RELEASE_TAG/`
- Create outside Git: `/root/deploy-artifacts/sub2api-$RELEASE_TAG.tar.gz`
- Create outside Git: `/root/deploy-artifacts/sub2api-v0.1.162-release.env`

- [ ] **Step 1: Define immutable release variables after the final source commit**

Run:

```bash
set -euo pipefail
cd /root/sub2api
umask 077
RELEASE_TS="$(date -u +%Y%m%dT%H%M%SZ)"
RELEASE_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
RELEASE_COMMIT="$(git rev-parse HEAD)"
RELEASE_TAG="v0.1.162-$(git rev-parse --short=12 HEAD)-$RELEASE_TS"
RELEASE_IMAGE="weishaw/sub2api:$RELEASE_TAG"
ARTIFACT_DIR="/root/deploy-artifacts/$RELEASE_TAG"
BACKUP_DIR="/root/sub2api/deploy/backups/$RELEASE_TAG"
BUILD_DIR="/tmp/opencode/sub2api-build-$RELEASE_TAG"
export RELEASE_TS RELEASE_DATE RELEASE_COMMIT RELEASE_TAG RELEASE_IMAGE ARTIFACT_DIR BACKUP_DIR BUILD_DIR
test -d /root
test -d /tmp/opencode
install -d -m 0700 /root/deploy-artifacts
test ! -e "$ARTIFACT_DIR"
test ! -e "$BUILD_DIR"
install -d -m 0700 "$ARTIFACT_DIR" "$BUILD_DIR"
printf 'export RELEASE_TS=%q\nexport RELEASE_DATE=%q\nexport RELEASE_COMMIT=%q\nexport RELEASE_TAG=%q\nexport RELEASE_IMAGE=%q\nexport ARTIFACT_DIR=%q\nexport BACKUP_DIR=%q\n' \
  "$RELEASE_TS" "$RELEASE_DATE" "$RELEASE_COMMIT" "$RELEASE_TAG" "$RELEASE_IMAGE" "$ARTIFACT_DIR" "$BACKUP_DIR" \
  > /root/deploy-artifacts/sub2api-v0.1.162-release.env
```

Expected: a unique tag and root-only artifact/build directories.

- [ ] **Step 2: Export a clean build context**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
cd /root/sub2api
git archive HEAD | tar -xf - -C "$BUILD_DIR"
test ! -e "$BUILD_DIR/deploy/sub2api.dump"
test "$(git -C /root/sub2api rev-parse HEAD)" = "$RELEASE_COMMIT"
```

Expected: the build context contains only committed source.

- [ ] **Step 3: Build and export the frontend stage manifest**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker buildx build --load --platform linux/amd64 --target frontend-builder \
  -t "sub2api-frontend-builder:$RELEASE_TAG" "$BUILD_DIR"
docker create --name "sub2api-frontend-export-$RELEASE_TAG" "sub2api-frontend-builder:$RELEASE_TAG"
docker cp "sub2api-frontend-export-$RELEASE_TAG:/app/backend/internal/web/dist" "$ARTIFACT_DIR/frontend-dist"
docker rm "sub2api-frontend-export-$RELEASE_TAG"
(cd "$ARTIFACT_DIR/frontend-dist" && find . -type f ! -name index.html -print0 | sort -z | xargs -0 sha256sum) > "$ARTIFACT_DIR/frontend.sha256"
test -s "$ARTIFACT_DIR/frontend.sha256"
sha256sum "$ARTIFACT_DIR/frontend.sha256" > "$ARTIFACT_DIR/frontend-manifest.sha256"
```

Expected: a non-empty manifest of immutable frontend assets. `index.html` is excluded because runtime setting injection intentionally changes it.

- [ ] **Step 4: Build the integrated runtime image**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker buildx build --load --platform linux/amd64 \
  --build-arg VERSION=0.1.162 \
  --build-arg COMMIT="$RELEASE_COMMIT" \
  --build-arg DATE="$RELEASE_DATE" \
  --label org.opencontainers.image.version=0.1.162 \
  --label org.opencontainers.image.revision="$RELEASE_COMMIT" \
  --label org.opencontainers.image.created="$RELEASE_DATE" \
  --label org.opencontainers.image.source=https://github.com/Mocha-s/sub2api \
  -t "$RELEASE_IMAGE" "$BUILD_DIR"
```

Expected: one local `linux/amd64` image with the unique tag; the old rollback tag is untouched.

- [ ] **Step 5: Record image, binary, source, migration, and asset identities**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker image inspect "$RELEASE_IMAGE" --format '{{.Id}}' > "$ARTIFACT_DIR/image.id"
docker image inspect "$RELEASE_IMAGE" --format '{{.Id}}|{{.Architecture}}|{{json .Config.Labels}}' > "$ARTIFACT_DIR/image.metadata"
docker run --rm --network none --read-only --tmpfs /app/data:uid=1000,gid=1000,mode=0700 \
  "$RELEASE_IMAGE" -version > "$ARTIFACT_DIR/version.txt" 2>&1
docker run --rm --network none --entrypoint sha256sum "$RELEASE_IMAGE" /app/sub2api > "$ARTIFACT_DIR/binary.sha256"
git -C /root/sub2api show -s --format=fuller "$RELEASE_COMMIT" > "$ARTIFACT_DIR/source.txt"
git -C /root/sub2api diff --name-status refs/tags/v0.1.160.."$RELEASE_COMMIT" > "$ARTIFACT_DIR/paths.txt"
install -m 0600 /root/sub2api/deploy/release/v0162_state_snapshot.sql "$ARTIFACT_DIR/state.sql"
sha256sum "$ARTIFACT_DIR/state.sql" > "$ARTIFACT_DIR/state.sql.sha256"
for migration in \
  backend/migrations/183_ops_ingress_reject_aggregates.sql \
  backend/migrations/184_auth_cache_invalidation_outbox.sql \
  backend/migrations/185_add_channel_model_pricing_description.sql
do
  checksum="$(perl -0777 -pe 's/\A\s+|\s+\z//g' "/root/sub2api/$migration" | sha256sum | cut -d' ' -f1)"
  printf '%s|%s\n' "$(basename "$migration")" "$checksum"
done > "$ARTIFACT_DIR/migrations.runner"
```

Expected: all manifests are non-empty and `version.txt` reports `0.1.162` with the integrated commit.

- [ ] **Step 6: Save and verify the image archive**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker save "$RELEASE_IMAGE" | gzip -9 > "/root/deploy-artifacts/sub2api-$RELEASE_TAG.tar.gz"
gzip -t "/root/deploy-artifacts/sub2api-$RELEASE_TAG.tar.gz"
(cd /root/deploy-artifacts && sha256sum "sub2api-$RELEASE_TAG.tar.gz" > "$RELEASE_TAG/archive.sha256")
chmod -R go-rwx "$ARTIFACT_DIR" "/root/deploy-artifacts/sub2api-$RELEASE_TAG.tar.gz" /root/deploy-artifacts/sub2api-v0.1.162-release.env
```

Expected: a verified root-only image archive and checksum.

## Task 14: Merge and Validate the Production Compose Candidate

**Files:**
- Read remotely: `/root/sub2api/deploy/docker-compose.local.yml`
- Create locally: `/root/deploy-artifacts/$RELEASE_TAG/docker-compose.production.pre.yml`
- Create locally: `/root/deploy-artifacts/$RELEASE_TAG/docker-compose.local.yml`

- [ ] **Step 1: Fetch the current production Compose as the authoritative base**

Run from the local host; authenticate interactively without storing the SSH password:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
ssh root@186.244.215.254 'test -f /root/sub2api/deploy/docker-compose.local.yml'
scp root@186.244.215.254:/root/sub2api/deploy/docker-compose.local.yml "$ARTIFACT_DIR/docker-compose.production.pre.yml"
install -m 0600 "$ARTIFACT_DIR/docker-compose.production.pre.yml" "$ARTIFACT_DIR/docker-compose.local.yml"
sha256sum "$ARTIFACT_DIR/docker-compose.production.pre.yml" > "$ARTIFACT_DIR/compose.pre.sha256"
```

Expected: a fresh snapshot, regardless of whether it differs from `/tmp/opencode/sub2api-prod-compose.yml`.

- [ ] **Step 2: Pin the unique image and apply the official environment additions**

Mechanically replace only the old image value with `$RELEASE_IMAGE`. In the application environment preserve all current logging, CORS, security, image-concurrency, and OpenAI settings, and ensure these lines exist exactly once:

```yaml
      - ENABLE_SERVER_TIMING=${ENABLE_SERVER_TIMING:-false}
      - UPDATE_GITHUB_TOKEN=${UPDATE_GITHUB_TOKEN:-}
      - SETUP_MIGRATION_TIMEOUT_SECONDS=${SETUP_MIGRATION_TIMEOUT_SECONDS:-0}
      - GATEWAY_OPENAI_RESPONSE_HEADER_TIMEOUT=${GATEWAY_OPENAI_RESPONSE_HEADER_TIMEOUT:-0}
      - GATEWAY_OPENAI_HTTP2_ENABLED=${GATEWAY_OPENAI_HTTP2_ENABLED:-true}
      - GATEWAY_OPENAI_HTTP2_ALLOW_PROXY_FALLBACK_TO_HTTP1=${GATEWAY_OPENAI_HTTP2_ALLOW_PROXY_FALLBACK_TO_HTTP1:-true}
      - GATEWAY_OPENAI_HTTP2_FALLBACK_ERROR_THRESHOLD=${GATEWAY_OPENAI_HTTP2_FALLBACK_ERROR_THRESHOLD:-2}
      - GATEWAY_OPENAI_HTTP2_FALLBACK_WINDOW_SECONDS=${GATEWAY_OPENAI_HTTP2_FALLBACK_WINDOW_SECONDS:-60}
      - GATEWAY_OPENAI_HTTP2_FALLBACK_TTL_SECONDS=${GATEWAY_OPENAI_HTTP2_FALLBACK_TTL_SECONDS:-600}
```

Run the image replacement and assert it:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
RELEASE_IMAGE="$RELEASE_IMAGE" perl -0pi -e 's#image:\s+weishaw/sub2api:v0\.1\.160-e56abf53#image: $ENV{RELEASE_IMAGE}#' "$ARTIFACT_DIR/docker-compose.local.yml"
grep -F "image: $RELEASE_IMAGE" "$ARTIFACT_DIR/docker-compose.local.yml"
```

- [ ] **Step 3: Apply official PostgreSQL and Redis command corrections**

The PostgreSQL service must contain this command while retaining current bind paths and log rotation:

```yaml
    command: >
      postgres
      -c max_connections=${POSTGRES_MAX_CONNECTIONS:-100}
      -c shared_buffers=${POSTGRES_SHARED_BUFFERS:-128MB}
      -c effective_cache_size=${POSTGRES_EFFECTIVE_CACHE_SIZE:-4GB}
      -c maintenance_work_mem=${POSTGRES_MAINTENANCE_WORK_MEM:-64MB}
```

The Redis service must contain the corrected quoted script, with a trailing backslash on each continued argument:

```yaml
    command: >
      sh -c '
        redis-server \
        --save 60 1 \
        --appendonly yes \
        --appendfsync everysec \
        ${REDIS_PASSWORD:+--requirepass "$REDIS_PASSWORD"}'
```

These dependency commands remain latent in this rollout because PostgreSQL and Redis will not be recreated.

- [ ] **Step 4: Assert production-specific Compose invariants**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
COMPOSE="$ARTIFACT_DIR/docker-compose.local.yml"
test "$(grep -cF 'max-size: "100m"' "$COMPOSE")" -eq 3
test "$(grep -cF 'max-file: "5"' "$COMPOSE")" -eq 3
test "$(grep -cF 'host.docker.internal:host-gateway' "$COMPOSE")" -eq 1
test "$(grep -cF 'CORS_ALLOWED_ORIGINS=${CORS_ALLOWED_ORIGINS:-*}' "$COMPOSE")" -eq 1
test "$(grep -cF 'CORS_ALLOW_CREDENTIALS=${CORS_ALLOW_CREDENTIALS:-false}' "$COMPOSE")" -eq 1
test "$(grep -cF ':Z' "$COMPOSE" || true)" -eq 0
test "$(grep -cF "image: $RELEASE_IMAGE" "$COMPOSE")" -eq 1
test "$(grep -cF 'image: weishaw/sub2api:v0.1.160-e56abf53' "$COMPOSE" || true)" -eq 0
grep -F -- '- ENABLE_SERVER_TIMING=${ENABLE_SERVER_TIMING:-false}' "$COMPOSE"
grep -F -- '- UPDATE_GITHUB_TOKEN=${UPDATE_GITHUB_TOKEN:-}' "$COMPOSE"
grep -F -- '- SETUP_MIGRATION_TIMEOUT_SECONDS=${SETUP_MIGRATION_TIMEOUT_SECONDS:-0}' "$COMPOSE"
grep -F -- '- GATEWAY_OPENAI_RESPONSE_HEADER_TIMEOUT=${GATEWAY_OPENAI_RESPONSE_HEADER_TIMEOUT:-0}' "$COMPOSE"
grep -F -- '- GATEWAY_OPENAI_HTTP2_ENABLED=${GATEWAY_OPENAI_HTTP2_ENABLED:-true}' "$COMPOSE"
grep -F -- '-c max_connections=${POSTGRES_MAX_CONNECTIONS:-100}' "$COMPOSE"
grep -F -- '-c shared_buffers=${POSTGRES_SHARED_BUFFERS:-128MB}' "$COMPOSE"
grep -F -- 'redis-server \' "$COMPOSE"
grep -F -- '--save 60 1 \' "$COMPOSE"
grep -F -- '--appendonly yes \' "$COMPOSE"
grep -F -- '--appendfsync everysec \' "$COMPOSE"
grep -F -- '- SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP=${SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP:-false}' "$COMPOSE"
grep -F -- '- SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS=${SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS:-false}' "$COMPOSE"
grep -F -- '- LOG_FORMAT=${LOG_FORMAT:-json}' "$COMPOSE"
```

Expected: log rotation, proxy host access, CORS, strict security defaults, logging, and non-SELinux bind paths remain intact.

- [ ] **Step 5: Validate Compose with production environment without rendering secrets**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
scp "$ARTIFACT_DIR/docker-compose.local.yml" root@186.244.215.254:/root/deploy-artifacts/docker-compose-v0162-candidate.yml
ssh root@186.244.215.254 "RELEASE_IMAGE='$RELEASE_IMAGE' docker compose --project-directory /root/sub2api/deploy --env-file /root/sub2api/deploy/.env -f /root/deploy-artifacts/docker-compose-v0162-candidate.yml config --quiet"
ssh root@186.244.215.254 "RELEASE_IMAGE='$RELEASE_IMAGE' docker compose --project-directory /root/sub2api/deploy --env-file /root/sub2api/deploy/.env -f /root/deploy-artifacts/docker-compose-v0162-candidate.yml config --services" > "$ARTIFACT_DIR/compose.services"
test "$(wc -l < "$ARTIFACT_DIR/compose.services")" -eq 3
sha256sum "$ARTIFACT_DIR/docker-compose.local.yml" > "$ARTIFACT_DIR/compose.sha256"
```

Expected: quiet validation succeeds and exactly `sub2api`, `postgres`, and `redis` are listed. Do not run unqualified `docker compose config`, which would print resolved secrets.

## Task 15: Rehearse Migrations and Startup Against a Production Copy

**Files:**
- Temporary local dump: `/tmp/opencode/sub2api-$RELEASE_TAG-rehearsal.dump`
- Temporary local Docker network/containers.

- [ ] **Step 1: Stream a root-only production database copy for rehearsal**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
REHEARSAL_DUMP="/tmp/opencode/sub2api-$RELEASE_TAG-rehearsal.dump"
umask 077
ssh root@186.244.215.254 'docker exec sub2api-postgres pg_dump -U sub2api -d sub2api --format=custom --compress=zstd:9 --no-owner --no-privileges' > "$REHEARSAL_DUMP"
chmod 600 "$REHEARSAL_DUMP"
docker run --rm -i -v "$REHEARSAL_DUMP:/dump:ro" postgres:18-alpine pg_restore -l /dump > "$ARTIFACT_DIR/rehearsal.restore.list"
test -s "$ARTIFACT_DIR/rehearsal.restore.list"
```

Expected: a readable logical dump; production remains unchanged.

- [ ] **Step 2: Restore the copy into an internal rehearsal network**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
REHEARSAL_DUMP="/tmp/opencode/sub2api-$RELEASE_TAG-rehearsal.dump"
docker network create --internal sub2api-migration-rehearsal
docker run -d --name sub2api-rehearsal-postgres --network sub2api-migration-rehearsal --network-alias postgres \
  -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_USER=sub2api -e POSTGRES_DB=sub2api postgres:18-alpine
timeout 60 sh -c 'until docker exec sub2api-rehearsal-postgres pg_isready -U sub2api -d sub2api >/dev/null; do sleep 2; done'
docker exec -i sub2api-rehearsal-postgres pg_restore --exit-on-error --no-owner --no-privileges -U sub2api -d sub2api < "$REHEARSAL_DUMP"
docker run -d --name sub2api-rehearsal-redis --network sub2api-migration-rehearsal --network-alias redis \
  redis:8-alpine redis-server --save '' --appendonly no
```

Expected: the production copy is restored with no public network exposure.

- [ ] **Step 3: Start the integrated application and apply migrations 183-185**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker run -d --name sub2api-rehearsal-app --network sub2api-migration-rehearsal \
  --read-only --tmpfs /app/data:uid=1000,gid=1000,mode=0700 \
  -e AUTO_SETUP=true -e SERVER_MODE=release -e RUN_MODE=standard \
  -e DATABASE_HOST=postgres -e DATABASE_USER=sub2api -e DATABASE_PASSWORD= -e DATABASE_DBNAME=sub2api -e DATABASE_SSLMODE=disable \
  -e REDIS_HOST=redis -e REDIS_PASSWORD= -e SETUP_MIGRATION_TIMEOUT_SECONDS=600 \
  "$RELEASE_IMAGE"
timeout 180 sh -c 'until test "$(docker inspect sub2api-rehearsal-app --format "{{.State.Health.Status}}")" = healthy; do sleep 5; done'
```

Expected: healthy startup with all three migrations applied.

- [ ] **Step 4: Verify migration identities and description schema**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker exec sub2api-rehearsal-postgres psql -XAt -U sub2api -d sub2api -c \
  "SELECT filename||'|'||checksum FROM schema_migrations WHERE filename IN ('183_ops_ingress_reject_aggregates.sql','184_auth_cache_invalidation_outbox.sql','185_add_channel_model_pricing_description.sql') ORDER BY filename" \
  > "/tmp/opencode/sub2api-$RELEASE_TAG-migrations.actual"
cmp "$ARTIFACT_DIR/migrations.runner" "/tmp/opencode/sub2api-$RELEASE_TAG-migrations.actual"
test "$(docker exec sub2api-rehearsal-postgres psql -XAt -U sub2api -d sub2api -c "SELECT data_type='character varying' AND character_maximum_length=500 AND is_nullable='NO' AND column_default IS NOT NULL FROM information_schema.columns WHERE table_schema='public' AND table_name='channel_model_pricing' AND column_name='description'")" = t
test "$(docker exec sub2api-rehearsal-postgres psql -XAt -U sub2api -d sub2api -c "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='channel_account_stats_model_pricing' AND column_name='description'")" = 0
```

Expected: exact runner checksums, a primary `VARCHAR(500) NOT NULL DEFAULT ''`, and no account-stat column.

- [ ] **Step 5: Verify official worker behavior and embedded assets**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker exec sub2api-rehearsal-postgres psql -X -U sub2api -d sub2api -c \
  "INSERT INTO auth_cache_invalidation_outbox(cache_key) VALUES (repeat('0',64))" >/dev/null
sleep 40
test "$(docker exec sub2api-rehearsal-postgres psql -XAt -U sub2api -d sub2api -c 'SELECT count(*) FROM auth_cache_invalidation_outbox')" = 0
while read -r expected path; do
  actual="$(docker exec sub2api-rehearsal-app wget -qO- "http://127.0.0.1:8080/${path#./}" | sha256sum | cut -d' ' -f1)"
  test "$expected" = "$actual"
done < "$ARTIFACT_DIR/frontend.sha256"
```

Expected: the invalidation worker drains the synthetic event and every embedded static asset matches the frontend stage manifest.

- [ ] **Step 6: Restart once to prove migration idempotence**

Run:

```bash
docker restart sub2api-rehearsal-app >/dev/null
timeout 180 sh -c 'until test "$(docker inspect sub2api-rehearsal-app --format "{{.State.Health.Status}}")" = healthy; do sleep 5; done'
docker logs sub2api-rehearsal-app > /tmp/opencode/sub2api-rehearsal-app.log 2>&1
if grep -Eiq 'panic|fatal|migration.*(failed|mismatch)|worker.*failed' /tmp/opencode/sub2api-rehearsal-app.log; then
  exit 1
fi
```

Expected: a second healthy start with no migration/checksum/worker failure.

- [ ] **Step 7: Remove rehearsal resources and the database copy**

Run:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker rm -f sub2api-rehearsal-app sub2api-rehearsal-redis sub2api-rehearsal-postgres
docker network rm sub2api-migration-rehearsal
rm -f "/tmp/opencode/sub2api-$RELEASE_TAG-rehearsal.dump" "/tmp/opencode/sub2api-$RELEASE_TAG-migrations.actual"
```

Expected: no rehearsal containers, network, or database dump remain.

## Task 16: Transfer Artifacts and Capture the Immediate Production Backup

**Files:**
- Transfer: release image archive, candidate Compose, manifests, and release variable file.
- Create remotely: `/root/sub2api/deploy/backups/$RELEASE_TAG/`

- [ ] **Step 1: Transfer release artifacts without source credentials**

Run locally:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
ssh root@186.244.215.254 'test -d /root/deploy-artifacts && test -d /root/sub2api/deploy'
scp "/root/deploy-artifacts/sub2api-$RELEASE_TAG.tar.gz" root@186.244.215.254:/root/deploy-artifacts/
scp /root/deploy-artifacts/sub2api-v0.1.162-release.env root@186.244.215.254:/root/deploy-artifacts/
scp -r "$ARTIFACT_DIR" root@186.244.215.254:/root/deploy-artifacts/
```

Expected: root-owned release artifacts on production; no `.env` or application credentials leave production.

- [ ] **Step 2: Verify free space and the complete old rollback artifact**

Run on production over SSH:

```bash
set -euo pipefail
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
umask 077
install -d -m 0700 "$BACKUP_DIR"
read -r _ AVAILABLE_BYTES < <(df -B1 --output=avail /root | xargs)
DATABASE_BYTES="$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c 'SELECT pg_database_size(current_database())')"
test "$AVAILABLE_BYTES" -gt "$((DATABASE_BYTES + 2147483648))"
printf '%s  %s\n' 34a4c32dce3db4935ac1e5abb910bd865401de9f2154f9e77e4b33cb6dbe7f45 /root/deploy-artifacts/sub2api-v0.1.160-e56abf53.tar.gz | sha256sum -c -
test "$(docker image inspect weishaw/sub2api:v0.1.160-e56abf53 --format '{{.Id}}')" = sha256:646b0cd4797e85db6de52364f43724fe4e91655dd89ae294c7b0871bd455be50
test "$(docker exec sub2api sha256sum /app/sub2api | cut -d' ' -f1)" = 5c742d0de3b820cb8bcbe3fc913ad84e1985841f018d9a57bcff3eff91c0ac21
```

Expected: enough room for a compressed logical dump plus 2 GiB safety margin, and all old-image identities match the approved baseline.

- [ ] **Step 3: Back up current production configuration and container identity**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
install -m 0600 /root/sub2api/deploy/docker-compose.local.yml "$BACKUP_DIR/docker-compose.local.yml"
install -m 0600 /root/sub2api/deploy/.env "$BACKUP_DIR/.env"
install -m 0600 /root/sub2api/deploy/data/config.yaml "$BACKUP_DIR/config.yaml"
install -m 0600 /root/sub2api/deploy/data/model_pricing.json "$BACKUP_DIR/model_pricing.json"
sha256sum "$BACKUP_DIR/docker-compose.local.yml" "$BACKUP_DIR/.env" "$BACKUP_DIR/config.yaml" "$BACKUP_DIR/model_pricing.json" > "$BACKUP_DIR/config.sha256"
docker inspect sub2api sub2api-postgres sub2api-redis > "$BACKUP_DIR/docker-inspect.json"
chmod 600 "$BACKUP_DIR/docker-inspect.json"
docker inspect sub2api-postgres sub2api-redis --format '{{.Name}}|{{.Id}}|{{.State.StartedAt}}' > "$BACKUP_DIR/dependencies.pre"
systemctl is-active api-pricing-sync mihomo cliproxy-api ops-config-backup.timer > "$BACKUP_DIR/services.pre"
curl -fsS http://127.0.0.1:8080/health > "$BACKUP_DIR/health.pre.json"
docker exec sub2api /app/sub2api -version > "$BACKUP_DIR/version.pre.txt" 2>&1
while read -r method route; do
  code="$(curl -sS -o /dev/null -w '%{http_code}' -X "$method" "http://127.0.0.1:8080$route")"
  printf '%s|%s|%s\n' "$method" "$route" "$code"
done < <(printf '%s\n' \
  'POST /v1/videos' \
  'POST /videos' \
  'GET /v1/videos' \
  'GET /videos' \
  'POST /v1/video/generations' \
  'POST /video/generations' \
  'POST /v1/videos/generations' \
  'POST /videos/generations' \
  'GET /v1/videos/request-check' \
  'GET /videos/request-check' \
  'GET /v1/videos/request-check/content' \
  'GET /videos/request-check/content') > "$BACKUP_DIR/routes.pre.tsv"
test "$(cut -d'|' -f3 "$BACKUP_DIR/routes.pre.tsv" | sort -u)" = 401
```

Expected: root-only configuration and identity snapshots; supporting services are active.

- [ ] **Step 4: Require a quiet durable-video release window**

Run on production:

```bash
ACTIVE_VIDEO_TASKS="$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT count(*) FROM video_tasks WHERE status IN ('submitting','queued','in_progress')")"
test "$ACTIVE_VIDEO_TASKS" = 0
```

Expected: zero active video tasks. Delay deployment if this assertion fails.

- [ ] **Step 5: Create and validate the production PostgreSQL dump**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker exec sub2api-postgres pg_dump -U sub2api -d sub2api \
  --format=custom --compress=zstd:9 --no-owner --no-privileges > "$BACKUP_DIR/postgres.dump"
docker exec -i sub2api-postgres pg_restore -l < "$BACKUP_DIR/postgres.dump" > "$BACKUP_DIR/postgres.restore.list"
test -s "$BACKUP_DIR/postgres.restore.list"
sha256sum "$BACKUP_DIR/postgres.dump" > "$BACKUP_DIR/postgres.dump.sha256"
```

Expected: a readable compressed logical dump. Do not copy the PostgreSQL data directory.

- [ ] **Step 6: Capture the stable production state projection**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
sha256sum -c "$ARTIFACT_DIR/state.sql.sha256"
install -m 0600 "$ARTIFACT_DIR/state.sql" "$BACKUP_DIR/state.sql"
docker exec -i sub2api-postgres psql -v ON_ERROR_STOP=1 -XAtF '|' -U sub2api -d sub2api -f - \
  < "$BACKUP_DIR/state.sql" > "$BACKUP_DIR/state.pre.tsv"
test -s "$BACKUP_DIR/state.pre.tsv"
```

Expected: the committed non-secret query runs successfully and creates a non-empty authoritative stable-state snapshot.

- [ ] **Step 7: Capture migration, trigger, auth-outbox, and settlement snapshots**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker exec sub2api-postgres psql -XAtF '|' -U sub2api -d sub2api -c \
  "SELECT filename,checksum,applied_at FROM schema_migrations ORDER BY filename" > "$BACKUP_DIR/migrations.pre.tsv"
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c 'SELECT count(*) FROM schema_migrations')" = 230
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT count(*) FROM schema_migrations WHERE filename='182_prompt_audit_full_prompt.sql'")" = 1
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT count(*) FROM schema_migrations WHERE filename IN ('183_ops_ingress_reject_aggregates.sql','184_auth_cache_invalidation_outbox.sql','185_add_channel_model_pricing_description.sql')")" = 0
docker exec sub2api-postgres psql -XAtF '|' -U sub2api -d sub2api -c \
  "SELECT tgname,tgenabled,pg_get_triggerdef(oid) FROM pg_trigger WHERE NOT tgisinternal AND tgname IN ('trg_flow2api_gemini_image_billing_compensate','trg_api_keys_auth_cache_invalidation','trg_users_auth_cache_invalidation','trg_groups_auth_cache_invalidation','trg_user_allowed_groups_auth_cache_invalidation') ORDER BY tgname" > "$BACKUP_DIR/triggers.pre.tsv"
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT count(*) FROM pg_trigger WHERE tgname='trg_flow2api_gemini_image_billing_compensate' AND tgenabled='O'")" = 1
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT value FROM settings WHERE key='allow_user_view_error_requests'")" = true
docker exec sub2api-postgres psql -XAtF '|' -U sub2api -d sub2api -c \
  "SELECT to_regclass('public.ops_ingress_reject_aggregates'),to_regclass('public.auth_cache_invalidation_outbox')" > "$BACKUP_DIR/official-tables.pre.tsv"
test "$(<"$BACKUP_DIR/official-tables.pre.tsv")" = '|'
docker exec sub2api-postgres psql -XAtF '|' -U sub2api -d sub2api -c \
  "SELECT state,count(*) FROM video_task_settlements GROUP BY state ORDER BY state" > "$BACKUP_DIR/video-settlements.pre.tsv"
docker exec sub2api-postgres psql -XAtF '|' -U sub2api -d sub2api -c \
  "SELECT count(*) FILTER (WHERE t.status='failed' AND s.state='charged'), count(*) FILTER (WHERE t.status='completed' AND s.state<>'charged'), count(*) FILTER (WHERE s.next_reconcile_at IS NOT NULL AND s.next_reconcile_at<=now()) FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id" > "$BACKUP_DIR/video-anomalies.pre.tsv"
test "$(<"$BACKUP_DIR/video-anomalies.pre.tsv")" = '0|0|0'
```

Expected: compensation trigger enabled in the snapshot and zero failed-unreversed, completed-uncharged, and due-reconciliation rows.

- [ ] **Step 8: Revalidate the candidate and archive immediately before rollout**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
(cd /root/deploy-artifacts && sha256sum -c "$RELEASE_TAG/archive.sha256")
sha256sum -c "$ARTIFACT_DIR/compose.sha256"
docker compose --project-directory /root/sub2api/deploy --env-file /root/sub2api/deploy/.env \
  -f "$ARTIFACT_DIR/docker-compose.local.yml" config --quiet
```

Expected: archive, candidate Compose checksum, and secret-resolved validation all pass.

## Task 17: Deploy Only the Application Container

**Files:**
- Replace remotely: `/root/sub2api/deploy/docker-compose.local.yml`
- Load remotely: `weishaw/sub2api:$RELEASE_TAG`
- Do not recreate: `sub2api-postgres`, `sub2api-redis`

- [ ] **Step 1: Load and verify the unique image**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
gzip -dc "/root/deploy-artifacts/sub2api-$RELEASE_TAG.tar.gz" | docker load > "$BACKUP_DIR/image-load.txt"
test "$(docker image inspect "$RELEASE_IMAGE" --format '{{.Id}}')" = "$(<"$ARTIFACT_DIR/image.id")"
```

Expected: loaded image ID exactly matches the release manifest.

- [ ] **Step 2: Install the candidate Compose and recreate only `sub2api`**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
install -m 0600 "$ARTIFACT_DIR/docker-compose.local.yml" /root/sub2api/deploy/docker-compose.local.yml
date -u +%Y-%m-%dT%H:%M:%SZ > "$BACKUP_DIR/deploy.started_at"
docker compose --project-directory /root/sub2api/deploy --env-file /root/sub2api/deploy/.env \
  -f /root/sub2api/deploy/docker-compose.local.yml \
  up -d --no-deps --force-recreate --pull never sub2api
```

Expected: Compose recreates only the application container.

- [ ] **Step 3: Wait for health and prove dependencies were not recreated**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
timeout 180 sh -c 'until test "$(docker inspect sub2api --format "{{.State.Health.Status}}")" = healthy; do sleep 5; done'
curl -fsS http://127.0.0.1:8080/health
docker inspect sub2api-postgres sub2api-redis --format '{{.Name}}|{{.Id}}|{{.State.StartedAt}}' > "$BACKUP_DIR/dependencies.post"
cmp "$BACKUP_DIR/dependencies.pre" "$BACKUP_DIR/dependencies.post"
```

Expected: application healthy; PostgreSQL and Redis IDs/start times unchanged.

## Task 18: Run Ordered Production Acceptance or Roll Back

**Files:**
- Write remotely: post-deploy snapshots under `$BACKUP_DIR`.
- Possibly restore remotely: backed-up old Compose and old image.

- [ ] **Step 1: Verify image, binary, version, health, and frontend assets**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
test "$(docker image inspect "$RELEASE_IMAGE" --format '{{.Id}}')" = "$(<"$ARTIFACT_DIR/image.id")"
test "$(docker exec sub2api sha256sum /app/sub2api)" = "$(<"$ARTIFACT_DIR/binary.sha256")"
docker exec sub2api /app/sub2api -version > "$BACKUP_DIR/version.post.txt" 2>&1
cmp "$ARTIFACT_DIR/version.txt" "$BACKUP_DIR/version.post.txt"
curl -fsS http://127.0.0.1:8080/health > "$BACKUP_DIR/health.post.json"
while read -r expected path; do
  actual="$(docker exec sub2api wget -qO- "http://127.0.0.1:8080/${path#./}" | sha256sum | cut -d' ' -f1)"
  test "$expected" = "$actual"
done < "$ARTIFACT_DIR/frontend.sha256"
```

Expected: every identity and asset hash matches the release manifest.

- [ ] **Step 2: Verify migrations 183-185 and all auth invalidation triggers**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c \
  "SELECT filename||'|'||checksum FROM schema_migrations WHERE filename IN ('183_ops_ingress_reject_aggregates.sql','184_auth_cache_invalidation_outbox.sql','185_add_channel_model_pricing_description.sql') ORDER BY filename" \
  > "$BACKUP_DIR/migrations.release.tsv"
cmp "$ARTIFACT_DIR/migrations.runner" "$BACKUP_DIR/migrations.release.tsv"
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal AND tgenabled='O' AND tgname IN ('trg_api_keys_auth_cache_invalidation','trg_users_auth_cache_invalidation','trg_groups_auth_cache_invalidation','trg_user_allowed_groups_auth_cache_invalidation')")" = 4
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT data_type='character varying' AND character_maximum_length=500 AND is_nullable='NO' AND column_default IS NOT NULL FROM information_schema.columns WHERE table_schema='public' AND table_name='channel_model_pricing' AND column_name='description'")" = t
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT to_regclass('public.ops_ingress_reject_aggregates') IS NOT NULL AND to_regclass('public.auth_cache_invalidation_outbox') IS NOT NULL")" = t
```

Expected: exact checksums, four enabled auth triggers, and the expected description column.

- [ ] **Step 3: Prove the auth invalidation worker drains a synthetic hash**

Run on production:

```bash
docker exec sub2api-postgres psql -X -U sub2api -d sub2api -c \
  "INSERT INTO auth_cache_invalidation_outbox(cache_key) VALUES (repeat('0',64))" >/dev/null
sleep 40
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c 'SELECT count(*) FROM auth_cache_invalidation_outbox')" = 0
```

Expected: zero pending rows. In the authenticated admin Ops health view, also require auth outbox `running=true`, subscriber `connected=true`, and ingress rejection aggregation accepting events; use the existing browser session and do not place an admin token in shell history.

- [ ] **Step 4: Verify critical trigger, setting, and stable state preservation**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT count(*) FROM pg_trigger WHERE tgname='trg_flow2api_gemini_image_billing_compensate' AND tgenabled='O'")" = 1
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT value FROM settings WHERE key='allow_user_view_error_requests'")" = true
docker exec -i sub2api-postgres psql -v ON_ERROR_STOP=1 -XAtF '|' -U sub2api -d sub2api -f - < "$BACKUP_DIR/state.sql" > "$BACKUP_DIR/state.post.tsv"
cmp "$BACKUP_DIR/state.pre.tsv" "$BACKUP_DIR/state.post.tsv"
systemctl is-active api-pricing-sync mihomo cliproxy-api ops-config-backup.timer > "$BACKUP_DIR/services.post"
cmp "$BACKUP_DIR/services.pre" "$BACKUP_DIR/services.post"
```

Expected: exact stable-state and service-state matches. Any intentional operator change during the window must be documented before accepting a non-empty diff.

- [ ] **Step 5: Verify settlement invariants and route contracts**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker exec sub2api-postgres psql -XAtF '|' -U sub2api -d sub2api -c \
  "SELECT count(*) FILTER (WHERE t.status='failed' AND s.state='charged'), count(*) FILTER (WHERE t.status='completed' AND s.state<>'charged'), count(*) FILTER (WHERE s.next_reconcile_at IS NOT NULL AND s.next_reconcile_at<=now()) FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id" > "$BACKUP_DIR/video-anomalies.post.tsv"
test "$(<"$BACKUP_DIR/video-anomalies.post.tsv")" = '0|0|0'
while read -r method route; do
  code="$(curl -sS -o /dev/null -w '%{http_code}' -X "$method" "http://127.0.0.1:8080$route")"
  printf '%s|%s|%s\n' "$method" "$route" "$code"
done < <(printf '%s\n' \
  'POST /v1/videos' \
  'POST /videos' \
  'GET /v1/videos' \
  'GET /videos' \
  'POST /v1/video/generations' \
  'POST /video/generations' \
  'POST /v1/videos/generations' \
  'POST /videos/generations' \
  'GET /v1/videos/request-check' \
  'GET /videos/request-check' \
  'GET /v1/videos/request-check/content' \
  'GET /videos/request-check/content') > "$BACKUP_DIR/routes.post.tsv"
test "$(cut -d'|' -f3 "$BACKUP_DIR/routes.post.tsv" | sort -u)" = 401
cmp "$BACKUP_DIR/routes.pre.tsv" "$BACKUP_DIR/routes.post.tsv"
```

Expected: zero settlement anomalies and authentication-shaped responses from every durable/Grok video route family.

The committed Gemini image-count regression and the verified release binary identity are the production acceptance evidence for that hotfix. Do not create a paid Gemini image solely for deployment validation.

- [ ] **Step 6: Perform the authenticated description acceptance test and clear it**

Using the existing authenticated admin browser session:

1. Open Channel 4 and choose one primary pricing row.
2. Save exactly two lines of plain text under 500 characters.
3. Reopen the channel and verify the editor reloads both lines.
4. Open Available Channels as a user who can see Channel 4 and verify every model bound to that row shows the same two-line text above billing details.
5. Verify account-statistics pricing has no description editor or value.
6. Clear the test description, save, reopen, and verify the popover has no blank description space.
7. Rerun `$BACKUP_DIR/state.sql` and require the stable-state comparison from Step 4 to remain equal.

Expected: save/reload/display/blank behavior matches the feature contract without leaving production test data.

- [ ] **Step 7: Inspect release-window logs**

Run on production:

```bash
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
docker logs --since "$RELEASE_DATE" sub2api > "$BACKUP_DIR/sub2api.release.log" 2>&1
if grep -Eiq 'panic|fatal|migration.*(failed|mismatch)|billing.*failed|worker.*failed|auth.*invalidation.*(failed|error)' "$BACKUP_DIR/sub2api.release.log"; then
  exit 1
fi
sha256sum "$BACKUP_DIR"/* > "$BACKUP_DIR/SHA256SUMS"
chmod -R go-rwx "$BACKUP_DIR"
```

Expected: no fatal, migration, billing, worker-startup, or repeated auth-invalidation errors.

- [ ] **Step 8: Roll back the application immediately if any acceptance gate fails**

Run on production only after a failed gate:

```bash
set -euo pipefail
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
install -m 0600 "$BACKUP_DIR/docker-compose.local.yml" /root/sub2api/deploy/docker-compose.local.yml
printf '%s  %s\n' 34a4c32dce3db4935ac1e5abb910bd865401de9f2154f9e77e4b33cb6dbe7f45 /root/deploy-artifacts/sub2api-v0.1.160-e56abf53.tar.gz | sha256sum -c -
gzip -dc /root/deploy-artifacts/sub2api-v0.1.160-e56abf53.tar.gz | docker load
test "$(docker image inspect weishaw/sub2api:v0.1.160-e56abf53 --format '{{.Id}}')" = sha256:646b0cd4797e85db6de52364f43724fe4e91655dd89ae294c7b0871bd455be50
docker compose --project-directory /root/sub2api/deploy --env-file /root/sub2api/deploy/.env \
  -f /root/sub2api/deploy/docker-compose.local.yml config --quiet
docker compose --project-directory /root/sub2api/deploy --env-file /root/sub2api/deploy/.env \
  -f /root/sub2api/deploy/docker-compose.local.yml \
  up -d --no-deps --force-recreate --pull never sub2api
timeout 180 sh -c 'until test "$(docker inspect sub2api --format "{{.State.Health.Status}}")" = healthy; do sleep 5; done'
curl -fsS http://127.0.0.1:8080/health
docker inspect sub2api-postgres sub2api-redis --format '{{.Name}}|{{.Id}}|{{.State.StartedAt}}' > "$BACKUP_DIR/dependencies.rollback"
cmp "$BACKUP_DIR/dependencies.pre" "$BACKUP_DIR/dependencies.rollback"
docker exec -i sub2api-postgres psql -v ON_ERROR_STOP=1 -XAtF '|' -U sub2api -d sub2api -f - < "$BACKUP_DIR/state.sql" > "$BACKUP_DIR/state.rollback.tsv"
cmp "$BACKUP_DIR/state.pre.tsv" "$BACKUP_DIR/state.rollback.tsv"
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT count(*) FROM pg_trigger WHERE tgname='trg_flow2api_gemini_image_billing_compensate' AND tgenabled='O'")" = 1
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT value FROM settings WHERE key='allow_user_view_error_requests'")" = true
test "$(docker exec sub2api-postgres psql -XAt -U sub2api -d sub2api -c "SELECT count(*) FILTER (WHERE t.status='failed' AND s.state='charged')||'|'||count(*) FILTER (WHERE t.status='completed' AND s.state<>'charged')||'|'||count(*) FILTER (WHERE s.next_reconcile_at IS NOT NULL AND s.next_reconcile_at<=now()) FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id")" = '0|0|0'
while IFS='|' read -r method route expected; do
  actual="$(curl -sS -o /dev/null -w '%{http_code}' -X "$method" "http://127.0.0.1:8080$route")"
  test "$actual" = "$expected"
done < "$BACKUP_DIR/routes.pre.tsv"
```

Expected: old application health is restored without recreating PostgreSQL or Redis. Leave migrations 183-185 in place. Restore `$BACKUP_DIR/postgres.dump` only after proven database damage, stopped writes, an approved maintenance window, and explicit operator authorization.

- [ ] **Step 9: Record the accepted production identity**

After every acceptance gate passes, create `/root/runbook/2026-07-22-sub2api-v0162-preserved-upgrade.md` on production:

```bash
set -euo pipefail
source /root/deploy-artifacts/sub2api-v0.1.162-release.env
RUNBOOK=/root/runbook/2026-07-22-sub2api-v0162-preserved-upgrade.md
DEPLOY_END="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
test -d /root/runbook
{
  printf '# Sub2API v0.1.162 Preserved Upgrade\n\n'
  printf -- '- Release tag: `%s`\n' "$RELEASE_TAG"
  printf -- '- Integrated commit: `%s`\n' "$RELEASE_COMMIT"
  printf -- '- Canonical tag object: `34b7a5ad70b4b9b9bb96955562fe632ad625d783`\n'
  printf -- '- Canonical peeled commit: `27f094e0960ebd8e52de7ff7e763c6fec2ff4057`\n'
  printf -- '- Docker image ID: `%s`\n' "$(<"$ARTIFACT_DIR/image.id")"
  printf -- '- Image archive: `%s`\n' "$(<"$ARTIFACT_DIR/archive.sha256")"
  printf -- '- Application binary: `%s`\n' "$(<"$ARTIFACT_DIR/binary.sha256")"
  printf -- '- Compose: `%s`\n' "$(<"$ARTIFACT_DIR/compose.sha256")"
  printf -- '- Frontend manifest: `%s`\n' "$(<"$ARTIFACT_DIR/frontend-manifest.sha256")"
  printf -- '- Production backup: `%s`\n' "$BACKUP_DIR"
  printf -- '- Deployment started: `%s`\n' "$(<"$BACKUP_DIR/deploy.started_at")"
  printf -- '- Deployment accepted: `%s`\n' "$DEPLOY_END"
  printf -- '- Acceptance: `PASS`\n\n'
  printf '## Migration Checksums\n\n```text\n%s\n```\n\n' "$(<"$ARTIFACT_DIR/migrations.runner")"
  printf '## Dependency Identity\n\n### Before\n\n```text\n%s\n```\n\n' "$(<"$BACKUP_DIR/dependencies.pre")"
  printf '### After\n\n```text\n%s\n```\n' "$(<"$BACKUP_DIR/dependencies.post")"
} > "$RUNBOOK"
chmod 600 "$RUNBOOK"
sha256sum "$RUNBOOK" > "$BACKUP_DIR/runbook.sha256"
```

Expected: a root-only release record containing identities and outcomes but no secrets or raw credentials.
