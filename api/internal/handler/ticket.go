package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
)

// 用户侧工单。
//
// ============================================================
// 🔴 两条贯穿本文件的纪律（与 tickets_user.sql 的文件头是同一条）
// ============================================================
//
// **一、is_internal 是全系统最容易出安全事故的一列。**
//
//	用户侧的读一律走 `ticket_messages_public` 视图（视图里根本没有这一列），
//	用户侧的写一律走 tickets_user.sql 的 CreateUserTicketMessage（语句里根本没有这个参数）。
//	🔴 本文件**一次都没有**调用 tickets.sql 的 CreateTicketMessage ——
//	那一条的第 7 个参数就是 is_internal，而「建单时顺手用它写首条消息」
//	正是最容易把这个参数带进用户面的入口。
//	首条消息因此也走 CreateUserTicketMessage（见 CreateTicket 的注释）。
//
// **二、context 快照由服务端采集，客户端那份只能进 `client_reported` 子对象。**
//
//	CreateUserTicket 把这条规则做成了**语句形状**：context 的值在 SQL 里由
//	jsonb_build_object 现场从 users / user_traffic / plans / user_device_state /
//	subscription_fetch_log 读取，handler **没有**一个可以传整份 context 的参数。
//	客户端那份只能通过 client_reported 进入一个嵌套子对象，在 jsonb 结构上
//	覆盖不了任何服务端键。
//	🔴 不要改用 tickets.sql 的 CreateTicket —— 它把 context 当第 7 个参数收，
//	那正是「handler 图省事把请求体直接塞进去」的入口，而那是一次**看不出来**的错误：
//	页面照常渲染，只是里面的数字是用户想让我们看到的。

// ============================================================
// 参数与限额
// ============================================================

const (
	// ticketSubjectMaxRunes 与 CreateTicketRequest.subject 的 maxLength 同值。
	// 按 **rune** 计：128 字节只够 42 个汉字，而契约说的是 128 个字符。
	ticketSubjectMaxRunes = 128

	// ticketMessageMaxRunes 是正文上限。
	//
	// ⚠️ **契约没有给 message 定 maxLength**（只有 minLength: 1）。
	// 但 ticket_messages.body 是无长度限制的 text，且工单正文会被客服逐条读、
	// 会进导出、会进邮件通知 —— 不设上限意味着一次请求就能往表里写一个 MB。
	// 取 20000（约 2 万字）是**设定值**：远超任何真实报障描述，
	// 又能挡住「把日志整个粘进来」这种把工单页面撑死的情况。
	// 超限返回 422 并明确说出上限，让用户知道要删掉什么。
	ticketMessageMaxRunes = 20000

	// ticketPublicIDPrefix / ticketPublicIDLen 决定 public_id 的形状（如 BP-7K2M9Q）。
	// 字符集复用 inviteCodeAlphabet：public_id 会被用户念给客服听、
	// 会被抄进邮件，0/O 与 1/I/l 分不清会变成一次「你这单号是不是给错了」的对话。
	ticketPublicIDPrefix = "BP-"
	ticketPublicIDLen    = 6

	// createTicketRetries 是 public_id 撞号重试次数。
	// tickets_public_id_key 是唯一约束，撞号该被重试而不是变成 500。
	createTicketRetries = 3

	// ticketContextMaxBytes 是 client_reported 序列化后的字节上限。
	//
	// 🔴 这不是防御式编程：tickets.context 是 **jsonb 且被 GIN 索引**
	// （tickets_context_idx），还会被 SearchTicketsByContext 全库检索、会进导出。
	// 客户端那份是 additionalProperties 开放的对象，不设上限的话
	// 一次建单就能往 GIN 索引里塞进一兆的垃圾，而受害的是**所有人**的工单搜索。
	// 超限时丢弃客户端那份（服务端快照照常采集），并打 WARN —— 建单本身不该因此失败。
	ticketContextMaxBytes = 8 * 1024

	// 建单限流：每用户每小时 N 条。
	//
	// ⚠️ api-contract §10.1 的表里**没有**这一行（用户面其余是 per user_id 120 req/min），
	// 但 openapi 单独给 createTicket 声明了 429 —— 那个 429 只可能是建单本身的限额。
	// 取 10/h 是**设定值**：真实报障一小时内开十张单已经不可能是同一件事，
	// 而工单的成本不是数据库而是**人**（客服要逐条读）。
	// 走「精确档」（rate_limit 表）而不是进程内令牌桶：建单本来就低频，
	// 一次 upsert 的代价可以忽略，而 8 倍放大在这里意味着一个人能开 80 张单。
	ticketCreateLimit  = 10
	ticketCreateWindow = time.Hour
	bucketTicketUser1h = "ticket_create_user_1h"
)

// ============================================================
// 建单
// ============================================================

// CreateTicket 建一张工单，并把首条消息写进去。
func (s *Server) CreateTicket(ctx context.Context, req gen.CreateTicketRequestObject) (gen.CreateTicketResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	if req.Body == nil {
		return gen.CreateTicket422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失")}, nil
	}

	subject := strings.TrimSpace(req.Body.Subject)
	message := strings.TrimSpace(req.Body.Message)
	if subject == "" || len([]rune(subject)) > ticketSubjectMaxRunes {
		return gen.CreateTicket422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("标题长度必须在 1–%d 个字符之间", ticketSubjectMaxRunes),
				detail("subject", "长度不合法")),
		}, nil
	}
	if message == "" || len([]rune(message)) > ticketMessageMaxRunes {
		return gen.CreateTicket422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("正文长度必须在 1–%d 个字符之间", ticketMessageMaxRunes),
				detail("message", "长度不合法")),
		}, nil
	}

	// 限流放在**校验之后、查库之前**：校验只花 CPU，而限流要写一次 rate_limit；
	// 反过来的话一批格式非法的请求会先把限额吃光，把真正要报障的人挡在外面。
	if allowed, retryAfter, err := s.limiter.Allow(ctx, bucketTicketUser1h,
		fmt.Sprintf("u:%d", userID), ticketCreateLimit, ticketCreateWindow); err == nil && !allowed {
		return gen.CreateTicket429JSONResponse{
			ErrRateLimitedJSONResponse: s.rateLimited(ctx,
				fmt.Sprintf("提交工单过于频繁，每小时最多 %d 张。若是同一个问题，请在原工单里补充说明", ticketCreateLimit),
				int32(retryAfter.Seconds())),
		}, nil
	}

	// 分类 + SLA 一条语句解析。
	// 🔴 不拆成「先 GetTicketCategory 再 GetSLAPolicy」：优先级（分类上的 SLA 覆盖 >
	// 全局/按套餐的 sla_policies）只存在于 data-model 的一句话里，
	// 写反了不会有任何报错 —— 现象是所有工单都用了全局 SLA，
	// 而「首次响应 SLA 从 30 分钟变成 240 分钟」没有任何告警会说话。
	cat, err := s.db.ResolveTicketCategoryAndSLA(ctx, dbgen.ResolveTicketCategoryAndSLAParams{
		CategorySlug: string(req.Body.Category),
		UserID:       userID,
	})
	if err != nil {
		return s.createTicketResolveFailure(ctx, userID, string(req.Body.Category), err), nil
	}

	clientReported := s.clientReportedJSON(ctx, userID, req.Body.Context)

	var created dbgen.CreateUserTicketRow
	var firstMsg dbgen.CreateUserTicketMessageRow
	for attempt := 0; ; attempt++ {
		publicID, idErr := newTicketPublicID()
		if idErr != nil {
			return gen.CreateTicket500JSONResponse{
				ErrInternalJSONResponse: s.internalErr(ctx, "生成工单号失败", idErr),
			}, nil
		}

		// 建单 + 首条消息 + message_count 必须**同一事务**：
		// 一张没有任何消息的工单在客服工作台上是一条无法处理的记录，
		// 而 message_count 停在 0 会让列表页显示「0 条消息」。
		err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
			var txErr error
			created, txErr = q.CreateUserTicket(ctx, dbgen.CreateUserTicketParams{
				PublicID:       publicID,
				UserID:         userID,
				CategoryID:     &cat.CategoryID,
				Subject:        subject,
				Priority:       cat.Priority,
				ClientReported: clientReported,
				// SLA 截止时刻在 SQL 里用数据库时钟算（now() + n * interval '1 minute'）。
				// 在 Go 里算的话，它后面要跟 ListTicketsBreachingFirstResponse 里的 now() 比较，
				// 两个时钟差几秒就会让「刚建的单立刻违约」。
				FirstResponseMinutes: &cat.EffectiveFirstResponseMinutes,
				ResolutionMinutes:    &cat.EffectiveResolutionMinutes,
			})
			if txErr != nil {
				return txErr
			}

			// 🔴 首条消息走 **CreateUserTicketMessage**，不是 tickets.sql 的 CreateTicketMessage。
			// 后者的第 7 个参数是 is_internal —— 让它出现在用户面的调用点上，
			// 就等于给「某次重构把 actor 也参数化」留了门。
			// 代价是多一次按 public_id 的查找（工单刚建好，必然命中且状态是 open）。
			firstMsg, txErr = q.CreateUserTicketMessage(ctx, dbgen.CreateUserTicketMessageParams{
				PublicID: publicID,
				UserID:   userID,
				Body:     message,
			})
			if txErr != nil {
				return txErr
			}
			if firstMsg.MessageID == nil {
				// 刚建的单一定是 open，走不到「已关闭」那一支。真走到了说明
				// CreateUserTicketMessage 的三态语义变了 —— 回滚，宁可建不出单
				// 也不要留下一张没有正文的工单。
				return errors.New("首条工单消息未写入（CreateUserTicketMessage 返回了 message_id = NULL）")
			}

			// ⚠️ message_count / last_user_reply_at 的维护**刻意不用触发器**
			// （0010 修改 4）。漏了这一步的后果是列表页的消息数与最后回复时间
			// 停在建单那一刻 —— 静默、且只有用户会发现。
			_, txErr = q.BumpTicketMessageCount(ctx, dbgen.BumpTicketMessageCountParams{
				ID:      created.ID,
				Column2: dbgen.TicketActorUser,
			})
			return txErr
		})
		if err == nil {
			break
		}
		if isUniqueViolation(err) && attempt < createTicketRetries {
			s.logger.WarnContext(ctx, "工单号撞号，重试", "user_id", userID, "attempt", attempt+1)
			continue
		}
		return gen.CreateTicket500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "建单失败", err),
		}, nil
	}

	s.logger.InfoContext(ctx, "工单已创建",
		"user_id", userID, "public_id", created.PublicID, "category", cat.CategorySlug)

	body := struct {
		Data gen.Ticket `json:"data"`
		Meta gen.Meta   `json:"meta"`
	}{
		Data: s.ticketFromCreated(ctx, created, cat.CategorySlug),
		Meta: s.meta(ctx),
	}
	return gen.CreateTicket201JSONResponse{
		Body:    body,
		Headers: gen.CreateTicket201ResponseHeaders{Location: "/api/v1/tickets/" + created.PublicID},
	}, nil
}

// createTicketResolveFailure 把 ResolveTicketCategoryAndSLA 的失败分成三种。
//
// 🔴 **0 行有两个成因，HTTP 码不同。** 那条查询 `CROSS JOIN users` 把
// 「分类不存在 / 未启用」与「用户已注销」并成了同一个 ErrNoRows
// （这么写换掉的是「未注销用户在两条语句之间被注销」这种不可能测到的窗口，
//
//	代价就是这里要再判一次会话主体）。
//	用 GetUserNotificationPrefs 做这次判定：它是全仓最窄的「这个用户还在不在」查询
//	（两个 boolean，带 deleted_at IS NULL），既不带 password_hash 也不带 uuid。
//	用 GetUserByID（SELECT *）来判存在是把一堆凭据列拉进一个只需要一个布尔值的分支。
//
// 🔴 **第三种失败是扫描失败，不是 0 行。**
//
//	ResolveTicketCategoryAndSLARow 的 EffectiveFirstResponseMinutes 是**非指针 int32**
//	（显式 `::int` cast 让 sqlc 判成 NOT NULL），而那个 coalesce 在
//	「分类没覆盖 且 sla_policies 一条都没配」时求值为 NULL。
//	于是 pgx 报 `cannot scan NULL into *int32`，而这在**全新部署上是默认状态** ——
//	migrations 里没有任何一支 seed 过 sla_policies（实查 0001–0018）。
//	现象会是：第一个用户报障时建不出单。所以这条分支的日志必须直接写出修复动作。
//	返回 500 而不是 422（用户的输入没错）也不是 503（契约给 createTicket
//	只声明了 401/422/429/500，而这里不像 transferCommission 那样有
//	「用户盯着自己的钱反复重试」的失败形态）。
func (s *Server) createTicketResolveFailure(ctx context.Context, userID int64, slug string, err error) gen.CreateTicketResponseObject {
	if isNullScanError(err) {
		s.logger.ErrorContext(ctx,
			"bp_ticket_sla_unseeded 建单失败：SLA 策略未配置。ticket_categories 上没有覆盖，"+
				"且 sla_policies 里没有可用策略（migrations 从未 seed 过它）。"+
				"修复：插入一条 plan_id IS NULL 的兜底 sla_policies 策略",
			"user_id", userID, "category", slug, "err", err)
		return gen.CreateTicket500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "工单系统配置缺失，暂时无法建单", err),
		}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gen.CreateTicket500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "解析工单分类失败", err),
		}
	}

	if _, probeErr := s.db.GetUserNotificationPrefs(ctx, userID); probeErr != nil {
		if errors.Is(probeErr, pgx.ErrNoRows) {
			return gen.CreateTicket401JSONResponse{
				ErrUnauthorizedJSONResponse: s.unauthorizedDeletedUser(ctx, userID, "createTicket"),
			}
		}
		return gen.CreateTicket500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "确认账号状态失败", probeErr),
		}
	}
	// 用户在、分类不在。
	// ⚠️ ticket_categories 也从未被 migration seed 过 —— 一个全新部署上
	// **每一个**分类都会走到这里。日志里带上 slug，让「是不是分类没建」一眼可判。
	s.logger.WarnContext(ctx, "建单分类不存在或未启用", "user_id", userID, "category", slug)
	return gen.CreateTicket422JSONResponse{
		ErrUnprocessableJSONResponse: s.unprocessable(ctx, "工单分类不可用", detail("category", "未知分类")),
	}
}

// clientReportedJSON 把客户端自述的快照序列化成 jsonb 参数。
//
// 返回 nil 表示「不带客户端那份」。它进的是 context.client_reported 子对象，
// **覆盖不了任何服务端键**（SQL 的 jsonb_build_object 结构决定的），
// 所以这里不需要做任何字段级的过滤 —— 只需要管住体积（见 ticketContextMaxBytes）。
func (s *Server) clientReportedJSON(ctx context.Context, userID int64, c *gen.TicketClientContext) []byte {
	if c == nil {
		return nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		s.logger.WarnContext(ctx, "客户端自述快照无法序列化，已丢弃", "user_id", userID, "err", err)
		return nil
	}
	if len(b) > ticketContextMaxBytes {
		s.logger.WarnContext(ctx, "客户端自述快照过大，已丢弃（服务端快照不受影响）",
			"user_id", userID, "bytes", len(b), "limit", ticketContextMaxBytes)
		return nil
	}
	return b
}

// newTicketPublicID 生成形如 BP-7K2M9Q 的工单号。
func newTicketPublicID() (string, error) {
	const n = len(inviteCodeAlphabet)
	out := make([]byte, 0, ticketPublicIDLen)
	buf := make([]byte, 1)
	for len(out) < ticketPublicIDLen {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成工单号随机数失败: %w", err)
		}
		if buf[0] >= 248 { // 248 = 31 × 8，拒绝采样，理由同 randomInviteCode
			continue
		}
		out = append(out, inviteCodeAlphabet[int(buf[0])%n])
	}
	return ticketPublicIDPrefix + string(out), nil
}

// ticketFromCreated 用建单返回的行组装契约的 Ticket。
//
// 新单必然没有任何回复，所以 last_reply_at 为空、status 为 open。
func (s *Server) ticketFromCreated(ctx context.Context, r dbgen.CreateUserTicketRow, slug string) gen.Ticket {
	return gen.Ticket{
		PublicId:  r.PublicID,
		Subject:   r.Subject,
		Category:  s.ticketCategory(ctx, slug),
		Status:    ticketStatusView(r.Status, pgtypeNever, pgtypeNever),
		CreatedAt: ttime(r.CreatedAt),
		UpdatedAt: tptr(r.UpdatedAt),
	}
}

// pgtypeNever 是一个恒为「无值」的时间，给「新单没有任何回复」这类场景用。
//
// 🔴 必须是零值 `pgtype.Timestamptz{}`（Valid = false），**不能**写成
// `tstz(time.Time{})` —— 后者的 Valid 是 true，只是时间是 0001-01-01。
// 那会让 ticketStatusView 把一张全新的工单判成「客服已回复」
// （lastAgent.Valid 为 true 且 lastUser 也是零值时间，比较落到 After 的假分支之外），
// 而且这个 bug 只在**新建的工单**上出现 —— 最难在测试里被注意到的那一类。
var pgtypeNever = pgtype.Timestamptz{}

// ============================================================
// 列表与详情
// ============================================================

// ListTickets 列出用户的工单。
func (s *Server) ListTickets(ctx context.Context, req gen.ListTicketsRequestObject) (gen.ListTicketsResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	want, page := listPageLimit(req.Params.Limit, defaultPageLimit)
	arg := dbgen.ListUserTicketsPageParams{UserID: userID, PageLimit: page}
	if req.Params.Cursor != nil {
		if c, valid := decodePageCursor(string(*req.Params.Cursor)); valid {
			// (created_at, id) 两个字段必须同时传：只传一个时行比较求值为 NULL，
			// 返回 0 行而不是报错。
			arg.CursorAt = tstz(c.At)
			id := c.ID
			arg.CursorID = &id
		} else {
			s.logger.WarnContext(ctx, "工单列表的游标无法解析，已从首页开始",
				"user_id", userID, "cursor_len", len(*req.Params.Cursor))
		}
	}

	rows, err := s.db.ListUserTicketsPage(ctx, arg)
	if err != nil {
		return gen.ListTickets500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询工单列表失败", err),
		}, nil
	}

	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, gen.Ticket{
			PublicId:    r.PublicID,
			Subject:     r.Subject,
			Category:    s.ticketCategory(ctx, derefOr(r.CategorySlug, "")),
			Status:      ticketStatusView(r.Status, r.LastAgentReplyAt, r.LastUserReplyAt),
			CreatedAt:   ttime(r.CreatedAt),
			UpdatedAt:   tptr(r.UpdatedAt),
			LastReplyAt: lastReplyAt(r.LastAgentReplyAt, r.LastUserReplyAt),
		})
	}

	meta := s.meta(ctx)
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		c := encodePageCursor(last.ID, ttime(last.CreatedAt))
		meta.NextCursor = &c
	}
	return gen.ListTickets200JSONResponse{Data: out, Meta: meta}, nil
}

// GetTicket 返回工单详情与会话。
func (s *Server) GetTicket(ctx context.Context, req gen.GetTicketRequestObject) (gen.GetTicketResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	// 🔴 GetUserTicketDetail 是窄投影，**不是** tickets.sql 的 GetTicketForUser（SELECT *）。
	// 后者会把 assignee_id（哪个客服在处理）、first_response_due / resolution_due
	// （我们的内部 SLA 承诺）、tags（可能是 'suspected-fraud' 这类字样）
	// 一并带进用户面 handler —— 而「结构体里有的字段迟早会被某次 json.Marshal 带出去」
	// 是本仓库反复登记的一类事故。
	t, err := s.db.GetUserTicketDetail(ctx, dbgen.GetUserTicketDetailParams{
		PublicID: req.PublicId,
		UserID:   userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 「不存在」与「不是你的」同一个 404：public_id 是可枚举的路径参数，
			// 区分两者等于确认某个单号存在。
			return gen.GetTicket404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "工单不存在")}, nil
		}
		return gen.GetTicket500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询工单详情失败", err),
		}, nil
	}

	// 🔴 会话只走 ListTicketMessagesPublic —— 它 FROM 的是
	// `ticket_messages_public` 视图，**视图里根本没有 is_internal 这一列**。
	// 用户面不存在第二条取消息的查询，所以「忘了加 WHERE is_internal = false」
	// 这个错误在这里没有发生的地方。
	msgs, err := s.db.ListTicketMessagesPublic(ctx, t.ID)
	if err != nil {
		return gen.GetTicket500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询工单消息失败", err),
		}, nil
	}

	out := make([]gen.TicketMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, gen.TicketMessage{
			Id:        m.ID,
			Author:    ticketAuthor(m.ActorType),
			Body:      m.Body,
			CreatedAt: ttime(m.CreatedAt),
		})
	}

	return gen.GetTicket200JSONResponse{
		Data: gen.TicketDetail{
			Ticket: gen.Ticket{
				PublicId:    t.PublicID,
				Subject:     t.Subject,
				Category:    s.ticketCategory(ctx, derefOr(t.CategorySlug, "")),
				Status:      ticketStatusView(t.Status, t.LastAgentReplyAt, t.LastUserReplyAt),
				CreatedAt:   ttime(t.CreatedAt),
				UpdatedAt:   tptr(t.UpdatedAt),
				LastReplyAt: lastReplyAt(t.LastAgentReplyAt, t.LastUserReplyAt),
			},
			Messages: out,
		},
		Meta: s.meta(ctx),
	}, nil
}

// ticketCategory 把 ticket_categories.slug 映射成契约的枚举。
//
// ⚠️ 契约的 TicketCategory 是 [subscription, node-down, billing, account] 四个值，
// 而 ticket_categories 是一张**运营可写**的表 —— 管理员新建一个分类，
// 这里就会拿到一个不在枚举里的 slug。
//
// 认不出来时**原样透传并打 WARN**，不猜一个：把「网络问题」硬映射成 account
// 会让客服按错误的分类去处理，而透传出去至少每个字都是真的
// （前端 switch 落到兜底分支显示原始 slug，是可理解的降级）。
// slug 为空（category_id 为 NULL，分类被删过）时同样透传空串 ——
// 编一个分类比留空更糟。
func (s *Server) ticketCategory(ctx context.Context, slug string) gen.TicketCategory {
	switch gen.TicketCategory(slug) {
	case gen.Subscription, gen.NodeDown, gen.Billing, gen.Account:
	default:
		s.logger.WarnContext(ctx, "工单分类不在契约枚举内，已原样下发", "category_slug", slug)
	}
	return gen.TicketCategory(slug)
}

// ticketStatusView 把 DB 的六态映射成契约的四态。纯函数。
//
// ⚠️ **两处对不上，且缺两格**（tickets_user.sql §3 已逐条登记）：
//
//	契约      open / pending / replied / closed
//	DB        open / pending / in_progress / on_hold / resolved / closed
//
//	· `replied`（客服已回复）在库里**没有对应的 status 值** —— 那个事实在
//	  `last_agent_reply_at > last_user_reply_at` 里。所以它由两个时间戳推，
//	  而不是从 status 映射。
//	· `in_progress` / `on_hold` / `resolved` 在契约里没有对应值，只能并进 pending。
//	  🔴 其中 **resolved 并进 pending 会让用户看不出「已解决、即将自动关闭」** ——
//	  他会以为还没人处理。这是契约表达力不足，登记在交付说明里；
//	  在契约放开之前，前端应当靠 last_reply_at 与文案补上这个信息。
//
// 判定顺序：closed 最先（已关闭的单不该再显示成「客服已回复」，
// 那会让用户以为还能继续对话），然后才是 replied，最后按 status 落。
func ticketStatusView(st dbgen.TicketStatus, lastAgent, lastUser pgtype.Timestamptz) gen.TicketStatus {
	if st == dbgen.TicketStatusClosed {
		return gen.Closed
	}
	if lastAgent.Valid && (!lastUser.Valid || lastAgent.Time.After(lastUser.Time)) {
		return gen.Replied
	}
	if st == dbgen.TicketStatusOpen {
		return gen.Open
	}
	return gen.Pending
}

// lastReplyAt 取两个回复时间里较晚的那个。
//
// 契约的 Ticket.last_reply_at 没说是谁的回复，而列表页要靠它排「最近有动静的单」——
// 取较晚的那个才是「最后一次有动静」。只取客服那一侧的话，
// 用户刚追问完的单在列表上看起来像一周没人管。
func lastReplyAt(lastAgent, lastUser pgtype.Timestamptz) *time.Time {
	switch {
	case lastAgent.Valid && lastUser.Valid:
		if lastAgent.Time.After(lastUser.Time) {
			return tptr(lastAgent)
		}
		return tptr(lastUser)
	case lastAgent.Valid:
		return tptr(lastAgent)
	case lastUser.Valid:
		return tptr(lastUser)
	default:
		return nil
	}
}

// ticketAuthor 把 actor_type 三值映射成契约的两值。纯函数。
//
// ⚠️ `system` 在契约里没有对应值（TicketMessage.author 只有 user / staff）。
// 两个候选是「映射成 staff」与「在用户面过滤掉」，这里选前者：
// 系统消息是「本单已自动关闭」「已为你转交技术组」这类**解释状态变化**的话，
// 过滤掉之后用户会看到一张状态莫名其妙变了的工单，然后再开一张单来问为什么。
// 把机器人标成 staff 是一处小的不精确；把解释藏起来是一次真实的信息损失。
// 已登记在交付说明里。
func ticketAuthor(a dbgen.TicketActor) gen.TicketMessageAuthor {
	if a == dbgen.TicketActorUser {
		return gen.TicketMessageAuthorUser
	}
	return gen.TicketMessageAuthorStaff
}

// ============================================================
// 回复与关单
// ============================================================

// CreateTicketMessage 在工单里追加一条用户消息。
//
// 🔴 一条语句同时给出三种结果（tickets_user.sql §4）：
//
//	0 行                      → 工单不存在或不属于这个用户 → 404
//	1 行且 message_id 为 NULL → 工单已关闭                 → 409
//	1 行且 message_id 非 NULL → 已插入                     → 201
//
// 先 SELECT 判状态再 INSERT 会留一个窗口：客服在两条语句之间关了单，
// 用户的回复就落进了一张已关闭的工单，而没有任何人会去看它。
//
// ⚠️ 只挡 `closed`，**不挡 `resolved`**：resolved 是「已解决，进入自动关闭倒计时」，
// 用户在这个阶段说「还是不行」正是我们要的信号。挡掉它等于逼他新开一张单，
// 而新单会丢掉全部上下文。
func (s *Server) CreateTicketMessage(ctx context.Context, req gen.CreateTicketMessageRequestObject) (gen.CreateTicketMessageResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	if req.Body == nil {
		return gen.CreateTicketMessage422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}
	message := strings.TrimSpace(req.Body.Message)
	if message == "" || len([]rune(message)) > ticketMessageMaxRunes {
		return gen.CreateTicketMessage422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("正文长度必须在 1–%d 个字符之间", ticketMessageMaxRunes),
				detail("message", "长度不合法")),
		}, nil
	}

	var row dbgen.CreateUserTicketMessageRow
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var txErr error
		row, txErr = q.CreateUserTicketMessage(ctx, dbgen.CreateUserTicketMessageParams{
			PublicID: req.PublicId,
			UserID:   userID,
			Body:     message,
		})
		if txErr != nil {
			return txErr
		}
		if row.MessageID == nil {
			// 已关闭：没插入任何行，事务里也没有别的写，直接返回让它回滚（等价于空事务）。
			return nil
		}
		// 🔴 **必须同事务**跟一次 BumpTicketMessageCount。
		_, txErr = q.BumpTicketMessageCount(ctx, dbgen.BumpTicketMessageCountParams{
			ID:      row.TicketID,
			Column2: dbgen.TicketActorUser,
		})
		return txErr
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.CreateTicketMessage404JSONResponse{
				ErrNotFoundJSONResponse: s.notFound(ctx, "工单不存在"),
			}, nil
		}
		return gen.CreateTicketMessage500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "写入工单回复失败", err),
		}, nil
	}
	if row.MessageID == nil {
		return gen.CreateTicketMessage409JSONResponse{
			ErrConflictJSONResponse: s.conflict(ctx, "该工单已关闭，无法继续回复。如问题仍未解决，请新开一张工单"),
		}, nil
	}

	return gen.CreateTicketMessage201JSONResponse{
		Data: gen.TicketMessage{
			Id:        *row.MessageID,
			Author:    gen.TicketMessageAuthorUser,
			Body:      derefOr(row.Body, message),
			CreatedAt: ttime(row.CreatedAt),
		},
		Meta: s.meta(ctx),
	}, nil
}

// CloseTicket 由用户主动关单。
//
// 与回复同形的三态返回：0 行 → 404；already_closed → 409；否则 200。
//
// 🔴 关单**必须同时**写 resolved_at 与 closed_at（tickets_resolved_consistency 与
// tickets_closed_consistency 两条 CHECK 把状态与时间戳绑死了），
// 且用 coalesce 而不是 now()：工单可能是从 resolved 关过来的，
// 覆盖已有的 resolved_at 等于把「什么时候解决的」改成「什么时候关闭的」——
// 而 resolved_at 是 SLA 达成率的分子。这两点都在 SQL 里，本函数不重复实现。
func (s *Server) CloseTicket(ctx context.Context, req gen.CloseTicketRequestObject) (gen.CloseTicketResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	var row dbgen.CloseUserTicketRow
	var detailRow dbgen.GetUserTicketDetailRow
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var txErr error
		row, txErr = q.CloseUserTicket(ctx, dbgen.CloseUserTicketParams{
			PublicID: req.PublicId,
			UserID:   userID,
		})
		if txErr != nil {
			return txErr
		}
		if row.AlreadyClosed {
			return nil
		}
		// 🔴 **同事务**写一条状态流转审计。漏了它，工单的历史里就少了
		// 「是用户自己关的」这条 —— 而那正是事后判断「我们是不是没解决就关了单」
		// 的唯一依据。from_value 用本条返回的 previous_status，
		// 这也是 CloseUserTicket 没有把事件并进去的原因（它需要旧状态）。
		prev := string(row.PreviousStatus)
		if _, txErr = q.CreateTicketEvent(ctx, dbgen.CreateTicketEventParams{
			TicketID:  row.TicketID,
			ActorType: dbgen.TicketActorUser,
			EventType: "status_changed",
			FromValue: &prev,
			ToValue:   ptrOf(string(dbgen.TicketStatusClosed)),
		}); txErr != nil {
			return txErr
		}
		// 关单响应要回一个完整的 Ticket，而 Ticket.category 是 **slug**，
		// CloseUserTicket 只给 category_id（内部主键）。在同一事务里读一次详情，
		// 拿到的就是刚写完的那个状态 —— 事务外再读会有一个窗口，
		// 期间客服重开了单，用户就会看到「我点了关闭，它却显示 open」。
		detailRow, txErr = q.GetUserTicketDetail(ctx, dbgen.GetUserTicketDetailParams{
			PublicID: req.PublicId,
			UserID:   userID,
		})
		return txErr
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.CloseTicket404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "工单不存在")}, nil
		}
		return gen.CloseTicket500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "关闭工单失败", err),
		}, nil
	}
	if row.AlreadyClosed {
		return gen.CloseTicket409JSONResponse{
			ErrConflictJSONResponse: s.conflict(ctx, "该工单已经关闭"),
		}, nil
	}

	s.logger.InfoContext(ctx, "用户关闭工单",
		"user_id", userID, "public_id", req.PublicId, "previous_status", string(row.PreviousStatus))

	return gen.CloseTicket200JSONResponse{
		Data: gen.Ticket{
			PublicId:    detailRow.PublicID,
			Subject:     detailRow.Subject,
			Category:    s.ticketCategory(ctx, derefOr(detailRow.CategorySlug, "")),
			Status:      ticketStatusView(detailRow.Status, detailRow.LastAgentReplyAt, detailRow.LastUserReplyAt),
			CreatedAt:   ttime(detailRow.CreatedAt),
			UpdatedAt:   tptr(detailRow.UpdatedAt),
			LastReplyAt: lastReplyAt(detailRow.LastAgentReplyAt, detailRow.LastUserReplyAt),
		},
		Meta: s.meta(ctx),
	}, nil
}
