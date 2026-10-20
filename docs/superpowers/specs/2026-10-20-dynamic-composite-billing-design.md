# Dynamic Composite Billing

## Goal

Allow one API key bound to a Composite group to route dynamically to several
concrete provider groups while preserving the production billing behavior:
managed sync accounts use `account_rate_multiplier × pricing_markup_factor`,
and non-managed accounts use the Composite group's effective multiplier.

## Decisions

- A Composite group stores references to concrete target groups; it does not
  copy their accounts for the dynamic path.
- Target-group account changes become effective through the existing scheduler
  outbox/snapshot flow, normally within the configured outbox polling interval.
- Existing copied-account Composite groups remain supported unchanged.
- `dynamic_rate_billing_enabled` is a Composite-group flag and defaults to
  `false`.
- When the flag is enabled, managed sync accounts use the production formula:
  - `api-pricing-sync` account: account multiplier × sync markup factor;
  - other accounts: Composite group's user override/group multiplier.
- When the flag is disabled, every account uses the Composite group's user
  override/group multiplier; the managed-account replacement is bypassed.
- In both modes, the Composite group's peak factor is applied at the frozen
  request pricing time, once. The target group's multiplier and peak do not
  replace the customer-rate rules above.
- Target-group settings still control account pool, subscription, quotas, RPM,
  model permissions, provider configuration, channel pricing, and model mapping.
- Account quota and account statistics continue to use the selected account's
  account multiplier.
- API-key quota/rate-limit identity remains the original API key and is shared
  across all target groups.
- Each target group uses the existing single `Group.Platform` value; target
  platform validation compares against that value.
- Target-group subscription, group RPM, group quotas, model restrictions, and
  provider policies apply after target resolution; source Composite group-level
  limits do not stack. API-key limits remain shared across targets.
- Composite targets cannot be Composite groups themselves.

## Data Model

Add `composite_group_targets` with:

- `id`
- `composite_group_id`
- `target_group_id`
- `priority`
- `enabled`
- `created_at`, `updated_at`, `deleted_at`

Add `groups.dynamic_rate_billing_enabled` with a default of `false`; it is
meaningful only for Composite groups.

Add nullable `target_group_id` to `composite_model_routes`. Existing routes
with a null target group retain platform-only behavior. New dynamic routes
must reference an enabled target reference, and their target platform must
match the referenced concrete group.

Target references are configured through new admin operations under the
existing group-management surface, with a frontend editor for Composite
groups. Concrete target groups are global reusable groups; the same target may
be referenced by multiple Composite groups. No tenant-owner constraint is
added because groups are not tenant-owned in the current domain model.

Target priority uses lower values first; equal priorities use lower target ID.
Only a Composite with zero target-reference rows falls back to legacy copied
account bindings. If references exist but all are disabled, deleted, inactive,
or otherwise unusable, requests fail closed.

## Routing

1. API-key authentication validates the source Composite group and key-level
   controls.
2. Composite route resolution chooses an explicit target group when present;
   otherwise it chooses an enabled target reference matching the detected
   platform by priority.
3. The request context retains source Composite group ID, target group ID,
   route ID/source, target platform, and upstream model.
4. A shallow effective API-key copy exposes the target group to existing
   scheduling, channel, model restriction, subscription, and provider code.
5. When dynamic billing is disabled, the original Composite group is retained
   separately as the billing-rate group; it must not be lost when the effective
   key is installed.

Missing, disabled, deleted, nested, or platform-mismatched targets fail closed.
An active target with no currently schedulable accounts resolves normally and
uses the existing no-available-account error; provider credentials/channels are
not preflighted at reference creation.

The existing OpenAI-compatible surface remains available through Composite
routing: `POST /v1/chat/completions`, `POST /v1/responses`, Responses WebSocket,
`POST /v1/embeddings`, and image generation/edit endpoints. Built-in model
detection selects OpenAI for `gpt-*`, `o*`, `codex-*`, embedding/image model
families, and `openai/*`. Legacy `POST /v1/completions` is not added.

## Dynamic Updates

Target-group account additions, removals, status changes, rate changes, and
credential pricing-sync changes reuse the existing scheduler outbox and target
group snapshot invalidation. No per-request database account query is added.

Model listing aggregates enabled target groups and deduplicates public models.
Existing Composite account-copy behavior remains the fallback when no dynamic
target references are configured.

Target-reference writes and target-group status changes invalidate the relevant
auth/model-list caches through the existing group-change notification path;
the next request must observe the new reference state. Account-pool changes
may take the existing outbox polling interval to appear; target-reference
creation does not synchronously warm a snapshot. Model IDs are
deduplicated case-insensitively; the winning metadata follows target priority,
target ID, and the existing model ordering rules.

## Billing Invariants

- Enabled dynamic billing must produce the same final `rate_multiplier` as the
  current production path for the same selected account and target group.
- Disabled dynamic billing must use the Composite group's user/group rate and
  peak settings even when the selected account is sync-managed.
- Enabled dynamic billing must preserve the production sync-managed rate
  formula; ordinary accounts use the Composite group's user/group rate.
- `account_rate_multiplier` remains an independent snapshot used for account
  quota/statistics calculations.
- Usage records must retain the target group ID; the source Composite group is
  recoverable from the API key/context audit data.
- Async video/image task creation persists the resolved target group and the
  existing immutable pricing snapshot, and uses both during settlement instead
  of resolving the Composite model again. Later target disable/delete or rate
  changes do not retroactively reprice an accepted task.

## Compatibility and Non-Goals

- No removal or migration of existing static Composite account bindings.
- The billing switch also applies to legacy copied-account Composites. With no
  target references, copied accounts remain the selected pool and the
  Composite group remains the billing-rate group when the switch is off.
- No nested Composite groups.
- No client-supplied arbitrary target-group selection.
- No quality-based automatic routing among target groups beyond configured
  priority and model-platform matching.
- No second account-selection or billing implementation.

## Verification

Tests must cover target-reference validation, explicit and detected target
resolution, dynamic account updates, effective API-key context, disabled and
enabled billing formulas, sync-managed and ordinary accounts, user overrides,
peak rates, API-key quota aggregation, model listing deduplication, and async
settlement target persistence.
