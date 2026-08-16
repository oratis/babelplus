-- 0001 · 枚举类型
--
-- 事实源：docs/02-architecture/data-model.md §2.2「枚举策略」
-- 只有「删掉一个值 = 业务语义变了」的集合才用 ENUM；配置类用 text + CHECK。
-- ENUM 的声明序就是比较序 —— ticket_priority 的 low < normal < high < urgent
-- 被 tickets_queue_idx 的 priority DESC 直接依赖（data-model §10）。
--
-- ⚠️ 本文件必须最先执行：下游全部 DDL 依赖这 10 个类型。

-- 邮箱验证码用途（data-model §4）
CREATE TYPE verification_purpose AS ENUM ('register', 'password_reset', 'email_change');

-- 流量重置模式：Xboard v2_plan.reset_traffic_method 的六种模式，改成有名字的枚举（data-model §6）
CREATE TYPE reset_method AS ENUM (
  'follow_system',         -- Xboard null：跟随系统默认
  'monthly_first',         -- Xboard 0：每月 1 号
  'monthly_on_order_day',  -- Xboard 1：按订单日按月 ← 竞品实测的实际行为，我们的默认
  'never',                 -- Xboard 2：不重置（不限时套餐用）
  'yearly_jan_first',      -- Xboard 3：每年 1 月 1 日
  'yearly_on_order_day'    -- Xboard 4：按订单日按年
);

-- 订单类型 / 周期 / 状态机（data-model §6，沿用 payments.md §4.13 的定义，一字不改）
CREATE TYPE order_type AS ENUM (
  'new', 'renew', 'upgrade', 'traffic_pack', 'reset_pack', 'wallet_topup'
);

CREATE TYPE order_period AS ENUM (
  'monthly', 'quarterly', 'half_yearly', 'yearly', 'onetime'
);

CREATE TYPE order_status AS ENUM (
  'pending','paying','underpaid','paid','completed',
  'cancelled','expired','failed',
  'refunding','refunded','partially_refunded',
  'chargeback','chargeback_won','chargeback_lost'
);

-- 节点协议（data-model §8）
CREATE TYPE server_protocol AS ENUM (
  'vless_reality',      -- VLESS + XTLS-Vision + REALITY，TCP:443，默认主力
  'hysteria2',          -- Hysteria2 + salamander，UDP:443，加速通路（BBR 不用 Brutal）
  'shadowsocks2022',    -- 2022-blake3-aes-128-gcm，兜底
  'vless_xhttp_cdn'     -- 应急：VLESS + XHTTP over CF CDN，默认关闭
);

-- 工单四枚举（data-model §10 / admin-support-docs §2.4）
CREATE TYPE ticket_status AS ENUM (
  'open',          -- 用户新建，待客服首次响应
  'pending',       -- 客服已回复，等待用户补充信息
  'in_progress',   -- 客服正在处理
  'on_hold',       -- 挂起（等待上游/节点供应商等外部依赖）
  'resolved',      -- 已解决，进入自动关闭倒计时
  'closed'         -- 终态
);

-- ⚠️ 声明序即比较序：low < normal < high < urgent。tickets_queue_idx 的
--    priority DESC 因此把 urgent 排在最前 —— 这不是巧合，是依赖声明序。
CREATE TYPE ticket_priority AS ENUM ('low', 'normal', 'high', 'urgent');

CREATE TYPE ticket_channel AS ENUM (
  'web',        -- 站内工单页（主通道）
  'email',      -- 邮件转工单
  'telegram',   -- Telegram Bot（第一阶段不启用，ADR 0002）
  'admin'       -- 客服代客户创建
);

CREATE TYPE ticket_actor AS ENUM ('user', 'agent', 'system');
