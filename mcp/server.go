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
)

const protocolVersion = "2025-06-18"
const serverVersion = "0.1.0"

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
				"device":     {Type: "string", Description: "The target device name"},
				"command":    {Type: "string", Description: "The shell command to execute"},
				"timeout_ms": {Type: "integer", Description: "Timeout in milliseconds (default: 30000, max: 120000)", Default: 30000},
				"workdir":    {Type: "string", Description: "Working directory for the command (optional)"},
			},
			Required: []string{"device", "command"},
		},
	},
	{
		Name:        "file_read",
		Description: "Read a file from a remote device. Returns the file content as text with line count.",
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
		Description: "Write content to a file on a remote device. Creates parent directories as needed and overwrites existing files.",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device":  {Type: "string", Description: "The target device name"},
				"path":    {Type: "string", Description: "Absolute path to the file"},
				"content": {Type: "string", Description: "Content to write to the file"},
			},
			Required: []string{"device", "path", "content"},
		},
	},
	{
		Name:        "file_edit",
		Description: "Edit a file on a remote device by replacing old_string with new_string. The old_string must match exactly once in the file (or set replace_all to replace all occurrences).",
		InputSchema: JSONSchema{
			Type: "object",
			Properties: map[string]JSONProp{
				"device":      {Type: "string", Description: "The target device name"},
				"path":        {Type: "string", Description: "Absolute path to the file"},
				"old_string":  {Type: "string", Description: "The exact text to find and replace"},
				"new_string":  {Type: "string", Description: "The replacement text"},
				"replace_all": {Type: "boolean", Description: "Replace all occurrences instead of requiring a unique match (default: false)"},
			},
			Required: []string{"device", "path", "old_string", "new_string"},
		},
	},
	{
		Name:        "file_glob",
		Description: "List files and directories on a remote device matching a glob pattern. Supports * and ** wildcards.",
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
				"device":  {Type: "string", Description: "The target device name"},
				"pattern": {Type: "string", Description: "Regular expression pattern to search for (RE2 syntax)"},
				"path":    {Type: "string", Description: "File or directory to search in (optional, defaults to current directory)"},
				"glob":    {Type: "string", Description: "Filter files by glob pattern, e.g. '*.go' (optional)"},
			},
			Required: []string{"device", "pattern"},
		},
	},
}

// Server is a stdio-based MCP server.
type Server struct {
	transports  map[string]*Transport
	devices     map[string]DeviceConfig
	unavailable map[string]string
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
		transports:  transports,
		devices:     devices,
		unavailable: unavailable,
	}
}

// Run starts the stdio read-eval loop. Blocks until stdin closes.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
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
			continue
		}
		if base.JSONRPC != "2.0" {
			continue
		}

		if base.ID == nil {
			if base.Method == "initialized" {
				log.Println("MCP client initialized")
			}
			continue
		}

		var result any
		var rpcErr *Error

		switch base.Method {
		case "initialize":
			result = InitializeResult{
				ProtocolVersion: protocolVersion,
				Capabilities: ServerCapabilities{
					Tools: &struct{}{},
				},
				ServerInfo: ServerInfo{
					Name:    "conch-mcp",
					Version: serverVersion,
				},
			}
		case "tools/list":
			result = s.buildToolsList()
		case "tools/call":
			result, rpcErr = s.handleToolsCall(ctx, base.Params)
		default:
			rpcErr = &Error{Code: -32601, Message: fmt.Sprintf("unknown method: %s", base.Method)}
		}

		resp := Response{
			JSONRPC: "2.0",
			ID:      base.ID,
			Result:  result,
			Error:   rpcErr,
		}
		if err := writeJSON(os.Stdout, resp); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	return scanner.Err()
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
		Name        string `json:"name"`
		Description string `json:"description"`
		URL         string `json:"url"`
		Available   bool   `json:"available"`
		Error       string `json:"error,omitempty"`
	}
	var devices []DeviceInfo
	names := make([]string, 0, len(s.devices))
	for name := range s.devices {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cfg := s.devices[name]
		_, available := s.transports[name]
		devices = append(devices, DeviceInfo{
			Name:        name,
			Description: cfg.Description,
			URL:         cfg.URL,
			Available:   available,
			Error:       s.unavailable[name],
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
			devices = append(devices, DeviceInfo{Name: name, Available: true})
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
	timeoutMs := 30000
	if val, ok := args["timeout_ms"].(float64); ok {
		timeoutMs = int(val)
	}
	workdir, _ := args["workdir"].(string)

	events, err := t.Execute(ctx, command, timeoutMs, workdir)
	if err != nil {
		return errorResult(fmt.Sprintf("Conch error: %v", err)), nil
	}
	return formatShellOutput(events), nil
}

func formatShellOutput(events []LineEvent) ToolCallResult {
	var text string
	hasError := false
	for _, evt := range events {
		switch {
		case evt.Error != "":
			text += fmt.Sprintf("[error] %s\n", evt.Error)
			hasError = true
		case evt.ExitCode != nil:
			text += fmt.Sprintf("\nExit code: %d\n", *evt.ExitCode)
		default:
			if evt.Stream == "stderr" {
				text += fmt.Sprintf("[stderr] %s\n", evt.Line)
			} else {
				text += fmt.Sprintf("%s\n", evt.Line)
			}
		}
	}
	if text == "" {
		text = "(no output)"
	}
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
	return textResult(result.Content), nil
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
	if path == "" {
		return errorResult("path is required"), nil
	}
	if err := t.FileWrite(ctx, path, content); err != nil {
		return errorResult(fmt.Sprintf("file_write error: %v", err)), nil
	}
	return textResult("file written successfully"), nil
}

// --- file_edit ---

func (s *Server) doFileEdit(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	path, _ := args["path"].(string)
	oldStr, _ := args["old_string"].(string)
	newStr, _ := args["new_string"].(string)
	replaceAll, _ := args["replace_all"].(bool)

	if path == "" {
		return errorResult("path is required"), nil
	}
	if oldStr == "" {
		return errorResult("old_string is required"), nil
	}

	// Read the file
	result, err := t.FileRead(ctx, path, 0, 0)
	if err != nil {
		return errorResult(fmt.Sprintf("file_edit read error: %v", err)), nil
	}

	content := result.Content
	count := strings.Count(content, oldStr)
	if count == 0 {
		return errorResult("old_string not found in file"), nil
	}
	if count > 1 && !replaceAll {
		return errorResult(fmt.Sprintf("Found %d matches of old_string. Use replace_all=true to replace all, or provide more context to make it unique.", count)), nil
	}

	replaced := strings.ReplaceAll(content, oldStr, newStr)
	if err := t.FileWrite(ctx, path, replaced); err != nil {
		return errorResult(fmt.Sprintf("file_edit write error: %v", err)), nil
	}

	if replaceAll {
		return textResult(fmt.Sprintf("Replaced %d occurrences", count)), nil
	}
	return textResult("replaced 1 occurrence"), nil
}

// --- file_glob ---

func (s *Server) doFileGlob(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	pattern, _ := args["pattern"].(string)
	basePath, _ := args["path"].(string)
	if pattern == "" {
		return errorResult("pattern is required"), nil
	}

	files, err := t.FileGlob(ctx, pattern, basePath)
	if err != nil {
		return errorResult(fmt.Sprintf("file_glob error: %v", err)), nil
	}
	if len(files) == 0 {
		return textResult("(no matches)"), nil
	}
	return textResult(strings.Join(files, "\n")), nil
}

// --- file_grep ---

func (s *Server) doFileGrep(ctx context.Context, t *Transport, args map[string]interface{}) (ToolCallResult, *Error) {
	pattern, _ := args["pattern"].(string)
	basePath, _ := args["path"].(string)
	fileGlob, _ := args["glob"].(string)
	if pattern == "" {
		return errorResult("pattern is required"), nil
	}

	matches, err := t.FileGrep(ctx, pattern, basePath, fileGlob)
	if err != nil {
		return errorResult(fmt.Sprintf("file_grep error: %v", err)), nil
	}
	if len(matches) == 0 {
		return textResult("(no matches)"), nil
	}

	var sb strings.Builder
	for _, m := range matches {
		sb.WriteString(fmt.Sprintf("%s:%d: %s\n", m.Path, m.Line, m.Content))
	}
	return textResult(strings.TrimSpace(sb.String())), nil
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
