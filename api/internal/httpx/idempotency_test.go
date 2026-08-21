package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

// fakeIdemStore 是内存版 idempotency_keys，语义逐条对齐 orders.sql：
//   - Claim: ON CONFLICT (key) DO NOTHING —— 冲突时返回 pgx.ErrNoRows
//   - Get:   带 expires_at > now() 过滤 —— 过期行查不到
//   - Complete: 置 status/response_*
type fakeIdemStore struct {
	rows    map[string]dbgen.IdempotencyKey
	expired map[string]bool // 主键占用但已过期（Get 查不到）
	claims  int
}

func newFakeIdemStore() *fakeIdemStore {
	return &fakeIdemStore{rows: map[string]dbgen.IdempotencyKey{}, expired: map[string]bool{}}
}

func (f *fakeIdemStore) ClaimIdempotencyKey(_ context.Context, arg dbgen.ClaimIdempotencyKeyParams) (dbgen.IdempotencyKey, error) {
	f.claims++
	if _, exists := f.rows[arg.Key]; exists {
		return dbgen.IdempotencyKey{}, pgx.ErrNoRows
	}
	row := dbgen.IdempotencyKey{
		Key:         arg.Key,
		UserID:      arg.UserID,
		Endpoint:    arg.Endpoint,
		RequestHash: arg.RequestHash,
		Status:      idempotencyStatusInProgress,
	}
	f.rows[arg.Key] = row
	return row, nil
}

func (f *fakeIdemStore) GetIdempotencyKey(_ context.Context, key string) (dbgen.IdempotencyKey, error) {
	row, ok := f.rows[key]
	if !ok || f.expired[key] {
		return dbgen.IdempotencyKey{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeIdemStore) CompleteIdempotencyKey(_ context.Context, arg dbgen.CompleteIdempotencyKeyParams) error {
	row, ok := f.rows[arg.Key]
	if !ok {
		return pgx.ErrNoRows
	}
	row.Status = idempotencyStatusCompleted
	row.ResponseCode = arg.ResponseCode
	row.ResponseBody = arg.ResponseBody
	f.rows[arg.Key] = row
	return nil
}

func userPtr(id int64) *int64 { return &id }

func orderReq(key string) IdempotentRequest {
	return IdempotentRequest{
		Key:      key,
		UserID:   userPtr(42),
		Endpoint: "CreateOrder",
		Body:     []byte(`{"plan_id":3,"period":"month"}`),
	}
}

const validKey = "1f0d2c4e-8b3a-4f6d-9c1e-0a7b5d3e2f10"

func TestBeginIdempotentFirstCallExecutes(t *testing.T) {
	db := newFakeIdemStore()

	att, err := BeginIdempotent(context.Background(), db, orderReq(validKey))
	if err != nil {
		t.Fatalf("首次执行不该报错: %v", err)
	}
	if att.Outcome != OutcomeExecute {
		t.Fatalf("Outcome = %v，应为 execute", att.Outcome)
	}
	if att.Key != validKey {
		t.Fatalf("Key = %q", att.Key)
	}
}

func TestBeginIdempotentReplaysCompleted(t *testing.T) {
	db := newFakeIdemStore()
	ctx := context.Background()
	req := orderReq(validKey)

	att, err := BeginIdempotent(ctx, db, req)
	if err != nil || att.Outcome != OutcomeExecute {
		t.Fatalf("首次应执行: %v %v", att, err)
	}
	body := []byte(`{"data":{"order_no":"BP20260817001"}}`)
	if err := CompleteIdempotent(ctx, db, att.Key, http.StatusCreated, body); err != nil {
		t.Fatalf("落盘失败: %v", err)
	}

	// 同键同载荷再来一次 —— 必须重放，不能重新执行。
	again, err := BeginIdempotent(ctx, db, req)
	if err != nil {
		t.Fatalf("重放不该报错: %v", err)
	}
	if again.Outcome != OutcomeReplay {
		t.Fatalf("Outcome = %v，应为 replay", again.Outcome)
	}
	if again.Status != http.StatusCreated {
		t.Fatalf("Status = %d，应为 201", again.Status)
	}
	if string(again.Body) != string(body) {
		t.Fatalf("重放的响应体不一致: %q", again.Body)
	}
}

// 第三态：同键的上一次还没写完结果。既不能执行也无从重放。
func TestBeginIdempotentInProgress(t *testing.T) {
	db := newFakeIdemStore()
	ctx := context.Background()
	req := orderReq(validKey)

	if _, err := BeginIdempotent(ctx, db, req); err != nil {
		t.Fatalf("首次: %v", err)
	}
	_, err := BeginIdempotent(ctx, db, req)
	if !errors.Is(err, ErrIdempotencyInProgress) {
		t.Fatalf("并发同键应返回 in-progress，实得 %v", err)
	}
}

func TestBeginIdempotentMismatchOnDifferentBody(t *testing.T) {
	db := newFakeIdemStore()
	ctx := context.Background()

	if _, err := BeginIdempotent(ctx, db, orderReq(validKey)); err != nil {
		t.Fatalf("首次: %v", err)
	}

	other := orderReq(validKey)
	other.Body = []byte(`{"plan_id":9,"period":"year"}`)
	_, err := BeginIdempotent(ctx, db, other)
	if !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("同键不同载荷应 mismatch（→409），实得 %v", err)
	}
}

func TestBeginIdempotentMismatchOnDifferentEndpoint(t *testing.T) {
	db := newFakeIdemStore()
	ctx := context.Background()

	if _, err := BeginIdempotent(ctx, db, orderReq(validKey)); err != nil {
		t.Fatalf("首次: %v", err)
	}

	other := orderReq(validKey)
	other.Endpoint = "PayOrder"
	if _, err := BeginIdempotent(ctx, db, other); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("同键不同端点应 mismatch，实得 %v", err)
	}
}

// 🔴 归属校验：DDL 的主键是 key 单列，B 用了 A 的键时会命中 A 的行。
// 必须挡住，否则 B 构造相同载荷就能读到 A 的订单号与支付地址。
func TestBeginIdempotentRejectsCrossUserReplay(t *testing.T) {
	db := newFakeIdemStore()
	ctx := context.Background()

	userA := orderReq(validKey)
	att, err := BeginIdempotent(ctx, db, userA)
	if err != nil {
		t.Fatalf("A 首次: %v", err)
	}
	secret := []byte(`{"data":{"order_no":"BP-A-0001","pay_address":"T9yD..."}}`)
	if err := CompleteIdempotent(ctx, db, att.Key, 201, secret); err != nil {
		t.Fatalf("落盘: %v", err)
	}

	// B 拿到 A 的键，并且**载荷完全一样**。
	userB := orderReq(validKey)
	userB.UserID = userPtr(43)
	got, err := BeginIdempotent(ctx, db, userB)
	if !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("跨用户复用应 mismatch，实得 %v (att=%v)", err, got)
	}
	if got != nil {
		t.Fatal("跨用户绝不能拿到 Attempt —— 那里面是别人的响应体")
	}
}

// 匿名（内部任务）场景：UserID 为 nil 的键与带 user 的键互不相认。
func TestBeginIdempotentAnonymousOwnership(t *testing.T) {
	db := newFakeIdemStore()
	ctx := context.Background()

	anon := orderReq(validKey)
	anon.UserID = nil
	if _, err := BeginIdempotent(ctx, db, anon); err != nil {
		t.Fatalf("首次: %v", err)
	}
	if _, err := BeginIdempotent(ctx, db, orderReq(validKey)); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("带 user 的请求不该命中匿名键，实得 %v", err)
	}
	// 匿名对匿名仍然认得出来（走到 in-progress 而不是 mismatch）。
	if _, err := BeginIdempotent(ctx, db, anon); !errors.Is(err, ErrIdempotencyInProgress) {
		t.Fatalf("匿名同键应 in-progress，实得 %v", err)
	}
}

// 主键冲突但按 expires_at 已过期的残留行：必须报明确的 stale，
// 而不是当成首次执行（会重复扣款）或当成 mismatch（会指向错误的排查方向）。
func TestBeginIdempotentStaleKey(t *testing.T) {
	db := newFakeIdemStore()
	ctx := context.Background()

	if _, err := BeginIdempotent(ctx, db, orderReq(validKey)); err != nil {
		t.Fatalf("首次: %v", err)
	}
	db.expired[validKey] = true

	if _, err := BeginIdempotent(ctx, db, orderReq(validKey)); !errors.Is(err, ErrIdempotencyKeyStale) {
		t.Fatalf("过期未清理的残留键应报 stale，实得 %v", err)
	}
}

func TestBeginIdempotentValidatesKeyBeforeDB(t *testing.T) {
	db := newFakeIdemStore()
	req := orderReq("short")

	if _, err := BeginIdempotent(context.Background(), db, req); !errors.Is(err, ErrIdempotencyKeyMalformed) {
		t.Fatalf("应为 malformed，实得 %v", err)
	}
	if db.claims != 0 {
		t.Fatal("键不合法不该查库")
	}
}

func TestFingerprintIsUnambiguous(t *testing.T) {
	// 长度前缀防拼接歧义：endpoint+body 的边界挪动不能撞出同一个指纹。
	a := IdempotentRequest{Endpoint: "ab", Body: []byte("c")}
	b := IdempotentRequest{Endpoint: "a", Body: []byte("bc")}
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("拼接歧义：不同的 (endpoint, body) 撞出了同一指纹")
	}

	// 同一输入必须稳定。
	c := orderReq(validKey)
	if c.Fingerprint() != orderReq(validKey).Fingerprint() {
		t.Fatal("指纹不稳定")
	}

	// user 进指纹。
	d := orderReq(validKey)
	d.UserID = userPtr(43)
	if c.Fingerprint() == d.Fingerprint() {
		t.Fatal("user_id 未参与指纹")
	}

	// nil user 与某个具体 user 也必须不同。
	e := orderReq(validKey)
	e.UserID = nil
	if c.Fingerprint() == e.Fingerprint() {
		t.Fatal("匿名与具名撞了指纹")
	}

	if len(c.Fingerprint()) != 64 {
		t.Fatalf("应为 sha256 的十六进制（64 字符），实得 %d", len(c.Fingerprint()))
	}
}

func TestValidateIdempotencyKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want error
	}{
		{"合法 UUID", validKey, nil},
		{"刚好 8 字符", "12345678", nil},
		{"空", "", ErrIdempotencyKeyMissing},
		{"太短", "1234567", ErrIdempotencyKeyMalformed},
		{"太长", longKey(129), ErrIdempotencyKeyMalformed},
		{"刚好 128 字符", longKey(128), nil},
		{"含换行（日志注入）", "1234567\n8", ErrIdempotencyKeyMalformed},
		{"含空格", "1234 5678", ErrIdempotencyKeyMalformed},
		{"含 NUL", "1234\x00678", ErrIdempotencyKeyMalformed},
		{"非 ASCII", "幂等键幂等键幂等", ErrIdempotencyKeyMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIdempotencyKey(tc.key)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ValidateIdempotencyKey(%q) = %v，应为 %v", tc.key, err, tc.want)
			}
		})
	}
}

func TestReadIdempotencyKey(t *testing.T) {
	t.Run("缺头", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
		if _, err := ReadIdempotencyKey(r); !errors.Is(err, ErrIdempotencyKeyMissing) {
			t.Fatalf("实得 %v", err)
		}
	})
	t.Run("正常", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
		r.Header.Set(IdempotencyKeyHeader, "  "+validKey+"  ")
		got, err := ReadIdempotencyKey(r)
		if err != nil {
			t.Fatalf("实得 %v", err)
		}
		if got != validKey {
			t.Fatalf("应去掉首尾空白，实得 %q", got)
		}
	})
	t.Run("形态非法", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
		r.Header.Set(IdempotencyKeyHeader, "abc")
		if _, err := ReadIdempotencyKey(r); !errors.Is(err, ErrIdempotencyKeyMalformed) {
			t.Fatalf("实得 %v", err)
		}
	})
}

func TestWriteReplay(t *testing.T) {
	t.Run("原样回放", func(t *testing.T) {
		att := &Attempt{Outcome: OutcomeReplay, Status: 201, Body: []byte(`{"data":{"id":1}}`)}
		w := httptest.NewRecorder()
		if err := att.WriteReplay(w); err != nil {
			t.Fatalf("实得 %v", err)
		}
		if w.Code != 201 {
			t.Fatalf("Code = %d", w.Code)
		}
		if w.Body.String() != `{"data":{"id":1}}` {
			t.Fatalf("Body = %q", w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("重放结果不该被缓存")
		}
	})

	t.Run("状态码缺失退回 200", func(t *testing.T) {
		// response_code 是可空列。0 会让 net/http panic，必须有兜底。
		att := &Attempt{Outcome: OutcomeReplay, Status: 0, Body: nil}
		w := httptest.NewRecorder()
		if err := att.WriteReplay(w); err != nil {
			t.Fatalf("实得 %v", err)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("Code = %d，应退回 200", w.Code)
		}
	})
}

func TestOutcomeString(t *testing.T) {
	if OutcomeExecute.String() != "execute" || OutcomeReplay.String() != "replay" {
		t.Fatal("Outcome.String 不对")
	}
	if Outcome(99).String() != "unknown(99)" {
		t.Fatalf("实得 %q", Outcome(99).String())
	}
}

func longKey(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'k'
	}
	return string(b)
}
