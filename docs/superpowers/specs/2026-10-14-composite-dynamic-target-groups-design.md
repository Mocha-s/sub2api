# Composite Dynamic Target Groups

## Goal

Allow one API key bound to a Composite group to dynamically reference several
concrete provider groups. A request is routed to one target group, and that
target group owns the account pool and all group-level policy: pricing
multiplier, subscription, quotas, RPM, model permissions, and provider
configuration.

The API key remains the owner of key-level controls such as expiration, API-key
quota, and API-key rate limits.

## Existing Constraints

- `api_keys.group_id` is singular, so a key cannot directly bind to multiple
  groups.
- Composite route resolution currently returns a target platform and upstream
  model, but no target group.
- Account selection already queries by `groupID` and platform.
- Billing and policy checks read `apiKey.Group` and `apiKey.GroupID`.
- API-key authentication performs source-group checks before the Composite
  route middleware runs.
- Composite groups currently support copied account bindings. Those bindings
  must remain usable during migration.

## Terminology

- **Source group**: the Composite group bound to the API key.
- **Target group**: a concrete provider group selected for the current request.
- **Target reference**: an active link from a Composite group to a concrete
  target group.
- **Effective API key**: a request-scoped view of the authenticated key whose
  `Group` and `GroupID` represent the target group while preserving the real
  key identity and key-level limits.

## Data Model

### Composite target references

Add `composite_group_targets`:

- `id`
- `composite_group_id` foreign key to `groups.id`
- `target_group_id` foreign key to `groups.id`
- `priority` lower values first
- `enabled`
- `created_at`, `updated_at`, `deleted_at`

Add an active unique index on `(composite_group_id, target_group_id)`.

Validation rules:

- The source group must have platform `composite`.
- The target group must have a concrete platform.
- A target group cannot itself be `composite`.
- A target group must exist; runtime resolution additionally requires it to be
  active.
- A target reference can be disabled without deleting route history.

### Composite model routes

Add nullable `target_group_id` to `composite_model_routes`.

- New explicit routes should specify `target_group_id`.
- `target_platform` remains for response compatibility and validation. It must
  match the target group's concrete platform when `target_group_id` is set.
- Existing routes with a null target group continue to use the existing
  platform-only behavior.

The route input and decision types gain `TargetGroupID`. The decision also
returns the resolved target group metadata when the resolver has loaded it.

## Resolution

Resolution order remains:

1. Exact explicit route.
2. Prefix explicit route.
3. Endpoint specificity, prefix length, priority, and route ID tie-breakers.
4. Built-in model platform detection.

For an explicit route with `target_group_id`, the resolver verifies that the
target is an enabled reference of the Composite source and that the target
group platform matches the route.

For built-in detection, the resolver selects enabled target references whose
platform matches the detected platform, ordered by reference priority and ID.

If no target reference matches, the request fails closed with the existing no
available route error. It must not silently use a different provider group.

For backward compatibility:

- A Composite with no target references keeps its existing platform-only route
  behavior and uses its directly bound accounts.
- Existing routes with no target group continue to resolve by platform.
- New dynamic configuration is opt-in until target references are added.

## Request Flow

The gateway flow becomes:

```text
API key authentication
  -> Composite model/endpoint resolution
  -> target group loading and validation
  -> effective API key/context installation
  -> target-group policy checks
  -> existing handler and service flow
```

### Effective API key

After a target group is selected, the Composite middleware creates a shallow
request-scoped copy of the authenticated API key:

- Keep `ID`, `UserID`, key quota, key rate limits, expiration, and owner.
- Set `GroupID` to the target group ID.
- Set `Group` to the hydrated target group.
- Keep the original API key and source group in dedicated request context values
  for audit and error logging.

The downstream `ContextKeyAPIKey` contains the effective key. This preserves
existing call sites that already use `apiKey.Group` and `apiKey.GroupID` for
account selection, pricing, subscriptions, RPM, and usage logs without adding
parallel target-group arguments throughout the gateway.

The request context also stores:

- source Composite group ID
- target group ID
- route ID when an explicit route matched
- route source (`route` or `detector`)
- resolved target platform and upstream model

### Authentication and policy timing

The current authentication middleware performs source-group billing checks
before target resolution. For Composite keys it must defer group-dependent
checks until the target group is installed.

It still enforces immediately:

- API key validity and status
- user status
- IP restrictions
- API key expiration
- API key quota status

After target resolution, the target policy stage supplies the target
subscription, if any, and performs target-group availability and subscription
validation. Existing handlers then run their normal billing eligibility checks
against the effective target group. This avoids changing the billing formulas
and ensures the target group is used by all existing checks.

The source Composite group remains the authorization entry point. Target group
references are administrator-controlled capabilities. Subscription-type target
groups still require an active subscription for the user. Key-level quota and
rate limits remain shared across all target groups because they belong to one
API key.

## Scheduling and Billing

No new scheduler abstraction is required.

With the effective API key installed:

- account selection receives the target `GroupID`;
- sticky-session keys are scoped to the target group;
- model routing and channel restrictions resolve against the target group;
- user-specific group multipliers use the target group ID;
- group multiplier and peak multiplier come from the target group;
- channel pricing and model mappings use the target group;
- usage logs record the target group ID;
- user-platform quota uses the resolved concrete platform;
- subscription usage is charged to the target group's subscription;
- image and video pricing use the target group's settings.

The API key ID remains unchanged, so one key's quota and rate-limit windows
aggregate usage across all target groups.

Video and other asynchronous tasks must persist the resolved target group ID at
creation time. Settlement and replay must use that stored group rather than
re-resolving the Composite model later.

## Model Listing

`GET /v1/models` has no request model and therefore cannot select one target
group through normal route resolution.

The Composite model-list path should aggregate model candidates from enabled
target groups, deduplicate by public model ID, and preserve the existing
provider mapping metadata. If no target references exist, it keeps the current
Composite behavior.

Read-only task endpoints without a model do not need target policy resolution;
they use the persisted task ownership and the API key's key-level checks.

## Admin API and UI

Add target-reference operations under the existing Composite group API:

- list target references
- add target group
- update priority/enabled state
- remove target group

Update the route editor to choose a concrete target group. The UI should show
the target group's platform and current status, and reject Composite targets.

Keep the existing account-copy controls during migration, but label them as
legacy/static account binding. New dynamic configurations should use target
references instead.

## Error Handling and Security

- Missing, deleted, disabled, or platform-mismatched target groups fail closed.
- Nested Composite references are rejected at write time and runtime.
- A target group status change takes effect on the next request; auth/group
  caches must be invalidated when references or target group status change.
- Failed route resolution must not fall back to an unrelated target group.
- Audit and usage diagnostics retain both source and target group IDs where the
  existing log model permits it; at minimum the source is recoverable through
  the API key and the target is recorded in usage logs.

## Implementation Boundaries

Expected code areas:

- migration and `ent/schema` for target references and route target IDs;
- composite route repository/resolver and decision context;
- gateway route middleware and API-key authentication deferral;
- admin group service/handler and Composite route DTOs;
- frontend Composite target-reference and route editors;
- model-list aggregation;
- async video/image task persistence and settlement verification;
- generated Ent and Wire files as required by repository conventions.

The existing scheduler and billing calculation functions should be reused. Do
not introduce a second account-selection or billing pipeline.

## Verification

Unit tests should cover:

- target-reference validation and Composite nesting rejection;
- explicit route target-group resolution;
- detector resolution by target platform and priority;
- disabled/deleted/mismatched target failures;
- effective API key context installation;
- target subscription and multiplier selection;
- key quota aggregation across two target groups;
- target-group RPM and user-group override selection;
- model-list deduplication;
- video/image target-group persistence and settlement.

Integration tests should cover:

- one key calling two target groups without copied account bindings;
- adding an account to a target group becoming effective without editing the
  Composite group;
- changing a target group's multiplier affecting the next request;
- target subscription limits and source Composite subscription isolation;
- cache invalidation after target reference or group status changes.

## Non-Goals

- Automatic quality-based routing among multiple target groups.
- Per-request arbitrary group selection supplied by the client.
- Nested Composite groups.
- Removing existing Composite account-copy behavior in the first release.
- Changing API-key quota semantics from one shared quota to per-target quotas.
