-- 逐日出口用量（两台自用节点所在区域），2026-06-28 起
SELECT DATE(usage_start_time) AS day,
  ROUND(SUM(IF(location.location='us-west1',        usage.amount_in_pricing_units,0)),2) AS us_west1_gib,
  ROUND(SUM(IF(location.location='asia-northeast1', usage.amount_in_pricing_units,0)),2) AS asia_northeast1_gib,
  ROUND(SUM(usage.amount_in_pricing_units),2) AS total_gib,
  ROUND(SUM(cost),2) AS gross_usd
FROM `loopback-500616.billing_export.gcp_billing_export_v1_0130C2_FA2146_786074`
WHERE project.id='oratis-491316'
  AND sku.description LIKE 'Network%Data Transfer Out%'
  AND location.location IN ('us-west1','asia-northeast1')
  AND usage_start_time >= TIMESTAMP('2026-06-28 00:00:00 UTC')
  AND usage_start_time <  TIMESTAMP('2026-08-21 00:00:00 UTC')
GROUP BY day ORDER BY day
