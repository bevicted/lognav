package queryeditor

import (
	"strings"
	"testing"
)

func TestSnippetsForEditor(t *testing.T) {
	tests := []struct {
		name     string
		snippets []snippet
		want     []string
	}{
		{
			name:     "empty",
			snippets: nil,
			want:     nil,
		},
		{
			name: "single snippet",
			snippets: []snippet{
				{Snippet: "| filter", Description: "filter logs"},
			},
			want: []string{"| filter filter logs"},
		},
		{
			name: "column alignment",
			snippets: []snippet{
				{Snippet: "| f", Description: "short filter"},
				{Snippet: "| groupby", Description: "group results"},
			},
			want: []string{
				"| f       short filter",
				"| groupby group results",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := snippetsForEditor(tt.snippets)
			if len(tt.want) == 0 {
				if got != "" {
					t.Fatalf("expected empty, got %q", got)
				}
				return
			}
			lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			if len(lines) != len(tt.want) {
				t.Fatalf("got %d lines, want %d\ngot:  %q\nwant: %q", len(lines), len(tt.want), lines, tt.want)
			}
			for i, want := range tt.want {
				if lines[i] != want {
					t.Errorf("line %d:\ngot:  %q\nwant: %q", i, lines[i], want)
				}
			}
		})
	}
}

func TestFormatSnippetList(t *testing.T) {
	tests := []struct {
		name     string
		snippets []snippet
		wantLen  int
	}{
		{
			name:     "empty",
			snippets: nil,
			wantLen:  0,
		},
		{
			name: "contains snippet and description",
			snippets: []snippet{
				{Snippet: "| filter", Description: "filter logs"},
				{Snippet: "| limit 100", Description: "limit output"},
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatSnippetList(tt.snippets)
			if len(got) != tt.wantLen {
				t.Fatalf("got %d items, want %d", len(got), tt.wantLen)
			}
			for i, segments := range got {
				var combined strings.Builder
				for _, seg := range segments {
					combined.WriteString(seg.Text)
				}
				if !strings.Contains(combined.String(), tt.snippets[i].Description) {
					t.Errorf("item %d missing description: %q", i, combined.String())
				}
			}
		})
	}
}
