package encryptedhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/conch/crypto"
)

func bodyFrame(t *testing.T, key []byte, kind string, sequence uint64) string {
	t.Helper()
	value, err := json.Marshal(map[string]any{"body": []byte("authenticated prefix")})
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := crypto.EncryptEvent(key, kind, sequence, value)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", kind, encrypted)
}

func TestResponseBodyRejectsPostTerminalBytes(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	terminal := bodyFrame(t, key, "http_end", 0)
	for name, suffix := range map[string]string{
		"ordered authenticated body": bodyFrame(t, key, "http_body", 1),
		"ordered authenticated end":  bodyFrame(t, key, "http_end", 1),
		"replayed end":               terminal,
		"garbage":                    "garbage",
		"blank line":                 "\n",
	} {
		t.Run(name, func(t *testing.T) {
			body := newResponseBody(io.NopCloser(strings.NewReader(terminal+suffix)), key)
			defer body.Close()
			for range 2 {
				if n, err := body.Read(make([]byte, 1)); n != 0 || err == nil || err == io.EOF {
					t.Fatalf("post-terminal bytes accepted: n=%d err=%v", n, err)
				}
			}
		})
	}
}

func TestResponseBodyRejectsCrossRequestFrame(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	otherKey := bytes.Repeat([]byte{8}, 32)
	wire := bodyFrame(t, otherKey, "http_body", 0) + bodyFrame(t, key, "http_end", 1)
	body := newResponseBody(io.NopCloser(strings.NewReader(wire)), key)
	defer body.Close()
	if raw, err := io.ReadAll(body); err == nil || len(raw) != 0 {
		t.Fatalf("cross-request frame accepted: %q, %v", raw, err)
	}
}

func TestResponseBodyRejectsDelayedTrailingByte(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	body := newResponseBody(reader, key)
	result := make(chan error, 1)
	go func() { _, err := io.ReadAll(body); result <- err }()
	if _, err := io.WriteString(writer, bodyFrame(t, key, "http_end", 0)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		t.Fatalf("terminal accepted before delayed byte: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := io.WriteString(writer, "x"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("delayed trailing byte accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("trailing byte was not rejected promptly")
	}
}

func TestResponseBodyEmptyAndMissingTerminal(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	for _, wire := range []string{"", bodyFrame(t, key, "http_end", 0)} {
		body := newResponseBody(io.NopCloser(strings.NewReader(wire)), key)
		if n, err := body.Read(nil); n != 0 || err != nil {
			t.Fatalf("empty read: %d, %v", n, err)
		}
		_, err := io.ReadAll(body)
		body.Close()
		if (wire == "") != (err != nil) {
			t.Fatalf("wire length %d: %v", len(wire), err)
		}
	}
}

func TestResponseBodyWaitsForDelayedOuterCompletionAndCanClose(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancel), func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			body := newResponseBody(reader, key)
			result := make(chan error, 1)
			go func() { _, err := io.ReadAll(body); result <- err }()
			if _, err := io.WriteString(writer, bodyFrame(t, key, "http_end", 0)); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				t.Fatalf("terminal accepted before outer completion: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			if cancel {
				body.Close()
			} else {
				writer.Close()
			}
			select {
			case err := <-result:
				if cancel != (err != nil) {
					t.Fatalf("cancel=%v: %v", cancel, err)
				}
			case <-time.After(time.Second):
				t.Fatal("outer completion or close did not unblock read")
			}
		})
	}
}

func TestClientTerminalRequiresHTTPBodyCompletionAndSupportsReuse(t *testing.T) {
	gateway, err := New(testKey, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gateway)
	defer server.Close()
	base := &http.Transport{}
	defer base.CloseIdleConnections()
	client := &http.Client{Transport: &Transport{Key: testKey, Base: base}, Timeout: time.Second}
	for index := range 2 {
		var reused bool
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}
		request, _ := http.NewRequest("GET", server.URL+"/v1/empty", nil)
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || len(raw) != 0 || (index > 0 && !reused) {
			t.Fatalf("empty response/reuse: raw=%q reused=%v err=%v", raw, reused, err)
		}
	}
}

func TestClientPartialOuterBodyCancellationDoesNotReplay(t *testing.T) {
	gateway, err := New(testKey, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.ServeHTTP(w, r)
		if r.URL.Path == RequestPath {
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: &Transport{Key: testKey}}
	request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/empty", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result := make(chan error, 1)
	go func() { _, err := io.ReadAll(response.Body); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("accepted terminal before HTTP body completed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not release response read")
	}
}
