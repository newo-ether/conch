package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/newo-ether/conch/crypto"
)

func TestRawRequestMutationNeverReachesDispatch(t *testing.T) {
	key := []byte("isolated-raw-request-fixture")
	for _, changed := range []string{"none", "method", "path", "body", "nonce", "clientKey", "timestamp", "signature", "expired"} {
		t.Run(changed, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader("ciphertext"))
			timestamp := strconv.FormatInt(time.Now().Unix(), 10)
			if changed == "expired" {
				timestamp = strconv.FormatInt(time.Now().Unix()-301, 10)
			}
			request.Header.Set("X-Timestamp", timestamp)
			request.Header.Set("X-Nonce", "request-nonce")
			request.Header.Set("X-Client-Public-Key", "ephemeral-key")
			request.Header.Set("X-Signature", crypto.Sign(key, timestamp, "POST", "/execute",
				crypto.SHA256Hex([]byte("ciphertext")), "request-nonce", "ephemeral-key"))
			switch changed {
			case "method":
				request.Method = "DELETE"
			case "path":
				request.URL.Path = "/file/write"
			case "body":
				request.Body = io.NopCloser(strings.NewReader("other ciphertext"))
			case "nonce":
				request.Header.Set("X-Nonce", "other-nonce")
			case "clientKey":
				request.Header.Set("X-Client-Public-Key", "other-ephemeral-key")
			case "timestamp":
				request.Header.Set("X-Timestamp", strconv.FormatInt(time.Now().Unix()+1, 10))
			case "signature":
				request.Header.Set("X-Signature", strings.Repeat("0", 64))
			}
			calls := 0
			auth := AuthMiddleware(key, crypto.NewNonceTracker())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			auth.ServeHTTP(response, request)
			if changed == "none" {
				if calls != 1 || response.Code != 204 {
					t.Fatal("valid request rejected")
				}
			} else if calls != 0 || response.Code != 401 {
				t.Fatal("mutated request reached dispatch")
			}
		})
	}
}

func TestConcurrentCapturedRequestDispatchesExactlyOnce(t *testing.T) {
	key := []byte("isolated-concurrent-replay-fixture")
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature := crypto.Sign(key, timestamp, "POST", "/execute", crypto.SHA256Hex([]byte("body")), "one-nonce", "one-key")
	var calls atomic.Int32
	auth := AuthMiddleware(key, crypto.NewNonceTracker())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(204)
	}))
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			request := httptest.NewRequest("POST", "/execute", strings.NewReader("body"))
			request.Header.Set("X-Timestamp", timestamp)
			request.Header.Set("X-Nonce", "one-nonce")
			request.Header.Set("X-Client-Public-Key", "one-key")
			request.Header.Set("X-Signature", signature)
			auth.ServeHTTP(httptest.NewRecorder(), request)
		})
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("captured request dispatched %d times", calls.Load())
	}
}
