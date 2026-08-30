// Package config 负责从环境变量装载配置，并在缺少任何必需项时**拒绝启动**。
//
// 设计原则：fail-closed。宁可起不来，也不要带着半截配置跑起来 ——
// 一个缺了 SUBSCRIPTION_TOKEN_PEPPER 的实例会静默签发无效订阅 token，
// 这种故障比启动失败难查一个数量级。
//
// 参考 Proxy_Skill 的 gen-clash.py：它用 REQUIRED 列表在渲染前一次性校验，
// 缺任何一个直接 sys.exit，不生成半成品。这里是同一条纪律的 Go 版本。
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是进程的全部配置。字段按来源分组，不做嵌套 —— 扁平结构更容易在启动日志里打印核对。
type Config struct {
	// ---- 运行时 ----
	Env  string // dev | staging | prod
	Port string // Cloud Run 通过 PORT 注入，本地默认 8080

	// ---- 数据库 ----
	// ADR 0005：Cloud Run 走内建 Cloud SQL 连接器的 **Unix socket**，不建 VPC connector。
	// 形如 /cloudsql/oratis-491316:us-central1:bp-pg
	DatabaseURL string

	// DBMaxConns 是**每实例**连接池上限。
	//
	// 🔴 硬约束，不要随手调大：ADR 0005 选的 db-f1-micro 可用连接数很少，
	// 而 Cloud Run 会横向扩容。max-instances=8 × 每实例 2 连接 = 峰值 16 连接，
	// 这是按实例规格倒推出来的，不是拍脑袋。
	// 改这个值之前必须同步改 deploy-api.sh 的 --max-instances，两者是一对。
	DBMaxConns int32

	// ---- 密钥 ----
	// SubscriptionTokenPepper 参与订阅 token 的哈希，泄漏等于所有订阅可被离线爆破。
	SubscriptionTokenPepper string
	// NodeKeyPepper 参与节点密钥哈希。与订阅分开，避免一处泄漏波及两个面。
	NodeKeyPepper string
	// SessionSigningKey 用户会话签名密钥。
	SessionSigningKey string

	// ---- 外部依赖 ----
	GCPProjectID string

	// AllowedOrigins 是用户面 / 管理面的 CORS Origin 白名单（**精确匹配**）。
	//
	// 为什么走配置而不是代码常量：生产形态是「API 与 Web 各用独立主域名池」，
	// 而域名池会被封、会轮换（ADR 0003 §5「一键新增镜像域名」）。
	// 硬编码意味着换域名要发一次版 —— 那是封锁当天最不该有的依赖。
	//
	// dev 有默认值（本地两个 vite 端口），staging / prod **必须显式配置**，
	// 缺失则拒绝启动：一个 CORS 白名单为空的生产实例表现为「前端全线报跨域错」，
	// 比启动失败难查得多，而且是在用户面前失败。
	AllowedOrigins []string

	// ---- 可选项（有默认值，不影响启动）----
	LogLevel        string
	ShutdownTimeout time.Duration
	// TrustProxyHeaders 决定是否信任 X-Forwarded-For。
	// Cloud Run 前面有 Google 的前端，可以信任；裸跑时**必须关**，否则来源 IP 可被伪造，
	// 而来源 IP 会写进 subscription_fetch_log 用于识别账号共享。
	TrustProxyHeaders bool

	// ---- 内部面：/internal/tasks/* 的 Google OIDC（api-contract.md §7）----
	//
	// 两项都**没有默认值**，也都不在 required 表里 —— 缺失时进程照常启动，
	// 但 middleware.AuthenticateInternal 会把整条内部面全部拒掉（fail-closed 在运行时那一侧）。
	// 之所以不做成「缺失即拒绝启动」：本地开发与 CI 不必为了跑起来先配两个用不上的变量。
	//
	// ⚠️ 内部面**已经接线**（authmap.go 的 internalTaskOperations 分支现在调
	// AuthenticateInternal），所以在 prod/staging 上这两项缺失的后果不再是「501」，
	// 而是「六条 Cloud Scheduler 与一条 Cloud Tasks 队列全部 403」。
	// TODO(P2): 把这两项加进 prod/staging 的必需集。**必须与 infra/deploy/deploy-api.sh
	//   同一个 PR**：先在这里设为必需而部署脚本还没传值，会让下一次 prod 部署起不来。

	// InternalOIDCAudience 是内部面 ID token 的 `aud` 必须逐字节等于的值。
	//
	// 🔴 取值是 **Cloud Run 服务的默认 URL**（`https://bp-api-xxxxxxxx.a.run.app`），
	// 不是镜像域名，也不是 API 主域名 —— roadmap.md §4.2 把这一条单独写出来是因为踩过：
	// 创建 Scheduler / Tasks 时填的 `--oidc-token-audience` 与这里配的必须完全相同，
	// 而镜像域名会被封、会轮换（ADR 0003 §5「一键新增镜像域名」）。
	// 用镜像域名当 aud 的后果是**每换一次域名就要重建六条 Scheduler 与一条队列**，
	// 而漏掉哪一条只表现为「那个任务安静地不再运行」。
	// 环境变量：BP_INTERNAL_OIDC_AUDIENCE
	InternalOIDCAudience string

	// InternalTaskCallers 是允许调用 /internal/tasks/* 的服务账号 email 白名单（已小写化、已去重）。
	// 环境变量：BP_INTERNAL_TASK_CALLERS（逗号分隔）
	InternalTaskCallers []string

	// ---- 管理面：IAP 断言 + TOTP step-up（api-contract.md §6）----
	//
	// 与内部面同一条纪律：两项都没有默认值，也都不在 required 表里 ——
	// 缺失时进程照常启动，但 middleware.AuthenticateAdmin 会把整个管理面拒掉
	// （fail-closed 落在运行时那一侧）。本地开发与 CI 不必为了跑起来先配两个用不上的变量。
	//
	// 🔴 「缺失 = 整体拒绝」这件事本身是承重的：管理面前面站着 IAP，
	// 而 `x-goog-iap-jwt-assertion` 在没有 IAP 的部署形态下是**任何人都能设的普通请求头**
	// （bp-api 目前 --ingress=all 直接暴露在 *.run.app 上）。
	// 如果哪天有人把「没配 audience」实现成「跳过校验」，现象就是「谁都能进管理面」，
	// 而且完全静默。所以这里只负责校验形状，**不负责给默认值**。

	// AdminIAPAudience 是 IAP 断言的 `aud` 必须逐字节等于的值，形如
	// `/projects/<PROJECT_NUMBER>/global/backendServices/<BACKEND_SERVICE_ID>`。
	//
	// 取值来自负载均衡器后端服务，**不是** URL、不是项目 ID：
	//	gcloud compute backend-services describe <NAME> --global --format='value(id)'
	// 环境变量：BP_ADMIN_IAP_AUDIENCE
	AdminIAPAudience string

	// AdminTOTPEncKey 是解密 admin_users.totp_secret_enc 的 AES-256 密钥（**32 字节**），
	// 来自 Secret Manager。环境变量里是 base64 形态，这里存解码后的原始字节。
	//
	// 缺失时 §6.2 L3 的危险操作（D3 D5 D6 D10 D15 D16）一律被拒 ——
	// 「危险操作做不了」，不是「危险操作不需要 TOTP」。
	// 环境变量：BP_ADMIN_TOTP_ENC_KEY
	AdminTOTPEncKey []byte
}

// required 列出所有必需的环境变量及其写入位置。
// 新增必需配置时**只改这张表**，Load 的逻辑不用动。
var required = []struct {
	key    string
	assign func(*Config, string)
	desc   string
}{
	{"BP_ENV", func(c *Config, v string) { c.Env = v }, "运行环境：dev | staging | prod"},
	{"BP_DATABASE_URL", func(c *Config, v string) { c.DatabaseURL = v }, "Postgres 连接串；Cloud Run 上用 Unix socket 形式"},
	{"BP_SUBSCRIPTION_TOKEN_PEPPER", func(c *Config, v string) { c.SubscriptionTokenPepper = v }, "订阅 token 哈希的 pepper"},
	{"BP_NODE_KEY_PEPPER", func(c *Config, v string) { c.NodeKeyPepper = v }, "节点密钥哈希的 pepper"},
	{"BP_SESSION_SIGNING_KEY", func(c *Config, v string) { c.SessionSigningKey = v }, "用户会话签名密钥"},
	{"BP_GCP_PROJECT_ID", func(c *Config, v string) { c.GCPProjectID = v }, "GCP 项目 ID，应为 oratis-491316"},
}

// Load 读取环境变量并校验。
//
// 返回的 error 会**一次性列出所有缺失项**，而不是遇到第一个就返回 ——
// 否则配置一个环境要来回启动六次。
func Load() (*Config, error) {
	c := &Config{
		Port:            envOr("PORT", "8080"),
		LogLevel:        envOr("BP_LOG_LEVEL", "info"),
		ShutdownTimeout: 10 * time.Second,
	}

	var missing []string
	for _, r := range required {
		v := strings.TrimSpace(os.Getenv(r.key))
		if v == "" {
			missing = append(missing, fmt.Sprintf("  %-32s %s", r.key, r.desc))
			continue
		}
		r.assign(c, v)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("缺少 %d 个必需的环境变量：\n%s\n\n参考 api/.env.example",
			len(missing), strings.Join(missing, "\n"))
	}

	// DBMaxConns 有默认值，但允许覆盖 —— 换数据库规格时需要跟着改。
	n, err := strconv.Atoi(envOr("BP_DB_MAX_CONNS", "2"))
	if err != nil || n < 1 {
		return nil, fmt.Errorf("BP_DB_MAX_CONNS 必须是正整数，当前值 %q", os.Getenv("BP_DB_MAX_CONNS"))
	}
	c.DBMaxConns = int32(n)

	switch c.Env {
	case "dev", "staging", "prod":
	default:
		return nil, fmt.Errorf("BP_ENV 必须是 dev / staging / prod 之一，当前值 %q", c.Env)
	}

	// CORS 白名单。dev 有默认值，staging / prod 必须显式配置。
	// 放在 Env 校验之后，因为「缺失是否致命」取决于环境。
	origins, err := parseAllowedOrigins(os.Getenv("BP_ALLOWED_ORIGINS"), c.Env)
	if err != nil {
		return nil, err
	}
	c.AllowedOrigins = origins

	// 生产环境跑在 Cloud Run 后面，信任 XFF；其余默认不信任。
	c.TrustProxyHeaders = envOr("BP_TRUST_PROXY_HEADERS", boolStr(c.Env != "dev")) == "true"

	// 内部面（可缺省，但**给了就必须是对的形状**）。
	// 与 AllowedOrigins 同一条理由：一个写错的值永远匹配不上真实 token 的 aud，
	// 现象是「六条定时任务全部 403」，与「没配」一模一样，却要往完全不同的方向查。
	aud, err := parseInternalAudience(os.Getenv("BP_INTERNAL_OIDC_AUDIENCE"), c.Env)
	if err != nil {
		return nil, err
	}
	c.InternalOIDCAudience = aud

	callers, err := parseInternalCallers(os.Getenv("BP_INTERNAL_TASK_CALLERS"), c.Env)
	if err != nil {
		return nil, err
	}
	c.InternalTaskCallers = callers

	// 两项要么都不配（内部面整体关闭），要么都配。只配一项是**纯粹的配置错误**：
	// AuthenticateInternal 在任一项为空时拒掉全部内部面，所以现象与「都没配」一样，
	// 但配置者会以为自己已经打开了内部面 —— 于是六条 Scheduler 全部 403，
	// 而他会去查 Scheduler、查 IAM、查 OIDC，唯独不会回来看这里。
	if (c.InternalOIDCAudience == "") != (len(c.InternalTaskCallers) == 0) {
		return nil, errors.New(
			"BP_INTERNAL_OIDC_AUDIENCE 与 BP_INTERNAL_TASK_CALLERS 必须同时配置或同时留空：\n" +
				"  只配一项时内部面仍然整体拒绝（与没配无异），但排查方向会被带偏")
	}

	// 管理面（同样可缺省，但**给了就必须是对的形状**）。
	iapAud, err := parseAdminIAPAudience(os.Getenv("BP_ADMIN_IAP_AUDIENCE"))
	if err != nil {
		return nil, err
	}
	c.AdminIAPAudience = iapAud

	totpKey, err := parseAdminTOTPEncKey(os.Getenv("BP_ADMIN_TOTP_ENC_KEY"))
	if err != nil {
		return nil, err
	}
	c.AdminTOTPEncKey = totpKey

	// 与内部面那一对同一条理由：只配一项是纯粹的配置错误，而两种漏配的现象
	// 都不指向配置本身 ——
	//   · 只配 audience：管理面能进，但每一个危险操作都回 AUTH_TOTP_REQUIRED，
	//     运维会去查 TOTP 绑定、查手机时间，唯独不会回来看这里；
	//   · 只配 TOTP 密钥：管理面**整体**进不去，看起来像 IAP 配错了。
	if (c.AdminIAPAudience == "") != (len(c.AdminTOTPEncKey) == 0) {
		return nil, errors.New(
			"BP_ADMIN_IAP_AUDIENCE 与 BP_ADMIN_TOTP_ENC_KEY 必须同时配置或同时留空：\n" +
				"  只配 audience → 管理面能进但所有危险操作被拒；只配密钥 → 管理面整体进不去。\n" +
				"  两种现象都不指向配置本身")
	}

	// 一条低成本的防呆：项目 ID 写错会把资源建到别的项目里。
	if c.Env == "prod" && c.GCPProjectID != "oratis-491316" {
		return nil, fmt.Errorf("prod 环境的 BP_GCP_PROJECT_ID 应为 oratis-491316，当前值 %q", c.GCPProjectID)
	}

	return c, nil
}

// Redacted 返回可安全写进启动日志的配置快照 —— 所有密钥只显示长度。
func (c *Config) Redacted() map[string]any {
	return map[string]any{
		"env":                 c.Env,
		"port":                c.Port,
		"db_max_conns":        c.DBMaxConns,
		"gcp_project_id":      c.GCPProjectID,
		"log_level":           c.LogLevel,
		"trust_proxy_headers": c.TrustProxyHeaders,
		"database_url":        redactDSN(c.DatabaseURL),
		// 白名单原样打印，不脱敏 —— 它不是秘密，而且「线上到底放行了哪些 Origin」
		// 是排查跨域故障时第一个要看的东西。
		"allowed_origins": c.AllowedOrigins,
		// 两条鉴权面的开关状态：它们不是秘密，而「这个实例的管理面/内部面到底开没开」
		// 是排查「所有人都被拒」时第一个要看的东西。
		"admin_iap_audience":     c.AdminIAPAudience,
		"internal_oidc_audience": c.InternalOIDCAudience,
		"internal_task_callers":  c.InternalTaskCallers,
		"secrets_len": map[string]int{
			"subscription_token_pepper": len(c.SubscriptionTokenPepper),
			"node_key_pepper":           len(c.NodeKeyPepper),
			"session_signing_key":       len(c.SessionSigningKey),
			"admin_totp_enc_key":        len(c.AdminTOTPEncKey),
		},
	}
}

// devDefaultOrigins 是 dev 环境的默认 CORS 白名单：两个 vite dev server 端口
// （用户面板 5173 / 后台 5174，见 docs/04-ops/local-development.md §1）。
//
// 只在 dev 生效。给 dev 默认值是为了让 `make run` + `pnpm dev` 直接互通 ——
// 本地开发**不该**用 vite dev proxy 绕过 CORS（§3.6 已裁定：
// 生产本来就是跨源的，用同源代理绕过等于把问题藏到上线那天）。
var devDefaultOrigins = []string{"http://localhost:5173", "http://localhost:5174"}

// parseAllowedOrigins 解析并校验 BP_ALLOWED_ORIGINS（逗号分隔）。
//
// 校验不是洁癖：一个写错的白名单项（带路径、带尾斜杠、大小写不一致）
// 永远匹配不上浏览器发来的 Origin，而**表现**是「CORS 不生效」——
// 与「没配置」完全一样的现象，却要按完全不同的方向去查。在启动时就拒绝掉。
func parseAllowedOrigins(raw, env string) ([]string, error) {
	fields := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))

	for _, f := range fields {
		o := strings.TrimSpace(f)
		if o == "" {
			continue
		}
		norm, err := normalizeOrigin(o, env)
		if err != nil {
			return nil, fmt.Errorf("BP_ALLOWED_ORIGINS 中的 %q 不合法：%w", o, err)
		}
		if _, dup := seen[norm]; dup {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}

	if len(out) == 0 {
		if env == "dev" {
			return devDefaultOrigins, nil
		}
		// fail-closed：非 dev 环境缺白名单直接拒绝启动。
		return nil, fmt.Errorf(
			"缺少 BP_ALLOWED_ORIGINS：%s 环境必须显式列出允许的 Web Origin（逗号分隔，形如 https://web.babel.plus）\n"+
				"  只有 dev 环境有默认值（%s）", env, strings.Join(devDefaultOrigins, ","))
	}
	return out, nil
}

// normalizeOrigin 校验单个 Origin 并归一化成 scheme://host[:port] 的小写形式。
//
// 归一化只做小写化：origin 没有 path，scheme 与 host 都大小写不敏感，
// 所以整串小写是安全的等价变换，而这让中间件的精确匹配不必关心大小写。
func normalizeOrigin(o, env string) (string, error) {
	// 这两个值必须显式拒绝，不能靠「反正匹配不上」——
	// 它们是配置者「想放开全部」时最可能手写进去的两个字符串，
	// 而 `*` 配 credentials 会被浏览器拒绝、`null` 会放行沙箱 iframe 与 file:// 页面。
	switch strings.ToLower(o) {
	case "*":
		return "", errors.New("通配符 `*` 不被接受：它与 Allow-Credentials 不兼容，且等于放开全网")
	case "null":
		return "", errors.New("`null` 不被接受：它会放行沙箱 iframe 与 file:// 页面")
	}
	// `https://*.babel.plus` 这类写法必须显式报错，不能靠「反正匹配不上」。
	// url.Parse **不会**拒绝 host 里的 `*`，于是它会安安静静躺进白名单，
	// 然后永远匹配不上任何真实 Origin —— 现象与「没配置」一模一样，
	// 而配置者会以为自己已经放开了整个子域。中间件只做集合相等判断，不支持任何模式。
	if strings.Contains(o, "*") {
		return "", errors.New("不支持通配符子域：CORS 白名单是精确匹配，请逐个列出实际域名")
	}

	u, err := url.Parse(o)
	if err != nil {
		return "", fmt.Errorf("解析失败: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("scheme 必须是 http 或 https")
	}
	if u.Host == "" {
		return "", errors.New("缺少主机名")
	}
	if u.User != nil {
		return "", errors.New("不能包含用户名密码")
	}
	// host 的字符集：字母数字 + `-` `.`（域名）、`:`（端口）、`[` `]`（IPv6 字面量）。
	// url.Parse 对 host 相当宽容，不自己收一道的话各种畸形串都能进白名单。
	for i := 0; i < len(u.Host); i++ {
		c := u.Host[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '.', c == ':', c == '[', c == ']':
		default:
			return "", fmt.Errorf("主机名含非法字符 %q", string(c))
		}
	}
	// Origin 是 scheme + host + port，**没有路径**。带路径（含尾斜杠 "/"）的白名单项
	// 永远匹配不上浏览器发来的 Origin 头 —— 这是配置 CORS 最常踩的一个坑。
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("不能包含路径、查询串或片段（尾部的 `/` 也算路径）")
	}
	// 生产的 Web 域名池全部走 HTTPS。允许 http 进 prod 白名单等于
	// 给「明文页面可读取已登录用户的 API 响应」留一条路。
	if env == "prod" && scheme != "https" {
		return "", errors.New("prod 环境只接受 https")
	}
	return scheme + "://" + strings.ToLower(u.Host), nil
}

// parseInternalAudience 校验内部面 OIDC 的 `aud`。
//
// 留空是允许的（内部面尚未接线，见 Config.InternalOIDCAudience 的注释）——
// 缺失时 middleware.AuthenticateInternal 会把整条内部面拒掉，fail-closed 落在运行时那一侧。
// 但**给了就必须是对的形状**：aud 的比对是 `string(claims.Aud) != cfg.Audience` 的逐字节相等
// （internal.go），所以一个多出来的尾斜杠、一个大写字母、一段 path，
// 都会让每一个真实 token 永远匹配不上，而现象是「六条定时任务全部 403」——
// 与「根本没配」一模一样，却要往完全不同的方向查。这个函数存在的唯一理由就是
// 把那种排查搬到启动期。
func parseInternalAudience(raw, env string) (string, error) {
	aud := strings.TrimSpace(raw)
	if aud == "" {
		return "", nil
	}

	u, err := url.Parse(aud)
	if err != nil {
		return "", fmt.Errorf("BP_INTERNAL_OIDC_AUDIENCE %q 不是合法 URL：%w", aud, err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("BP_INTERNAL_OIDC_AUDIENCE 必须是 https URL，当前 %q", aud)
	}
	if u.Host == "" {
		return "", fmt.Errorf("BP_INTERNAL_OIDC_AUDIENCE %q 缺少主机名", aud)
	}
	// 尾斜杠、path、query、fragment 一律拒绝：Cloud Run 签发的 ID token 里
	// aud 就是裸的服务 URL，任何一处多余字符都只会导致永不匹配。
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf(
			"BP_INTERNAL_OIDC_AUDIENCE 只能是裸的服务 URL（不带路径 / 查询 / 片段，也不带尾斜杠），当前 %q", aud)
	}
	// 大小写：aud 是逐字节比对的，而 Google 签发的值是全小写的服务 URL。
	// 这里不做静默小写化 —— 静默修正会让「配错了」变成「看起来配对了但仍不匹配」。
	if aud != strings.ToLower(aud) {
		return "", fmt.Errorf("BP_INTERNAL_OIDC_AUDIENCE 必须全小写（aud 逐字节比对），当前 %q", aud)
	}
	// prod 额外钉死 *.run.app：见 Config.InternalOIDCAudience 的注释 ——
	// 用镜像域名当 aud，每换一次域名就要重建六条 Scheduler 与一条队列，
	// 而漏掉哪一条只表现为「那个任务安静地不再运行」。
	if env == "prod" && !strings.HasSuffix(u.Host, ".run.app") {
		return "", fmt.Errorf(
			"prod 的 BP_INTERNAL_OIDC_AUDIENCE 必须是 Cloud Run 默认 URL（*.run.app），当前 %q\n"+
				"  不要用镜像域名或 API 主域名：它们会轮换，而 aud 换一次就要重建全部 Scheduler 与队列", aud)
	}
	return aud, nil
}

// parseInternalCallers 校验允许调用 /internal/tasks/* 的服务账号 email 白名单。
//
// 同样允许留空（整条内部面被拒）。给了就必须逐条是形状合法的 email：
// internal.go 的 internalCallerAllowed 做的是小写全等比对，所以这里就把小写化与去重做掉,
// 让「配置里写了大写」不至于变成一次线上排查。
func parseInternalCallers(raw, env string) ([]string, error) {
	fields := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))

	for _, f := range fields {
		c := strings.ToLower(strings.TrimSpace(f))
		if c == "" {
			continue
		}
		// `*` 与 `all` 必须显式拒绝，理由与 normalizeOrigin 相同：
		// 它们是配置者「想先放开跑通」时最可能手写进去的值，而这条白名单是
		// 内部面唯一的调用方约束 —— 放开它等于把九个定时任务端点开放给任何持有
		// 合法 Google ID token 的人（那是全世界任何一个 Google 账号）。
		switch c {
		case "*", "all", "any":
			return nil, fmt.Errorf("BP_INTERNAL_TASK_CALLERS 不接受 %q：必须逐个列出服务账号 email", c)
		}
		at := strings.IndexByte(c, '@')
		if at <= 0 || at == len(c)-1 || strings.Count(c, "@") != 1 {
			return nil, fmt.Errorf("BP_INTERNAL_TASK_CALLERS 中的 %q 不是合法 email", c)
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}

	// prod 下如果配了，就顺带检查它们像不像服务账号 —— 定时任务的调用方只可能是
	// 服务账号，写成个人 Google 账号说明配错了对象（且个人账号的 token 更容易泄漏）。
	if env == "prod" {
		for _, c := range out {
			if !strings.HasSuffix(c, ".iam.gserviceaccount.com") && !strings.HasSuffix(c, ".gserviceaccount.com") {
				return nil, fmt.Errorf(
					"prod 的 BP_INTERNAL_TASK_CALLERS 只接受服务账号 email（*.gserviceaccount.com），当前 %q", c)
			}
		}
	}
	return out, nil
}

// parseAdminIAPAudience 校验管理面 IAP 断言的 `aud`。
//
// 留空是允许的（缺失时 middleware.AuthenticateAdmin 把整个管理面拒掉）。
// 给了就必须是对的形状：aud 的比对是 subtle.ConstantTimeCompare 的逐字节相等，
// 所以一个多出来的尾斜杠、一段 https:// 前缀、一个大写字母，都会让每一份真实断言
// 永远匹配不上 —— 而现象是「所有管理员都进不去」，与「根本没配」一模一样。
//
// 形态来自 Google 的 IAP 文档：
//
//	/projects/<PROJECT_NUMBER>/global/backendServices/<BACKEND_SERVICE_ID>
//
// 两段都是**数字 ID**，不是名字：写项目 ID（oratis-491316）而不是项目编号
// 是这里最容易犯的错，且同样表现为「永远匹配不上」。
func parseAdminIAPAudience(raw string) (string, error) {
	aud := strings.TrimSpace(raw)
	if aud == "" {
		return "", nil
	}
	const (
		prefix = "/projects/"
		middle = "/global/backendServices/"
	)
	bad := func(why string) error {
		return fmt.Errorf("BP_ADMIN_IAP_AUDIENCE %q %s\n"+
			"  正确形态：%s<PROJECT_NUMBER>%s<BACKEND_SERVICE_ID>（两段都是数字 ID，不是名字）\n"+
			"  取值：gcloud compute backend-services describe <NAME> --global --format='value(id)'",
			aud, why, prefix, middle)
	}
	// 显式拒绝 URL 形态：把 IAP 的 aud 写成服务 URL 是最常见的一种猜法
	// （内部面的 aud 确实是 URL），而它永远匹配不上。
	if strings.Contains(aud, "://") {
		return "", bad("不是 URL —— 那是内部面 OIDC 的 aud 形态，IAP 断言用的是后端服务资源路径")
	}
	rest, ok := strings.CutPrefix(aud, prefix)
	if !ok {
		return "", bad("缺少 " + prefix + " 前缀")
	}
	projectNumber, serviceID, ok := strings.Cut(rest, middle)
	if !ok {
		return "", bad("缺少 " + middle + " 分段")
	}
	if !allDigits(projectNumber) {
		return "", bad("的 PROJECT_NUMBER 段不是纯数字（写成项目 ID 了？要的是项目编号）")
	}
	if !allDigits(serviceID) {
		return "", bad("的 BACKEND_SERVICE_ID 段不是纯数字（写成后端服务名了？要的是它的数字 id）")
	}
	return aud, nil
}

// allDigits 判断非空且全为十进制数字。
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseAdminTOTPEncKey 解码并校验 admin_users.totp_secret_enc 的 AES 密钥。
//
// 留空是允许的（缺失时 step-up 一律拒绝，危险操作做不了）。
// 给了就必须解出**恰好 32 字节** —— AES-256 的密钥长度。
//
// 🔴 为什么不接受 16 / 24 字节：aes.NewCipher 会照单全收，于是一个被截断的密钥
// 会安安静静地退化成 AES-128，而唯一的现象是……没有现象。密钥长度是我们能在
// 启动期一次性确认的少数几件事之一，不该留给运行时。
// 长度不对时也**不打印密钥内容**，只报长度。
func parseAdminTOTPEncKey(raw string) ([]byte, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, nil
	}
	// 两种 base64 都收：`openssl rand -base64 32` 出带填充的标准形态，
	// 而从 Secret Manager 里取时可能已经去掉了填充。
	key, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(v)
	}
	if err != nil {
		return nil, fmt.Errorf("BP_ADMIN_TOTP_ENC_KEY 不是合法 base64：%w\n"+
			"  生成：openssl rand -base64 32", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf(
			"BP_ADMIN_TOTP_ENC_KEY 解码后应为 32 字节（AES-256），实得 %d 字节\n"+
				"  16 / 24 字节也能被 aes.NewCipher 收下，于是密钥被截断这件事完全没有现象 ——\n"+
				"  所以这里拒绝启动。生成：openssl rand -base64 32", len(key))
	}
	return key, nil
}

// redactDSN 去掉连接串里的密码。Postgres DSN 的密码可能出现在 URL 形式或 kv 形式里，两种都处理。
func redactDSN(dsn string) string {
	if i := strings.Index(dsn, "://"); i >= 0 {
		rest := dsn[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			return dsn[:i+3] + "***@" + rest[at+1:]
		}
		return dsn
	}
	parts := strings.Fields(dsn)
	for i, p := range parts {
		if strings.HasPrefix(p, "password=") {
			parts[i] = "password=***"
		}
	}
	return strings.Join(parts, " ")
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
