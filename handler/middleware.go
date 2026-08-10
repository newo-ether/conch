package handler

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/newo-ether/conch/crypto"
)

// MaxRequestBodyBytes is applied before authentication reads a body. It must accommodate the
// encrypted/base64 envelope of the largest supported plaintext file write while preventing a
// caller with forged auth headers from forcing an unbounded allocation.
const MaxRequestBodyBytes int64 = 4 << 20

func AuthMiddleware(apiKey []byte, nonceTracker *crypto.NonceTracker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)

			// If no API key is configured, still enforce the global request-body limit.
			if len(apiKey) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			// 1. Require X-Signature header (HMAC proves API key possession).
			sigHeader := r.Header.Get("X-Signature")
			if sigHeader == "" {
				writeAuthError(w)
				return
			}

			// 2. Verify timestamp.
			tsStr := r.Header.Get("X-Timestamp")
			ts, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				writeAuthError(w)
				return
			}

			// 3. Read the bounded body for SHA-256, then reset it for the downstream handler.
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) {
					http.Error(w, `{"error":"request body too large"}`, http.StatusRequestEntityTooLarge)
					return
				}
				writeAuthError(w)
				return
			}
			if err := r.Body.Close(); err != nil {
				writeAuthError(w)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			bodySHA256 := crypto.SHA256Hex(bodyBytes)

			// 4. Extract nonce and client public key.
			nonceHMAC := r.Header.Get("X-Nonce")
			clientPubKey := r.Header.Get("X-Client-Public-Key")

			// 5. Verify signature (covers all request fields including client public key).
			if !crypto.Verify(apiKey, tsStr, r.Method, r.URL.Path, bodySHA256, nonceHMAC, clientPubKey, sigHeader) {
				writeAuthError(w)
				return
			}

			// 6. Check nonce for replay.
			if nonceTracker == nil || !nonceTracker.CheckAndRecord(ts, nonceHMAC) {
				writeAuthError(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writeAuthError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}
