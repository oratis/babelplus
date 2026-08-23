package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/httpx"
	"github.com/oratis/babelplus/api/internal/middleware"
	"github.com/oratis/babelplus/api/internal/subgen"
)

// 订阅下发（`GET /s/{token}` 与 `GET /api/v1/client/subscribe`）。
//
// 事实源：api-contract.md §4、data-model.md §5、openapi.yaml 的两个 operation。
// 两条路由走**完全相同的 handler 与鉴权链**，只是 token 的位置不同。
//
// 这是整个产品的核心价值：用户付了钱，拿到的就是这一条 URL 返回的东西。
// 它同时承担三件互相纠缠的事 —— 格式协商、用量回显、以及
// **「订阅 URL 本身就是通知通道」**（user-journey §11.2）：它是「邮箱收不到、
// Telegram 连不上、主站被封时仍然能触达用户」的唯一通道。
//
// # 四条不能动的判定（api-contract §4.2 / ADR 0006 §10.2）
//
//  1. 形态不合法 → **直接 404，不查库**。省一次数据库往返，也让针对 token 表的
//     探测拿不到时序差异。
//  2. 不存在 / 已吊销 / `issued_at < users.sub_revoked_at` → **一律 404，不是 403**。
//     403 会告诉攻击者「这个 token 存在但你不能用」—— 那正是枚举者要的信号。
//  3. **banned / 到期 / 配额耗尽不走 404**，它们是 **200 + 空节点列表 + 伪节点**。
//     被封禁的用户看到「所有节点消失」会开工单；看到伪节点写着原因会去申诉。
//  4. 每次拉取**同步**写 `subscription_fetch_log`。这是唯一能识别账号共享的数据来源
//     （system-design §5.2），异步会引入「审计缺失且无人知道」这类最坏的失败模式。
//
// # 不设 ETag（刻意）
//
// 订阅内容内嵌当前用量数字，每次都在变；更要紧的是 304 会让客户端继续显示
// **旧的流量条**，而流量条恰恰是用户判断「我还剩多少」的唯一入口。
// 节点面那套 ETag 协商在这里是负收益，不要照搬。

// ---- 常量 ----

const (
	// subProfileUpdateInterval 是客户端自动更新间隔，单位小时（api-contract §4.4）。
	subProfileUpdateInterval = 24

	// subContentDisposition 决定客户端里显示的订阅名。固定值，照抄契约。
	subContentDisposition = `attachment; filename*=UTF-8''babel.plus`

	// subCacheControl：订阅内容内嵌用量数字，任何一层缓存都会让用户看到过期数据。
	subCacheControl = "no-store"

	// subTokenMinLen / subTokenMaxLen 与 openapi 的 SubscribeTokenPath/Query
	// （minLength 16 / maxLength 64）**逐字对齐**。改一处必须改另一处。
	subTokenMinLen = 16
	subTokenMaxLen = 64

	// subNoExpiryUnix 是不限时套餐（users.expired_at IS NULL）下 `expire` 的取值：
	// 4102444800 = 2100-01-01T00:00:00Z。
	//
	// 🔴 **这是提案，不是裁决。** glossary 与 api-contract §4.4 都把这一条标为「未裁决」：
	// Xboard 的行为是输出空值（`expire=`），而**部分客户端会把空值当 0 处理，
	// 渲染成「1970-01-01 已过期」** —— 一个付了不限时套餐的用户打开客户端看到
	// 「已过期」，是本系统能造出的最糟糕的用户体验之一。
	// 本实现把「客户端可能渲染错」的风险换成「显示 2100 年到期」的确定行为。
	// **必须实测三个客户端后再定**，实测结果若相反，改这一个常量即可。
	subNoExpiryUnix int64 = 4102444800
)

// ---- 错误 ----

// errNoBoundRequest 表示原始 *http.Request 没有被注入上下文。
//
// 这是**装配错误**，不是用户错误：订阅下发必须拿到 User-Agent（决定格式）
// 与来源 IP（写审计表），而 oapi-codegen 的 strict 接口只给 ctx。
// 缺了绑定中间件就意味着「格式全部退化成 base64 且审计 IP 全是 0.0.0.0」——
// 那是一种**能正常返回 200 的静默失效**，所以这里必须响亮地 500。
var errNoBoundRequest = errors.New("订阅下发缺少原始请求：路由未挂载 handler.RequestBinding() 中间件")

// ---- 原始请求绑定 ----

type ctxKeyBoundRequest struct{}

// RequestBinding 把原始 *http.Request 放进上下文。
//
// 为什么需要它：oapi-codegen 的 StrictServerInterface 只把 ctx 和解包后的参数
// 交给 handler，**拿不到请求头**。订阅下发必须读 User-Agent（§4.3 的格式分发）
// 与来源 IP（§4.2 步 7 的审计），两者都只在原始请求里。
//
// 为什么不按 operation 过滤：过滤就要维护第二张「哪些 operation 需要原始请求」的表，
// 而那张表漏一行的现象是「某个端点静默退化」。一次 context.WithValue 的成本
// 远低于维护那张表的风险。
//
// 装配位置见 cmd/server/main.go 的 StrictMiddlewareFunc 列表。
func RequestBinding() gen.StrictMiddlewareFunc {
	return func(f gen.StrictHandlerFunc, _ string) gen.StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			return f(context.WithValue(ctx, ctxKeyBoundRequest{}, r), w, r, request)
		}
	}
}

// boundRequestFrom 取出原始请求。
func boundRequestFrom(ctx context.Context) (*http.Request, bool) {
	r, ok := ctx.Value(ctxKeyBoundRequest{}).(*http.Request)
	return r, ok
}

// ---- StrictServerInterface 实现 ----

// GetShortSubscription 是短路径 `GET /s/{token}` —— **默认对外发这一条**
// （短、无 query、不易被聊天软件截断）。
func (s *Server) GetShortSubscription(ctx context.Context, req gen.GetShortSubscriptionRequestObject) (gen.GetShortSubscriptionResponseObject, error) {
	var flag string
	if req.Params.Flag != nil {
		flag = string(*req.Params.Flag)
	}
	return s.serveSubscription(ctx, req.Token, flag)
}

// GetClientSubscription 是与 Xboard 同形的长路径 `GET /api/v1/client/subscribe?token=`。
// 存在的理由只有一个：方便从竞品/旧配置迁移。语义与短路径**完全相同**。
func (s *Server) GetClientSubscription(ctx context.Context, req gen.GetClientSubscriptionRequestObject) (gen.GetClientSubscriptionResponseObject, error) {
	var flag string
	if req.Params.Flag != nil {
		flag = string(*req.Params.Flag)
	}
	return s.serveSubscription(ctx, req.Params.Token, flag)
}

// serveSubscription 是两条路由共用的实现。
func (s *Server) serveSubscription(ctx context.Context, rawToken, flag string) (*subscriptionResponse, error) {
	r, ok := boundRequestFrom(ctx)
	if !ok {
		return nil, errNoBoundRequest
	}
	return deliverSubscription(ctx, s.subDeps(), r, rawToken, flag), nil
}

// ---- 依赖 ----

// subscriptionStore 是订阅下发用到的**最小**数据库能力。
//
// 收窄成接口而不是直接吃 *store.Store，是为了让「banned 用户拿到 200 + 伪节点」
// 这类判定能在没有数据库的前提下被断言 —— 与 middleware.NodeAuthenticator 同一条纪律。
type subscriptionStore interface {
	ResolveSubscriptionToken(ctx context.Context, tokenHash []byte) (dbgen.ResolveSubscriptionTokenRow, error)
	GetSubscriptionUsage(ctx context.Context, userID int64) (dbgen.GetSubscriptionUsageRow, error)
	ListVisibleServersForUser(ctx context.Context, groupID int64) ([]dbgen.Server, error)
	InsertSubscriptionFetchLog(ctx context.Context, arg dbgen.InsertSubscriptionFetchLogParams) (dbgen.InsertSubscriptionFetchLogRow, error)
	TouchSubscriptionToken(ctx context.Context, arg dbgen.TouchSubscriptionTokenParams) error
}

// subDeps 是订阅下发的全部外部依赖。
type subDeps struct {
	db         subscriptionStore
	pepper     string
	trustProxy bool
	// webBaseURL 形如 https://web.babel.plus（无尾斜杠）。用于 profile-web-page-url
	// 与伪节点名里的「<web 域名>」。
	webBaseURL string
	logger     *slog.Logger
}

// subDeps 从 Server 组装依赖。
//
// ⚠️ webBaseURL 目前取 CORS 白名单的第一项，这是**将就**：
// config 里没有独立的「Web 主域名」配置项，而 CORS 白名单恰好就是 Web 的 Origin 列表。
// TODO(P2): 加一个 BP_WEB_BASE_URL —— ADR 0002 的失联恢复会轮换 Web 域名，
// 届时「订阅里印的是哪个域名」是一个必须能单独控制的量，
// 借用 CORS 白名单意味着「放行某个 Origin」和「告诉用户去哪个域名续费」被绑死了。
// 这一条不在本轮的文件范围内（config 是别人的文件），故只登记不动手。
func (s *Server) subDeps() subDeps {
	var web string
	if len(s.cfg.AllowedOrigins) > 0 {
		web = strings.TrimRight(s.cfg.AllowedOrigins[0], "/")
	}
	return subDeps{
		db:         s.db,
		pepper:     s.cfg.SubscriptionTokenPepper,
		trustProxy: s.cfg.TrustProxyHeaders,
		webBaseURL: web,
		logger:     s.logger,
	}
}

// ---- 主流程 ----

// deliverSubscription 执行 api-contract §4.2 的十步。
//
// 返回值永远非 nil：这个端点没有「返回错误让上层映射」的分支 ——
// 任何内部失败都必须落到 404（防枚举）或 200 + 伪节点（通知通道），
// 500 只留给装配错误。
func deliverSubscription(ctx context.Context, d subDeps, r *http.Request, rawToken, flag string) *subscriptionResponse {
	// 步 1：形态校验。不合法直接 404，**不查库**。
	if !plausibleSubscriptionToken(rawToken) {
		return notFoundResponse()
	}

	// 步 2–6：查库并判定。四种 404 情形（不存在 / 已吊销 / token 自身过期 /
	// issued_at < sub_revoked_at）在 SQL 层就已经**不可区分** —— 这不是偷懒，
	// 是刻意的：Go 侧拿不到差异，就没有任何一条日志或错误分支能把差异泄漏出去。
	tok, err := d.db.ResolveSubscriptionToken(ctx, subscriptionTokenHash(d.pepper, rawToken))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			// 查库失败也返回 404 而不是 500：对调用方而言两者的区别只有
			// 「这个 token 到底存不存在」这一个信息，而那正是不能泄漏的东西。
			// 运维侧靠这条 ERROR 日志发现，不靠状态码。
			d.logger.ErrorContext(ctx, "订阅 token 查询失败", "err", err)
		}
		// ⚠️ 已知缺口：**404 无法写进 subscription_fetch_log**。
		// 该表的 user_id 是 NOT NULL，而走到这里恰恰意味着我们不知道 user_id。
		// 后果：「有人拿着已吊销的 token 一直在刷」在审计表里看不见。
		// 修法需要 DDL 改动（user_id 可空，或另开一张 anonymous 拉取表），
		// 不在本轮范围。当前只在应用日志里留一条 —— 注意**不要**把 token 明文写进日志。
		d.logger.InfoContext(ctx, "订阅 token 无效，返回 404",
			"client_ip", middleware.ClientIP(r, d.trustProxy),
			"user_agent", r.UserAgent())
		return notFoundResponse()
	}

	// 步 9（提前）：先定格式，因为审计表要记 format 与 client_flag。
	format, clientFlag := negotiateFormat(r.UserAgent(), flag)

	// 步 8：可用性判定。**这里不返回 404** —— banned / 到期 / 配额耗尽
	// 一律 200 + 空列表 + 伪节点（api-contract §4.6）。
	usage, err := d.db.GetSubscriptionUsage(ctx, tok.UserID)
	if err != nil {
		// 用量查不到不该让用户断网：把用量当 0 继续下发节点，
		// subscription-userinfo 的流量条会显示 0，比「所有节点消失」好得多。
		d.logger.ErrorContext(ctx, "订阅用量查询失败，按 0 继续下发", "user_id", tok.UserID, "err", err)
	}

	state := accountStateOf(tok, usage)

	var proxies []subgen.Proxy
	if state == stateActive {
		servers, err := d.db.ListVisibleServersForUser(ctx, tok.GroupID)
		if err != nil {
			d.logger.ErrorContext(ctx, "订阅节点列表查询失败", "user_id", tok.UserID, "err", err)
		}
		proxies = buildProxies(ctx, d.logger, servers, tok.Uuid.String())
	}
	nodeCount := len(proxies)

	if len(proxies) == 0 {
		// 伪节点必须是**语法合法但连不上**的条目，否则部分客户端会因为
		// 「proxies 为空」拒绝导入整份配置 —— 用户连这句话都看不到。
		proxies = []subgen.Proxy{subgen.NoticeProxy(noticeName(state, d.webBaseURL))}
	}

	body, err := subgen.Render(format, subgen.Document{Proxies: proxies})
	if err != nil {
		// Render 只在「文档为空」或「格式未知」时出错，两者都已在上面挡掉。
		// 真出错说明有 bug —— 但仍然不能给用户一个 500 的订阅 URL，
		// 那会让客户端把订阅标记为失效并停止重试。降级成一条伪节点。
		d.logger.ErrorContext(ctx, "订阅渲染失败，降级为伪节点", "format", format, "err", err)
		body, _ = subgen.Render(subgen.FormatBase64,
			subgen.Document{Proxies: []subgen.Proxy{subgen.NoticeProxy(noticeName(stateInternalError, d.webBaseURL))}})
		format, nodeCount = subgen.FormatBase64, 0
	}

	// 步 7：**同步**写审计（api-contract §4.2 的实现层修正）。
	// 量级 10³ 行/天、一次 INSERT < 1 ms —— 用这个成本换掉「审计缺失且无人知道」。
	writeFetchLog(ctx, d, r, tok, clientFlag, format, nodeCount)

	// token 留痕：允许失败，且**异步** —— 与 middleware.TouchKeyLastUsed 同理，
	// 不要为了一个运营字段把纯读路径变成写路径。
	touchToken(d, r, tok.TokenID)

	// 步 10：响应头。
	return &subscriptionResponse{
		status:      http.StatusOK,
		contentType: format.ContentType(),
		headers:     subscriptionHeaders(d.webBaseURL, tok, usage),
		body:        body,
	}
}

// ---- token ----

// subTokenAlphabet 是订阅 token 的字符集：URL-safe base64（RFC 4648 §5）。
//
// 🔴 **校验端与签发端必须共用这一个常量。**
// 签发端（POST /api/v1/user/subscription/tokens）尚未实现；它落地时**不要**再写一遍
// 字符集，而是从这里取，随机取字符也从这里取。
//
// 两边各写一份的失败模式极其难查：签发端多放进一个字符（比如 '+' 或 '='），
// 那枚 token 会被本函数在**查库之前**判成形态非法 → 直接 404，
// 而 404 恰恰又是「token 不存在 / 已吊销」的正常返回（ADR 0006 §10.2 规定不泄露存在性）。
// 于是现象是「刚签发的订阅链接立刻 404」，签发端和校验端各自看都毫无问题，
// 数据库里那行 subscription_tokens 也好端端地在。
//
// 为什么是 URL-safe base64：token 出现在路径段 `/s/{token}` 里，
// 任何需要百分号编码的字符都会让用户手工复制的链接更容易被聊天软件截断。
const subTokenAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// plausibleSubscriptionToken 做一次廉价的形态校验，**在查库之前**。
//
// 按字节而不是按 rune 遍历：非 ASCII 字符的每个字节都 ≥ 0x80，一个都不在
// subTokenAlphabet 里，所以行为与按 rune 判等价，而少一次 UTF-8 解码。
func plausibleSubscriptionToken(s string) bool {
	if len(s) < subTokenMinLen || len(s) > subTokenMaxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(subTokenAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// subscriptionTokenHash 计算查库用的哈希。
//
// 加 pepper（BP_SUBSCRIPTION_TOKEN_PEPPER，在 Secret Manager 里、不落库）：
// 只拿到数据库的攻击者无法离线爆破 token —— token 熵足够时爆破本就不可行，
// 但 pepper 让「运营手工建的短 token」这类错误不会立刻变成灾难。
// 与节点密钥用**不同**的 pepper，一处泄漏不波及两个面。
//
// 🔴 与签发端必须逐字一致（同样的 pepper、同样的拼接顺序）。
func subscriptionTokenHash(pepper, token string) []byte {
	sum := sha256.Sum256([]byte(pepper + token))
	return sum[:]
}

// ---- 账号状态 ----

type accountState int

const (
	stateActive accountState = iota
	stateBanned
	stateExpired
	stateQuotaExhausted
	// stateInternalError 不在 api-contract §4.6 的表里，是本实现加的兜底：
	// 与其在渲染失败时给客户端一个 5xx（客户端会把订阅标记为失效并停止重试），
	// 不如给一条能被用户读到、能变成工单的伪节点。
	stateInternalError
)

// accountStateOf 判定账号状态。
//
// 判定顺序 banned → 到期 → 配额，是刻意的：一个既被封禁又已到期的账号，
// 用户需要看到的是「已停用，去申诉」而不是「已到期，去续费」——
// 后者会让他付了钱之后发现还是不能用。
func accountStateOf(tok dbgen.ResolveSubscriptionTokenRow, usage dbgen.GetSubscriptionUsageRow) accountState {
	if tok.Banned {
		return stateBanned
	}
	// expired_at IS NULL 天然支撑「不限时套餐」，不是「已过期」。
	if tok.ExpiredAt.Valid && !tok.ExpiredAt.Time.After(time.Now()) {
		return stateExpired
	}
	// 与节点面 ListAvailableUsersByServer 的判定同形：u + d < transfer_enable。
	// 两处必须一致，否则会出现「订阅里有节点但节点不认这个用户」的经典幽灵故障。
	if usage.U+usage.D >= tok.TransferEnable {
		return stateQuotaExhausted
	}
	return stateActive
}

// noticeName 生成伪节点名。**节点名即说明书** —— 这是竞品验证过的做法，
// 也是订阅通道能传达的全部信息量（api-contract §4.6 的表）。
//
// ⚠️ 各客户端对节点名长度与特殊字符（emoji、空格、`·`）的渲染差异 **待核实**
// （user-journey §16 已登记为伪节点通道的前提）。这里的 emoji 与 `·`
// 直接来自契约的表，没有自创。
func noticeName(state accountState, webBaseURL string) string {
	host := displayHost(webBaseURL)
	suffix := ""
	if host != "" {
		suffix = " " + host
	}
	switch state {
	case stateBanned:
		return "⚠️ 账号已停用 · 请提交工单" + suffix
	case stateExpired:
		return "⚠️ 订阅已到期 · 续费" + suffix
	case stateQuotaExhausted:
		return "⚠️ 流量已用尽 · 购买流量包" + suffix
	case stateInternalError:
		return "⚠️ 订阅生成异常 · 请提交工单" + suffix
	default:
		// stateActive 走到这里说明「可用但一个节点都没有」——
		// 运营还没建节点，或者全部节点的协议参数都不完整。
		return "⚠️ 暂无可用节点 · 请提交工单" + suffix
	}
}

// displayHost 从 base URL 里取出给用户看的域名。
func displayHost(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	}
	return u.Host
}

// ---- 格式协商 ----

// uaRule 是一条 User-Agent 匹配规则。
type uaRule struct {
	substr string // 已小写
	format subgen.Format
	client string // 写进 subscription_fetch_log.client_flag
}

// uaRules 是 api-contract §4.3 的 UA 子串表，**按表内顺序取第一个命中**，
// 匹配不区分大小写。
//
// 🔴 **整张表在 api-contract 里标着「需实测」** —— 各客户端的真实 UA 必须逐个抓取
// 确认，不能靠推断。这张表错一行，对应客户端的用户就会拿到 base64 而不是 YAML，
// 现象是「导入后没有分组，只有一堆裸节点」（user-journey §L2「导入失败」层）。
// 本表**逐字照抄契约的子串**，没有自创任何一条。下面标注的是推断部分：
//
//   - `sfi` / `sfa` / `sfm` / `sft` 是 sing-box 官方客户端在 iOS / Android /
//     macOS / tvOS 上的名称缩写（**推断**：契约给了这四个大写缩写但没给完整 UA）。
//     三字母子串的误命中风险明显高于其他行 —— 一旦实测拿到完整 UA，
//     应当把它们换成带斜杠或版本号的更长子串。
//   - `karing` / `hiddify` 的完整 UA **未验证**，契约只给了产品名。
//   - `v2rayng` 排在 `v2rayn` **之前**是本实现加的：两者格式相同（base64），
//     顺序不影响下发内容，但影响 client_flag 记的是哪个客户端 ——
//     `v2rayn` 是 `v2rayng` 的前缀，反过来排会把所有 v2rayNG 记成 v2rayN。
var uaRules = []uaRule{
	{"clash-verge", subgen.FormatClash, "clash-verge"},
	{"clash.meta", subgen.FormatClash, "clash-meta"},
	{"mihomo", subgen.FormatClash, "mihomo"},
	{"clash", subgen.FormatClash, "clash"},
	{"sing-box", subgen.FormatSingbox, "singbox"},
	{"sfi", subgen.FormatSingbox, "singbox"},
	{"sfa", subgen.FormatSingbox, "singbox"},
	{"sfm", subgen.FormatSingbox, "singbox"},
	{"sft", subgen.FormatSingbox, "singbox"},
	{"karing", subgen.FormatSingbox, "karing"},
	{"hiddify", subgen.FormatSingbox, "hiddify"},
	{"v2rayng", subgen.FormatBase64, "v2rayng"},
	{"v2rayn", subgen.FormatBase64, "v2rayn"},
	{"shadowrocket", subgen.FormatBase64, "shadowrocket"},
}

// negotiateFormat 按 UA 选格式，`?flag=` 强制覆盖（照抄 Xboard 的 flag 参数语义）。
//
// 返回的 clientFlag **始终来自 UA**，即使 flag 覆盖了格式：
// 审计表要回答的是「谁在拉」，不是「他要了什么格式」。format 列另记后者。
func negotiateFormat(userAgent, flag string) (subgen.Format, string) {
	ua := strings.ToLower(userAgent)
	format := subgen.FormatBase64 // 未匹配的兜底（契约表第 6 行）
	client := "unknown"
	for _, rule := range uaRules {
		if strings.Contains(ua, rule.substr) {
			format, client = rule.format, rule.client
			break
		}
	}
	switch subgen.Format(flag) {
	case subgen.FormatClash, subgen.FormatSingbox, subgen.FormatBase64:
		format = subgen.Format(flag)
	}
	return format, client
}

// ---- 节点组装 ----

// subProtocolSettings 是 servers.protocol_settings（jsonb）里与**客户端**相关的字段。
//
// 🔴 **键名必须与 node.go 的 nodeProtocolSettings 对齐** —— 那是同一列的另一半读者。
// 两边各起一套名字的后果是运营要把同一个值填两遍（`short_id` 和 `reality_short_id`），
// 而只填一遍的现象是「节点起来了但客户端连不上」，两边配置各看各的都「没问题」。
// 这里复用的键：`server_name` / `reality_short_id` / `obfs` / `obfs_password` / `cipher`。
//
// 本结构体独有的两个键（节点侧不需要，所以 nodeProtocolSettings 里没有）：
//
//	reality_public_key  —— 客户端要用的 REALITY 公钥
//	client_fingerprint  —— uTLS 指纹（**不是**证书 pin，见 protocol-and-infra §5.4.3）
//
// ⚠️ **私钥类字段绝不出现在本结构体里。** node.go 那侧有 reality_private_key /
// server_key / obfs_password 三个凭据；订阅是**公开可拉取**的响应，
// 把私钥读进这条路径等于给「某天有人加一行调试输出」留了一条泄漏路。
// 所以这里显式只声明客户端需要的字段 —— 手滑加一行不够，得先写一个新字段。
// （obfs_password 是例外：它本来就要发给客户端，否则 obfs 无从协商。）
//
// ⚠️ **需实测/需运营纪律**：reality_public_key 与 node.go 的 reality_private_key
// 必须是同一对密钥。两者不匹配的现象是「配置全对但握手失败」，且两边单独看都合法。
// TODO(P2): 建节点的后台接口应当由 private key 推导 public key 后落库，
// 而不是让运营各填一次 —— 那要改管理面，不在本轮范围。
type subProtocolSettings struct {
	ServerName string `json:"server_name"`

	// VLESS + REALITY（客户端侧）
	RealityPublicKey  string `json:"reality_public_key"`
	RealityShortID    string `json:"reality_short_id"`
	ClientFingerprint string `json:"client_fingerprint"`

	// Hysteria2
	Obfs         string `json:"obfs"`
	ObfsPassword string `json:"obfs_password"`

	// Shadowsocks-2022
	Cipher string `json:"cipher"`
}

// 协议参数的默认值。运营没填时用它们，避免「后台少填一个字段 = 整个节点消失」。
//
// flow 与 obfs 刻意**直接引用 node.go 的常量**（vlessRealityFlow /
// defaultHysteriaObfs），不另写一份字面量：这两个值节点侧与客户端侧必须一字不差，
// 各写一份的后果是某次改动只改了一边，而现象是「握手失败」而不是编译错误。
// 同包共享常量把这件事变成编译期约束。
//
// SS-2022 的 cipher 默认值刻意**没有**在这里定义：那条分支当前不下发
// （见 buildProxies 的 shadowsocks2022 分支）。重新启用时要与 node.go 里
// `settingOr(ps.Cipher, "2022-blake3-aes-128-gcm")` 的字面量对齐。
const subDefaultFingerprint = "chrome"

// buildProxies 把 servers 行映射成协议无关的下发节点。
//
// 参数不完整的节点会被**跳过并打 ERROR 日志**，而不是下发一个残缺条目：
// 残缺条目在客户端里是「能导入、能显示、连不上」，用户会把它当成节点故障来报，
// 而真正的原因在后台的一个空字段里 —— 那是排查成本最高的一类故障。
func buildProxies(ctx context.Context, logger *slog.Logger, servers []dbgen.Server, uuid string) []subgen.Proxy {
	out := make([]subgen.Proxy, 0, len(servers))
	for _, srv := range servers {
		var ps subProtocolSettings
		if len(srv.ProtocolSettings) > 0 {
			if err := json.Unmarshal(srv.ProtocolSettings, &ps); err != nil {
				logger.ErrorContext(ctx, "节点 protocol_settings 不是合法 JSON，已跳过",
					"server_code", srv.Code, "err", err)
				continue
			}
		}

		p := subgen.Proxy{
			Name:   srv.Name,
			Server: srv.Host,
			Port:   int(srv.Port),
		}

		switch srv.Protocol {
		case dbgen.ServerProtocolVlessReality:
			if ps.RealityPublicKey == "" || ps.ServerName == "" {
				logger.ErrorContext(ctx, "REALITY 节点缺少 reality_public_key 或 server_name，已跳过",
					"server_code", srv.Code)
				continue
			}
			p.Kind = subgen.KindVLESSReality
			p.UUID = uuid
			p.SNI = ps.ServerName
			p.PublicKey = ps.RealityPublicKey
			p.ShortID = ps.RealityShortID
			// flow 与节点侧共用同一个常量：两边不一致 = 握手失败。
			p.Flow = vlessRealityFlow
			p.Fingerprint = subDefault(ps.ClientFingerprint, subDefaultFingerprint)
			// TCP 路径开 mux（ADR 0004 §裁决 2）：抗 TLS-in-TLS 指纹优先于吞吐。
			p.Mux = true

		case dbgen.ServerProtocolHysteria2:
			p.Kind = subgen.KindHysteria2
			// Hysteria2 的 password 是用户 uuid（XrayR / v2node 系的约定，
			// 与 api-contract §3.4 字段表里「SS-2022 侧直接把 uuid 当 password」同源）。
			p.Password = uuid
			p.SNI = ps.ServerName
			if ps.ObfsPassword != "" {
				// obfs 类型与节点侧共用常量。sing-box 1.13 稳定版只承认 salamander。
				p.ObfsType = subDefault(ps.Obfs, defaultHysteriaObfs)
				p.ObfsPassword = ps.ObfsPassword
			}
			// 🔴 不设任何带宽字段 —— ADR 0004 §裁决 1：用 BBR 不用 Brutal。
			// UDP 路径同样不开 mux（§裁决 2）。

		case dbgen.ServerProtocolShadowsocks2022:
			// 🔴 **暂不下发**，理由不是「懒得写」而是 blast radius。
			//
			// SS-2022 的客户端密码形态未确认：节点侧（node.go）会下发 `server_key`，
			// 说明服务端跑的是多用户（EIH）模式，而 EIH 下客户端密码是两段式
			// `{server_key}:{user_psk}`，不是 api-contract §3.4 说的单段 uuid
			// （那句话讲的是**节点**怎么识别用户，不是客户端怎么填密码）。
			//
			// 关键在于猜错的后果**不局限于这一个节点**：mihomo 在加载配置时会校验
			// 2022-* 系列 cipher 的 PSK 必须是定长 base64，而 uuid 里有 `-`、
			// 根本不是合法 base64 —— 一个填错的 SS-2022 节点会让**整份配置**加载失败，
			// 用户的 REALITY 与 Hysteria2 节点跟着一起消失。
			// 跳过只损失兜底通路，猜错则损失全部通路。
			//
			// subgen 侧的 SS-2022 渲染器已经写好并有单测，实测确认密码形态之后
			// 把这个分支改回构造 subgen.KindShadowsocks2022 即可。
			// TODO(P1): 接入首个 SS-2022 节点时实测客户端密码形态。
			logger.WarnContext(ctx, "shadowsocks2022 的客户端密码形态未实测，订阅暂不下发该节点",
				"server_code", srv.Code)
			continue

		case dbgen.ServerProtocolVlessXhttpCdn:
			// TODO(P2): VLESS + XHTTP over CDN 的**客户端**字段名（mihomo 的
			// xhttp-opts / sing-box 的 transport 块）在本仓没有任何事实源，
			// api-contract §4.5 的三个示例里也没有它。
			// 这条链路按 ADR 0004 是「应急、默认关闭」，所以宁可不下发，
			// 也不猜一组字段名 —— 猜错的现象是「应急通道在需要它的那天不能用」。
			logger.WarnContext(ctx, "vless_xhttp_cdn 尚未支持订阅下发，已跳过",
				"server_code", srv.Code)
			continue

		default:
			logger.ErrorContext(ctx, "未知节点协议，已跳过",
				"server_code", srv.Code, "protocol", string(srv.Protocol))
			continue
		}

		out = append(out, p)
	}
	return out
}

func subDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ---- 审计 ----

// writeFetchLog 同步写 subscription_fetch_log。
//
// 🔴 这是**唯一**能识别账号共享的数据来源（system-design §5.2 / data-model §5.3）：
// 面板把最近 10 次拉取（时间 / IP / UA）直接展示给用户，用户自己就能发现订阅被白嫖
// 并自助重置，不用开工单。
//
// 写失败**不阻断下发**：用户的连通性比一行审计重要，而且这一步失败时前面的读
// 多半也已经失败了。失败会打 ERROR —— 监控靠它，不靠状态码。
func writeFetchLog(ctx context.Context, d subDeps, r *http.Request, tok dbgen.ResolveSubscriptionTokenRow, clientFlag string, format subgen.Format, nodeCount int) {
	// 给审计单独一个短超时：同步写不等于「愿意为它无限等」。
	// 数据库慢的时候，先把订阅发出去比留下一行审计重要 —— 丢的那一条会打 ERROR。
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ip := clientAddr(r, d.trustProxy, d.logger)
	tokenID := tok.TokenID
	flagCopy := clientFlag
	formatCopy := string(format)
	// node_count 是 smallint：节点数不可能超过 int16，但显式截断比隐式溢出好。
	count := int16(min(nodeCount, 32767))

	if _, err := d.db.InsertSubscriptionFetchLog(ctx, dbgen.InsertSubscriptionFetchLogParams{
		UserID:     tok.UserID,
		TokenID:    &tokenID,
		RequestIp:  ip,
		UserAgent:  truncateUA(r.UserAgent()),
		ClientFlag: &flagCopy,
		StatusCode: http.StatusOK,
		Format:     &formatCopy,
		NodeCount:  &count,
	}); err != nil {
		d.logger.ErrorContext(ctx, "写订阅拉取审计失败（账号共享检测会缺这一条）",
			"user_id", tok.UserID, "err", err)
	}
}

// truncateUA 截断 User-Agent，并保证结果是合法 UTF-8。
//
// user_agent 列没有长度约束，而 UA 是**完全由调用方控制**的字符串 ——
// 不截断等于给了一条往库里塞任意大小数据的路径（每次拉取一行、无鉴权、可被脚本刷）。
// 512 字节远超真实客户端的 UA 长度。
//
// 🔴 **不能用 ua[:512] 直接切。** Go 的字符串切片按**字节**切，不对齐 rune 边界，
// 切在一个多字节 UTF-8 序列中间就会留下孤立的首字节。那串东西经 pgx 原样送到
// Postgres，服务端的编码校验报 22021 `invalid byte sequence for encoding "UTF8"`，
// InsertSubscriptionFetchLog 整条失败 —— 而失败路径只打一条 ERROR、订阅照常 200 下发，
// 于是**这次拉取在 subscription_fetch_log 里完全没有留痕**。
//
// 这不是理论问题：UA 由调用方控制，构造一个 511 字节 ASCII + 一个 `é` 的 UA 就能触发。
// 而这张表是识别账号共享的唯一数据来源（见本文件顶部与 system-design §5.2）——
// 也就是说共享订阅链接的人只要固定带这种 UA，就能让共享检测永远看不到他。
// 「按字节截断」把一条审计写入的成败开关交到了被审计者手上。
//
// 两步都必须做：
//  1. ToValidUTF8 —— HTTP 头的 value 允许 obs-text（≥0x80 的裸字节），
//     调用方可以直接送一段本来就非法的 UTF-8，与截断无关。
//  2. 按 rune 边界截断 —— 保证截断本身不制造非法序列。
//
// 上限仍按**字节**计（列是 text，防的是存储体积），不是按字符数。
func truncateUA(ua string) string {
	const maxUALen = 512

	// U+FFFD 替换字符本身是 3 字节合法 UTF-8，替换后长度可能变长，所以先替换再截断。
	ua = strings.ToValidUTF8(ua, "\uFFFD")
	if len(ua) <= maxUALen {
		return ua
	}
	// 退到不超过 maxUALen 的最后一个 rune 边界。
	cut := maxUALen
	for cut > 0 && !utf8.RuneStart(ua[cut]) {
		cut--
	}
	return ua[:cut]
}

// clientAddr 解析来源 IP。
//
// 走 middleware.ClientIP 而不是直接读 X-Forwarded-For：是否信任 XFF 由配置决定，
// 在不可信环境下信任它等于让用户自己决定审计日志里记什么 IP ——
// 而这张表的唯一用途就是识别账号共享。
func clientAddr(r *http.Request, trustProxy bool, logger *slog.Logger) netip.Addr {
	raw := middleware.ClientIP(r, trustProxy)
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		// request_ip 是 NOT NULL，必须给一个值。用未指定地址并打日志 ——
		// 0.0.0.0 在共享检测里会聚成一堆，日志是找回真相的唯一线索。
		logger.Warn("无法解析来源 IP，审计记为 0.0.0.0", "raw", raw)
		return netip.IPv4Unspecified()
	}
	// 去掉 IPv6 的 zone：inet 类型不接受 zone，带着它写库会直接报错。
	return addr.WithZone("")
}

// touchToken 异步更新 token 的 last_used_at / last_used_ip。
//
// 与 middleware.TouchKeyLastUsed 同一条纪律：不要为了一个运营字段
// 把纯读路径变成写路径。这条允许失败（subscriptions.sql 已注明）。
func touchToken(d subDeps, r *http.Request, tokenID int64) {
	ip := clientAddr(r, d.trustProxy, d.logger)
	go func() {
		// 用 Background 而不是请求的 ctx：请求结束会 cancel，
		// 那样这条更新几乎永远写不进去（现象是 last_used_at 恒为 NULL）。
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := d.db.TouchSubscriptionToken(ctx, dbgen.TouchSubscriptionTokenParams{
			ID: tokenID, LastUsedIp: &ip,
		}); err != nil {
			d.logger.Warn("更新订阅 token last_used_at 失败", "token_id", tokenID, "err", err)
		}
	}()
}

// ---- 响应 ----

// subscriptionHeaders 组装 api-contract §4.4 的那组头。
func subscriptionHeaders(webBaseURL string, tok dbgen.ResolveSubscriptionTokenRow, usage dbgen.GetSubscriptionUsageRow) map[string]string {
	h := map[string]string{
		// 🔴 与 Clash / sing-box / Shadowrocket 生态的硬接口，格式不能自创：
		// `upload={u}; download={d}; total={transfer_enable}; expire={unix}`，
		// 分隔符是「分号 + 一个空格」，值全部是十进制整数、无引号无单位。
		"subscription-userinfo": httpx.FormatSubscriptionUserInfo(
			usage.U, usage.D, tok.TransferEnable, expireUnix(tok.ExpiredAt)),
		"profile-update-interval": strconv.Itoa(subProfileUpdateInterval),
		"content-disposition":     subContentDisposition,
		"cache-control":           subCacheControl,
	}
	if webBaseURL != "" {
		// 客户端「访问网站」按钮的落点。webBaseURL 为空时**整个头省略** ——
		// 下发一个空 URL 会让部分客户端渲染出一个点不动的按钮。
		h["profile-web-page-url"] = webBaseURL + "/subscribe"
	}
	return h
}

// expireUnix 把 expired_at 转成 subscription-userinfo 的 expire 值（Unix 秒）。
//
// 不限时套餐（NULL）返回 subNoExpiryUnix —— 见该常量上的说明，
// **这是提案不是裁决**。
func expireUnix(expiredAt pgtype.Timestamptz) int64 {
	if !expiredAt.Valid {
		return subNoExpiryUnix
	}
	return expiredAt.Time.Unix()
}

// subscriptionResponse 是订阅下发的响应。
//
// 为什么自己实现 Visit 而不用生成的 gen.GetShortSubscription200TextyamlResponse：
//
//  1. 生成代码把 Content-Type 写死成 `text/yaml` / `text/plain` / `application/json`，
//     **没有 charset**。节点名里有中文与 emoji，缺 charset 会让部分客户端按 latin-1
//     解码 —— 现象是节点名乱码，而不是一个能被搜索的错误。
//  2. 生成代码按 200 的 content-type 分成三个互不相同的类型，而这个 handler
//     的三种格式共用一条完全相同的响应头组装逻辑。分成三个类型只会让
//     「某个格式漏了某个头」变成可能。
//  3. 生成的 404 类型是两个不同的具名 string 类型（长短路径各一个），
//     用一个同时实现两个接口的类型可以让两条路由**共用同一份响应**，
//     从结构上排除「短路径修了长路径没修」。
//
// 契约本身没有被违反：状态码、Content-Type 主类型、响应头集合都与 openapi 一致。
type subscriptionResponse struct {
	status      int
	contentType string
	headers     map[string]string
	body        []byte
}

// 编译期断言：一个类型同时满足两条路由的响应接口。
var (
	_ gen.GetShortSubscriptionResponseObject  = (*subscriptionResponse)(nil)
	_ gen.GetClientSubscriptionResponseObject = (*subscriptionResponse)(nil)
)

func (resp *subscriptionResponse) VisitGetShortSubscriptionResponse(w http.ResponseWriter) error {
	return resp.write(w)
}

func (resp *subscriptionResponse) VisitGetClientSubscriptionResponse(w http.ResponseWriter) error {
	return resp.write(w)
}

func (resp *subscriptionResponse) write(w http.ResponseWriter) error {
	// Content-Type 走 Set（规范大小写）：net/http 内部按规范名查这个头来决定
	// 要不要嗅探并补一个自己的 Content-Type，写成小写会让它看不见、然后再加一个。
	w.Header().Set("Content-Type", resp.contentType)

	// 其余头**直接写 map，绕开 Set 的规范化**，保住全小写。
	//
	// 为什么值得这么做：api-contract §4.4 明写「头名照抄 Xboard 用全小写」。
	// HTTP 头理论上不区分大小写，但 subscription-userinfo / profile-* 这几个
	// 是**代理客户端生态的非标准头**，而这个生态里确实存在按字面量匹配头名的实现。
	// Set 会把它们改成 Subscription-Userinfo / Profile-Update-Interval ——
	// 一旦碰上按小写字面量找的客户端，现象是「流量条不显示」，
	// 而响应看起来完全正常。代价只有这三行注释。
	//
	// HTTP/2 下 Go 会统一转小写，这里的写法与之一致，不会产生两种行为。
	for k, v := range resp.headers {
		w.Header()[k] = []string{v}
	}
	// x-request-id 由 middleware.RequestID 统一写，这里不重复设置 ——
	// 两处各写一遍迟早会写出两个不同的值。
	w.WriteHeader(resp.status)
	_, err := w.Write(resp.body)
	return err
}

// notFoundResponse 是全部 404 分支的**唯一**出口。
//
// 响应体刻意不含任何信息：区分「token 不存在」与「token 已吊销」正是枚举者要的信号。
// 同理不设 subscription-userinfo（那会暴露用户存在）。
func notFoundResponse() *subscriptionResponse {
	return &subscriptionResponse{
		status:      http.StatusNotFound,
		contentType: "text/plain; charset=utf-8",
		headers:     map[string]string{"cache-control": subCacheControl},
		body:        []byte("not found\n"),
	}
}
