package handler

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPublishFileNoReplacePreservesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	if err := os.WriteFile(source, []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishFileNoReplace(source, target); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing target error, got %v", err)
	}
	for path, expected := range map[string]string{source: "candidate", target: "existing"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != expected {
			t.Fatalf("publication changed %s: %q (%v)", path, got, err)
		}
	}
}

func TestPublishFileNoReplaceRacesExternalCreator(t *testing.T) {
	dir := t.TempDir()
	for range 32 {
		source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
		if err := os.WriteFile(source, []byte("publisher"), 0600); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var published, created error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			published = publishFileNoReplace(source, target)
		}()
		go func() {
			defer wait.Done()
			<-start
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				_, err = file.Write([]byte("external"))
				if closeErr := file.Close(); err == nil {
					err = closeErr
				}
			}
			created = err
		}()
		close(start)
		wait.Wait()
		expected := "publisher"
		if published == nil {
			if !errors.Is(created, os.ErrExist) {
				t.Fatalf("external creator did not lose atomically: %v", created)
			}
		} else {
			if !errors.Is(published, os.ErrExist) || created != nil {
				t.Fatalf("unexpected race results: publication=%v creation=%v", published, created)
			}
			expected = "external"
			if got, err := os.ReadFile(source); err != nil || string(got) != "publisher" {
				t.Fatalf("failed publication changed source: %q (%v)", got, err)
			}
		}
		if got, err := os.ReadFile(target); err != nil || string(got) != expected {
			t.Fatalf("winner bytes changed: %q, expected %q (%v)", got, expected, err)
		}
		if err := os.Remove(source); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTransferCommitPreservesTargetCreatedAfterBegin(t *testing.T) {
	store := newTestTransfers(t)
	target := filepath.Join(t.TempDir(), "target")
	req := stageTransfer(t, store, target, strings.Repeat("6", 32), []byte("candidate"), false)
	if err := os.WriteFile(target, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.apply(context.Background(), "commit", req); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing target error, got %v", err)
	}
	if transferApply(t, store, "status", req).State != "writing" {
		t.Fatal("failed publication lost its reconciliation state")
	}
	transferApply(t, store, "abort", req)
	if got, err := os.ReadFile(target); err != nil || string(got) != "external" {
		t.Fatal("failed publication or abort changed external target", err)
	}
}

func TestTransferPublishedStageAndJournalCleanup(t *testing.T) {
	store := newTestTransfers(t)
	target := filepath.Join(t.TempDir(), "target")
	data := []byte{255, 0, 128, 13, 10}
	req := stageTransfer(t, store, target, strings.Repeat("7", 32), data, false)
	stage := store.entries[req.ID].stage
	journal := filepath.Join(store.dir, req.ID+".json")
	transferApply(t, store, "commit", req)
	for _, path := range []string{stage, journal} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("published transfer retained staging or journal: %s (%v)", path, err)
		}
	}
	// A rename publication consumes staging; cleanup and abort must allow its absence.
	transferApply(t, store, "abort", req)
	if transferApply(t, store, "status", req).State != "committed" {
		t.Fatal("post-publication abort changed committed receipt")
	}
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, data) {
		t.Fatal("post-publication cleanup changed target", err)
	}
}
