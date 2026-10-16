# Sub2API v0.1.162 Preserved Upgrade and Model Pricing Description Design

## Summary

Upgrade the customized Sub2API production lineage from `v0.1.160-e56abf53` to official `v0.1.162`, preserve all locally developed and production-proven behavior, formally include the deployed Gemini image-count hotfix, and add an optional custom description to each primary channel model-pricing entry.

The release will be built from source, tested as one integrated system, backed up at source and production levels, and deployed by recreating only the Sub2API application container. PostgreSQL and Redis will remain running. Application rollback restores the current image and Compose file without reverting additive migrations; database restore is a manual last resort.

## Baselines

### Source Baseline

- Current customized head: `e56abf53e58ffe8574e6b40b1afaf58a02bdc219`.
- Current official ancestor: `v0.1.160`, commit `8bfbc5ca99bf2c0ac96e0f29ffd35eb6aca27e62`.
- Target official release: annotated tag `v0.1.162`, peeled commit `27f094e0960ebd8e52de7ff7e763c6fec2ff4057`.
- The current branch contains 41 local-side commits plus the merge commit over official `v0.1.160`.
- The official `v0.1.160..v0.1.162` range contains 176 graph commits and changes 377 paths.
- The configured `origin` is the customized `Mocha-s/sub2api` fork. The canonical release must be fetched from `Wei-Shaw/sub2api` by exact tag, not from canonical `main`.

### Uncommitted Production Hotfix

The current worktree has a two-file Gemini image-count fix:

- `backend/internal/service/gateway_usage_billing.go`
- `backend/internal/service/gateway_record_usage_test.go`

It sets a missing image count to one only when resolved channel pricing is `BillingModeImage`, then reapplies image billing normalization. The production binary contains this fix even though source control does not: `/app/sub2api` has SHA-256 `5c742d0de3b820cb8bcbe3fc913ad84e1985841f018d9a57bcff3eff91c0ac21`, matching the final July 21 build record.

This hotfix must become a dedicated source commit before merging the official tag. The untracked `deploy/sub2api.dump` must never be staged.

### Production Baseline

- Host: `186.244.215.254`.
- Running image: `weishaw/sub2api:v0.1.160-e56abf53`.
- Running image ID: `sha256:646b0cd4797e85db6de52364f43724fe4e91655dd89ae294c7b0871bd455be50`.
- Current image archive: `/root/deploy-artifacts/sub2api-v0.1.160-e56abf53.tar.gz`.
- Current archive SHA-256: `34a4c32dce3db4935ac1e5abb910bd865401de9f2154f9e77e4b33cb6dbe7f45`.
- Compose file: `/root/sub2api/deploy/docker-compose.local.yml`.
- Production source/config directory is not a Git repository. Rollback therefore depends on verified images, configuration snapshots, database dumps, and runbooks rather than server-local Git history.
- Production PostgreSQL has 230 migration records through `182_prompt_audit_full_prompt.sql`.
- Available disk space observed during design: about 5.4 GB. Backups must be space-conscious.

## Goals

- Integrate all changes in official `v0.1.161` and `v0.1.162` into the customized lineage.
- Preserve local managed-pricing, durable video, settlement/refund, proxy, billing, and UI behavior.
- Preserve and commit the production Gemini image-count hotfix.
- Add one shared plain-text description to each primary channel model-pricing entry.
- Display non-empty descriptions in the existing Available Channels model detail popover.
- Preserve current production configuration and current database state, including state that has legitimately changed since older runbooks were written.
- Produce verified source, image, configuration, and database rollback points before production changes.
- Deploy the integrated release directly to production after all gates pass.

## Non-Goals

- Deploying the raw official `v0.1.162` image.
- Rebasing or replaying the 41 local commits one by one.
- Restoring old account, channel, pricing, or proxy values merely because an older runbook recorded them.
- Independent descriptions for models sharing one pricing entry.
- Descriptions on account-statistics pricing rules.
- Markdown or HTML descriptions.
- Searching, billing, scheduling, or routing by description.
- Recreating PostgreSQL or Redis during the application deployment.
- Automatically restoring the database after a failed application rollout.

## Production State Preservation

Runbooks under `/root/runbook` are historical evidence, not an authoritative desired-state database. Several values have changed through later operations. The release process must snapshot immediately before deployment and compare immediately after deployment.

Known current state includes:

- `trg_flow2api_gemini_image_billing_compensate` is enabled.
- `allow_user_view_error_requests=true`.
- Video settlements contain three charged, ten refunded, and one released record, with no failed task awaiting reversal, completed task awaiting charge, or due reconciliation.
- Scheduled test plan 1 for account 159 remains enabled every five minutes with `auto_recover=true`.
- Account 159 is currently active but deliberately `schedulable=false`, with base multiplier `0.0010`, `pricing_managed_by=api-pricing-sync`, and markup factor `1.5`.
- Group 19 has video generation enabled.
- Channel 4 currently uses `billing_model_source=channel_mapped`, has an empty model mapping, and contains newer Gemini image pricing rows. It must not be overwritten with older Nano-Banana mappings from historical runbooks.
- Proxy `mihomo-us-7891` is active and currently represented as SOCKS5 through `host.docker.internal:7892`.
- `api-pricing-sync`, Mihomo, CLIProxyAPI, and the Sub2API configuration-backup timer are active.

All these values are observations, not hard-coded migration targets. The deployment snapshot is authoritative if any value changes before the release window.

## Integration Strategy

### Commit and Merge Order

1. Capture a source rollback branch and bundle while the current repository still points at `e56abf53`.
2. Commit only the two tracked Gemini image-count hotfix files as a dedicated commit.
3. Fetch and cryptographically identify the canonical `v0.1.162` tag.
4. Merge `v0.1.162` into the customized first-parent lineage with an explicit merge commit.
5. Resolve integration conflicts as functional unions.
6. Regenerate Wire from `wire.go`; do not hand-maintain `wire_gen.go`.
7. Implement the model-pricing description feature using migration 185.
8. Build, test, review, and package the complete integrated state.

Do not merge canonical `main`, which is newer than the requested release. Do not replace the customized tree with the official release and then attempt to recover behavior from production artifacts.

### Expected Conflict Resolution

#### Dependency Injection

`backend/cmd/server/wire_gen.go` is expected to conflict. Resolve `backend/cmd/server/wire.go` first so cleanup and forced construction include both sets of long-running services:

- Local durable video poller and settlement reconciler services.
- Official auth-cache invalidation worker and subscriber.
- Official ingress-reject aggregation.
- Official runtime image-storage and related release services.
- Every existing local and upstream cleanup step.

Then run `go generate ./cmd/server` and verify generation is stable on a second run.

#### Gateway Video Routes

`backend/internal/server/routes/gateway.go` is expected to conflict at video routes. Use one registration per path and dispatch by group platform:

- Grok: official generation, edit, extension, status, and chained content proxy handlers.
- OpenAI: local durable video task create/status/content handlers and compatibility aliases.
- Other platforms: the existing protocol-shaped 404 behavior.

Retain official local Grok `count_tokens`, text-body limits, Codex models behavior, and all local OpenAI video aliases. Route tests must prove there is no duplicate Gin registration.

#### Semantically Overlapping Files

Mechanically clean merges still require review in account scheduling/auth cache, upstream HTTP/failover, embedded frontend bypass, service provider sets, migration tests, and frontend API types. Local managed-pricing markers and markup multiplication must remain intact.

## Official v0.1.162 Changes

The integrated release must retain official improvements, including:

- Configurable real-client IP resolution and trusted proxy handling.
- Runtime async-image object-storage settings.
- Grok client tool caching, content proxying, local token estimation, and probe improvements.
- Update checks with optional GitHub token.
- API-key partial-update protection for IP lists.
- Same-account retry billing correction.
- OpenAI/Codex failover and response compatibility fixes.
- Available Channels scrolling and dark-mode fixes.
- Docker Redis command and PostgreSQL tuning corrections.

Official migrations are:

- `183_ops_ingress_reject_aggregates.sql`.
- `184_auth_cache_invalidation_outbox.sql`.

Migration 184 adds triggers on API keys, users, groups, and allowed-group associations. Startup verification must confirm the invalidation worker is running and the outbox is not accumulating undelivered rows.

## Model Pricing Description

### Data Model

Add `backend/migrations/185_add_channel_model_pricing_description.sql`:

```sql
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS description VARCHAR(500) NOT NULL DEFAULT '';
```

Use migration 185 because official `v0.1.162` occupies 183 and 184. Existing records receive an empty description. No Ent generation is required because channel pricing uses handwritten SQL.

Add `Description string` to `service.ChannelModelPricing`. A pricing row may bind multiple model names, so every model in `Models` shares the row's description.

Do not alter `channel_account_stats_model_pricing`. Although account-statistics rules reuse `ChannelModelPricing` in memory, their persistence and API conversion must ignore `Description`.

### Backend Persistence and APIs

Update all primary `channel_model_pricing` select, scan, insert, update, and replacement paths to include `description`.

Add `description` to the primary admin model-pricing request and response. Trim surrounding whitespace and enforce a maximum of 500 characters. The shared admin conversion code must explicitly distinguish primary pricing from account-statistics pricing so nested account-statistics requests cannot persist a supplied description.

Add `description` to the user-facing available-channels pricing whitelist and copy it in `toUserPricing`.

When global LiteLLM display pricing fills an otherwise unpriced channel entry, preserve the channel-authored description in the synthesized pricing object. Description does not affect fallback eligibility.

### Frontend Editor

Add `description: string` to the primary admin/user DTOs and `PricingFormEntry`. New and hydrated entries default to an empty string. Primary serialization trims the value.

Extend `PricingEntryCard` with an explicit prop controlling whether the description editor appears:

- Primary channel model pricing enables the editor.
- Account-statistics pricing leaves it disabled.

Place the editor below the models/billing-mode row and above prices. Follow the current Sub2API input styling and include:

- Localized label and placeholder.
- Plain-text multiline input.
- `maxlength="500"`.
- Live `current/500` character count.
- Existing immutable entry update behavior.

The collapsed pricing summary remains unchanged.

### Available Channels Display

Keep the existing model chip, platform colors, hover/focus behavior, popover dimensions, and detail rows. For a non-empty description, render escaped text immediately below the model header and above billing mode/prices. Preserve line breaks, wrap long words, and use restrained secondary text consistent with the existing design. An empty description renders no node and consumes no space.

Descriptions are not included in Available Channels search.

## Compose Integration

The production Compose file is a customized deployment artifact and must be merged intentionally rather than replaced by the repository example.

Preserve:

- Unique integrated image tag pinned by name and recorded digest.
- JSON log rotation (`100m`, five files) for application, PostgreSQL, and Redis.
- `host.docker.internal:host-gateway` for production proxy access.
- Existing bind paths without forcing SELinux suffixes on this host.
- CORS wildcard with credentials disabled.
- Current logging environment variables.
- Current security defaults and actual `.env` overrides.
- Image stream/concurrency and OpenAI HTTP settings still supported by the integrated code.

Integrate from official `v0.1.162`:

- `UPDATE_GITHUB_TOKEN` environment passthrough.
- `ENABLE_SERVER_TIMING` and setup migration timeout where supported.
- Correct Redis `sh -c` line continuation so persistence flags are passed to `redis-server`.
- Official PostgreSQL tuning or command corrections.

Validate with `docker compose config` using the production `.env` before deployment. Never print resolved secrets into logs or the runbook.

## Backup Design

### Local Source Backup

Create a timestamped root-only backup directory outside the repository and capture:

- A branch pointing to the exact pre-upgrade head.
- A Git bundle containing branches, tags, remotes, and stash references, followed by `git bundle verify`.
- `git show-ref`, recent history, and porcelain-v2 status manifests.
- Binary patches for staged and unstaged tracked changes.
- A manifest of untracked and ignored files.
- A separate root-only copy of `deploy/sub2api.dump`; never add it to Git.

Do not use `git stash -u`, `git stash --all`, `git add -A`, or destructive cleanup because sensitive ignored deployment state must not enter Git objects or be deleted.

### Production Backup

Create a timestamped root-only release backup directory containing:

- Current Compose, `.env`, `data/config.yaml`, `model_pricing.json`, and checksums.
- Docker inspect output and current application binary/image/archive hashes.
- The current image archive, referenced in place if its verified artifact remains available.
- A custom-format PostgreSQL dump created immediately before deployment.
- Successful `pg_restore -l` output proving the dump is readable.
- Migration ledger, trigger, critical setting, channel/pricing, proxy, account/group, scheduled-plan, auth-outbox, and video-settlement snapshots.
- Current health/version/route contract and relevant service-status snapshots.

The existing six-hour config backup is useful but excludes PostgreSQL and Redis. It is not sufficient for this release.

Because only about 5.4 GB was free during design, do not copy the 2 GB PostgreSQL data directory. A compressed logical dump plus the verified current image archive and small configuration snapshots provides the required rollback point. Check free space again before backup and deployment.

## Verification Gates

### Source and Unit Gates

- `git diff --check` and secret scan.
- Stable Wire generation.
- Backend unit suite with the repository's `unit` tag.
- Focused managed-pricing and scheduler metadata tests.
- Focused Gemini image-count regression test.
- Focused durable video route, adapter, poller, settlement, refund, and reconciliation tests.
- Focused official auth-cache, ingress, Grok content, real-IP, and image-storage tests.
- Frontend ESLint and Vue typecheck.
- Existing critical Vitest suite.
- Focused pricing form and `SupportedModelChip` tests.
- Embedded frontend route/bypass tests.
- Migration checksum and schema tests.

### Model Description Tests

- Migration creates a non-null, default-empty, 500-character column.
- Primary pricing repository round trip preserves description.
- Admin request validation and trimming.
- Account-statistics conversion ignores an injected description.
- Available-channels DTO exposes description.
- LiteLLM token and image/request fallback retains description.
- Form initialization, hydration, trimming, and serialization.
- Primary editor renders textarea and count; account-statistics editor does not.
- Popover preserves multiline text and renders nothing for empty descriptions.

### Release Artifact Gates

- Build frontend before the embedded Go binary.
- Build a unique image tag; never overwrite the current rollback tag.
- Record image ID, digest, archive hash, binary hash, version, commit, and frontend asset names.
- Confirm the embedded assets match the just-built frontend. This prevents recurrence of the July 21 stale-dist incident.
- Start an isolated stack and test migration from a production-ledger/database copy through 183, 184, and 185.
- Confirm the application can start twice against the migrated copy without migration or checksum errors.

## Production Deployment

### Preflight

1. Recheck disk space, container health, external supporting services, and active workload.
2. Capture the authoritative immediate pre-deploy state snapshot.
3. Create and validate the PostgreSQL dump.
4. Validate the merged Compose with the production `.env`.
5. Load the uniquely tagged image and verify its ID/digest before use.
6. Ensure the old Compose and current image remain locally available.

### Rollout

Update only the Sub2API image reference, then run Compose with `--no-deps` to recreate only the application container. Do not recreate PostgreSQL or Redis.

The new application applies migrations 183, 184, and 185 under the existing migration advisory lock. Observe startup logs until migrations, background workers, and health checks complete.

### Post-Deploy Acceptance

Verify, in order:

1. Container health, `/health`, version, commit, image digest, and frontend assets.
2. Migration records and checksums for 183, 184, and 185.
3. Auth-cache invalidation worker/subscriber health and no stuck outbox accumulation.
4. Ingress aggregation and other newly integrated official workers are running.
5. Production compensation trigger and critical settings remain present.
6. Current channel pricing, proxy, managed-pricing markers, account/group state, and scheduled plans equal the preflight snapshot unless intentionally changed during the window.
7. Video settlements still have no due, failed-unreversed, or completed-uncharged records.
8. OpenAI durable video aliases and Grok chained video status/content routes return the expected unauthenticated or existing-task contract.
9. Gemini image-count behavior is covered by the integrated regression test and binary identity. Do not create a paid image solely for deployment validation.
10. Model pricing description saves, reloads, appears in Available Channels, preserves line breaks, and leaves blank descriptions unchanged.
11. Recent logs contain no panic, fatal, migration failure, billing failure, worker startup failure, or repeated auth invalidation errors.

## Rollback

### Automatic Application Rollback Conditions

Immediately restore the old Compose and `v0.1.160-e56abf53` image if any of these occur:

- Migration or checksum failure.
- Persistent unhealthy application container or failed `/health`.
- Panic, fatal startup error, or missing required background worker.
- Embedded frontend assets do not match the release manifest.
- Critical OpenAI/Grok/video route regression.
- Loss of production trigger, setting, pricing, account/group, proxy, or scheduled-plan state.
- New video settlement inconsistency.
- Auth invalidation outbox grows without delivery.

After application rollback, run the same health and read-only state checks against the preflight snapshot.

### Database Rollback Policy

Migrations 183, 184, and 185 are additive. The old application ignores their new tables, triggers, and column, so ordinary rollback must leave the migrated database in place. This avoids losing legitimate writes made after deployment.

Restore the PostgreSQL dump only if an additive migration is proven to have damaged production state. Database restore requires a maintenance window, stopped application writes, explicit operator approval, and post-restore integrity checks. It is never automatic.

## Acceptance Criteria

1. The integrated source contains official `v0.1.162`, every current customized behavior, and the Gemini image-count hotfix as committed history.
2. Managed-pricing multiplication, scheduler metadata, durable video tasks, settlement/refund ordering, and production proxy support remain operational.
3. Official auth-cache, ingress, real-IP, Grok, image-storage, and Compose fixes are present and verified.
4. Administrators can save up to 500 plain-text characters on primary model-pricing entries, and every model sharing the entry displays the description in Available Channels.
5. Account-statistics pricing exposes and persists no description.
6. Production is backed up with verified source, image, configuration, and PostgreSQL recovery artifacts before deployment.
7. Only the application container is recreated during rollout.
8. Immediate post-deploy state matches the authoritative pre-deploy snapshot, except for expected additive migrations and explicitly tested description data.
9. The old application image and Compose can be restored without database restore.
