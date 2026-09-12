// Package encryptedhttp applies Conch's authenticated channel to an HTTP API.
package encryptedhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/newo-ether/conch/crypto"
)

const RequestPath = "/e2e/request"
const frameBytes = 16 << 10

type Request struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	ContentType string `json:"contentType"`
	Body        []byte `json:"body"`
}

// New creates a fresh, memory-only server key and replay tracker. Recreating a
// gateway never reuses its old key, so replay state cannot be lost independently.
func New(apiKey []byte, application http.Handler) (http.Handler, error) {
	if len(apiKey) == 0 || application == nil {
		return nil, errors.New("encrypted API requires a key and application")
	}
	pair, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	auth := AuthMiddleware(apiKey, crypto.NewNonceTracker())
	protected := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != RequestPath || r.URL.RawQuery != "" || r.Header.Get("X-Encryption") != "v2" {
			http.Error(w, "invalid encrypted request", 400)
			return
		}
		public, err := crypto.DecodePublicKey(r.Header.Get("X-Client-Public-Key"))
		if err != nil {
			http.Error(w, "invalid public key", 400)
			return
		}
		key, err := crypto.DeriveSharedSecret(pair.PrivateKey, public)
		if err != nil {
			http.Error(w, "invalid public key", 400)
			return
		}
		encoded, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid encrypted body", 400)
			return
		}
		plain, err := crypto.Decrypt(key, string(encoded))
		if err != nil {
			http.Error(w, "invalid encrypted body", 400)
			return
		}
		var input Request
		if json.Unmarshal(plain, &input) != nil || len(input.Body) > 512<<10 {
			http.Error(w, "invalid request envelope", 400)
			return
		}
		target, err := url.ParseRequestURI(input.Path)
		if err != nil || target.Host != "" || target.Scheme != "" || !strings.HasPrefix(input.Path, "/v1/") || len(input.Path) > 65536 || len(input.ContentType) > 128 {
			http.Error(w, "invalid application path", 400)
			return
		}
		switch input.Method {
		case "GET", "POST", "PUT", "DELETE":
		default:
			http.Error(w, "invalid application method", 400)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "invalid origin", 403)
			return
		}
		inner := r.Clone(r.Context())
		inner.Method = input.Method
		inner.URL = target
		inner.RequestURI = input.Path
		inner.Header = make(http.Header)
		inner.Header.Set("Authorization", "Bearer "+string(apiKey))
		inner.Header.Set("Content-Type", input.ContentType)
		inner.Body = io.NopCloser(bytes.NewReader(input.Body))
		inner.ContentLength = int64(len(input.Body))
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		stream := &responseWriter{outer: w, key: key, header: make(http.Header)}
		application.ServeHTTP(stream, inner)
		if stream.err == nil {
			if !stream.started {
				stream.WriteHeader(200)
			}
			stream.frame("http_end", map[string]any{})
		}
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/public-key" && r.Method == "GET" {
			challenge := r.URL.Query().Get("challenge")
			if len(challenge) > 64 {
				http.Error(w, "invalid challenge", 400)
				return
			}
			document, err := crypto.NewHandshake(apiKey, pair, challenge)
			if err != nil {
				http.Error(w, "handshake unavailable", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(document)
			return
		}
		protected.ServeHTTP(w, r)
	}), nil
}

type responseWriter struct {
	outer    http.ResponseWriter
	key      []byte
	sequence uint64
	header   http.Header
	started  bool
	err      error
}

func (w *responseWriter) Header() http.Header { return w.header }
func (w *responseWriter) frame(kind string, value any) error {
	if w.err != nil {
		return w.err
	}
	data, err := json.Marshal(value)
	if err == nil {
		var encrypted string
		encrypted, err = crypto.EncryptEvent(w.key, kind, w.sequence, data)
		if err == nil {
			_, err = fmt.Fprintf(w.outer, "event: %s\ndata: %s\n\n", kind, encrypted)
		}
	}
	w.err = err
	if err == nil {
		w.sequence++
		if flush, ok := w.outer.(http.Flusher); ok {
			flush.Flush()
		}
	}
	return err
}
func (w *responseWriter) WriteHeader(status int) {
	if w.started {
		return
	}
	w.started = true
	w.frame("http_headers", map[string]any{"status": status, "contentType": w.header.Get("Content-Type")})
}
func (w *responseWriter) Write(data []byte) (int, error) {
	if !w.started {
		w.WriteHeader(200)
	}
	written := 0
	for len(data) > 0 {
		size := min(len(data), frameBytes)
		if err := w.frame("http_body", map[string]any{"body": data[:size]}); err != nil {
			return written, err
		}
		written += size
		data = data[size:]
	}
	return written, w.err
}
func (w *responseWriter) Flush() {
	if !w.started {
		w.WriteHeader(200)
	}
	if f, ok := w.outer.(http.Flusher); ok {
		f.Flush()
	}
}
