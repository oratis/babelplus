-- 抵扣情况：oratis-491316 在同窗口内的 credit 明细（按类型与名称）
SELECT c.type AS credit_type, c.name AS credit_name, ROUND(SUM(c.amount),2) AS credit_usd
FROM `loopback-500616.billing_export.gcp_billing_export_v1_0130C2_FA2146_786074`, UNNEST(credits) c
WHERE project.id='oratis-491316'
  AND usage_start_time >= TIMESTAMP('2026-06-28 00:00:00 UTC')
  AND usage_start_time <  TIMESTAMP('2026-08-21 00:00:00 UTC')
GROUP BY credit_type, credit_name ORDER BY credit_usd
