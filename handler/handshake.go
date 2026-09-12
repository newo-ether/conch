package handler

import (
	"encoding/json"
	"net/http"

	"github.com/newo-ether/conch/crypto"
)

func PublicKeyHandler(apiKey []byte, pair *crypto.KeyPair) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		challenge := r.URL.Query().Get("challenge")
		if len(challenge) > 64 {
			http.Error(w, "invalid challenge", http.StatusBadRequest)
			return
		}
		document, err := crypto.NewHandshake(apiKey, pair, challenge)
		if err != nil {
			http.Error(w, "handshake unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(document)
	}
}
