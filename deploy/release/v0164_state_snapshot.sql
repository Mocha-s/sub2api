SELECT 'setting', key, value
FROM settings
WHERE key IN ('allow_user_view_error_requests', 'ALIPAY_MOBILE_PRECREATE_DEEP_LINK', 'ollama_cloud_usage_settings')
ORDER BY key;

SELECT 'pricing', p.channel_id, p.platform, p.models::text, p.billing_mode,
       p.input_price, p.output_price, p.cache_write_price, p.cache_read_price,
       p.image_input_price, p.image_output_price, p.per_request_price,
       p.video_price_per_second, p.video_default_seconds, p.video_allowed_seconds::text,
       COALESCE(to_jsonb(p)->>'description', '')
FROM channel_model_pricing p
ORDER BY p.channel_id, p.platform, p.models::text, p.billing_mode, p.id;

SELECT 'group', g.id, g.name, g.platform, g.status, g.rate_multiplier,
       g.allow_image_generation, g.allow_video_generation,
       COALESCE(to_jsonb(g)->>'max_reasoning_effort', ''),
       COALESCE(to_jsonb(g)->'reasoning_effort_mappings', '[]'::jsonb)::text
FROM groups g
ORDER BY g.id;

SELECT to_regclass('public.composite_model_routes') IS NOT NULL AS has_composite_routes \gset
\if :has_composite_routes
SELECT 'composite_route', r.group_id, r.public_model, r.match_type, r.endpoint,
       r.target_platform, r.upstream_model, r.priority, r.enabled
FROM composite_model_routes r
ORDER BY r.group_id, r.priority, r.id;
\endif

SELECT 'channel', id, name, description, status, billing_model_source,
       restrict_models, features,
       jsonb_build_object(
           'bedrock_cc_compat', features_config->'bedrock_cc_compat',
           'codex_image_generation_bridge', features_config->'codex_image_generation_bridge',
           'web_search_emulation', features_config->'web_search_emulation'
       )::text,
       apply_pricing_to_account_stats, model_mapping::text
FROM channels
ORDER BY id;

SELECT 'stats_rule', channel_id, name, group_ids::text, account_ids::text, sort_order
FROM channel_account_stats_pricing_rules
ORDER BY channel_id, sort_order, name, id;

SELECT 'stats_pricing', r.channel_id, r.name, p.platform, p.models::text,
       p.billing_mode, p.input_price, p.output_price, p.cache_write_price,
       p.cache_read_price, p.image_input_price, p.image_output_price,
       p.per_request_price, p.video_price_per_second,
       p.video_default_seconds, p.video_allowed_seconds::text
FROM channel_account_stats_model_pricing p
JOIN channel_account_stats_pricing_rules r ON r.id = p.rule_id
ORDER BY r.channel_id, r.sort_order, r.name, r.id, p.platform, p.models::text, p.billing_mode, p.id;

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

SELECT 'migration', filename, checksum
FROM schema_migrations
ORDER BY filename;

SELECT 'video_settlement', state, count(*)
FROM video_task_settlements
GROUP BY state
ORDER BY state;
