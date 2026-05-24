package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/newo-ether/conch/crypto"
)

// LineEvent represents one line of shell output.
type LineEvent struct {
	Line     string `json:"line,omitempty"`
	Stream   string `json:"stream,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Transport handles the encrypted Conch protocol.
type Transport struct {
	serverURL    string
	apiKey       []byte
	serverPubKey *ecdh.PublicKey
	client       *http.Client
}

func NewTransport(serverURL, apiKey string) *Transport {
	return &Transport{
		serverURL: serverURL,
		apiKey:    []byte(apiKey),
		client: &http.Client{
			Timeout: 130 * time.Second, // max timeout + buffer
		},
	}
}

// Initialize fetches and verifies the server's public key.
func (t *Transport) Initialize(ctx context.Context) error {
	if len(t.apiKey) == 0 {
		return fmt.Errorf("API key is required")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", t.serverURL+"/public-key", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch public key: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read public key response: %w", err)
	}

	var result struct {
		PublicKey string `json:"public_key"`
		Nonce     string `json:"nonce"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse public key response: %w", err)
	}
	if result.PublicKey == "" || result.Nonce == "" || result.Signature == "" {
		return fmt.Errorf("server response missing fields")
	}

	if !crypto.VerifyPayload(t.apiKey, result.Nonce, result.PublicKey, result.Signature) {
		return fmt.Errorf("public key signature verification failed — API keys may not match")
	}

	pubKey, err := crypto.DecodePublicKey(result.PublicKey)
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	t.serverPubKey = pubKey
	return nil
}

// Execute sends an encrypted command and returns the shell output.
func (t *Transport) Execute(ctx context.Context, command string, timeoutMs int, workdir string) ([]LineEvent, error) {
	if t.serverPubKey == nil {
		return nil, fmt.Errorf("not initialized — call Initialize first")
	}

	// Build command JSON
	cmdJSON, err := json.Marshal(map[string]interface{}{
		"command":    command,
		"timeout_ms": timeoutMs,
		"workdir":    workdir,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal command: %w", err)
	}

	// Generate ephemeral key pair
	ephKP, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral key: %w", err)
	}

	// ECDH → AES key
	aesKey, err := crypto.DeriveSharedSecret(ephKP.PrivateKey, t.serverPubKey)
	if err != nil {
		return nil, fmt.Errorf("derive AES key: %w", err)
	}

	// Encrypt command body
	encryptedBody, err := crypto.Encrypt(aesKey, cmdJSON)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}

	bodyBytes := []byte(encryptedBody)
	bodySHA256 := crypto.SHA256Hex(bodyBytes)
	timestamp := time.Now().Unix()
	method := "POST"
	path := "/execute"
	nonce, err := crypto.GenerateNonce()
	if err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	clientPubKey := crypto.B64.EncodeToString(ephKP.PublicKey.Bytes())
	signature := crypto.Sign(t.apiKey, fmt.Sprintf("%d", timestamp), method, path, bodySHA256, nonce, clientPubKey)

	req, err := http.NewRequestWithContext(ctx, method, t.serverURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Timestamp", fmt.Sprintf("%d", timestamp))
	req.Header.Set("X-Signature", signature)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Encryption", "v1")
	req.Header.Set("X-Client-Public-Key", clientPubKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	return parseSSE(resp.Body, aesKey)
}

// parseSSE reads an SSE stream, decrypts data lines, and returns shell events.
func parseSSE(r io.Reader, aesKey []byte) ([]LineEvent, error) {
	var events []LineEvent
	var currentEvent string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue // SSE event separator
		}
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			// Decrypt if we have an AES key; otherwise treat as plaintext
			var payload []byte
			if aesKey != nil {
				var err error
				payload, err = crypto.Decrypt(aesKey, data)
				if err != nil {
					log.Printf("ERROR: SSE decryption failed: %v", err)
					continue
				}
			} else {
				payload = []byte(data)
			}

			var evt LineEvent
			switch currentEvent {
			case "line":
				json.Unmarshal(payload, &evt)
			case "result":
				var r struct {
					ExitCode int `json:"exit_code"`
				}
				json.Unmarshal(payload, &r)
				code := r.ExitCode
				evt = LineEvent{ExitCode: &code}
			case "error":
				var e struct {
					Message string `json:"message"`
				}
				json.Unmarshal(payload, &e)
				evt = LineEvent{Error: e.Message}
			default:
				continue
			}
			events = append(events, evt)
		}
	}
	return events, scanner.Err()
}
