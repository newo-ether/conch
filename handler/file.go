package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/newo-ether/conch/crypto"
)

const maxFileSize = 1 << 20          // 1 MB request/response content
const maxEditableFileSize = 16 << 20 // 16 MB server-side edit
const maxImageFileSize = 20 << 20    // 20 MB

// grepMaxFileSize bounds per-file reads during grep so a single huge file cannot
// exhaust memory; mirrors the 500 KB cap used by the on-device sandbox backend.
const grepMaxFileSize = 500 * 1024

// grepMaxContentLen truncates a returned matching line, matching the other backends.
const grepMaxContentLen = 500

const maxFileSearchDuration = 30 * time.Second

// FileReadRequest is the decrypted body for POST /file/read.
type FileReadRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Limit  int64  `json:"limit"`
}

type FileReadResponse struct {
	Content    string `json:"content"`
	Lines      int    `json:"lines"`
	TotalLines int    `json:"totalLines"`
	Size       int64  `json:"size"`
	Truncated  bool   `json:"truncated"`
}

// FileImageRequest is the decrypted body for POST /file/image.
type FileImageRequest struct {
	Path string `json:"path"`
}

type FileImageResponse struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
}

// FileWriteRequest is the decrypted body for POST /file/write.
type FileWriteRequest struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}

type FileWriteResponse struct {
	OK     bool   `json:"ok"`
	SHA256 string `json:"sha256"`
}

type FileEditRequest struct {
	Path           string `json:"path"`
	OldString      string `json:"old_string"`
	NewString      string `json:"new_string"`
	ReplaceAll     bool   `json:"replace_all"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}

type FileEditResponse struct {
	OK           bool   `json:"ok"`
	Replacements int    `json:"replacements"`
	SHA256       string `json:"sha256"`
}

// FileGlobRequest is the decrypted body for POST /file/glob.
// Depth is optional: nil or <= 0 means unlimited recursion below Path (matching
// the sandbox/SSH backends); a value >= 1 limits the walk to that many directory
// levels below Path (1 = Path itself only). A bare Pattern (no '/') matches file
// basenames at any depth; patterns may use '**' for recursive segment matching.
type FileGlobRequest struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Depth   *int   `json:"depth"`
}

type FileGlobResponse struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
}

// FileGrepRequest is the decrypted body for POST /file/grep.
type FileGrepRequest struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Glob    string `json:"glob"`
}

type GrepMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type FileGrepResponse struct {
	Matches   []GrepMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
}

// decryptBody handles the shared encryption-detection logic.
func decryptBody(r *http.Request, bodyBytes []byte, apiKey []byte, keyPair *crypto.KeyPair) ([]byte, []byte, error) {
	var aesKey []byte
	var plaintext []byte

	if r.Header.Get("X-Encryption") == "v1" {
		clientPubKeyStr := r.Header.Get("X-Client-Public-Key")
		if clientPubKeyStr == "" {
			return nil, nil, fmt.Errorf("missing client public key")
		}
		clientPubKey, err := crypto.DecodePublicKey(clientPubKeyStr)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid client public key: %w", err)
		}
		aesKey, err = crypto.DeriveSharedSecret(keyPair.PrivateKey, clientPubKey)
		if err != nil {
			return nil, nil, fmt.Errorf("key derivation failed: %w", err)
		}
		plaintext, err = crypto.Decrypt(aesKey, string(bodyBytes))
		if err != nil {
			return nil, nil, fmt.Errorf("decryption failed: %w", err)
		}
	} else if len(apiKey) > 0 {
		return nil, nil, fmt.Errorf("encryption required")
	} else {
		plaintext = bodyBytes
	}

	return plaintext, aesKey, nil
}

func writeJSONResponse(w http.ResponseWriter, v any, aesKey []byte) {
	writeJSONResponseStatus(w, http.StatusOK, v, aesKey)
}

func writeJSONResponseStatus(w http.ResponseWriter, status int, v any, aesKey []byte) {
	var data []byte
	if v != nil {
		data, _ = json.Marshal(v)
	}

	var body string
	if aesKey != nil {
		enc, err := crypto.Encrypt(aesKey, data)
		if err != nil {
			log.Printf("ERROR: encrypting response: %v", err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		body = enc
	} else {
		body = string(data)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// FileReadHandler serves POST /file/read.
type FileReadHandler struct {
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

func (h *FileReadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	plaintext, aesKey, err := decryptBody(r, bodyBytes, h.APIKey, h.KeyPair)
	if err != nil {
		writeJSONResponseStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()}, aesKey)
		return
	}

	var req FileReadRequest
	if err := json.Unmarshal(plaintext, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
		return
	}
	if req.Path == "" {
		writeJSONResponse(w, map[string]string{"error": "path is required"}, aesKey)
		return
	}
	if req.Limit <= 0 || req.Limit > maxFileSize {
		req.Limit = maxFileSize
	}
	if req.Offset < 0 {
		req.Offset = 0
	}

	f, err := os.Open(req.Path)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("open: %v", err)}, aesKey)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("stat: %v", err)}, aesKey)
		return
	}
	if !info.Mode().IsRegular() {
		writeJSONResponse(w, map[string]string{"error": "path is not a regular file"}, aesKey)
		return
	}

	if req.Offset > 0 {
		if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
			writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("seek: %v", err)}, aesKey)
			return
		}
	}

	buf := make([]byte, req.Limit)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("read: %v", err)}, aesKey)
		return
	}
	content := string(buf[:n])

	// Count lines in returned content
	lines := 0
	if len(content) > 0 {
		lines = 1
		for _, c := range content {
			if c == '\n' {
				lines++
			}
		}
	}

	truncated := req.Offset+int64(n) < info.Size()
	totalLines := 0
	if req.Offset == 0 && !truncated {
		totalLines = lines
	}
	resp := FileReadResponse{
		Content:    content,
		Lines:      lines,
		TotalLines: totalLines,
		Size:       info.Size(),
		Truncated:  truncated,
	}
	writeJSONResponse(w, resp, aesKey)
}

// FileImageHandler reads one bounded raster image without any text conversion.
// The base64 payload stays inside the existing encrypted JSON envelope.
type FileImageHandler struct {
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

func (h *FileImageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	plaintext, aesKey, err := decryptBody(r, bodyBytes, h.APIKey, h.KeyPair)
	if err != nil {
		writeJSONResponseStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()}, aesKey)
		return
	}

	var req FileImageRequest
	if err := json.Unmarshal(plaintext, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
		return
	}
	if req.Path == "" {
		writeJSONResponse(w, map[string]string{"error": "path is required"}, aesKey)
		return
	}

	f, err := os.Open(req.Path)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("open: %v", err)}, aesKey)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("stat: %v", err)}, aesKey)
		return
	}
	if !info.Mode().IsRegular() {
		writeJSONResponse(w, map[string]string{"error": "path is not a regular file"}, aesKey)
		return
	}
	if info.Size() <= 0 {
		writeJSONResponse(w, map[string]string{"error": "image is empty"}, aesKey)
		return
	}
	if info.Size() > maxImageFileSize {
		writeJSONResponse(w, map[string]string{"error": "image exceeds 20MB limit"}, aesKey)
		return
	}

	data, err := io.ReadAll(io.LimitReader(f, maxImageFileSize+1))
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("read: %v", err)}, aesKey)
		return
	}
	if len(data) > maxImageFileSize {
		writeJSONResponse(w, map[string]string{"error": "image exceeds 20MB limit"}, aesKey)
		return
	}
	mimeType := http.DetectContentType(data[:min(len(data), 512)])
	if !strings.HasPrefix(mimeType, "image/") {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("unsupported image type: %s", mimeType)}, aesKey)
		return
	}

	writeJSONResponse(w, FileImageResponse{
		Data:     base64.StdEncoding.EncodeToString(data),
		MimeType: mimeType,
		Size:     info.Size(),
	}, aesKey)
}

// FileWriteHandler serves POST /file/write.
type FileWriteHandler struct {
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

func (h *FileWriteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	plaintext, aesKey, err := decryptBody(r, bodyBytes, h.APIKey, h.KeyPair)
	if err != nil {
		writeJSONResponseStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()}, aesKey)
		return
	}

	var req FileWriteRequest
	if err := json.Unmarshal(plaintext, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
		return
	}
	if req.Path == "" {
		writeJSONResponse(w, map[string]string{"error": "path is required"}, aesKey)
		return
	}
	if len(req.Content) > maxFileSize {
		writeJSONResponse(w, map[string]string{"error": "content exceeds 1MB limit"}, aesKey)
		return
	}

	hash, err := atomicWriteFile(req.Path, []byte(req.Content), req.ExpectedSHA256)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("write: %v", err)}, aesKey)
		return
	}

	writeJSONResponse(w, FileWriteResponse{OK: true, SHA256: hash}, aesKey)
}

// FileEditHandler performs a bounded server-side compare-and-swap edit. The entire file is read
// and replaced on the target host, so MCP never rewrites a truncated /file/read response.
type FileEditHandler struct {
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

func (h *FileEditHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	plaintext, aesKey, err := decryptBody(r, bodyBytes, h.APIKey, h.KeyPair)
	if err != nil {
		writeJSONResponseStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()}, aesKey)
		return
	}

	var req FileEditRequest
	if err := json.Unmarshal(plaintext, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
		return
	}
	if req.Path == "" {
		writeJSONResponse(w, map[string]string{"error": "path is required"}, aesKey)
		return
	}
	if req.OldString == "" {
		writeJSONResponse(w, map[string]string{"error": "old_string is required"}, aesKey)
		return
	}

	unlock := lockFileMutation(req.Path)
	defer unlock()

	file, err := os.Open(req.Path)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("open: %v", err)}, aesKey)
		return
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("stat: %v", err)}, aesKey)
		return
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		writeJSONResponse(w, map[string]string{"error": "path is not a regular file"}, aesKey)
		return
	}
	if info.Size() > maxEditableFileSize {
		_ = file.Close()
		writeJSONResponse(w, map[string]string{"error": "file exceeds 16MB edit limit"}, aesKey)
		return
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxEditableFileSize+1))
	closeErr := file.Close()
	if readErr != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("read: %v", readErr)}, aesKey)
		return
	}
	if closeErr != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("close: %v", closeErr)}, aesKey)
		return
	}
	if len(data) > maxEditableFileSize {
		writeJSONResponse(w, map[string]string{"error": "file exceeds 16MB edit limit"}, aesKey)
		return
	}
	currentHash := crypto.SHA256Hex(data)
	if req.ExpectedSHA256 != "" && !strings.EqualFold(req.ExpectedSHA256, currentHash) {
		writeJSONResponse(w, map[string]string{
			"error": fmt.Sprintf(
				"file changed concurrently: expected sha256 %s, got %s",
				req.ExpectedSHA256,
				currentHash,
			),
		}, aesKey)
		return
	}

	content := string(data)
	count := strings.Count(content, req.OldString)
	if count == 0 {
		writeJSONResponse(w, map[string]string{"error": "old_string not found in file"}, aesKey)
		return
	}
	if count > 1 && !req.ReplaceAll {
		writeJSONResponse(w, map[string]string{
			"error": fmt.Sprintf(
				"found %d matches of old_string; set replace_all=true or provide a unique match",
				count,
			),
		}, aesKey)
		return
	}
	replacementCount := count
	replaced := strings.ReplaceAll(content, req.OldString, req.NewString)
	if !req.ReplaceAll {
		replacementCount = 1
		replaced = strings.Replace(content, req.OldString, req.NewString, 1)
	}
	if len(replaced) > maxEditableFileSize {
		writeJSONResponse(w, map[string]string{"error": "edited file exceeds 16MB limit"}, aesKey)
		return
	}

	hash, err := atomicWriteFileLocked(req.Path, []byte(replaced), currentHash)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("edit: %v", err)}, aesKey)
		return
	}
	writeJSONResponse(w, FileEditResponse{
		OK:           true,
		Replacements: replacementCount,
		SHA256:       hash,
	}, aesKey)
}

// FileGlobHandler serves POST /file/glob.
type FileGlobHandler struct {
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

func (h *FileGlobHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	plaintext, aesKey, err := decryptBody(r, bodyBytes, h.APIKey, h.KeyPair)
	if err != nil {
		writeJSONResponseStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()}, aesKey)
		return
	}

	var req FileGlobRequest
	if err := json.Unmarshal(plaintext, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
		return
	}
	if req.Pattern == "" {
		writeJSONResponse(w, map[string]string{"error": "pattern is required"}, aesKey)
		return
	}
	if req.Path == "" {
		req.Path, _ = os.UserHomeDir()
	}

	// nil / <= 0 → unlimited recursion (consistent with the sandbox & SSH backends).
	maxDepth := 0
	if req.Depth != nil && *req.Depth > 0 {
		maxDepth = *req.Depth
	}
	searchCtx, cancel := context.WithTimeout(r.Context(), maxFileSearchDuration)
	defer cancel()
	matches, err := globWithDepth(searchCtx, req.Path, req.Pattern, maxDepth)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("glob: %v", err)}, aesKey)
		return
	}

	truncated := len(matches) >= 1000
	if len(matches) > 1000 {
		matches = matches[:1000]
	}
	if matches == nil {
		matches = []string{}
	}

	writeJSONResponse(w, FileGlobResponse{Files: matches, Truncated: truncated}, aesKey)
}

// globWithDepth recursively walks root and returns files matching pattern,
// descending at most maxDepth directory levels below root (maxDepth <= 0 =
// unlimited; a file directly inside root is depth 1). A bare pattern (no '/')
// matches basenames at any depth; otherwise the pattern is matched against the
// path relative to root. '**' is supported via doublestar, giving the same
// semantics as the Java PathMatcher used by the Kotlin backends.
func globWithDepth(ctx context.Context, root, pattern string, maxDepth int) ([]string, error) {
	matches := make([]string, 0)
	rootClean := filepath.Clean(root)
	rootSeps := strings.Count(rootClean, string(os.PathSeparator))
	// A separator-less pattern matches file names at any depth.
	matchPattern := pattern
	if !strings.Contains(pattern, "/") {
		matchPattern = "**/" + pattern
	}
	err := filepath.Walk(rootClean, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil || path == rootClean {
			return nil
		}
		level := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - rootSeps
		if info.IsDir() {
			if maxDepth > 0 && level >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if maxDepth > 0 && level > maxDepth {
			return nil
		}
		rel, relErr := filepath.Rel(rootClean, path)
		if relErr != nil {
			return nil
		}
		if ok, _ := doublestar.Match(matchPattern, filepath.ToSlash(rel)); ok {
			matches = append(matches, path)
			if len(matches) >= 1000 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return matches, err
}

// FileGrepHandler serves POST /file/grep.
type FileGrepHandler struct {
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

func (h *FileGrepHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	plaintext, aesKey, err := decryptBody(r, bodyBytes, h.APIKey, h.KeyPair)
	if err != nil {
		writeJSONResponseStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()}, aesKey)
		return
	}

	var req FileGrepRequest
	if err := json.Unmarshal(plaintext, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
		return
	}
	if req.Pattern == "" {
		writeJSONResponse(w, map[string]string{"error": "pattern is required"}, aesKey)
		return
	}
	if req.Path == "" {
		req.Path, _ = os.UserHomeDir()
	}

	re, err := regexp.Compile(req.Pattern)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("regex compile: %v", err)}, aesKey)
		return
	}

	matches := make([]GrepMatch, 0)

	var globFunc func(string) bool
	if req.Glob != "" {
		globFunc = func(name string) bool {
			ok, _ := filepath.Match(req.Glob, filepath.Base(name))
			return ok
		}
	}

	searchCtx, cancel := context.WithTimeout(r.Context(), maxFileSearchDuration)
	defer cancel()
	err = filepath.Walk(req.Path, func(path string, info os.FileInfo, err error) error {
		if ctxErr := searchCtx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil || info.IsDir() {
			return nil
		}
		if len(matches) >= 500 {
			return filepath.SkipAll
		}
		if globFunc != nil && !globFunc(path) {
			return nil
		}
		// Skip oversized files so a single huge file can't exhaust memory.
		if info.Size() > grepMaxFileSize {
			return nil
		}
		if err := grepFile(searchCtx, path, re, &matches); err != nil {
			return err
		}
		if len(matches) >= 500 {
			return filepath.SkipAll
		}
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("walk: %v", err)}, aesKey)
		return
	}

	writeJSONResponse(w, FileGrepResponse{
		Matches:   matches,
		Truncated: len(matches) >= 500,
	}, aesKey)
}

// grepFile streams a single file line-by-line, appending matches (up to the
// global 500 cap) to matches. Binary files — detected by a NUL byte in the head,
// the same heuristic grep uses — are skipped so they don't emit garbage matches.
func grepFile(ctx context.Context, path string, re *regexp.Regexp, matches *[]GrepMatch) error {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	br := bufio.NewReader(f)
	if head, _ := br.Peek(512); bytes.IndexByte(head, 0) >= 0 {
		return nil // binary file
	}

	scanner := bufio.NewScanner(br)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		lineNo++
		line := scanner.Text()
		if re.MatchString(line) {
			if len(line) > grepMaxContentLen {
				line = line[:grepMaxContentLen]
			}
			*matches = append(*matches, GrepMatch{Path: path, Line: lineNo, Content: line})
			if len(*matches) >= 500 {
				return nil
			}
		}
	}
	return scanner.Err()
}
