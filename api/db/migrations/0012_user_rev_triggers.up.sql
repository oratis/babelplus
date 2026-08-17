-- 0012 · user_rev 的 bump 函数与触发器
--
-- 事实源：data-model.md §8.4（对 ADR 0006 的两处补全）
--
-- ADR 0006 定的规则是：「凡改变节点可见用户集合或密钥的写操作必须 bump user_rev；
-- 流量累加**不得** bump」。字面执行会漏两个场景，本文件补全：
--   1. 配额耗尽（u+d 跨过 transfer_enable）：累加不 bump，**跨越阈值那一次 bump**
--      —— 这一条写在流量入账的 SQL 里（db/queries/stats.sql），不是触发器
--   2. 到期（expired_at 走过）：到期本身不产生写操作，靠每分钟扫描 + expiry_applied_at 标记，
--      标记的 UPDATE 本身触发下面的触发器
--
-- 🔴 为什么这里用触发器，而 sqlc 的方向是「SQL 写在文件里」：
--    漏 bump 的后果是**静默的** —— 节点永远拿旧用户表，没有报错、没有告警，
--    只有「封禁了但那个人还能用」。触发器是唯一能保证「无论从哪条代码路径写入都不漏」的机制。
--    代价（data-model §15.1）：sqlc 生成的 Go 代码里看不到它，第一次调试的人会困惑。
--    ⚠️ 撤回条件：当所有写路径都收敛到 3–5 个明确的 service 方法时，
--       应当把触发器改回显式调用并删掉它。

-- 把「哪些节点该重新拉用户表」这件事集中在一个函数里
CREATE FUNCTION bump_user_rev(p_group_id bigint) RETURNS void AS $$
  UPDATE node_rev SET user_rev = user_rev + 1, user_rev_at = now()
  WHERE server_id IN (SELECT server_id FROM server_group_map WHERE group_id = p_group_id);
$$ LANGUAGE sql;

CREATE FUNCTION bump_user_rev_for_user(p_user_id bigint) RETURNS void AS $$
  UPDATE node_rev SET user_rev = user_rev + 1, user_rev_at = now()
  WHERE server_id IN (
    SELECT m.server_id FROM server_group_map m
    JOIN users u ON u.group_id = m.group_id
    WHERE u.id = p_user_id
  );
$$ LANGUAGE sql;

-- 触发器：任何改变「节点可见用户集合或其密钥」的写都必须传播
CREATE FUNCTION users_bump_user_rev() RETURNS trigger AS $$
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

CREATE TRIGGER users_bump_user_rev_trg
  AFTER INSERT OR UPDATE OR DELETE ON users
  FOR EACH ROW EXECUTE FUNCTION users_bump_user_rev();

-- 🔴 user_traffic 上禁止任何触发器（data-model §8.4）。
--    它是每 60 秒 × 节点数 × 活跃用户被写的表；一个 ROW 级触发器会把 bump
--    从「偶发」变成「每次 push 都发生」，ETag 就彻底失效 —— 节点每 60 秒都收 200 而不是 304。
