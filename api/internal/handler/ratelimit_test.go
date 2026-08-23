package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/oratis/babelplus/api/internal/gen"
)

// 账户面限流的接线测试。
//
// 这里测不到「计数对不对」—— 那在 internal/ratelimit 里，因为 Server.db 是具体类型
// *store.Store，塞不了假实现。这里能钉住、也必须钉住的是**契约层面的形状**：
// 超限时客户端到底收到了什么。
//
// 为什么这值得单独测：`Retry-After` 是唯一一个「客户端会照着做」的响应头。
// 它错了不会有任何报错 —— 客户端只是退避得太早（再吃一个 429）或太晚（用户干等），
// 而两种表现都会被归因成「服务不稳定」。

// ============================================================
// Retry-After 的取整
// ============================================================

// TestRetryAfterSecondsRoundsUp 钉住「向上取整、下限 1 秒」。
//
// 向下取整是这里最容易写出来的 bug（int32(d.Seconds()) 一行就够），
// 而它的后果是**每一个守规矩的客户端都会在窗口结束前一刻重试**并再吃一个 429。
func TestRetryAfterSecondsRoundsUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want int32
	}{
		{"整秒原样", 60 * time.Second, 60},
		{"有余数则进位（59.001s → 60）", 59*time.Second + time.Millisecond, 60},
		{"不足一秒也报 1", time.Millisecond, 1},
		{"零", 0, 1},
		{"负数（时钟异常）", -time.Hour, 1},
		{"整小时", time.Hour, 3600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryAfterSeconds(tc.in); got != tc.want {
				t.Fatalf("retryAfterSeconds(%v) = %d，期望 %d", tc.in, got, tc.want)
			}
		})
	}
}

// ============================================================
// 429 的线上形状
// ============================================================

// TestRateLimitedResponseShape 走一遍**生成代码真正写响应的那条路径**，
// 断言四个账户面端点的 429 都带 Retry-After、状态码是 429、
// 且 error.code 落在 openapi 的 ErrorCode enum 里。
//
// 最后一条不是形式主义：这个仓库出过「六个错误码不在 enum 里」的事故，
// 现象是前端按 code 分支时全部落到兜底文案 —— 用户看到「未知错误」而不是
// 「太频繁了，60 秒后再试」，于是继续点，于是继续 429。
func TestRateLimitedResponseShape(t *testing.T) {
	srv := &Server{}
	ctx := context.Background()
	const retry int32 = 47

	for _, tc := range []struct {
		name  string
		visit func(w http.ResponseWriter) error
	}{
		{"login", func(w http.ResponseWriter) error {
			return gen.Login429JSONResponse{ErrRateLimitedJSONResponse: srv.rateLimited(ctx, "登录尝试过于频繁，请稍后再试", retry)}.
				VisitLoginResponse(w)
		}},
		{"email-code", func(w http.ResponseWriter) error {
			return gen.SendEmailCode429JSONResponse{ErrRateLimitedJSONResponse: srv.rateLimited(ctx, "获取验证码过于频繁，请稍后再试", retry)}.
				VisitSendEmailCodeResponse(w)
		}},
		{"password/forgot", func(w http.ResponseWriter) error {
			return gen.ForgotPassword429JSONResponse{ErrRateLimitedJSONResponse: srv.rateLimited(ctx, "操作过于频繁，请稍后再试", retry)}.
				VisitForgotPasswordResponse(w)
		}},
		{"invite/verify", func(w http.ResponseWriter) error {
			return gen.VerifyInviteCode429JSONResponse{ErrRateLimitedJSONResponse: srv.rateLimited(ctx, "校验过于频繁，请稍后再试", retry)}.
				VisitVerifyInviteCodeResponse(w)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := tc.visit(rec); err != nil {
				t.Fatalf("写响应失败: %v", err)
			}
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("状态码 = %d，期望 429", rec.Code)
			}
			if got := rec.Header().Get("Retry-After"); got != "47" {
				t.Fatalf("Retry-After = %q，期望 \"47\"（契约把它标为 429 上**必带**的头）", got)
			}

			var env struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("响应体不是 JSON 信封: %v（body=%s）", err, rec.Body.String())
			}
			if env.Error.Code != string(gen.QUOTARATELIMITED) {
				t.Fatalf("error.code = %q，期望 %q", env.Error.Code, gen.QUOTARATELIMITED)
			}
			if env.Error.Message == "" {
				t.Fatalf("error.message 为空 —— 前端没有可展示的文案")
			}
		})
	}
}

// ============================================================
// 维度取值
// ============================================================

// TestRateSubjectIP 钉住「采集不到 IP 时返回空串」。
//
// 空串会让 checkRateRules **跳过**这条规则。反过来的实现（回退成 "unknown"
// 之类的固定串）会把所有采集不到 IP 的请求算成同一个人 ——
// 第一个触顶的人把所有人一起锁在门外，而这批人恰恰是我们最看不清的那批。
func TestRateSubjectIP(t *testing.T) {
	if got := rateSubjectIP(RequestMetadata{}); got != "" {
		t.Fatalf("没有 IP 时应返回空串（跳过该维度），得到 %q", got)
	}

	ip := netip.MustParseAddr("203.0.113.7")
	if got := rateSubjectIP(RequestMetadata{IP: &ip}); got != "203.0.113.7" {
		t.Fatalf("subject = %q，期望 %q", got, "203.0.113.7")
	}

	// requestMetadata 会把 ::ffff:1.2.3.4 Unmap 成 1.2.3.4。这里确认同一个地址的
	// 两种写法不会变成两个 subject（否则同一个人有两份配额）。
	v4 := netip.MustParseAddr("1.2.3.4")
	mapped := netip.MustParseAddr("::ffff:1.2.3.4").Unmap()
	if rateSubjectIP(RequestMetadata{IP: &v4}) != rateSubjectIP(RequestMetadata{IP: &mapped}) {
		t.Fatalf("IPv4 与它的 IPv4-mapped 形态必须是同一个 subject")
	}
}

// TestRateBucketsAreDistinct 钉住「窗口长度编进了桶名」。
//
// 两条限额共用一个桶的后果是它们互相覆盖对方的 window_start：
// 5/min 与 10/h 都还在，但哪一条都不准 —— 而且只在两条同时接近触顶时才显形。
func TestRateBucketsAreDistinct(t *testing.T) {
	buckets := []string{
		bucketLoginIPMinute, bucketLoginIPHour,
		bucketLoginEmailMinute, bucketLoginEmailHour,
		bucketEmailCodeIPHour, bucketForgotIPHour, bucketInviteIPMinute,
	}
	seen := make(map[string]bool, len(buckets))
	for _, b := range buckets {
		if b == "" {
			t.Fatalf("桶名不能为空")
		}
		if seen[b] {
			t.Fatalf("桶名 %q 重复 —— 两条限额会互相覆盖窗口", b)
		}
		seen[b] = true
	}
}
