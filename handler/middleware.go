package handler

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func AuthMiddleware(apiKey string) func(http.Handler) http.Handler {
	if apiKey == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"missing authorization"}`))
				return
			}
			token := strings.TrimPrefix(header, "Bearer ")
			if subtle.ConstantTimeCompare([]byte(apiKey), []byte(token)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"invalid api key"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
