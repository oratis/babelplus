-- 0012 down · user_rev 的 bump 函数与触发器

DROP TRIGGER IF EXISTS users_bump_user_rev_trg ON users;
DROP FUNCTION IF EXISTS users_bump_user_rev();
DROP FUNCTION IF EXISTS bump_user_rev_for_user(bigint);
DROP FUNCTION IF EXISTS bump_user_rev(bigint);
