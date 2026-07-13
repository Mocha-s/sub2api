# Per-Request Video Settlement And UI Design

## Context

Video tasks with channel `billing_mode=video` use `VideoTaskSettlement` and correctly reserve, capture, and refund funding. A video task with channel `billing_mode=per_request` instead calls the legacy `OpenAIGatewayService.RecordUsage` after upstream acceptance. The task does not retain the resulting usage-log ID and its terminal reconciliation only invokes the settlement service, which has no settlement row to reverse.

Production probe `task_32d91ebd2001e678fca0ad8310067765` demonstrated the gap. Upstream charged and refunded `video-ds-2.0-fast`, while the local task became `failed` and the local customer balance retained a `6.9225 USD` charge. The corresponding local usage row is `70697` and has no task link or refund metadata.

The same production probe also revealed three display gaps for a correctly billed `seedance-2.0-mini-480p` video:

- The `video` billing mode is labeled as `按次(视频)` rather than per-second video billing.
- The usage cost tooltip falls back to `单次价格`, displaying the total gross cost rather than the per-second calculation.
- The available-channels API sends video-price fields, but the frontend types and model popover omit them.

## Goals

- Route newly created priced video tasks, including `per_request` video models, through durable settlement and terminal refunds.
- Preserve legacy billing semantics for requests with no channel pricing selection.
- Compensate only the single confirmed probe charge using a guarded, auditable transaction.
- Provide accurate per-second video billing labels and cost calculations in usage and available-channel UI.
- Produce a read-only report of other historical failed per-request video charges without changing them.

## Non-Goals

- Do not automatically compensate existing historical rows other than the confirmed probe.
- Do not change token, image, subscription, API-key quota, account quota, or platform-quota rules beyond using the existing settlement effects.
- Do not delay charging until task completion.
- Do not alter upstream video request or polling protocol behavior.

## Settlement Design

### Unified priced-video admission

`VideoTaskService.Create` resolves channel pricing before task creation. When the selection has either `billing_mode=video` or `billing_mode=per_request`, it creates an immutable settlement quote and calls the existing settlement prepare/reserve flow before upstream submission.

For `video` mode, quote construction remains duration-based: normalized seconds, resolution tier, output count, USD per second, group multiplier, and account-stat price are snapshotted exactly as today.

For `per_request` mode, quote construction uses the existing resolved per-request model price and customer/account multipliers. Its effective dimensions record one output and preserve any client-supplied video metadata for audit, but no synthetic per-second price is exposed. It uses the same balance/subscription/API-key/account/platform effects as existing video settlement.

Once upstream accepts, capture atomically creates and links the usage log, records `capture`, and writes immutable identity/effect snapshots. `video_tasks.usage_log_id` is always populated for priced tasks.

### Terminal behavior

Provider submission failures release an existing reservation. Terminal `failed`, `cancelled`, and `expired` tasks reconcile through the persisted settlement and produce one fenced `refund` event. The refund reverses exactly the effects captured by that settlement, fills usage refund fields, and schedules reporting/cache invalidation outbox jobs.

No legacy `RecordVideoTaskSubmission` call occurs for tasks with a priced settlement quote. Requests that have no resolvable channel pricing keep the current legacy path so this change does not invent prices or mutate unpriced traffic.

### Probe compensation and history audit

A separate guarded transaction compensates the known probe only after revalidating all of the following:

- Task `task_32d91ebd2001e678fca0ad8310067765` is terminal `failed`.
- Usage log `70697` belongs to user 24, API key 60, account 221, model `video-ds-2.0-fast`, gross `65`, actual `6.9225`, and has zero refund fields.
- No settlement, refund event, or prior compensation marker is present.

It credits `6.9225 USD` to user 24, records usage refunds and a legacy compensation audit row/event, and enqueues reporting/cache work. It does not decrement unreconstructable API-key, account, or platform windows.

The historical audit is read-only. It reports failed `per_request` video tasks whose task-to-usage link can be reconstructed and whose usage refund fields remain zero. It emits candidate task ID, usage ID, user/API key/account IDs, gross/customer/account costs, and exclusions. Applying any later historical compensation requires explicit approval and a separate guarded transaction.

## UI Design

### Usage views

The billing-mode label for `video` becomes `按秒（视频）` in Chinese and `Per-second video` in English. The shared usage cost tooltip adds a dedicated video branch before the generic per-request fallback. It shows:

- Resolution, duration, and output count.
- Per-second gross unit price calculated as `total_cost / (video_duration_seconds * video_count)`.
- Explicit calculation: `unit price x seconds x videos = gross cost`.
- Customer rate multiplier, customer gross/net/refund amounts, and account multiplier/account gross-net-refund amounts using the existing refund helpers.

The user usage page keeps account-only values hidden; the admin view continues to show them.

### Available channels

The user channel API type definitions gain default video USD/s, default seconds, allowed seconds, and interval USD/s fields that the backend already returns. `SupportedModelChip` gains a video branch that shows:

- `Per-second video` billing mode.
- Default USD/s price.
- Default duration and allowed duration policy (`any duration` for an empty allowed list).
- Resolution/tier overrides in USD/s.

The popover remains compact and follows the existing token/image/per-request layout and localization conventions.

## Tests And Verification

- Unit/service tests cover per-request quote construction, reserve/capture, submit failure release, async failed refund, repeated terminal polling idempotency, and absence of legacy usage recording for priced tasks.
- Repository integration tests verify task-to-usage linking and exact refund fields/effects for per-request video.
- Guarded compensation SQL is dry-run on an isolated PostgreSQL fixture, then reviewed before production execution.
- Frontend tests cover video labels, tooltip calculation, unavailable/empty duration policy, default USD/s, and tier USD/s rendering.
- Run Ent/Wire generation, backend unit suite, targeted PostgreSQL/Redis integrations, frontend lint/typecheck/critical tests, and production-local probe verification.
