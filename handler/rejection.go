package handler

import (
	"encoding/json"
	"net/http"

	"github.com/newo-ether/conch/crypto"
)

// This helper is only for failure before operation dispatch. Never sign errors
// from execution, job creation or file mutation as proof that nothing happened.
func writePreDispatchError(w http.ResponseWriter, r *http.Request, apiKey []byte, message string) {
	const status = http.StatusBadRequest
	body, _ := json.Marshal(map[string]string{"error": message})
	if len(apiKey) != 0 && r.Header.Get("X-Signature") != "" {
		w.Header().Set(crypto.RejectionSignatureHeader, crypto.SignPayload(
			apiKey, r.Header.Get("X-Signature"), crypto.RejectionPayload(status, body),
		))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
