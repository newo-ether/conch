package handler

import (
	"testing"
	"time"

	"github.com/newo-ether/conch/shell"
)

func TestJobListResponseOmitsOutputButKeepsRetrievalMetadata(t *testing.T) {
	exitCode := 0
	finished := time.Now().UTC()
	response := jobListResponse(shell.Job{
		JobID:       "job-1",
		Command:     "printf done",
		State:       shell.JobStateSucceeded,
		CreatedAt:   finished,
		StartedAt:   finished,
		FinishedAt:  &finished,
		ExitCode:    &exitCode,
		Warning:     "output truncated",
		Output:      "large output body",
		OutputBytes: 17,
		Truncated:   true,
	})

	if _, ok := response["output"]; ok {
		t.Fatal("list response must not inline retained job output")
	}
	if response["job_id"] != "job-1" || response["output_bytes"] != int64(17) {
		t.Fatalf("missing retrieval metadata: %#v", response)
	}
	if response["warning"] != "output truncated" {
		t.Fatalf("warning missing from response: %#v", response)
	}
}

func TestStoppingJobRemainsBackgroundActive(t *testing.T) {
	response := jobListResponse(shell.Job{
		JobID:     "job-1",
		State:     shell.JobStateStopping,
		CreatedAt: time.Now().UTC(),
		StartedAt: time.Now().UTC(),
	})

	if response["background"] != true {
		t.Fatalf("stopping job must remain active: %#v", response)
	}
}
