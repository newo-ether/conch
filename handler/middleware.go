package handler

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/newo-ether/conch/crypto"
)

func AuthMiddleware(apiKey []byte, nonceTracker *crypto.NonceTracker) func(http.Handler) http.Handler {
	// If no API key is configured, pass through all requests.
	if len(apiKey) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Require X-Signature header (HMAC proves API key possession)
			sigHeader := r.Header.Get("X-Signature")
			if sigHeader == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}

			// 2. Verify timestamp
			tsStr := r.Header.Get("X-Timestamp")
			ts, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}

			// 3. Read body for SHA-256, then reset for downstream handler
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			bodySHA256 := crypto.SHA256Hex(bodyBytes)

			// 4. Extract nonce and client public key
			nonceHMAC := r.Header.Get("X-Nonce")
			clientPubKey := r.Header.Get("X-Client-Public-Key")

			// 5. Verify signature (covers all request fields including client public key)
			if !crypto.Verify(apiKey, tsStr, r.Method, r.URL.Path, bodySHA256, nonceHMAC, clientPubKey, sigHeader) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}

			// 6. Check nonce for replay
			if !nonceTracker.CheckAndRecord(ts, nonceHMAC) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
