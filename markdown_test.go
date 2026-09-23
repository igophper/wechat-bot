package wechat

import (
	"strings"
	"testing"
)

func TestFlattenMarkdownTables(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "2-column table",
			input: `| Parameter | Value |
| --- | --- |
| Status | OK |
| Code | 200 |`,
			expected: `Parameter: Value
Status: OK
Code: 200`,
		},
		{
			name: "3-column table",
			input: `| Col1 | Col2 | Col3 |
| :--- | :---: | ---: |
| A | B | C |
| 1 | 2 | 3 |`,
			expected: `Col1 · Col2 · Col3
A · B · C
1 · 2 · 3`,
		},
		{
			name: "Mixed prose and table",
			input: `Alert Notice:
| Service | State |
| --- | --- |
| redis | active |

Please check the dashboard.`,
			expected: `Alert Notice:
Service: State
redis: active

Please check the dashboard.`,
		},
		{
			name:     "Non table pipes",
			input:    "Command: cat foo | grep bar",
			expected: "Command: cat foo | grep bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := FlattenMarkdownTables(tt.input)
			if strings.TrimSpace(actual) != strings.TrimSpace(tt.expected) {
				t.Errorf("got:\n%s\nwant:\n%s", actual, tt.expected)
			}
		})
	}
}

func TestStripMarkdown(t *testing.T) {
	input := "### Title\n**Bold** and __italic__ with `code` block ```here```."
	expected := "Title\nBold and italic with code block here."
	actual := StripMarkdown(input)
	if actual != expected {
		t.Errorf("got %q, want %q", actual, expected)
	}
}

func TestFormatForWeChat(t *testing.T) {
	input := `### System Alert
| Service | Status |
| --- | --- |
| **API** | ` + "`UP`" + ` |
`
	actual := FormatForWeChat(input)
	expected := `System Alert
Service: Status
API: UP`
	if strings.TrimSpace(actual) != strings.TrimSpace(expected) {
		t.Errorf("got:\n%s\nwant:\n%s", actual, expected)
	}
}
