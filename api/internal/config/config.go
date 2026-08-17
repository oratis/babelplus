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
	"fmt"
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
		"secrets_len": map[string]int{
			"subscription_token_pepper": len(c.SubscriptionTokenPepper),
			"node_key_pepper":           len(c.NodeKeyPepper),
			"session_signing_key":       len(c.SessionSigningKey),
		},
	}
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
