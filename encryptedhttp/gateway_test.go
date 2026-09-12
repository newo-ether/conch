package encryptedhttp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/conch/crypto"
)

var testKey = []byte(strings.Repeat("a", 64))

func request(t *testing.T, gateway http.Handler, input Request) (*http.Request, []byte) {
	t.Helper()
	handshake := httptest.NewRecorder()
	gateway.ServeHTTP(handshake, httptest.NewRequest("GET", "/public-key?challenge=fixture", nil))
	var server crypto.Handshake
	if err := json.Unmarshal(handshake.Body.Bytes(), &server); err != nil {
		t.Fatal(err)
	}
	if !crypto.VerifyHandshakeSecurity(testKey, server, "fixture") {
		t.Fatal("unauthenticated handshake")
	}
	client, _ := crypto.GenerateKeyPair()
	public, _ := crypto.DecodePublicKey(server.PublicKey)
	key, _ := crypto.DeriveSharedSecret(client.PrivateKey, public)
	plain, _ := json.Marshal(input)
	encoded, _ := crypto.Encrypt(key, plain)
	r := httptest.NewRequest("POST", RequestPath, strings.NewReader(encoded))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, _ := crypto.GenerateNonce()
	r.Header.Set("X-Encryption", "v2")
	r.Header.Set("X-Timestamp", timestamp)
	r.Header.Set("X-Nonce", nonce)
	r.Header.Set("X-Client-Public-Key", client.PublicKeyBase64())
	r.Header.Set("X-Signature", crypto.Sign(testKey, timestamp, "POST", RequestPath, crypto.SHA256Hex([]byte(encoded)), nonce, client.PublicKeyBase64()))
	return r, key
}

func TestBinaryAndApplicationErrorsUseTheSameAuthenticatedStream(t *testing.T) {
	for _, status := range []int{200, 409, 502} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			raw := bytes.Repeat([]byte("private\x00binary\xff"), 5000)
			gateway, err := New(testKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PUT" || r.URL.RequestURI() != "/v1/uploads/fixture?offset=2" || r.Header.Get("Authorization") != "Bearer "+string(testKey) {
					t.Fatal("request changed")
				}
				input, _ := io.ReadAll(r.Body)
				if !bytes.Equal(input, raw) {
					t.Fatal("input bytes changed")
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(status)
				w.Write(raw)
			}))
			if err != nil {
				t.Fatal(err)
			}
			r, key := request(t, gateway, Request{Method: "PUT", Path: "/v1/uploads/fixture?offset=2", ContentType: "application/octet-stream", Body: raw})
			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, r)
			if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "application/octet-stream") {
				t.Fatal("plaintext response escaped")
			}
			var restored []byte
			terminal := false
			for i, frame := range strings.Split(strings.TrimSpace(w.Body.String()), "\n\n") {
				lines := strings.Split(frame, "\n")
				kind := strings.TrimPrefix(lines[0], "event: ")
				data, err := crypto.DecryptEvent(key, kind, uint64(i), strings.TrimPrefix(lines[1], "data: "))
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "http_headers":
					var headers struct {
						Status int `json:"status"`
					}
					json.Unmarshal(data, &headers)
					if i != 0 || headers.Status != status {
						t.Fatal("status lost")
					}
				case "http_body":
					var value struct {
						Body []byte `json:"body"`
					}
					json.Unmarshal(data, &value)
					restored = append(restored, value.Body...)
				case "http_end":
					terminal = true
				default:
					t.Fatal("unexpected frame")
				}
			}
			if !terminal || !bytes.Equal(raw, restored) {
				t.Fatal("incomplete or changed binary response")
			}
		})
	}
}

func TestReplayAndRestartNeverExecuteTheSameCiphertextAgain(t *testing.T) {
	executions := 0
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { executions++; w.Write([]byte("accepted")) })
	gateway, _ := New(testKey, app)
	r, _ := request(t, gateway, Request{Method: "POST", Path: "/v1/sessions"})
	body, _ := io.ReadAll(r.Body)
	replay := func(h http.Handler) int {
		copy := r.Clone(r.Context())
		copy.Body = io.NopCloser(bytes.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, copy)
		return w.Code
	}
	if replay(gateway) != 200 || executions != 1 {
		t.Fatal("first execution failed")
	}
	if replay(gateway) != 401 || executions != 1 {
		t.Fatal("same-generation replay accepted")
	}
	restarted, _ := New(testKey, app)
	if replay(restarted) != 400 || executions != 1 {
		t.Fatal("restart replay executed")
	}
}

func TestRawMutationFailsBeforeApplication(t *testing.T) {
	executions := 0
	gateway, _ := New(testKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { executions++ }))
	r, _ := request(t, gateway, Request{Method: "POST", Path: "/v1/sessions"})
	body, _ := io.ReadAll(r.Body)
	body[len(body)/2] ^= 1
	r.Body = io.NopCloser(bytes.NewReader(body))
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, r)
	if w.Code != 401 || executions != 0 {
		t.Fatal("tampered body entered application")
	}
}
