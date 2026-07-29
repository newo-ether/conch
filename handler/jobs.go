package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/shell"
)

type JobHandler struct {
	Action  string
	Jobs    *shell.JobManager
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

type jobIDRequest struct {
	JobID string `json:"job_id"`
}

func (h *JobHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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

	switch h.Action {
	case "start":
		var req shell.Request
		if json.Unmarshal(plaintext, &req) != nil {
			writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
			return
		}
		job, err := h.Jobs.Start(req)
		if err != nil {
			writeJSONResponse(w, map[string]string{"error": err.Error()}, aesKey)
			return
		}
		writeJSONResponse(w, jobResponse(job, true), aesKey)
	case "list":
		writeJSONResponse(w, map[string]any{
			"type": "list_shell_jobs",
			"jobs": h.Jobs.List(),
		}, aesKey)
	case "get", "stop":
		var req jobIDRequest
		if json.Unmarshal(plaintext, &req) != nil || req.JobID == "" {
			writeJSONResponse(w, map[string]string{"error": "job_id is required"}, aesKey)
			return
		}
		var job shell.Job
		var ok bool
		if h.Action == "stop" {
			job, ok = h.Jobs.Stop(req.JobID)
		} else {
			job, ok = h.Jobs.Get(req.JobID)
		}
		if !ok {
			writeJSONResponse(w, map[string]string{"error": "job not found"}, aesKey)
			return
		}
		writeJSONResponse(w, jobResponse(job, job.State == shell.JobStateRunning), aesKey)
	default:
		writeJSONResponse(w, map[string]string{"error": "unknown job action"}, aesKey)
	}
}

func jobResponse(job shell.Job, background bool) map[string]any {
	response := map[string]any{
		"type":         "shell_job",
		"background":   background,
		"job_id":       job.JobID,
		"command":      job.Command,
		"workdir":      job.Workdir,
		"state":        job.State,
		"created_at":   job.CreatedAt,
		"started_at":   job.StartedAt,
		"finished_at":  job.FinishedAt,
		"output":       job.Output,
		"output_bytes": job.OutputBytes,
		"truncated":    job.Truncated,
	}
	if job.ExitCode != nil {
		response["exit_code"] = *job.ExitCode
	}
	if job.Error != "" {
		response["error"] = job.Error
	}
	return response
}
