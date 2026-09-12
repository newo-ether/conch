package encryptedhttp

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientPreservesRawBytesAndAuthenticatedErrors(t *testing.T) {
	for _, status := range []int{200, 409, 503} {
		raw := bytes.Repeat([]byte{0, 1, 255, 10}, 50000)
		var count atomic.Int32
		gateway, _ := New(testKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			body, _ := io.ReadAll(r.Body)
			if !bytes.Equal(body, raw) || r.Method != "PUT" || r.URL.RequestURI() != "/v1/upload?offset=7" {
				t.Error("application input changed")
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(status)
			_, _ = w.Write(raw)
		}))
		server := httptest.NewServer(gateway)
		client := &http.Client{Transport: &Transport{Key: testKey}, Timeout: 5 * time.Second}
		request, _ := http.NewRequest("PUT", server.URL+"/v1/upload?offset=7", bytes.NewReader(raw))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := io.ReadAll(response.Body)
		response.Body.Close()
		server.Close()
		if err != nil || response.StatusCode != status || !bytes.Equal(restored, raw) || count.Load() != 1 {
			t.Fatalf("round trip status=%d calls=%d err=%v", response.StatusCode, count.Load(), err)
		}
	}
}

type interceptTransport func(*http.Request) (*http.Response, error)

func (f interceptTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientRejectsMutatedStreamWithoutRetryingMutation(t *testing.T) {
	for _, mutation := range []string{"reorder", "truncate", "kind", "duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			var count atomic.Int32
			gateway, _ := New(testKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				_, _ = w.Write([]byte("private application bytes"))
			}))
			server := httptest.NewServer(gateway)
			defer server.Close()
			base := interceptTransport(func(r *http.Request) (*http.Response, error) {
				response, err := http.DefaultTransport.RoundTrip(r)
				if err != nil || r.URL.Path != RequestPath {
					return response, err
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					return nil, err
				}
				frames := strings.Split(strings.TrimSuffix(string(body), "\n\n"), "\n\n")
				switch mutation {
				case "reorder":
					frames[1], frames[2] = frames[2], frames[1]
				case "truncate":
					frames = frames[:len(frames)-1]
				case "kind":
					frames[1] = strings.Replace(frames[1], "event: http_body", "event: http_end", 1)
				case "duplicate":
					frames = append(frames[:2], frames[1], frames[2])
				}
				response.Body = io.NopCloser(strings.NewReader(strings.Join(frames, "\n\n") + "\n\n"))
				return response, nil
			})
			client := &http.Client{Transport: &Transport{Key: testKey, Base: base}, Timeout: 5 * time.Second}
			request, _ := http.NewRequest("POST", server.URL+"/v1/sessions", strings.NewReader("{}"))
			response, err := client.Do(request)
			if err == nil {
				_, err = io.ReadAll(response.Body)
				response.Body.Close()
			}
			if err == nil || count.Load() != 1 {
				t.Fatalf("tampering accepted or mutation replayed: %v / %d", err, count.Load())
			}
		})
	}
}

func TestClientWrongKeyAndRedirectNeverReachApplication(t *testing.T) {
	var count atomic.Int32
	gateway, _ := New(testKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1) }))
	server := httptest.NewServer(gateway)
	defer server.Close()
	client := &http.Client{Transport: &Transport{Key: []byte("incorrect")}, Timeout: time.Second}
	if _, err := client.Get(server.URL + "/v1/info"); err == nil || count.Load() != 0 {
		t.Fatal("bad proof admitted")
	}
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer redirected.Close()
	client.Transport = &Transport{Key: testKey}
	if _, err := client.Get(redirected.URL + "/v1/info"); err == nil || count.Load() != 0 {
		t.Fatal("redirect admitted")
	}
}
