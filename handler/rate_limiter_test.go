package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterKeysByHostWithoutEphemeralPort(t *testing.T) {
	limiter := NewRateLimiter(0, 1)
	t.Cleanup(limiter.Close)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRequest(http.MethodGet, "/health", nil)
	first.RemoteAddr = "192.0.2.10:10001"
	firstRecorder := httptest.NewRecorder()
	handler.ServeHTTP(firstRecorder, first)
	if firstRecorder.Code != http.StatusNoContent {
		t.Fatalf("first status = %d", firstRecorder.Code)
	}

	second := httptest.NewRequest(http.MethodGet, "/health", nil)
	second.RemoteAddr = "192.0.2.10:10002"
	secondRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondRecorder, second)
	if secondRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want rate limited", secondRecorder.Code)
	}
}

func TestRateLimiterIgnoresForwardedForFromUntrustedPeer(t *testing.T) {
	limiter := NewRateLimiter(0, 1)
	t.Cleanup(limiter.Close)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i, forwarded := range []string{"198.51.100.1", "198.51.100.2"} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.RemoteAddr = "192.0.2.20:40000"
		req.Header.Set("X-Forwarded-For", forwarded)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		want := http.StatusNoContent
		if i == 1 {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want {
			t.Fatalf("request %d status = %d, want %d", i, recorder.Code, want)
		}
	}
}

func TestRateLimiterBoundsVisitorCardinality(t *testing.T) {
	limiter := NewRateLimiter(1, 1)
	t.Cleanup(limiter.Close)
	now := time.Now()
	for i := 0; i < maxRateLimitVisitors; i++ {
		limiter.visitors[fmt.Sprintf("192.0.2.%d", i)] = &tokenBucket{
			tokens:    1,
			lastCheck: now,
		}
	}
	if limiter.Allow("198.51.100.1") {
		t.Fatal("accepted a new visitor after reaching the memory bound")
	}
	if len(limiter.visitors) != maxRateLimitVisitors {
		t.Fatalf("visitor count = %d, want %d", len(limiter.visitors), maxRateLimitVisitors)
	}
}

func TestRateLimiterUsesFirstUntrustedHopBehindTrustedProxy(t *testing.T) {
	limiter := NewRateLimiter(0, 1, "10.0.0.0/8")
	t.Cleanup(limiter.Close)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "10.0.0.2:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if got := limiter.clientIP(req); got != "203.0.113.9" {
		t.Fatalf("client IP = %q, want 203.0.113.9", got)
	}
}
