package wechat

import (
	"strings"
)

// FormatForWeChat transforms markdown text into a WeChat-friendly plain text format.
// It first flattens markdown tables into clean key-value or dot-separated lists,
// then strips common markdown formatting syntax (headers, bold, italics, backticks)
// that WeChat does not natively render.
func FormatForWeChat(text string) string {
	if text == "" {
		return ""
	}
	return StripMarkdown(FlattenMarkdownTables(text))
}

// FlattenMarkdownTables converts GFM-style table blocks into clean plain-text representations.
// - 2-column tables become "Header1: Header2" and "Cell1: Cell2" lines.
// - 3+ column tables become "Cell1 · Cell2 · Cell3" lines.
// Non-table lines are preserved verbatim.
func FlattenMarkdownTables(text string) string {
	if !strings.Contains(text, "|") {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		if i+1 < len(lines) && isMarkdownTableRow(lines[i]) && isMarkdownTableSeparator(lines[i+1]) {
			header := parseMarkdownTableRow(lines[i])
			i += 2 // consume header + separator
			rows := [][]string{header}
			for i < len(lines) && isMarkdownTableRow(lines[i]) {
				rows = append(rows, parseMarkdownTableRow(lines[i]))
				i++
			}
			out = append(out, renderFlatTable(rows))
			continue
		}
		out = append(out, lines[i])
		i++
	}
	return strings.Join(out, "\n")
}

// StripMarkdown removes common Markdown syntax markers so plain-text channels
// do not display raw markdown symbols.
func StripMarkdown(text string) string {
	if text == "" {
		return ""
	}
	out := text
	// Strip ATX headers at line starts (#, ##, ###)
	for _, prefix := range []string{"### ", "## ", "# "} {
		out = replaceAtLineStart(out, prefix, "")
	}
	// Bold/italic markers
	out = strings.ReplaceAll(out, "**", "")
	out = strings.ReplaceAll(out, "__", "")
	// Inline code backticks
	out = strings.ReplaceAll(out, "```", "")
	out = strings.ReplaceAll(out, "`", "")
	return out
}

func isMarkdownTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 || trimmed[0] != '|' || trimmed[len(trimmed)-1] != '|' {
		return false
	}
	interior := trimmed[1 : len(trimmed)-1]
	for i := 0; i < len(interior); i++ {
		if interior[i] == '|' && (i == 0 || interior[i-1] != '\\') {
			return true
		}
	}
	return false
}

func isMarkdownTableSeparator(line string) bool {
	if !isMarkdownTableRow(line) {
		return false
	}
	cells := parseMarkdownTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		c = strings.TrimPrefix(c, ":")
		c = strings.TrimSuffix(c, ":")
		if c == "" {
			return false
		}
		for _, r := range c {
			if r != '-' {
				return false
			}
		}
	}
	return true
}

func parseMarkdownTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if c == '\\' && i+1 < len(trimmed) && trimmed[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if c == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

func renderFlatTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	var b strings.Builder
	if cols == 2 {
		for i, r := range rows {
			left := ""
			right := ""
			if len(r) > 0 {
				left = r[0]
			}
			if len(r) > 1 {
				right = r[1]
			}
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(left)
			b.WriteString(": ")
			b.WriteString(right)
		}
		return b.String()
	}
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		padded := r
		for len(padded) < cols {
			padded = append(padded, "")
		}
		b.WriteString(strings.Join(padded, " · "))
	}
	return b.String()
}

func replaceAtLineStart(s, prefix, replacement string) string {
	if prefix == "" {
		return s
	}
	out := make([]byte, 0, len(s))
	atLineStart := true
	for i := 0; i < len(s); {
		if atLineStart && i+len(prefix) <= len(s) && s[i:i+len(prefix)] == prefix {
			out = append(out, replacement...)
			i += len(prefix)
			atLineStart = false
			continue
		}
		out = append(out, s[i])
		atLineStart = s[i] == '\n'
		i++
	}
	return string(out)
}
