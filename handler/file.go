package handler

import (
	"bufio"
	"bytes"
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

	"github.com/bmatcuk/doublestar/v4"
	"github.com/newo-ether/conch/crypto"
)

const maxFileSize = 1 << 20       // 1 MB
const maxImageFileSize = 20 << 20 // 20 MB

// grepMaxFileSize bounds per-file reads during grep so a single huge file cannot
// exhaust memory; mirrors the 500 KB cap used by the on-device sandbox backend.
const grepMaxFileSize = 500 * 1024

// grepMaxContentLen truncates a returned matching line, matching the other backends.
const grepMaxContentLen = 500

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
	Path    string `json:"path"`
	Content string `json:"content"`
}

type FileWriteResponse struct {
	OK bool `json:"ok"`
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
	Files []string `json:"files"`
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
	Matches []GrepMatch `json:"matches"`
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
	w.Write([]byte(body))
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
		writeJSONResponse(w, map[string]string{"error": err.Error()}, aesKey)
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

	resp := FileReadResponse{
		Content:    content,
		Lines:      lines,
		TotalLines: lines, // not scanning whole file for total
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
		writeJSONResponse(w, map[string]string{"error": err.Error()}, aesKey)
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
		writeJSONResponse(w, map[string]string{"error": err.Error()}, aesKey)
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

	if err := os.MkdirAll(filepath.Dir(req.Path), 0755); err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("mkdir: %v", err)}, aesKey)
		return
	}
	if err := os.WriteFile(req.Path, []byte(req.Content), 0644); err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("write: %v", err)}, aesKey)
		return
	}

	writeJSONResponse(w, FileWriteResponse{OK: true}, aesKey)
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
		writeJSONResponse(w, map[string]string{"error": err.Error()}, aesKey)
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
	matches := globWithDepth(req.Path, req.Pattern, maxDepth)

	if len(matches) > 1000 {
		matches = matches[:1000]
	}
	if matches == nil {
		matches = []string{}
	}

	writeJSONResponse(w, FileGlobResponse{Files: matches}, aesKey)
}

// globWithDepth recursively walks root and returns files matching pattern,
// descending at most maxDepth directory levels below root (maxDepth <= 0 =
// unlimited; a file directly inside root is depth 1). A bare pattern (no '/')
// matches basenames at any depth; otherwise the pattern is matched against the
// path relative to root. '**' is supported via doublestar, giving the same
// semantics as the Java PathMatcher used by the Kotlin backends.
func globWithDepth(root, pattern string, maxDepth int) []string {
	matches := make([]string, 0)
	rootClean := filepath.Clean(root)
	rootSeps := strings.Count(rootClean, string(os.PathSeparator))
	// A separator-less pattern matches file names at any depth.
	matchPattern := pattern
	if !strings.Contains(pattern, "/") {
		matchPattern = "**/" + pattern
	}
	filepath.Walk(rootClean, func(path string, info os.FileInfo, err error) error {
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
	return matches
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
		writeJSONResponse(w, map[string]string{"error": err.Error()}, aesKey)
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

	err = filepath.Walk(req.Path, func(path string, info os.FileInfo, err error) error {
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
		grepFile(path, re, &matches)
		if len(matches) >= 500 {
			return filepath.SkipAll
		}
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("walk: %v", err)}, aesKey)
		return
	}

	writeJSONResponse(w, FileGrepResponse{Matches: matches}, aesKey)
}

// grepFile streams a single file line-by-line, appending matches (up to the
// global 500 cap) to matches. Binary files — detected by a NUL byte in the head,
// the same heuristic grep uses — are skipped so they don't emit garbage matches.
func grepFile(path string, re *regexp.Regexp, matches *[]GrepMatch) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	br := bufio.NewReader(f)
	if head, _ := br.Peek(512); bytes.IndexByte(head, 0) >= 0 {
		return // binary file
	}

	scanner := bufio.NewScanner(br)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if re.MatchString(line) {
			if len(line) > grepMaxContentLen {
				line = line[:grepMaxContentLen]
			}
			*matches = append(*matches, GrepMatch{Path: path, Line: lineNo, Content: line})
			if len(*matches) >= 500 {
				return
			}
		}
	}
}
