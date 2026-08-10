package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/conch/crypto"
)

func TestAuthMiddlewareRejectsOversizedBodyBeforeAllocation(t *testing.T) {
	apiKey := []byte("test-key")
	body := strings.Repeat("x", int(MaxRequestBodyBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(body))
	req.Header.Set("X-Signature", "not-a-valid-signature")
	req.Header.Set("X-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))

	nextCalled := false
	handler := AuthMiddleware(apiKey, crypto.NewNonceTracker())(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			nextCalled = true
		},
	))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if nextCalled {
		t.Fatal("oversized request reached downstream handler")
	}
}

func TestAuthMiddlewareLimitsBodyWhenAuthenticationIsDisabled(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodPost,
		"/file/write",
		strings.NewReader(strings.Repeat("x", int(MaxRequestBodyBytes)+1)),
	)
	handler := AuthMiddleware(nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err == nil {
			t.Fatal("downstream read unexpectedly accepted oversized body")
		}
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
}
