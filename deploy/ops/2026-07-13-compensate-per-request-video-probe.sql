BEGIN;

DO $legacy_compensation$
DECLARE
    v_task video_tasks%ROWTYPE;
    v_usage usage_logs%ROWTYPE;
    v_user users%ROWTYPE;
    v_api_key api_keys%ROWTYPE;
    v_account_cost NUMERIC(20,10);
    v_settlement_id BIGINT;
    v_zero_effects JSONB := jsonb_build_object(
        'balance_cost', 0,
        'subscription_cost', 0,
        'api_key_quota_cost', 0,
        'api_key_rate_limit_cost', 0,
        'account_quota_cost', 0,
        'platform_quota_cost', 0,
        'account_stats_cost', 0
    );
BEGIN
    -- Lock the immutable target rows before evaluating any repair guard.
    SELECT * INTO STRICT v_task
    FROM video_tasks
    WHERE public_task_id = 'task_32d91ebd2001e678fca0ad8310067765'
    FOR UPDATE;

    SELECT * INTO STRICT v_usage
    FROM usage_logs
    WHERE id = 70697
    FOR UPDATE;

    SELECT * INTO STRICT v_user
    FROM users
    WHERE id = 24
    FOR UPDATE;

    SELECT * INTO STRICT v_api_key
    FROM api_keys
    WHERE id = 60
    FOR UPDATE;

    IF v_task.status <> 'failed'
        OR v_task.user_id <> 24
        OR v_task.api_key_id <> 60
        OR v_task.account_id <> 221
        OR v_task.group_id IS DISTINCT FROM v_api_key.group_id
        OR v_task.platform IS DISTINCT FROM 'openai_video'
        OR v_task.subscription_id IS NOT NULL
        OR (v_task.usage_log_id IS NOT NULL AND v_task.usage_log_id <> 70697)
        -- This legacy task persisted neither usage_log_id nor request_id. If a
        -- request_id exists, it must still match the immutable target usage.
        OR (
            v_task.usage_log_id IS NULL
            AND NULLIF(BTRIM(v_task.result_metadata ->> 'request_id'), '') IS NOT NULL
            AND (v_task.result_metadata ->> 'request_id') IS DISTINCT FROM v_usage.request_id
        ) THEN
        RAISE EXCEPTION 'legacy per-request video compensation guard failed for task';
    END IF;

    IF v_api_key.user_id <> 24
        OR v_api_key.group_id IS NULL THEN
        RAISE EXCEPTION 'legacy per-request video compensation guard failed for API key';
    END IF;

    IF v_usage.user_id <> 24
        OR v_usage.api_key_id <> 60
        OR v_usage.account_id <> 221
        OR v_usage.model <> 'video-ds-2.0-fast'
        OR v_usage.billing_type IS DISTINCT FROM 0
        OR v_usage.billing_mode IS DISTINCT FROM 'per_request'
        OR v_usage.subscription_id IS NOT NULL
        OR v_usage.total_cost <> 65
        OR v_usage.actual_cost <> 6.9225
        OR v_usage.refunded_cost <> 0
        OR v_usage.refunded_total_cost <> 0
        OR v_usage.refunded_account_cost <> 0
        OR v_usage.refund_reason IS NOT NULL
        OR v_usage.refunded_at IS NOT NULL
        OR (v_usage.account_stats_cost IS NULL AND v_usage.account_rate_multiplier IS NULL) THEN
        RAISE EXCEPTION 'legacy per-request video compensation guard failed for usage log';
    END IF;

    v_account_cost := COALESCE(v_usage.account_stats_cost, v_usage.total_cost * v_usage.account_rate_multiplier);
    IF v_account_cost < 0 THEN
        RAISE EXCEPTION 'legacy per-request video compensation guard failed for account cost';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM video_task_settlements s
        WHERE s.video_task_id = v_task.id
    ) THEN
        RAISE EXCEPTION 'legacy per-request video compensation guard failed: settlement already exists';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM video_task_settlement_events e
        JOIN video_task_settlements s ON s.id = e.settlement_id
        WHERE s.video_task_id = v_task.id
          AND e.event_type IN ('refund', 'legacy_refund')
    ) THEN
        RAISE EXCEPTION 'legacy per-request video compensation guard failed: refund event already exists';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM video_task_settlements s
        WHERE s.charge_request_id = 'legacy_per_request_video_probe_compensation:task_32d91ebd2001e678fca0ad8310067765'
    ) THEN
        RAISE EXCEPTION 'legacy per-request video compensation guard failed: charge request already exists';
    END IF;

    -- Historic quota and rate-window effects are intentionally not reconstructed.
    UPDATE users
    SET balance = balance + v_usage.actual_cost,
        updated_at = NOW()
    WHERE id = 24;

    UPDATE usage_logs
    SET refunded_cost = actual_cost,
        refunded_total_cost = total_cost,
        refunded_account_cost = COALESCE(account_stats_cost, total_cost * account_rate_multiplier),
        refund_reason = 'Legacy per-request video failure compensation; balance only; historic quota effects unchanged.',
        refunded_at = NOW()
    WHERE id = 70697;

    UPDATE video_tasks
    SET usage_log_id = 70697,
        updated_at = NOW()
    WHERE id = v_task.id
      AND public_task_id = 'task_32d91ebd2001e678fca0ad8310067765';

    INSERT INTO video_task_settlements (
        video_task_id, user_id, api_key_id, group_id, account_id, platform, channel_id, subscription_id, usage_log_id,
        charge_request_id, state, billing_type, gross_cost_usd, actual_cost_usd, account_cost_usd, refunded_cost_usd,
        pricing_snapshot, effect_snapshot, refunded_at
    ) VALUES (
        v_task.id, v_task.user_id, v_task.api_key_id, v_task.group_id, v_task.account_id, v_task.platform, v_task.channel_id, v_task.subscription_id, v_usage.id,
        'legacy_per_request_video_probe_compensation:task_32d91ebd2001e678fca0ad8310067765', 'refunded', 0, v_usage.total_cost, v_usage.actual_cost, v_account_cost, v_usage.actual_cost,
        jsonb_build_object(
            'billing_mode', 'per_request',
            'compensation_kind', 'legacy_per_request_compensation',
            'source_usage_log_id', v_usage.id
        ),
        v_zero_effects,
        NOW()
    )
    RETURNING id INTO v_settlement_id;

    INSERT INTO video_task_settlement_events (settlement_id, event_id, event_type, amount_usd, metadata)
    VALUES (
        v_settlement_id,
        'legacy_per_request_video_probe_refund:task_32d91ebd2001e678fca0ad8310067765',
        'legacy_refund',
        v_usage.actual_cost,
        jsonb_build_object(
            'event_id', 'legacy_per_request_video_probe_refund:task_32d91ebd2001e678fca0ad8310067765',
            'guard_version', 1,
            'target_ids', jsonb_build_object(
                'video_task_id', v_task.id,
                'usage_log_id', v_usage.id,
                'user_id', v_task.user_id,
                'api_key_id', v_task.api_key_id,
                'account_id', v_task.account_id
            ),
            'omitted_effects', jsonb_build_array(
                'api_key_quota',
                'api_key_rate_windows',
                'account_quota',
                'subscription_quota',
                'platform_quota'
            )
        )
    );

    INSERT INTO video_task_refund_reporting_jobs (settlement_id, usage_log_id, usage_created_at)
    VALUES (v_settlement_id, v_usage.id, v_usage.created_at);

    INSERT INTO video_task_cache_invalidation_jobs (settlement_id, event_type, payload)
    VALUES (
        v_settlement_id,
        'legacy_refund',
        jsonb_build_object(
            'Version', 1,
            'UserID', v_task.user_id,
            'APIKeyID', v_task.api_key_id,
            'GroupID', v_task.group_id,
            'Platform', v_task.platform,
            'BillingType', 0,
            'Effects', v_zero_effects
        )
    );
END;
$legacy_compensation$;

COMMIT;
