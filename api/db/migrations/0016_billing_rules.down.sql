-- 0016 down · 计费三条规则（ADR 0013 §6.2）
--
-- 🔴 **顺序是硬要求，第 1 步尤其。**
--
-- 反方在 postgres:17 上实测、ADR 作者复现（§7.1 C7 · §7.2）：
-- 按「先删列、后管触发器」的直觉顺序跑完 down，`users` 表的**任何 UPDATE 都会失败**，
-- 报 `ERROR: record "old" has no field "transfer_enable_plan"` ——
-- 因为 up 把 `users_bump_user_rev()` 的监视列表换成了两个分量，而 down 把那两列删掉了，
-- 函数体却还留在库里。**函数体不随列一起消失，plpgsql 也不在 DDL 时校验字段名。**
--
-- 所以必须**先把函数换回 0012 的函数体，再删分量列**。
--
-- ⚠️ 这一步只在「先跑 0016.down、观察、再决定要不要继续」这种真实生产回滚里才救得了命 ——
--    CI 逆序继续跑过 0013_rate_limit.down，到 0012.down 的第一句就是
--    `DROP TRIGGER … DROP FUNCTION users_bump_user_rev()`（0012_user_rev_triggers.down.sql:3–4），
--    **把证物销毁了**，而作业最后只断言「表 0 · 视图 0 · 枚举 0」，**从不写一行数据**。
--    也就是说：**这条 down 写错了，现有 CI 抓不到。**它靠的是 ADR 0013 §6.4 要新增的那个
--    「回滚后再写一次」步骤（`UPDATE users SET updated_at = now() WHERE false;`）兜底。
--    在那个步骤落地之前，这段注释就是唯一的防线。

-- ============================================================
-- 1. 🔴 第一步：把 users_bump_user_rev() 还原成 0012 的函数体（监视 OLD.transfer_enable）
-- ============================================================
CREATE OR REPLACE FUNCTION users_bump_user_rev() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    PERFORM bump_user_rev(NEW.group_id);
  ELSIF TG_OP = 'DELETE' THEN
    PERFORM bump_user_rev(OLD.group_id);
  ELSIF OLD.group_id IS DISTINCT FROM NEW.group_id THEN
    PERFORM bump_user_rev(OLD.group_id);
    PERFORM bump_user_rev(NEW.group_id);
  ELSIF (OLD.uuid, OLD.banned, OLD.expired_at, OLD.transfer_enable,
         OLD.speed_limit_mbps, OLD.device_limit, OLD.deleted_at, OLD.expiry_applied_at)
     IS DISTINCT FROM
        (NEW.uuid, NEW.banned, NEW.expired_at, NEW.transfer_enable,
         NEW.speed_limit_mbps, NEW.device_limit, NEW.deleted_at, NEW.expiry_applied_at) THEN
    PERFORM bump_user_rev(NEW.group_id);
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;

-- ============================================================
-- 2. 恢复普通列，再回填，再删分量列（顺序反了会丢数据）
-- ============================================================
--
-- 先删生成列再加同名普通列：生成列不能被「改回」普通列，只能重建。
-- 回填必须夹在「加回来」与「删分量」之间 —— 先删分量就再也算不出总额了。
ALTER TABLE users DROP COLUMN transfer_enable;
ALTER TABLE users ADD COLUMN transfer_enable bigint NOT NULL DEFAULT 0;
UPDATE users SET transfer_enable = transfer_enable_plan + transfer_enable_pack;
ALTER TABLE users DROP COLUMN transfer_enable_plan, DROP COLUMN transfer_enable_pack,
                  DROP COLUMN pack_expire_at;
-- 索引随 pack_expire_at 一起消失，这一句是无害的显式确认（IF EXISTS，所以不会因为已消失而报错）。
DROP INDEX IF EXISTS users_pack_expiry_due_idx;

-- ============================================================
-- 3. 其余对称回退
-- ============================================================

-- ---- traffic_reset_log ----
COMMENT ON COLUMN traffic_reset_log.new_transfer_enable IS NULL;
ALTER TABLE traffic_reset_log DROP COLUMN new_transfer_enable_pack;

-- ---- commissions ----
ALTER TABLE commissions DROP CONSTRAINT commissions_no_self_invite;

-- ---- refunds（两索引随列消失，仍显式写出以便与 up 逐条对读）----
COMMENT ON COLUMN refunds.amount IS NULL;
DROP INDEX IF EXISTS refunds_rule_idx;
DROP INDEX IF EXISTS refunds_cooling_off_once;
ALTER TABLE refunds DROP COLUMN rule, DROP COLUMN user_id;

-- ---- plans（先删 CHECK 再删列：约束引用了这一列）----
ALTER TABLE plans DROP CONSTRAINT plans_cycle_needs_monthly;
ALTER TABLE plans DROP COLUMN kind;

-- ---- orders 五列 ----
-- amount_refunded 是既有列，up 只给它加了 COMMENT，所以 down 要把 COMMENT 摘掉，
-- 否则「回滚后再灌一次 up」得到的库与第一次不同（注释是 schema 的一部分，pg_dump 会带上它）。
COMMENT ON COLUMN orders.amount_refunded IS NULL;
DROP INDEX IF EXISTS orders_prev_idx;
ALTER TABLE orders
  DROP COLUMN pay_from_address,
  DROP COLUMN price_monthly_at_order,
  DROP COLUMN prev_order_id,
  DROP COLUMN covers_to,
  DROP COLUMN covers_from;
