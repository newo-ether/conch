package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/newo-ether/conch/shell"
)

type ExecuteHandler struct {
	Executor *shell.Executor
}

func (h *ExecuteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
	var req shell.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	events := h.Executor.Execute(r.Context(), req)

	for evt := range events {
		if evt.Error != "" {
			data, _ := json.Marshal(map[string]string{"message": evt.Error})
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", string(data))
		} else if evt.ExitCode != nil {
			data, _ := json.Marshal(map[string]int{"exit_code": *evt.ExitCode})
			fmt.Fprintf(w, "event: result\ndata: %s\n\n", string(data))
		} else {
			data, _ := json.Marshal(map[string]string{
				"line":   evt.Line,
				"stream": evt.Stream,
			})
			fmt.Fprintf(w, "event: line\ndata: %s\n\n", string(data))
		}
		flusher.Flush()
	}
}

func HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}
