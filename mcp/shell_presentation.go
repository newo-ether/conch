package mcp

import (
	"fmt"
	"strings"
)

// Tool schemas require purpose text, while old wire callers remain compatible.
func init() {
	for index := range allTools {
		tool := &allTools[index]
		if !strings.HasPrefix(tool.Name, "shell_") {
			continue
		}
		tool.InputSchema.Properties["description"] = JSONProp{
			Type: "string", Description: "Short description of what this shell call is intended to do (4KB UTF-8 maximum)",
		}
		tool.InputSchema.Required = append(tool.InputSchema.Required, "description")
		if tool.Name == "shell_jobs" {
			tool.InputSchema.Properties["offset"] = JSONProp{Type: "integer", Description: "Zero-based page offset; continue with returned next_offset when has_more is true"}
			tool.InputSchema.Properties["limit"] = JSONProp{Type: "integer", Description: "Maximum jobs in this page (default and maximum 100); stored jobs are not deleted"}
		}
		if tool.Name == "shell_execute" || tool.Name == "shell_job_get" || tool.Name == "shell_start" {
			tool.InputSchema.Properties["head_lines"] = JSONProp{Type: "integer", Description: "First N output lines; combine with tail_lines without duplicating overlap"}
			tool.InputSchema.Properties["tail_lines"] = JSONProp{Type: "integer", Description: "Last N output lines; omitted middle line count is shown. Full retained output remains queryable"}
		}
	}
}

func shellDescription(args map[string]interface{}) string {
	value, _ := args["description"].(string)
	return value
}

func shellLineCount(args map[string]interface{}, name string) int {
	value, ok := args[name].(float64)
	if !ok || value <= 0 || value > float64(1<<31-1) || value != float64(int(value)) {
		return 0
	}
	return int(value)
}

func selectShellOutput(output string, args map[string]interface{}) string {
	head, tail := shellLineCount(args, "head_lines"), shellLineCount(args, "tail_lines")
	if output == "" || head == 0 && tail == 0 {
		return output
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if head > len(lines) {
		head = len(lines)
	}
	if tail > len(lines) {
		tail = len(lines)
	}
	omitted := len(lines) - head - tail
	if omitted <= 0 {
		return output
	}
	selected := append([]string{}, lines[:head]...)
	selected = append(selected, fmt.Sprintf("... (%d lines omitted) ...", omitted))
	selected = append(selected, lines[len(lines)-tail:]...)
	return strings.Join(selected, "\n")
}

// Keep warnings, errors, job identity and terminal evidence outside the output selection.
func selectShellEvents(events []LineEvent, args map[string]interface{}) []LineEvent {
	if shellLineCount(args, "head_lines") == 0 && shellLineCount(args, "tail_lines") == 0 {
		return events
	}
	var lines strings.Builder
	selected := make([]LineEvent, 0, len(events))
	for _, event := range events {
		if event.Stream != "" {
			if event.Stream == "stderr" {
				lines.WriteString("[stderr] ")
			}
			lines.WriteString(event.Line)
			lines.WriteByte('\n')
		} else {
			selected = append(selected, event)
		}
	}
	if lines.Len() > 0 {
		output := selectShellOutput(lines.String(), args)
		selected = append([]LineEvent{{Line: strings.TrimSuffix(output, "\n")}}, selected...)
	}
	return selected
}
