package handler

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const transferTTL = 15 * time.Minute
const maxTransfers = 64

// FileTransfers owns private staging and bounded, process-local completion receipts.
// A small journal lets a new process remove abandoned staging without scanning user folders.
type FileTransfers struct {
	mu      sync.Mutex
	entries map[string]*fileTransfer
	dir     string
	stop    chan struct{}
	done    chan struct{}
}

type fileTransfer struct {
	mu        sync.Mutex
	path      string
	stage     string
	id        string
	size      int64
	sha256    string
	overwrite bool
	offset    int64
	state     string
	touched   time.Time
}

func NewFileTransfers(dir string) (*FileTransfers, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	journals, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, journal := range journals {
		if !strings.HasSuffix(journal.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, journal.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var stage string
		if err := json.Unmarshal(data, &stage); err != nil {
			return nil, err
		}
		if filepath.Base(stage) != ".conch-transfer-"+strings.TrimSuffix(journal.Name(), ".json") {
			return nil, fmt.Errorf("invalid transfer cleanup journal")
		}
		if err := os.Remove(stage); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	s := &FileTransfers{entries: make(map[string]*fileTransfer), dir: dir, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case now := <-ticker.C:
				s.expire(now)
			}
		}
	}()
	return s, nil
}

func (s *FileTransfers) Close() {
	close(s.stop)
	<-s.done
	s.expire(time.Now().Add(2 * transferTTL))
}

// Keep the journal until removal succeeds, so restart can retry abandoned cleanup.
func (s *FileTransfers) cleanupStage(entry *fileTransfer) error {
	if err := os.Remove(entry.stage); err != nil && !os.IsNotExist(err) {
		return err
	}
	err := os.Remove(filepath.Join(s.dir, entry.id+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *FileTransfers) expire(now time.Time) {
	s.mu.Lock()
	entries := make([]*fileTransfer, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	for _, entry := range entries {
		entry.mu.Lock()
		if now.Sub(entry.touched) > transferTTL {
			if err := s.cleanupStage(entry); err != nil {
				entry.mu.Unlock()
				continue
			}
			s.mu.Lock()
			if s.entries[entry.id] == entry {
				delete(s.entries, entry.id)
			}
			s.mu.Unlock()
		}
		entry.mu.Unlock()
	}
}

func (s *FileTransfers) apply(ctx context.Context, action string, req transferRequest) (transferResult, error) {
	if err := ctx.Err(); err != nil {
		return transferResult{}, err
	}
	if action == "begin" {
		return s.begin(req)
	}
	s.mu.Lock()
	entry := s.entries[req.ID]
	s.mu.Unlock()
	if entry == nil {
		return transferResult{}, fmt.Errorf("unknown or expired transfer; publication outcome may require checking destination")
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.touched = time.Now()
	switch action {
	case "status":
		return entry.result(), nil
	case "abort":
		if err := s.cleanupStage(entry); err != nil {
			return transferResult{}, err
		}
		if entry.state != "committed" {
			entry.state = "aborted"
		}
		return entry.result(), nil
	case "chunk":
		if entry.state != "writing" {
			return transferResult{}, fmt.Errorf("transfer is %s", entry.state)
		}
		if len(req.Data) > base64.StdEncoding.EncodedLen(TransferChunkBytes) {
			return transferResult{}, fmt.Errorf("transfer chunk exceeds 256KiB")
		}
		data, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil || len(data) == 0 || len(data) > TransferChunkBytes {
			return transferResult{}, fmt.Errorf("invalid transfer chunk")
		}
		if req.Offset != entry.offset || int64(len(data)) > entry.size-entry.offset {
			return transferResult{}, fmt.Errorf("transfer offset or size mismatch; query status before continuing")
		}
		file, err := os.OpenFile(entry.stage, os.O_WRONLY, 0600)
		if err != nil {
			return transferResult{}, err
		}
		n, err := file.WriteAt(data, entry.offset)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil || n != len(data) {
			entry.state = "failed"
			_ = s.cleanupStage(entry)
			return transferResult{}, fmt.Errorf("staged write failed: %v", err)
		}
		entry.offset += int64(n)
		return entry.result(), nil
	case "commit":
		if entry.state == "committed" {
			return entry.result(), nil
		}
		if entry.state != "writing" || entry.offset != entry.size {
			return transferResult{}, fmt.Errorf("incomplete transfer")
		}
		hash, err := transferInfo(ctx, entry.stage)
		if err != nil || hash.Size != entry.size || hash.SHA256 != entry.sha256 {
			entry.state = "failed"
			_ = s.cleanupStage(entry)
			return transferResult{}, fmt.Errorf("staged file size/hash verification failed: %v", err)
		}
		file, err := os.OpenFile(entry.stage, os.O_WRONLY, 0600)
		if err != nil {
			return transferResult{}, err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return transferResult{}, err
		}
		if err := ctx.Err(); err != nil {
			return transferResult{}, err
		}
		unlock := lockFileMutation(entry.path)
		defer unlock()
		if err := ctx.Err(); err != nil {
			return transferResult{}, err
		}
		if entry.overwrite {
			if info, statErr := os.Lstat(entry.path); statErr == nil && !info.Mode().IsRegular() {
				return transferResult{}, fmt.Errorf("destination is not a regular file")
			} else if statErr != nil && !os.IsNotExist(statErr) {
				return transferResult{}, statErr
			}
			err = replaceFile(entry.stage, entry.path)
		} else {
			// Publish atomically without replacing a concurrently-created destination.
			err = publishFileNoReplace(entry.stage, entry.path)
		}
		if err != nil {
			return transferResult{}, fmt.Errorf("publish destination: %w", err)
		}
		entry.state = "committed"
		_ = s.cleanupStage(entry)
		if err := syncFileDirectory(filepath.Dir(entry.path)); err != nil {
			return transferResult{}, fmt.Errorf("destination published; directory flush failed: %w", err)
		}
		return entry.result(), nil
	default:
		return transferResult{}, fmt.Errorf("unknown transfer action")
	}
}

func (s *FileTransfers) begin(req transferRequest) (transferResult, error) {
	if len(req.ID) != 32 {
		return transferResult{}, fmt.Errorf("transfer id must be 32 hex characters")
	}
	if _, err := hex.DecodeString(req.ID); err != nil {
		return transferResult{}, fmt.Errorf("invalid transfer id")
	}
	if req.Size < 0 || req.Size > TransferMaxBytes || len(req.SHA256) != 64 {
		return transferResult{}, fmt.Errorf("invalid transfer size or sha256")
	}
	if _, err := hex.DecodeString(req.SHA256); err != nil {
		return transferResult{}, fmt.Errorf("invalid transfer sha256")
	}
	path, err := transferPath(req.Path)
	if err != nil {
		return transferResult{}, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return transferResult{}, fmt.Errorf("destination parent must exist: %w", err)
	}
	path = filepath.Join(parent, filepath.Base(path))
	stage := filepath.Join(parent, ".conch-transfer-"+req.ID)
	if path == stage || (runtime.GOOS == "windows" && strings.EqualFold(path, stage)) {
		return transferResult{}, fmt.Errorf("destination conflicts with transfer staging path")
	}
	entry := &fileTransfer{path: path, stage: stage, id: req.ID,
		size: req.Size, sha256: strings.ToLower(req.SHA256), overwrite: req.Overwrite, state: "opening", touched: time.Now()}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	s.mu.Lock()
	if s.entries[req.ID] != nil || len(s.entries) >= maxTransfers {
		s.mu.Unlock()
		return transferResult{}, fmt.Errorf("transfer id already exists or transfer capacity reached")
	}
	s.entries[req.ID] = entry
	s.mu.Unlock()
	stageCreated := false
	journalCreated := false
	journalPath := filepath.Join(s.dir, req.ID+".json")
	fail := func(err error) (transferResult, error) {
		cleanupOK := true
		if stageCreated {
			if removeErr := os.Remove(entry.stage); removeErr != nil && !os.IsNotExist(removeErr) {
				cleanupOK = false
			}
		}
		if journalCreated && cleanupOK {
			_ = os.Remove(journalPath)
		}
		s.mu.Lock()
		delete(s.entries, req.ID)
		s.mu.Unlock()
		return transferResult{}, err
	}
	if info, err := os.Lstat(path); err == nil {
		if !req.Overwrite {
			return fail(fmt.Errorf("destination already exists; overwrite is false"))
		}
		if !info.Mode().IsRegular() {
			return fail(fmt.Errorf("destination is not a regular file"))
		}
	} else if !os.IsNotExist(err) {
		return fail(err)
	}
	journal, err := os.OpenFile(journalPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail(err)
	}
	journalCreated = true
	err = json.NewEncoder(journal).Encode(entry.stage)
	if err == nil {
		err = journal.Sync()
	}
	closeErr := journal.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fail(err)
	}
	if err := syncFileDirectory(s.dir); err != nil {
		return fail(err)
	}
	file, err := os.OpenFile(entry.stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail(err)
	}
	stageCreated = true
	if err := file.Close(); err != nil {
		return fail(err)
	}
	entry.state = "writing"
	return entry.result(), nil
}

func (entry *fileTransfer) result() transferResult {
	return transferResult{OK: true, ID: entry.id, Path: entry.path, Size: entry.size,
		SHA256: entry.sha256, Offset: entry.offset, State: entry.state}
}
