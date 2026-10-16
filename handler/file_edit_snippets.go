package handler

import (
	"sort"
	"strings"
)

// FileEditSnippet is a numbered excerpt of the edited file around one or more
// changed regions, so a client can confirm an edit without reading the file again.
type FileEditSnippet struct {
	StartLine int      `json:"start_line"`
	Lines     []string `json:"lines"`
}

const (
	fileEditSnippetContext  = 3
	maxFileEditSnippets     = 10
	maxFileEditSnippetLines = 60
	// maxTrackedEditRanges bounds range bookkeeping for replace_all over many matches.
	maxTrackedEditRanges = 1000
)

// fileEditTracker follows the byte ranges that edits inserted into the content,
// remapping earlier ranges as later edits shift or overwrite them.
type fileEditTracker struct {
	ranges    [][2]int
	truncated bool
}

// apply records one replacement pass. matches are the replaced ranges in the
// previous content and inserted the matching ranges in the new content; both are
// sorted and non-overlapping.
func (t *fileEditTracker) apply(matches, inserted [][2]int) {
	for i, r := range t.ranges {
		t.ranges[i] = [2]int{
			remapEditOffset(r[0], matches, inserted, true),
			remapEditOffset(r[1], matches, inserted, false),
		}
	}
	room := maxTrackedEditRanges - len(t.ranges)
	if room < len(inserted) {
		t.truncated = true
		if room < 0 {
			room = 0
		}
		inserted = inserted[:room]
	}
	t.ranges = append(t.ranges, inserted...)
}

// remapEditOffset maps an offset in the previous content to the new content. An
// offset strictly inside a replaced range snaps to the start (leanStart) or end of
// its replacement, so a tracked range overlapping a later edit grows to cover it.
func remapEditOffset(p int, matches, inserted [][2]int, leanStart bool) int {
	// First match whose end is beyond p; all earlier matches lie at or before p.
	i := sort.Search(len(matches), func(i int) bool { return matches[i][1] > p })
	if i < len(matches) && p > matches[i][0] {
		if leanStart {
			return inserted[i][0]
		}
		return inserted[i][1]
	}
	if i == 0 {
		return p
	}
	return p + inserted[i-1][1] - matches[i-1][1]
}

// snippets renders the tracked ranges of content as merged, numbered excerpts.
// The bool reports whether any range or line was dropped by the bounds.
func (t *fileEditTracker) snippets(content string) ([]FileEditSnippet, bool) {
	if len(t.ranges) == 0 || content == "" {
		return nil, t.truncated
	}
	ranges := append([][2]int(nil), t.ranges...)
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })

	// Resolve every range endpoint to a 1-based line with one forward sweep.
	offsets := make([]int, 0, len(ranges)*2)
	for _, r := range ranges {
		end := r[1]
		if end > r[0] {
			end-- // last byte of the inserted text
		}
		offsets = append(offsets, r[0], end)
	}
	sortedOffsets := append([]int(nil), offsets...)
	sort.Ints(sortedOffsets)
	lineOf := make(map[int]int, len(sortedOffsets))
	line, pos := 1, 0
	for _, offset := range sortedOffsets {
		if offset > len(content) {
			offset = len(content)
		}
		line += strings.Count(content[pos:offset], "\n")
		pos = offset
		lineOf[offset] = line
	}
	totalLines := countLines([]byte(content))

	type window struct{ first, last int }
	var windows []window
	for i := range ranges {
		first := clampLine(lineOf[minInt(offsets[2*i], len(content))]-fileEditSnippetContext, totalLines)
		last := clampLine(lineOf[minInt(offsets[2*i+1], len(content))]+fileEditSnippetContext, totalLines)
		if n := len(windows); n > 0 && first <= windows[n-1].last+1 {
			if last > windows[n-1].last {
				windows[n-1].last = last
			}
			continue
		}
		windows = append(windows, window{first, last})
	}

	truncated := t.truncated
	if len(windows) > maxFileEditSnippets {
		windows = windows[:maxFileEditSnippets]
		truncated = true
	}
	out := make([]FileEditSnippet, 0, len(windows))
	line, pos = 1, 0
	for _, w := range windows {
		last := w.last
		if last-w.first+1 > maxFileEditSnippetLines {
			last = w.first + maxFileEditSnippetLines - 1
			truncated = true
		}
		for line < w.first {
			next := strings.IndexByte(content[pos:], '\n')
			if next < 0 {
				break
			}
			pos += next + 1
			line++
		}
		snippet := FileEditSnippet{StartLine: w.first}
		cursor, current := pos, line
		for current <= last && cursor <= len(content) {
			next := strings.IndexByte(content[cursor:], '\n')
			text := content[cursor:]
			if next >= 0 {
				text = content[cursor : cursor+next]
			}
			snippet.Lines = append(snippet.Lines, truncateGrepLine(strings.TrimSuffix(text, "\r")))
			if next < 0 {
				break
			}
			cursor += next + 1
			current++
		}
		out = append(out, snippet)
	}
	return out, truncated
}

func clampLine(line, total int) int {
	if line < 1 {
		return 1
	}
	if total > 0 && line > total {
		return total
	}
	return line
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
