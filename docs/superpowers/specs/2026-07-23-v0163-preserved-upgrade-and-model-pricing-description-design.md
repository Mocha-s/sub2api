# Sub2API v0.1.163 Preserved Upgrade and Model Pricing Description Design

## Summary

Upgrade the customized production lineage from `v0.1.160-e56abf53` to the
official `v0.1.163` release while retaining all local production fixes and
optimizations. The release also formalizes the deployed Gemini image-count
hotfix and adds an optional, plain-text description to each primary channel
model-pricing entry.

The release is built and tested from source, rehearsed against a production
database copy, and deployed directly to production by recreating only the
Sub2API application container. PostgreSQL and Redis remain running.

The committed `v0.1.162` design and implementation plan remain historical
records. This specification supersedes them for the pending upgrade; it does
not alter their approved facts or erase their recovery guidance.

## Baselines

### Source Baseline

- Current customized head when this design was written:
  `6a606e0fd89cdf5a6f8b13bc27a1256e379e1499`.
- The customized lineage was previously merged through official `v0.1.160`.
- The current worktree contains an uncommitted, deployed Gemini image-count
  hotfix in `backend/internal/service/gateway_usage_billing.go` and
  `backend/internal/service/gateway_record_usage_test.go`.
- `deploy/sub2api.dump` is untracked production data. It must remain outside
  Git history, commits, stashes, release contexts, and image build contexts.
- `origin` is the customized `Mocha-s/sub2api` fork. The release source is
  `Wei-Shaw/sub2api`; fetch only its exact annotated tag, never canonical
  `main`.

### Official Target

- Annotated tag: `v0.1.163`.
- Tag object: `bb752ef7776dc126ffca5df9188087d0d0aed559`.
- Peeled commit: `d0bdd7e771636a8d315f542cafd39484f39bd60c`.
- Peeled tree: `cd14d52ba33db23835f7233304c1d88fe76b0f57`.
- Relative to official `v0.1.162`, the target contains 69 commits and changes
  171 paths.

### Production Baseline

- Production continues to run the customized `v0.1.160-e56abf53` application
  image until this release passes all gates.
- Existing source recovery artifacts and historical production recovery records
  must be rechecked and augmented immediately before any source or production
  mutation. They are not a substitute for a current pre-deploy snapshot.
- Production deployment recreates only the application service. It must never
  recreate PostgreSQL or Redis.

## Goals

1. Integrate official `v0.1.161`, `v0.1.162`, and `v0.1.163` changes into the
   customized first-parent lineage.
2. Preserve local managed pricing, proxy support, durable video, settlement and
   refund, billing, Gemini image-count, route, and UI behavior.
3. Formalize the deployed Gemini image-count fix as a dedicated source commit
   before the official merge.
4. Add a shared, optional plain-text description to primary model-pricing rows.
5. Incorporate official group-level OpenAI reasoning policy support without
   changing the current behavior of any existing group at rollout.
6. Validate and deploy the integrated release directly to production with
   complete application rollback coverage.

## Non-Goals

- Deploying a raw official image or replacing the customized tree with the
  official tree.
- Rebasing or replaying local commits one at a time.
- Configuring group reasoning caps or mappings during this release window.
- Adding descriptions to account-statistics pricing rows.
- Rendering pricing descriptions as Markdown or HTML, or using them for
  search, billing, scheduling, or routing.
- Recreating PostgreSQL or Redis during rollout.
- Automatically restoring the database after a failed application rollout.

## Integration Architecture

### Commit and Merge Order

1. Verify the current source recovery point and create or refresh root-only
   recovery materials for the present worktree, including the uncommitted
   hotfix and a separately protected dump copy.
2. Commit only the two Gemini hotfix files as one dedicated commit. Do not
   stage the dump or use broad add, stash, or cleanup commands.
3. Fetch and verify only the exact canonical `v0.1.163` annotated tag against
   the object, commit, and tree identities above.
4. Create an integration branch from the customized lineage and merge the
   verified official tag with an explicit merge commit. The customized lineage
   remains the first parent and the official target is the second parent.
5. Resolve semantic conflicts as functional unions, regenerate generated code
   from source inputs, and implement the custom pricing-description feature.

This approach preserves reviewable local history and makes all upstream changes
visible as a single, auditable integration event. The older `v0.1.162` design
and plan are retained for provenance, while new implementation instructions are
written specifically for `v0.1.163`.

### Generated Code and Long-Running Services

Resolve `backend/cmd/server/wire.go` before regenerating
`backend/cmd/server/wire_gen.go`. The resulting dependency graph and cleanup
function must include both the local durable video poller and settlement
reconciler and every official long-running service, including auth-cache
invalidation, ingress-reject aggregation, runtime image storage, and their
cleanup paths. Run Wire generation twice and require a clean second result.

The group reasoning policy changes touch the Group Ent schema and generated
Ent code. Preserve local video-generation fields while adding the official
reasoning fields, then run the required Ent generator. Do not hand-edit
generated Ent or Wire output.

### Gateway, Scheduling, and Billing Union

The gateway merge retains exactly one registration per path and dispatches by
group platform:

- Official Grok `/responses/compact`, client tool protocol preservation,
  protected chained-content download, OAuth model synchronization, session
  affinity, and model-scoped content-policy handling are retained.
- Local OpenAI durable-video create, status, content, and compatibility aliases
  remain available with their existing quote, task, and settlement behavior.
- Existing protocol-shaped errors for unsupported platform/path combinations
  remain intact.

The scheduler union keeps official quota metadata and its monotonic `LastUsedAt`
Redis side key, while retaining local `pricing_managed_by` and
`pricing_markup_factor` metadata. Neither set of fields may be dropped during
cache publication, readback, or failover.

The local fallback of one image request when a channel image-priced Gemini
request has no detected count remains active. It coexists with the official
hosted `image_generation` tool token extraction for OpenAI responses. Tests
must prove both paths account for images correctly without double counting.

## Group Reasoning Policy

Official migration `185_group_reasoning_effort_policy.sql` adds
`groups.max_reasoning_effort` and `groups.reasoning_effort_mappings`. The
official persistence, handler, service, API-key hydration, cache, and frontend
behavior are merged as one capability.

At rollout, every existing group remains at the migration defaults: empty
maximum effort and empty mappings. Therefore, the new capability is present but
does not intentionally alter current request behavior. Administrators may
configure policies later through the upstream-supported controls.

Authentication cache payloads must contain both local video-generation access
and the official reasoning fields. Because both lineages expanded version 16
in incompatible ways, the integrated snapshot version advances to 17. Existing
version-16 cache data is refreshed rather than interpreted as a partial policy.
Policy enforcement is verified on HTTP chat/responses paths and each WebSocket
response-create turn, including cache invalidation after a group update.

## Model Pricing Description

### Data Model

The pricing-description migration is
`186_add_channel_model_pricing_description.sql`, not 185, because official
`v0.1.163` occupies migration 185. It adds a non-null, default-empty
`VARCHAR(500)` `description` column only to `channel_model_pricing`.

The primary in-memory pricing model gains `Description string`. One pricing row
can bind multiple model names, and all models bound to that row share its
description. No Ent generation is needed for this column because primary
pricing uses handwritten SQL.

`channel_account_stats_model_pricing` is unchanged. Shared in-memory types and
converters must explicitly omit the description for account-statistics rules so
an injected request value cannot be stored, echoed, or displayed there.

### Backend and API Flow

Primary-pricing select, scan, insert, update, and replacement queries carry the
description column. The admin request trims surrounding whitespace, accepts at
most 500 characters, and returns the normalized value. The user-facing
Available Channels DTO explicitly whitelists the field.

When LiteLLM display pricing fills otherwise missing price information, it must
preserve an authored description. The description does not affect whether the
fallback is eligible.

### Frontend Flow

Primary pricing DTOs and form state default descriptions to an empty string and
trim at serialization. `PricingEntryCard` receives an explicit control for the
description editor:

- Primary channel pricing renders a localized plain-text multiline editor with
  `maxlength=500` and a live character count.
- Account-statistics pricing does not render or serialize the editor.

The existing collapsed price summary does not change. In Available Channels,
non-empty descriptions render as escaped secondary text below the model header
and above billing/prices. Line breaks are preserved, long words wrap, blank
descriptions render no extra node, and descriptions remain outside search.

## Migration and Error Handling

The integrated release applies migrations in lexical order through:

1. `183_ops_ingress_reject_aggregates.sql`
2. `184_auth_cache_invalidation_outbox.sql`
3. `185_group_reasoning_effort_policy.sql`
4. `186_add_channel_model_pricing_description.sql`

Rehearse the sequence against a database copy whose migration ledger reflects
production. Require the expected filenames and checksums, successful first
startup, and successful second startup. Stop the release on any unexpected
migration checksum, failed lock, missing worker, data-loss signal, or state
comparison anomaly.

`184` auth-cache invalidation triggers and workers must be active, and the
outbox must not accumulate undelivered rows. `185` defaults must leave existing
groups behaviorally unchanged. `186` must create only the primary pricing
description column, with a default empty string and 500-character limit.

## Verification Design

### Source Gates

- Check source status, staged state, dump exclusion, and `git diff --check`.
- Verify exact tag object, peeled commit, tree, ancestry, and release delta.
- Verify stable Wire and Ent generation after the relevant source changes.
- Run backend unit tests plus focused tests for managed pricing, scheduler cache
  metadata, Gemini image count, hosted image tools, graceful shutdown flushing,
  group reasoning policy, auth-cache invalidation, Grok compact/content paths,
  OpenAI durable video, settlement, refund, and reconciliation.
- Run frontend lint, Vue typecheck, critical Vitest tests, and focused tests for
  group policy UI, mobile fixes, pricing-description forms, and supported-model
  popovers.
- Run migration shape, checksum, schema, and integration tests.
- Run the repository secret scan.

### Integration and Artifact Gates

Build the frontend before the embedded Go binary. Build the release from a
clean `git archive HEAD`, never from the dirty working directory, because the
deployment dump is not protected by the Docker ignore rules. Record a unique
image tag, image digest, archive hash, binary hash, frontend asset manifest,
source revision, and migration manifest.

Start an isolated stack against a production-ledger/database copy. Require two
successful starts after migrations, health checks, embedded asset verification,
and the route/billing/cache contracts listed above. Preserve the old production
image and Compose file until post-deploy acceptance completes.

### Production Preflight and Acceptance

Before deployment, take an immediate authoritative snapshot of database and
runtime state, validate a PostgreSQL 18 logical dump with `pg_restore -l`,
recheck disk space and container health, validate the merged Compose file
without exposing secrets, and confirm the old rollback image is locally
available.

After deployment, verify:

1. Application health, version, commit, image digest, and embedded frontend
   assets.
2. Migration records and checksums for 183 through 186.
3. Required worker startup, auth-cache invalidation delivery, and no growing
   outbox backlog.
4. Reasoning policy defaults remain empty for existing groups and local
   video-generation gating remains present.
5. Proxy, account/group, scheduled-plan, managed-pricing, channel-pricing, and
   video-settlement state match the immediate preflight snapshot unless a
   deliberate release test changed it.
6. Grok compact/content and OpenAI durable-video route contracts remain valid.
7. The Gemini image-count regression and hosted image-tool billing behavior are
   covered without creating a paid image solely for acceptance.
8. A primary pricing description saves, reloads, preserves line breaks in
   Available Channels, and a blank description has no display effect.
9. Recent logs contain no panic, fatal, migration, billing, shutdown-flush, or
   repeated cache-invalidation failure.

## Deployment and Rollback

Update only the unique application image reference in the customized production
Compose deployment and recreate only the application service with Compose
`--no-deps`. PostgreSQL and Redis are never recreated. Watch startup until
migrations, health checks, and workers are healthy before accepting traffic as
fully released.

Immediately restore the prior application image and Compose file if migrations
or checksums fail, the application remains unhealthy, a required worker is
missing, embedded assets are wrong, critical gateway/video/billing behavior
regresses, protected state changes unexpectedly, or the auth outbox grows
without delivery.

The ordinary rollback leaves migrations 183 through 186 in place because they
are additive and the old application ignores the added schema. A database
restore is a manual last-resort operation requiring proven database damage,
integrity verification.

## Acceptance Criteria

1. The merged source identifies the exact official `v0.1.163` tag while the
   customized lineage remains its first parent.
2. All existing local fixes and optimizations, including Gemini image-count
   billing, managed pricing, proxy support, durable video, settlements, and UI
   behavior, remain operational.
3. Official group reasoning policy, Grok, scheduler, billing, Redis ACL,
   shutdown, and mobile improvements are integrated and verified.
4. Existing groups keep default-empty reasoning settings and retain current
   request behavior through the release.
5. Primary pricing descriptions work end to end; account-statistics pricing
   never exposes or persists them.
6. Migrations 183 through 186 pass rehearsal, production validation, and
   idempotent second-start checks.
7. Production deployment recreates only the application container and has
   verified source, image, configuration, and database recovery artifacts.
8. The prior application image and Compose file can be restored without a
   database restore.
