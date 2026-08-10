package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/newo-ether/conch/buildinfo"
	"github.com/newo-ether/conch/crypto"
)

// LineEvent represents one line of shell output.
type LineEvent struct {
	Line     string `json:"line,omitempty"`
	Stream   string `json:"stream,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
	Warning  string `json:"warning,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"`
}

// Transport handles the encrypted Conch protocol.
type Transport struct {
	serverURL string
	apiKey    []byte
	client    *http.Client

	initMu         sync.Mutex
	stateMu        sync.RWMutex
	serverPubKey   *ecdh.PublicKey
	serverMetadata buildinfo.Metadata
	lastError      string
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

// Initialize fetches and verifies the server's public key. Calls are serialized so offline
// recovery and key-rotation refreshes cannot race.
func (t *Transport) Initialize(ctx context.Context) error {
	t.initMu.Lock()
	defer t.initMu.Unlock()

	err := t.initialize(ctx)
	t.stateMu.Lock()
	if err != nil {
		t.lastError = err.Error()
	} else {
		t.lastError = ""
	}
	t.stateMu.Unlock()
	return err
}

func (t *Transport) initialize(ctx context.Context) error {
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

	body, err := readBoundedBody(resp.Body, maxControlResponseBytes)
	if err != nil {
		return fmt.Errorf("read public key response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &serverHTTPError{statusCode: resp.StatusCode, body: string(body)}
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
	metadata, err := t.fetchServerMetadata(ctx)
	if err != nil {
		return err
	}
	t.stateMu.Lock()
	t.serverPubKey = pubKey
	t.serverMetadata = metadata
	t.stateMu.Unlock()
	return nil
}

func (t *Transport) currentServerKey() *ecdh.PublicKey {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	return t.serverPubKey
}

func (t *Transport) ensureInitialized(ctx context.Context) error {
	if t.currentServerKey() != nil {
		return nil
	}
	return t.Initialize(ctx)
}

func (t *Transport) Status() (bool, string) {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	if t.serverPubKey == nil {
		if t.lastError != "" {
			return false, t.lastError
		}
		return false, "transport is not initialized"
	}
	return t.lastError == "", t.lastError
}

func (t *Transport) recordOperation(err error) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if err != nil {
		t.lastError = err.Error()
	} else {
		t.lastError = ""
	}
}

func (t *Transport) Metadata() buildinfo.Metadata {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	metadata := t.serverMetadata
	metadata.Capabilities = append([]string(nil), metadata.Capabilities...)
	return metadata
}

func (t *Transport) fetchServerMetadata(ctx context.Context) (buildinfo.Metadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.serverURL+"/version", nil)
	if err != nil {
		return buildinfo.Metadata{}, fmt.Errorf("create version request: %w", err)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return buildinfo.Metadata{}, fmt.Errorf("fetch server version: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return buildinfo.Metadata{
			Name:            "conch",
			Version:         "legacy",
			Revision:        "unknown",
			BuildTime:       "unknown",
			ProtocolVersion: "0",
		}, nil
	}
	body, err := readBoundedBody(resp.Body, maxControlResponseBytes)
	if err != nil {
		return buildinfo.Metadata{}, fmt.Errorf("read server version: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return buildinfo.Metadata{}, &serverHTTPError{
			statusCode: resp.StatusCode,
			body:       string(body),
		}
	}
	var metadata buildinfo.Metadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return buildinfo.Metadata{}, fmt.Errorf("parse server version: %w", err)
	}
	if metadata.Version == "" || metadata.ProtocolVersion == "" {
		return buildinfo.Metadata{}, fmt.Errorf("server version response missing fields")
	}
	if metadata.ProtocolVersion != buildinfo.ProtocolVersion {
		return buildinfo.Metadata{}, fmt.Errorf(
			"unsupported Conch protocol %s (client requires %s)",
			metadata.ProtocolVersion,
			buildinfo.ProtocolVersion,
		)
	}
	return metadata, nil
}

type serverHTTPError struct {
	statusCode int
	body       string
}

func (e *serverHTTPError) Error() string {
	return fmt.Sprintf("server returned %d: %s", e.statusCode, e.body)
}

func isStaleServerKeyError(err error) bool {
	var responseError *serverHTTPError
	if err == nil || !errors.As(err, &responseError) || responseError.statusCode != http.StatusBadRequest {
		return false
	}
	return strings.Contains(strings.ToLower(responseError.body), "decryption failed")
}

// Execute sends an encrypted command and returns the shell output. It retries only after an
// explicit server-side decryption rejection, which is known to happen before command execution.
func (t *Transport) Execute(
	ctx context.Context,
	command string,
	timeoutMs int,
	workdir string,
) (events []LineEvent, err error) {
	defer func() { t.recordOperation(err) }()
	events, err = t.executeOnce(ctx, command, timeoutMs, workdir)
	if !isStaleServerKeyError(err) {
		return events, err
	}
	if err = t.Initialize(ctx); err != nil {
		return nil, fmt.Errorf("refresh server key: %w", err)
	}
	return t.executeOnce(ctx, command, timeoutMs, workdir)
}

func (t *Transport) executeOnce(ctx context.Context, command string, timeoutMs int, workdir string) ([]LineEvent, error) {
	if err := t.ensureInitialized(ctx); err != nil {
		return nil, fmt.Errorf("initialize transport: %w", err)
	}
	serverPubKey := t.currentServerKey()

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
	aesKey, err := crypto.DeriveSharedSecret(ephKP.PrivateKey, serverPubKey)
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
		body, readErr := readBoundedBody(resp.Body, maxControlResponseBytes)
		if readErr != nil {
			return nil, fmt.Errorf("read execute error response: %w", readErr)
		}
		return nil, &serverHTTPError{statusCode: resp.StatusCode, body: string(body)}
	}

	return parseSSE(resp.Body, aesKey)
}

type ShellJobResult struct {
	Type            string     `json:"type"`
	Background      bool       `json:"background"`
	JobID           string     `json:"job_id"`
	Command         string     `json:"command"`
	Workdir         string     `json:"workdir,omitempty"`
	State           string     `json:"state"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	Error           string     `json:"error,omitempty"`
	Warning         string     `json:"warning,omitempty"`
	SettlementError string     `json:"settlement_error,omitempty"`
	Output          string     `json:"output,omitempty"`
	OutputBytes     int64      `json:"output_bytes"`
	Truncated       bool       `json:"truncated"`
}

type ShellJobListResult struct {
	Type  string           `json:"type"`
	Jobs  []ShellJobResult `json:"jobs"`
	Error string           `json:"error,omitempty"`
}

type ShellJobAckResult struct {
	Type         string `json:"type"`
	JobID        string `json:"job_id"`
	Acknowledged bool   `json:"acknowledged"`
	Error        string `json:"error,omitempty"`
}

func (t *Transport) StartJob(
	ctx context.Context,
	command string,
	timeoutMs int,
	workdir string,
) (*ShellJobResult, error) {
	payload, err := json.Marshal(map[string]any{
		"command":    command,
		"timeout_ms": timeoutMs,
		"workdir":    workdir,
	})
	if err != nil {
		return nil, err
	}
	var result ShellJobResult
	if err := t.doFileRequest(ctx, "/jobs/start", payload, &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}
	if result.JobID == "" {
		return nil, fmt.Errorf("server returned incomplete job start result")
	}
	return &result, nil
}

func (t *Transport) ListJobs(ctx context.Context) (*ShellJobListResult, error) {
	var result ShellJobListResult
	if err := t.doFileRequest(ctx, "/jobs/list", []byte("{}"), &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}
	if result.Jobs == nil {
		result.Jobs = []ShellJobResult{}
	}
	return &result, nil
}

func (t *Transport) GetJob(ctx context.Context, jobID string) (*ShellJobResult, error) {
	return t.jobAction(ctx, "/jobs/get", jobID)
}

func (t *Transport) StopJob(ctx context.Context, jobID string) (*ShellJobResult, error) {
	return t.jobAction(ctx, "/jobs/stop", jobID)
}

func (t *Transport) jobAction(
	ctx context.Context,
	path string,
	jobID string,
) (*ShellJobResult, error) {
	payload, err := json.Marshal(map[string]string{"job_id": jobID})
	if err != nil {
		return nil, err
	}
	var result ShellJobResult
	if err := t.doFileRequest(ctx, path, payload, &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}
	if result.JobID == "" {
		return nil, fmt.Errorf("server returned incomplete job result")
	}
	return &result, nil
}

func (t *Transport) AcknowledgeJob(ctx context.Context, jobID string) (*ShellJobAckResult, error) {
	payload, err := json.Marshal(map[string]string{"job_id": jobID})
	if err != nil {
		return nil, err
	}
	var result ShellJobAckResult
	if err := t.doFileRequest(ctx, "/jobs/ack", payload, &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}
	if result.JobID == "" {
		return nil, fmt.Errorf("server returned incomplete job acknowledgement")
	}
	return &result, nil
}

// FileReadResult is the decrypted response from POST /file/read.
type FileReadResult struct {
	Content    string `json:"content"`
	Lines      int    `json:"lines"`
	TotalLines int    `json:"totalLines"`
	Size       int64  `json:"size"`
	Truncated  bool   `json:"truncated"`
	Error      string `json:"error,omitempty"`
}

// FileImageResult is the decrypted binary-safe response from POST /file/image.
type FileImageResult struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Error    string `json:"error,omitempty"`
}

type FileMutationResult struct {
	OK           bool   `json:"ok"`
	Replacements int    `json:"replacements,omitempty"`
	SHA256       string `json:"sha256"`
	Error        string `json:"error,omitempty"`
}

// GrepMatchResult is one match from POST /file/grep.
type GrepMatchResult struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// FileRead sends a file read request.
func (t *Transport) FileRead(ctx context.Context, path string, offset, limit int64) (*FileReadResult, error) {
	if limit <= 0 || limit > 1<<20 {
		limit = 1 << 20
	}
	body, err := json.Marshal(map[string]interface{}{
		"path":   path,
		"offset": offset,
		"limit":  limit,
	})
	if err != nil {
		return nil, err
	}
	var result FileReadResult
	if err := t.doFileRequest(ctx, "/file/read", body, &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}
	return &result, nil
}

// FileImage reads an image without passing binary bytes through a UTF-8 string.
func (t *Transport) FileImage(ctx context.Context, path string) (*FileImageResult, error) {
	body, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		return nil, err
	}
	var result FileImageResult
	if err := t.doFileRequest(ctx, "/file/image", body, &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}
	if result.Data == "" || result.MimeType == "" {
		return nil, fmt.Errorf("server returned incomplete image data")
	}
	return &result, nil
}

// FileWrite sends an unconditional atomic file write request.
func (t *Transport) FileWrite(ctx context.Context, path, content string) error {
	_, err := t.FileWriteCAS(ctx, path, content, "")
	return err
}

// FileWriteCAS sends an atomic file write with an optional expected content hash.
func (t *Transport) FileWriteCAS(
	ctx context.Context,
	path string,
	content string,
	expectedSHA256 string,
) (*FileMutationResult, error) {
	body, err := json.Marshal(map[string]interface{}{
		"path":            path,
		"content":         content,
		"expected_sha256": expectedSHA256,
	})
	if err != nil {
		return nil, err
	}
	var result FileMutationResult
	if err := t.doFileRequest(ctx, "/file/write", body, &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}
	if !result.OK || result.SHA256 == "" {
		return nil, fmt.Errorf("server returned incomplete file write result")
	}
	return &result, nil
}

// FileEdit performs the replacement atomically on the target host. It never rebuilds a file from
// the bounded /file/read response, which prevents silent tail truncation for files over 1 MiB.
func (t *Transport) FileEdit(
	ctx context.Context,
	path string,
	oldString string,
	newString string,
	replaceAll bool,
	expectedSHA256 string,
) (*FileMutationResult, error) {
	body, err := json.Marshal(map[string]interface{}{
		"path":            path,
		"old_string":      oldString,
		"new_string":      newString,
		"replace_all":     replaceAll,
		"expected_sha256": expectedSHA256,
	})
	if err != nil {
		return nil, err
	}
	var result FileMutationResult
	if err := t.doFileRequest(ctx, "/file/edit", body, &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}
	if !result.OK || result.Replacements <= 0 || result.SHA256 == "" {
		return nil, fmt.Errorf("server returned incomplete file edit result")
	}
	return &result, nil
}

// FileGlob sends a file glob request. Returns matching paths.
func (t *Transport) FileGlob(
	ctx context.Context,
	pattern string,
	basePath string,
) ([]string, bool, error) {
	body, err := json.Marshal(map[string]string{
		"pattern": pattern,
		"path":    basePath,
	})
	if err != nil {
		return nil, false, err
	}
	var result struct {
		Files     []string `json:"files"`
		Truncated bool     `json:"truncated"`
		Error     string   `json:"error,omitempty"`
	}
	if err := t.doFileRequest(ctx, "/file/glob", body, &result); err != nil {
		return nil, false, err
	}
	if result.Error != "" {
		return nil, false, fmt.Errorf("%s", result.Error)
	}
	return result.Files, result.Truncated, nil
}

// FileGrep sends a file grep request. Returns matching lines.
func (t *Transport) FileGrep(
	ctx context.Context,
	pattern string,
	basePath string,
	fileGlob string,
) ([]GrepMatchResult, bool, error) {
	body, err := json.Marshal(map[string]string{
		"pattern": pattern,
		"path":    basePath,
		"glob":    fileGlob,
	})
	if err != nil {
		return nil, false, err
	}
	var result struct {
		Matches   []GrepMatchResult `json:"matches"`
		Truncated bool              `json:"truncated"`
		Error     string            `json:"error,omitempty"`
	}
	if err := t.doFileRequest(ctx, "/file/grep", body, &result); err != nil {
		return nil, false, err
	}
	if result.Error != "" {
		return nil, false, fmt.Errorf("%s", result.Error)
	}
	return result.Matches, result.Truncated, nil
}

// doFileRequest sends an encrypted JSON request. A retry is permitted only for an explicit
// decryption rejection, which the server emits before dispatching the requested operation.
func (t *Transport) doFileRequest(
	ctx context.Context,
	path string,
	payload []byte,
	result any,
) (err error) {
	defer func() { t.recordOperation(err) }()
	err = t.doFileRequestOnce(ctx, path, payload, result)
	if !isStaleServerKeyError(err) {
		return err
	}
	if err = t.Initialize(ctx); err != nil {
		return fmt.Errorf("refresh server key: %w", err)
	}
	return t.doFileRequestOnce(ctx, path, payload, result)
}

func (t *Transport) doFileRequestOnce(ctx context.Context, path string, payload []byte, result any) error {
	if err := t.ensureInitialized(ctx); err != nil {
		return fmt.Errorf("initialize transport: %w", err)
	}
	serverPubKey := t.currentServerKey()

	ephKP, err := crypto.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generate ephemeral key: %w", err)
	}

	aesKey, err := crypto.DeriveSharedSecret(ephKP.PrivateKey, serverPubKey)
	if err != nil {
		return fmt.Errorf("derive AES key: %w", err)
	}

	encryptedBody, err := crypto.Encrypt(aesKey, payload)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	bodyBytes := []byte(encryptedBody)
	bodySHA256 := crypto.SHA256Hex(bodyBytes)
	timestamp := time.Now().Unix()
	method := "POST"
	nonce, err := crypto.GenerateNonce()
	if err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	clientPubKey := crypto.B64.EncodeToString(ephKP.PublicKey.Bytes())
	signature := crypto.Sign(t.apiKey, fmt.Sprintf("%d", timestamp), method, path, bodySHA256, nonce, clientPubKey)

	req, err := http.NewRequestWithContext(ctx, method, t.serverURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Timestamp", fmt.Sprintf("%d", timestamp))
	req.Header.Set("X-Signature", signature)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Encryption", "v1")
	req.Header.Set("X-Client-Public-Key", clientPubKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	responseLimit := int64(maxEncryptedResponseBytes)
	if resp.StatusCode != http.StatusOK {
		responseLimit = maxControlResponseBytes
	}
	respBody, err := readBoundedBody(resp.Body, responseLimit)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return &serverHTTPError{statusCode: resp.StatusCode, body: string(respBody)}
	}

	plaintext, err := crypto.Decrypt(aesKey, string(respBody))
	if err != nil {
		return fmt.Errorf("decrypt response: %w", err)
	}

	if err := json.Unmarshal(plaintext, result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	return nil
}

const (
	maxControlResponseBytes   int64 = 64 << 10
	maxEncryptedResponseBytes       = 40 << 20
	maxSSELineBytes                 = 10 << 20
	maxRetainedShellBytes           = 1 << 20
)

func readBoundedBody(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response body exceeds %d-byte limit", limit)
	}
	return body, nil
}

// parseSSE reads an SSE stream and fails closed. Corrupt encryption, invalid JSON, unknown event
// types, duplicate terminals, scanner truncation, and EOF without a result/error can never be
// mistaken for successful command completion.
func parseSSE(r io.Reader, aesKey []byte) ([]LineEvent, error) {
	events := make([]LineEvent, 0)
	currentEvent := ""
	terminalSeen := false
	retainedBytes := 0
	outputTruncated := false
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			if currentEvent != "" {
				return nil, fmt.Errorf("SSE event %q missing data", currentEvent)
			}
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			return nil, fmt.Errorf("malformed SSE line")
		}
		if currentEvent == "" {
			return nil, fmt.Errorf("SSE data missing event type")
		}
		if terminalSeen {
			return nil, fmt.Errorf("SSE data received after terminal event")
		}

		data := strings.TrimPrefix(line, "data: ")
		var payload []byte
		if aesKey != nil {
			var err error
			payload, err = crypto.Decrypt(aesKey, data)
			if err != nil {
				return nil, fmt.Errorf("decrypt SSE %s event: %w", currentEvent, err)
			}
		} else {
			payload = []byte(data)
		}

		switch currentEvent {
		case "line":
			var evt LineEvent
			if err := json.Unmarshal(payload, &evt); err != nil {
				return nil, fmt.Errorf("decode SSE line event: %w", err)
			}
			if evt.Stream != "stdout" && evt.Stream != "stderr" {
				return nil, fmt.Errorf("invalid SSE output stream %q", evt.Stream)
			}
			eventBytes := len(evt.Line) + len(evt.Stream) + 2
			if retainedBytes+eventBytes <= maxRetainedShellBytes {
				events = append(events, evt)
				retainedBytes += eventBytes
			} else {
				outputTruncated = true
			}
		case "warning":
			var warning struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(payload, &warning); err != nil {
				return nil, fmt.Errorf("decode SSE warning event: %w", err)
			}
			if warning.Message == "" {
				return nil, fmt.Errorf("SSE warning event missing message")
			}
			events = append(events, LineEvent{Warning: warning.Message})
		case "result":
			var result struct {
				ExitCode *int `json:"exit_code"`
			}
			if err := json.Unmarshal(payload, &result); err != nil {
				return nil, fmt.Errorf("decode SSE result event: %w", err)
			}
			if result.ExitCode == nil {
				return nil, fmt.Errorf("SSE result event missing exit_code")
			}
			if outputTruncated {
				events = append(events, LineEvent{
					Warning: "MCP retained output was truncated at 1 MiB",
				})
			}
			events = append(events, LineEvent{ExitCode: result.ExitCode})
			terminalSeen = true
		case "error":
			var eventError struct {
				Message  string `json:"message"`
				TimedOut bool   `json:"timed_out,omitempty"`
			}
			if err := json.Unmarshal(payload, &eventError); err != nil {
				return nil, fmt.Errorf("decode SSE error event: %w", err)
			}
			if eventError.Message == "" {
				return nil, fmt.Errorf("SSE error event missing message")
			}
			if outputTruncated {
				events = append(events, LineEvent{
					Warning: "MCP retained output was truncated at 1 MiB",
				})
			}
			events = append(events, LineEvent{
				Error:    eventError.Message,
				TimedOut: eventError.TimedOut,
			})
			terminalSeen = true
		default:
			return nil, fmt.Errorf("unknown SSE event type %q", currentEvent)
		}
		currentEvent = ""
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSE stream: %w", err)
	}
	if currentEvent != "" {
		return nil, fmt.Errorf("SSE event %q missing data at EOF", currentEvent)
	}
	if !terminalSeen {
		return nil, fmt.Errorf("SSE stream ended without a terminal result or error")
	}
	return events, nil
}
