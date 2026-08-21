package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/argon2"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/config"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

const testPepper = "test-session-signing-key"

func testServer() *Server {
	return &Server{
		cfg:    &config.Config{Env: "test", SessionSigningKey: testPepper},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// ============================================================
// argon2id
// ============================================================

func TestHashPasswordRoundTrip(t *testing.T) {
	ctx := context.Background()
	const pw = "correct horse battery staple"

	encoded, err := hashPassword(ctx, pw)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}

	ok, err := verifyPassword(ctx, encoded, pw)
	if err != nil {
		t.Fatalf("verifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("正确口令校验失败")
	}

	// 差一个字符就必须不通过。
	ok, err = verifyPassword(ctx, encoded, pw+"x")
	if err != nil {
		t.Fatalf("verifyPassword(错误口令): %v", err)
	}
	if ok {
		t.Fatal("错误口令通过了校验")
	}
}

// 同一个口令两次哈希必须不同 —— 相同就说明 salt 没起作用，
// 而那意味着一张彩虹表能同时打穿所有用相同口令的账号。
func TestHashPasswordUsesRandomSalt(t *testing.T) {
	ctx := context.Background()
	a, err := hashPassword(ctx, "same-password")
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashPassword(ctx, "same-password")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("两次哈希结果相同：salt 没有随机化")
	}
}

func TestHashPasswordEncoding(t *testing.T) {
	encoded, err := hashPassword(context.Background(), "pw-for-encoding")
	if err != nil {
		t.Fatal(err)
	}

	wantPrefix := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$", argon2.Version, argon2MemoryKiB, argon2Time, argon2Threads)
	if !strings.HasPrefix(encoded, wantPrefix) {
		t.Fatalf("编码串前缀不对\n got: %s\nwant前缀: %s", encoded, wantPrefix)
	}

	p, salt, key, err := parseArgon2Hash(encoded)
	if err != nil {
		t.Fatalf("parseArgon2Hash: %v", err)
	}
	if p.Memory != argon2MemoryKiB || p.Time != argon2Time || p.Threads != argon2Threads {
		t.Fatalf("解析出的参数与常量不一致: %+v", p)
	}
	if len(salt) != argon2SaltLen {
		t.Fatalf("salt 长度 = %d, want %d", len(salt), argon2SaltLen)
	}
	if len(key) != int(argon2KeyLen) {
		t.Fatalf("key 长度 = %d, want %d", len(key), argon2KeyLen)
	}
}

// parseArgon2Hash 解析的是**从数据库读回来的值**，不是我们自己刚写出去的值。
// 这里逐条钉住它对畸形输入的拒绝，尤其是那条内存上限 ——
// 一个 m=4194304 的串会让 argon2.IDKey 老老实实去申请 4 GiB。
func TestParseArgon2HashRejectsMalformed(t *testing.T) {
	valid, err := hashPassword(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(valid, "$")

	cases := []struct {
		name    string
		encoded string
	}{
		{"空串", ""},
		{"AnonymizeUser 置空", ""},
		{"bcrypt 串", "$2a$10$abcdefghijklmnopqrstuv"},
		{"段数不足", "$argon2id$v=19$m=19456,t=2,p=1$onlysalt"},
		{"算法不对", "$argon2i$v=19$m=19456,t=2,p=1$" + parts[4] + "$" + parts[5]},
		{"版本不对", "$argon2id$v=16$m=19456,t=2,p=1$" + parts[4] + "$" + parts[5]},
		{"参数缺一项", "$argon2id$v=19$m=19456,t=2$" + parts[4] + "$" + parts[5]},
		{"内存超上限", "$argon2id$v=19$m=4194304,t=2,p=1$" + parts[4] + "$" + parts[5]},
		{"内存为零", "$argon2id$v=19$m=0,t=2,p=1$" + parts[4] + "$" + parts[5]},
		{"迭代超上限", "$argon2id$v=19$m=19456,t=99,p=1$" + parts[4] + "$" + parts[5]},
		{"并行度超上限", "$argon2id$v=19$m=19456,t=2,p=99$" + parts[4] + "$" + parts[5]},
		{"salt 非 base64", "$argon2id$v=19$m=19456,t=2,p=1$!!!!$" + parts[5]},
		{"key 为空", "$argon2id$v=19$m=19456,t=2,p=1$" + parts[4] + "$"},
		{"key 超长", "$argon2id$v=19$m=19456,t=2,p=1$" + parts[4] + "$" +
			base64.RawStdEncoding.EncodeToString(make([]byte, maxArgon2KeyLen+1))},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, _, err := parseArgon2Hash(c.encoded); err == nil {
				t.Fatalf("畸形编码串被接受了: %q", c.encoded)
			}
		})
	}
}

// 参数存在编码串里，所以按老参数生成的哈希必须仍然验得过 ——
// 否则任何一次调参都等于强制全体用户改密码。
func TestVerifyPasswordUsesEmbeddedParams(t *testing.T) {
	const pw = "legacy-params-password"
	var (
		legacyMem  uint32 = 8192
		legacyTime uint32 = 1
		legacyThr  uint8  = 1
	)
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(pw), salt, legacyTime, legacyMem, legacyThr, 32)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, legacyMem, legacyTime, legacyThr,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))

	ok, err := verifyPassword(context.Background(), encoded, pw)
	if err != nil {
		t.Fatalf("verifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("按旧参数生成的哈希验不过 —— 调参会把所有存量用户锁在门外")
	}
}

// AnonymizeUser 把 password_hash 置成空串。这条路径必须返回 (false, err)，
// 而调用方（Login / ChangePassword）必须把它当成「凭据无效」而不是 500。
func TestVerifyPasswordEmptyStoredHash(t *testing.T) {
	ok, err := verifyPassword(context.Background(), "", "anything")
	if ok {
		t.Fatal("空 password_hash 竟然验证通过")
	}
	if !errors.Is(err, errBadPasswordHash) {
		t.Fatalf("err = %v, want errBadPasswordHash", err)
	}
}

// 上下文取消时不应再去烧 CPU 算一个没人接收的哈希。
func TestHashPasswordRespectsCanceledContext(t *testing.T) {
	// 先占满全部槽位，让下一次获取必然阻塞在 select 上。
	for i := 0; i < argon2Concurrency; i++ {
		argon2Slots <- struct{}{}
	}
	defer func() {
		for i := 0; i < argon2Concurrency; i++ {
			<-argon2Slots
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hashPassword(ctx, "pw"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestBurnPasswordVerificationDoesNotPanic(t *testing.T) {
	// 「用户不存在」分支必须能安全跑完 —— 它在每一次针对不存在邮箱的登录上执行。
	burnPasswordVerification(context.Background(), "whatever")
}

// ============================================================
// 邀请码
// ============================================================

func inviteCode(mut func(*dbgen.InviteCode)) dbgen.InviteCode {
	c := dbgen.InviteCode{ID: 7, Code: "ABCD2345", MaxUses: 1, UsedCount: 0}
	if mut != nil {
		mut(&c)
	}
	return c
}

func TestClassifyInviteCode(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		code dbgen.InviteCode
		want gen.InviteVerifyResultState
	}{
		{"全新用户码", inviteCode(nil), gen.InviteVerifyResultStateOk},
		{"种子码未用完", inviteCode(func(c *dbgen.InviteCode) { c.MaxUses = 10; c.UsedCount = 3 }), gen.InviteVerifyResultStateOk},
		{"一次性码已核销", inviteCode(func(c *dbgen.InviteCode) { c.UsedCount = 1 }), gen.InviteVerifyResultStateExhausted},
		{"种子码用尽", inviteCode(func(c *dbgen.InviteCode) { c.MaxUses = 10; c.UsedCount = 10 }), gen.InviteVerifyResultStateExhausted},
		{"已吊销", inviteCode(func(c *dbgen.InviteCode) {
			c.RevokedAt = pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}
		}), gen.InviteVerifyResultStateInvalid},
		{"已过期", inviteCode(func(c *dbgen.InviteCode) {
			c.ExpiresAt = pgtype.Timestamptz{Time: now.Add(-time.Second), Valid: true}
		}), gen.InviteVerifyResultStateInvalid},
		{"未过期", inviteCode(func(c *dbgen.InviteCode) {
			c.ExpiresAt = pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}
		}), gen.InviteVerifyResultStateOk},
		// 吊销优先于用尽：告诉用户「去催邀请人再生成一个」是错的引导。
		{"既吊销又用尽 → invalid", inviteCode(func(c *dbgen.InviteCode) {
			c.UsedCount = 1
			c.RevokedAt = pgtype.Timestamptz{Time: now, Valid: true}
		}), gen.InviteVerifyResultStateInvalid},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyInviteCode(c.code, now); got != c.want {
				t.Fatalf("classifyInviteCode = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNormalizeInviteCode(t *testing.T) {
	for in, want := range map[string]string{
		"  abcd2345 ": "ABCD2345",
		"Abcd2345":    "ABCD2345",
		"ABCD2345":    "ABCD2345",
	} {
		if got := normalizeInviteCode(in); got != want {
			t.Fatalf("normalizeInviteCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlausibleInviteCode(t *testing.T) {
	if plausibleInviteCode("ABC") {
		t.Fatal("过短的码应当被拒")
	}
	if plausibleInviteCode(strings.Repeat("A", maxInviteCodeLen+1)) {
		t.Fatal("过长的码应当被拒")
	}
	if !plausibleInviteCode("ABCD") {
		t.Fatal("下界长度应当被接受")
	}
}

// ============================================================
// 注册事务
// ============================================================

// fakeQuerier 只实现注册事务用到的那几个方法。
//
// 内嵌 dbgen.Querier 且不赋值：任何**未预期**的查询都会因为空接口而 panic，
// 于是「注册路径悄悄多打了一次库」这种事会在测试里直接炸出来，而不是被忽略。
type fakeQuerier struct {
	dbgen.Querier

	groupID  int64
	groupErr error

	redeemErr     error
	createUserErr error

	calls      []string
	createUser dbgen.CreateUserParams
	trafficFor int64
	inviteUse  dbgen.RecordInviteCodeUseParams
	consumedID int64
	verifiedID int64
	session    dbgen.CreateUserSessionParams
}

func (f *fakeQuerier) note(name string) { f.calls = append(f.calls, name) }

func (f *fakeQuerier) GetRegistrationGroupID(context.Context) (int64, error) {
	f.note("GetRegistrationGroupID")
	if f.groupErr != nil {
		return 0, f.groupErr
	}
	return f.groupID, nil
}

func (f *fakeQuerier) RedeemInviteCode(_ context.Context, id int64) (dbgen.InviteCode, error) {
	f.note("RedeemInviteCode")
	if f.redeemErr != nil {
		return dbgen.InviteCode{}, f.redeemErr
	}
	return dbgen.InviteCode{ID: id, UsedCount: 1, MaxUses: 1}, nil
}

func (f *fakeQuerier) CreateUser(_ context.Context, arg dbgen.CreateUserParams) (dbgen.User, error) {
	f.note("CreateUser")
	f.createUser = arg
	if f.createUserErr != nil {
		return dbgen.User{}, f.createUserErr
	}
	return dbgen.User{ID: 4242, Email: arg.Email, GroupID: arg.GroupID, InvitedBy: arg.InvitedBy}, nil
}

func (f *fakeQuerier) CreateUserTraffic(_ context.Context, userID int64) (dbgen.UserTraffic, error) {
	f.note("CreateUserTraffic")
	f.trafficFor = userID
	return dbgen.UserTraffic{UserID: userID}, nil
}

func (f *fakeQuerier) RecordInviteCodeUse(_ context.Context, arg dbgen.RecordInviteCodeUseParams) (dbgen.InviteCodeUse, error) {
	f.note("RecordInviteCodeUse")
	f.inviteUse = arg
	return dbgen.InviteCodeUse{ID: 1, InviteCodeID: arg.InviteCodeID, UserID: arg.UserID}, nil
}

func (f *fakeQuerier) ConsumeEmailVerification(_ context.Context, id int64) error {
	f.note("ConsumeEmailVerification")
	f.consumedID = id
	return nil
}

func (f *fakeQuerier) MarkEmailVerified(_ context.Context, id int64) error {
	f.note("MarkEmailVerified")
	f.verifiedID = id
	return nil
}

func (f *fakeQuerier) CreateUserSession(_ context.Context, arg dbgen.CreateUserSessionParams) (dbgen.UserSession, error) {
	f.note("CreateUserSession")
	f.session = arg
	return dbgen.UserSession{
		ID: 99, UserID: arg.UserID, RefreshHash: arg.RefreshHash,
		ExpiresAt: arg.ExpiresAt,
		IssuedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}, nil
}

func testRegisterInput(mut func(*registerInput)) registerInput {
	owner := int64(1001)
	ip := netip.MustParseAddr("203.0.113.7")
	ua := "bp-web/1.0"
	in := registerInput{
		email:          "newbie@example.com",
		passwordHash:   "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2Fs$aGFzaA",
		invite:         inviteCode(func(c *dbgen.InviteCode) { c.OwnerUserID = &owner }),
		verificationID: 555,
		meta:           RequestMetadata{IP: &ip, UserAgent: &ua},
		now:            time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
	}
	if mut != nil {
		mut(&in)
	}
	return in
}

func TestRegisterTxHappyPath(t *testing.T) {
	s := testServer()
	q := &fakeQuerier{groupID: 3}
	in := testRegisterInput(nil)

	user, sess, raw, err := s.registerTx(context.Background(), q, in)
	if err != nil {
		t.Fatalf("registerTx: %v", err)
	}

	// 五张表必须都写到。user_traffic 尤其不能漏 —— 缺它的用户
	// 在 ListAvailableUsersByServer 的 JOIN 里永远查不到，节点看不见他。
	want := []string{
		"GetRegistrationGroupID", "RedeemInviteCode", "CreateUser",
		"CreateUserTraffic", "RecordInviteCodeUse",
		"ConsumeEmailVerification", "MarkEmailVerified", "CreateUserSession",
	}
	if strings.Join(q.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("调用序列不对\n got: %v\nwant: %v", q.calls, want)
	}

	if q.createUser.GroupID != 3 {
		t.Fatalf("group_id = %d, want 3", q.createUser.GroupID)
	}
	if q.createUser.InvitedBy == nil || *q.createUser.InvitedBy != 1001 {
		t.Fatalf("invited_by = %v, want 1001（返佣归属）", q.createUser.InvitedBy)
	}
	if q.trafficFor != user.ID {
		t.Fatalf("user_traffic.user_id = %d, want %d", q.trafficFor, user.ID)
	}
	if q.inviteUse.UserID != user.ID || q.inviteUse.InviteCodeID != in.invite.ID {
		t.Fatalf("invite_code_uses 写错: %+v", q.inviteUse)
	}
	if q.inviteUse.RequestIp == nil || q.inviteUse.RequestIp.String() != "203.0.113.7" {
		t.Fatalf("invite_code_uses.request_ip = %v, want 203.0.113.7", q.inviteUse.RequestIp)
	}
	if q.consumedID != in.verificationID {
		t.Fatalf("核销的验证码 id = %d, want %d", q.consumedID, in.verificationID)
	}
	if q.verifiedID != user.ID {
		t.Fatalf("MarkEmailVerified 的 user_id = %d, want %d", q.verifiedID, user.ID)
	}

	// 🔴 会话表里存的必须是哈希，明文只回给客户端。
	if raw == "" {
		t.Fatal("没有返回明文会话 token")
	}
	if string(q.session.RefreshHash) == raw {
		t.Fatal("user_sessions.refresh_hash 存了明文")
	}
	// 且哈希必须与中间件的校验侧同源，否则「刚注册完就被登出」。
	wantHash := middleware.HashSessionToken(testPepper, raw)
	if string(q.session.RefreshHash) != string(wantHash) {
		t.Fatal("签发侧与校验侧的哈希不一致：注册后的会话立刻就是无效的")
	}
	if !sess.ExpiresAt.Valid || !sess.ExpiresAt.Time.Equal(in.now.Add(sessionTTL)) {
		t.Fatalf("会话过期时刻 = %v, want %v", sess.ExpiresAt.Time, in.now.Add(sessionTTL))
	}
	if q.session.UserAgent == nil || *q.session.UserAgent != "bp-web/1.0" {
		t.Fatalf("user_agent = %v", q.session.UserAgent)
	}
}

// 一次性邀请码被并发抢走：RedeemInviteCode 的条件 UPDATE 影响 0 行 → ErrNoRows。
// 这条分支决定了「同一个码带进来两个人」不会发生，而它没法在真库上稳定复现。
func TestRegisterTxInviteRaced(t *testing.T) {
	s := testServer()
	q := &fakeQuerier{groupID: 1, redeemErr: pgx.ErrNoRows}

	_, _, _, err := s.registerTx(context.Background(), q, testRegisterInput(nil))
	if !errors.Is(err, errInviteRaced) {
		t.Fatalf("err = %v, want errInviteRaced", err)
	}
	// 核销失败之后**一行用户都不能建**。
	for _, c := range q.calls {
		if c == "CreateUser" {
			t.Fatal("邀请码没核销成功却建了用户")
		}
	}
}

// 邮箱唯一索引才是真正的互斥（预检查只是为了给出好的错误信息）。
// 撞索引必须转成 409，不能是 500。
func TestRegisterTxEmailTaken(t *testing.T) {
	s := testServer()
	q := &fakeQuerier{groupID: 1, createUserErr: &pgconn.PgError{Code: "23505", ConstraintName: "users_email_uk"}}

	_, _, _, err := s.registerTx(context.Background(), q, testRegisterInput(nil))
	if !errors.Is(err, errEmailTaken) {
		t.Fatalf("err = %v, want errEmailTaken", err)
	}
}

// 其他 PG 错误不能被误判成「邮箱已注册」。
func TestRegisterTxOtherDBErrorNotMisread(t *testing.T) {
	s := testServer()
	boom := &pgconn.PgError{Code: "23503", ConstraintName: "users_group_id_fkey"}
	q := &fakeQuerier{groupID: 1, createUserErr: boom}

	_, _, _, err := s.registerTx(context.Background(), q, testRegisterInput(nil))
	if errors.Is(err, errEmailTaken) {
		t.Fatal("外键错误被误判成邮箱冲突")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want 原始 PG 错误", err)
	}
}

// 管理员种子码的 owner_user_id 为 NULL，此时 invited_by 也必须是 NULL ——
// 硬塞一个 0 进去会撞 users.invited_by 的外键。
func TestRegisterTxSeedCodeHasNoInviter(t *testing.T) {
	s := testServer()
	q := &fakeQuerier{groupID: 1}
	in := testRegisterInput(func(in *registerInput) {
		in.invite = inviteCode(func(c *dbgen.InviteCode) { c.OwnerUserID = nil; c.MaxUses = 20 })
	})

	if _, _, _, err := s.registerTx(context.Background(), q, in); err != nil {
		t.Fatalf("registerTx: %v", err)
	}
	if q.createUser.InvitedBy != nil {
		t.Fatalf("种子码注册的 invited_by = %v, want nil", q.createUser.InvitedBy)
	}
}

// server_groups 为空时必须给出可识别的错误，而不是一个裸 ErrNoRows ——
// 那是「库没灌种子数据」，运维一眼要能看出来。
func TestRegisterTxNoDefaultGroup(t *testing.T) {
	s := testServer()
	q := &fakeQuerier{groupErr: pgx.ErrNoRows}

	_, _, _, err := s.registerTx(context.Background(), q, testRegisterInput(nil))
	if !errors.Is(err, errNoDefaultGroup) {
		t.Fatalf("err = %v, want errNoDefaultGroup", err)
	}
}

// ============================================================
// 验证码 / 重置令牌
// ============================================================

// 🔴 这是本文件最重要的一条断言。
// 找回密码只能按 code_hash 全表定位（GetEmailVerificationByCodeHash），
// 一旦重置令牌退化成 6 位数字，随便猜一个就能命中**任意用户**的待重置记录。
func TestResetSecretIsHighEntropy(t *testing.T) {
	secret, hash, err := newVerificationSecret(testPepper, "victim@example.com", dbgen.VerificationPurposePasswordReset)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		t.Fatalf("重置令牌不是 base64url: %v", err)
	}
	if len(raw) != resetTokenBytes {
		t.Fatalf("重置令牌 = %d 字节, want %d —— 低熵令牌配全表哈希查找等于任意账号接管", len(raw), resetTokenBytes)
	}
	if len(hash) != 32 {
		t.Fatalf("哈希长度 = %d, want 32(sha256)", len(hash))
	}

	// 而且它的哈希**不能**依赖邮箱：依赖了就查不出来（查询只有 code_hash + purpose）。
	other := hashResetToken(testPepper, secret)
	if string(other) != string(hash) {
		t.Fatal("重置令牌的哈希依赖了邮箱，GetEmailVerificationByCodeHash 将永远查不到它")
	}
}

func TestRegisterSecretIsSixDigits(t *testing.T) {
	secret, hash, err := newVerificationSecret(testPepper, "a@example.com", dbgen.VerificationPurposeRegister)
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != emailCodeDigits {
		t.Fatalf("验证码长度 = %d, want %d", len(secret), emailCodeDigits)
	}
	for _, c := range secret {
		if c < '0' || c > '9' {
			t.Fatalf("验证码含非数字字符: %q", secret)
		}
	}
	want := hashEmailCode(testPepper, "a@example.com", dbgen.VerificationPurposeRegister, secret)
	if string(hash) != string(want) {
		t.Fatal("验证码哈希与 hashEmailCode 不一致")
	}
}

// 同一个 6 位码在不同邮箱下必须产生不同哈希 —— 这样将来谁加了按 code_hash
// 查找的路径也不会打开「一个码通用」的缺口。
func TestEmailCodeHashIsScopedToEmail(t *testing.T) {
	a := hashEmailCode(testPepper, "a@example.com", dbgen.VerificationPurposeRegister, "123456")
	b := hashEmailCode(testPepper, "b@example.com", dbgen.VerificationPurposeRegister, "123456")
	if string(a) == string(b) {
		t.Fatal("不同邮箱的同一验证码产生了相同哈希")
	}
	// 大小写归一化后必须一致，否则用户用 Gmail 的大写形式就验不过。
	c := hashEmailCode(testPepper, "A@Example.com", dbgen.VerificationPurposeRegister, "123456")
	if string(a) != string(c) {
		t.Fatal("邮箱大小写影响了验证码哈希")
	}
}

// 换 pepper 必须让所有存量哈希失效（这正是 pepper 存在的意义）。
func TestVerificationHashDependsOnPepper(t *testing.T) {
	a := hashEmailCode("pepper-a", "x@example.com", dbgen.VerificationPurposeRegister, "123456")
	b := hashEmailCode("pepper-b", "x@example.com", dbgen.VerificationPurposeRegister, "123456")
	if string(a) == string(b) {
		t.Fatal("pepper 没有参与验证码哈希")
	}
}

func TestVerificationPurposeForScene(t *testing.T) {
	// ⚠️ 两套命名不一致是真实存在的：openapi 的 reset_password ↔ DB 的 password_reset。
	cases := map[gen.EmailCodeRequestScene]dbgen.VerificationPurpose{
		gen.Register:      dbgen.VerificationPurposeRegister,
		gen.ResetPassword: dbgen.VerificationPurposePasswordReset,
		gen.BindEmail:     dbgen.VerificationPurposeEmailChange,
	}
	for scene, want := range cases {
		got, ok := verificationPurposeForScene(scene)
		if !ok || got != want {
			t.Fatalf("scene %q → (%q, %v), want %q", scene, got, ok, want)
		}
	}
	if _, ok := verificationPurposeForScene(gen.EmailCodeRequestScene("nope")); ok {
		t.Fatal("未知 scene 被接受了")
	}
}

func TestRandomDigits(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		c, err := randomDigits(emailCodeDigits)
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != emailCodeDigits {
			t.Fatalf("长度 = %d", len(c))
		}
		seen[c] = true
	}
	if len(seen) < 32 {
		t.Fatalf("64 次只生成了 %d 个不同的码，随机性可疑", len(seen))
	}
}

// ============================================================
// 邮箱 / 口令 / 会话
// ============================================================

func TestNormalizeAndValidateEmail(t *testing.T) {
	valid := []string{"a@example.com", "user.name+tag@sub.example.co.uk", "x_y@example.io"}
	for _, e := range valid {
		if !validEmail(normalizeEmail(e)) {
			t.Fatalf("%q 应当是合法邮箱", e)
		}
	}

	invalid := []string{
		"", "a", "@example.com", "a@", "a@b", "a b@example.com",
		"张三 <a@example.com>", // 带显示名的形态不能直接入库
		"a@example.com.",
		"a@.example.com",
		strings.Repeat("a", 250) + "@example.com",
	}
	for _, e := range invalid {
		if validEmail(normalizeEmail(e)) {
			t.Fatalf("%q 不该被判为合法邮箱", e)
		}
	}

	if got := normalizeEmail("  Foo@EXAMPLE.com "); got != "foo@example.com" {
		t.Fatalf("normalizeEmail = %q", got)
	}
}

func TestEmailDomain(t *testing.T) {
	if got := emailDomain("a@qq.com"); got != "qq.com" {
		t.Fatalf("emailDomain = %q, want qq.com", got)
	}
	if got := emailDomain("broken"); got != "" {
		t.Fatalf("emailDomain(无 @) = %q, want 空", got)
	}
}

func TestValidPassword(t *testing.T) {
	if validPassword(strings.Repeat("a", minPasswordRunes-1)) {
		t.Fatal("过短的口令被接受")
	}
	if !validPassword(strings.Repeat("a", minPasswordRunes)) {
		t.Fatal("下界长度的口令被拒")
	}
	if !validPassword(strings.Repeat("a", maxPasswordRunes)) {
		t.Fatal("上界长度的口令被拒")
	}
	if validPassword(strings.Repeat("a", maxPasswordRunes+1)) {
		t.Fatal("超长口令被接受")
	}
	// 按**字符**计数而不是字节：8 个汉字是 24 字节，不该因为字节数而被当成超长，
	// 也不该因为「才 8 个字符」而被当成过短。
	if !validPassword(strings.Repeat("密", minPasswordRunes)) {
		t.Fatal("8 个汉字的口令被拒 —— 长度算的是字节而不是字符")
	}
	if validPassword(strings.Repeat("密", maxPasswordRunes+1)) {
		t.Fatal("129 个汉字的口令被接受")
	}
}

func TestPlausibleSessionTokenShape(t *testing.T) {
	tok, err := randomToken(sessionTokenBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !plausibleSessionTokenShape(tok) {
		t.Fatalf("自己签发的 token 被自己的形态检查拒了: %q", tok)
	}
	// 与中间件那侧的区间必须兼容，否则「刚拿到的 token 一用就 401」。
	if len(tok) < 24 || len(tok) > 128 {
		t.Fatalf("token 长度 %d 落在 middleware 的 [24,128] 之外", len(tok))
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 129), "has space", "has+plus", "has=pad"} {
		if plausibleSessionTokenShape(bad) {
			t.Fatalf("%q 不该通过形态检查", bad)
		}
	}
}

func TestSessionTokensShape(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sess := dbgen.UserSession{ExpiresAt: pgtype.Timestamptz{Time: now.Add(sessionTTL), Valid: true}}

	got := sessionTokens(sess, "RAWTOKEN", now)
	if got.TokenType != gen.Bearer {
		t.Fatalf("token_type = %q, want Bearer", got.TokenType)
	}
	if want := int32(sessionTTL / time.Second); got.ExpiresIn != want {
		t.Fatalf("expires_in = %d, want %d（必须是真实剩余秒数，不是契约里写死的 900）", got.ExpiresIn, want)
	}
	// 已登记的契约偏差：没有 JWT 基础设施，access 与 refresh 是同一枚不透明 token。
	// 这条断言存在的意义是：将来接 JWT 时它会失败，提醒改的人回来读文件头的说明。
	if got.AccessToken != got.RefreshToken {
		t.Fatal("access/refresh 不再相同 —— 说明已接入 JWT，请同步更新 auth.go 文件头的偏差说明")
	}

	// 过期时刻已过 → 剩余秒数不能是负数。
	expired := dbgen.UserSession{ExpiresAt: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}}
	if got := sessionTokens(expired, "X", now); got.ExpiresIn != 0 {
		t.Fatalf("已过期会话的 expires_in = %d, want 0", got.ExpiresIn)
	}
}

func TestRandomTokenIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 256; i++ {
		tok, err := randomToken(sessionTokenBytes)
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatal("randomToken 产生了重复值")
		}
		seen[tok] = true
	}
}

// PublicOperations 里的每个名字都必须是 StrictServerInterface 上真实存在的方法。
//
// 拼错一个名字的后果是**静默的**：deny-by-default 的装配会认为它不在免登录表里，
// 于是给一个本该免登录的端点挂上鉴权 —— 而「注册要求先登录」这种故障
// 只有新用户遇得到，我们自己永远测不出来。这条断言就是为了让拼写错误在 CI 里炸掉。
func TestPublicOperationsAreRealOperations(t *testing.T) {
	iface := reflect.TypeOf((*gen.StrictServerInterface)(nil)).Elem()
	for name := range PublicOperations {
		if _, ok := iface.MethodByName(name); !ok {
			t.Fatalf("PublicOperations 里的 %q 不是 StrictServerInterface 的方法（拼写错误或 operationId 已改）", name)
		}
	}
	// 数量对齐 openapi.yaml 里 `security: []` 的 operation 数。
	// 契约新增免登录端点时这条会失败，提醒同步更新 —— 这正是我们要的。
	if len(PublicOperations) != 11 {
		t.Fatalf("PublicOperations 有 %d 项，openapi 里 security: [] 的是 11 项", len(PublicOperations))
	}
}

// 本文件实现的 10 个 operation 必须都在 StrictServerInterface 上（且签名对得上）。
// Server 内嵌 Unimplemented，所以**漏实现不会在编译期暴露** —— 一个方法名写错
// 只会让它悄悄退回 501。这条断言把「以为实现了其实没有」变成编译期错误。
func TestAuthOperationsAreImplemented(t *testing.T) {
	var s any = &Server{}
	if _, ok := s.(interface {
		RegisterAccount(context.Context, gen.RegisterAccountRequestObject) (gen.RegisterAccountResponseObject, error)
		SendEmailCode(context.Context, gen.SendEmailCodeRequestObject) (gen.SendEmailCodeResponseObject, error)
		Login(context.Context, gen.LoginRequestObject) (gen.LoginResponseObject, error)
		RefreshToken(context.Context, gen.RefreshTokenRequestObject) (gen.RefreshTokenResponseObject, error)
		Logout(context.Context, gen.LogoutRequestObject) (gen.LogoutResponseObject, error)
		ForgotPassword(context.Context, gen.ForgotPasswordRequestObject) (gen.ForgotPasswordResponseObject, error)
		ResetPassword(context.Context, gen.ResetPasswordRequestObject) (gen.ResetPasswordResponseObject, error)
		VerifyInviteCode(context.Context, gen.VerifyInviteCodeRequestObject) (gen.VerifyInviteCodeResponseObject, error)
		GetCurrentUser(context.Context, gen.GetCurrentUserRequestObject) (gen.GetCurrentUserResponseObject, error)
		ChangePassword(context.Context, gen.ChangePasswordRequestObject) (gen.ChangePasswordResponseObject, error)
	}); !ok {
		t.Fatal("账户体系的某个 operation 没有被 Server 覆盖，仍落在 Unimplemented 的 501 上")
	}
}

func TestIsUniqueViolation(t *testing.T) {
	if !isUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("23505 应当被识别为唯一约束冲突")
	}
	if !isUniqueViolation(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505"})) {
		t.Fatal("包装过的 23505 应当仍被识别")
	}
	if isUniqueViolation(pgx.ErrNoRows) {
		t.Fatal("ErrNoRows 被误判为唯一约束冲突")
	}
	if isUniqueViolation(nil) {
		t.Fatal("nil 被误判为唯一约束冲突")
	}
}
