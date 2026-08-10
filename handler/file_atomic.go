package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const fileMutationStripeCount = 256

var fileMutationStripes [fileMutationStripeCount]sync.Mutex

func lockFileMutation(path string) func() {
	canonical, err := filepath.Abs(path)
	if err != nil {
		canonical = filepath.Clean(path)
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(canonical))
	lock := &fileMutationStripes[hash.Sum32()%fileMutationStripeCount]
	lock.Lock()
	return lock.Unlock
}

func atomicWriteFile(path string, data []byte, expectedSHA256 string) (string, error) {
	unlock := lockFileMutation(path)
	defer unlock()
	return atomicWriteFileLocked(path, data, expectedSHA256)
}

func atomicWriteFileLocked(path string, data []byte, expectedSHA256 string) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("target is not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat target: %w", err)
	}

	if expectedSHA256 != "" {
		current, err := fileSHA256(path)
		if err != nil {
			return "", fmt.Errorf("verify expected_sha256: %w", err)
		}
		if !strings.EqualFold(current, expectedSHA256) {
			return "", fmt.Errorf("file changed concurrently: expected sha256 %s, got %s", expectedSHA256, current)
		}
	}

	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tempPath := temp.Name()
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("set temp permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("write temp file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("flush temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}

	// Recheck immediately before replacement. This catches writers outside Conch that raced the
	// edit after its initial read; within Conch, striped locks serialize writes to the same path.
	if expectedSHA256 != "" {
		current, err := fileSHA256(path)
		if err != nil {
			return "", fmt.Errorf("recheck expected_sha256: %w", err)
		}
		if !strings.EqualFold(current, expectedSHA256) {
			return "", fmt.Errorf("file changed concurrently: expected sha256 %s, got %s", expectedSHA256, current)
		}
	}

	if err := replaceFile(tempPath, path); err != nil {
		return "", fmt.Errorf("replace target: %w", err)
	}
	keepTemp = false
	if err := syncFileDirectory(dir); err != nil {
		return "", fmt.Errorf("flush target directory: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
