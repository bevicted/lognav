package logviewer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWrapLinesAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		width int
		want  string
	}{
		{
			name:  "no wrap needed",
			input: `{"k": "val"}`,
			width: 20,
			want:  `{"k": "val"}`,
		},
		{
			name:  "wraps before reaching width",
			input: `{"k": "val"}`,
			width: 12,
			// 12 chars total; last char wraps because 12 >= 12
			want: "{\"k\": \"val\"\n       }",
		},
		{
			name:  "backslash would be last char on line wraps early",
			input: `{"k": "ab\"cd"}`,
			width: 11,
			// Without backslash fix: \ stays at end, " wraps alone.
			// With fix: \ wraps early, keeping \" together.
			want: "{\"k\": \"ab\n       \\\"c\n       d\"}",
		},
		{
			name:  "backslash-backslash at line boundary wraps early",
			input: `{"k": "ab\\cd"}`,
			width: 12,
			// Without fix: first \ stays at end, second \ wraps alone.
			// With fix: first \ wraps early, keeping \\ together.
			want: "{\"k\": \"ab\\\n       \\cd\"\n       }",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := string(wrapLinesAt([]byte(tt.input), tt.width))
			assert.Equal(t, tt.want, got)
		})
	}
}
