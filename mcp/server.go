package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/newo-ether/conch/buildinfo"
)

const protocolVersion = "2025-06-18"

var allTools = []Tool{
	{
		Name:        "list_devices",
		Description: "List all configured remote devices with their names, descriptions, and connection URLs. Use this to discover available devices before running commands on them.",
		InputSchema: JSONSchema{
			Type:       "object",
			Properties: map[string]JSONProp{},
		},
	},
	{
		Name:        "shell_execute",
		Description: "Execute a shell command on a remote server via Conch. The command runs in a non-interactive shell (/bin/sh -c or powershell). Output is streamed line-by-line with stdout/stderr markers. Returns the combined output lines and exit code.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device":  {Type: "string", Description: "The target device name"},
				"command": {Type: "string", Description: "The shell command to execute"},
				"timeout_ms": {
					Type:        "integer",
					Description: "Required execution timeout in milliseconds; maximum 1800000 (30 minutes)",
					Minimum:     minShellExecuteTimeoutMs,
					Maximum:     maxShellExecuteTimeoutMs,
				},
				"workdir": {Type: "string", Description: "Working directory for the command (optional)"},
			},
			Required: []string{"device", "command", "timeout_ms"},
		},
	},
	{
		Name:        "shell_start",
		Description: "Start a durable background shell job without first attempting the command synchronously. Returns a job_id for later polling, stopping, and acknowledgement.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device":     {Type: "string", Description: "The target device name"},
				"command":    {Type: "string", Description: "The shell command to execute"},
				"timeout_ms": {Type: "integer", Description: "Job timeout in milliseconds (server-bounded)"},
				"workdir":    {Type: "string", Description: "Working directory for the command (optional)"},
			},
			Required: []string{"device", "command"},
		},
	},
	{
		Name:        "shell_jobs",
		Description: "List durable shell jobs without inlining their retained output. Use shell_job_get to retrieve one result.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device": {Type: "string", Description: "The target device name"},
			},
			Required: []string{"device"},
		},
	},
	{
		Name:        "shell_job_get",
		Description: "Get one durable shell job, including bounded retained output and terminal settlement evidence.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device": {Type: "string", Description: "The target device name"},
				"job_id": {Type: "string", Description: "Durable job identifier"},
			},
			Required: []string{"device", "job_id"},
		},
	},
	{
		Name:        "shell_job_stop",
		Description: "Request cancellation of a running durable shell job and its process tree.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device": {Type: "string", Description: "The target device name"},
				"job_id": {Type: "string", Description: "Durable job identifier"},
			},
			Required: []string{"device", "job_id"},
		},
	},
	{
		Name:        "shell_job_ack",
		Description: "Acknowledge and delete one durably settled terminal job after the caller has retained its result. Running or settling jobs are rejected.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device": {Type: "string", Description: "The target device name"},
				"job_id": {Type: "string", Description: "Durable job identifier"},
			},
			Required: []string{"device", "job_id"},
		},
	},
	{
		Name:        "file_read",
		Description: "Read a bounded file range from a remote device. Returns content plus size and explicit truncation metadata.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device": {Type: "string", Description: "The target device name"},
				"path":   {Type: "string", Description: "Absolute path to the file"},
				"offset": {Type: "integer", Description: "Byte offset to start reading from (optional, default 0)"},
				"limit":  {Type: "integer", Description: "Maximum bytes to read (optional, default 1MB)"},
			},
			Required: []string{"device", "path"},
		},
	},
	{
		Name:        "view_image",
		Description: "Read and inspect an image from a remote device. Returns standard MCP image content so a vision-capable model can see the file.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device": {Type: "string", Description: "The target device name"},
				"path":   {Type: "string", Description: "Absolute path to the image file"},
			},
			Required: []string{"device", "path"},
		},
	},
	{
		Name:        "file_write",
		Description: "Atomically write content on a remote device, preserving existing permissions and optionally enforcing a SHA-256 precondition.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device":          {Type: "string", Description: "The target device name"},
				"path":            {Type: "string", Description: "Absolute path to the file"},
				"content":         {Type: "string", Description: "Content to write to the file"},
				"expected_sha256": {Type: "string", Description: "Optional SHA-256 precondition; the write fails if the current file differs"},
			},
			Required: []string{"device", "path", "content"},
		},
	},
	{
		Name:        "file_edit",
		Description: "Edit a file on a remote device by replacing old_string with new_string. The old_string must match exactly once in the file (or set replace_all to replace all occurrences). For several changes to one file, use file_multi_edit.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device":          {Type: "string", Description: "The target device name"},
				"path":            {Type: "string", Description: "Absolute path to the file"},
				"old_string":      {Type: "string", Description: "The exact text to find and replace"},
				"new_string":      {Type: "string", Description: "The replacement text (may be empty to delete the match)"},
				"replace_all":     {Type: "boolean", Description: "Replace all occurrences instead of requiring a unique match (default: false)"},
				"expected_sha256": {Type: "string", Description: "Optional SHA-256 precondition; the edit fails if the current file differs"},
			},
			Required: []string{"device", "path", "old_string", "new_string"},
		},
	},
	{
		Name:        "file_multi_edit",
		Description: "Apply several replacements to one file on a remote device in a single atomic operation. Edits apply in order, each sees the result of the previous ones, and nothing is written unless every edit succeeds.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device":          {Type: "string", Description: "The target device name"},
				"path":            {Type: "string", Description: "Absolute path to the file"},
				"expected_sha256": {Type: "string", Description: "Optional SHA-256 precondition; the edit fails if the current file differs"},
				"edits": {
					Type:        "array",
					Description: "Ordered replacements (1 to 100); each old_string must match exactly once unless its replace_all is true.",
					Items: &JSONSchema{
						Type: "object",
						Properties: map[string]JSONProp{
							"old_string":  {Type: "string", Description: "The exact text to find"},
							"new_string":  {Type: "string", Description: "The replacement text"},
							"replace_all": {Type: "boolean", Description: "Replace all occurrences (default: false)"},
						},
						Required: []string{"old_string", "new_string"},
					},
				},
			},
			Required: []string{"device", "path", "edits"},
		},
	},
	{
		Name:        "file_glob",
		Description: "List files on a remote device matching a glob pattern. Supports * and ** wildcards and reports truncation.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device":  {Type: "string", Description: "The target device name"},
				"pattern": {Type: "string", Description: "Glob pattern (e.g. '*.go', '**/*.md')"},
				"path":    {Type: "string", Description: "Base directory (optional, defaults to current directory)"},
			},
			Required: []string{"device", "pattern"},
		},
	},
	{
		Name:        "file_grep",
		Description: "Search for a regex pattern in files on a remote device. Returns matching lines with file paths and line numbers.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device":           {Type: "string", Description: "The target device name"},
				"pattern":          {Type: "string", Description: "Regular expression pattern to search for (RE2 syntax)"},
				"path":             {Type: "string", Description: "File or directory to search in (optional, defaults to current directory)"},
				"glob":             {Type: "string", Description: "Filter files by glob pattern, e.g. '*.go' or 'src/**/*.kt' (optional)"},
				"case_insensitive": {Type: "boolean", Description: "Match case-insensitively (default: false)"},
				"context":          {Type: "integer", Description: "Lines of context before and after each match (0-10, default 0)", Minimum: 0, Maximum: 10},
			},
			Required: []string{"device", "pattern"},
		},
	},
}

// Server is a stdio-based MCP server.
type Server struct {
	transports       map[string]*Transport
	devices          map[string]DeviceConfig
	unavailable      map[string]string
	imageConcurrency chan struct{}
}

func NewServer(transports map[string]*Transport, devices map[string]DeviceConfig) *Server {
	return NewServerWithUnavailable(transports, devices, nil)
}

// NewServerWithUnavailable keeps failed configured devices visible without allowing one failed
// initialization to remove healthy devices from the MCP server.
func NewServerWithUnavailable(
	transports map[string]*Transport,
	devices map[string]DeviceConfig,
	unavailable map[string]string,
) *Server {
	return &Server{
		transports:       transports,
		devices:          devices,
		unavailable:      unavailable,
		imageConcurrency: make(chan struct{}, 2),
	}
}

// Run starts the stdio server. Closing stdin on cancellation unblocks the scanner during service
// shutdown; Serve contains the testable concurrent JSON-RPC loop.
func (s *Server) Run(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = os.Stdin.Close()
		case <-done:
		}
	}()
	err := s.Serve(ctx, os.Stdin, os.Stdout)
	close(done)
	return err
}

func (s *Server) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	ctx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var requestWG sync.WaitGroup
	var writerMu sync.Mutex
	var pendingMu sync.Mutex
	pending := make(map[string]context.CancelFunc)
	concurrency := make(chan struct{}, 16)
	requestSlots := make(chan struct{}, 128)

	var writeErr error
	var writeErrMu sync.Mutex
	var closeReaderOnce sync.Once
	writeResponse := func(response Response) {
		writerMu.Lock()
		err := writeJSON(writer, response)
		writerMu.Unlock()
		if err != nil {
			writeErrMu.Lock()
			if writeErr == nil {
				writeErr = fmt.Errorf("write response: %w", err)
				cancelAll()
				closeReaderOnce.Do(func() {
					if closer, ok := reader.(io.Closer); ok {
						_ = closer.Close()
					}
				})
			}
			writeErrMu.Unlock()
		}
	}

scanLoop:
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}

		var base struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &base); err != nil {
			log.Printf("ERROR: invalid JSON-RPC: %v", err)
			writeResponse(Response{
				JSONRPC: "2.0",
				Error:   &Error{Code: -32700, Message: "parse error"},
			})
			continue
		}
		if base.JSONRPC != "2.0" {
			writeResponse(Response{
				JSONRPC: "2.0",
				ID:      base.ID,
				Error:   &Error{Code: -32600, Message: "invalid request"},
			})
			continue
		}

		if base.ID == nil {
			switch base.Method {
			case "initialized":
				log.Println("MCP client initialized")
			case "notifications/cancelled", "cancelled":
				var params struct {
					RequestID any `json:"requestId"`
				}
				if json.Unmarshal(base.Params, &params) == nil {
					key := requestIDKey(params.RequestID)
					pendingMu.Lock()
					cancel := pending[key]
					pendingMu.Unlock()
					if cancel != nil {
						cancel()
					}
				}
			}
			continue
		}

		select {
		case requestSlots <- struct{}{}:
		case <-ctx.Done():
			break scanLoop
		default:
			writeResponse(Response{
				JSONRPC: "2.0",
				ID:      base.ID,
				Error:   &Error{Code: -32000, Message: "too many pending requests"},
			})
			continue
		}

		requestCtx, cancel := context.WithCancel(ctx)
		requestKey := requestIDKey(base.ID)
		pendingMu.Lock()
		if pending[requestKey] != nil {
			pendingMu.Unlock()
			cancel()
			<-requestSlots
			writeResponse(Response{
				JSONRPC: "2.0",
				ID:      base.ID,
				Error:   &Error{Code: -32600, Message: "duplicate request id"},
			})
			continue
		}
		pending[requestKey] = cancel
		pendingMu.Unlock()

		requestWG.Add(1)
		go func(base struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}) {
			defer requestWG.Done()
			defer func() { <-requestSlots }()
			defer cancel()
			defer func() {
				pendingMu.Lock()
				delete(pending, requestKey)
				pendingMu.Unlock()
			}()

			select {
			case concurrency <- struct{}{}:
				defer func() { <-concurrency }()
			case <-requestCtx.Done():
				writeResponse(Response{
					JSONRPC: "2.0",
					ID:      base.ID,
					Error:   &Error{Code: -32800, Message: "request cancelled"},
				})
				return
			}

			var result any
			var rpcErr *Error
			switch base.Method {
			case "initialize":
				result = InitializeResult{
					ProtocolVersion: protocolVersion,
					Capabilities:    ServerCapabilities{Tools: &struct{}{}},
					ServerInfo: ServerInfo{
						Name:    "conch-mcp",
						Version: buildinfo.Version,
					},
				}
			case "tools/list":
				result = s.buildToolsList()
			case "tools/call":
				result, rpcErr = s.handleToolsCall(requestCtx, base.Params)
			default:
				rpcErr = &Error{
					Code:    -32601,
					Message: fmt.Sprintf("unknown method: %s", base.Method),
				}
			}
			if requestCtx.Err() != nil {
				result = nil
				rpcErr = &Error{Code: -32800, Message: "request cancelled"}
			}
			writeResponse(Response{
				JSONRPC: "2.0",
				ID:      base.ID,
				Result:  result,
				Error:   rpcErr,
			})
		}(base)
	}

	cancelAll()
	requestWG.Wait()
	if err := scanner.Err(); err != nil {
		return err
	}
	writeErrMu.Lock()
	defer writeErrMu.Unlock()
	return writeErr
}

func requestIDKey(id any) string {
	data, err := json.Marshal(id)
	if err != nil {
		return fmt.Sprintf("%T:%v", id, id)
	}
	return string(data)
}

func (s *Server) buildToolsList() ToolsListResult {
	single := len(s.transports) == 1

	tools := make([]Tool, len(allTools))
	for i, t := range allTools {
		tools[i] = t
		if single {
			req := make([]string, 0)
			for _, r := range t.InputSchema.Required {
				if r != "device" {
					req = append(req, r)
				}
			}
			tools[i].InputSchema.Required = req
		}
	}

	return ToolsListResult{Tools: tools}
}

func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) (any, *Error) {
	var p ToolCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &Error{Code: -32602, Message: "invalid params"}
	}

	switch p.Name {
	case "list_devices":
		return s.doListDevices(), nil
	case "shell_execute":
		transport, errText := s.resolveDevice(p.Arguments)
		if errText != "" {
			return errorResult(errText), nil
		}
		return s.doShellExecute(ctx, transport, p.Arguments)
	case "shell_start", "shell_jobs", "shell_job_get", "shell_job_stop", "shell_job_ack":
		transport, errText := s.resolveDevice(p.Arguments)
		if errText != "" {
			return errorResult(errText), nil
		}
		return s.doShellJob(ctx, transport, p.Name, p.Arguments)
	case "file_read":
		transport, errText := s.resolveDevice(p.Arguments)
		if errText != "" {
			return errorResult(errText), nil
		}
		return s.doFileRead(ctx, transport, p.Arguments)
	case "view_image":
		transport, errText := s.resolveDevice(p.Arguments)
		if errText != "" {
			return errorResult(errText), nil
		}
		// Image responses can each retain tens of MiB across encryption, JSON, and base64 layers.
		// Bound them separately from ordinary request concurrency.
		select {
		case s.imageConcurrency <- struct{}{}:
			defer func() { <-s.imageConcurrency }()
		case <-ctx.Done():
			return errorResult("view_image cancelled"), nil
		}
		return s.doViewImage(ctx, transport, p.Arguments)
	case "file_write":
		transport, errText := s.resolveDevice(p.Arguments)
		if errText != "" {
			return errorResult(errText), nil
		}
		return s.doFileWrite(ctx, transport, p.Arguments)
	case "file_edit":
		transport, errText := s.resolveDevice(p.Arguments)
		if errText != "" {
			return errorResult(errText), nil
		}
		return s.doFileEdit(ctx, transport, p.Arguments)
	case "file_multi_edit":
		transport, errText := s.resolveDevice(p.Arguments)
		if errText != "" {
			return errorResult(errText), nil
		}
		return s.doFileMultiEdit(ctx, transport, p.Arguments)
	case "file_glob":
		transport, errText := s.resolveDevice(p.Arguments)
		if errText != "" {
			return errorResult(errText), nil
		}
		return s.doFileGlob(ctx, transport, p.Arguments)
	case "file_grep":
		transport, errText := s.resolveDevice(p.Arguments)
		if errText != "" {
			return errorResult(errText), nil
		}
		return s.doFileGrep(ctx, transport, p.Arguments)
	default:
		return nil, &Error{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", p.Name)}
	}
}

func (s *Server) resolveDevice(args map[string]interface{}) (*Transport, string) {
	device, _ := args["device"].(string)
	if device == "" {
		if len(s.transports) == 1 {
			for n := range s.transports {
				device = n
			}
		} else {
			return nil, fmt.Sprintf("device is required. Available: %v", deviceNames(s.transports))
		}
	}
	t, ok := s.transports[device]
	if !ok {
		if reason, configured := s.unavailable[device]; configured {
			return nil, fmt.Sprintf("device '%s' is unavailable: %s", device, reason)
		}
		return nil, fmt.Sprintf("unknown device '%s'. Available: %v", device, deviceNames(s.transports))
	}
	return t, ""
}

// --- list_devices ---

func (s *Server) doListDevices() ToolCallResult {
	type DeviceInfo struct {
		Name            string   `json:"name"`
		Description     string   `json:"description"`
		URL             string   `json:"url"`
		Version         string   `json:"version,omitempty"`
		Revision        string   `json:"revision,omitempty"`
		ProtocolVersion string   `json:"protocol_version,omitempty"`
		Capabilities    []string `json:"capabilities,omitempty"`
		Error           string   `json:"error,omitempty"`
	}
	var devices []DeviceInfo
	names := make([]string, 0, len(s.devices))
	for name := range s.devices {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cfg := s.devices[name]
		transport, configured := s.transports[name]
		errorText := s.unavailable[name]
		var metadata buildinfo.Metadata
		if configured {
			_, errorText = transport.Status()
			metadata = transport.Metadata()
		}
		devices = append(devices, DeviceInfo{
			Name:            name,
			Description:     cfg.Description,
			URL:             cfg.URL,
			Version:         metadata.Version,
			Revision:        metadata.Revision,
			ProtocolVersion: metadata.ProtocolVersion,
			Capabilities:    metadata.Capabilities,
			Error:           errorText,
		})
	}
	// Fallback: list transports if no device configs stored
	if len(devices) == 0 {
		names = names[:0]
		for name := range s.transports {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			transport := s.transports[name]
			_, errorText := transport.Status()
			metadata := transport.Metadata()
			devices = append(devices, DeviceInfo{
				Name:            name,
				Version:         metadata.Version,
				Revision:        metadata.Revision,
				ProtocolVersion: metadata.ProtocolVersion,
				Capabilities:    metadata.Capabilities,
				Error:           errorText,
			})
		}
	}
	data, _ := json.Marshal(devices)
	return textResult(string(data))
}

// --- shell_execute ---

func (s *Server) doShellExecute(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	command, _ := args["command"].(string)
	if command == "" {
		return errorResult("command is required"), nil
	}
	timeoutMs, err := requiredShellExecuteTimeoutMs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	workdir, _ := args["workdir"].(string)

	events, err := t.Execute(ctx, command, timeoutMs, workdir)
	if err != nil {
		return errorResult(fmt.Sprintf("Conch error: %v", err)), nil
	}
	return formatShellOutput(events), nil
}

func (s *Server) doShellJob(
	ctx context.Context,
	t *Transport,
	toolName string,
	args map[string]interface{},
) (ToolCallResult, *Error) {
	var result any
	switch toolName {
	case "shell_start":
		command, _ := args["command"].(string)
		if command == "" {
			return errorResult("command is required"), nil
		}
		timeoutMs := 0
		if value, ok := args["timeout_ms"].(float64); ok {
			timeoutMs = int(value)
		}
		workdir, _ := args["workdir"].(string)
		job, err := t.StartJob(ctx, command, timeoutMs, workdir)
		if err != nil {
			return errorResult(fmt.Sprintf("shell_start error: %v", err)), nil
		}
		result = job
	case "shell_jobs":
		jobs, err := t.ListJobs(ctx)
		if err != nil {
			return errorResult(fmt.Sprintf("shell_jobs error: %v", err)), nil
		}
		result = jobs
	case "shell_job_get", "shell_job_stop", "shell_job_ack":
		jobID, _ := args["job_id"].(string)
		if jobID == "" {
			return errorResult("job_id is required"), nil
		}
		switch toolName {
		case "shell_job_get":
			job, err := t.GetJob(ctx, jobID)
			if err != nil {
				return errorResult(fmt.Sprintf("shell_job_get error: %v", err)), nil
			}
			result = job
		case "shell_job_stop":
			job, err := t.StopJob(ctx, jobID)
			if err != nil {
				return errorResult(fmt.Sprintf("shell_job_stop error: %v", err)), nil
			}
			result = job
		case "shell_job_ack":
			ack, err := t.AcknowledgeJob(ctx, jobID)
			if err != nil {
				return errorResult(fmt.Sprintf("shell_job_ack error: %v", err)), nil
			}
			result = ack
		}
	default:
		return errorResult("unknown shell job action"), nil
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("encode shell job result: %v", err)), nil
	}
	return ToolCallResult{
		Content: []ContentItem{{Type: "text", Text: string(data)}},
	}, nil
}

func formatShellOutput(events []LineEvent) ToolCallResult {
	var output strings.Builder
	hasError := false
	for _, evt := range events {
		switch {
		case evt.Warning != "":
			fmt.Fprintf(&output, "[warning] %s\n", evt.Warning)
		case evt.Error != "":
			fmt.Fprintf(&output, "[error] %s\n", evt.Error)
			hasError = true
		case evt.ExitCode != nil:
			fmt.Fprintf(&output, "\nExit code: %d\n", *evt.ExitCode)
		case evt.Stream == "stderr":
			fmt.Fprintf(&output, "[stderr] %s\n", evt.Line)
		default:
			fmt.Fprintf(&output, "%s\n", evt.Line)
		}
	}
	text := output.String()
	if text == "" {
		text = "(no output)"
	}
	// The complete payload lives in the content text. Emitting a metadata-only
	// structuredContent would make MCP clients that prefer structuredContent
	// (per spec) render an empty shell instead of the output.
	return ToolCallResult{
		Content: []ContentItem{{Type: "text", Text: text}},
		IsError: hasError,
	}
}

// --- file_read ---

func (s *Server) doFileRead(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	path, _ := args["path"].(string)
	if path == "" {
		return errorResult("path is required"), nil
	}
	var offset, limit int64
	if v, ok := args["offset"].(float64); ok {
		offset = int64(v)
	}
	if v, ok := args["limit"].(float64); ok {
		limit = int64(v)
	}

	result, err := t.FileRead(ctx, path, offset, limit)
	if err != nil {
		return errorResult(fmt.Sprintf("file_read error: %v", err)), nil
	}
	content := []ContentItem{{Type: "text", Text: result.Content}}
	if result.Truncated {
		content = append(content, ContentItem{
			Type: "text",
			Text: fmt.Sprintf(
				"Warning: file_read returned a partial range (offset %d, size %d bytes)",
				offset,
				result.Size,
			),
		})
	}
	return ToolCallResult{
		Content: content,
	}, nil
}

// --- view_image ---

func (s *Server) doViewImage(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	path, _ := args["path"].(string)
	if path == "" {
		return errorResult("path is required"), nil
	}
	result, err := t.FileImage(ctx, path)
	if err != nil {
		return errorResult(fmt.Sprintf("view_image error: %v", err)), nil
	}
	return ToolCallResult{
		Content: []ContentItem{
			{Type: "text", Text: fmt.Sprintf("Loaded image %s (%d bytes)", path, result.Size)},
			{Type: "image", Data: result.Data, MimeType: result.MimeType},
		},
	}, nil
}

// --- file_write ---

func (s *Server) doFileWrite(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	expectedSHA256, _ := args["expected_sha256"].(string)
	if path == "" {
		return errorResult("path is required"), nil
	}
	result, err := t.FileWriteCAS(ctx, path, content, expectedSHA256)
	if err != nil {
		return errorResult(fmt.Sprintf("file_write error: %v", err)), nil
	}
	return textResult(fmt.Sprintf("file written successfully (sha256 %s)", result.SHA256)), nil
}

// --- file_edit / file_multi_edit ---

// doFileMultiEdit reads only the edits array. Top-level single-edit fields are
// not part of its schema and are ignored.
func (s *Server) doFileMultiEdit(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	path, _ := args["path"].(string)
	expectedSHA256, _ := args["expected_sha256"].(string)
	if path == "" {
		return errorResult("path is required"), nil
	}
	rawEdits, present := args["edits"]
	if !present || rawEdits == nil {
		return errorResult("edits is required"), nil
	}
	edits, parseErr := parseFileEditOperations(rawEdits)
	if parseErr != "" {
		return errorResult(parseErr), nil
	}
	result, err := t.FileEditBatch(ctx, path, edits, expectedSHA256)
	if err != nil {
		return errorResult(fmt.Sprintf("file_multi_edit error: %v", err)), nil
	}
	return textResult(fmt.Sprintf(
		"applied %d edits, %d replacements (sha256 %s)",
		len(edits),
		result.Replacements,
		result.SHA256,
	)), nil
}

// doFileEdit performs exactly one replacement. Batches belong to file_multi_edit.
func (s *Server) doFileEdit(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	path, _ := args["path"].(string)
	oldStr, _ := args["old_string"].(string)
	newStr, _ := args["new_string"].(string)
	replaceAll, _ := args["replace_all"].(bool)
	expectedSHA256, _ := args["expected_sha256"].(string)

	if path == "" {
		return errorResult("path is required"), nil
	}
	if rawEdits, present := args["edits"]; present && rawEdits != nil {
		return errorResult("file_edit takes a single replacement; use file_multi_edit for edits"), nil
	}
	if oldStr == "" {
		return errorResult("old_string is required"), nil
	}

	result, err := t.FileEdit(ctx, path, oldStr, newStr, replaceAll, expectedSHA256)
	if err != nil {
		return errorResult(fmt.Sprintf("file_edit error: %v", err)), nil
	}
	if replaceAll {
		return textResult(fmt.Sprintf(
			"Replaced %d occurrences (sha256 %s)",
			result.Replacements,
			result.SHA256,
		)), nil
	}
	return textResult(fmt.Sprintf("replaced 1 occurrence (sha256 %s)", result.SHA256)), nil
}

// --- file_glob ---

func (s *Server) doFileGlob(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	pattern, _ := args["pattern"].(string)
	basePath, _ := args["path"].(string)
	if pattern == "" {
		return errorResult("pattern is required"), nil
	}

	files, truncated, err := t.FileGlob(ctx, pattern, basePath)
	if err != nil {
		return errorResult(fmt.Sprintf("file_glob error: %v", err)), nil
	}
	text := "(no matches)"
	if len(files) > 0 {
		text = strings.Join(files, "\n")
	}
	content := []ContentItem{{Type: "text", Text: text}}
	if truncated {
		content = append(content, ContentItem{
			Type: "text",
			Text: "Warning: file_glob results were truncated at 1000 paths",
		})
	}
	return ToolCallResult{
		Content: content,
	}, nil
}

// --- file_grep ---

func (s *Server) doFileGrep(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	pattern, _ := args["pattern"].(string)
	basePath, _ := args["path"].(string)
	fileGlob, _ := args["glob"].(string)
	caseInsensitive, _ := args["case_insensitive"].(bool)
	contextLines := 0
	if v, ok := args["context"].(float64); ok {
		contextLines = min(max(int(v), 0), 10)
	}
	if pattern == "" {
		return errorResult("pattern is required"), nil
	}

	result, err := t.FileGrep(ctx, FileGrepOptions{
		Pattern:         pattern,
		Path:            basePath,
		Glob:            fileGlob,
		CaseInsensitive: caseInsensitive,
		Context:         contextLines,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("file_grep error: %v", err)), nil
	}
	text := "(no matches)"
	if len(result.Matches) > 0 {
		var sb strings.Builder
		for i, m := range result.Matches {
			if contextLines > 0 && i > 0 {
				sb.WriteString("--\n")
			}
			for j, line := range m.Before {
				sb.WriteString(fmt.Sprintf("%s-%d- %s\n", m.Path, m.Line-len(m.Before)+j, line))
			}
			sb.WriteString(fmt.Sprintf("%s:%d: %s\n", m.Path, m.Line, m.Content))
			for j, line := range m.After {
				sb.WriteString(fmt.Sprintf("%s-%d- %s\n", m.Path, m.Line+1+j, line))
			}
		}
		text = strings.TrimSpace(sb.String())
	}
	content := []ContentItem{{Type: "text", Text: text}}
	if result.Truncated {
		content = append(content, ContentItem{
			Type: "text",
			Text: "Warning: file_grep results were truncated at 500 matches",
		})
	}
	if result.SkippedLargeFiles > 0 {
		content = append(content, ContentItem{
			Type: "text",
			Text: fmt.Sprintf("Note: %d files over 500KB were not searched", result.SkippedLargeFiles),
		})
	}
	if contextLines > 0 && !result.ContextSupported {
		content = append(content, ContentItem{
			Type: "text",
			Text: "Note: the target Conch server is older and returned no context lines",
		})
	}
	return ToolCallResult{
		Content: content,
	}, nil
}

// parseFileEditOperations converts the JSON-decoded edits argument. The returned
// string is a client-facing error, empty on success.
func parseFileEditOperations(raw interface{}) ([]FileEditOperation, string) {
	items, ok := raw.([]interface{})
	if !ok {
		return nil, "edits must be an array"
	}
	if len(items) == 0 {
		return nil, "edits must not be empty"
	}
	edits := make([]FileEditOperation, 0, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Sprintf("edits[%d] must be an object", i)
		}
		oldStr, _ := obj["old_string"].(string)
		if oldStr == "" {
			return nil, fmt.Sprintf("edits[%d]: old_string is required", i)
		}
		newStr, _ := obj["new_string"].(string)
		replaceAll, _ := obj["replace_all"].(bool)
		edits = append(edits, FileEditOperation{OldString: oldStr, NewString: newStr, ReplaceAll: replaceAll})
	}
	return edits, ""
}

// --- helpers ---

func textResult(text string) ToolCallResult {
	return ToolCallResult{
		Content: []ContentItem{{Type: "text", Text: text}},
	}
}

func errorResult(text string) ToolCallResult {
	return ToolCallResult{
		Content: []ContentItem{{Type: "text", Text: "Error: " + text}},
		IsError: true,
	}
}

func deviceNames(transports map[string]*Transport) []string {
	names := make([]string, 0, len(transports))
	for n := range transports {
		names = append(names, n)
	}
	return names
}

func writeJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}
