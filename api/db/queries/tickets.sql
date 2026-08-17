-- tickets.sql · 工单
--
-- 事实源：data-model.md §10、admin-support-docs.md §2.4
--
-- 🔴 is_internal 是全系统最容易出安全事故的一列。
--    用户侧的读一律走 ticket_messages_public 视图（下面 List*Public 那几条），
--    **不接受调用方传 is_internal 参数**。视图是机制，「在 repository 层强制」只是约定。

-- ============================================================
-- 分类
-- ============================================================

-- name: ListTicketCategories :many
SELECT * FROM ticket_categories WHERE is_active = true ORDER BY sort_order, id;

-- name: GetTicketCategory :one
SELECT * FROM ticket_categories WHERE id = $1;

-- name: CreateTicketCategory :one
INSERT INTO ticket_categories (
  slug, name_zh, name_en, sla_first_response_minutes, sla_resolution_minutes,
  default_priority, sort_order
) VALUES ($1,$2,$3,$4,$5,$6,$7)
RETURNING *;


-- ============================================================
-- 工单
-- ============================================================

-- context 是建单瞬间的诊断快照（JSONB 而非外键）：用户事后续费或换节点
-- 不应改变工单的诊断上下文。
-- name: CreateTicket :one
INSERT INTO tickets (
  public_id, user_id, category_id, subject, priority, channel,
  context, first_response_due, resolution_due
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
RETURNING *;

-- 对外只用 public_id（'BP-7K2M9Q'），不暴露自增 id —— 防枚举工单总量与他人工单
-- name: GetTicketByPublicID :one
SELECT * FROM tickets WHERE public_id = $1;

-- name: GetTicketForUser :one
SELECT * FROM tickets WHERE public_id = $1 AND user_id = $2;

-- name: ListUserTickets :many
SELECT id, public_id, category_id, subject, status, priority, channel,
       message_count, last_agent_reply_at, last_user_reply_at, created_at, updated_at
FROM tickets
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- 客服工作台主查询：走 tickets_queue_idx。
-- priority DESC 依赖 ticket_priority 的 ENUM 声明序（low < normal < high < urgent），
-- 所以 urgent 排最前 —— 这不是巧合，是依赖声明序。
-- name: ListTicketQueue :many
SELECT id, public_id, user_id, category_id, subject, status, priority, channel,
       assignee_id, first_response_due, resolution_due, message_count, created_at
FROM tickets
WHERE status NOT IN ('resolved','closed')
  AND (sqlc.narg(assignee_id)::bigint IS NULL OR assignee_id = sqlc.narg(assignee_id)::bigint)
ORDER BY priority DESC, first_response_due NULLS LAST
LIMIT $1 OFFSET $2;

-- ⚠️ tickets_resolved_consistency / tickets_closed_consistency 把状态与时间戳绑死：
--    status='resolved'|'closed' ⟺ resolved_at IS NOT NULL；status='closed' ⟺ closed_at IS NOT NULL。
--    这里用 CASE 一次算对，否则 UPDATE 会被 CHECK 拒绝。
-- name: UpdateTicketStatus :one
UPDATE tickets SET
  status = $2,
  resolved_at = CASE WHEN $2::ticket_status IN ('resolved','closed') THEN coalesce(resolved_at, now()) ELSE NULL END,
  closed_at   = CASE WHEN $2::ticket_status = 'closed' THEN coalesce(closed_at, now()) ELSE NULL END,
  updated_at  = now()
WHERE id = $1
RETURNING *;

-- name: AssignTicket :one
UPDATE tickets SET assignee_id = $2, assigned_at = now(), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateTicketPriority :one
UPDATE tickets SET priority = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetTicketTags :one
UPDATE tickets SET tags = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: RateTicket :one
UPDATE tickets SET satisfaction_rating = $2, satisfaction_comment = $3, updated_at = now()
WHERE id = $1 AND user_id = $4 AND status IN ('resolved','closed')
RETURNING *;

-- 按诊断上下文检索（走 tickets_context_idx，GIN jsonb_path_ops）
-- name: SearchTicketsByContext :many
SELECT id, public_id, user_id, subject, status, priority, context, created_at
FROM tickets
WHERE context @> $1
ORDER BY created_at DESC
LIMIT $2;


-- ============================================================
-- 消息
-- ============================================================

-- ticket_messages_actor_consistency 保证 actor_type 与 user_id/admin_user_id 一致；
-- ticket_messages_internal_only_agent 保证用户消息永远不能是内部备注。
-- name: CreateTicketMessage :one
INSERT INTO ticket_messages (
  ticket_id, actor_type, user_id, admin_user_id, body, body_format, is_internal, channel, external_id
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
RETURNING *;

-- message_count 的维护：写消息时在同一事务内 UPDATE，**刻意不用触发器**
-- （data-model §10.1 修改 4：本表写频率低，漏了只是计数不准，不是静默故障）。
-- name: BumpTicketMessageCount :one
UPDATE tickets SET
  message_count = message_count + 1,
  first_response_at = CASE
    WHEN $2::ticket_actor = 'agent' AND first_response_at IS NULL THEN now()
    ELSE first_response_at END,
  last_user_reply_at  = CASE WHEN $2::ticket_actor = 'user'  THEN now() ELSE last_user_reply_at  END,
  last_agent_reply_at = CASE WHEN $2::ticket_actor = 'agent' THEN now() ELSE last_agent_reply_at END,
  updated_at = now()
WHERE id = $1
RETURNING *;

-- 🔴 用户侧：只走视图。视图里根本没有 is_internal 这一列，
--    任何一次「忘了加 WHERE is_internal = false」在这里是不可能发生的。
-- name: ListTicketMessagesPublic :many
SELECT id, ticket_id, actor_type, user_id, admin_user_id,
       body, body_format, channel, created_at, edited_at
FROM ticket_messages_public
WHERE ticket_id = $1
ORDER BY created_at;

-- 客服侧：含内部备注。调用点必须在管理员中间件链后面。
-- name: ListTicketMessagesInternal :many
SELECT * FROM ticket_messages
WHERE ticket_id = $1
ORDER BY created_at;

-- name: EditTicketMessage :one
UPDATE ticket_messages SET body = $2, edited_at = now()
WHERE id = $1
RETURNING *;


-- ============================================================
-- 附件
-- ============================================================

-- storage_key 是 GCS 对象键，不存公开 URL（签名 URL 由 handler 现算）
-- name: CreateTicketAttachment :one
INSERT INTO ticket_attachments (message_id, storage_key, filename, content_type, size_bytes)
VALUES ($1,$2,$3,$4,$5)
RETURNING *;

-- name: ListTicketAttachments :many
SELECT a.* FROM ticket_attachments a
JOIN ticket_messages m ON m.id = a.message_id
WHERE m.ticket_id = $1
ORDER BY a.created_at;


-- ============================================================
-- SLA
-- ============================================================

-- 分类上的 SLA 覆盖优先级更高；这里查的是全局/按套餐的兜底
-- name: GetSLAPolicy :one
SELECT * FROM sla_policies
WHERE is_active = true
  AND priority = $1
  AND (plan_id = $2 OR plan_id IS NULL)
ORDER BY plan_id NULLS LAST
LIMIT 1;

-- name: CreateSLAPolicy :one
INSERT INTO sla_policies (
  name, plan_id, priority, first_response_minutes, resolution_minutes,
  business_hours_only, timezone
) VALUES ($1,$2,$3,$4,$5,$6,$7)
RETURNING *;

-- 违约扫描：首次响应超时
-- name: ListTicketsBreachingFirstResponse :many
SELECT id, public_id, first_response_due
FROM tickets
WHERE status NOT IN ('resolved','closed')
  AND first_response_at IS NULL
  AND first_response_due IS NOT NULL
  AND first_response_due < now()
ORDER BY first_response_due
LIMIT $1;

-- 违约独立成表，不在 tickets 上加布尔列 —— 「违约了多少次、哪一类」可直接聚合，
-- 也不会因为工单后续被重开而丢失历史。UNIQUE (ticket_id, breach_type) 兼作幂等。
-- name: RecordSLABreach :one
INSERT INTO ticket_sla_breaches (ticket_id, breach_type, due_at)
VALUES ($1, $2, $3)
ON CONFLICT (ticket_id, breach_type) DO NOTHING
RETURNING *;

-- name: ListSLABreaches :many
SELECT b.*, t.public_id
FROM ticket_sla_breaches b
JOIN tickets t ON t.id = b.ticket_id
WHERE b.breached_at BETWEEN $1 AND $2
ORDER BY b.breached_at DESC;


-- ============================================================
-- 事件审计与快捷回复
-- ============================================================

-- name: CreateTicketEvent :one
INSERT INTO ticket_events (ticket_id, actor_type, admin_user_id, event_type, from_value, to_value, metadata)
VALUES ($1,$2,$3,$4,$5,$6,$7)
RETURNING *;

-- name: ListTicketEvents :many
SELECT * FROM ticket_events WHERE ticket_id = $1 ORDER BY created_at;

-- name: ListCannedResponses :many
SELECT * FROM canned_responses
WHERE (sqlc.narg(category_id)::bigint IS NULL OR category_id = sqlc.narg(category_id)::bigint)
ORDER BY usage_count DESC, id;

-- name: IncrementCannedResponseUsage :exec
UPDATE canned_responses SET usage_count = usage_count + 1 WHERE id = $1;

-- name: CreateCannedResponse :one
INSERT INTO canned_responses (slug, title, body, locale, category_id)
VALUES ($1,$2,$3,$4,$5)
RETURNING *;
