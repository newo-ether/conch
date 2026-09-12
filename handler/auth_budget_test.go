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

type heldBody struct {
	entered chan<- struct{}
	release <-chan struct{}
	reads   atomic.Int32
}

func (b *heldBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	b.entered <- struct{}{}
	<-b.release
	return 0, io.EOF
}
func (b *heldBody) Close() error { return nil }

func signedBudgetRequest(key []byte, path, nonce string) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader("body"))
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	r.Header.Set("X-Timestamp", ts)
	r.Header.Set("X-Nonce", nonce)
	r.Header.Set("X-Client-Public-Key", "fixture-key")
	r.Header.Set("X-Signature", crypto.Sign(key, ts, r.Method, r.URL.RequestURI(), crypto.SHA256Hex([]byte("body")), nonce, "fixture-key"))
	return r
}

func TestAuthenticationReadBudgetSharedAcrossRoutesAndRecovered(t *testing.T) {
	key := []byte("budget-fixture")
	auth := AuthMiddleware(key, crypto.NewNonceTracker())
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	first, second := auth(next), auth(next)
	entered, release := make(chan struct{}, maxAuthenticatingRequests), make(chan struct{})
	var workers sync.WaitGroup
	for index := range maxAuthenticatingRequests {
		workers.Go(func() {
			r := signedBudgetRequest(key, "/file/read", strconv.Itoa(index))
			r.Body = &heldBody{entered: entered, release: release}
			first.ServeHTTP(httptest.NewRecorder(), r)
		})
	}
	for range maxAuthenticatingRequests {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("read did not start")
		}
	}
	r := signedBudgetRequest(key, "/execute", "overflow")
	held := &heldBody{entered: entered, release: release}
	r.Body = held
	rejected := httptest.NewRecorder()
	second.ServeHTTP(rejected, r)
	close(release)
	workers.Wait()
	if rejected.Code != 429 || held.reads.Load() != 0 {
		t.Fatal("excess request allocated a body", rejected.Code)
	}
	accepted := httptest.NewRecorder()
	second.ServeHTTP(accepted, signedBudgetRequest(key, "/execute", "after"))
	if accepted.Code != 204 {
		t.Fatal("read budget leaked", accepted.Code)
	}
}

func TestAuthenticationBindsExactEscapedPathAndQuery(t *testing.T) {
	key := []byte("query-binding-fixture")
	auth := AuthMiddleware(key, crypto.NewNonceTracker())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, changed := range []bool{false, true} {
		r := signedBudgetRequest(key, "/v1/payloads?revision=a%2Fb&index=1", strconv.FormatBool(changed))
		if changed {
			r.URL.RawQuery = "revision=a%2Fb&index=2"
		}
		response := httptest.NewRecorder()
		auth.ServeHTTP(response, r)
		expected := 204
		if changed {
			expected = 401
		}
		if response.Code != expected {
			t.Fatal("query binding failed", changed, response.Code)
		}
	}
}

func TestExpiredAuthenticationDoesNotReadBody(t *testing.T) {
	key := []byte("expired-fixture")
	entered, release := make(chan struct{}, 1), make(chan struct{})
	close(release)
	body := &heldBody{entered: entered, release: release}
	r := signedBudgetRequest(key, "/execute", "expired")
	r.Header.Set("X-Timestamp", "1")
	r.Body = body
	response := httptest.NewRecorder()
	AuthMiddleware(key, crypto.NewNonceTracker())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("expired request dispatched") })).ServeHTTP(response, r)
	if response.Code != 401 || body.reads.Load() != 0 {
		t.Fatal("expired request read a body")
	}
}
