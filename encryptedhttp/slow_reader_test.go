package encryptedhttp

import (
	"bufio"
	"fmt"
	"github.com/newo-ether/conch/crypto"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise real socket deadlines with no http.Server ReadTimeout configured.
func TestUnauthenticatedSlowBodyReleasesReaderWithoutServerTimeout(t *testing.T) {
	var calls atomic.Int32
	auth := AuthMiddleware([]byte("fixture-only"), crypto.NewNonceTracker())
	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewServer(handler)
	defer server.Close()
	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	_, err = fmt.Fprintf(connection, "POST /execute HTTP/1.1\r\nHost: fixture\r\nX-Signature: forged\r\nX-Timestamp: %d\r\nContent-Length: 100\r\n\r\nx", time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatalf("slow body never received bounded rejection: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("unauthenticated slow request reached application")
	}
	request := httptest.NewRequest("POST", "/execute", nil)
	request.Header.Set("X-Signature", "forged")
	request.Header.Set("X-Timestamp", fmt.Sprint(time.Now().Unix()))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatal("reader budget was not reusable")
	}
}
