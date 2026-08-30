package handler

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 面板侧订阅域 + 设备 / 用量 / 自检，外加节点面最后一个未实现的端点（pushUniProxyStatus）。
//
// 这一组的共同点是：**它们全都在回答「我为什么连不上」**。
// 用户打开面板不是为了看数字好看，是因为某样东西不工作了。
// 所以下面每一处「显示一个近似值」的地方都必须把近似性写进响应或文案里 ——
// 一个看起来精确但实际上偏小的数字，会让用户拿它跟自己手上的设备台数对质，
// 而我们无法解释。
//
// ---- 三条贯穿本文件的纪律 ----
//
//  1. **设备数是软限制，而且系统性偏小。** alivelist 拉取失败时 v2node 静默降级为
//     「零在线设备」（B16 实证，node.go 的 GetUniProxyAliveList 已登记）。
//     所以本文件任何一处设备计数都**不做拒绝服务的判定**，
//     文案与错误信息也不能写成硬保证。
//  2. **合成 id 的表达式只存在于 SQL 里，Go 侧一份都不能有。**
//     UserDevice.id 由 devices.sql 的 md5 表达式算出，列表与踢下线是同一个表达式；
//     handler 只做透传（列表给出的 id 原样喂给 kick）。
//     在 Go 里再写一遍 md5 是第三份拷贝 —— 三份里改漏任何一份，
//     现象都是「点了踢下线没反应」，而且只对某些 IP 出现。
//     守卫见 usersub_test.go 的 TestDeviceIDExpressionIsShared。
//  3. **节点面裸 JSON，不套信封**（api-contract §2 第 2 条）。
//     PushUniProxyStatus 与 node.go 的其余端点同形，`node_id` 一律以密钥绑定为准。

// ============================================================
// 本组（usersub / wallet / ticket / account 四个文件）共用的用户面小工具
// ============================================================
//
// 放在本文件而不是新开一个 helpers.go：信封 helper 已经在 auth.go 里，
// 再拆一个文件只会让「到底该去哪儿找」变成三选一。
// 需要跨文件用的都集中在这一节，改动时一眼能看全影响面。

// currentUser 取当前会话的用户 id。
//
// false 表示这条路由**没挂**用户面鉴权中间件，或者会话上下文被谁清掉了。
// 🔴 调用方一律 `return nil, errNoUserAuth` —— **复用 auth.go 里已有的那个哨兵**，
// 不新造一个。它由 main.go 的 responseErrorHandler 映射成
// 500 + INTERNAL_ERROR + 一条带 path/request_id 的 ERROR 日志。
//
// **不要**回 401。理由逐字在 errNoUserAuth 与 middleware/user.go 的 UserFrom 注释里：
// 未登录的请求根本到不了 handler（会被 RequireUser 挡在 401），
// 所以这里冒出来的一定是装配错误；把它伪装成鉴权问题，
// 会让「用户面鉴权忘了挂」表现为「所有人都登录不上」，而日志里一条异常都没有。
// 与 node.go 的 errNoNodeAuth 是同一条纪律。
//
// 这里包一层而不是每处直接写 middleware.UserFrom，只为少写四行；
// 语义与 auth.go / catalog.go 的调用点完全一致。
func (s *Server) currentUser(ctx context.Context) (int64, bool) {
	u, ok := middleware.UserFrom(ctx)
	if !ok || u == nil {
		return 0, false
	}
	return u.UserID, true
}

// unauthorizedDeletedUser 构造「会话有效但账号已注销」的 401。
//
// 本组所有读 users 的查询都带 `deleted_at IS NULL`（data-model §1.2 裁决 8），
// 所以已注销账号在这些查询上一律是 ErrNoRows。回 401 而不是 404：
// 404 说的是「你要的东西不存在」，而这里不存在的是**你**。
func (s *Server) unauthorizedDeletedUser(ctx context.Context, userID int64, op string) gen.ErrUnauthorizedJSONResponse {
	s.logger.WarnContext(ctx, "会话指向的用户已注销", "user_id", userID, "op", op)
	return s.unauthorized(ctx, gen.AUTHTOKENINVALID, "账号已注销")
}

// notFound / forbidden 补齐 auth.go 没有的两个信封构造器。
func (s *Server) notFound(ctx context.Context, msg string) gen.ErrNotFoundJSONResponse {
	return gen.ErrNotFoundJSONResponse{
		Body:    s.envelope(ctx, gen.RESOURCENOTFOUND, msg),
		Headers: gen.ErrNotFoundResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}
}

func (s *Server) forbidden(ctx context.Context, code gen.ErrorCode, msg string) gen.ErrForbiddenJSONResponse {
	return gen.ErrForbiddenJSONResponse{
		Body:    s.envelope(ctx, code, msg),
		Headers: gen.ErrForbiddenResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}
}

// ---- 游标分页与页大小（api-contract §2.4）----
//
// 🔴 **编解码本身复用 catalog.go 的 keysetCursor / encodeKeysetCursor /
// decodeKeysetCursor 与 pageLimit，本节只加一层「拿到坏游标怎么办」的策略。**
// 一个包里两套游标编解码就是「两份真相」：两边对未知字段、对填充形态、
// 对字段必填性的判断迟早分叉，而分叉的现象是「A 页面发出去的游标在 B 页面解不开」。
//
// 策略确实不同，而且必须不同：catalog.go 的调用点（listNotices / listOrders）
// 把 errBadCursor 变成 400，而**本组的六个列表端点在契约上一个 4xx 都没声明**
// —— listSubscriptionFetchLog / listTickets / listCommissions /
// listWalletTransactions / listInviteCodes / listUserDevices 只有 401 与 500。
// 也就是说「游标坏了」在契约上没有出口。三个候选：
//   - 回 500：把一次用户自己就能纠正的操作变成「服务器错误」，还会进错误率告警；
//   - 带着坏游标去查：类型不对时退化成空列表，用户看到「你一条记录都没有」，最坏；
//   - 从头开始：用户看到第一页（他刚刚就在那儿），下次点「下一页」自愈。
// 取第三个。代价是「翻页失效」对用户是静默的 —— 所以调用方**必须**打 WARN 日志。

// pageCursor 是解出来并校验过的游标位置。
type pageCursor struct {
	ID int64
	At time.Time
}

// encodePageCursor 编码下一页的位置。
//
// 时间带纳秒（keysetCursor.At 是 time.Time，json 编码即 RFC3339Nano）：
// at 要参与 `(created_at, id) < (at, id)` 的行比较，截到秒会让同一秒内的多行
// 被整批跳过或整批重复。
func encodePageCursor(id int64, at time.Time) string {
	return encodeKeysetCursor(keysetCursor{ID: &id, At: &at})
}

// decodePageCursor 解游标。第二个返回值 false = 这个游标不可用，按「没有游标」处理。
//
// 校验（base64 / JSON / 未知字段 / id 与 at 必须都在）全部由 decodeKeysetCursor 做，
// 那正是契约「服务端必须校验解出的字段类型」要求的那一层。
// 不校验的话 `{"id":"abc"}` 会解成 id = 0，而 `WHERE id < 0` 返回空列表 ——
// 用户看到的是「你一条记录都没有」，比「翻页坏了」糟得多。
func decodePageCursor(raw string) (pageCursor, bool) {
	c, err := decodeKeysetCursor(raw)
	if err != nil {
		return pageCursor{}, false
	}
	return pageCursor{ID: *c.ID, At: *c.At}, true
}

// listPageLimit 在 catalog.go 的 pageLimit 之上只多做一件事：允许端点自带默认页大小。
//
// ⚠️ **契约自身冲突**：listSubscriptionFetchLog 的 description 写「默认 10 条」，
// 而它引用的共享参数 LimitQuery 写 `default: 20`。取端点自述的 10 ——
// 共享参数是所有端点的兜底，端点自己说的话优先。冲突已登记在交付说明里。
// 上限与 has_more 的判据（多取一行，不要用「行数 == limit」）都沿用 pageLimit。
func listPageLimit(v *gen.LimitQuery, def int32) (want int, page int32) {
	if v == nil {
		d := gen.LimitQuery(def)
		v = &d
	}
	return pageLimit(v)
}

// fetchLogLimitDefault 见 listPageLimit 的注释。
const fetchLogLimitDefault int32 = 10

// ---- pgtype 到契约类型的小转换 ----

// tptr 把可空时间转成契约里的 *time.Time。
//
// 「没有值」与「零值时间」必须能区分：前者是 `null`（客户端渲染成「—」），
// 后者会被渲染成 0001-01-01，而那看起来像一个真实发生过的时刻。
func tptr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

// ttime 取时间值，无值时返回零值。只用于契约上 required 的字段。
func ttime(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}

// anyTime 把 sqlc 推断成 interface{} 的时间列取出来。
//
// 🔴 为什么会有 interface{}：`(SELECT max(f.request_at) ...)` 这类**子查询里的聚合**
// sqlc 的内建引擎推不出类型，于是生成 `interface{}`（devices.sql.go 的
// GetUserDiagnoseFactsRow.SubscriptionLastFetchedAt 与 ListUserDevicesByIPRow.LastSeenAt
// 都是这个形态）。pgx 扫进 interface{} 时按 OID 给出 time.Time，NULL 给 nil。
//
// 断言失败时返回 nil 而不是 panic：这一列的类型将来可能随 sqlc 版本变成
// pgtype.Timestamptz，那时 panic 会让**整个自检页**挂掉，而它恰恰是用户
// 已经连不上网之后才会打开的页面。
func anyTime(v any) *time.Time {
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return nil
		}
		return &t
	case pgtype.Timestamptz:
		return tptr(t)
	default:
		return nil
	}
}

// ============================================================
// 订阅 token 的加解密
// ============================================================
//
// 🔴 **这一节是本文件里唯一一处「没有现成裁决、由我定下来」的东西，读之前先看完这段。**
//
// 0008 建表时给 subscription_tokens 留了两列：token_hash（查找）与
// **token_enc（NOT NULL，AES-256-GCM(token)，「密钥在 Secret Manager 不落 DB」）**。
// token_enc 存在的理由不是加密洁癖，是 ADR 0002 的失联恢复：换域名之后
// 用户要能自己把明文 token 拼进新域名，token 若不可再次展示，
// 每换一次域名就要给所有人重签一次。
//
// **而 config 里没有这把密钥。** 实查 api/internal/config/config.go：
// 只有 BP_SUBSCRIPTION_TOKEN_PEPPER（哈希用的 pepper）、BP_NODE_KEY_PEPPER、
// BP_SESSION_SIGNING_KEY、BP_ADMIN_TOTP_ENC_KEY 四把，没有订阅 token 的加密密钥，
// 而 config.go 不在本轮的可写范围内。
//
// 三个选择，取第三个：
//
//	a) 不实现 createSubscriptionToken（留 501）。代价是用户**根本拿不到订阅链接** ——
//	   注册流程里没有任何地方自动签发 token（auth.go 实查确认），
//	   于是整个产品在这一步断掉。
//	b) 借用 BP_ADMIN_TOTP_ENC_KEY。**不行**：那是管理面 2FA 的密钥，
//	   用它加密用户数据等于把两个安全域绑在一起，将来轮换管理面密钥
//	   会静默地让所有用户的订阅链接无法展示。
//	c) 从 BP_SUBSCRIPTION_TOKEN_PEPPER **派生**一把 AES-256 密钥（本节的做法）。
//
// c 为什么成立：0008 对这层加密的威胁模型写得很死 ——
// 「防的是**只拿到数据库**（备份泄漏 / 只读注入 / 快照误共享），**不防** bp-api 被攻破」。
// pepper 在 Secret Manager 里、不落库，所以「只拿到数据库」的攻击者
// 拿到的仍然只有密文。威胁模型逐字成立。
//
// c 的代价，必须写下来：
//   - 轮换 pepper 现在会**同时**让哈希查找与密文展示失效。但轮换 pepper 本来就会
//     让全部 token_hash 对不上（= 所有订阅链接立刻失效），也就是说这一条耦合
//     没有引入新的坏结果，只是把一件已经不可做的事继续保持为不可做。
//   - 派生用 HMAC-SHA256 + 域分隔串，与 middleware/admin.go 的 totpCodeHash
//     派生子密钥是同一手法（同一把密钥同时用于两种用途是密钥用途混用，
//     域分隔的成本只有一次 HMAC）。
//
// 🔴 **ADR 0002 的失联恢复路径落地时，解密必须调用本节的 decryptSubToken，
// 不能另起一套。** 另起一套的现象是「恢复页面显示的 token 全是乱码」，
// 而那一天正是我们最没有余裕排查的一天。
// TODO(P2): 加 BP_SUBSCRIPTION_TOKEN_ENC_KEY 之后，本节改成读那把密钥，
// 并写一支把存量密文重新加密的迁移 —— 存量不重加密的话旧 token 全部展示不出来。

// subTokenEncDomain 是派生 AES 密钥的域分隔串。
//
// 🔴 **改它等于把所有存量 token_enc 变成解不开的字节。** 明文 token 无处可取，
// 现象是「面板打不出订阅链接、失联恢复页面空白」，而数据库里那些行都好端端地在。
const subTokenEncDomain = "bp/subscription_token_enc/v1"

// errNoSubTokenKey 表示 pepper 缺失 —— 属于启动配置错误（config.Load 会拒绝启动，
// 所以正常不会走到），但签发路径必须自己再判一次：
// 用空 pepper 派生出来的密钥是一个**常量**，等于没加密。
var errNoSubTokenKey = errors.New("BP_SUBSCRIPTION_TOKEN_PEPPER 为空，拒绝签发订阅 token")

// subTokenEncKey 从 pepper 派生 32 字节 AES-256 密钥。
func subTokenEncKey(pepper string) []byte {
	m := hmac.New(sha256.New, []byte(pepper))
	m.Write([]byte(subTokenEncDomain))
	return m.Sum(nil)
}

// encryptSubToken 产出 `nonce(12) || ciphertext || tag(16)`。
//
// 这个拼接顺序与 0002_foundation.up.sql 给 admin_users.totp_secret_enc 定的形态
// 逐字相同（middleware/admin.go 的 decryptTOTPSecret 就是按它解的）。
// 同一个仓库里两种密文布局会让「解不开」多一个排查分支，而统一的成本是零。
func encryptSubToken(pepper, token string) ([]byte, error) {
	if pepper == "" {
		return nil, errNoSubTokenKey
	}
	gcm, err := subTokenGCM(pepper)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce 失败: %w", err)
	}
	return gcm.Seal(nonce, nonce, []byte(token), nil), nil
}

// decryptSubToken 还原明文 token。
func decryptSubToken(pepper string, enc []byte) (string, error) {
	if pepper == "" {
		return "", errNoSubTokenKey
	}
	gcm, err := subTokenGCM(pepper)
	if err != nil {
		return "", err
	}
	if len(enc) < gcm.NonceSize() {
		return "", errors.New("token_enc 长度不足，无法取出 nonce")
	}
	nonce, body := enc[:gcm.NonceSize()], enc[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("token_enc 解密失败: %w", err)
	}
	return string(plain), nil
}

func subTokenGCM(pepper string) (cipher.AEAD, error) {
	block, err := aes.NewCipher(subTokenEncKey(pepper))
	if err != nil {
		return nil, fmt.Errorf("订阅 token 密钥不可用: %w", err)
	}
	return cipher.NewGCM(block)
}

// ============================================================
// 订阅 token 管理
// ============================================================

const (
	// maxActiveSubTokens 是每用户的有效 token 上限。
	//
	// ⚠️ **这个数字未裁决**，是 subscription_user.sql 明确交给 handler 的参数：
	// openapi 给 createSubscriptionToken 声明了 403，而用户侧唯一能触发 403 的
	// 理由就是名额上限（401 是没登录、422 是名字不合法、500 是我们的错），
	// 也就是说契约隐含了一个上限但没写数字。
	//
	// 取 10 是**宽松侧**的选择：上限设紧的表现是「用户装第四台设备时被拒，
	// 而他完全不知道为什么」，而设松的代价只是几行数据。
	// 命中时打 INFO 日志 —— 定这个数要看真实设备数分布，而那要等 P2 的数据。
	maxActiveSubTokens = 10

	// subTokenNameMaxRunes 与 SubscriptionTokenCreateRequest.name 的 maxLength 同值。
	// 按 **rune** 数而不是字节数：名字是「iPhone」「公司电脑」这类中文串，
	// 按字节截会把 64 字符的上限变成 21 个汉字，而契约说的是 64。
	subTokenNameMaxRunes = 64

	// subTokenBytes 是随机 token 的字节数。24 字节 → base64url 32 字符，
	// 落在 subscription.go 的 [subTokenMinLen, subTokenMaxLen] = [16, 64] 之内。
	// 用 randomToken（base64url 无填充）而不是自己抽字符，
	// 是因为它的输出字符集恰好就是 subTokenAlphabet —— 两处共用一个事实。
	subTokenBytes = 24

	// subTokenPrefixLen 是明文前缀长度（0008 的列注释：「明文前 8 位」）。
	subTokenPrefixLen = 8

	// createSubTokenRetries 是撞码重试次数。撞码概率是 2⁻¹⁹²，
	// 重试存在的理由不是概率而是「唯一索引冲突不该变成 500」。
	createSubTokenRetries = 3
)

// ListSubscriptionTokens 列出用户的订阅 token（含已失效的）。
func (s *Server) ListSubscriptionTokens(ctx context.Context, _ gen.ListSubscriptionTokensRequestObject) (gen.ListSubscriptionTokensResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	// 🔴 用 subscription_user.sql 的 ListUserSubscriptionTokens，**不是**
	// subscriptions.sql 的 ListSubscriptionTokens：后者按 `revoked_at IS NULL` 过滤，
	// 而「一键全撤」只写 users.sub_revoked_at、不动 token 行。
	// 用错那条的现象是：用户点完「全部重置」，列表里每条 token 仍显示有效，
	// 而 /s/{token} 对它们全部 404 —— 没有报错、没有日志。
	rows, err := s.db.ListUserSubscriptionTokens(ctx, userID)
	if err != nil {
		return gen.ListSubscriptionTokens500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询订阅 token 列表失败", err),
		}, nil
	}

	out := make([]gen.SubscriptionToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, subscriptionTokenView(r))
	}
	return gen.ListSubscriptionTokens200JSONResponse{Data: out, Meta: s.meta(ctx)}, nil
}

// subscriptionTokenView 把一行映射成契约类型。纯函数，便于单测。
//
// 🔴 `revoked_at` 的语义在这里被**扩展**了，这是刻意的：
// 契约的 SubscriptionToken 只有 revoked_at 一个字段能表达「这条不能用了」，
// 而失效有四种成因（单条吊销 / 自身过期 / 一键全撤 / 账号注销）。
// 只回填 t.revoked_at 的话，被「一键全撤」干掉的 token 在面板上仍然是一副有效的样子。
// 所以：**is_active = false 时必须给出一个非空的 revoked_at**，
// 取值优先级是「真正的吊销时刻」→「过期时刻」→「一键全撤时刻」。
// 三者都缺时（理论上不会发生）退回 sub_revoked_at，仍然保证前端能判定失效。
func subscriptionTokenView(r dbgen.ListUserSubscriptionTokensRow) gen.SubscriptionToken {
	v := gen.SubscriptionToken{
		Id:         r.ID,
		Name:       r.Name,
		Masked:     maskSubToken(r.TokenPrefix),
		CreatedAt:  ttime(r.CreatedAt),
		LastUsedAt: tptr(r.LastUsedAt),
		RevokedAt:  tptr(r.RevokedAt),
	}
	if !r.IsActive && v.RevokedAt == nil {
		switch {
		case r.ExpiresAt.Valid:
			v.RevokedAt = tptr(r.ExpiresAt)
		default:
			v.RevokedAt = tptr(r.SubRevokedAt)
		}
	}
	return v
}

// maskSubToken 渲染打码后的 token。
//
// ⚠️ 契约的例子是 `a1b2…f9`（首尾各留几位），但**尾部取不到** ——
// 数据库里只有 token_prefix（明文前 8 位）与不可逆的 hash，
// 密文虽然能解，但为了渲染一个列表去解 N 条密文是把展示路径变成密钥路径。
// 所以这里只渲染前缀 + 省略号。
//
// 露出 8 个字符是安全的：token 是 24 字节 CSPRNG（≈192 bit），
// 前 8 个 base64 字符约 48 bit，剩下 144 bit 仍然爆破不动。
// 而 token_prefix 这一列本来就是 0008 为「面板列表与日志定位」建的。
func maskSubToken(prefix string) string {
	if prefix == "" {
		return "…"
	}
	if len(prefix) > subTokenPrefixLen {
		prefix = prefix[:subTokenPrefixLen]
	}
	return prefix + "…"
}

// CreateSubscriptionToken 签发一枚新的订阅 token。**明文只在这一个响应里出现一次。**
func (s *Server) CreateSubscriptionToken(ctx context.Context, req gen.CreateSubscriptionTokenRequestObject) (gen.CreateSubscriptionTokenResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	if req.Body == nil {
		return gen.CreateSubscriptionToken422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" || len([]rune(name)) > subTokenNameMaxRunes {
		return gen.CreateSubscriptionToken422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "备注名长度必须在 1–64 个字符之间",
				detail("name", "长度不合法")),
		}, nil
	}

	// 名额闸门。**先查再插**在这里是可接受的（与 wallet.go 的邀请码闸门形成对照）：
	// 输掉这次竞态的后果是「多了一条订阅 token」，而订阅 token 本身可以被用户自己撤销，
	// 也不涉及任何金钱含义。邀请码那条把闸门做进了 INSERT 的 WHERE，
	// 这里做不到 —— 写入路径必须复用 subscriptions.sql 的 CreateSubscriptionToken
	// （写只能有一个事实源），而那条是一个普通的六列 INSERT。
	active, err := s.db.CountActiveSubscriptionTokens(ctx, userID)
	if err != nil {
		return gen.CreateSubscriptionToken500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "统计有效订阅 token 失败", err),
		}, nil
	}
	if int(active) >= maxActiveSubTokens {
		// INFO 而不是 WARN：命中上限是预期内的用户行为，不是故障。
		// 但它必须被记下来 —— 上限取多少要看这条日志出现的频率。
		s.logger.InfoContext(ctx, "订阅 token 名额已满", "user_id", userID,
			"active", active, "limit", maxActiveSubTokens)
		return gen.CreateSubscriptionToken403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.QUOTADEVICELIMIT,
				fmt.Sprintf("订阅链接数量已达上限（%d 条）。请先撤销不再使用的链接", maxActiveSubTokens)),
		}, nil
	}

	pepper := s.cfg.SubscriptionTokenPepper
	var created dbgen.SubscriptionToken
	var plain string
	for attempt := 0; ; attempt++ {
		plain, err = randomToken(subTokenBytes)
		if err != nil {
			return gen.CreateSubscriptionToken500JSONResponse{
				ErrInternalJSONResponse: s.internalErr(ctx, "生成订阅 token 失败", err),
			}, nil
		}
		enc, encErr := encryptSubToken(pepper, plain)
		if encErr != nil {
			return gen.CreateSubscriptionToken500JSONResponse{
				ErrInternalJSONResponse: s.internalErr(ctx, "加密订阅 token 失败", encErr),
			}, nil
		}
		created, err = s.db.CreateSubscriptionToken(ctx, dbgen.CreateSubscriptionTokenParams{
			UserID:      userID,
			TokenHash:   subscriptionTokenHash(pepper, plain),
			TokenEnc:    enc,
			TokenPrefix: plain[:subTokenPrefixLen],
			Name:        name,
			// expires_at 留空 = 不过期。契约的创建请求里没有有效期字段，
			// 凭空给一个期限等于让用户的订阅在某天突然停掉，而他没同意过任何期限。
			ExpiresAt: pgtype.Timestamptz{},
		})
		if err == nil {
			break
		}
		// 撞 subscription_tokens_hash_uk。重试而不是回 500 —— 撞码是我们的事，不是用户的事。
		if isUniqueViolation(err) && attempt < createSubTokenRetries {
			s.logger.WarnContext(ctx, "订阅 token 撞码，重试", "user_id", userID, "attempt", attempt+1)
			continue
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// user_id 外键指向的用户已注销：INSERT 会违反外键而不是返回 0 行，
			// 但保留这条分支让「将来 CreateSubscriptionToken 改成条件插入」不至于静默失败。
			return gen.CreateSubscriptionToken401JSONResponse{
				ErrUnauthorizedJSONResponse: s.unauthorizedDeletedUser(ctx, userID, "createSubscriptionToken"),
			}, nil
		}
		return gen.CreateSubscriptionToken500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "写入订阅 token 失败", err),
		}, nil
	}

	base := s.apiBaseURL(ctx)
	body := struct {
		Data gen.SubscriptionTokenCreated `json:"data"`
		Meta gen.Meta                     `json:"meta"`
	}{
		Data: gen.SubscriptionTokenCreated{
			Id:        created.ID,
			Name:      created.Name,
			CreatedAt: ttime(created.CreatedAt),
			// 🔴 明文只在这一次出现。它不进日志、不进 token_prefix 之外的任何一列。
			Token:        plain,
			SubscribeUrl: shortSubscribeURL(base, plain),
		},
		Meta: s.meta(ctx),
	}
	return gen.CreateSubscriptionToken201JSONResponse{
		Body:    body,
		Headers: gen.CreateSubscriptionToken201ResponseHeaders{Location: fmt.Sprintf("/api/v1/user/subscription/tokens/%d", created.ID)},
	}, nil
}

// RevokeSubscriptionToken 吊销单条 token。
//
// 复用 subscriptions.sql 的 RevokeSubscriptionToken：它的
// `WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL` 让「不是你的」与
// 「已经撤过了」同为 0 行 → 同一个 404。这正是契约要的（本端点没有声明 409），
// 也不给「猜别人的 token id」留任何信号。
func (s *Server) RevokeSubscriptionToken(ctx context.Context, req gen.RevokeSubscriptionTokenRequestObject) (gen.RevokeSubscriptionTokenResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	reason := "user_revoked"
	_, err := s.db.RevokeSubscriptionToken(ctx, dbgen.RevokeSubscriptionTokenParams{
		ID:            req.Id,
		UserID:        userID,
		RevokedReason: &reason,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.RevokeSubscriptionToken404JSONResponse{
				ErrNotFoundJSONResponse: s.notFound(ctx, "订阅链接不存在或已撤销"),
			}, nil
		}
		return gen.RevokeSubscriptionToken500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "吊销订阅 token 失败", err),
		}, nil
	}
	return gen.RevokeSubscriptionToken204Response{
		Headers: gen.RevokeSubscriptionToken204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}, nil
}

// RevokeAllSubscriptionTokens 一键全撤。**所有设备都要重新导入。**
func (s *Server) RevokeAllSubscriptionTokens(ctx context.Context, _ gen.RevokeAllSubscriptionTokensRequestObject) (gen.RevokeAllSubscriptionTokensResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	// RevokeAllUserSubscriptionTokens 的 CTE 求值顺序是被依赖的语义：
	// `live` 看到的是 UPDATE 之前的快照，也就是「本次真正撤掉的条数」。
	// 不能先 count 再 update —— 两条语句之间插进一次签发，返回的条数就少一条，
	// 而这个界面的全部作用就是让用户相信「都撤干净了」。
	row, err := s.db.RevokeAllUserSubscriptionTokens(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.RevokeAllSubscriptionTokens401JSONResponse{
				ErrUnauthorizedJSONResponse: s.unauthorizedDeletedUser(ctx, userID, "revokeAllSubscriptionTokens"),
			}, nil
		}
		return gen.RevokeAllSubscriptionTokens500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "一键撤销订阅 token 失败", err),
		}, nil
	}
	s.logger.InfoContext(ctx, "用户一键撤销全部订阅 token", "user_id", userID, "revoked", row.Revoked)

	return gen.RevokeAllSubscriptionTokens200JSONResponse{
		Data: gen.RevokeAllResult{
			Revoked:      row.Revoked,
			SubRevokedAt: ttime(row.SubRevokedAt),
		},
		Meta: s.meta(ctx),
	}, nil
}

// ListSubscriptionFetchLog 是面板的「谁在拉我的订阅」自助查漏页。
//
// 这个页面的用途是让用户**自己**发现订阅被盗用并只撤那一条 token，
// 所以它对「漏行」零容忍：漏掉一行就是漏掉那一次可疑拉取。
// 用 keyset 游标而不是既有那条 OFFSET 查询，理由在 subscription_user.sql §3。
func (s *Server) ListSubscriptionFetchLog(ctx context.Context, req gen.ListSubscriptionFetchLogRequestObject) (gen.ListSubscriptionFetchLogResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	want, page := listPageLimit(req.Params.Limit, fetchLogLimitDefault)
	arg := dbgen.ListUserSubscriptionFetchLogParams{
		UserID: userID,
		// pageLimit 已经把 has_more 的判据做成「多取一行」。**不要**用
		// 「行数 == limit」判 —— 总数恰好整除页大小时会多给用户一页空数据，
		// 而空页在前端通常长得像加载失败。
		PageLimit: page,
	}
	if req.Params.Cursor != nil {
		if c, valid := decodePageCursor(string(*req.Params.Cursor)); valid {
			// ⚠️ cursor_at 与 cursor_id 必须**同时**传：只传一个时行比较
			// `(request_at, id) < (at, NULL)` 求值为 NULL → 返回 0 行而不报错。
			arg.CursorAt = tstz(c.At)
			id := c.ID
			arg.CursorID = &id
		} else {
			s.logger.WarnContext(ctx, "订阅拉取记录的游标无法解析，已从首页开始",
				"user_id", userID, "cursor_len", len(*req.Params.Cursor))
		}
	}

	rows, err := s.db.ListUserSubscriptionFetchLog(ctx, arg)
	if err != nil {
		return gen.ListSubscriptionFetchLog500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询订阅拉取记录失败", err),
		}, nil
	}

	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.SubscriptionFetchLogEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, fetchLogEntryView(r))
	}

	meta := s.meta(ctx)
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		c := encodePageCursor(last.ID, ttime(last.RequestAt))
		meta.NextCursor = &c
	}
	return gen.ListSubscriptionFetchLog200JSONResponse{Data: out, Meta: meta}, nil
}

// fetchLogEntryView 映射一行拉取记录。纯函数。
//
// ⚠️ `format` 是自由文本列（0008 没有 CHECK），而契约的枚举只有
// base64 / clash / singbox 三个值。认不出来的取值**不下发**（留 nil）而不是原样透出：
// 前端按枚举 switch，多一个值会落到兜底分支渲染成空白，
// 而「格式那一列时有时无」比「一直是空」更难被人报上来。
//
// ⚠️ `cn_mode` 选出来了但**不进响应体** —— 契约里没有这个字段。
// 它的用途是运营指标（ADR 0015 §5.7 的失效条件），走 GetCnProxyModeRatio。
func fetchLogEntryView(r dbgen.ListUserSubscriptionFetchLogRow) gen.SubscriptionFetchLogEntry {
	e := gen.SubscriptionFetchLogEntry{
		Id:         r.ID,
		RequestAt:  ttime(r.RequestAt),
		RequestIp:  r.RequestIp.String(),
		SubTokenId: r.TokenID,
	}
	if r.UserAgent != "" {
		ua := r.UserAgent
		e.UserAgent = &ua
	}
	if r.TokenName != nil && *r.TokenName != "" {
		n := *r.TokenName
		e.SubTokenName = &n
	}
	if r.Format != nil {
		switch gen.SubscriptionFetchLogEntryFormat(*r.Format) {
		case gen.SubscriptionFetchLogEntryFormatBase64,
			gen.SubscriptionFetchLogEntryFormatClash,
			gen.SubscriptionFetchLogEntryFormatSingbox:
			f := gen.SubscriptionFetchLogEntryFormat(*r.Format)
			e.Format = &f
		}
	}
	return e
}

// ============================================================
// 订阅概览与节点列表
// ============================================================

// GetUserSubscription 返回订阅概览（summary + urls）。
func (s *Server) GetUserSubscription(ctx context.Context, _ gen.GetUserSubscriptionRequestObject) (gen.GetUserSubscriptionResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	// summary 半边：一条语句 = 一个快照。拆成两次查询会让「配额」与「在线设备数」
	// 来自两个时刻，而这个页面正是用户拿来核对「我开了 3 台，为什么说我超了」的地方。
	sum, err := s.db.GetUserSubscriptionSummary(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.GetUserSubscription401JSONResponse{
				ErrUnauthorizedJSONResponse: s.unauthorizedDeletedUser(ctx, userID, "getUserSubscription"),
			}, nil
		}
		return gen.GetUserSubscription500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询订阅概览失败", err),
		}, nil
	}

	urls, err := s.subscriptionURLsFor(ctx, userID)
	if err != nil {
		return gen.GetUserSubscription500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "拼装订阅链接失败", err),
		}, nil
	}

	return gen.GetUserSubscription200JSONResponse{
		Data: gen.UserSubscription{
			Summary: subscriptionSummaryView(sum),
			Urls:    urls,
		},
		Meta: s.meta(ctx),
	}, nil
}

// subscriptionSummaryView 映射用量与到期摘要。纯函数。
//
// ⚠️ device_limit 为 NULL = **不限设备**（0003 的列注释）。契约里 DeviceLimit 是
// 必填 int32，装不下「不限」，只能填 0。前端必须把 0 渲染成「不限」而不是「0 台」——
// 这一条登记在交付说明里，它是契约表达力不足，不是这里的选择。
//
// ⚠️ total_bytes 读的是 users.transfer_enable（0016 起是 GENERATED STORED 列，
// = _plan + _pack）。读安全，写会在运行时炸 —— 本文件一个字都不写它。
func subscriptionSummaryView(r dbgen.GetUserSubscriptionSummaryRow) gen.SubscriptionSummary {
	sum := gen.SubscriptionSummary{
		UploadBytes:   r.UploadBytes,
		DownloadBytes: r.DownloadBytes,
		TotalBytes:    r.TotalBytes,
		DeviceCount:   r.DeviceCount,
		ExpiredAt:     tptr(r.ExpiredAt),
		ResetAt:       tptr(r.ResetAt),
		PlanName:      r.PlanName,
	}
	if r.DeviceLimit != nil {
		sum.DeviceLimit = *r.DeviceLimit
	}
	return sum
}

// subscriptionURLsFor 拼出五条订阅 URL。
//
// 🔴 **域名来自请求，不来自数据库、也不来自配置。**
// ADR 0002 的失联恢复会轮换 API 主域名；用户此刻能打开面板，说明他手上这个域名是通的，
// 那正是他的客户端应该继续用的那一个。把域名固化进配置意味着换域名要重新部署，
// 而换域名恰恰发生在我们最不想动部署的时候。
// ⚠️ Host 头是客户端可控的，所以这里存在「Host 注入」——
// 但注入的结果只污染**攻击者自己**看到的那一份响应（URL 只回给发起请求的人），
// 既不写库也不发给别人。这个面在这里是可接受的。
//
// 🔴 **没有任何一条 token 时返回空串，而不是造一条。**
// 三个理由：(a) GET 不该签发凭据 —— 客户端一次带缓存的重复请求就能凭空多出几条 token；
// (b) 自动签发的 token 没有用户起的名字，而「哪台设备」这一列的全部价值就是那个名字；
// (c) createSubscriptionToken 有 403 名额闸门，GET 里签发会让一个只读请求
// 突然回一个用户无法理解的错误。前端拿到空串应当渲染「创建订阅链接」按钮。
func (s *Server) subscriptionURLsFor(ctx context.Context, userID int64) (gen.SubscriptionUrls, error) {
	base := s.apiBaseURL(ctx)

	rows, err := s.db.ListUserSubscriptionTokens(ctx, userID)
	if err != nil {
		return gen.SubscriptionUrls{}, err
	}
	// 列表按 created_at DESC 排，取第一条**有效**的。
	// 用 is_active 而不是 revoked_at IS NULL：后者看不见「一键全撤」，
	// 于是面板会把一条 /s/ 必然 404 的链接当成主链接印出来。
	var pick *dbgen.ListUserSubscriptionTokensRow
	for i := range rows {
		if rows[i].IsActive {
			pick = &rows[i]
			break
		}
	}
	if pick == nil {
		return gen.SubscriptionUrls{}, nil
	}

	// 明文只能从密文还原。这一步是 token_enc 这一列存在的**唯一**理由
	// （0008：「token 若不可再次展示，每换一次域名就要给所有用户重签」）。
	full, err := s.db.GetSubscriptionToken(ctx, dbgen.GetSubscriptionTokenParams{ID: pick.ID, UserID: userID})
	if err != nil {
		return gen.SubscriptionUrls{}, err
	}
	plain, err := decryptSubToken(s.cfg.SubscriptionTokenPepper, full.TokenEnc)
	if err != nil {
		// 解不开是配置/数据事故（pepper 被轮换过、密文被截断），不是用户错误。
		// 但**不能**让整个概览页 500 —— 到期时间与流量数字仍然是对的，
		// 而那些正是用户此刻要看的东西。降级成「没有链接」并打 ERROR。
		s.logger.ErrorContext(ctx, "订阅 token 密文解密失败，概览页降级为无链接",
			"user_id", userID, "token_id", pick.ID, "err", err)
		return gen.SubscriptionUrls{}, nil
	}

	short := shortSubscribeURL(base, plain)
	long := fmt.Sprintf("%s/api/v1/client/subscribe?token=%s", base, plain)
	clash := long + "&flag=clash"
	singbox := long + "&flag=singbox"
	b64 := long + "&flag=base64"
	return gen.SubscriptionUrls{
		Short:   short,
		Long:    long,
		Clash:   &clash,
		Singbox: &singbox,
		Base64:  &b64,
	}, nil
}

// shortSubscribeURL 拼短链接。**默认对外发这一条**（短、无 query、不易被聊天软件截断）。
func shortSubscribeURL(base, token string) string {
	return base + "/s/" + token
}

// apiBaseURL 从当前请求推出 API 主域名，形如 https://api.example.com（无尾斜杠）。
//
// 拿不到原始请求时返回空串：调用方拼出来的会是 `/s/xxxx` 这样的相对路径 ——
// 不好看，但仍然可用（面板与 API 同域时就是对的），比返回一个写死的错域名好。
func (s *Server) apiBaseURL(ctx context.Context) string {
	r, ok := boundRequestFrom(ctx)
	if !ok {
		s.logger.WarnContext(ctx, "缺少原始请求，订阅链接将退化为相对路径（未挂载 handler.RequestBinding()）")
		return ""
	}
	scheme := "https"
	if s.cfg.TrustProxyHeaders {
		if p := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); p != "" {
			// 只取第一段：多层代理会拼成 "https, http"。
			if i := strings.IndexByte(p, ','); i >= 0 {
				p = p[:i]
			}
			scheme = strings.ToLower(strings.TrimSpace(p))
		}
	} else if r.TLS == nil {
		// 不信代理头时以本进程的实际监听形态为准。本地开发是明文 http，
		// 写死 https 会让开发环境拿到一条打不开的链接。
		scheme = "http"
	}
	if r.Host == "" {
		return ""
	}
	return scheme + "://" + r.Host
}

// ---- 节点列表 ----

const (
	// nodeOnlineWithin / nodeDegradedWithin 是 UserNode.status 的三态阈值。
	//
	// ⚠️ **这两个数字没有任何文档裁决过。** 查遍 openapi、api-contract、ADR 0011/0014：
	// `degraded` 只出现在域名池与限流器语境，与节点心跳无关。
	// subscription_user.sql 因此刻意不在 SQL 里算 status（写死会把未裁决的数字
	// 固化进生成物，改它要重跑 sqlc 并过 contract-drift 门禁），把事实交给这里。
	//
	// 取值推导自节点的上报周期（node.go 的 nodePushIntervalSeconds = 60s）：
	//   · 连丢 3 次 → degraded（一次网络抖动不该让用户看到红点）
	//   · 连丢 10 次 → offline（10 分钟没上报，基本可以确定不是抖动）
	// **需实测**：真实抖动幅度要等节点上线才知道。这条映射一旦定下来
	// 应当写进 api-contract 而不是留在这里。
	nodeOnlineWithin   = 180 * time.Second
	nodeDegradedWithin = 600 * time.Second

	// userNodeMultiplierE9 是倍率的定点表示，1e9 = 1.0 倍。
	// 第一阶段不引入倍率（servers 表刻意没有 rate 列，0004 原话：
	// 引入倍率是一次 ADR 级决策 + 一次 stat_user_server 重建），恒填 1.0。
	userNodeMultiplierE9 int64 = 1_000_000_000
)

// ListUserNodes 列出用户可见的节点及其状态。
func (s *Server) ListUserNodes(ctx context.Context, _ gen.ListUserNodesRequestObject) (gen.ListUserNodesResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	// ⚠️ ListUserNodesWithState 按 **group_id** 取节点，而 subscription_user.sql
	// 里没有任何一条只取 group_id 的窄查询，所以这里借 users.sql 的 GetUserWithTraffic。
	// 它比 GetUserByID（SELECT *）窄得多（没有 password_hash），
	// 但仍然带着 uuid —— **那是节点侧的连接凭据**。本函数只读它的 GroupID，
	// 整行绝不进响应、绝不进日志。登记在交付说明里：缺一条 GetUserGroupID。
	u, err := s.db.GetUserWithTraffic(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.ListUserNodes401JSONResponse{
				ErrUnauthorizedJSONResponse: s.unauthorizedDeletedUser(ctx, userID, "listUserNodes"),
			}, nil
		}
		return gen.ListUserNodes500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询用户分组失败", err),
		}, nil
	}

	rows, err := s.db.ListUserNodesWithState(ctx, u.GroupID)
	if err != nil {
		return gen.ListUserNodes500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询节点列表失败", err),
		}, nil
	}

	out := make([]gen.UserNode, 0, len(rows))
	for _, r := range rows {
		out = append(out, userNodeView(r))
	}
	return gen.ListUserNodes200JSONResponse{Data: out, Meta: s.meta(ctx)}, nil
}

// userNodeView 映射一行节点。纯函数。
func userNodeView(r dbgen.ListUserNodesWithStateRow) gen.UserNode {
	n := gen.UserNode{
		Id:           r.ID,
		Name:         r.Name,
		Type:         nodeDisplayType(r.Protocol),
		Status:       nodeStatusOf(r.Enabled, r.ReportedAt, r.SecondsSinceReport),
		MultiplierE9: ptrOf(userNodeMultiplierE9),
	}
	if r.Region != "" {
		region := r.Region
		n.Region = &region
	}
	return n
}

// nodeDisplayType 把 servers.protocol 映射成对外的粗粒度展示名。
//
// 🔴 **绝不能直接下发 protocol。** 取值是 vless_reality / hysteria2 /
// shadowsocks2022 / vless_xhttp_cdn，而 `vless_xhttp_cdn` 是 ADR 0004 的**应急**通路 ——
// 它出现在一个任何登录用户都能拉的列表里，等于对外宣告「我们正在被封」。
// 两条 vless 变体折叠成同一个 "vless" 之后，切到应急通路在这个接口上不可观测。
//
// ⚠️ 契约的 UserNode.type 说「权威来源是 servers.type」，而 servers 表上没有 type 列
// （真名 protocol），且契约举的例子是 vless / hysteria —— 与枚举值本身也对不上。
// openapi 没给 type 定 enum，所以这是一处待定映射而不是 drift，登记在交付说明里。
//
// 认不出来的协议返回 "unknown" 而不是猜一个：猜错会让客户端按错误的协议提示用户，
// 而 "unknown" 至少是真的。
func nodeDisplayType(p dbgen.ServerProtocol) string {
	switch p {
	case dbgen.ServerProtocolVlessReality, dbgen.ServerProtocolVlessXhttpCdn:
		return "vless"
	case dbgen.ServerProtocolHysteria2:
		return "hysteria"
	case dbgen.ServerProtocolShadowsocks2022:
		return "shadowsocks"
	default:
		return "unknown"
	}
}

// nodeStatusOf 由「最后一次上报距今多少秒」与「是否被禁用」推出三态。纯函数。
//
// ⚠️ **从未上报过**（server_online_state 里没有行，或者那张 UNLOGGED 表刚被
// 崩溃后的 TRUNCATE 清空）时 reported_at 为 NULL → offline。
// 这是对的：我们确实不知道它活着。把「没有数据」渲染成 online 是最坏的选择 ——
// 数据库重启一次，整张节点列表就会集体撒谎。
//
// ⚠️ enabled = false 的节点一律 offline。契约的三态里没有「已停用」，
// 而把一个管理员刚关掉的节点显示成 online 会让用户一直往它上面连。
func nodeStatusOf(enabled bool, reportedAt pgtype.Timestamptz, secondsSinceReport int64) gen.UserNodeStatus {
	if !enabled || !reportedAt.Valid {
		return gen.Offline
	}
	d := time.Duration(secondsSinceReport) * time.Second
	switch {
	case d <= nodeOnlineWithin:
		return gen.Online
	case d <= nodeDegradedWithin:
		return gen.Degraded
	default:
		return gen.Offline
	}
}

// ptrOf 取任意值的地址。契约里大量可选字段是指针，而 Go 不允许对字面量取址。
func ptrOf[T any](v T) *T { return &v }

// ============================================================
// 在线设备
// ============================================================

// kickEffectiveWithinSeconds 是「多久之后真正生效」，固定 60（节点轮询周期）。
//
// 🔴 **这不是装饰。** 配置下发是 60 秒轮询，删掉 user_device_state 的行
// **不会**立刻断开任何连接 —— 节点要到下一次拉 alivelist 才知道。
// user-journey §12.2 的原话是不告知的话「用户会连点五次然后开工单」。
const kickEffectiveWithinSeconds int32 = 60

// ListUserDevices 列出在线设备。**口径是按 IP，一个 IP 一行。**
func (s *Server) ListUserDevices(ctx context.Context, _ gen.ListUserDevicesRequestObject) (gen.ListUserDevicesResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	rows, err := s.db.ListUserDevicesByIP(ctx, userID)
	if err != nil {
		return gen.ListUserDevices500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询在线设备失败", err),
		}, nil
	}

	out := make([]gen.UserDevice, 0, len(rows))
	for _, r := range rows {
		out = append(out, userDeviceView(r))
	}
	return gen.ListUserDevices200JSONResponse{Data: out, Meta: s.meta(ctx)}, nil
}

// userDeviceView 映射一台设备。纯函数。
//
// 🔴 `Id` 是 SQL 算出来的合成值，这里**原样透传**。
// 不在 Go 里重算：devices.sql 的注释写明列表与踢下线必须用同一个表达式，
// 而 Go 侧再写一遍就是第三份 —— 三份里改漏任何一份，
// 现象都是「点了踢下线没反应」，且只对某些 IP 出现。
//
// ⚠️ `first_seen_at` **无法提供**：user_device_state 只有 last_seen_at，
// 每次 push 都覆盖它。字段在契约里是可选的，留空即可 ——
// 但绝不能用 last_seen_at 冒充它（那会让「这台设备什么时候第一次出现」
// 变成一个每分钟都在变的数字）。
func userDeviceView(r dbgen.ListUserDevicesByIPRow) gen.UserDevice {
	d := gen.UserDevice{
		Id: r.ID,
		Ip: r.DeviceIp.String(),
	}
	if t := anyTime(r.LastSeenAt); t != nil {
		d.LastSeenAt = *t
	}
	if r.LastServerID != 0 {
		id := r.LastServerID
		d.NodeId = &id
	}
	if r.LastServerName != nil && *r.LastServerName != "" {
		n := *r.LastServerName
		d.NodeName = &n
	}
	return d
}

// KickUserDevice 把一台设备（= 一个 IP）踢下线。
func (s *Server) KickUserDevice(ctx context.Context, req gen.KickUserDeviceRequestObject) (gen.KickUserDeviceResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	// 🔴 req.Id 原样喂给 DeviceID。KickUserDeviceByID 的 WHERE 里那个 md5 表达式
	// 与 ListUserDevicesByIP 的 SELECT 里那个**逐字相同**，
	// 所以列表给出的 id 必然能命中同一台设备。
	// 中间做任何转换（比如「先查一次拿 device_ip 再删」）都会引入一个 60 秒的窗口：
	// 那台设备完全可能在两条语句之间换到另一个节点上。
	removed, err := s.db.KickUserDeviceByID(ctx, dbgen.KickUserDeviceByIDParams{
		UserID:   userID,
		DeviceID: req.Id,
	})
	if err != nil {
		return gen.KickUserDevice500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "踢下线失败", err),
		}, nil
	}
	if removed == 0 {
		// removed = 0 = 「这个 id 在你名下没有对应的在线设备」。
		// 可能是别人的 id，也可能是这台设备刚好在 5 分钟窗口外掉出去了 ——
		// 两者对用户的动作相同（刷新列表），所以同一个 404。
		return gen.KickUserDevice404JSONResponse{
			ErrNotFoundJSONResponse: s.notFound(ctx, "该设备当前不在线"),
		}, nil
	}
	return gen.KickUserDevice200JSONResponse{Data: kickResult(removed), Meta: s.meta(ctx)}, nil
}

// KickAllUserDevices 把全部设备踢下线。
//
// ⚠️ 0 台也是 200。契约给 kickUserDevice 声明了 404，给本端点**没有** ——
// 用户点了「全部下线」而本来就没人在线，那不是错误。
func (s *Server) KickAllUserDevices(ctx context.Context, _ gen.KickAllUserDevicesRequestObject) (gen.KickAllUserDevicesResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	removed, err := s.db.KickAllUserDevices(ctx, userID)
	if err != nil {
		return gen.KickAllUserDevices500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "全部踢下线失败", err),
		}, nil
	}
	s.logger.InfoContext(ctx, "用户踢下线全部设备", "user_id", userID, "removed", removed)
	return gen.KickAllUserDevices200JSONResponse{Data: kickResult(removed), Meta: s.meta(ctx)}, nil
}

// kickResult 组装踢下线结果。
//
// `removed` 的单位是**设备（IP）**不是行：一个 IP 连三个节点是三行一台设备，
// SQL 用 count(DISTINCT device_ip) 算，不是 :execrows 的行数。
// 回 3 而屏幕上只有一台设备被踢，比不给数字更糟。
func kickResult(removed int32) gen.KickDevicesResult {
	return gen.KickDevicesResult{
		Removed:                removed,
		EffectiveWithinSeconds: kickEffectiveWithinSeconds,
	}
}

// ============================================================
// 用量曲线
// ============================================================

// shanghaiOffset 是 stat_date 的口径时区（0009：「按 Asia/Shanghai 切天」）。
//
// 🔴 **为什么是固定 +08:00 而不是 time.LoadLocation("Asia/Shanghai")。**
// 运行镜像是 `FROM scratch`（api/Dockerfile），里面**没有 tzdata**，
// 也没有 import time/tzdata。LoadLocation 在那里返回 error 而不是 panic ——
// 于是「忽略 error 用 time.UTC 兜底」会让每天有 8 小时（北京 00:00–08:00）
// 算出来的日期比数据库少一天：用户早上看不到昨天的柱子，中午它又自己出现。
// 这正是 devices.sql §3 把窗口计算搬进 SQL 要避免的那个 bug，
// 而补零的日期轴在 Go 侧，同一个坑必须在这里也堵住。
//
// 固定偏移在这里是安全的：中国自 1991 年起不再实行夏令时，
// stat_user 里不可能有 1991 年之前的行。
// ⚠️ 若中国重新实行夏令时，本常量会与 PostgreSQL 的 'Asia/Shanghai' 分叉，
// 现象是每年两次、持续半年的「日期轴整体错一天」。届时必须改成
// import _ "time/tzdata" + LoadLocation。
var shanghaiOffset = time.FixedZone("Asia/Shanghai", 8*60*60)

// usageRangeDays 把契约的 range 枚举映射成天数。
//
// 缺省取 30d（openapi 的 `default: 30d`）。认不出来的取值也取 30d 而不是报错：
// 本端点在契约上没有 4xx 出口（只有 401 / 500），而 30d 是默认值，
// 给默认值比给 500 对用户好。
func usageRangeDays(r *gen.GetUserUsageParamsRange) (int32, gen.UsageSeriesRange) {
	if r == nil {
		return 30, gen.UsageSeriesRangeN30d
	}
	switch *r {
	case gen.GetUserUsageParamsRangeN7d:
		return 7, gen.UsageSeriesRangeN7d
	case gen.GetUserUsageParamsRangeN90d:
		return 90, gen.UsageSeriesRangeN90d
	default:
		return 30, gen.UsageSeriesRangeN30d
	}
}

// GetUserUsage 返回用量曲线。
func (s *Server) GetUserUsage(ctx context.Context, req gen.GetUserUsageRequestObject) (gen.GetUserUsageResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	days, rng := usageRangeDays(req.Params.Range)

	rows, err := s.db.ListUserUsageSeries(ctx, dbgen.ListUserUsageSeriesParams{
		UserID: userID,
		Days:   days,
	})
	if err != nil {
		return gen.GetUserUsage500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询用量曲线失败", err),
		}, nil
	}

	return gen.GetUserUsage200JSONResponse{
		Data: buildUsageSeries(rng, days, rows, time.Now()),
		Meta: s.meta(ctx),
	}, nil
}

// buildUsageSeries 铺满日期轴并求和。纯函数（now 由调用方注入，便于测边界）。
//
// 🔴 **总计由这一份 points 加出来，不另查一条 sum。**
// 另查一条会引入第二次查询，而两次查询之间 BulkUpsertStatUserServer 正在写今天的行 ——
// 结果是「柱状图加起来不等于旁边的总计」。用户一定会发现，而我们无法解释。
//
// ⚠️ 单位是**整数字节**，这里不做任何换算（api-contract §2.6：「流量 | 整数字节」）。
// KB / KiB 是纯展示层的选择，在这里除一次就是第二个口径。
//
// ⚠️ 没有流量的那一天在 stat_user 里**没有行**（不是 0 值行）。补零在这里做，
// 不在 SQL 里用 generate_series —— 那会让每次查询多扫 90 行常量，
// 换来的只是省掉这十几行代码。
func buildUsageSeries(rng gen.UsageSeriesRange, days int32, rows []dbgen.ListUserUsageSeriesRow, now time.Time) gen.UsageSeries {
	type ud struct{ up, down int64 }
	byDate := make(map[string]ud, len(rows))
	for _, r := range rows {
		if !r.StatDate.Valid {
			continue
		}
		k := r.StatDate.Time.Format("2006-01-02")
		v := byDate[k]
		v.up += r.UploadBytes
		v.down += r.DownloadBytes
		byDate[k] = v
	}

	// 轴的右端是「上海时区的今天」，与 SQL 里 `(now() AT TIME ZONE 'Asia/Shanghai')::date`
	// 同口径；左端是 today-days+1，共 days 天 —— 与 SQL 的
	// `stat_date > (today - days)` 覆盖同一个闭区间。
	todaySH := now.In(shanghaiOffset)
	today := time.Date(todaySH.Year(), todaySH.Month(), todaySH.Day(), 0, 0, 0, 0, shanghaiOffset)

	points := make([]gen.UsagePoint, 0, days)
	var totalUp, totalDown int64
	for i := int32(days) - 1; i >= 0; i-- {
		d := today.AddDate(0, 0, -int(i))
		v := byDate[d.Format("2006-01-02")]
		totalUp += v.up
		totalDown += v.down
		points = append(points, gen.UsagePoint{
			Date:          openapi_types.Date{Time: d},
			UploadBytes:   v.up,
			DownloadBytes: v.down,
		})
	}
	return gen.UsageSeries{
		Range:              rng,
		Points:             points,
		TotalUploadBytes:   totalUp,
		TotalDownloadBytes: totalDown,
	}
}

// ============================================================
// 账号自检
// ============================================================

// dataDelayNote 是 DiagnoseResult.data_delay_note 的文案。
//
// 🔴 契约对这个字段的原话是「**不是装饰**」：流量数字有三个天然不一致的口径
// （面板 / 客户端 subscription-userinfo / 邮件快照）。
// 不写这一句，必然有用户拿客户端的数字质问面板的数字，而我们没有任何可以指的东西。
//
// 文案里同时点明设备数是**近似值**：alivelist 拉取失败时 v2node 静默降级为
// 「零在线设备」，所以这个数偏小是常态。
const dataDelayNote = "流量与设备数来自节点每分钟一次的上报，可能有 1–2 分钟延迟；" +
	"设备数按 IP 统计（同一台设备切换 Wi-Fi / 蜂窝会算两台），且节点上报失败时会偏小。" +
	"客户端里显示的用量来自订阅下发时的快照，与本页数字不完全同步属于正常现象。"

// GetUserDiagnose 返回账号侧四项自检。
func (s *Server) GetUserDiagnose(ctx context.Context, _ gen.GetUserDiagnoseRequestObject) (gen.GetUserDiagnoseResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	facts, err := s.db.GetUserDiagnoseFacts(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.GetUserDiagnose401JSONResponse{
				ErrUnauthorizedJSONResponse: s.unauthorizedDeletedUser(ctx, userID, "getUserDiagnose"),
			}, nil
		}
		return gen.GetUserDiagnose500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询自检事实失败", err),
		}, nil
	}
	return gen.GetUserDiagnose200JSONResponse{
		Data: buildDiagnose(facts, time.Now()),
		Meta: s.meta(ctx),
	}, nil
}

// buildDiagnose 由原始事实推四项判定。纯函数（now 注入，便于测到期边界）。
//
// 🔴 **traffic_left 的表达式必须与节点侧逐字相同**：`u + d < transfer_enable`
// （servers.sql 的 ListAvailableUsersByServer 就是这么判的，subscription.go 的
// accountStateOf 也是同一条）。写成 `>=` 或把等号挪到另一边，就会出现
// 「面板说还有流量，节点不给连」—— 而用户看不到节点侧的判定，只能开工单。
//
// ⚠️ device_limit 为 NULL = **不限设备**，必须判成通过，不能当 0 ——
// 当 0 的话所有不限设备的用户都会看到一条红色的「设备超限」。
//
// ⚠️ 设备数是软限制且系统性偏小，所以这一项**只展示不拦截**，
// detail 里把口径写清楚。
func buildDiagnose(f dbgen.GetUserDiagnoseFactsRow, now time.Time) gen.DiagnoseResult {
	checks := make([]gen.DiagnoseCheck, 0, 4)

	// ① 账号可用。
	//
	// ⚠️ `subscription_last_ok_fetched_at` 在契约的 DiagnoseResult 里**没有落点**
	// （只有 subscription_last_fetched_at 一个字段），但它是区分
	// 「他压根没拉」与「他一直在拉、一直被拒」的唯一依据 —— 两者的用户动作完全不同。
	// 放进这一项的 detail：被拒的最常见成因（封禁、一键全撤）恰好都在这一项里。
	accountDetail := map[string]any{
		"banned": f.Banned,
	}
	if f.BannedReason != nil {
		accountDetail["banned_reason"] = *f.BannedReason
	}
	if t := tptr(f.BannedAt); t != nil {
		accountDetail["banned_at"] = *t
	}
	if t := tptr(f.SubRevokedAt); t != nil {
		// 一键全撤之后签发的 token 才有效。客户端里那条旧链接会一直 404。
		accountDetail["sub_revoked_at"] = *t
	}
	if t := anyTime(f.SubscriptionLastOkFetchedAt); t != nil {
		accountDetail["subscription_last_ok_fetched_at"] = *t
	}
	checks = append(checks, gen.DiagnoseCheck{
		Key:    gen.AccountActive,
		Ok:     !f.Banned,
		Detail: &accountDetail,
	})

	// ② 未到期。expired_at IS NULL 是**不限时套餐**，不是「已过期」。
	notExpired := !f.ExpiredAt.Valid || f.ExpiredAt.Time.After(now)
	expiryDetail := map[string]any{}
	if t := tptr(f.ExpiredAt); t != nil {
		expiryDetail["expired_at"] = *t
	} else {
		expiryDetail["unlimited"] = true
	}
	checks = append(checks, gen.DiagnoseCheck{
		Key:    gen.NotExpired,
		Ok:     notExpired,
		Detail: &expiryDetail,
	})

	// ③ 还有流量。三个原始值一并给出：用户拿客户端的数字来质问时，
	// 我们需要能指着同一行说「面板用的是这三个数」。
	trafficDetail := map[string]any{
		"used_bytes":           f.U + f.D,
		"upload_bytes":         f.U,
		"download_bytes":       f.D,
		"total_bytes":          f.TransferEnable,
		"transfer_enable_plan": f.TransferEnablePlan,
		"transfer_enable_pack": f.TransferEnablePack,
	}
	if t := tptr(f.PackExpireAt); t != nil {
		// ADR 0013 §5.3 的「加油包被吃掉了还是结转了」在用户侧的样子。
		trafficDetail["pack_expire_at"] = *t
	}
	checks = append(checks, gen.DiagnoseCheck{
		Key:    gen.TrafficLeft,
		Ok:     f.U+f.D < f.TransferEnable,
		Detail: &trafficDetail,
	})

	// ④ 设备未超限。
	deviceDetail := map[string]any{
		"device_count": f.DeviceCount,
		// 口径必须跟着数字走，否则用户会拿它跟手上的设备台数对质。
		"counted_by":  "ip",
		"approximate": true,
	}
	deviceOK := true
	if f.DeviceLimit == nil {
		deviceDetail["device_limit"] = nil
		deviceDetail["unlimited"] = true
	} else {
		deviceDetail["device_limit"] = *f.DeviceLimit
		deviceOK = f.DeviceCount <= *f.DeviceLimit
	}
	checks = append(checks, gen.DiagnoseCheck{
		Key:    gen.DeviceUnderLimit,
		Ok:     deviceOK,
		Detail: &deviceDetail,
	})

	return gen.DiagnoseResult{
		Checks:                    checks,
		DataDelayNote:             dataDelayNote,
		SubscriptionLastFetchedAt: anyTime(f.SubscriptionLastFetchedAt),
		TrafficLastReportedAt:     tptr(f.TrafficReportedAt),
	}
}

// ============================================================
// 节点面：负载快照上报
// ============================================================

// PushUniProxyStatus 接收节点每 60 秒一次的负载上报。
//
// 🔴 **节点面，裸 JSON 不套信封**（api-contract §2 第 2 条）。响应体固定 `{"data": true}`。
//
// 🔴 **落点是 server_online_state 不是 servers。** 契约的 description 逐字写着
// 「落 servers.load_status jsonb + servers.last_status_at」，而 **servers 表上
// 这两列都不存在**（0004 逐列核过）。0005 建的 server_online_state 一节点一行、
// UNLOGGED、被覆盖 —— 形状与意图完全一致，只是落点换了张表，
// 而且落在 UNLOGGED 表上更对：负载快照崩溃后就该丢，
// servers 是要写 WAL 的配置表，每分钟被 8 个节点覆写一遍只会白白产生死元组。
// 响应形状（NodeAck）不受影响，所以这是描述文字过时，不是 contract drift。
//
// 🔴 **不用 servers.sql 的 UpsertServerOnlineState。** 它的第二个参数是 online_users，
// 而冻结契约的 NodeStatusReport 根本没有这个字段 —— handler 只能传 0，
// 于是每 60 秒一次的负载上报都会把在线人数清零。没有任何报错：
// 一个恒为 0 的运营指标看起来就像「今晚没人用」。
// UpsertServerLoadSnapshot 只写负载七列，online_users 一个字都不碰。
//
// ⚠️ **不 bump node_rev。** 负载上报不改变任何节点要拉的配置或用户列表，
// 顺手 bump 会让 8 个节点每分钟全量重拉一次用户表，正好废掉 ADR 0006 的 ETag。
func (s *Server) PushUniProxyStatus(ctx context.Context, req gen.PushUniProxyStatusRequestObject) (gen.PushUniProxyStatusResponseObject, error) {
	auth, ok := middleware.NodeAuthFrom(ctx)
	if !ok {
		return nil, errNoNodeAuth
	}
	// 权威来源永远是**密钥**绑定的 server_id；query 里的 node_id 只是节点自报。
	if !s.nodeIDMatches(ctx, auth, req.Params.NodeId) {
		return gen.PushUniProxyStatus403JSONResponse{
			NodeForbiddenJSONResponse: gen.NodeForbiddenJSONResponse(errNodeIDMismatch()),
		}, nil
	}
	// 每个节点面端点在 node_id 校验通过后都要记一次心跳（node.go 纪律 4）：
	// bp_node_alive 是「节点心跳缺失」告警的唯一数据来源，而那条告警是 metric absence——
	// 漏掉它不会有任何报错，只会让采样点变少。
	s.noteNodeAlive(ctx, auth)

	if req.Body == nil {
		return gen.PushUniProxyStatus400JSONResponse{
			NodeBadRequestJSONResponse: gen.NodeBadRequestJSONResponse(
				nodeError(gen.VALIDATIONMALFORMEDBODY, "请求体缺失")),
		}, nil
	}

	params, reason := statusReportParams(auth.ServerID, *req.Body)
	if reason != "" {
		// 校验在 handler 做（schema 只有 cpu_pct 的 CHECK 兜底）。
		// 400 而不是静默丢弃：负载数据错了不影响用户上网，但它是「节点是不是快挂了」
		// 的唯一依据 —— 静默接受一个非法值等于让那张图永远看起来正常。
		s.logger.WarnContext(ctx, "节点负载上报字段非法",
			"server_code", auth.ServerCode, "reason", reason)
		return gen.PushUniProxyStatus400JSONResponse{
			NodeBadRequestJSONResponse: gen.NodeBadRequestJSONResponse(
				nodeError(gen.VALIDATIONFAILED, reason)),
		}, nil
	}

	if err := s.db.UpsertServerLoadSnapshot(ctx, params); err != nil {
		s.logger.ErrorContext(ctx, "写入节点负载快照失败",
			"server_code", auth.ServerCode, "err", err)
		return nil, err
	}
	return gen.PushUniProxyStatus200JSONResponse{NodeAckJSONResponse: gen.NodeAckJSONResponse{Data: true}}, nil
}

// statusReportParams 校验并映射一份负载上报。纯函数，第二个返回值非空表示拒绝的理由。
//
// ⚠️ cpu 是本 spec 里**唯一**允许的浮点字段（api-contract §2.6），落到 real 列上；
// mem / swap / disk 是字节，bigint。
//
// ⚠️ used > total 不拒绝，只归零 used？**不** —— 直接拒绝。
// 一个 used > total 的上报要么是节点算错了要么是我们解错了，
// 两种情况下把它写进库都会让容量图出现一根超过 100% 的柱子，
// 而那根柱子会被当成「磁盘要满了」去处理一整晚。
//
// ⚠️ total = 0 是合法的（节点没有 swap 分区是常见配置），此时 used 必须也是 0。
func statusReportParams(serverID int64, rep gen.NodeStatusReport) (dbgen.UpsertServerLoadSnapshotParams, string) {
	var p dbgen.UpsertServerLoadSnapshotParams
	if rep.Cpu < 0 || rep.Cpu > 100 {
		return p, "cpu 必须在 0–100 之间"
	}
	// NaN / Inf 会通过上面的比较吗：NaN 与任何值比较都是 false，所以它**通不过**
	// `rep.Cpu < 0 || rep.Cpu > 100`（两边都 false）→ 会被放行。必须单独判。
	if rep.Cpu != rep.Cpu {
		return p, "cpu 不是一个数字"
	}
	for _, u := range []struct {
		name string
		v    gen.NodeResourceUsage
	}{{"mem", rep.Mem}, {"swap", rep.Swap}, {"disk", rep.Disk}} {
		if u.v.Total < 0 || u.v.Used < 0 {
			return p, u.name + " 的 total / used 不能为负"
		}
		if u.v.Used > u.v.Total {
			return p, u.name + " 的 used 不能大于 total"
		}
	}

	p = dbgen.UpsertServerLoadSnapshotParams{
		ServerID: serverID,
		// float64 → float32 会丢精度，但 cpu 只需要两位小数（0–100），
		// float32 的有效位数远远够用，而列类型就是 real。
		CpuPct:    float32(rep.Cpu),
		MemTotal:  ptrOf(rep.Mem.Total),
		MemUsed:   ptrOf(rep.Mem.Used),
		SwapTotal: ptrOf(rep.Swap.Total),
		SwapUsed:  ptrOf(rep.Swap.Used),
		DiskTotal: ptrOf(rep.Disk.Total),
		DiskUsed:  ptrOf(rep.Disk.Used),
	}
	return p, ""
}
