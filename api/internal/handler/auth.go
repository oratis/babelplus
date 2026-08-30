package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"golang.org/x/crypto/argon2"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 账户体系：注册 / 发码 / 登录 / 会话轮换 / 密码。
//
// 这一组是**唯一能凭空造出一个身份**的代码路径，所以下面几条约束贯穿全文件：
//
//  1. 注册必须持有一个未核销的邀请码（user-journey §1 裁决 2：邀请码可重复使用
//     等于开放注册，与「内部使用」定位直接冲突）。核销走 RedeemInviteCode 的
//     条件 UPDATE，**不是**「先查再写」—— 后者在并发下会让同一个一次性码带进来两个人。
//  2. 登录失败一律 401 `AUTH_INVALID_CREDENTIALS`，且「用户不存在」与「密码错误」
//     走**等长的计算路径**（见 burnPasswordVerification）。只统一错误码不统一耗时，
//     等于把用户枚举从响应体挪到了秒表上。
//  3. 密码用 argon2id，会话 token 用 sha256。两者不是二选一，是各管各的：
//     密码是低熵人类输入，必须慢；会话 token 是 256 位 CSPRNG，慢哈希的成本要在
//     **每一个已登录请求**上付，而收益为零（离线爆破 256 位随机值在物理上不成立）。
//
// 🔴 与契约的一处已知偏差（middleware/user.go 已登记，这里是签发侧的同一条）：
// api-contract.md §5 与 openapi 的 SessionTokens 写的是「access JWT（15 分钟）
// + refresh token（30 天，一次性轮换）」，但 DB 里只有 user_sessions（不透明 token
// + sha256），没有任何 JWT 相关的表或密钥配置，中间件校验的也是不透明 token。
// 因此本文件签发的是**一枚**不透明会话 token，`access_token` 与 `refresh_token`
// 是同一个值，`expires_in` 是这枚 token 的真实剩余秒数（会话 TTL），不是 900。
// 这样做而不是硬塞一个 JWT 进 access_token，是因为那样签出来的 access_token
// 会被中间件当成会话 token 去查库、查不到、每个已登录请求全部 401 ——
// 一个「看起来符合契约」但整条链路不通的实现比一个诚实的偏差坏得多。
// TODO(P2): access JWT 落地时，本文件改为签发 JWT + 独立的 refresh 值，
// 那时 SessionTokens 的两个字段才真正分开，expires_in 才是 900。

// ============================================================
// 密码哈希：argon2id
// ============================================================

// argon2id 参数。**不要在没算过内存账的情况下调大 argon2MemoryKiB。**
//
// 取值 m=19 MiB / t=2 / p=1 是 OWASP 对 argon2id 的最低推荐档
// （另一档是 m=64 MiB / t=3，两者被认为等价安全）。选低内存档的理由是一道乘法：
//
//	Cloud Run 实例规格 --memory=512Mi、--concurrency=80（infra/deploy/deploy-api.sh）。
//	64 MiB × 8 并发 = 512 MiB，一个实例的全部内存；× 80 并发 = 5 GiB。
//	也就是说**不限并发的话，80 个并发登录请求就能把实例 OOM 掉** ——
//	一个不需要任何凭据、不需要任何量级的拒绝服务。
//
// 所以除了选低内存档，还必须限制**同时进行的哈希次数**（argon2Slots）。
// 两者缺一不可：只调参数不限并发，攻击者用并发把内存乘回去；
// 只限并发不调参数，4 × 64 MiB = 256 MiB 仍然占掉半个实例。
//
// p=1 而不是 p=4：--cpu=1，并行度超过核数不增加攻击者的成本，只增加我们的调度开销。
const (
	argon2Time      uint32 = 2
	argon2MemoryKiB uint32 = 19456 // 19 MiB
	argon2Threads   uint8  = 1
	argon2KeyLen    uint32 = 32
	argon2SaltLen          = 16

	// passwordAlgo 写进 users.password_algo，供将来换算法时识别存量格式。
	passwordAlgo = "argon2id"
)

// argon2Slots 限制同时进行的 argon2 计算数，把峰值内存钉在
// argon2Concurrency × argon2MemoryKiB（4 × 19 MiB ≈ 76 MiB）。
//
// 超出的请求在这里排队而不是被拒绝：登录本身是低频操作，排队几十毫秒无感，
// 而返回 503 会把一次流量尖峰变成一次可见故障。
const argon2Concurrency = 4

var argon2Slots = make(chan struct{}, argon2Concurrency)

func acquireArgon2Slot(ctx context.Context) error {
	select {
	case argon2Slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		// 客户端已断开或请求超时：不要再去算一次没人接收的哈希。
		return ctx.Err()
	}
}

func releaseArgon2Slot() { <-argon2Slots }

// argon2Params 是从编码串里解析出来的参数。
//
// 校验的**不是**我们自己写出来的值，而是从数据库读回来的值：
// password_hash 是一列普通 text，任何能写这一列的路径（后台工具、迁移脚本、
// 将来的导入功能）都可能塞进一个 m=4194304 的串，而 argon2.IDKey 会老老实实
// 去申请 4 GiB。这是「解析不可信输入」而不是洁癖。
type argon2Params struct {
	Memory  uint32
	Time    uint32
	Threads uint8
}

const (
	maxArgon2MemoryKiB uint32 = 1 << 20 // 1 GiB，远高于任何合理配置
	maxArgon2Time      uint32 = 16
	maxArgon2Threads   uint8  = 16
	maxArgon2KeyLen           = 64
)

// hashPassword 生成 PHC 格式的 argon2id 编码串。
//
// 输出形如：$argon2id$v=19$m=19456,t=2,p=1$<b64salt>$<b64hash>
// 参数与 salt 一起存，所以将来调参不需要强制所有人改密码 ——
// 老口令按老参数验，验过之后可以就地重算（TODO(P2) 见 VerifyPassword 注释）。
func hashPassword(ctx context.Context, plain string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成 salt 失败: %w", err)
	}
	if err := acquireArgon2Slot(ctx); err != nil {
		return "", err
	}
	defer releaseArgon2Slot()

	key := argon2.IDKey([]byte(plain), salt, argon2Time, argon2MemoryKiB, argon2Threads, argon2KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2MemoryKiB, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

var errBadPasswordHash = errors.New("password_hash 格式非法")

// parseArgon2Hash 解析 PHC 编码串。
func parseArgon2Hash(encoded string) (argon2Params, []byte, []byte, error) {
	var p argon2Params

	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, key]
	if len(parts) != 6 || parts[0] != "" || parts[1] != passwordAlgo {
		return p, nil, nil, errBadPasswordHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, errBadPasswordHash
	}

	var mem, tim uint64
	var thr uint64
	kv := strings.Split(parts[3], ",")
	if len(kv) != 3 {
		return p, nil, nil, errBadPasswordHash
	}
	var err error
	if mem, err = parseKV(kv[0], "m="); err != nil {
		return p, nil, nil, errBadPasswordHash
	}
	if tim, err = parseKV(kv[1], "t="); err != nil {
		return p, nil, nil, errBadPasswordHash
	}
	if thr, err = parseKV(kv[2], "p="); err != nil {
		return p, nil, nil, errBadPasswordHash
	}
	if mem == 0 || mem > uint64(maxArgon2MemoryKiB) ||
		tim == 0 || tim > uint64(maxArgon2Time) ||
		thr == 0 || thr > uint64(maxArgon2Threads) {
		return p, nil, nil, errBadPasswordHash
	}
	p = argon2Params{Memory: uint32(mem), Time: uint32(tim), Threads: uint8(thr)}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return p, nil, nil, errBadPasswordHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 || len(key) > maxArgon2KeyLen {
		return p, nil, nil, errBadPasswordHash
	}
	return p, salt, key, nil
}

func parseKV(field, prefix string) (uint64, error) {
	v, ok := strings.CutPrefix(field, prefix)
	if !ok {
		return 0, errBadPasswordHash
	}
	return strconv.ParseUint(v, 10, 32)
}

// verifyPassword 用编码串里记录的参数重算并做恒定时间比较。
//
// 返回 (false, err) 表示存量哈希本身有问题（例如 AnonymizeUser 把 password_hash
// 置成了空串）；调用方**必须**把它与「密码不对」一视同仁地处理成 401，
// 否则「这个账号是被注销的」就从错误码里泄漏出去了。
func verifyPassword(ctx context.Context, encoded, plain string) (bool, error) {
	p, salt, want, err := parseArgon2Hash(encoded)
	if err != nil {
		return false, err
	}
	if err := acquireArgon2Slot(ctx); err != nil {
		return false, err
	}
	defer releaseArgon2Slot()

	got := argon2.IDKey([]byte(plain), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	// TODO(P2): 参数低于当前档时就地重算并写回（登录路径已经拿到了明文口令，
	// 这是唯一能不打扰用户就完成迁移的时机）。需要一条 UpdateUserPassword 调用，
	// 且必须只在验证通过后做。
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyPasswordHash 是一个对着随机口令算出来的编码串，只用于「用户不存在」时烧掉
// 与真实校验等量的时间。
//
// 懒加载（第一次登录失败时才算）而不是 init()：Cloud Run 冷启动有启动探针，
// 在 init 里挂 40 毫秒的 argon2 是给每一次冷启动无条件加成本。
//
// 口令取随机值而不是常量：即使有人拿到这个编码串，它也不对应任何可登录的口令。
var dummyPasswordHash = sync.OnceValue(func() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	h, err := hashPassword(context.Background(), base64.RawURLEncoding.EncodeToString(b))
	if err != nil {
		return ""
	}
	return h
})

// burnPasswordVerification 在「用户不存在」分支上消耗与真实校验相同的算力。
//
// 不做这件事的后果是可测量的：命中存在的邮箱要跑一次 argon2（几十毫秒），
// 不存在的邮箱直接返回（亚毫秒）。两者差三个数量级，用一次请求就能判定
// 一个邮箱是否注册过 —— 而这正是统一错误码想挡住的事。
func burnPasswordVerification(ctx context.Context, plain string) {
	if h := dummyPasswordHash(); h != "" {
		_, _ = verifyPassword(ctx, h, plain)
	}
}

// ============================================================
// 随机值、邮箱、口令策略
// ============================================================

const (
	// sessionTokenBytes 32 字节 = 256 位。base64url 无填充后 43 字符，
	// 落在 middleware.plausibleSessionToken 的 [24,128] 区间内。
	sessionTokenBytes = 32
	// resetTokenBytes 与会话 token 同长。见 GetEmailVerificationByCodeHash 的注释：
	// 找回密码只能按 code_hash 全表定位，令牌熵不足会变成「随便猜一个就命中别人」。
	resetTokenBytes = 32

	emailCodeDigits = 6

	// sessionTTL 30 天，对齐 api-contract §5 的 refresh 有效期。
	sessionTTL = 30 * 24 * time.Hour
	// emailCodeTTL 10 分钟（user-journey §3）。
	emailCodeTTL = 10 * time.Minute
	// resetTokenTTL 30 分钟。比验证码长，因为找回密码要经过「收信 → 点链接 → 想新密码」
	// 三步，10 分钟里做完这些对不熟练的用户是紧的；但也不能像会话那样长，
	// 它是一枚能直接接管账号的凭据。
	resetTokenTTL = 30 * time.Minute

	// 口令长度区间与 openapi 的 minLength/maxLength 一致。
	minPasswordRunes = 8
	maxPasswordRunes = 128
)

// randomToken 返回 base64url 无填充的高熵串。
func randomToken(nbytes int) (string, error) {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机 token 失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// randomDigits 生成 n 位十进制验证码。
//
// 用拒绝采样而不是 `b % 10`：256 不是 10 的整数倍，直接取模会让数字 0–5
// 比 6–9 多出约 2.7% 的出现概率。对 6 位码而言这点偏斜不足以致命，
// 但它是零成本可以避免的，而「验证码分布有偏」这种事没人会去复查。
func randomDigits(n int) (string, error) {
	const digits = "0123456789"
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成验证码失败: %w", err)
		}
		if buf[0] >= 250 { // 250 = 25 × 10，丢弃 250–255
			continue
		}
		out = append(out, digits[buf[0]%10])
	}
	return string(out), nil
}

// normalizeEmail 统一成小写去空白。
//
// 入库也用这个归一化后的值：users_email_uk 建在 lower(email) 上，
// 存原样大小写不会造成重复注册，但会让「日志里的邮箱」与「查询用的邮箱」
// 长得不一样，排查时要多想一步。
func normalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// validEmail 做一次结构校验。
//
// openapi 标了 `format: email`，但 oapi-codegen 生成的服务端**不做**格式校验
// （没有挂 request validator 中间件），所以这里是唯一一道闸。
func validEmail(e string) bool {
	if len(e) < 3 || len(e) > 254 {
		return false
	}
	addr, err := mail.ParseAddress(e)
	// addr.Address != e 用来拒掉 `张三 <a@b.com>` 这类带显示名的形态 ——
	// mail.ParseAddress 认它，但那不是一个可以直接入库的邮箱地址。
	if err != nil || addr.Address != e {
		return false
	}
	at := strings.LastIndex(e, "@")
	domain := e[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	return true
}

// emailDomain 取收件域名，写进 email_log.to_domain 供按域名分组统计送达率
// （ADR 0002 §7 要求的实测数据源）。
func emailDomain(e string) string {
	if at := strings.LastIndex(e, "@"); at >= 0 && at+1 < len(e) {
		return e[at+1:]
	}
	return ""
}

// validPassword 只管长度，不做「必须含大小写数字符号」那类组合规则。
//
// 组合规则会把用户推向 `Passw0rd!` 这种可预测形态，而长度是唯一与实际
// 破解成本单调相关的约束。上限 128 是契约写死的，也顺带挡住了
// 「超长口令拖慢 argon2」这条廉价的资源消耗路径。
func validPassword(p string) bool {
	n := utf8.RuneCountInString(p)
	return n >= minPasswordRunes && n <= maxPasswordRunes
}

// hashEmailCode 计算验证码的存库哈希（register / email_change 用）。
//
// 拌入邮箱：这两类的查找走 (email, purpose)，理论上不需要，
// 但拌进去之后一条 code_hash 就不可能在另一个邮箱下被复用 ——
// 将来谁加了一条按 code_hash 查找的路径也不会打开缺口。
func hashEmailCode(pepper, email string, purpose dbgen.VerificationPurpose, code string) []byte {
	sum := sha256.Sum256([]byte(pepper + "|" + string(purpose) + "|" + normalizeEmail(email) + "|" + code))
	return sum[:]
}

// hashResetToken 计算找回密码令牌的存库哈希。
//
// **不能拌邮箱**：重置链路的输入只有 token，查找必须能只凭哈希定位。
// 这也是重置令牌必须高熵的原因（见 resetTokenBytes 的注释）。
func hashResetToken(pepper, token string) []byte {
	sum := sha256.Sum256([]byte(pepper + "|" + string(dbgen.VerificationPurposePasswordReset) + "|" + token))
	return sum[:]
}

// verificationPurposeForScene 把契约里的 scene 映射到 DB 枚举。
//
// ⚠️ 两套命名不一致，且**不是笔误**：openapi 的 EmailCodeRequest.scene 是
// register / reset_password / bind_email，DB 的 verification_purpose 是
// register / password_reset / email_change。这个函数是两者之间唯一的翻译点，
// 别在别处直接把 scene 字符串塞进 SQL。
func verificationPurposeForScene(scene gen.EmailCodeRequestScene) (dbgen.VerificationPurpose, bool) {
	switch scene {
	case gen.Register:
		return dbgen.VerificationPurposeRegister, true
	case gen.ResetPassword:
		return dbgen.VerificationPurposePasswordReset, true
	case gen.BindEmail:
		return dbgen.VerificationPurposeEmailChange, true
	default:
		return "", false
	}
}

// newVerificationSecret 按用途生成验证码/令牌与它的存库哈希。
//
// 🔴 「password_reset 用高熵令牌、其余用 6 位数字」这条规则**只在这里实现一次**。
// 拆到各个 handler 里各写一遍的后果是具体的：谁在 /auth/email-code 上给
// scene=reset_password 也发一个 6 位码，就等于让 GetEmailVerificationByCodeHash
// 变成「随便猜 6 位数就能命中任意用户的重置记录」。
func newVerificationSecret(pepper, email string, purpose dbgen.VerificationPurpose) (secret string, hash []byte, err error) {
	if purpose == dbgen.VerificationPurposePasswordReset {
		secret, err = randomToken(resetTokenBytes)
		if err != nil {
			return "", nil, err
		}
		return secret, hashResetToken(pepper, secret), nil
	}
	secret, err = randomDigits(emailCodeDigits)
	if err != nil {
		return "", nil, err
	}
	return secret, hashEmailCode(pepper, email, purpose, secret), nil
}

// ============================================================
// 请求元数据（来源 IP / User-Agent）
// ============================================================

// errNoUserAuth 表示中间件没有把用户身份注入上下文 —— 装配错误，不是「未登录」。
//
// 未登录的请求根本到不了 handler（会被 RequireUser 挡在 401）。所以这里必须
// 冒出 500 而不是 401：把装配错误伪装成鉴权问题，会让「用户面鉴权忘了挂」
// 表现为「所有人都登录不上」，而日志里一条异常都没有。
var errNoUserAuth = errors.New("用户身份缺失：路由未挂载 RequireUser 中间件")

// PublicOperations 是**免登录**的 operation 全集。
//
// 由来：openapi.yaml 里显式写了 `security: []` 的那 11 个 operation，一个不多一个不少。
// 其余每个 operation 都声明了具体的 scheme（userSession 41 个 / adminSession 61 个 /
// nodeKey 6 个 / internalOidc 9 个），没有任何一个 operation 是「没写 security」的 ——
// 所以这张表可以机械地从契约推出来，不需要有人凭记忆维护。
//
// 🔴 为什么给的是**免登录**清单而不是「需要登录」清单：
// 两者写反的后果完全不对称。漏一个「需要登录」的 operation，它的 handler 里
// UserFrom(ctx) 拿不到身份 → errNoUserAuth → 500，响亮地坏掉；
// 而漏一个「免登录」的 operation，**注册端点会要求先登录** —— 新用户永远进不来，
// 且这个故障只有新用户遇得到，我们自己测不出来。
// 所以正确的装配形状是 deny-by-default：不在本表里的一律要凭据。
//
// 本轮（账户体系）只负责裁定这条规则与这张表。真正挂进链路是 main.go 的事，
// 且还需要把「userSession / adminSession / internalOidc」三种凭据分开挂 ——
// 那要等管理面与内部面各自成文。
var PublicOperations = map[string]bool{
	"GetHealthz": true,
	// 订阅下发：凭据是 URL 里的订阅 token 本身，不是会话。
	"GetShortSubscription":  true,
	"GetClientSubscription": true,
	// 账户入口：这几个恰恰是「还没有会话」的人要用的。
	"RegisterAccount":  true,
	"SendEmailCode":    true,
	"Login":            true,
	"RefreshToken":     true,
	"ForgotPassword":   true,
	"ResetPassword":    true,
	"VerifyInviteCode": true,
	// 支付回调：凭据是网关的签名，不是会话（api-contract §8.1）。
	"HandlePaymentNotify": true,
}

// RequestMetadata 是写进 user_sessions / email_verifications / invite_code_uses
// 的来源信息。
//
// 两个字段都是指针：对应的列都可空，而「没采集到」与「采集到空串」必须能区分 ——
// 后者会让审计表里出现一批看不出所以然的空值行。
type RequestMetadata struct {
	IP        *netip.Addr
	UserAgent *string
}

// maxUserAgentLen 截断上限。user_agent 是无长度限制的 text，
// 不截断的话一个 1 MB 的 UA 头就能往表里写 1 MB。
const maxUserAgentLen = 512

// requestMetadata 从上下文里的原始请求提取来源 IP 与 User-Agent。
//
// 复用 subscription.go 的 RequestBinding()（已挂在 main.go 的 StrictMiddlewareFunc
// 列表里）而不是另起一个中间件：同一个包里放两套「把请求塞进 ctx」的机制，
// 迟早会有一个没被挂上，而现象是**某几个端点的审计字段静默为空**。
//
// 与订阅面的处理刻意不同：那边拿不到请求就 500（格式分发会静默退化成 base64，
// 是能返回 200 的失效）；这边只记一条警告然后继续 —— 注册/登录本身不依赖 IP，
// 为了一列审计信息让用户注册不了是把代价放错了地方。
func (s *Server) requestMetadata(ctx context.Context) RequestMetadata {
	var m RequestMetadata
	r, ok := boundRequestFrom(ctx)
	if !ok {
		s.logger.WarnContext(ctx, "缺少原始请求，来源 IP / UA 将写入 NULL（未挂载 handler.RequestBinding()）")
		return m
	}
	if ip, err := netip.ParseAddr(middleware.ClientIP(r, s.cfg.TrustProxyHeaders)); err == nil {
		// Unmap 掉 ::ffff:1.2.3.4 形态：inet 列里混着两种写法会让
		// 「同一个 IP 请求了几次」的分组统计静默算错。
		ip = ip.Unmap()
		m.IP = &ip
	}
	if ua := strings.TrimSpace(r.UserAgent()); ua != "" {
		if len(ua) > maxUserAgentLen {
			ua = ua[:maxUserAgentLen]
		}
		m.UserAgent = &ua
	}
	return m
}

// ============================================================
// 响应构造
// ============================================================

func (s *Server) meta(ctx context.Context) gen.Meta {
	return gen.Meta{RequestId: middleware.RequestIDFrom(ctx)}
}

func (s *Server) envelope(ctx context.Context, code gen.ErrorCode, msg string, details ...gen.ErrorDetail) gen.ErrorEnvelope {
	body := gen.ErrorBody{Code: code, Message: msg}
	if len(details) > 0 {
		body.Details = &details
	}
	return gen.ErrorEnvelope{Error: body, Meta: s.meta(ctx)}
}

func (s *Server) badRequest(ctx context.Context, code gen.ErrorCode, msg string, d ...gen.ErrorDetail) gen.ErrBadRequestJSONResponse {
	return gen.ErrBadRequestJSONResponse{
		Body:    s.envelope(ctx, code, msg, d...),
		Headers: gen.ErrBadRequestResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}
}

func (s *Server) unauthorized(ctx context.Context, code gen.ErrorCode, msg string) gen.ErrUnauthorizedJSONResponse {
	return gen.ErrUnauthorizedJSONResponse{
		Body:    s.envelope(ctx, code, msg),
		Headers: gen.ErrUnauthorizedResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}
}

func (s *Server) conflict(ctx context.Context, msg string) gen.ErrConflictJSONResponse {
	return gen.ErrConflictJSONResponse{
		Body:    s.envelope(ctx, gen.STATECONFLICT, msg),
		Headers: gen.ErrConflictResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}
}

func (s *Server) unprocessable(ctx context.Context, msg string, d ...gen.ErrorDetail) gen.ErrUnprocessableJSONResponse {
	return gen.ErrUnprocessableJSONResponse{
		Body:    s.envelope(ctx, gen.VALIDATIONFAILED, msg, d...),
		Headers: gen.ErrUnprocessableResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}
}

func (s *Server) rateLimited(ctx context.Context, msg string, retryAfter int32) gen.ErrRateLimitedJSONResponse {
	return gen.ErrRateLimitedJSONResponse{
		Body: s.envelope(ctx, gen.QUOTARATELIMITED, msg),
		Headers: gen.ErrRateLimitedResponseHeaders{
			RetryAfter: retryAfter,
			XRequestId: middleware.RequestIDFrom(ctx),
		},
	}
}

// internalErr 记日志并构造 500 信封。
//
// 刻意不走「return nil, err 让 main.go 兜底」：那条路径写出来的 code 是 `INTERNAL`，
// 而它**不在** openapi 的 ErrorCode enum 里（enum 里是 INTERNAL_ERROR），
// 前端按 code 分支时会落到兜底。本文件的响应一律用 enum 内的码。
func (s *Server) internalErr(ctx context.Context, msg string, err error) gen.ErrInternalJSONResponse {
	s.logger.ErrorContext(ctx, msg, "err", err, "request_id", middleware.RequestIDFrom(ctx))
	return gen.ErrInternalJSONResponse{
		Body:    s.envelope(ctx, gen.INTERNALERROR, "内部错误"),
		Headers: gen.ErrInternalResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}
}

func detail(field, reason string) gen.ErrorDetail {
	return gen.ErrorDetail{Field: field, Reason: reason}
}

func tstz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// isUniqueViolation 识别 PG 的唯一约束冲突。
//
// 注册路径必须靠它兜住并发：先 GetUserByEmail 查一次只是为了给出好的错误信息，
// **真正的互斥是 users_email_uk 这个唯一索引** —— 两个请求同时通过预检查时，
// 一定有一个在 INSERT 上撞索引，那一个必须转成 409 而不是 500。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// isCheckViolation 识别 PG 的 CHECK 约束冲突（23514）。
//
// 与 isUniqueViolation 同一形状、同一理由：**让数据库拒绝，再把拒绝翻译成 422/409**，
// 而不是自己先读一次再比大小 —— 后者是一次 TOCTOU，读与写之间被另一笔操作插进来，
// 判断就是错的。管理面有五处 CHECK 会被**正常输入**触发，全部必须翻成 422/409 而不是 500
// （500 会让管理员反复重试一个被数据库正确拒绝的请求）：
//   - `users.transfer_enable_plan >= 0`（D1 把总配额改到低于已购加油包，0003）
//   - `plans_cycle_needs_monthly`（kind='cycle' 必须有 price_monthly，0016）
//   - `plans.speed_limit_mbps > 0`（不限速是 NULL 不是 0，0002）
//   - `commissions.amount >= 0`（D11 负向调整调过头，0007）
//   - `wallet_balances.balance >= 0`（退款追回佣金时扣到负数，0007）——
//     这一条尤其不能是 500：现象会是「退款偶尔报服务器错误」，没有人会想到去看余额。
//
// 放在这里而不是某个 admin_*.go：它是 PG 错误码的分类，不是管理面的业务规则。
// 曾经三个并行交付的 admin_*.go 各带一份（isPgCheckViolation / adminCheckViolation /
// isCheckViolation），三份一字不差。
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// ============================================================
// 精确档限流（api-contract §10.1 的限额 / §10.2 的存储）
// ============================================================

// 桶名。**窗口长度必须编进桶名。**
//
// 一行 rate_limit 只持有一个 window_start，把 5/min 与 10/h 塞进同一个桶
// 会让两条规则互相覆盖对方的窗口 —— 现象是「两条限额都在，但哪条都不准」，
// 而且只在两条同时接近触顶时才显形。
const (
	bucketLoginIPMinute    = "login_ip_1m"
	bucketLoginIPHour      = "login_ip_1h"
	bucketLoginEmailMinute = "login_email_1m"
	bucketLoginEmailHour   = "login_email_1h"
	bucketEmailCodeIPHour  = "email_code_ip_1h"
	bucketForgotIPHour     = "forgot_ip_1h"
	bucketInviteIPMinute   = "invite_ip_1m"
)

// 限额。**来源已标注**，照 monitoring.md 的约定分「契约」与「设定」两档 ——
// 「设定」= 拍板的值，没有实测依据，上线后要按基线回来改。
const (
	// 契约：api-contract §10.1「POST /auth/login，per IP + per email 双维度，
	// 5/min 且 10/h」。
	loginPerMinute = 5
	loginPerHour   = 10

	// 设定：契约给了 email-code 的 per email 3/h 与 60 秒间隔，**没有给 per IP 维度**。
	// 取 10/h 是沿用 forgot 的 per IP 值，因为两者消耗的是同一份 SES 配额，
	// 而 SES 退信率 ≥ 5% 进审查这条外部约束对两个端点是同一条。
	// per email 那两条限额挡不住「一个 IP 轮着换邮箱发码」——正是这个缺口需要它。
	emailCodePerIPPerHour = 10

	// 设定：契约没给 /invite/verify 任何限额（它既不是登录也不消耗邮件）。
	// 30/min 的依据是「人类填一次注册表单不可能超过它，而它把这个免登录端点
	// 当免费数据库查询放大器用的价值压到很低」。**不是**为了防邀请码枚举 ——
	// 那件事靠的是码本身的随机性（4–32 位、剔除易混字符）。
	invitePerIPPerMinute = 30
)

// rateRule 是一条待检查的限流规则。
type rateRule struct {
	bucket  string
	subject string // 明文（IP 或归一化邮箱）；哈希由 ratelimit 包负责
	limit   int
	window  time.Duration
}

// checkRateRules 依次检查若干条规则，**第一条**超限就返回它的 Retry-After 秒数。
//
// 三条实现要点：
//
//  1. subject 为空的规则被**跳过**而不是归到一个「未知」桶里。
//     采集不到来源 IP（没挂 RequestBinding、或 XFF 解析不出地址）时把所有人算成
//     同一个 subject，会让第一个触顶的人把所有人一起锁在门外。
//     宁可漏限流也不要误伤 —— 与 ForgotPassword 里原有的那句注释同一条取舍。
//
//  2. Allow 的 error 被**刻意忽略**。它在失败时已经放行并写了
//     bp_ratelimit_degraded；在这里再判一次 err 只会诱使人写出「err != nil 就拒绝」，
//     而那正是 ratelimit 包注释里论证过不能做的事。
//
//  3. 短路返回意味着后面的桶这一次不计数。这是有意的：既然请求已经要被拒，
//     再去给其余维度记账只是额外的写入 —— 而拒绝本身已经保护了下游。
func (s *Server) checkRateRules(ctx context.Context, rules ...rateRule) (int32, bool) {
	for _, r := range rules {
		if r.subject == "" {
			continue
		}
		allowed, retry, _ := s.limiter.Allow(ctx, r.bucket, r.subject, r.limit, r.window)
		if !allowed {
			return retryAfterSeconds(retry), true
		}
	}
	return 0, false
}

// retryAfterSeconds 把剩余时长换成 `Retry-After` 头要的整数秒。
//
// **向上取整，且至少 1。** 向下取整会让守规矩的客户端在窗口结束前一刻重试，
// 再吃一个 429 —— 一次完全可以避免的往返，而且它看起来像是限流器在骗人。
func retryAfterSeconds(d time.Duration) int32 {
	if d <= 0 {
		return 1
	}
	secs := (d + time.Second - 1) / time.Second
	return int32(secs)
}

// rateSubjectIP 取来源 IP 作为限流维度；采集不到时返回空串（该维度被跳过）。
func rateSubjectIP(meta RequestMetadata) string {
	if meta.IP == nil {
		return ""
	}
	return meta.IP.String()
}

// ============================================================
// 会话签发
// ============================================================

// issueSession 写一条 user_sessions 并返回明文 token。
//
// 哈希**必须**调 middleware.HashSessionToken —— 签发侧与校验侧各写一遍
// sha256(pepper+raw) 的后果是某天有人改了一侧的拼接顺序，
// 表现为「部分用户随机被登出」（middleware/user.go 已把这条写在函数注释里）。
func (s *Server) issueSession(ctx context.Context, q dbgen.Querier, userID int64, meta RequestMetadata, now time.Time) (dbgen.UserSession, string, error) {
	raw, err := randomToken(sessionTokenBytes)
	if err != nil {
		return dbgen.UserSession{}, "", err
	}
	sess, err := q.CreateUserSession(ctx, dbgen.CreateUserSessionParams{
		UserID:      userID,
		RefreshHash: middleware.HashSessionToken(s.cfg.SessionSigningKey, raw),
		UserAgent:   meta.UserAgent,
		CreatedIp:   meta.IP,
		ExpiresAt:   tstz(now.Add(sessionTTL)),
	})
	if err != nil {
		return dbgen.UserSession{}, "", err
	}
	return sess, raw, nil
}

// sessionTokens 组装契约里的 SessionTokens。
//
// access_token 与 refresh_token 是同一个值，见文件头的偏差说明。
// expires_in 给的是这枚 token 的**真实**剩余秒数，不是契约里写的 900 ——
// 报一个假的 900 会让前端每 15 分钟发一次注定无意义的 refresh。
func sessionTokens(sess dbgen.UserSession, raw string, now time.Time) gen.SessionTokens {
	remaining := int32(0)
	if sess.ExpiresAt.Valid {
		if d := sess.ExpiresAt.Time.Sub(now); d > 0 {
			remaining = int32(d / time.Second)
		}
	}
	return gen.SessionTokens{
		AccessToken:  raw,
		RefreshToken: raw,
		TokenType:    gen.Bearer,
		ExpiresIn:    remaining,
	}
}

// ============================================================
// 邀请码
// ============================================================

// classifyInviteCode 把一行 invite_codes 判成契约的三态之一。
//
// 顺序有意义：先判吊销再判用尽。一个「被吊销且已用完」的码报 invalid 而不是
// exhausted —— 吊销是更强的陈述，告诉用户「去催邀请人再生成一个」是错的引导。
func classifyInviteCode(c dbgen.InviteCode, now time.Time) gen.InviteVerifyResultState {
	switch {
	case c.RevokedAt.Valid:
		return gen.InviteVerifyResultStateInvalid
	case c.ExpiresAt.Valid && !c.ExpiresAt.Time.After(now):
		return gen.InviteVerifyResultStateInvalid
	case c.UsedCount >= c.MaxUses:
		return gen.InviteVerifyResultStateExhausted
	default:
		return gen.InviteVerifyResultStateOk
	}
}

// normalizeInviteCode 归一化成大写去空白。
//
// 0003_accounts.up.sql 的注释写明 code 入库即为大写（且剔除 0/O/1/I/l 等易混字符）。
// 用户从邮件里复制时大小写与前后空格都可能变，这里不归一化的话，
// 一个完全有效的码会被判成「不存在」。
func normalizeInviteCode(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

const (
	minInviteCodeLen = 4
	maxInviteCodeLen = 32
)

func plausibleInviteCode(c string) bool {
	return len(c) >= minInviteCodeLen && len(c) <= maxInviteCodeLen
}

// VerifyInviteCode 校验邀请码，区分「无效」与「已用尽」。
//
// 本端点是**免登录**的，而且会如实回答「这个码存在但已用尽」—— 契约要求如此
// （两种结果对应的用户动作完全不同：一个是「换个码」，一个是「催邀请人再生成」）。
// 代价是它天然可被探测，缓解有两条，缺一不可：
//
//  1. 邀请码本身的随机性（4–32 位，且入库时剔除 0/O/1/I/l 等易混字符）；
//  2. 下面这条 per-IP 限流 —— 它挡的不是枚举（码空间本来就爆破不动），
//     而是「把一个免登录端点当成免费的数据库查询放大器」。
//
// 第 2 条曾经因为没有 rate_limit 表而缺席（TODO(P1)），0013 落地后补上。
func (s *Server) VerifyInviteCode(ctx context.Context, req gen.VerifyInviteCodeRequestObject) (gen.VerifyInviteCodeResponseObject, error) {
	if retry, limited := s.checkRateRules(ctx, rateRule{
		bucket:  bucketInviteIPMinute,
		subject: rateSubjectIP(s.requestMetadata(ctx)),
		limit:   invitePerIPPerMinute,
		window:  time.Minute,
	}); limited {
		return gen.VerifyInviteCode429JSONResponse{ErrRateLimitedJSONResponse: s.rateLimited(ctx,
			"校验过于频繁，请稍后再试", retry)}, nil
	}

	code := normalizeInviteCode(req.Params.Code)

	result := gen.InviteVerifyResult{Valid: false, State: gen.InviteVerifyResultStateInvalid}
	if plausibleInviteCode(code) {
		row, err := s.db.GetInviteCodeAnyState(ctx, code)
		switch {
		case err == nil:
			result.State = classifyInviteCode(row, time.Now())
			result.Valid = result.State == gen.InviteVerifyResultStateOk
		case errors.Is(err, pgx.ErrNoRows):
			// 保持 invalid
		default:
			return gen.VerifyInviteCode500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询邀请码失败", err)}, nil
		}
	}

	return gen.VerifyInviteCode200JSONResponse{Data: result, Meta: s.meta(ctx)}, nil
}

// ============================================================
// 注册
// ============================================================

// 注册链路的失败态。分开定义是为了让事务体只关心「哪里错了」，
// 由外层统一决定 HTTP 状态码 —— 事务体里不该出现 gen.XxxResponse。
var (
	errInviteRaced    = errors.New("邀请码在核销瞬间被用尽")
	errEmailTaken     = errors.New("邮箱已被注册")
	errNoDefaultGroup = errors.New("server_groups 为空，注册无法确定 group_id")
	errVerifyNotFound = errors.New("没有待验证的验证码")
	errVerifyExpired  = errors.New("验证码已过期")
	errVerifyAttempts = errors.New("验证码尝试次数已用尽")
	errVerifyMismatch = errors.New("验证码不正确")
)

// RegisterAccount 注册（邀请码 + 邮箱验证码双闸）。
//
// 顺序刻意是「先验邀请码 → 再验邮箱验证码 → 最后建账号」，与 user-journey §3
// 的「验证码通过后才真正建账号并核销邀请码」一致：
// 邀请码是稀缺资源，先核销后失败等于每一次输错验证码都烧掉一个码。
func (s *Server) RegisterAccount(ctx context.Context, req gen.RegisterAccountRequestObject) (gen.RegisterAccountResponseObject, error) {
	if req.Body == nil {
		return gen.RegisterAccount400JSONResponse{ErrBadRequestJSONResponse: s.badRequest(ctx, gen.VALIDATIONMALFORMEDBODY, "请求体不能为空")}, nil
	}
	body := *req.Body
	now := time.Now()
	meta := s.requestMetadata(ctx)

	email := normalizeEmail(string(body.Email))
	inviteCode := normalizeInviteCode(body.InviteCode)

	var details []gen.ErrorDetail
	if !validEmail(email) {
		details = append(details, detail("email", "email_invalid"))
	}
	if !validPassword(body.Password) {
		details = append(details, detail("password", "password_length_out_of_range"))
	}
	if !plausibleInviteCode(inviteCode) {
		details = append(details, detail("invite_code", "invite_code_invalid"))
	}
	if strings.TrimSpace(body.EmailCode) == "" {
		details = append(details, detail("email_code", "email_code_required"))
	}
	if len(details) > 0 {
		return gen.RegisterAccount422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求参数不合法", details...)}, nil
	}

	// ---- 1. 邀请码状态 ----
	invite, err := s.db.GetInviteCodeAnyState(ctx, inviteCode)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return gen.RegisterAccount500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询邀请码失败", err)}, nil
		}
		return gen.RegisterAccount422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
			"邀请码无效", detail("invite_code", "invite_code_invalid"))}, nil
	}
	switch classifyInviteCode(invite, now) {
	case gen.InviteVerifyResultStateExhausted:
		return gen.RegisterAccount422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
			"邀请码已被使用", detail("invite_code", "invite_code_exhausted"))}, nil
	case gen.InviteVerifyResultStateInvalid:
		return gen.RegisterAccount422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
			"邀请码无效", detail("invite_code", "invite_code_invalid"))}, nil
	}

	// ---- 2. 邮箱验证码 ----
	verification, verr := s.checkEmailCode(ctx, email, dbgen.VerificationPurposeRegister, body.EmailCode, now)
	if verr != nil {
		switch {
		case errors.Is(verr, errVerifyAttempts):
			return gen.RegisterAccount422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"验证码错误次数过多，请重新获取", detail("email_code", "email_code_attempts_exceeded"))}, nil
		case errors.Is(verr, errVerifyExpired):
			return gen.RegisterAccount422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"验证码已过期，请重新获取", detail("email_code", "email_code_expired"))}, nil
		case errors.Is(verr, errVerifyNotFound), errors.Is(verr, errVerifyMismatch):
			return gen.RegisterAccount422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"验证码不正确", detail("email_code", "email_code_invalid"))}, nil
		default:
			return gen.RegisterAccount500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "校验邮箱验证码失败", verr)}, nil
		}
	}

	// ---- 3. 邮箱是否已注册 ----
	// 这次查询给的是**友好的错误信息**，不是互斥。真正的互斥是 users_email_uk。
	if _, err := s.db.GetUserByEmail(ctx, email); err == nil {
		return gen.RegisterAccount409JSONResponse{ErrConflictJSONResponse: s.conflict(ctx, "该邮箱已注册")}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return gen.RegisterAccount500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询邮箱失败", err)}, nil
	}

	// ---- 4. 口令哈希（**在事务外**）----
	// argon2 要跑几十毫秒。放进事务里等于让每次注册都占着一条 db-f1-micro 的连接
	// 空转几十毫秒 —— 连接池上限是每实例 2 条（ADR 0005 倒推出来的），很贵。
	pwHash, err := hashPassword(ctx, body.Password)
	if err != nil {
		return gen.RegisterAccount500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "口令哈希失败", err)}, nil
	}

	// ---- 5. 建账号（单事务）----
	var (
		newUser dbgen.User
		newSess dbgen.UserSession
		rawTok  string
	)
	txErr := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		u, sess, raw, err := s.registerTx(ctx, q, registerInput{
			email:          email,
			passwordHash:   pwHash,
			invite:         invite,
			verificationID: verification.ID,
			meta:           meta,
			now:            now,
		})
		if err != nil {
			return err
		}
		newUser, newSess, rawTok = u, sess, raw
		return nil
	})
	if txErr != nil {
		switch {
		case errors.Is(txErr, errInviteRaced):
			return gen.RegisterAccount422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"邀请码已被使用", detail("invite_code", "invite_code_exhausted"))}, nil
		case errors.Is(txErr, errEmailTaken):
			return gen.RegisterAccount409JSONResponse{ErrConflictJSONResponse: s.conflict(ctx, "该邮箱已注册")}, nil
		default:
			return gen.RegisterAccount500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "注册事务失败", txErr)}, nil
		}
	}

	// 送达率探针的另一半：回填 redeemed_at。
	// 放在事务外且忽略错误 —— 探针数据丢一条是统计噪音，让它回滚掉一次成功的注册才是事故。
	if err := s.db.MarkEmailProbeRedeemed(ctx, dbgen.MarkEmailProbeRedeemedParams{
		Lower: email, Template: emailTemplateVerifyCode,
	}); err != nil {
		s.logger.WarnContext(ctx, "回填邮件探针 redeemed_at 失败", "err", err)
	}

	s.logger.InfoContext(ctx, "注册成功",
		"user_id", newUser.ID, "invite_code_id", invite.ID, "request_id", middleware.RequestIDFrom(ctx))

	// TODO(P2): 按 user-journey §3 还应在这里签发首个订阅 token
	// （「账号建立 → 邀请码核销 → 签发订阅 token」是同一步）。
	// 订阅 token 的哈希与 pepper 属于订阅面（handler/subscription.go +
	// db/queries/subscriptions.sql），不在本文件范围。没有它的后果是可控的：
	// 用户注册后要去面板点一次「生成订阅链接」，而不是拿不到订阅。

	return gen.RegisterAccount201JSONResponse{
		Data: sessionTokens(newSess, rawTok, now),
		Meta: s.meta(ctx),
	}, nil
}

type registerInput struct {
	email          string
	passwordHash   string
	invite         dbgen.InviteCode
	verificationID int64
	meta           RequestMetadata
	now            time.Time
}

// registerTx 是注册的事务体。
//
// 拆出来接 dbgen.Querier 而不是写在 InTx 的闭包里，是为了它能被单测直接调 ——
// 「邀请码在核销瞬间被抢走」这条分支没法用真库稳定复现，但它恰恰是
// 整个注册流程里最需要被验证的一条。
//
// 五张表必须同事务（data-model §4.1）：
//
//	invite_codes（核销）· users · user_traffic · invite_code_uses · email_verifications
//
// 其中 user_traffic 尤其不能漏：ListAvailableUsersByServer 是
// `users JOIN user_traffic`，缺这一行的用户**永远不会出现在节点的用户列表里**，
// 而他自己在面板上看一切正常。
func (s *Server) registerTx(ctx context.Context, q dbgen.Querier, in registerInput) (dbgen.User, dbgen.UserSession, string, error) {
	var (
		zeroUser dbgen.User
		zeroSess dbgen.UserSession
	)

	groupID, err := q.GetRegistrationGroupID(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return zeroUser, zeroSess, "", errNoDefaultGroup
		}
		return zeroUser, zeroSess, "", err
	}

	// 条件 UPDATE 是**唯一**的并发闸：used_count < max_uses 在同一条语句里判定并自增，
	// 两个并发请求只有一个能拿到行。先查再写会让一次性码带进来两个人。
	if _, err := q.RedeemInviteCode(ctx, in.invite.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return zeroUser, zeroSess, "", errInviteRaced
		}
		return zeroUser, zeroSess, "", err
	}

	user, err := q.CreateUser(ctx, dbgen.CreateUserParams{
		Email:        in.email,
		PasswordHash: in.passwordHash,
		GroupID:      groupID,
		// 种子码的 owner_user_id 为 NULL，此时 invited_by 也为 NULL ——
		// 返佣归属天然不存在，不需要额外分支。
		InvitedBy: in.invite.OwnerUserID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return zeroUser, zeroSess, "", errEmailTaken
		}
		return zeroUser, zeroSess, "", err
	}

	if _, err := q.CreateUserTraffic(ctx, user.ID); err != nil {
		return zeroUser, zeroSess, "", err
	}

	if _, err := q.RecordInviteCodeUse(ctx, dbgen.RecordInviteCodeUseParams{
		InviteCodeID: in.invite.ID,
		UserID:       user.ID,
		RequestIp:    in.meta.IP,
	}); err != nil {
		return zeroUser, zeroSess, "", err
	}

	// 验证码核销与建号同事务：分开做的话，一次事务失败会留下一个已核销的验证码，
	// 用户重试时被告知「验证码不正确」，而他手里的码明明是刚收到的。
	if err := q.ConsumeEmailVerification(ctx, in.verificationID); err != nil {
		return zeroUser, zeroSess, "", err
	}
	// 走到这里说明验证码已经验过了，邮箱是真实可达的。
	if err := q.MarkEmailVerified(ctx, user.ID); err != nil {
		return zeroUser, zeroSess, "", err
	}

	sess, raw, err := s.issueSession(ctx, q, user.ID, in.meta, in.now)
	if err != nil {
		return zeroUser, zeroSess, "", err
	}
	return user, sess, raw, nil
}

// checkEmailCode 校验一条邮箱验证码，但**不**核销（核销留给调用方的事务）。
//
// 失败时自增 attempts：没有这一步，6 位码就是一个可以无限重试的 10^6 空间，
// 而一次 HTTP 请求几毫秒 —— 十分钟有效期内足够跑完全部组合。
func (s *Server) checkEmailCode(ctx context.Context, email string, purpose dbgen.VerificationPurpose, code string, now time.Time) (dbgen.EmailVerification, error) {
	row, err := s.db.GetLatestEmailVerification(ctx, dbgen.GetLatestEmailVerificationParams{
		Lower: email, Purpose: purpose,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return row, errVerifyNotFound
		}
		return row, err
	}
	if !row.ExpiresAt.Valid || !row.ExpiresAt.Time.After(now) {
		return row, errVerifyExpired
	}
	if row.Attempts >= row.MaxAttempts {
		return row, errVerifyAttempts
	}

	want := hashEmailCode(s.cfg.SessionSigningKey, email, purpose, strings.TrimSpace(code))
	if subtle.ConstantTimeCompare(want, row.CodeHash) != 1 {
		// 自增失败不改变结论（码本来就不对），但要能在日志里看见 ——
		// 「attempts 一直是 0」是这条防线失效时唯一的现场特征。
		if _, err := s.db.IncrementVerificationAttempts(ctx, row.ID); err != nil {
			s.logger.WarnContext(ctx, "自增验证码尝试次数失败", "err", err, "verification_id", row.ID)
		}
		return row, errVerifyMismatch
	}
	return row, nil
}

// ============================================================
// 发码
// ============================================================

const (
	emailTemplateVerifyCode    = "verify_code"
	emailTemplatePasswordReset = "password_reset"

	// espUnwired 是「还没接 ESP」的占位。email_log.esp 是 NOT NULL，
	// 写一个显式的占位值比写 'ses' 好 —— 后者会让将来按 esp 分组的送达率统计
	// 把一批从未发出的邮件算进 SES 的分母。
	espUnwired = "unwired"

	// 发码限流（api-contract §10.1）。
	emailCodePerHour   = 3
	emailCodeMinGap    = 60 * time.Second
	forgotPerIPPerHour = 10
)

// SendEmailCode 发送邮箱验证码。
//
// 两条契约要点：
//  1. **无论邮箱是否已注册都返回 204**（防枚举）。所以「已注册 → 不发注册码」
//     这个分支是静默的。
//  2. 每次发码写一条 email_log —— api-contract §5.1 把它定为这个端点
//     **存在的第二个理由**：依 ADR 0002 邮件是唯一失联恢复通道，
//     收不到验证码的用户就是封锁当天必然失联的用户，按收件域名分组的送达率
//     只能从这里采。
func (s *Server) SendEmailCode(ctx context.Context, req gen.SendEmailCodeRequestObject) (gen.SendEmailCodeResponseObject, error) {
	if req.Body == nil {
		return gen.SendEmailCode422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空")}, nil
	}
	now := time.Now()
	meta := s.requestMetadata(ctx)
	email := normalizeEmail(string(req.Body.Email))

	// ---- 限流第一层：门口的 per IP（rate_limit 表）----
	//
	// **在任何校验之前**，与 Login 同一条理由：格式非法的请求同样是敲门声，
	// 让它免费重试等于给限流器留后门。
	//
	// 它补的是第二层补不到的两个洞：
	//   · per email 的 3/h 与 60 秒间隔只约束**同一个邮箱**，一个 IP 轮着换邮箱发码
	//     可以无限次消耗同一份 SES 配额，而退信率 ≥ 5% 就进审查；
	//   · 第二层数的是 email_verifications 的行，而本端点有若干条不写行就返回的路径
	//     （422、email_change 的 501、以及第二层自己拒掉的那些）——
	//     走这些路的请求对第二层完全隐形。
	if retry, limited := s.checkRateRules(ctx, rateRule{
		bucket:  bucketEmailCodeIPHour,
		subject: rateSubjectIP(meta),
		limit:   emailCodePerIPPerHour,
		window:  time.Hour,
	}); limited {
		return gen.SendEmailCode429JSONResponse{ErrRateLimitedJSONResponse: s.rateLimited(ctx,
			"获取验证码过于频繁，请稍后再试", retry)}, nil
	}

	if !validEmail(email) {
		return gen.SendEmailCode422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
			"邮箱格式不正确", detail("email", "email_invalid"))}, nil
	}
	purpose, ok := verificationPurposeForScene(req.Body.Scene)
	if !ok {
		return gen.SendEmailCode422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
			"不支持的验证码场景", detail("scene", "scene_invalid"))}, nil
	}
	if purpose == dbgen.VerificationPurposeEmailChange {
		// TODO(P2): 换绑邮箱需要「已登录用户 + 目标邮箱未被占用」两个前提，
		// 而本端点在契约里是 security: []（免登录）。要支持它得先裁定
		// 「本端点是否接受可选鉴权」，那是契约层面的决定，不在本轮范围。
		return nil, ErrNotImplemented
	}

	// ---- 限流第二层：per email（精确档，走 email_verifications 的历史行）----
	// 计数直接来自 email_verifications，不走 rate_limit 表，也天然跨实例一致。
	// 保留它而不是一并挪进 rate_limit，是因为**两层的失败模式互相独立**：
	// email_verifications 是普通表（写 WAL、进备份），rate_limit 是 UNLOGGED
	// （崩溃即 TRUNCATE）。发信配额是外部机构对我们的判罚，不该只由易失表守着。
	window, err := s.db.CountRecentEmailVerifications(ctx, dbgen.CountRecentEmailVerificationsParams{
		Lower: email, Purpose: purpose, CreatedAt: tstz(now.Add(-time.Hour)),
	})
	if err != nil {
		return gen.SendEmailCode500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询发码频率失败", err)}, nil
	}
	if window.SentInWindow >= emailCodePerHour {
		return gen.SendEmailCode429JSONResponse{ErrRateLimitedJSONResponse: s.rateLimited(ctx, "获取验证码过于频繁，请稍后再试", 3600)}, nil
	}
	if window.LastSentAt.Valid {
		if gap := now.Sub(window.LastSentAt.Time); gap < emailCodeMinGap {
			retry := int32((emailCodeMinGap - gap).Seconds()) + 1
			return gen.SendEmailCode429JSONResponse{ErrRateLimitedJSONResponse: s.rateLimited(ctx, "请稍后再获取验证码", retry)}, nil
		}
	}

	// ---- 目标用户 ----
	var userID *int64
	existing, err := s.db.GetUserByEmail(ctx, email)
	switch {
	case err == nil:
		userID = &existing.ID
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return gen.SendEmailCode500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询用户失败", err)}, nil
	}

	// 「注册码发给已注册邮箱」与「重置码发给未注册邮箱」都不真的发信。
	//
	// 🔴 但**仍然写 email_verifications 行**。这一条看着浪费，实际上是防枚举的关键：
	// 限流计数就来自这张表，跳过写入的话「已注册邮箱」永远撞不到 3/h 上限，
	// 于是发 4 次请求看第 4 次是 204 还是 429，就能判定一个邮箱是否注册过 ——
	// 端点辛辛苦苦对两种情况都返回 204，却被状态码之外的这条侧信道拆穿。
	// 两条路径写同样的行、走同样的限流，差别只在**发不发信**。
	deliver := !((purpose == dbgen.VerificationPurposeRegister && userID != nil) ||
		(purpose == dbgen.VerificationPurposePasswordReset && userID == nil))

	if err := s.issueVerification(ctx, email, purpose, userID, meta, now, deliver); err != nil {
		return gen.SendEmailCode500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "签发验证码失败", err)}, nil
	}

	return gen.SendEmailCode204Response{
		Headers: gen.SendEmailCode204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}, nil
}

// issueVerification 生成验证码 / 重置令牌，写 email_verifications，并在 deliver 时发信。
//
// deliver=false 表示「记账但不发信」（防枚举，见调用点的说明）：
// email_verifications **照写**（限流计数靠它），email_log **不写** ——
// 后者是送达率统计的分母，把从未发出的邮件算进去会让 ADR 0002 §7 要的那份
// 实测数据凭空多出一批永远不会 delivered 的行。
//
// 两条写入不放同一个事务：email_log 是观测数据，它写失败不该让用户拿不到验证码。
// 反过来 email_verifications 写失败则必须整体失败 —— 发了信却没有可校验的记录，
// 用户会拿着一个永远验不过的码。
func (s *Server) issueVerification(ctx context.Context, email string, purpose dbgen.VerificationPurpose, userID *int64, meta RequestMetadata, now time.Time, deliver bool) error {
	secret, hash, err := newVerificationSecret(s.cfg.SessionSigningKey, email, purpose)
	if err != nil {
		return err
	}

	ttl := emailCodeTTL
	template := emailTemplateVerifyCode
	subject := "【babel.plus】邮箱验证码"
	if purpose == dbgen.VerificationPurposePasswordReset {
		ttl = resetTokenTTL
		template = emailTemplatePasswordReset
		subject = "【babel.plus】重置密码"
	}

	if _, err := s.db.CreateEmailVerification(ctx, dbgen.CreateEmailVerificationParams{
		Email:     email,
		UserID:    userID,
		Purpose:   purpose,
		CodeHash:  hash,
		ExpiresAt: tstz(now.Add(ttl)),
		RequestIp: meta.IP,
	}); err != nil {
		return err
	}

	if !deliver {
		// 记账已完成，到此为止。日志里留一条，否则「用户说没收到验证码」这类工单
		// 会完全没有现场 —— 邮箱**不打全量**，只留域名。
		s.logger.InfoContext(ctx, "已记账但不发信（防枚举）",
			"purpose", string(purpose), "to_domain", emailDomain(email))
		return nil
	}

	// TODO(P1): 接 ESP 真正发信（ADR 0002 定的 AWS SES）。
	// 发信成功后要把 email_log 的 status 改成 'sent'、写 provider_msg_id 与 sent_at，
	// 并挂一个 bounce/complaint 回调把 bounce_code 与 delivered_at 写回来 ——
	// 那三列才是 ADR 0002 §7 要求的送达率数据的主体。
	// 当前 status 恒为 'queued'，任何按 status='delivered' 做的统计都会是 0，
	// 这是「还没接」而不是「送达率为 0」，别看着面板下结论。
	if _, err := s.db.CreateEmailLog(ctx, dbgen.CreateEmailLogParams{
		UserID:   userID,
		ToEmail:  email,
		ToDomain: emailDomain(email),
		Esp:      espUnwired,
		Template: template,
		Subject:  subject,
		Status:   "queued",
		SentAt:   pgtype.Timestamptz{}, // 还没真发，保持 NULL
	}); err != nil {
		s.logger.WarnContext(ctx, "写 email_log 失败（探针数据丢失，不影响发码）", "err", err)
	}

	// 🔴 只在 dev 打明文。staging/prod 打出来等于把验证码写进 Cloud Logging，
	// 而日志的读取权限比数据库宽得多。
	if s.cfg.Env == "dev" {
		s.logger.WarnContext(ctx, "【仅 dev】验证码明文（ESP 未接入）",
			"email", email, "purpose", string(purpose), "secret", secret)
	}
	return nil
}

// ============================================================
// 登录 / 刷新 / 登出
// ============================================================

// Login 登录。
//
// 三条不可动的规则：
//  1. 「用户不存在」与「密码错误」返回**同一个** code、同一句 message；
//  2. 两者跑**等量**的 argon2（burnPasswordVerification），否则用秒表就能枚举用户；
//  3. 封禁走 AUTH_PERMISSION_DENIED 而不是 AUTH_INVALID_CREDENTIALS ——
//     被封的用户看到「邮箱或密码不正确」会反复重试并开工单，
//     而 HTTP 状态仍是 401（契约没给本端点定义 403）。
//
// 限流（api-contract §10.1：per IP + per email 双维度，各 5/min 与 10/h），
// 计数走 0013 的 rate_limit 表，跨 Cloud Run 实例一致。原 TODO(P1) 到此为止。
//
// **必须跑在 argon2 之前**：argon2Slots 挡的是资源耗尽（并发打满内存），
// 它对慢速凭据填充完全无效，而每一次哈希的 CPU 都是我们自己付。
//
// ⚠️ 两处与契约不一致，都是明知的：
//
//  1. 契约还要求「指数退避 + 解锁倒计时」，**未实现**。当前是固定窗口，
//     Retry-After 给的是本窗口剩余时间，不是逐次翻倍的锁定时长。
//     要做退避得先裁定「锁定的是 IP 还是账号」—— 锁定账号可以被用来定向
//     拒绝某个用户登录（只要一直用错密码打他的邮箱），这条没裁决前不做。
//
//  2. 🔴 per IP 10/h 在 **CGNAT / 企业 NAT 后面是有真实误伤风险的**：
//     契约自己在订阅面那一行就写了「放宽是因为一个企业 NAT 后可能有多个用户」，
//     但登录这一行没放宽。这里按契约实施（不擅自放宽），
//     **撤回条件**：bp_api_429 上出现登录路径的持续 429，或者收到
//     「登录提示过于频繁」类工单 —— 命中任一条就把 per IP 的小时限额单列并放宽，
//     per email 的那两条不动（它才是真正保护账号的维度）。
func (s *Server) Login(ctx context.Context, req gen.LoginRequestObject) (gen.LoginResponseObject, error) {
	if req.Body == nil {
		return gen.Login422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空")}, nil
	}
	now := time.Now()
	meta := s.requestMetadata(ctx)
	email := normalizeEmail(string(req.Body.Email))

	// 限流放在**所有校验之前**：格式非法的请求同样是敲门声，
	// 让它免费重试等于给限流器留了一道后门（发一批空邮箱的请求就能把
	// per IP 的计数绕过去）。email 为空时 per email 两条会自动跳过（subject 为空），
	// per IP 两条照常计数。
	//
	// 代价是每次登录多 4 次 upsert。相对同一请求里 argon2id 的哈希耗时，
	// 这四次同区往返是低一个数量级的（具体毫秒数需实测），
	// 且它们在**验证密码之前**就完成 —— 被限流的请求根本不会走到哈希。
	if retry, limited := s.checkRateRules(ctx,
		rateRule{bucket: bucketLoginIPMinute, subject: rateSubjectIP(meta), limit: loginPerMinute, window: time.Minute},
		rateRule{bucket: bucketLoginIPHour, subject: rateSubjectIP(meta), limit: loginPerHour, window: time.Hour},
		rateRule{bucket: bucketLoginEmailMinute, subject: email, limit: loginPerMinute, window: time.Minute},
		rateRule{bucket: bucketLoginEmailHour, subject: email, limit: loginPerHour, window: time.Hour},
	); limited {
		return gen.Login429JSONResponse{ErrRateLimitedJSONResponse: s.rateLimited(ctx,
			"登录尝试过于频繁，请稍后再试", retry)}, nil
	}

	if email == "" || req.Body.Password == "" {
		return gen.Login422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "邮箱与密码不能为空")}, nil
	}

	invalid := gen.Login401JSONResponse{ErrUnauthorizedJSONResponse: s.unauthorized(ctx,
		gen.AUTHINVALIDCREDENTIALS, "邮箱或密码不正确")}

	user, err := s.db.GetUserByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return gen.Login500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询用户失败", err)}, nil
		}
		burnPasswordVerification(ctx, req.Body.Password)
		return invalid, nil
	}

	ok, verr := verifyPassword(ctx, user.PasswordHash, req.Body.Password)
	if verr != nil {
		// 存量哈希解析不了（例如被 AnonymizeUser 清成空串）。
		// 归到「凭据无效」而不是 500：这不是我们的故障，而告诉调用方
		// 「这个账号的哈希坏了」等于确认了账号存在。
		s.logger.WarnContext(ctx, "口令哈希无法解析", "user_id", user.ID, "err", verr)
		return invalid, nil
	}
	if !ok {
		return invalid, nil
	}

	if user.Banned {
		return gen.Login401JSONResponse{ErrUnauthorizedJSONResponse: s.unauthorized(ctx,
			gen.AUTHPERMISSIONDENIED, "账号已被封禁")}, nil
	}

	sess, raw, err := s.issueSession(ctx, s.db.Queries, user.ID, meta, now)
	if err != nil {
		return gen.Login500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "签发会话失败", err)}, nil
	}

	// 尽力而为：登录已经成功，写不进 last_login_at 不该让用户重登。
	// （TouchUserLogin 只改 last_login_at / last_login_ip / updated_at，
	//  不在 0012 触发器的监听列里，所以**不会** bump user_rev、不会让节点重拉用户表。）
	if err := s.db.TouchUserLogin(ctx, dbgen.TouchUserLoginParams{ID: user.ID, LastLoginIp: meta.IP}); err != nil {
		s.logger.WarnContext(ctx, "更新 last_login_at 失败", "err", err, "user_id", user.ID)
	}

	return gen.Login200JSONResponse{Data: sessionTokens(sess, raw, now), Meta: s.meta(ctx)}, nil
}

// plausibleSessionTokenShape 是签发侧对自己格式的一次廉价复核。
//
// 与 middleware 里那份**刻意各写各的**：那边校验的是「进来的东西像不像 token」，
// 这边是「别拿一个明显不是 token 的串去查库」。合并成一个函数会让
// 收紧其中一边的形态检查意外收紧另一边。
func plausibleSessionTokenShape(t string) bool {
	if len(t) < 24 || len(t) > 128 {
		return false
	}
	for _, c := range t {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// RefreshToken 轮换会话。
//
// 旧 token 在同一个事务里被撤销并指向新行（replaced_by_id 构成轮换链）。
// 复用一枚已经轮换过的 token → GetUserSessionByHash 的 revoked_at IS NULL
// 过滤让它查不到 → 401，与「token 根本不存在」不可区分。
//
// TODO(P2): 真正的 token 复用检测（拿到一枚已撤销的 refresh 说明它被偷了，
// 应当把整条轮换链一起撤掉）需要一条「按 hash 查**含已撤销**行」的查询。
// 现在做不到，因为唯一的查询在 SQL 层就把已撤销的滤掉了 ——
// 而放宽那个过滤会让中间件的鉴权路径也跟着放宽，得先拆成两条查询。
func (s *Server) RefreshToken(ctx context.Context, req gen.RefreshTokenRequestObject) (gen.RefreshTokenResponseObject, error) {
	if req.Body == nil {
		return gen.RefreshToken401JSONResponse{ErrUnauthorizedJSONResponse: s.unauthorized(ctx, gen.AUTHTOKENINVALID, "会话无效或已过期")}, nil
	}
	now := time.Now()
	meta := s.requestMetadata(ctx)
	raw := strings.TrimSpace(req.Body.RefreshToken)

	invalid := gen.RefreshToken401JSONResponse{ErrUnauthorizedJSONResponse: s.unauthorized(ctx,
		gen.AUTHTOKENINVALID, "会话无效或已过期")}

	// 形态不合法直接拒，不查库 —— 与 middleware 同一条纪律。
	if !plausibleSessionTokenShape(raw) {
		return invalid, nil
	}

	old, err := s.db.GetUserSessionByHash(ctx, middleware.HashSessionToken(s.cfg.SessionSigningKey, raw))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return invalid, nil
		}
		return gen.RefreshToken500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询会话失败", err)}, nil
	}
	if old.UserDeletedAt.Valid {
		return invalid, nil
	}
	if old.Banned {
		return gen.RefreshToken401JSONResponse{ErrUnauthorizedJSONResponse: s.unauthorized(ctx,
			gen.AUTHPERMISSIONDENIED, "账号已被封禁")}, nil
	}

	var (
		newSess dbgen.UserSession
		newRaw  string
	)
	txErr := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		sess, token, err := s.issueSession(ctx, q, old.UserID, meta, now)
		if err != nil {
			return err
		}
		// 先建新的再撤旧的：顺序反过来的话，中间任何一步失败都会让用户
		// 既没有新会话、旧会话也已作废 —— 一次刷新把人踢下线。
		if err := q.RotateUserSession(ctx, dbgen.RotateUserSessionParams{
			ID: old.ID, ReplacedByID: &sess.ID,
		}); err != nil {
			return err
		}
		newSess, newRaw = sess, token
		return nil
	})
	if txErr != nil {
		return gen.RefreshToken500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "轮换会话失败", txErr)}, nil
	}

	return gen.RefreshToken200JSONResponse{Data: sessionTokens(newSess, newRaw, now), Meta: s.meta(ctx)}, nil
}

// Logout 撤销当前会话。
//
// 只撤这一条，不撤全部：「在这台设备上退出」不该把用户手机上的登录也踢掉。
// 「全部登出」是另一个动作（RevokeAllUserSessions），契约里没给它端点。
func (s *Server) Logout(ctx context.Context, _ gen.LogoutRequestObject) (gen.LogoutResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	if err := s.db.RevokeUserSession(ctx, auth.SessionID); err != nil {
		return gen.Logout500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "撤销会话失败", err)}, nil
	}
	return gen.Logout204Response{
		Headers: gen.Logout204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}, nil
}

// ============================================================
// 忘记密码 / 重置密码 / 修改密码
// ============================================================

// ForgotPassword 发送重置令牌。
//
// **无论邮箱是否存在都返回 204**（防枚举），per email 超限也返回 204 ——
// 只有 per IP 超限才 429。这个不对称是契约明写的（api-contract §10.1）：
// per email 的 429 会把「这个邮箱最近请求过重置」泄漏出去。
//
// per IP 限额单列的理由：本端点**消耗邮件配额**，而 AWS SES 退信率 ≥ 5%
// 进入审查、≥ 10% 可能暂停发信 —— 一次针对不存在邮箱的爆破就能把退信率打上去，
// 而邮件是 ADR 0002 裁定的唯一失联恢复通道。
func (s *Server) ForgotPassword(ctx context.Context, req gen.ForgotPasswordRequestObject) (gen.ForgotPasswordResponseObject, error) {
	if req.Body == nil {
		return gen.ForgotPassword422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空")}, nil
	}
	now := time.Now()
	meta := s.requestMetadata(ctx)
	email := normalizeEmail(string(req.Body.Email))

	// per IP 10/h 的第一层：门口计数（rate_limit 表），**在任何校验之前**。
	//
	// 它与第二层不是重复。第二层数的是 email_verifications 的行，
	// 而本端点有**三条**不写行的返回路径：邮箱格式非法（422）、
	// per email 超限（静默 204）、以及 issueVerification 之前的任何失败。
	// 走这三条路的请求对第二层完全隐形 —— 也就是说，反复拿同一个邮箱轰炸
	// （最典型的攻击形态）在触发 per email 上限之后就再也不计入 per IP 了。
	// 门口这一层数的是每一次请求，没有这个盲区。
	if retry, limited := s.checkRateRules(ctx, rateRule{
		bucket:  bucketForgotIPHour,
		subject: rateSubjectIP(meta),
		limit:   forgotPerIPPerHour,
		window:  time.Hour,
	}); limited {
		return gen.ForgotPassword429JSONResponse{ErrRateLimitedJSONResponse: s.rateLimited(ctx,
			"操作过于频繁，请稍后再试", retry)}, nil
	}

	if !validEmail(email) {
		return gen.ForgotPassword422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
			"邮箱格式不正确", detail("email", "email_invalid"))}, nil
	}

	accepted := gen.ForgotPassword204Response{
		Headers: gen.ForgotPassword204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}

	// per IP 10/h 的第二层：只数**真的产生了验证码**的请求。
	// 保留它的理由与 SendEmailCode 相同 —— email_verifications 是普通表，
	// 不随 UNLOGGED 的 rate_limit 一起在崩溃时清零，而它守的是 SES 配额。
	// 只在采集到 IP 时生效 —— 没挂 CaptureRequestMetadata 时
	// meta.IP 为 nil，此处静默跳过（宁可漏限流也不要把所有人算成同一个 IP）。
	if meta.IP != nil {
		n, err := s.db.CountRecentEmailVerificationsByIP(ctx, dbgen.CountRecentEmailVerificationsByIPParams{
			RequestIp: meta.IP,
			Purpose:   dbgen.VerificationPurposePasswordReset,
			CreatedAt: tstz(now.Add(-time.Hour)),
		})
		if err != nil {
			return gen.ForgotPassword500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询找回密码频率失败", err)}, nil
		}
		if n >= forgotPerIPPerHour {
			return gen.ForgotPassword429JSONResponse{ErrRateLimitedJSONResponse: s.rateLimited(ctx, "操作过于频繁，请稍后再试", 3600)}, nil
		}
	}

	// per email 3/h：超限**仍返回 204**。
	window, err := s.db.CountRecentEmailVerifications(ctx, dbgen.CountRecentEmailVerificationsParams{
		Lower:     email,
		Purpose:   dbgen.VerificationPurposePasswordReset,
		CreatedAt: tstz(now.Add(-time.Hour)),
	})
	if err != nil {
		return gen.ForgotPassword500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询找回密码频率失败", err)}, nil
	}
	if window.SentInWindow >= emailCodePerHour {
		s.logger.InfoContext(ctx, "找回密码超出 per email 限额（仍返回 204）", "to_domain", emailDomain(email))
		return accepted, nil
	}

	var userID *int64
	user, err := s.db.GetUserByEmail(ctx, email)
	switch {
	case err == nil:
		userID = &user.ID
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return gen.ForgotPassword500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询用户失败", err)}, nil
	}

	// 邮箱不存在也**照样记账**（deliver=false）。与 SendEmailCode 同一个理由，
	// 外加一条本端点独有的：契约给 forgot 单列 per email 3/h 的理由是
	// 「一次针对不存在邮箱的爆破就能把 SES 退信率打上去」（api-contract §10.1）——
	// 而不存在的邮箱如果不留记录，这条限额恰恰对它设计要防的那个场景完全无效。
	if err := s.issueVerification(ctx, email, dbgen.VerificationPurposePasswordReset, userID, meta, now, userID != nil); err != nil {
		return gen.ForgotPassword500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "签发重置令牌失败", err)}, nil
	}
	return accepted, nil
}

// ResetPassword 用重置令牌设置新密码，并撤销全部会话。
//
// 撤销全部会话是必须的：找回密码的典型场景就是「账号可能已被别人拿到」，
// 只改密码不撤会话等于让攻击者的登录态继续有效 30 天。
func (s *Server) ResetPassword(ctx context.Context, req gen.ResetPasswordRequestObject) (gen.ResetPasswordResponseObject, error) {
	if req.Body == nil {
		return gen.ResetPassword422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空")}, nil
	}
	now := time.Now()
	token := strings.TrimSpace(req.Body.Token)

	if !validPassword(req.Body.Password) {
		return gen.ResetPassword422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
			"密码长度必须在 8–128 之间", detail("password", "password_length_out_of_range"))}, nil
	}

	invalid := gen.ResetPassword401JSONResponse{ErrUnauthorizedJSONResponse: s.unauthorized(ctx,
		gen.AUTHTOKENINVALID, "重置链接无效或已过期")}

	if !plausibleSessionTokenShape(token) {
		return invalid, nil
	}

	row, err := s.db.GetEmailVerificationByCodeHash(ctx, dbgen.GetEmailVerificationByCodeHashParams{
		CodeHash: hashResetToken(s.cfg.SessionSigningKey, token),
		Purpose:  dbgen.VerificationPurposePasswordReset,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 不存在 / 已用过 / 已过期在 SQL 层已经不可区分，这是好事。
			return invalid, nil
		}
		return gen.ResetPassword500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询重置令牌失败", err)}, nil
	}
	if row.UserID == nil {
		// purpose=password_reset 的记录必然带 user_id（issueVerification 保证）。
		// 走到这里说明有别的路径写了这张表，属于数据异常，不是用户错误。
		s.logger.ErrorContext(ctx, "重置令牌缺少 user_id", "verification_id", row.ID)
		return invalid, nil
	}
	if !row.ExpiresAt.Valid || !row.ExpiresAt.Time.After(now) {
		return invalid, nil
	}

	pwHash, err := hashPassword(ctx, req.Body.Password)
	if err != nil {
		return gen.ResetPassword500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "口令哈希失败", err)}, nil
	}

	userID := *row.UserID
	txErr := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := q.UpdateUserPassword(ctx, dbgen.UpdateUserPasswordParams{
			ID: userID, PasswordHash: pwHash, PasswordAlgo: passwordAlgo,
		}); err != nil {
			return err
		}
		// 核销与改密同事务：分开做的话一次失败会留下一枚仍然有效的重置令牌。
		if err := q.ConsumeEmailVerification(ctx, row.ID); err != nil {
			return err
		}
		if _, err := q.RevokeAllUserSessions(ctx, userID); err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		return gen.ResetPassword500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "重置密码失败", txErr)}, nil
	}

	s.logger.InfoContext(ctx, "密码已重置，全部会话已撤销", "user_id", userID)
	return gen.ResetPassword204Response{
		Headers: gen.ResetPassword204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}, nil
}

// ChangePassword 修改密码（需旧密码），并撤销**其余**会话。
//
// 保留当前会话是刻意的：把自己也踢掉会让「改完密码立刻被登出」，
// 用户以为改失败了。别的设备必须踢 —— 改密码的另一个典型动机就是
// 「我怀疑别人登着我的号」。
func (s *Server) ChangePassword(ctx context.Context, req gen.ChangePasswordRequestObject) (gen.ChangePasswordResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	if req.Body == nil {
		return gen.ChangePassword422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空")}, nil
	}
	if !validPassword(req.Body.NewPassword) {
		return gen.ChangePassword422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx,
			"密码长度必须在 8–128 之间", detail("new_password", "password_length_out_of_range"))}, nil
	}

	user, err := s.db.GetUserByID(ctx, auth.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 会话有效但用户查不到 = 账号在会话有效期内被注销了。
			return gen.ChangePassword401JSONResponse{ErrUnauthorizedJSONResponse: s.unauthorized(ctx,
				gen.AUTHTOKENINVALID, "会话无效或已过期")}, nil
		}
		return gen.ChangePassword500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询用户失败", err)}, nil
	}

	okPw, verr := verifyPassword(ctx, user.PasswordHash, req.Body.OldPassword)
	if verr != nil {
		s.logger.WarnContext(ctx, "口令哈希无法解析", "user_id", user.ID, "err", verr)
		okPw = false
	}
	if !okPw {
		return gen.ChangePassword401JSONResponse{ErrUnauthorizedJSONResponse: s.unauthorized(ctx,
			gen.AUTHINVALIDCREDENTIALS, "原密码不正确")}, nil
	}

	pwHash, err := hashPassword(ctx, req.Body.NewPassword)
	if err != nil {
		return gen.ChangePassword500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "口令哈希失败", err)}, nil
	}

	txErr := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := q.UpdateUserPassword(ctx, dbgen.UpdateUserPasswordParams{
			ID: user.ID, PasswordHash: pwHash, PasswordAlgo: passwordAlgo,
		}); err != nil {
			return err
		}
		_, err := q.RevokeOtherUserSessions(ctx, dbgen.RevokeOtherUserSessionsParams{
			UserID: user.ID, ID: auth.SessionID,
		})
		return err
	})
	if txErr != nil {
		return gen.ChangePassword500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "修改密码失败", txErr)}, nil
	}

	return gen.ChangePassword204Response{
		Headers: gen.ChangePassword204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}, nil
}

// ============================================================
// 当前用户
// ============================================================

// GetCurrentUser 当前用户信息 + 订阅摘要。
//
// 余额与佣金取不到时**不让整个响应失败**：面板首屏靠这个端点渲染，
// 一个钱包缓存表的缺行不该表现为「打不开面板」。
func (s *Server) GetCurrentUser(ctx context.Context, _ gen.GetCurrentUserRequestObject) (gen.GetCurrentUserResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	row, err := s.db.GetUserWithTraffic(ctx, auth.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.GetCurrentUser401JSONResponse{ErrUnauthorizedJSONResponse: s.unauthorized(ctx,
				gen.AUTHTOKENINVALID, "会话无效或已过期")}, nil
		}
		return gen.GetCurrentUser500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "查询用户失败", err)}, nil
	}

	out := gen.CurrentUser{
		Id:        row.ID,
		Email:     openapi_types.Email(row.Email),
		Banned:    row.Banned,
		CreatedAt: row.CreatedAt.Time,
	}

	// 余额缓存表可能还没有这个用户的行（从未有过资金往来），此时余额就是 0。
	if w, err := s.db.GetWalletBalance(ctx, auth.UserID); err == nil {
		out.BalanceAmount = w.Balance
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.logger.WarnContext(ctx, "查询钱包余额失败", "err", err, "user_id", auth.UserID)
	}

	if sum, err := s.db.SumConfirmedCommissions(ctx, auth.UserID); err == nil {
		out.CommissionBalanceAmount = &sum
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.logger.WarnContext(ctx, "查询佣金余额失败", "err", err, "user_id", auth.UserID)
	}

	// TODO(P2): totp_enabled。users 表没有任何 TOTP 列（用户侧 2FA 在
	// api-contract §5.1 标为 P3），字段是可选的，这里留空而不是硬报 false ——
	// 报 false 会让前端渲染出一个「未开启，点此开启」的入口，而后端还没有这个能力。

	summary := gen.SubscriptionSummary{
		TotalBytes:    row.TransferEnable,
		UploadBytes:   row.U,
		DownloadBytes: row.D,
	}
	if row.ExpiredAt.Valid {
		t := row.ExpiredAt.Time
		summary.ExpiredAt = &t
	}
	if row.ResetAt.Valid {
		t := row.ResetAt.Time
		summary.ResetAt = &t
	}
	// device_limit 在契约里是必填 int32，而 DB 里 NULL = 不限。
	// 用 0 表达「不限」：前端渲染时 0 与任何正整数天然可区分，
	// 而给一个假的上限（比如 999）会在用户界面上变成一条不存在的规则。
	if row.DeviceLimit != nil {
		summary.DeviceLimit = *row.DeviceLimit
	}
	if devices, err := s.db.ListUserDevices(ctx, auth.UserID); err == nil {
		summary.DeviceCount = int32(len(devices))
	} else {
		s.logger.WarnContext(ctx, "查询在线设备失败", "err", err, "user_id", auth.UserID)
	}
	if row.PlanID != nil {
		if p, err := s.db.GetPlan(ctx, *row.PlanID); err == nil {
			name := p.Name
			summary.PlanName = &name
		} else if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.WarnContext(ctx, "查询套餐失败", "err", err, "plan_id", *row.PlanID)
		}
	}
	out.Subscription = &summary

	return gen.GetCurrentUser200JSONResponse{Data: out, Meta: s.meta(ctx)}, nil
}
