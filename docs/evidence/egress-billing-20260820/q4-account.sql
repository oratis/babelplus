SELECT invoice.month AS invoice_month,
  ROUND(SUM(cost),2) AS gross_all_projects_usd,
  ROUND(SUM(IF(project.id='oratis-491316',cost,0)),2) AS gross_oratis_491316_usd,
  ROUND(SUM((SELECT SUM(c.amount) FROM UNNEST(credits) c WHERE c.type='PROMOTION')),2) AS promotion_credit_usd
FROM `loopback-500616.billing_export.gcp_billing_export_v1_0130C2_FA2146_786074`
GROUP BY invoice_month ORDER BY invoice_month
