package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

func ticketTestServer() *Server {
	return &Server{logger: testLogger()}
}

func withUser(id int64) context.Context {
	return middleware.WithUser(context.Background(), &middleware.UserAuth{UserID: id})
}

// ============================================================
// 状态映射：两处对不上，且缺两格
// ============================================================

func TestTicketStatusView(t *testing.T) {
	base := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	agent := ts(base)
	user := ts(base.Add(-time.Hour))
	none := pgtype.Timestamptz{}

	cases := []struct {
		name       string
		st         dbgen.TicketStatus
		agent      pgtype.Timestamptz
		userReply  pgtype.Timestamptz
		want       gen.TicketStatus
		whyItMatte string
	}{
		{"新单没人回", dbgen.TicketStatusOpen, none, none, gen.Open, ""},
		{"客服回过了", dbgen.TicketStatusOpen, agent, user, gen.Replied, ""},
		{"用户追问在后", dbgen.TicketStatusOpen, ts(base.Add(-time.Hour)), ts(base), gen.Open, ""},
		// in_progress / on_hold 在契约里没有对应值，只能并进 pending。
		{"处理中", dbgen.TicketStatusInProgress, none, user, gen.Pending, ""},
		{"挂起", dbgen.TicketStatusOnHold, none, user, gen.Pending, ""},
		// ⚠️ resolved 并进 pending 会让用户看不出「已解决、即将自动关闭」。
		// 这是契约表达力不足，登记在交付说明里；此处只锁住当前行为。
		{"已解决", dbgen.TicketStatusResolved, none, user, gen.Pending, ""},
		// 🔴 已关闭必须先于 replied：一张关掉的单显示成「客服已回复」
		// 会让用户以为还能继续对话，而回复接口会给他 409。
		{"已关闭且客服最后回复", dbgen.TicketStatusClosed, agent, user, gen.Closed, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ticketStatusView(c.st, c.agent, c.userReply); got != c.want {
				t.Errorf("ticketStatusView = %q, want %q", got, c.want)
			}
		})
	}
}

// 🔴 这是本组的静默边界：**刚建好的工单不能显示成「客服已回复」。**
//
// pgtypeNever 如果写成 tstz(time.Time{})（Valid = true、时间是 0001-01-01），
// ticketStatusView 会看到一个「有效」的客服回复时间，把新单判成 replied。
// 而这个 bug 只在新建的工单上出现 —— 最难在集成测试里被注意到的那一类。
func TestNewTicketIsNotRepliedYet(t *testing.T) {
	if pgtypeNever.Valid {
		t.Fatal("pgtypeNever.Valid = true —— 它必须是「无值」，否则新单会被判成「客服已回复」")
	}
	srv := ticketTestServer()
	tk := srv.ticketFromCreated(context.Background(), dbgen.CreateUserTicketRow{
		PublicID: "BP-7K2M9Q", Subject: "连不上",
		Status: dbgen.TicketStatusOpen, CreatedAt: ts(time.Now()),
	}, "node-down")
	if tk.Status != gen.Open {
		t.Fatalf("新建工单的 status = %q, want open", tk.Status)
	}
	if tk.LastReplyAt != nil {
		t.Errorf("新单不该有 last_reply_at：%v", *tk.LastReplyAt)
	}
	if tk.Category != gen.NodeDown {
		t.Errorf("category = %q", tk.Category)
	}
}

func TestLastReplyAtTakesLatest(t *testing.T) {
	early := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	late := time.Date(2026, 8, 30, 18, 0, 0, 0, time.UTC)

	if got := lastReplyAt(ts(early), ts(late)); got == nil || !got.Equal(late) {
		t.Errorf("用户回复更晚时应取用户那条：%v", got)
	}
	if got := lastReplyAt(ts(late), ts(early)); got == nil || !got.Equal(late) {
		t.Errorf("客服回复更晚时应取客服那条：%v", got)
	}
	if got := lastReplyAt(pgtype.Timestamptz{}, ts(early)); got == nil || !got.Equal(early) {
		t.Errorf("只有一侧有值时应取那一侧：%v", got)
	}
	if got := lastReplyAt(pgtype.Timestamptz{}, pgtype.Timestamptz{}); got != nil {
		t.Errorf("都没有回复时应为 nil，got %v", *got)
	}
}

// ⚠️ system 在契约里没有对应的 author 取值。选「映射成 staff」而不是「过滤掉」：
// 系统消息是「本单已自动关闭」这类**解释状态变化**的话，
// 过滤掉之后用户会看到一张状态莫名其妙变了的工单，然后再开一张单来问为什么。
func TestTicketAuthorMapping(t *testing.T) {
	if got := ticketAuthor(dbgen.TicketActorUser); got != gen.TicketMessageAuthorUser {
		t.Errorf("user → %q", got)
	}
	if got := ticketAuthor(dbgen.TicketActorAgent); got != gen.TicketMessageAuthorStaff {
		t.Errorf("agent → %q", got)
	}
	if got := ticketAuthor(dbgen.TicketActorSystem); got != gen.TicketMessageAuthorStaff {
		t.Errorf("system → %q，系统消息必须仍然可见（映射成 staff），不能被丢掉", got)
	}
}

// ============================================================
// 分类
// ============================================================

func TestTicketCategoryPassThrough(t *testing.T) {
	srv := ticketTestServer()
	ctx := context.Background()
	for _, slug := range []string{"subscription", "node-down", "billing", "account"} {
		if got := srv.ticketCategory(ctx, slug); string(got) != slug {
			t.Errorf("%q → %q", slug, got)
		}
	}
	// 运营新建的分类会拿到一个不在枚举里的 slug。原样透传 + WARN，
	// 硬映射成 account 会让客服按错误的分类去处理。
	if got := srv.ticketCategory(ctx, "network"); string(got) != "network" {
		t.Errorf("未知 slug 被改写成了 %q", got)
	}
	if got := srv.ticketCategory(ctx, ""); string(got) != "" {
		t.Errorf("空 slug（分类被删过）被编造成了 %q", got)
	}
}

// ============================================================
// 工单号
// ============================================================

func TestNewTicketPublicID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id, err := newTicketPublicID()
		if err != nil {
			t.Fatalf("newTicketPublicID: %v", err)
		}
		if !strings.HasPrefix(id, ticketPublicIDPrefix) {
			t.Fatalf("工单号 %q 缺前缀", id)
		}
		body := strings.TrimPrefix(id, ticketPublicIDPrefix)
		if len(body) != ticketPublicIDLen {
			t.Fatalf("工单号 %q 长度不对", id)
		}
		// public_id 会被用户念给客服听、会被抄进邮件。
		if strings.ContainsAny(body, "01OIL") {
			t.Fatalf("工单号 %q 含易混字符", id)
		}
		seen[id] = true
	}
	if len(seen) < 190 {
		t.Fatalf("200 次生成只得到 %d 个不同的工单号", len(seen))
	}
}

// ============================================================
// 客户端自述快照
// ============================================================

func TestClientReportedJSON(t *testing.T) {
	srv := ticketTestServer()
	ctx := context.Background()

	if got := srv.clientReportedJSON(ctx, 1, nil); got != nil {
		t.Errorf("没传 context 时应为 nil，got %s", got)
	}

	c := &gen.TicketClientContext{
		DeviceCount: ptrOf(int32(3)),
		PlanName:    ptrOf("标准"),
		UsedBytes:   ptrOf(int64(123)),
	}
	got := srv.clientReportedJSON(ctx, 1, c)
	if got == nil {
		t.Fatal("正常快照被丢弃了")
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("序列化结果不是合法 JSON：%v", err)
	}
	if m["device_count"] != float64(3) {
		t.Errorf("device_count = %v", m["device_count"])
	}
}

// 🔴 tickets.context 是 jsonb 且被 GIN 索引（tickets_context_idx），
// 还会被 SearchTicketsByContext 全库检索、会进导出。
// 客户端那份是 additionalProperties 开放的对象 —— 不设上限的话，
// 一次建单就能往 GIN 索引里塞进一兆垃圾，而受害的是所有人的工单搜索。
func TestClientReportedJSONDropsOversize(t *testing.T) {
	srv := ticketTestServer()
	huge := map[string]any{}
	for i := 0; i < 2000; i++ {
		huge[strings.Repeat("k", 20)+string(rune('a'+i%26))+strings.Repeat("x", 10)+itoa(i)] = strings.Repeat("v", 40)
	}
	c := &gen.TicketClientContext{AdditionalProperties: huge}

	got := srv.clientReportedJSON(context.Background(), 1, c)
	if got != nil {
		t.Fatalf("超大的客户端快照被写进了 GIN 索引（%d 字节）", len(got))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// ============================================================
// 校验分支（不碰数据库）
// ============================================================

// 标题与正文按 **rune** 计长：128 字节只够 42 个汉字，而契约说的是 128 个字符。
func TestCreateTicketValidatesLengthsByRune(t *testing.T) {
	srv := ticketTestServer()
	ctx := withUser(42)

	ok := gen.CreateTicketRequestObject{Body: &gen.CreateTicketJSONRequestBody{
		Category: gen.NodeDown,
		Subject:  strings.Repeat("节", ticketSubjectMaxRunes), // 128 个汉字，384 字节
		Message:  "连不上",
	}}
	// 128 个汉字是**合法**的；走到这里会去调限流器（nil）→ panic 才说明校验放行了。
	// 用 recover 把「放行」这件事变成一个可断言的事实，同时不需要真的起数据库。
	if got := passesTicketLengthCheck(t, srv, ctx, ok); !got {
		t.Fatal("128 个汉字的标题被判成超长 —— 长度按字节算了")
	}

	tooLong := gen.CreateTicketRequestObject{Body: &gen.CreateTicketJSONRequestBody{
		Category: gen.NodeDown,
		Subject:  strings.Repeat("节", ticketSubjectMaxRunes+1),
		Message:  "连不上",
	}}
	if passesTicketLengthCheck(t, srv, ctx, tooLong) {
		t.Fatal("超长标题没有被挡住")
	}
}

// passesTicketLengthCheck 跑一次 CreateTicket，返回「是否通过了长度校验」。
//
// 通过校验之后下一步就是限流器（本测试里是 nil），必然 panic —— 用 recover 捕获。
// 这不优雅，但它换来的是：长度校验的**真实调用路径**被覆盖到了，
// 而不是只测一个被抽出来的纯函数（抽出来的那个可能根本没被 handler 调用）。
func passesTicketLengthCheck(t *testing.T, s *Server, ctx context.Context, req gen.CreateTicketRequestObject) (passed bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			passed = true // 走到了限流器 = 长度校验通过
		}
	}()
	resp, err := s.CreateTicket(ctx, req)
	if err != nil {
		t.Fatalf("CreateTicket 返回错误：%v", err)
	}
	if _, is422 := resp.(gen.CreateTicket422JSONResponse); is422 {
		return false
	}
	t.Fatalf("既没有 422 也没有走到限流器，响应类型 %T", resp)
	return false
}

func TestCreateTicketRejectsEmptyBody(t *testing.T) {
	srv := ticketTestServer()
	resp, err := srv.CreateTicket(withUser(1), gen.CreateTicketRequestObject{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if _, ok := resp.(gen.CreateTicket422JSONResponse); !ok {
		t.Fatalf("空请求体应当 422，got %T", resp)
	}
}

func TestCreateTicketMessageRejectsBlank(t *testing.T) {
	srv := ticketTestServer()
	for _, msg := range []string{"", "   ", "\n\t "} {
		resp, err := srv.CreateTicketMessage(withUser(1), gen.CreateTicketMessageRequestObject{
			PublicId: "BP-7K2M9Q",
			Body:     &gen.CreateTicketMessageJSONRequestBody{Message: msg},
		})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if _, ok := resp.(gen.CreateTicketMessage422JSONResponse); !ok {
			t.Fatalf("空白正文应当 422，got %T", resp)
		}
	}
}

// 🔴 装配错误（路由没挂用户会话中间件）必须变成 500，不是 401。
// 逐字理由见 errNoUserAuth 与 middleware/user.go 的 UserFrom 注释：
// 回 401 会让每个用户反复重新登录并开出一堆指向账号系统的工单。
func TestMissingSessionIsInternalNotUnauthorized(t *testing.T) {
	srv := ticketTestServer()
	ctx := context.Background() // 没有用户身份

	if _, err := srv.ListTickets(ctx, gen.ListTicketsRequestObject{}); !errors.Is(err, errNoUserAuth) {
		t.Errorf("ListTickets 缺身份时 err = %v", err)
	}
	if _, err := srv.GetTicket(ctx, gen.GetTicketRequestObject{PublicId: "BP-1"}); !errors.Is(err, errNoUserAuth) {
		t.Errorf("GetTicket 缺身份时 err = %v", err)
	}
	if _, err := srv.CloseTicket(ctx, gen.CloseTicketRequestObject{PublicId: "BP-1"}); !errors.Is(err, errNoUserAuth) {
		t.Errorf("CloseTicket 缺身份时 err = %v", err)
	}
	if _, err := srv.GetWallet(ctx, gen.GetWalletRequestObject{}); !errors.Is(err, errNoUserAuth) {
		t.Errorf("GetWallet 缺身份时 err = %v", err)
	}
	if _, err := srv.ListUserDevices(ctx, gen.ListUserDevicesRequestObject{}); !errors.Is(err, errNoUserAuth) {
		t.Errorf("ListUserDevices 缺身份时 err = %v", err)
	}
}

// ============================================================
// 冲突响应的形状
// ============================================================

// 409 与 404 在用户界面上的动作完全不同（重开这张单 vs 换一张单），
// 所以两条路径必须给出不同的码。
func TestTicketConflictAndNotFoundShapes(t *testing.T) {
	srv := testServer()
	ctx := context.Background()

	conflict := gen.CreateTicketMessage409JSONResponse{ErrConflictJSONResponse: srv.conflict(ctx, "该工单已关闭")}
	rec := httptest.NewRecorder()
	if err := conflict.VisitCreateTicketMessageResponse(rec); err != nil {
		t.Fatalf("Visit: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("状态码 = %d, want 409", rec.Code)
	}
	var env gen.ErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("不是统一信封：%v", err)
	}
	if env.Error.Code != gen.STATECONFLICT {
		t.Errorf("code = %q", env.Error.Code)
	}

	notFound := gen.CreateTicketMessage404JSONResponse{ErrNotFoundJSONResponse: srv.notFound(ctx, "工单不存在")}
	rec2 := httptest.NewRecorder()
	if err := notFound.VisitCreateTicketMessageResponse(rec2); err != nil {
		t.Fatalf("Visit: %v", err)
	}
	if rec2.Code != http.StatusNotFound {
		t.Errorf("状态码 = %d, want 404", rec2.Code)
	}
	var env2 gen.ErrorEnvelope
	if err := json.Unmarshal(rec2.Body.Bytes(), &env2); err != nil {
		t.Fatalf("不是统一信封：%v", err)
	}
	if env2.Error.Code != gen.RESOURCENOTFOUND {
		t.Errorf("code = %q", env2.Error.Code)
	}
	if env.Error.Code == env2.Error.Code {
		t.Fatal("409 与 404 用了同一个错误码 —— 前端分不出「换一张单」与「重开这张单」")
	}
}
