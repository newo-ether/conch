package handler

import (
	"encoding/json"
	"fmt"
	"strings"
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

func TestJobListPagesKeepEveryRetainedJob(t *testing.T) {
	jobs := make([]shell.Job, 125)
	for index := range jobs {
		jobs[index] = shell.Job{JobID: fmt.Sprintf("job-%d", index), Output: "retained"}
	}
	first := jobListPage(jobs, 0, 100)
	second := jobListPage(jobs, first["next_offset"].(int), 100)
	if len(first["jobs"].([]map[string]any)) != 100 || first["has_more"] != true ||
		len(second["jobs"].([]map[string]any)) != 25 || second["has_more"] != false || second["total"] != 125 {
		t.Fatalf("invalid pagination: first=%v second=%v", first, second)
	}
	if second["jobs"].([]map[string]any)[0]["job_id"] != "job-100" || jobs[100].Output != "retained" {
		t.Fatal("pagination skipped or mutated a retained record")
	}
	if page := jobListPage(jobs, 999, 100); len(page["jobs"].([]map[string]any)) != 0 || page["has_more"] != false {
		t.Fatalf("past-end page = %v", page)
	}
}

func TestJobListPageBoundsEscapedMetadata(t *testing.T) {
	jobs := make([]shell.Job, 100)
	for index := range jobs {
		jobs[index] = shell.Job{Command: strings.Repeat("\x00", shell.MaxCommandBytes)}
	}
	page := jobListPage(jobs, 0, 100)
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) > maxJobListBytes+4096 || page["has_more"] != true || page["next_offset"].(int) == 0 {
		t.Fatalf("unbounded page: bytes=%d err=%v", len(encoded), err)
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
