package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oratis/babelplus/api/internal/config"
	"github.com/oratis/babelplus/api/internal/handler"
	"github.com/oratis/babelplus/api/internal/store"
)

// testRouter 用真实的 buildRouter 装配，只把 handler 换成全 501 的 Unimplemented。
//
// 刻意走真装配而不是直接调 requestErrorHandler：本组用例要挡的**就是装配漏挂**
// （HandlerFromMux 不接受 ErrorHandlerFunc，参数绑定失败因此落到生成代码的
// http.Error 默认实现）。单独测那个函数抓不到这一类问题。
//
// db 传一个零值 *store.Store（Pool 为 nil）：buildRouter 会读 db.Pool 去装配
// 管理面的 PgAdminStore，所以不能再传 nil 指针。零值是安全的 ——
// 本组用例走到的三条路径（参数绑定失败、节点面缺 token、用户面缺 token）
// 都在触碰数据库之前就返回了。
//
// cfg 里刻意不给 AdminIAPAudience / InternalOIDCAudience：那正是生产之外的默认形态，
// 而它意味着管理面与内部面**整体拒绝**（fail-closed）。
func testRouter(t *testing.T) http.Handler {
	t.Helper()
	cfg := &config.Config{
		Env:            "test",
		AllowedOrigins: []string{"https://web.babel.plus"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return buildRouter(cfg, &store.Store{}, logger, handler.Unimplemented{})
}

// 参数绑定失败以前返回 text/plain 400（生成代码的默认 ErrorHandlerFunc），
// 既没有 error.code 也不是 JSON —— 而前端是按 error.code 做分支的。
func TestParamBindErrorReturnsEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	// limit 在契约里是 integer，abc 必然绑定失败。这条路径不需要任何凭据。
	testRouter(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders?limit=abc", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400；body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q，期望 application/json（原来是 text/plain）", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q，期望 no-store", cc)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v；body=%s", err, rec.Body.String())
	}
	if body.Error.Code == "" {
		t.Errorf("error.code 为空 —— 前端会落到「未知错误」兜底分支；body=%s", rec.Body.String())
	}
}

// 节点面的错误必须是**裸 JSON**（openapi 的 NodeError），不套信封。
// api-contract §2.2 的例外表把整个 /api/v1/server/UniProxy/* 列为裸 JSON，
// 没有「错误响应除外」这一条。
func TestNodeFaceErrorIsBareNotEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	// 不带 token → 鉴权中间件返 401。
	testRouter(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/server/UniProxy/user?node_type=v2node&node_id=1", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401；body=%s", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("响应不是合法 JSON: %v；body=%s", err, rec.Body.String())
	}
	if _, wrapped := raw["error"]; wrapped {
		t.Fatalf("节点面错误被套进了信封，违反冻结契约；body=%s", rec.Body.String())
	}

	var bare struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bare); err != nil {
		t.Fatalf("裸形状解析失败: %v", err)
	}
	if bare.Code == "" || bare.Message == "" {
		t.Errorf("裸 NodeError 的 code/message 不该为空；body=%s", rec.Body.String())
	}
}

// 用户面保持信封 —— 上面那条改动不能把用户面也一起改成裸的。
func TestUserFaceErrorStaysEnveloped(t *testing.T) {
	rec := httptest.NewRecorder()
	// 不带 Authorization → 用户鉴权中间件返 401。用一条参数合法的用户面 GET，
	// 这样走到的是鉴权而不是参数绑定。
	testRouter(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders?limit=10", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401；body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("响应不是合法 JSON: %v；body=%s", err, rec.Body.String())
	}
	if _, wrapped := raw["error"]; !wrapped {
		t.Errorf("用户面错误应当套信封；body=%s", rec.Body.String())
	}
}
