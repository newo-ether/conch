package handler

import (
	"bytes"
	"crypto/subtle"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/newo-ether/conch/crypto"
)

func AuthMiddleware(apiKey []byte, nonceTracker *crypto.NonceTracker) func(http.Handler) http.Handler {
	// If no API key is configured, pass through all requests.
	if len(apiKey) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Bearer token check (constant-time)
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"missing authorization"}`))
				return
			}
			token := strings.TrimPrefix(header, "Bearer ")
			if subtle.ConstantTimeCompare(apiKey, []byte(token)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"invalid api key"}`))
				return
			}

			// 2. Require X-Signature header
			sigHeader := r.Header.Get("X-Signature")
			if sigHeader == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"missing signature"}`))
				return
			}

			// 3. Verify timestamp
			tsStr := r.Header.Get("X-Timestamp")
			ts, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid timestamp"}`))
				return
			}

			// 4. Read body for SHA-256, then reset for downstream handler
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"failed to read body"}`))
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			bodySHA256 := crypto.SHA256Hex(bodyBytes)

			// 5. Verify HMAC nonce
			nonceHMAC := r.Header.Get("X-Nonce")
			if nonceHMAC == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"missing nonce"}`))
				return
			}

			// 6. Verify signature (covers timestamp, method, path, body hash, and nonce)
			if !crypto.Verify(apiKey, tsStr, r.Method, r.URL.Path, bodySHA256, nonceHMAC, sigHeader) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"invalid signature"}`))
				return
			}

			// 7. Check nonce for replay
			if !nonceTracker.CheckAndRecord(ts, nonceHMAC) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"replayed request"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
