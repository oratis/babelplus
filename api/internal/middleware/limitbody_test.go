package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 这条钉的是「超限时读不下去」，而不是「读完之后再判断长度」——
// 后者仍然会把整个报文分配到内存里，正是这道闸要挡的东西。
func TestLimitBodyStopsReadingAtCap(t *testing.T) {
	var readErr error
	var readN int
	h := LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		readN, readErr = int(n), err
	}))

	body := strings.NewReader(strings.Repeat("a", int(MaxBodyBytes)+4096))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", body)
	h.ServeHTTP(httptest.NewRecorder(), r)

	if readErr == nil {
		t.Fatalf("超过 %d 字节仍然读完了（读了 %d），MaxBytesReader 没生效", MaxBodyBytes, readN)
	}
	if int64(readN) > MaxBodyBytes {
		t.Errorf("读进来 %d 字节，超过上限 %d —— 说明是读完才判断的", readN, MaxBodyBytes)
	}
}

func TestLimitBodyPassesThroughUnderCap(t *testing.T) {
	const payload = `{"email":"a@b.c","password":"correct horse battery staple"}`
	var got string
	h := LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("正常大小的请求体不该报错：%v", err)
		}
		got = string(b)
	}))

	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(payload))
	h.ServeHTTP(httptest.NewRecorder(), r)

	if got != payload {
		t.Errorf("请求体被改动了：%q", got)
	}
}

// GET 之类没有 Body 的请求不能因为这道闸崩掉。
func TestLimitBodyHandlesNilBody(t *testing.T) {
	called := false
	h := LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	r := httptest.NewRequest(http.MethodGet, "/-/healthz", nil)
	r.Body = nil
	h.ServeHTTP(httptest.NewRecorder(), r)

	if !called {
		t.Error("Body 为 nil 时没有把请求传下去")
	}
}
