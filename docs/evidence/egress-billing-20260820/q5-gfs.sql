-- GFS Y1 自定义窗口（2026-06-16 → 2027-06-15）内的 gross 累计（EXCLUDE_ALL_CREDITS 口径）
SELECT
  ROUND(SUM(cost),2) AS gross_excl_credits_usd,
  ROUND(SUM(IF(project.id='oratis-491316',cost,0)),2) AS of_which_oratis_491316,
  MIN(DATE(usage_start_time)) AS first_day,
  MAX(DATE(usage_start_time)) AS last_day
FROM `loopback-500616.billing_export.gcp_billing_export_v1_0130C2_FA2146_786074`
WHERE usage_start_time >= TIMESTAMP('2026-06-16 00:00:00 UTC')
