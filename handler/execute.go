package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/newo-ether/conch/buildinfo"
	"github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/shell"
)

type ExecuteHandler struct {
	Executor *shell.Executor
	APIKey   []byte
	KeyPair  *crypto.KeyPair
}

func (h *ExecuteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	// Read entire body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	var req shell.Request
	var aesKey []byte

	// Detect encryption
	if r.Header.Get("X-Encryption") == "v1" {
		clientPubKeyStr := r.Header.Get("X-Client-Public-Key")
		if clientPubKeyStr == "" {
			http.Error(w, `{"error":"missing client public key"}`, http.StatusBadRequest)
			return
		}
		clientPubKey, err := crypto.DecodePublicKey(clientPubKeyStr)
		if err != nil {
			log.Printf("ERROR: invalid client public key: %v", err)
			http.Error(w, `{"error":"invalid client public key"}`, http.StatusBadRequest)
			return
		}
		aesKey, err = crypto.DeriveSharedSecret(h.KeyPair.PrivateKey, clientPubKey)
		if err != nil {
			log.Printf("ERROR: key derivation failed: %v", err)
			http.Error(w, `{"error":"key derivation failed"}`, http.StatusInternalServerError)
			return
		}

		plaintext, err := crypto.Decrypt(aesKey, string(bodyBytes))
		if err != nil {
			log.Printf("ERROR: decryption failed: %v", err)
			http.Error(w, `{"error":"decryption failed"}`, http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(plaintext, &req); err != nil {
			http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
			return
		}
	} else if len(h.APIKey) > 0 {
		http.Error(w, `{"error":"encryption required"}`, http.StatusBadRequest)
		return
	} else {
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	events := h.Executor.Execute(r.Context(), req)
	responseController := http.NewResponseController(w)

	for evt := range events {
		var eventName string
		var payload string
		if evt.Warning != "" {
			// Its own event type so a client can surface degraded output without treating the
			// command as failed and without losing the exit code that still follows.
			data, _ := json.Marshal(map[string]string{"message": evt.Warning})
			eventName, payload = "warning", string(data)
		} else if evt.Error != "" {
			data, _ := json.Marshal(map[string]any{
				// timed_out is the machine-readable discriminator. The message text is for
				// humans only; clients decide whether to promote a command to a background
				// job from this flag alone, never by pattern-matching the message.
				"message":   evt.Error,
				"timed_out": evt.TimedOut,
			})
			eventName, payload = "error", string(data)
		} else if evt.ExitCode != nil {
			data, _ := json.Marshal(map[string]int{"exit_code": *evt.ExitCode})
			eventName, payload = "result", string(data)
		} else {
			data, _ := json.Marshal(map[string]string{
				"line":   evt.Line,
				"stream": evt.Stream,
			})
			eventName, payload = "line", string(data)
		}

		// The server-level write timeout is intentionally disabled for SSE. Bound each actual write
		// instead so an authenticated client that stops reading cannot pin a handler forever.
		if err := responseController.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil &&
			!errors.Is(err, http.ErrNotSupported) {
			log.Printf("ERROR: setting SSE write deadline: %v", err)
			return
		}
		if err := writeSSE(w, eventName, payload, aesKey); err != nil {
			log.Printf("ERROR: writing SSE event: %v", err)
			return
		}
		flusher.Flush()
	}
}

func writeSSE(w io.Writer, event, payload string, aesKey []byte) error {
	var data string
	if aesKey != nil {
		encrypted, err := crypto.Encrypt(aesKey, []byte(payload))
		if err != nil {
			return fmt.Errorf("encrypt SSE event: %w", err)
		}
		data = encrypted
	} else {
		data = payload
	}
	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}

func HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{"status": "ok"}
	metadata := buildinfo.Current("conch")
	response["version"] = metadata.Version
	response["revision"] = metadata.Revision
	response["protocol_version"] = metadata.ProtocolVersion
	_ = json.NewEncoder(w).Encode(response)
}

func VersionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(buildinfo.Current("conch"))
}
