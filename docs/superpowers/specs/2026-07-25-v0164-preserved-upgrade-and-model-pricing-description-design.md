# Sub2API v0.1.164 Preserved Upgrade and Model Pricing Description Design

## Summary

Upgrade the customized production lineage from `v0.1.160-e56abf53` to the
official `v0.1.164` release while retaining all locally developed and
production-proven behavior. The release formalizes the already committed
Gemini image-count and resolved-pricing fixes, adds an optional plain-text
description to each primary channel model-pricing row, and incorporates the
official composite-group, Ollama Cloud usage, payment, reliability, and
compatibility changes.

The release is built and tested from source, rehearsed against a copy of the
production database and migration ledger, then deployed directly to production
by recreating only the Sub2API application container. PostgreSQL and Redis
remain running throughout the ordinary rollout and rollback paths.

The committed `v0.1.162` and `v0.1.163` designs and plans remain historical
records. This document supersedes them for the pending upgrade without
rewriting their recorded production observations or recovery guidance.

## Confirmed Decisions

- The integration target is the complete official `v0.1.164` release, not a
  selective backport.
- The customized lineage remains the first parent of one explicit merge from
  the verified official `v0.1.164` tag. The release does not first merge
  `v0.1.163` as a separate integration step.
- Composite groups, Ollama Cloud refresh, and mobile Alipay deep links are
  fully integrated but remain unconfigured and inactive at first production
  rollout.
- The release includes direct production deployment after every source,
  artifact, rehearsal, backup, and preflight gate passes.
- The production deployment recreates only the application container. Database
  restoration is manual and is never an automatic rollback action.

## Baselines

### Source Baseline

- Current customized head when this design was written:
  `06e748a9bcaad4b2f04eb503b7dc3f82e6110919`.
- The customized lineage was previously merged through official `v0.1.160`.
- The deployed Gemini image-count behavior is now committed rather than an
  uncommitted hotfix:
  - `1473cfff fix(billing): count Gemini image alias requests`
  - `06e748a9 fix(billing): reuse resolved channel pricing`
- These commits must be retained and regression-tested, not committed again.
- `deploy/sub2api.dump` is untracked production data. It must stay outside Git
  history, commits, stashes, release contexts, and image build contexts.
- `origin` is the customized `Mocha-s/sub2api` fork. The canonical source is
  `Wei-Shaw/sub2api`; fetch only its exact annotated release tag and never
  merge canonical `main`.

### Official Target

- Annotated tag: `v0.1.164`.
- Tag object: `38a46fd33795c8946a1e88d0f72597c79ca02a76`.
- Peeled commit: `cd8bb98c44303b2c8f04c0da340447c992f0cb7d`.
- The release is 43 commits and 202 changed paths after official `v0.1.163`
  (`d0bdd7e771636a8d315f542cafd39484f39bd60c`).
- The upstream source at the tag still reports `0.1.163` through its server
  version source. The integrated source must correct the build-facing version
  to `0.1.164`; image labels alone are insufficient.

### Production Baseline

- Production is expected to remain on customized
  `v0.1.160-e56abf53` until this release passes every gate.
- Historical runbooks and earlier release artifacts are evidence, not the
  authoritative current state. Capture new source and production recovery
  materials immediately before any mutation.
- Recheck current disk space, containers, supporting services, database ledger,
  account/group/channel/pricing/proxy state, scheduled plans, and video
  settlements during preflight. Do not overwrite live configuration with values
  copied from an old runbook.

## Goals

1. Integrate official `v0.1.161` through `v0.1.164` into the customized
   first-parent lineage.
2. Preserve local managed pricing, proxy support, durable video, settlement and
   refund behavior, Gemini image billing, resolved pricing, gateway routes, and
   frontend behavior.
3. Integrate official composite group routing without changing the behavior of
   existing non-composite groups at rollout.
4. Integrate Ollama Cloud usage synchronization without creating, exposing, or
   refreshing a Cloud session until an administrator explicitly configures one.
5. Retain group reasoning policy support from `v0.1.163` with default-empty
   settings for all existing groups.
6. Add a shared optional description to each primary model-pricing row and
   display it in Available Channels.
7. Produce verified source, image, configuration, and database recovery points
   before directly deploying the complete release to production.

## Non-Goals

- Deploying a raw official image or replacing the customized tree with the
  official tree.
- Rebasing or replaying local commits one by one.
- Creating composite groups, configuring model routes, enabling an Ollama Cloud
  session, or enabling mobile Alipay deep links in the release window.
- Changing any existing group reasoning policy at rollout.
- Adding pricing descriptions to account-statistics pricing rows.
- Rendering pricing descriptions as Markdown or HTML, or using them for
  search, billing, scheduling, or routing.
- Recreating PostgreSQL or Redis during deployment.
- Automatically restoring the database after a failed application rollout.

## Integration Architecture

### Recovery, Commit, and Merge Order

1. Recheck source status and create a root-only source recovery point from the
   current worktree. Capture a branch, bundle, references, porcelain-v2 status,
   tracked patches, and an untracked/ignored-file manifest. Copy the production
   dump separately without staging or stashing it.
2. Verify the existing Gemini image-count and resolved-pricing commits and their
   regression tests. Do not create a duplicate hotfix commit.
3. Fetch and cryptographically identify only the exact `v0.1.164` annotated
   tag using the tag object and peeled commit listed above.
4. Create an integration branch from the customized lineage and merge the
   verified tag with an explicit merge commit. The customized lineage remains
   first parent; the official release is second parent.
5. Resolve semantic conflicts as functional unions, update the build-facing
   server version to `0.1.164`, regenerate generated code, and implement the
   local pricing-description feature.
6. Build, test, rehearse, review, package, and deploy the complete integrated
   state only after all gates pass.

Never use broad staging, `git stash -u`, `git stash --all`, destructive cleanup,
or a raw checkout that could include or remove the protected dump.

### Generated Code and Service Lifecycle

Composite routing adds Ent-backed route data and resolver dependencies. Resolve
the Ent schemas while retaining local group fields, then run `go generate ./ent`.
Resolve `backend/cmd/server/wire.go` before regenerating
`backend/cmd/server/wire_gen.go` with `go generate ./cmd/server`; never edit
generated Ent or Wire output manually.

The generated dependency graph and cleanup function must include all local and
official long-running services, including:

- Local durable-video poller and settlement reconciler.
- Auth-cache invalidation worker and subscriber.
- Ingress-reject aggregation and runtime image-storage services.
- Composite route resolver dependencies.
- Ollama Cloud usage refresh worker and its shutdown path.
- Every previously existing cleanup function.

Run both generators twice and require a clean second run. A missing cleanup path
or a generated-file diff after the second run blocks the release.

### Migration Design

The migration runner identifies migrations by complete filenames, not only their
numeric prefixes, applies them in lexical filename order, and hashes trimmed SQL
content. Both local and upstream `172_*` migrations can therefore coexist, but
their complete names, lexical order, and checksums must be rehearsed against the
production ledger.

The integrated migration manifest includes:

1. Official `172_composite_model_routes.sql`.
2. Local `172_video_per_second_billing_metadata.sql`.
3. Official `183_ops_ingress_reject_aggregates.sql`.
4. Official `184_auth_cache_invalidation_outbox.sql`.
5. Official `185_group_reasoning_effort_policy.sql`.
6. Official `186_alipay_mobile_precreate_deep_link.sql`.
7. Official `186_group_auth_cache_image_generation.sql`.
8. Local `187_add_channel_model_pricing_description.sql`.

The pending local description migration moves from `186` to `187` because the
official release already owns two `186_*` migration names. It adds only a
non-null, default-empty `VARCHAR(500)` `description` column to
`channel_model_pricing`. Account-statistics pricing receives no schema change.

Rehearse the complete manifest on a production-ledger/database copy. Require the
expected filenames and checksums, successful first startup, and successful
second startup before production deployment.

## Functional Compatibility

### Composite Group Routing

A composite group routes a request only when the caller explicitly targets a
configured composite group. Its resolver applies explicit or prefix model rules,
rewrites an alias when configured, and returns the concrete child group,
platform, and forwarded model.

Gateway dispatch, account scheduling, billing, and usage recording consume this
resolved routing context. Billing for a composite alias uses the concrete
forwarded model. A missing route, unavailable child group, or unsupported
platform returns the existing protocol-shaped error; it never silently falls
back to an unrelated group.

No composite group, child-group association, or route is created at first
rollout. Existing groups continue through their current direct routing behavior.
Composite routing must work across Claude, OpenAI, Gemini, Grok, and local
durable-video request paths without replacing local video compatibility aliases
or settlement semantics.

### Billing, Scheduling, and Gateway Union

The integration retains local `pricing_managed_by` and
`pricing_markup_factor` metadata through scheduler cache publication, readback,
and failover. It also retains the one-image fallback for image-priced Gemini
requests with no detected count and the resolved-channel-pricing reuse fix.

Upstream concrete-model fallback for composite aliases must coexist with both
local billing fixes. Tests must prove a request is charged exactly once at the
intended rate for direct, aliased, image-priced, and composite-routed models.

Official OAuth `input` normalization, OpenAI stream-disconnect proxy quarantine,
Grok 402 cooldown, model-name normalization, simple-mode Grok image capability,
and OpenAI test-model behavior are retained. Existing proxy configuration,
channel mappings, managed pricing, durable video quote/task/status/content
routes, settlement/refund ordering, and protocol-specific unsupported-route
responses remain intact.

### Authentication Cache and Group Policy

Existing groups retain empty reasoning-effort limits and mappings after
migration. The capability is present but does not intentionally alter request
behavior until an administrator configures it.

The auth-cache snapshot and invalidation design must combine the upstream
image-generation permission changes with local video-generation access,
reasoning-policy fields, and managed-pricing scheduler metadata. Updating any
covered group, API-key, association, or permission state must invalidate stale
cache data rather than permit a partial old snapshot. The invalidation outbox
and worker must remain healthy without an accumulating delivery backlog.

### Ollama Cloud Usage

Ollama accounts may store an encrypted Cloud session and a usage snapshot. The
integration preserves upstream write-only and redacted API behavior, encryption,
leader locking, account-change invalidation, and graceful worker shutdown.

At rollout no account receives an Ollama Cloud session or automatic refresh
configuration. The worker may run, but it only refreshes explicitly configured
and successfully decryptable account state. Decryption failures fail closed,
invalidate unusable state as upstream requires, and never expose session values
in APIs, audit logs, errors, or release artifacts.

### Payment and Other Default-Inactive Features

The mobile Alipay precreate deep-link capability is integrated with its
migration but stays disabled or unconfigured. Existing desktop and non-Alipay
payment flows retain their current behavior. Before release, verify that the
mobile deep-link setting is false, there are no composite-platform groups or
configured composite routes, and no Ollama account is a configured Cloud-refresh
candidate. Treat any non-empty or enabled state as a failed gate requiring an
explicit operator decision, not as a state to silently preserve or overwrite.

No release task enables composite routes, Ollama Cloud refresh, mobile Alipay
deep links, or new group reasoning policy values. Enabling any of them is a
separate, post-release operational change with its own acceptance checks.

## Model Pricing Description

### Data and API Contract

Primary `channel_model_pricing` gains `Description string` in its in-memory
model. One pricing row can bind multiple model names, and every model attached
to that row shares its description. Handwritten primary-pricing select, scan,
list, insert, update, replacement, admin-read, admin conversion, and
user-facing Available Channels paths carry the field.

The admin API trims surrounding whitespace and accepts at most 500 characters.
The user-facing pricing whitelist and `toUserPricing` conversion explicitly copy
the normalized description. When LiteLLM display pricing fills otherwise
missing price information, it preserves an authored description without using
that description to decide fallback eligibility.

`channel_account_stats_model_pricing` remains unchanged. Shared in-memory types
and conversion code must explicitly omit descriptions for those rules, so an
injected request field cannot be persisted, returned, or displayed.

### Frontend Contract

Primary pricing DTOs and form state default descriptions to an empty string and
trim them during serialization. `PricingEntryCard` receives an explicit control
for the description editor:

- Primary channel pricing renders a localized plain-text multiline editor with
  `maxlength="500"` and a live character count.
- Account-statistics pricing neither renders nor serializes a description
  editor.

The collapsed pricing summary remains unchanged. In Available Channels,
non-empty descriptions render as escaped secondary text below the model header
and above billing/prices. The display preserves line breaks, wraps long words,
and adds no node or space for an empty description. Descriptions remain excluded
from Available Channels search, with a focused test that proves they do not
change search results.

## Failure Handling

- A tag identity mismatch, unexpected merge parent, generated-code instability,
  migration filename/checksum mismatch, or version mismatch blocks the release
  before an image is accepted.
- Composite routing errors use the existing endpoint-specific error contract;
  they do not dispatch to an arbitrary child group.
- Billing regressions, duplicate charge paths, or loss of local pricing metadata
  block deployment and trigger application rollback if detected after rollout.
- Ollama encryption or refresh failures never expose credentials and never make
  an unconfigured account eligible for Cloud refresh.
- A missing required worker, growing auth invalidation outbox, unhealthy
  application, panic, fatal startup error, asset mismatch, or protected-state
  comparison anomaly triggers immediate application rollback.

## Verification Design

### Source Gates

- Verify source status, staged state, dump exclusion, `git diff --check`, exact
  tag object, peeled commit, tree, ancestry, and release delta.
- Verify stable Ent and Wire generation on two consecutive runs.
- Run the backend unit suite and focused tests for managed pricing, scheduler
  cache metadata, Gemini image counts, resolved pricing, proxy behavior,
  durable video, settlement, refund, reconciliation, and graceful shutdown.
- Run focused composite-route tests for explicit/prefix rules, aliases,
  unsupported paths, child selection, concrete-model billing, and direct-group
  non-regression.
- Run focused auth-cache, group reasoning, image-generation permission,
  invalidation outbox, Grok, OAuth, OpenAI proxy, and channel-model-name tests.
- Run Ollama tests for encryption, write-only/redacted responses, account and
  proxy invalidation, leader locking, refresh failure, and worker shutdown.
- Run frontend lint, Vue typecheck, critical Vitest tests, and focused tests for
  composite-group administration, Ollama usage, Alipay mobile links,
  pricing-description forms, and Supported Model popovers.
- Run migration shape, ledger, checksum, schema, and integration tests, plus
  the repository secret scan.

### Artifact and Rehearsal Gates

Build the frontend before the embedded Go binary. Build from a clean
`git archive HEAD`, not from a dirty worktree, because the protected dump is not
safe to include in a build context. Record a unique image tag, image ID and
digest, image archive hash, binary hash, embedded frontend asset manifest,
source revision, build-facing version, and migration manifest. Supply
`VERSION=0.1.164` and `COMMIT=<merged SHA>` through the supported build
provenance inputs and update the server version source before building.

The built application must report both `0.1.164` and the merged source SHA via
the binary/API contract; a `0.1.163`, `docker`, or otherwise mismatched version
or commit response blocks the artifact. Start an isolated stack against a copy
of the production database and migration ledger. Require two successful starts,
health checks, embedded asset verification, all migration identities, and the
gateway, billing, cache, video, and pricing-description contracts above.

### Production Preflight and Acceptance

Immediately before deployment, capture an authoritative database and runtime
state snapshot. Create a PostgreSQL logical dump and validate it with
`pg_restore -l`; capture configuration, checksums, Docker inspect data, old
image availability, migration ledger, critical settings/triggers, channel and
pricing state, proxy state, account/group state, scheduled plans, auth outbox,
and video settlements. Preserve a validated copy of the previous Compose file
and rollback image reference. Do not print resolved configuration, secrets,
Cloud sessions, or database credentials into command output or release records.

After deployment, verify:

1. Application health, version `0.1.164`, source revision, image digest, and
   embedded frontend assets.
2. Migration records and checksums for both `172_*` files and every relevant
   migration through `187`.
3. Required worker startup, auth-cache invalidation delivery, and no growing
   outbox backlog.
4. Existing group reasoning fields remain empty; the exact preflight checks
   confirm zero composite-platform groups/routes, zero configured Ollama Cloud
   refresh candidates, and a false mobile Alipay deep-link setting.
5. Proxy, account/group, scheduled-plan, managed-pricing, channel-pricing, and
   video-settlement state match the immediate preflight snapshot unless an
   intentional acceptance test changed it.
6. Composite routing tests, direct routing, Grok/content, OAuth, OpenAI
   durable-video, and proxy failure contracts remain valid.
7. The Gemini image-count and resolved-pricing regressions remain covered
   without creating a paid image solely for acceptance.
8. A primary pricing description saves, reloads, preserves line breaks in
   Available Channels, and a blank description has no display effect.
9. Recent logs contain no panic, fatal, migration, billing, worker, encryption,
   cache-invalidation, or repeated proxy-isolation failure.

## Deployment and Rollback

Update only the unique application image reference in the customized production
Compose deployment and recreate only the application service with Compose
`--no-deps`. Do not recreate PostgreSQL or Redis. Watch startup until migrations,
health checks, and every required worker are healthy before accepting traffic as
fully released.

Immediately restore the prior application image and Compose file if any
migration or checksum fails, the application remains unhealthy, a required
worker is missing, embedded assets are wrong, a critical gateway/video/billing
behavior regresses, protected state changes unexpectedly, or the auth outbox
grows without delivery. After rollback, rerun health and read-only state checks
against the immediate preflight snapshot.

The ordinary rollback leaves the additive migrations in place. The old
application has no direct dependency on the added composite tables, settings,
or pricing-description column, but migration `184` can still enqueue
cache-invalidation outbox rows after a legacy-image rollback. Rehearse the old
image against the migrated database before release, including a controlled
auth/group mutation, outbox inspection, health checks, and protected-state
comparison. If the legacy image cannot safely tolerate or bound the resulting
outbox behavior, direct release is blocked until a non-destructive,
operator-approved rollback procedure is proven; never truncate the outbox as an
automatic remedy.

Restore PostgreSQL only when an additive migration is proven to have damaged
production data, after explicit operator approval, a maintenance window, stopped
application writes, and post-restore integrity checks.

## Acceptance Criteria

1. The merged source identifies exact official `v0.1.164` while the customized
   lineage remains its first parent and the built application reports `0.1.164`.
2. Local Gemini image-count, resolved-pricing, managed-pricing, proxy, durable
   video, settlement, refund, and UI behavior remain operational.
3. Composite routing, Ollama usage, group reasoning, payment, OAuth, proxy,
   Grok, and model-normalization changes are integrated and verified.
4. Existing groups and accounts retain their pre-release behavior because new
   composite, Ollama Cloud, mobile Alipay, and reasoning configurations remain
   unconfigured.
5. Composite aliases charge against their concrete forwarded model without
   duplicate or lost local billing behavior.
6. Migrations through `187`, including both `172_*` and both `186_*` files,
   pass rehearsal and idempotent second-start checks.
7. Primary pricing descriptions work end to end; account-statistics pricing
   never exposes or persists them.
8. Production deployment recreates only the application container and has
   verified source, image, configuration, and database recovery artifacts.
9. The prior application image and Compose file can be restored without a
   database restore.
