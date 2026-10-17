package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	"unicode/utf8"

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

// grepMaxMatches bounds /file/grep results.
const grepMaxMatches = 500

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
	// BytesRead is the number of raw file bytes consumed from Offset, including any
	// leading continuation bytes dropped by UTF-8 trimming. Clients paging through a
	// file continue at Offset+BytesRead; len(Content) can differ when the file holds
	// invalid UTF-8 that JSON encoding replaces.
	BytesRead int64 `json:"bytesRead"`
	// SHA256 is the hash of the whole file, so a client can pass it as expected_sha256 to a
	// later write or edit. Omitted for files larger than maxEditableFileSize; older servers
	// omit it too.
	SHA256 string `json:"sha256,omitempty"`
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
	// Edits applies several replacements in order to the in-memory content and
	// writes once; any failing edit aborts the whole request. It is mutually
	// exclusive with the single-edit OldString/NewString/ReplaceAll fields.
	Edits []FileEditOperation `json:"edits,omitempty"`
}

// FileEditOperation is one replacement inside a batched FileEditRequest.
type FileEditOperation struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// maxFileEditOperations bounds the work a single batched edit request can demand.
const maxFileEditOperations = 100

type FileEditResponse struct {
	OK           bool   `json:"ok"`
	Replacements int    `json:"replacements"`
	SHA256       string `json:"sha256"`
	// Edits is the number of edit operations applied; older servers omit it,
	// which lets clients detect batched-edit support from a response.
	Edits int `json:"edits"`
	// Snippets shows the edited file around each changed region with a few lines
	// of context. Older servers omit it; clients must treat it as optional.
	Snippets          []FileEditSnippet `json:"snippets,omitempty"`
	SnippetsTruncated bool              `json:"snippets_truncated,omitempty"`
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
// Glob uses the same semantics as /file/glob: a pattern without '/' matches file
// basenames at any depth, otherwise it matches the path relative to Path ('**'
// supported). Context requests up to grepMaxContextLines lines before and after
// each match.
type FileGrepRequest struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Glob            string `json:"glob"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	Context         int    `json:"context,omitempty"`
}

type GrepMatch struct {
	Path    string   `json:"path"`
	Line    int      `json:"line"`
	Content string   `json:"content"`
	Before  []string `json:"before,omitempty"`
	After   []string `json:"after,omitempty"`
}

type FileGrepResponse struct {
	Matches   []GrepMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
	// SkippedLargeFiles counts candidate files skipped for exceeding grepMaxFileSize,
	// so an empty result is never silently ambiguous.
	SkippedLargeFiles int `json:"skippedLargeFiles"`
	// ContextSupported lets clients distinguish this server from older ones that
	// silently ignore the Context field.
	ContextSupported bool `json:"contextSupported"`
}

// grepMaxContextLines bounds the context lines returned on each side of a match.
const grepMaxContextLines = 10

// fileSearchMaxGlobMatches bounds /file/glob results.
const fileSearchMaxGlobMatches = 1000

// fileSearchSkippedDirs are version-control metadata directories that file search
// never descends into below the requested root.
var fileSearchSkippedDirs = map[string]bool{".git": true, ".hg": true, ".svn": true}

// decryptBody handles the shared encryption-detection logic.
func decryptBody(r *http.Request, bodyBytes []byte, apiKey []byte, keyPair *crypto.KeyPair) ([]byte, []byte, error) {
	var aesKey []byte
	var plaintext []byte

	if r.Header.Get("X-Encryption") == "v1" || r.Header.Get("X-Encryption") == "v2" {
		clientPubKeyStr := r.Header.Get("X-Client-Public-Key")
		if clientPubKeyStr == "" {
			return nil, nil, fmt.Errorf("missing client public key")
		}
		clientPubKey, err := crypto.DecodePublicKey(clientPubKeyStr)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid client public key: %w", err)
		}
		aesKey, err = crypto.DeriveRequestKey(keyPair.PrivateKey, clientPubKey, r.Header.Get("X-Encryption"))
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
		w.Header().Set(crypto.ResponseSignatureHeader, crypto.ResponseSignature(aesKey, status, []byte(body)))
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
		writePreDispatchError(w, r, h.APIKey, err.Error())
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
	// A single Read may legally return fewer bytes than requested before EOF; fill
	// the buffer so callers never mistake a short read for the end of the range.
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("read: %v", err)}, aesKey)
		return
	}
	// Trim split UTF-8 sequences at both ends: a caller offset may land inside a
	// rune, and Limit may cut one. Only genuinely split runes are trimmed, never
	// invalid bytes, so BytesRead always resumes exactly where the next rune
	// starts and paging can neither skip data nor stall on a corrupt byte.
	//   - Leading: drop at most UTFMax-1 continuation bytes.
	//   - Trailing: drop a final rune prefix that is incomplete only because the
	//     read stopped early; at end of file it is kept as-is.
	contentBytes := buf[:n]
	leadingDropped := 0
	for leadingDropped < utf8.UTFMax-1 && len(contentBytes) > 0 && !utf8.RuneStart(contentBytes[0]) {
		contentBytes = contentBytes[1:]
		leadingDropped++
	}
	if req.Offset+int64(n) < info.Size() {
		for k := 1; k <= min(utf8.UTFMax-1, len(contentBytes)); k++ {
			start := len(contentBytes) - k
			if !utf8.RuneStart(contentBytes[start]) {
				continue
			}
			if !utf8.FullRune(contentBytes[start:]) {
				contentBytes = contentBytes[:start]
			}
			break
		}
	}
	content := string(contentBytes)

	lines := countLines(contentBytes)

	truncated := req.Offset+int64(n) < info.Size()
	// Report the file's true line count whenever the file is small enough for a
	// single bounded read; for larger files the count stays 0 together with
	// truncated=true, which callers read as "not computed".
	totalLines := 0
	if info.Size() <= maxFileSize {
		if _, seekErr := f.Seek(0, io.SeekStart); seekErr == nil {
			totalLines = countTotalLines(io.LimitReader(f, maxFileSize+1))
		}
	}
	// A single read that covered the whole file hashes exactly the bytes returned; otherwise the
	// file is hashed again from the start.
	fileHash := ""
	if req.Offset == 0 && int64(n) == info.Size() {
		fileHash = crypto.SHA256Hex(buf[:n])
	} else if info.Size() <= maxEditableFileSize {
		if _, seekErr := f.Seek(0, io.SeekStart); seekErr == nil {
			hasher := sha256.New()
			if _, copyErr := io.Copy(hasher, io.LimitReader(f, maxEditableFileSize+1)); copyErr == nil {
				fileHash = hex.EncodeToString(hasher.Sum(nil))
			}
		}
	}
	resp := FileReadResponse{
		Content:    content,
		Lines:      lines,
		TotalLines: totalLines,
		Size:       info.Size(),
		Truncated:  truncated,
		BytesRead:  int64(leadingDropped + len(contentBytes)),
		SHA256:     fileHash,
	}
	writeJSONResponse(w, resp, aesKey)
}

// countTotalLines counts the whole file with the same convention as countLines.
func countTotalLines(r io.Reader) int {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0
	}
	return countLines(data)
}

// countLines counts one line per '\n' plus a final unterminated line, so a
// trailing newline does not add a phantom empty line.
func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	total := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		total++
	}
	return total
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
		writePreDispatchError(w, r, h.APIKey, err.Error())
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
		writePreDispatchError(w, r, h.APIKey, err.Error())
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

func fileEditMatchRanges(content string, oldString string) [][2]int {
	normalizedContent := make([]byte, 0, len(content))
	originalBoundaries := make([]int, 1, len(content)+1)
	for originalIndex := 0; originalIndex < len(content); {
		if content[originalIndex] == '\r' && originalIndex+1 < len(content) && content[originalIndex+1] == '\n' {
			normalizedContent = append(normalizedContent, '\n')
			originalIndex += 2
		} else {
			normalizedContent = append(normalizedContent, content[originalIndex])
			originalIndex++
		}
		originalBoundaries = append(originalBoundaries, originalIndex)
	}

	normalizedText := string(normalizedContent)
	normalizedOldString := strings.ReplaceAll(oldString, "\r\n", "\n")
	matches := make([][2]int, 0)
	for searchIndex := 0; searchIndex <= len(normalizedText)-len(normalizedOldString); {
		relativeStart := strings.Index(normalizedText[searchIndex:], normalizedOldString)
		if relativeStart < 0 {
			break
		}
		normalizedStart := searchIndex + relativeStart
		normalizedEnd := normalizedStart + len(normalizedOldString)
		matches = append(matches, [2]int{
			originalBoundaries[normalizedStart],
			originalBoundaries[normalizedEnd],
		})
		searchIndex = normalizedEnd
	}
	return matches
}

// replaceFileEditMatches also returns where each replacement landed in the new content.
func replaceFileEditMatches(content string, matches [][2]int, newString string) (string, [][2]int) {
	if len(matches) == 0 {
		return content, nil
	}

	fileLineEnding := preferredFileEditLineEnding(content)
	var replaced strings.Builder
	replaced.Grow(len(content))
	sourceIndex := 0
	inserted := make([][2]int, 0, len(matches))
	for _, match := range matches {
		replaced.WriteString(content[sourceIndex:match[0]])
		lineEnding := preferredFileEditLineEnding(content[match[0]:match[1]])
		if lineEnding == "" {
			lineEnding = fileLineEnding
		}
		if lineEnding == "" {
			lineEnding = preferredFileEditLineEnding(newString)
		}
		if lineEnding == "" {
			lineEnding = "\n"
		}
		start := replaced.Len()
		replaced.WriteString(withFileEditLineEnding(newString, lineEnding))
		inserted = append(inserted, [2]int{start, replaced.Len()})
		sourceIndex = match[1]
	}
	replaced.WriteString(content[sourceIndex:])
	return replaced.String(), inserted
}

func preferredFileEditLineEnding(text string) string {
	crlfCount := 0
	lfCount := 0
	firstLineEnding := ""
	for index := 0; index < len(text); {
		if text[index] == '\r' && index+1 < len(text) && text[index+1] == '\n' {
			crlfCount++
			if firstLineEnding == "" {
				firstLineEnding = "\r\n"
			}
			index += 2
			continue
		}
		if text[index] == '\n' {
			lfCount++
			if firstLineEnding == "" {
				firstLineEnding = "\n"
			}
		}
		index++
	}
	if crlfCount > lfCount {
		return "\r\n"
	}
	if lfCount > crlfCount {
		return "\n"
	}
	return firstLineEnding
}

func withFileEditLineEnding(text string, lineEnding string) string {
	if !strings.Contains(text, "\n") {
		return text
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	if lineEnding == "\n" {
		return normalized
	}
	return strings.ReplaceAll(normalized, "\n", lineEnding)
}

// fileEditOperations normalizes a request into an ordered list of edits. The
// legacy single-edit fields and the batched Edits array are mutually exclusive.
// The returned string is a client-facing error, empty on success.
func fileEditOperations(req FileEditRequest) ([]FileEditOperation, string) {
	if len(req.Edits) == 0 {
		if req.OldString == "" {
			return nil, "old_string is required"
		}
		return []FileEditOperation{{
			OldString:  req.OldString,
			NewString:  req.NewString,
			ReplaceAll: req.ReplaceAll,
		}}, ""
	}
	if req.OldString != "" || req.NewString != "" || req.ReplaceAll {
		return nil, "edits cannot be combined with old_string, new_string or replace_all"
	}
	if len(req.Edits) > maxFileEditOperations {
		return nil, fmt.Sprintf("too many edits: %d (max %d)", len(req.Edits), maxFileEditOperations)
	}
	for i, op := range req.Edits {
		if op.OldString == "" {
			return nil, fmt.Sprintf("edits[%d]: old_string is required", i)
		}
	}
	return req.Edits, ""
}

// applyFileEditOperations applies edits in order to content. Each edit sees the
// result of the previous ones. Any failure aborts the batch, so callers write
// nothing. batched only changes error wording so single-edit messages stay
// byte-identical to older servers.
func applyFileEditOperations(content string, operations []FileEditOperation, batched bool) (string, int, string) {
	content, total, _, errText := applyFileEditOperationsTracked(content, operations, batched)
	return content, total, errText
}

// applyFileEditOperationsTracked is applyFileEditOperations that also follows the
// changed regions so the response can show them.
func applyFileEditOperationsTracked(content string, operations []FileEditOperation, batched bool) (string, int, *fileEditTracker, string) {
	tracker := &fileEditTracker{}
	total := 0
	for i, op := range operations {
		prefix := ""
		if batched {
			prefix = fmt.Sprintf("edits[%d]: ", i)
		}
		matches := fileEditMatchRanges(content, op.OldString)
		count := len(matches)
		if count == 0 {
			if batched && i > 0 {
				return "", 0, nil, prefix + "old_string not found in file after applying earlier edits; no changes written"
			}
			if batched {
				return "", 0, nil, prefix + "old_string not found in file; no changes written"
			}
			return "", 0, nil, "old_string not found in file"
		}
		if count > 1 && !op.ReplaceAll {
			suffix := ""
			if batched {
				suffix = "; no changes written"
			}
			return "", 0, nil, fmt.Sprintf(
				"%sfound %d matches of old_string; set replace_all=true or provide a unique match%s",
				prefix, count, suffix,
			)
		}
		if !op.ReplaceAll {
			matches = matches[:1]
		}
		var inserted [][2]int
		content, inserted = replaceFileEditMatches(content, matches, op.NewString)
		tracker.apply(matches, inserted)
		total += len(matches)
		if len(content) > maxEditableFileSize {
			return "", 0, nil, "edited file exceeds 16MB limit"
		}
	}
	return content, total, tracker, ""
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
		writePreDispatchError(w, r, h.APIKey, err.Error())
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
	operations, opErr := fileEditOperations(req)
	if opErr != "" {
		writeJSONResponse(w, map[string]string{"error": opErr}, aesKey)
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

	replaced, replacementCount, tracker, applyErr := applyFileEditOperationsTracked(string(data), operations, len(req.Edits) > 0)
	if applyErr != "" {
		writeJSONResponse(w, map[string]string{"error": applyErr}, aesKey)
		return
	}

	hash, err := atomicWriteFileLocked(req.Path, []byte(replaced), currentHash)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("edit: %v", err)}, aesKey)
		return
	}
	snippets, snippetsTruncated := tracker.snippets(replaced)
	writeJSONResponse(w, FileEditResponse{
		OK:                true,
		Replacements:      replacementCount,
		SHA256:            hash,
		Edits:             len(operations),
		Snippets:          snippets,
		SnippetsTruncated: snippetsTruncated,
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
		writePreDispatchError(w, r, h.APIKey, err.Error())
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

	// globWithDepth collects one extra match so an exact-limit result is not
	// reported as truncated.
	truncated := len(matches) > fileSearchMaxGlobMatches
	if truncated {
		matches = matches[:fileSearchMaxGlobMatches]
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
			if fileSearchSkippedDirs[info.Name()] {
				return filepath.SkipDir
			}
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
			if len(matches) > fileSearchMaxGlobMatches {
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
		writePreDispatchError(w, r, h.APIKey, err.Error())
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

	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("regex compile: %v", err)}, aesKey)
		return
	}
	contextLines := min(max(req.Context, 0), grepMaxContextLines)

	root := filepath.Clean(req.Path)
	var globFunc func(string) bool
	if req.Glob != "" {
		// Same semantics as /file/glob: a separator-less glob matches basenames
		// at any depth, otherwise the path relative to root ('**' supported).
		globPattern := req.Glob
		if !strings.Contains(globPattern, "/") {
			globPattern = "**/" + globPattern
		}
		if !doublestar.ValidatePattern(globPattern) {
			writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("invalid glob: %s", req.Glob)}, aesKey)
			return
		}
		globFunc = func(path string) bool {
			rel := filepath.Base(path)
			if path != root {
				if r, relErr := filepath.Rel(root, path); relErr == nil {
					rel = r
				}
			}
			ok, _ := doublestar.Match(globPattern, filepath.ToSlash(rel))
			return ok
		}
	}

	// Collect one match past the cap so an exact-limit result is not reported
	// as truncated.
	limit := grepMaxMatches + 1
	matches := make([]GrepMatch, 0)
	skippedLarge := 0

	searchCtx, cancel := context.WithTimeout(r.Context(), maxFileSearchDuration)
	defer cancel()
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if ctxErr := searchCtx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if path != root && fileSearchSkippedDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if globFunc != nil && !globFunc(path) {
			return nil
		}
		// Skip oversized files so a single huge file can't exhaust memory, but
		// count them so an empty result is never silently ambiguous.
		if info.Size() > grepMaxFileSize {
			skippedLarge++
			return nil
		}
		if err := grepFile(searchCtx, path, re, contextLines, limit, &matches); err != nil {
			return err
		}
		if len(matches) >= limit {
			return filepath.SkipAll
		}
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("walk: %v", err)}, aesKey)
		return
	}

	truncated := len(matches) > grepMaxMatches
	if truncated {
		matches = matches[:grepMaxMatches]
	}
	writeJSONResponse(w, FileGrepResponse{
		Matches:           matches,
		Truncated:         truncated,
		SkippedLargeFiles: skippedLarge,
		ContextSupported:  true,
	}, aesKey)
}

// grepFile searches one file (already bounded by grepMaxFileSize) and appends
// matches until matches reaches limit. The whole file is read at once, so line
// length never aborts the search. Binary files, detected by a NUL byte in the
// first 512 bytes as grep does, are skipped so they don't emit garbage matches.
func grepFile(ctx context.Context, path string, re *regexp.Regexp, contextLines, limit int, matches *[]GrepMatch) error {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	if bytes.IndexByte(data[:min(len(data), 512)], 0) >= 0 {
		return nil
	}

	lines := strings.Split(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // trailing newline does not start a line
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}

	for i, line := range lines {
		if i%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if !re.MatchString(line) {
			continue
		}
		m := GrepMatch{Path: path, Line: i + 1, Content: truncateGrepLine(line)}
		if contextLines > 0 {
			m.Before = truncateGrepLines(lines[max(0, i-contextLines):i])
			m.After = truncateGrepLines(lines[i+1 : min(len(lines), i+1+contextLines)])
		}
		*matches = append(*matches, m)
		if len(*matches) >= limit {
			return nil
		}
	}
	return nil
}

// truncateGrepLine caps a returned line at grepMaxContentLen bytes without
// splitting a multi-byte UTF-8 sequence.
func truncateGrepLine(line string) string {
	if len(line) <= grepMaxContentLen {
		return line
	}
	cut := grepMaxContentLen
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut]
}

func truncateGrepLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = truncateGrepLine(line)
	}
	return out
}
