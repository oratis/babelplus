-- 回滚写探针（ADR 0013 §6.4）。**在 0016_billing_rules.down.sql 跑完之后、
-- 0012_user_rev_triggers.down.sql 跑到之前**执行 —— 0012.down 的第一句就是
-- DROP TRIGGER + DROP FUNCTION users_bump_user_rev()，跑过那一句证物就没了。
--
-- 它抓的是什么：
--   0016.up 把 users_bump_user_rev() 的监视列表换成了 transfer_enable_plan / _pack 两个分量。
--   如果 0016.down 忘了先把函数体换回 0012 的版本就去删那两列，函数体会留在库里指向已不存在的字段，
--   于是 users 上的**任何** UPDATE 都报 `record "old" has no field "transfer_enable_plan"`。
--   plpgsql 不在 DDL 时校验字段名，DROP COLUMN 也不会连带报错 —— 只有真的写一行才暴露。
--
-- 🔴 为什么必须先塞一行真实数据：
--   ADR §6.4 原文给的是 `UPDATE users SET updated_at = now() WHERE false;`。
--   **那一条抓不到它要抓的东西**（实测：返回 `UPDATE 0`，零报错）：
--   影响 0 行 ⇒ AFTER ... FOR EACH ROW 触发器一次都不执行 ⇒ plpgsql 永远不去解析那些字段名。
--   所以本文件先 INSERT 一行、再 UPDATE 到那一行上，并用 ROW_COUNT 断言确实打到了 1 行。
--
-- 整段包在事务里、结尾 ROLLBACK：探针不留任何数据，后面的 down 与「回滚后归零」断言不受影响。

BEGIN;

DO $$
DECLARE
  gid       bigint;
  n         integer;
  trg_count integer;
BEGIN
  -- 0. 先确认触发器真的还挂在 users 上。少了这一步，探针会在「触发器已经被删掉」的库上
  --    静默通过 —— 一条从未真正生效过的检查比没有检查更坏（AGENTS.md §3 的事实纪律）。
  SELECT count(*) INTO trg_count
  FROM pg_trigger WHERE tgrelid = 'users'::regclass AND NOT tgisinternal;
  IF trg_count = 0 THEN
    RAISE EXCEPTION 'users 上一个用户触发器都没有：本探针跑在了错误的回滚位置（0012.down 已经执行过？），它现在是空跑的';
  END IF;

  -- 1. 塞一行真实数据。users.group_id 是 NOT NULL 外键，所以先要有一个分组。
  INSERT INTO server_groups (code, name)
  VALUES ('ci-rollback-probe', 'CI 回滚写探针') RETURNING id INTO gid;

  INSERT INTO users (email, password_hash, group_id)
  VALUES ('ci-rollback-probe@example.invalid', 'x', gid);

  -- 2. 真的打到那一行上。只改 updated_at：group_id 不变，于是判定落进最后那条 ELSIF ——
  --    也就是逐列比较 OLD/NEW 的那一条，正是字段名失配会炸的地方。
  UPDATE users SET updated_at = now()
  WHERE email = 'ci-rollback-probe@example.invalid';

  GET DIAGNOSTICS n = ROW_COUNT;
  IF n <> 1 THEN
    RAISE EXCEPTION '写探针影响了 % 行（期望 1）：没打到行就等于触发器没执行，这一步是空跑的', n;
  END IF;

  RAISE NOTICE '✅ 回滚后写探针通过：真的 INSERT 了 1 行、真的 UPDATE 到了那 1 行，触发器执行且未报字段失配';
END $$;

ROLLBACK;
