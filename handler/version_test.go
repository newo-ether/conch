package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/newo-ether/conch/buildinfo"
)

func TestVersionHandlerExposesProtocolAndCapabilities(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	recorder := httptest.NewRecorder()
	VersionHandler(recorder, req)

	var metadata buildinfo.Metadata
	if err := json.Unmarshal(recorder.Body.Bytes(), &metadata); err != nil {
		t.Fatalf("decode version: %v", err)
	}
	if metadata.ProtocolVersion != buildinfo.ProtocolVersion || len(metadata.Capabilities) == 0 {
		t.Fatalf("metadata = %#v", metadata)
	}
}
