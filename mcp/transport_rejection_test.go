package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/newo-ether/conch/crypto"
)

func TestRetryProofBindsRequestStatusAndExactResponseBytes(t *testing.T) {
	key := []byte("isolated-proof-fixture")
	body := []byte(`{"error":"decryption failed"}`)
	for _, mutation := range []string{"none", "request", "body", "status", "signature", "missing"} {
		t.Run(mutation, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/execute", nil)
			request.Header.Set("X-Signature", "original-request-hmac")
			response := &http.Response{StatusCode: 400, Header: make(http.Header)}
			response.Header.Set(crypto.RejectionSignatureHeader, crypto.SignPayload(key,
				request.Header.Get("X-Signature"), crypto.RejectionPayload(400, body)))
			actualBody := body
			switch mutation {
			case "request":
				request.Header.Set("X-Signature", "another-request-hmac")
			case "body":
				actualBody = append(append([]byte{}, body...), ' ')
			case "status":
				response.StatusCode = 500
			case "signature":
				response.Header.Set(crypto.RejectionSignatureHeader, strings.Repeat("0", 64))
			case "missing":
				response.Header.Del(crypto.RejectionSignatureHeader)
			}
			err := authenticatedHTTPError(key, request, response, actualBody)
			if got := isStaleServerKeyError(err); got != (mutation == "none") {
				t.Fatalf("retry authorization for %s = %v", mutation, got)
			}
		})
	}
}

func TestRedirectCannotForwardAuthenticatedMutation(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+"/execute")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	transport := NewTransport(origin.URL, "isolated-fixture-key")
	for _, client := range []*http.Client{transport.client, transport.executeClient} {
		request, _ := http.NewRequest(http.MethodPost, origin.URL+"/execute", strings.NewReader("ciphertext"))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusTemporaryRedirect {
			t.Fatal("redirect was followed")
		}
	}
	if followed.Load() != 0 {
		t.Fatal("mutation forwarded to redirected endpoint")
	}
}
