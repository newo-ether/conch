package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
)

const protocolVersion = "2025-06-18"
const serverVersion = "0.1.0"

var shellTool = Tool{
	Name:        "shell_execute",
	Description: "Execute a shell command on the remote server via Conch. The command runs in a non-interactive shell (/bin/sh -c or powershell). Output is streamed line-by-line with stdout/stderr markers. Returns the combined output lines and exit code.",
	InputSchema: JSONSchema{
		Type: "object",
		Properties: map[string]JSONProp{
			"command": {
				Type:        "string",
				Description: "The shell command to execute",
			},
			"timeout_ms": {
				Type:        "integer",
				Description: "Timeout in milliseconds (default: 30000, max: 120000)",
				Default:     30000,
			},
			"workdir": {
				Type:        "string",
				Description: "Working directory for the command (optional)",
			},
		},
		Required: []string{"command"},
	},
}

// Server is a stdio-based MCP server.
type Server struct {
	transport *Transport
}

func NewServer(transport *Transport) *Server {
	return &Server{transport: transport}
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

		// Parse as JSON-RPC
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
			// Notification — handle if needed, else ignore
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
			result = ToolsListResult{
				Tools: []Tool{shellTool},
			}
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

func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) (any, *Error) {
	var p ToolCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &Error{Code: -32602, Message: "invalid params"}
	}

	if p.Name != "shell_execute" {
		return nil, &Error{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", p.Name)}
	}

	command, _ := p.Arguments["command"].(string)
	if command == "" {
		return ToolCallResult{
			Content: []ContentItem{{Type: "text", Text: "Error: command is required"}},
			IsError: true,
		}, nil
	}

	timeoutMs := 30000
	if t, ok := p.Arguments["timeout_ms"].(float64); ok {
		timeoutMs = int(t)
	}

	workdir, _ := p.Arguments["workdir"].(string)

	events, err := s.transport.Execute(ctx, command, timeoutMs, workdir)
	if err != nil {
		return ToolCallResult{
			Content: []ContentItem{{Type: "text", Text: fmt.Sprintf("Conch error: %v", err)}},
			IsError: true,
		}, nil
	}

	// Format output
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
	}, nil
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
