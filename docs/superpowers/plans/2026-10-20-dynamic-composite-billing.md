# Dynamic Composite Billing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one Composite API key dynamically route OpenAI-compatible and existing provider requests to current accounts in concrete target groups, while preserving current sync-account billing and adding the Composite-group rate switch.

**Architecture:** Add persistent Composite-to-concrete-group references and optional route target groups. Resolve a target group before dispatch, install a request-scoped effective API key for existing scheduling/policy code, and retain the source Composite group separately for disabled dynamic-rate billing. Reuse the scheduler outbox, account selection, channel pricing, and settlement pipelines instead of creating parallel implementations.

**Tech Stack:** Go 1.27, Gin, Ent, PostgreSQL migrations, Redis-backed scheduler snapshots, Vue 3/TypeScript frontend, Vitest.

**Spec:** `docs/superpowers/specs/2026-10-20-dynamic-composite-billing-design.md`

## Global Constraints

- Existing copied-account Composite groups remain supported.
- Target groups must be concrete platforms; nested Composite targets are rejected.
- `dynamic_rate_billing_enabled` defaults to `false` and is meaningful only for Composite groups.
- Enabled dynamic billing preserves production sync behavior: `account_rate_multiplier × pricing_markup_factor` for managed accounts.
- Disabled dynamic billing uses the Composite group's user/group multiplier and peak settings, bypassing managed-account replacement.
- The existing OpenAI-compatible endpoints covered are Chat Completions, Responses, Embeddings, and Images; do not add legacy `/v1/completions` in this change.
- Account additions/removals use scheduler outbox/snapshot propagation; no per-request account database query.

## Review Focus

- A Composite with target rows whose targets are all disabled must fail closed rather than silently fall back to copied accounts; test in target resolver task.
- A target group reused by multiple Composite groups must retain independent target-reference priority and billing source context; test in target repository/resolver task.
- A sync-managed account must produce the same final user multiplier as production when dynamic billing is enabled, but must use the Composite multiplier when disabled; test in billing task.
- A request whose target group changes after an async task is accepted must settle from the persisted target/pricing snapshot; test in settlement task.
- OpenAI Chat Completions, Responses, Embeddings, and Images must resolve the same target-group context before their platform handlers run; test in gateway middleware task.

---

### Task 1: Persist target groups and Composite billing switch

**Files:**
- Create: `backend/migrations/246_composite_dynamic_targets_and_billing.sql`
- Modify: `backend/ent/schema/group.go`
- Create: `backend/ent/schema/composite_group_target.go`
- Modify: `backend/ent/schema/composite_model_route.go`
- Generate: `backend/ent/**`
- Test: `backend/ent/schema/composite_dynamic_schema_test.go`

**Interfaces:**
- Produces Ent entities for `CompositeGroupTarget`, nullable `CompositeModelRoute.TargetGroupID`, and `Group.DynamicRateBillingEnabled`.
- The migration creates `composite_group_targets` with soft-delete support, foreign keys, active uniqueness on `(composite_group_id, target_group_id)`, and indexes for enabled/priority lookup.

- [ ] **Step 1: Write failing Ent schema and migration contract test**
  - Assert the target entity has source/target IDs, ordering, enabled, and soft-delete fields.
  - Assert the billing flag defaults false and route target group is optional/nillable.
  - Assert the migration declares the table, partial uniqueness index, and target-group column.
- [ ] **Step 2: Run the focused integration tests and verify they fail for missing schema/API**
  - Run: `go test ./ent/schema -run 'TestCompositeDynamicSchemas' -count=1`.
  - Expected: fail before schema generation because target schema/fields are missing.
- [ ] **Step 3: Add migration and Ent schema**
  - Add the group boolean with a false default.
  - Add target-reference fields/edges and nullable route target-group field/edge.
  - Preserve existing route rows with null target groups.
- [ ] **Step 4: Generate Ent code and rerun focused tests**
  - Run: `make generate` from `backend`.
  - Run: `go test ./ent/schema -run 'TestCompositeDynamicSchemas' -count=1`.
- [ ] **Step 5: Commit**
  - Commit: `feat: persist dynamic composite targets and billing flag`

### Task 2: Add target-reference service, repository, and admin API

**Files:**
- Modify: `backend/internal/service/group_service.go`
- Create/modify: `backend/internal/service/composite_group_target.go`
- Create: `backend/internal/repository/composite_group_target_repo.go`
- Modify: `backend/internal/repository/wire.go`
- Modify: `backend/internal/service/admin_service.go`
- Modify: `backend/internal/service/admin_group.go`
- Modify: `backend/internal/handler/admin/group_handler.go`
- Modify: `backend/internal/server/routes/admin.go`
- Test: `backend/internal/service/admin_service_composite_group_test.go`
- Test: `backend/internal/handler/admin/admin_basic_handlers_test.go`

**Interfaces:**
- Add `CompositeGroupTarget` service type and repository methods:
  `ListByCompositeGroup`, `Create`, `Update`, and `Delete`.
- Add AdminService methods for listing, creating, updating priority/enabled, and deleting target references.
- Expose `/api/v1/admin/groups/:id/composite-targets` CRUD endpoints.

- [ ] **Step 1: Write failing service tests**
  - Validate source is Composite, target is concrete and active at creation, and target is not self/nested.
  - Assert lower priority wins and equal priority is ordered by target ID.
  - Assert deleting or disabling a target does not mutate copied account bindings.
- [ ] **Step 2: Run focused service tests and verify RED**
  - Run: `go test -tags=unit ./internal/service -run 'TestAdminService_CompositeTarget' -count=1`.
- [ ] **Step 3: Implement repository/service validation and outbox/cache invalidation**
  - Permit a concrete target to be shared by multiple Composites.
  - Enqueue the existing group-change invalidation path after target-reference changes.
  - Return configured-but-unusable target references to the resolver instead of silently treating them as absent.
- [ ] **Step 4: Add handlers/routes and rerun service/handler tests**
  - Run: `go test -tags=unit ./internal/service ./internal/handler/admin -run 'Test(AdminService_CompositeTarget|.*CompositeTarget)' -count=1`.
- [ ] **Step 5: Commit**
  - Commit: `feat: manage dynamic composite target groups`

### Task 3: Extend route resolution with target groups

**Files:**
- Modify: `backend/internal/service/composite_model_route.go`
- Modify: `backend/internal/service/composite_route_resolver.go`
- Modify: `backend/internal/service/composite_platform.go`
- Modify: `backend/internal/repository/composite_model_route_repo.go`
- Modify: `backend/internal/service/gateway_service.go` or the existing group repository seam as needed
- Test: `backend/internal/service/composite_route_resolver_test.go`
- Test: `backend/internal/service/composite_platform_test.go`

**Interfaces:**
- Extend route/input/decision types with nullable `TargetGroupID`.
- Resolver receives a target-reference lookup seam and returns `TargetGroupID`, target metadata, and the existing platform/model decision.
- Explicit routes validate that the target is an enabled reference and platform-compatible.

- [ ] **Step 1: Write failing resolver tests**
  - Explicit route selects its configured target group.
  - Detector selects the enabled same-platform target with lowest priority, then lowest ID.
  - Missing/disabled/deleted/mismatched targets fail closed.
  - Zero target-reference rows preserve legacy platform-only resolution.
  - Configured but unusable target rows do not fall back to copied accounts.
- [ ] **Step 2: Run resolver tests and verify RED**
  - Run: `go test -tags=unit ./internal/service -run 'TestCompositeRouteResolver.*Target|TestComposite.*Target' -count=1`.
- [ ] **Step 3: Implement target-aware resolution and persistence mapping**
  - Keep null target-group routes backward compatible.
  - Carry source Composite group ID separately from target group ID in the decision/context.
- [ ] **Step 4: Run the focused resolver suite and the existing composite suite**
  - Run: `go test -tags=unit ./internal/service -run 'Test(Composite|.*CompositeRoute)' -count=1`.
- [ ] **Step 5: Commit**
  - Commit: `feat: resolve composite routes to target groups`

### Task 4: Install effective target API keys in gateway middleware

**Files:**
- Modify: `backend/internal/server/routes/gateway.go`
- Modify: `backend/internal/server/middleware/api_key_auth.go`
- Modify: `backend/internal/service/composite_platform.go`
- Modify: `backend/internal/pkg/ctxkey/ctxkey.go`
- Modify: `backend/internal/service/api_key_auth_cache_impl.go` if cache invalidation requires it
- Test: `backend/internal/server/routes/composite_platform_test.go`
- Test: `backend/internal/server/routes/gateway_test.go`
- Test: `backend/internal/server/middleware/api_key_auth_test.go`

**Interfaces:**
- Add request-context values for source Composite group, resolved target group, target route, and billing-rate group.
- Add a middleware helper that shallow-copies the authenticated API key, preserving key identity/quota/rate-limit fields while replacing `GroupID`/`Group` with the target group for downstream provider work.

- [ ] **Step 1: Write failing middleware tests**
  - OpenAI Chat Completions, Responses, Embeddings, and Images install the same target group context.
  - The original API key ID and key-level quota remain unchanged.
  - Source Composite group remains available for billing-rate selection.
  - A target subscription/policy is evaluated after target installation.
- [ ] **Step 2: Run the focused middleware tests and verify RED**
  - Run: `go test -tags=unit ./internal/server/routes ./internal/server/middleware -run 'Test.*Composite.*Target|Test.*Effective.*APIKey' -count=1`.
- [ ] **Step 3: Implement target resolution/effective-key installation**
  - Keep model extraction and JSON rewriting behavior for all existing route types.
  - Defer only group-dependent billing/subscription checks until target installation; keep API-key validity, user, IP, expiration, and key-level quota checks in authentication.
  - Preserve legacy behavior when no target references exist.
- [ ] **Step 4: Run OpenAI compatibility and gateway route tests**
  - Run: `go test -tags=unit ./internal/server/routes ./internal/handler -run 'Test.*Composite.*(Chat|Response|Embedding|Image|Target)|TestGatewayRoutes' -count=1`.
- [ ] **Step 5: Commit**
  - Commit: `feat: install composite target group context`

### Task 5: Implement the dynamic-rate billing switch

**Files:**
- Modify: `backend/internal/service/gateway_request_pricing.go`
- Modify: `backend/internal/service/gateway_usage_billing.go`
- Modify: `backend/internal/service/openai_gateway_usage.go`
- Modify: `backend/internal/service/group.go`
- Modify: `backend/internal/service/openai_profit_control.go`
- Modify: `backend/internal/service/gateway_profit_control.go`
- Modify: `backend/internal/service/openai_video.go` and video pricing helpers as required
- Test: `backend/internal/service/gateway_record_usage_test.go`
- Test: `backend/internal/service/openai_gateway_record_usage_test.go`
- Test: `backend/internal/service/account_billing_rate_multiplier_test.go`
- Test: `backend/internal/service/group_peak_rate_test.go`

**Interfaces:**
- Add one resolver for the customer-rate group and managed-account override policy so Gateway and OpenAI Gateway use identical rules.
- Preserve `UsageLog.RateMultiplier` as the final user multiplier and `AccountRateMultiplier` as the independent account snapshot.

- [ ] **Step 1: Write failing billing tests**
  - Enabled + sync-managed: final rate is account rate × sync markup, with Composite peak applied once.
  - Enabled + ordinary account: final rate is Composite user/group rate with Composite peak.
  - Disabled + sync-managed: final rate is Composite user/group rate with Composite peak, not account × sync markup.
  - User override and peak boundary behavior remain correct.
  - OpenAI and shared gateway paths produce identical results.
- [ ] **Step 2: Run billing tests and verify RED**
  - Run: `go test -tags=unit ./internal/service -run 'Test.*(ManagedAccount|DynamicRate|Peak|RecordUsage)' -count=1`.
- [ ] **Step 3: Implement the minimal shared multiplier selection**
  - Read the source Composite billing group from context when the effective API key points at a target.
  - Gate `effectiveRequestRateMultiplier` behind `dynamic_rate_billing_enabled`.
  - Apply the selected Composite peak exactly once at the frozen pricing instant.
  - Keep account quota/statistics calculation unchanged.
- [ ] **Step 4: Run focused billing and profit-control tests**
  - Run: `go test -tags=unit ./internal/service -run 'Test.*(Billing|ProfitControl|Peak|ManagedAccount)' -count=1`.
- [ ] **Step 5: Commit**
  - Commit: `feat: add composite dynamic billing rate switch`

### Task 6: Persist target ownership through model listing and async tasks

**Files:**
- Modify: `backend/internal/handler/gateway_models.go` and related model-list services
- Modify: `backend/internal/service/openai_codex_models_service.go` as needed
- Modify: `backend/internal/service/video_task_service.go`
- Modify: `backend/internal/service/video_task_settlement.go`
- Modify: `backend/internal/repository/video_task_repo.go` only if a new target field is required
- Test: `backend/internal/handler/gateway_models_test.go`
- Test: `backend/internal/service/video_task_service_test.go`
- Test: `backend/internal/service/video_task_create_settlement_test.go`

**Interfaces:**
- Model listing aggregates enabled target groups, deduplicates public model IDs case-insensitively, and keeps deterministic target metadata.
- Async tasks persist the resolved target group and existing pricing snapshot at creation.

- [ ] **Step 1: Write failing model-list and settlement tests**
  - Two target groups contribute one deduplicated model.
  - Adding/removing an account in a target group changes the next model-list/scheduling result after snapshot propagation.
  - A task settles with its stored target and quote even after target configuration changes.
- [ ] **Step 2: Run focused tests and verify RED**
  - Run: `go test -tags=unit ./internal/handler ./internal/service -run 'Test.*(Composite.*Models|VideoTask.*Target|Settlement.*Target)' -count=1`.
- [ ] **Step 3: Implement target-aware model listing and task persistence**
  - Preserve legacy copied-account behavior when no target references exist.
  - Do not re-resolve a Composite model during settlement.
- [ ] **Step 4: Run focused tests and repository tests**
  - Run: `go test -tags=unit ./internal/handler ./internal/service ./internal/repository -run 'Test.*(Composite|VideoTask|Settlement)' -count=1`.
- [ ] **Step 5: Commit**
  - Commit: `feat: persist composite target ownership for models and tasks`

### Task 7: Add frontend administration and documentation

**Files:**
- Modify: `frontend/src/types/index.ts`
- Modify: `frontend/src/api/admin/groups.ts`
- Modify: `frontend/src/views/admin/GroupsView.vue`
- Modify: `frontend/src/i18n/locales/en/admin/overview.ts`
- Modify: `frontend/src/i18n/locales/zh/admin/overview.ts`
- Test: `frontend/src/views/admin/__tests__/GroupsView.compositePlatforms.spec.ts`
- Test: new target-reference/route form tests alongside existing GroupsView tests
- Modify: `docs/COMPOSITE_GROUPS.md`

**Interfaces:**
- Admin UI lists/adds/updates/removes target references and edits the dynamic-rate switch.
- Route editor can choose a concrete target group and displays target platform/status.
- Existing static account-copy controls remain labeled and functional.

- [ ] **Step 1: Write failing frontend tests**
  - Composite-only controls appear for Composite groups.
  - Composite target options exclude Composite groups and the current group.
  - Dynamic-rate switch and target route ID are sent in request payloads.
- [ ] **Step 2: Run frontend focused tests and verify RED**
  - Run: `npm test -- --run frontend/src/views/admin/__tests__/GroupsView.compositePlatforms.spec.ts` from `frontend`.
- [ ] **Step 3: Implement API types, admin controls, and translations**
  - Keep labels explicit that static account copying is legacy and dynamic targets update automatically.
- [ ] **Step 4: Run frontend lint/typecheck/tests**
  - Run the repository's frontend test and typecheck commands from `frontend`.
- [ ] **Step 5: Commit**
  - Commit: `feat: expose dynamic composite targets in admin`

### Task 8: Full verification and branch review

**Files:**
- Modify only files required by failing verification.

- [ ] **Step 1: Run backend generation and full tests**
  - Run: `make generate` and `go test ./...` from `backend`.
- [ ] **Step 2: Run frontend checks**
  - Run the existing frontend lint, typecheck, and test commands.
- [ ] **Step 3: Inspect the final diff**
  - Confirm no credentials, production data, generated artifacts outside Ent, or unrelated formatting changes are included.
  - Confirm the branch still preserves static Composite account-copy behavior.
- [ ] **Step 4: Commit any verification-only fixes**
  - Commit: `test: verify dynamic composite billing integration`
