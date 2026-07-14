BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;

WITH failed_tasks AS (
    SELECT t.id AS task_id, t.public_task_id, t.user_id, t.api_key_id, t.account_id, t.usage_log_id, t.result_metadata
    FROM video_tasks t
    WHERE t.status = 'failed'
),
direct_links AS (
    SELECT t.task_id, l.id AS usage_log_id, l.user_id, l.api_key_id, l.account_id, l.billing_mode,
           l.total_cost, l.actual_cost, l.account_stats_cost, l.account_rate_multiplier,
           l.refunded_cost, l.refunded_total_cost, l.refunded_account_cost, l.refund_reason, l.refunded_at
    FROM failed_tasks t
    JOIN usage_logs l
      ON l.id = t.usage_log_id
     AND l.user_id = t.user_id
     AND l.api_key_id = t.api_key_id
     AND l.account_id = t.account_id
),
legacy_links AS (
    SELECT t.task_id, l.id AS usage_log_id, l.user_id, l.api_key_id, l.account_id, l.billing_mode,
           l.total_cost, l.actual_cost, l.account_stats_cost, l.account_rate_multiplier,
           l.refunded_cost, l.refunded_total_cost, l.refunded_account_cost, l.refund_reason, l.refunded_at
    FROM failed_tasks t
    JOIN usage_logs l
      ON l.user_id = t.user_id
     AND l.api_key_id = t.api_key_id
     AND l.account_id = t.account_id
     AND l.request_id = t.result_metadata ->> 'request_id'
    WHERE t.usage_log_id IS NULL
),
linked_usages AS (
    SELECT * FROM direct_links
    UNION ALL
    SELECT * FROM legacy_links
),
ranked_links AS (
    SELECT l.*, COUNT(*) OVER (PARTITION BY l.task_id) AS link_count
    FROM linked_usages l
)
SELECT
    'candidates' AS result_set,
    t.task_id,
    t.public_task_id,
    l.usage_log_id,
    t.user_id,
    t.api_key_id,
    t.account_id,
    l.total_cost AS gross_cost,
    l.actual_cost AS customer_cost,
    COALESCE(l.account_stats_cost, l.total_cost * COALESCE(l.account_rate_multiplier, 1)) AS account_cost
FROM failed_tasks t
JOIN ranked_links l ON l.task_id = t.task_id
WHERE l.link_count = 1
  AND l.billing_mode = 'per_request'
  AND l.refunded_cost = 0
  AND l.refunded_total_cost = 0
  AND l.refunded_account_cost = 0
  AND l.refund_reason IS NULL
  AND l.refunded_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM video_task_settlements s WHERE s.video_task_id = t.task_id)
ORDER BY t.task_id;

WITH failed_tasks AS (
    SELECT t.id AS task_id, t.public_task_id, t.user_id, t.api_key_id, t.account_id, t.usage_log_id, t.result_metadata
    FROM video_tasks t
    WHERE t.status = 'failed'
),
direct_links AS (
    SELECT t.task_id, l.id AS usage_log_id, l.billing_mode, l.total_cost, l.actual_cost, l.account_stats_cost, l.account_rate_multiplier,
           l.refunded_cost, l.refunded_total_cost, l.refunded_account_cost, l.refund_reason, l.refunded_at
    FROM failed_tasks t
    JOIN usage_logs l
      ON l.id = t.usage_log_id
     AND l.user_id = t.user_id
     AND l.api_key_id = t.api_key_id
     AND l.account_id = t.account_id
),
legacy_links AS (
    SELECT t.task_id, l.id AS usage_log_id, l.billing_mode, l.total_cost, l.actual_cost, l.account_stats_cost, l.account_rate_multiplier,
           l.refunded_cost, l.refunded_total_cost, l.refunded_account_cost, l.refund_reason, l.refunded_at
    FROM failed_tasks t
    JOIN usage_logs l
      ON l.user_id = t.user_id
     AND l.api_key_id = t.api_key_id
     AND l.account_id = t.account_id
     AND l.request_id = t.result_metadata ->> 'request_id'
    WHERE t.usage_log_id IS NULL
),
linked_usages AS (
    SELECT * FROM direct_links
    UNION ALL
    SELECT * FROM legacy_links
),
link_counts AS (
    SELECT task_id, COUNT(*) AS link_count
    FROM linked_usages
    GROUP BY task_id
),
single_links AS (
    SELECT l.*
    FROM linked_usages l
    JOIN link_counts c ON c.task_id = l.task_id AND c.link_count = 1
),
candidates AS (
    SELECT t.task_id
    FROM failed_tasks t
    JOIN single_links l ON l.task_id = t.task_id
    WHERE l.billing_mode = 'per_request'
      AND l.refunded_cost = 0
      AND l.refunded_total_cost = 0
      AND l.refunded_account_cost = 0
      AND l.refund_reason IS NULL
      AND l.refunded_at IS NULL
      AND NOT EXISTS (SELECT 1 FROM video_task_settlements s WHERE s.video_task_id = t.task_id)
)
SELECT
    'exclusions' AS result_set,
    t.task_id,
    t.public_task_id,
    l.usage_log_id,
    t.user_id,
    t.api_key_id,
    t.account_id,
    l.total_cost AS gross_cost,
    l.actual_cost AS customer_cost,
    COALESCE(l.account_stats_cost, l.total_cost * COALESCE(l.account_rate_multiplier, 1)) AS account_cost,
    CASE
        WHEN COALESCE(c.link_count, 0) = 0 THEN 'no_reconstructable_usage_link'
        WHEN c.link_count > 1 THEN 'ambiguous_usage_link'
        WHEN l.billing_mode IS DISTINCT FROM 'per_request' THEN 'usage_not_per_request'
        WHEN l.refunded_cost <> 0
          OR l.refunded_total_cost <> 0
          OR l.refunded_account_cost <> 0
          OR l.refund_reason IS NOT NULL
          OR l.refunded_at IS NOT NULL THEN 'usage_already_refunded'
        WHEN EXISTS (SELECT 1 FROM video_task_settlements s WHERE s.video_task_id = t.task_id) THEN 'settlement_exists'
        ELSE 'no_reconstructable_usage_link'
    END AS exclusion_reason
FROM failed_tasks t
LEFT JOIN link_counts c ON c.task_id = t.task_id
LEFT JOIN single_links l ON l.task_id = t.task_id
WHERE NOT EXISTS (SELECT 1 FROM candidates c WHERE c.task_id = t.task_id)
ORDER BY t.task_id;

ROLLBACK;
