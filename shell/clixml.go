package shell

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// Windows PowerShell 5.1 serializes host error/warning/verbose output as CLIXML when stderr is a
// pipe and the record bypasses Conch's bootstrap pipeline (Out-Host, a nested powershell.exe, a
// script that writes to the host directly). The block is a "#< CLIXML" marker line followed by one
// or more <Objs> lines.
const (
	clixmlMarker    = "#< CLIXML"
	clixmlNamespace = "http://schemas.microsoft.com/powershell/2004/04"
)

// renderCLIXMLLine turns one CLIXML line into plain text. keep is false when the line carries
// nothing worth showing (the marker, or only progress records). A line that is not CLIXML, or
// does not parse, is returned unchanged.
func renderCLIXMLLine(line string) (text string, keep bool) {
	trimmed := strings.TrimRight(line, "\r")
	if trimmed == clixmlMarker {
		return "", false
	}
	if !strings.HasPrefix(trimmed, "<Objs") || !strings.Contains(trimmed, clixmlNamespace) {
		return line, true
	}
	decoder := xml.NewDecoder(strings.NewReader(trimmed))
	var out strings.Builder
	depth := 0 // > 0 while inside a stream string element <S S="...">
	for {
		token, err := decoder.Token()
		if err != nil {
			if depth != 0 || !strings.HasSuffix(trimmed, "</Objs>") {
				return line, true
			}
			break
		}
		switch t := token.(type) {
		case xml.StartElement:
			if depth > 0 {
				depth++
			} else if t.Name.Local == "S" && hasStreamAttr(t) {
				depth = 1
			}
		case xml.EndElement:
			if depth > 0 {
				depth--
			}
		case xml.CharData:
			if depth == 1 {
				out.WriteString(decodeCLIXMLEscapes(string(t)))
			}
		}
	}
	text = collapseNativeErrorBanners(strings.ReplaceAll(out.String(), "\r\n", "\n"))
	text = strings.TrimRight(text, "\n ")
	if text == "" {
		return "", false
	}
	return text, true
}

// collapseNativeErrorBanners reduces the ErrorRecord view PowerShell prints for the first stderr
// line of a native command ("cmd : msg", "At line:1 char:1", "+ ...", CategoryInfo,
// FullyQualifiedErrorId : NativeCommandError) back to the original line.
func collapseNativeErrorBanners(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		end := nativeErrorBannerEnd(lines, i)
		if end < 0 {
			out = append(out, lines[i])
			continue
		}
		_, message, _ := strings.Cut(lines[i], " : ")
		out = append(out, message)
		i = end
		if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
			i++
		}
	}
	return strings.Join(out, "\n")
}

// nativeErrorBannerEnd returns the index of the FullyQualifiedErrorId line closing a banner that
// starts at lines[start], or -1.
func nativeErrorBannerEnd(lines []string, start int) int {
	if !strings.Contains(lines[start], " : ") || start+1 >= len(lines) ||
		!strings.HasPrefix(lines[start+1], "At ") || !strings.Contains(lines[start+1], " char:") {
		return -1
	}
	for j := start + 2; j < len(lines) && j <= start+10; j++ {
		line := strings.TrimSpace(lines[j])
		if strings.HasPrefix(line, "+ FullyQualifiedErrorId : NativeCommandError") {
			return j
		}
		if !strings.HasPrefix(line, "+") {
			return -1
		}
	}
	return -1
}

// nativeErrorBannerFilter removes the same ErrorRecord banner when it arrives as plain text, one
// line at a time (a nested powershell.exe whose stdout is a pipe prints it through Out-Host without
// CLIXML). The leading "cmd : msg" line has already been forwarded by the time the banner is
// recognised, so it stays; the "At line:" and "+ ..." lines are held only until the banner either
// completes (dropped) or breaks (forwarded unchanged). Not safe for concurrent use; one per stream.
type nativeErrorBannerFilter struct {
	lastForwarded string
	pending       []string
	dropBlank     bool
}

const maxNativeErrorBannerLines = 10

// push returns the lines ready to forward after line arrives.
func (f *nativeErrorBannerFilter) push(line string) []string {
	// Windows PowerShell relays a nested host's stderr with an empty line after every line; those
	// carry nothing and must not break banner recognition.
	if strings.TrimRight(line, "\r") == "" {
		if len(f.pending) > 0 {
			return nil
		}
		return []string{line}
	}
	if len(f.pending) > 0 {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "+ FullyQualifiedErrorId : NativeCommandError") {
			f.pending = f.pending[:0]
			f.dropBlank = true
			return nil
		}
		if strings.HasPrefix(trimmed, "+") && len(f.pending) < maxNativeErrorBannerLines {
			f.pending = append(f.pending, line)
			return nil
		}
		out := f.flush()
		return append(out, f.forward(line)...)
	}
	if f.dropBlank {
		f.dropBlank = false
		if strings.TrimSpace(line) == "" {
			return nil
		}
	}
	if strings.HasPrefix(line, "At ") && strings.Contains(line, " char:") &&
		strings.Contains(f.lastForwarded, " : ") {
		f.pending = append(f.pending, line)
		return nil
	}
	return f.forward(line)
}

func (f *nativeErrorBannerFilter) forward(line string) []string {
	f.lastForwarded = line
	return []string{line}
}

// flush returns any held lines; call it before the stream ends.
func (f *nativeErrorBannerFilter) flush() []string {
	if len(f.pending) == 0 {
		return nil
	}
	out := append([]string(nil), f.pending...)
	f.pending = f.pending[:0]
	f.lastForwarded = out[len(out)-1]
	return out
}

func hasStreamAttr(element xml.StartElement) bool {
	for _, attr := range element.Attr {
		if attr.Name.Local == "S" {
			return true
		}
	}
	return false
}

// decodeCLIXMLEscapes expands the _xHHHH_ escapes PowerShell uses for control characters.
func decodeCLIXMLEscapes(text string) string {
	if !strings.Contains(text, "_x") {
		return text
	}
	var out strings.Builder
	for i := 0; i < len(text); {
		if i+7 <= len(text) && text[i] == '_' && text[i+1] == 'x' && text[i+6] == '_' {
			if code, err := strconv.ParseUint(text[i+2:i+6], 16, 16); err == nil {
				out.WriteRune(rune(code))
				i += 7
				continue
			}
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}
