-- 账单账户级 gross 日燃烧率（EXCLUDE_ALL_CREDITS 口径，即 GFS 抵扣池的消耗速度）
SELECT DATE(usage_start_time) AS day, ROUND(SUM(cost),2) AS gross_usd
FROM `loopback-500616.billing_export.gcp_billing_export_v1_0130C2_FA2146_786074`
WHERE usage_start_time >= TIMESTAMP('2026-06-16 00:00:00 UTC')
GROUP BY day ORDER BY day DESC LIMIT 30
