package uieditor

import (
	"testing"
)

func runeLines(rls [][]rune) []string {
	out := make([]string, len(rls))
	for i, rl := range rls {
		out[i] = string(rl)
	}
	return out
}

// TestWrapLine is the wrap oracle. With the editor native, this table IS the
// spec for soft-wrap (there is no upstream textarea to defer to); it guards the
// CJK/long-word/space edge cases that the cursor mapping depends on.
func TestWrapLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		width int
		want  []string
	}{
		{
			name:  "fits in one line",
			input: "hello world",
			width: 20,
			want:  []string{"hello world "},
		},
		{
			name:  "wraps at word boundary",
			input: "hello world",
			width: 8,
			want:  []string{"hello ", "world "},
		},
		{
			name:  "long word exceeds width",
			input: "abcdefghij",
			width: 5,
			want:  []string{"abcde", "fghij", " "},
		},
		{
			name:  "multiple words with wrap",
			input: "aa bb cc dd",
			width: 6,
			want:  []string{"aa bb ", "cc dd "},
		},
		{
			name:  "single character",
			input: "x",
			width: 10,
			want:  []string{"x "},
		},
		{
			name:  "exact width word",
			input: "abcde fghij",
			width: 5,
			want:  []string{"abcde", " ", "fghij", " "},
		},
		{
			name:  "multiple spaces between words",
			input: "a  b",
			width: 20,
			want:  []string{"a  b "},
		},
		{
			name:  "wide chars (CJK)",
			input: "日本語テスト",
			width: 8,
			want:  []string{"日本語テ", "スト "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := runeLines(wrapLine([]rune(tt.input), tt.width))
			if len(got) != len(tt.want) {
				t.Fatalf("got %d lines, want %d\ngot:  %q\nwant: %q", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("line %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
