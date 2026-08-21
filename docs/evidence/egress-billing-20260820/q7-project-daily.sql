-- oratis-491316 逐日 gross（全部服务），用于给项目级 budget 定额度
SELECT DATE(usage_start_time) AS day, ROUND(SUM(cost),2) AS gross_usd
FROM `loopback-500616.billing_export.gcp_billing_export_v1_0130C2_FA2146_786074`
WHERE project.id='oratis-491316' AND usage_start_time >= TIMESTAMP('2026-07-15')
GROUP BY day ORDER BY day
