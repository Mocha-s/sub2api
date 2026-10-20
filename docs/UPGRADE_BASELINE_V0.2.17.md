# Sub2API v0.2.17 Upgrade Baseline

This document is the authoritative baseline for future upstream upgrades.

## Baseline Identity

- Application version: `0.2.17`
- Baseline commit: `32255f71c`
- Baseline tag: `baseline/v0.2.17-historical-fixes-sync`
- Supersedes tag: `baseline/v0.2.17-historical-fixes` (`c9012835e`)
- Upstream release included: `refs/tags/v0.2.17-upstream`
- Production image: `weishaw/sub2api:upgrade-v0.2.17-32255f71c`
- Production entrypoint: `https://api.aigcdo.top`
- Production host: `186.244.215.254`

The baseline commit includes the official `v0.2.17` implementation plus the
local historical fixes and optimizations listed below. The source of truth is
the commit and tag, not the branch name.

## Upgrade Rule

For every future upstream upgrade:

1. Start from `baseline/v0.2.17-historical-fixes-sync`.
2. Merge the new official upstream tag or release commit.
3. Compare the merge against this baseline with:

   ```bash
   git diff --stat baseline/v0.2.17-historical-fixes-sync...HEAD
   git diff --name-status baseline/v0.2.17-historical-fixes-sync...HEAD
   ```

4. When upstream already contains one of the historical fixes, keep the
   upstream implementation and verify its behavior; do not blindly replay the
   older local patch.
5. When upstream does not contain a baseline behavior, preserve it and add a
   regression test if the upgrade changes the surrounding code.
6. Regenerate Ent and Wire code when their sources or dependency injection
   change.
7. Run the baseline regression checks before building or deploying.

## Historical Behavior Preserved

### Durable Video and Seedance

- Durable OpenAI-compatible video task persistence and task lifecycle.
- `/v1/videos` and video-generations compatibility routes.
- Multipart video request parsing and compatibility field allowlists.
- Client `size` normalization to `aspect_ratio`, including `9:16`.
- Client aspect-ratio preservation for Seedance and other adapters.
- Seedance native Ark request/response compatibility and model mapping.
- Composite-group video routing and alias preservation.
- Per-request video pricing, duration validation, and pricing snapshots.
- Reservation, capture, release, refund, and settlement reconciliation.
- Provider submission failure handling without stranded charges.
- Detached provider work context and bounded local task/poller deadlines.
- Poller lease fencing, retry/backoff, task expiry, and terminal settlement.
- Video pricing form types, duration controls, resolution tiers, and display.

Representative history:

- `ff61df230` durable OpenAI video tasks
- `ed8291ff2` per-second settlement and refunds
- `c7297fae1` priced per-request settlement
- `0d0e598bb` composite video aliases
- `d255029bf`, `2c953fddb`, `45dc3876b`, `fd93aabf4` multipart, size,
  boolean, and aspect-ratio compatibility
- `15a6c6e6f` durable video and Banana preservation
- `cf8aa9712` preserved upgrade integration

### Gemini and Nano Banana

- Nano Banana model detection and Gemini-to-OpenAI image bridge.
- Data URL and inline image conversion for image generation and edits.
- OpenAI-compatible response conversion back to Gemini response parts.
- Gemini image output preservation in compatibility paths.
- Gemini explicit-cache pricing, failure recovery, and account scheduling.
- Gemini error policy single-check behavior and Vertex account handling.
- Antigravity image/tool-result compatibility and composite `/v1beta` routing.

Representative history:

- `dfff8bc00` route Nano Banana images through OpenAI
- `ba2a71733` preserve images from Gemini tool results
- `d5a802f84` explicit context cache support
- `26b31a83e` explicit cache reservation and rollback behavior
- `14cd58717`, `20c4bf5ae`, `89ad4c2ca` Gemini error and Vertex capacity
  handling
- `feab6d80d` Antigravity media and composite routing compatibility

### Images and Billing

- OpenAI image routing by account capability and protocol.
- OAuth/API-key image routing separation.
- Image input/output token pricing and catalog fallback behavior.
- Image URL-to-base64 backfill and private-destination protections.
- Fixed-price image billing, cache pricing, balance failures, and usage
  recording.
- Images API usage with unsplit `output_tokens` is classified as image output
  tokens, so `image_output_price` applies when the upstream omits
  `output_tokens_details.image_tokens`.
- Account statistics pricing and long-context billing gates.
- In-flight reservation protection against concurrent overdraft.
- Deleted-key usage settlement and upstream billing-probe behavior.
- Dedicated OpenAI Images scheduling honors the existing
  `openai_low_upstream_rate_priority_enabled` API-pricing-sync rate ordering for
  both native and basic fallback selection. This uses sync-owned account rates;
  it does not create a separate profit gate or rewrite group/account prices.
- The API-pricing-sync account rate remains the source of the upstream-cost
  scheduling signal. Image requests opt into the same existing low-upstream-rate
  priority used by token requests when the setting is enabled; the setting is a
  preference among eligible accounts, not a guarantee when cheaper accounts
  are unavailable.

Representative history:

- `5e259f456` image model routing and billing stabilization
- `f48866958` OpenAI-only Codex image bridge
- `a2007db70` preserve catalog image prices
- `fd7af1d46` account-stat long-context gate
- `43f88c99a`, `52c123e6f`, `652a7d74f` in-flight reservation and billing
  fallback safeguards
- `c2d5bbd93` settle usage after API-key deletion
- `de98bdff1` preserve insufficient-balance failures
- `c5897dc17` classify unsplit Images API output tokens as image tokens
- `32255f71c` pass API-pricing-sync rate priority through image scheduling

### Channel Pricing and Model Plaza

- Channel model pricing persistence for token, per-request, image, and video
  modes.
- Cache-write 5m/1h, cache-read, Fast/Flex, reasoning-effort, image, video,
  interval, and time-based pricing fields.
- Model pricing description persistence, multiline text, and scope handling.
- Channel pricing form round-trip and account-stat pricing isolation.
- Model Plaza grouping, platform isolation, composite expansion, official
  pricing, long-context tiers, time pricing, image/video multipliers, and
  cache pricing display.
- Model pricing descriptions now flow end to end:

  ```text
  channel pricing editor
    -> admin request/form conversion
    -> channel_model_pricing.description
    -> channel repository/service
    -> ModelPlazaService.PlazaModel.Description
    -> model-plaza API
    -> PlazaModelPricingTable
  ```

- The primary channel pricing editor exposes a 500-character description
  textarea with English and Chinese labels. Account-stat pricing intentionally
  omits this field because it is not public model pricing.

Representative history:

- `493612952`, `8cf79b886`, `e7e1d8e94` model description schema and
  persistence lineage
- `3cd4d2a1b`, `91e691f5a`, `5a1e53306` scoped description and UI lineage
- `b8d52fad3` configurable reasoning-effort pricing
- `b5b5f1f94` independent model-plaza video multipliers
- `83d4eb6a4`, `b07d85c49`, `f19095f96` time-pricing display behavior
- `d03c42d79`, `ff758f37d` cache and interval display safeguards
- `c9012835e` complete channel editor to model-plaza description flow

### Gateway, Compatibility, Security, and Operations

- CORS normalization and credential behavior.
- Composite platform routing and model restrictions.
- Anthropic/OpenAI/Gemini protocol compatibility.
- Prompt-audit route coverage and security-audit integration.
- Account scheduling, sticky sessions, rate-limit handling, and failover
  behavior.
- Payment order/refund safety, pending refund holds, and EasyPay/Alipay
  confirmation handling.
- Frontend security dependency updates and secret scanning.

The full exact file-level preservation set is captured by:

```bash
git diff --name-status refs/tags/v0.2.17-upstream..baseline/v0.2.17-historical-fixes-sync
```

## Migration and Deployment Safeguards

- Ent schema and generated code include durable video task and settlement
  tables.
- Migration `178_channel_image_input_price.sql` keeps its historical
  production checksum compatibility rule; applied migration records are not
  rewritten.
- Model pricing description migration and regression coverage are included.
- Production deployments retain the previous container and a PostgreSQL
  custom-format backup before replacement.
- The production image embeds the frontend and reports version `0.2.17`.

## Baseline Verification

Checks completed for this baseline:

- Backend no-run compile across `./...`.
- Video, settlement, model-plaza, handler, route, and repository targeted
  tests.
- Channel pricing description editor, locale, and form round-trip tests.
- Model Plaza description propagation unit test.
- Images API real-request verification: output-only token usage was classified
  as image output and charged against `image_output_price`.
- API-pricing-sync image scheduler regression: with low-upstream-rate priority
  enabled, a `2.35` account is ordered after a `0.107` account.
- Production sync-order image request returned one image; the `.091` account
  was tried first and returned upstream 503, then the `.105` account succeeded.
  The resulting usage log recorded account `601`, image output tokens `1056`,
  group rate `0.132`, and billed amount `$0.033`.
- Frontend `vue-tsc -b`.
- Frontend ESLint on changed files.
- Frontend production build and i18n completeness checks.
- Secret scan.
- Production health, startup, migration, table, and version checks.

The baseline tag must move only when a future upgrade has been reviewed
against this record and deployed checks have passed.
