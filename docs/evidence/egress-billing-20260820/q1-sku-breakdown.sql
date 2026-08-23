-- SKU 级拆分：oratis-491316 的全部出口流量 SKU，2026-06-28 → 2026-08-20（含）
SELECT
  sku.description                                   AS sku,
  location.location                                 AS source_region,
  ROUND(SUM(usage.amount_in_pricing_units), 2)      AS gib,
  ROUND(SUM(cost), 2)                               AS gross_usd,
  ROUND(SUM(IFNULL((SELECT SUM(c.amount) FROM UNNEST(credits) c), 0)), 2) AS credits_usd,
  ROUND(SUM(cost) + SUM(IFNULL((SELECT SUM(c.amount) FROM UNNEST(credits) c), 0)), 2) AS net_usd,
  ROUND(SAFE_DIVIDE(SUM(cost), SUM(usage.amount_in_pricing_units)), 4)    AS usd_per_gib
FROM `loopback-500616.billing_export.gcp_billing_export_v1_0130C2_FA2146_786074`
WHERE project.id = 'oratis-491316'
  AND sku.description LIKE 'Network%Data Transfer Out%'
  AND usage_start_time >= TIMESTAMP('2026-06-28 00:00:00 UTC')
  AND usage_start_time <  TIMESTAMP('2026-08-21 00:00:00 UTC')
GROUP BY sku, source_region
HAVING gib > 0
ORDER BY gross_usd DESC
