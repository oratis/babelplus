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
		"secrets_len": map[string]int{
			"subscription_token_pepper": len(c.SubscriptionTokenPepper),
			"node_key_pepper":           len(c.NodeKeyPepper),
			"session_signing_key":       len(c.SessionSigningKey),
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
