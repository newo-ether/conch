package encryptedhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type gatewayFlushWriter struct {
	*httptest.ResponseRecorder
	failAt  int
	flushes int
}

func (w *gatewayFlushWriter) FlushError() error {
	w.flushes++
	if w.flushes == w.failAt {
		return io.ErrClosedPipe
	}
	return nil
}

func (w *gatewayFlushWriter) Flush() { _ = w.FlushError() }

type gatewayWriterWrapper struct{ writer http.ResponseWriter }

func (w gatewayWriterWrapper) Header() http.Header         { return w.writer.Header() }
func (w gatewayWriterWrapper) WriteHeader(status int)      { w.writer.WriteHeader(status) }
func (w gatewayWriterWrapper) Write(b []byte) (int, error) { return w.writer.Write(b) }
func (w gatewayWriterWrapper) Unwrap() http.ResponseWriter { return w.writer }

func TestGatewayFlushFailureReleasesStreamingApplication(t *testing.T) {
	for _, phase := range []string{"headers", "body", "explicit"} {
		t.Run(phase, func(t *testing.T) {
			outer := &gatewayFlushWriter{ResponseRecorder: httptest.NewRecorder(), failAt: 1}
			if phase != "headers" {
				outer.failAt = 2
			}
			failure := make(chan error, 1)
			released := make(chan struct{})
			gateway, err := New(testKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(released)
				w.WriteHeader(http.StatusOK)
				var err error
				if phase == "body" {
					_, err = w.Write([]byte("first"))
				} else {
					err = http.NewResponseController(w).Flush()
				}
				failure <- err
				if err == nil {
					<-r.Context().Done()
					return
				}
				if err := http.NewResponseController(w).Flush(); !errors.Is(err, io.ErrClosedPipe) {
					t.Errorf("flush lost original failure: %v", err)
				}
				if n, err := w.Write([]byte("after failure")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
					t.Errorf("write after flush failure = %d, %v", n, err)
				}
				w.(http.Flusher).Flush()
			}))
			if err != nil {
				t.Fatal(err)
			}
			r, _ := request(t, gateway, Request{Method: "GET", Path: "/v1/events"})
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				gateway.ServeHTTP(gatewayWriterWrapper{outer}, r.WithContext(ctx))
			}()
			select {
			case err := <-failure:
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Errorf("encrypted stream discarded flush failure: %v", err)
					cancel()
				}
			case <-time.After(time.Second):
				t.Error("encrypted stream did not report flush failure")
				cancel()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("failed stream retained the application")
			}
			select {
			case <-released:
			default:
				t.Fatal("failed stream retained its application lease")
			}
			if outer.flushes != outer.failAt {
				t.Errorf("flush retried after failure: %d calls, failure at %d", outer.flushes, outer.failAt)
			}
			if strings.Contains(outer.Body.String(), "event: http_end") || strings.Count(outer.Body.String(), "event: http_body") > 1 {
				t.Fatalf("failed stream emitted a later frame: %q", outer.Body.String())
			}
		})
	}
}

type gatewayNonFlushingWriter struct{ recorder *httptest.ResponseRecorder }

func (w gatewayNonFlushingWriter) Header() http.Header         { return w.recorder.Header() }
func (w gatewayNonFlushingWriter) WriteHeader(status int)      { w.recorder.WriteHeader(status) }
func (w gatewayNonFlushingWriter) Write(b []byte) (int, error) { return w.recorder.Write(b) }

func TestGatewayUnsupportedFlushStillCompletes(t *testing.T) {
	gateway, err := New(testKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("body")); err != nil {
			t.Errorf("write without flush support: %v", err)
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("unsupported flush was not a no-op: %v", err)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	r, key := request(t, gateway, Request{Method: "GET", Path: "/v1/info"})
	recorder := httptest.NewRecorder()
	gateway.ServeHTTP(gatewayNonFlushingWriter{recorder}, r)
	body := newResponseBody(io.NopCloser(strings.NewReader(recorder.Body.String())), key)
	defer body.Close()
	if kind, _, err := body.next(); err != nil || kind != "http_headers" {
		t.Fatalf("non-flushing response headers = %q, %v", kind, err)
	}
	plain, err := io.ReadAll(body)
	if err != nil || string(plain) != "body" {
		t.Fatalf("non-flushing response = %q, %v", plain, err)
	}
}

type gatewayPipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
	address     net.Addr
}

func (l *gatewayPipeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *gatewayPipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *gatewayPipeListener) Addr() net.Addr { return l.address }

func TestGatewaySlowHeaderFlushReleasesStreamingApplication(t *testing.T) {
	released := make(chan struct{})
	failure := make(chan error, 1)
	gateway, err := New(testKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(released)
		w.WriteHeader(http.StatusOK)
		failure <- http.NewResponseController(w).Flush()
	}))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := request(t, gateway, Request{Method: "GET", Path: "/v1/events"})
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	listener := &gatewayPipeListener{
		connections: make(chan net.Conn, 1), closed: make(chan struct{}), address: serverConn.LocalAddr(),
	}
	listener.connections <- serverConn
	server := &http.Server{Handler: gateway, WriteTimeout: 30 * time.Millisecond}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	if err := clientConn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := r.Write(clientConn); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failure:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("slow header flush lost write deadline failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow reader retained the streaming application")
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("slow reader retained the application lease")
	}
}
