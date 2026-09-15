package timeline

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
)

func TestCellSequence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func() bucket
		scale     float64
		cellWidth int
		want      []icl.Severity
	}{
		{
			name:      "empty bucket yields no cells",
			setup:     func() bucket { return bucket{} },
			scale:     1.0,
			cellWidth: 10,
			want:      []icl.Severity{},
		},
		{
			name: "1:1 scale — severity descending with all 7 severities",
			setup: func() bucket {
				b := bucket{}
				b.counts[icl.SeverityVerbose] = 1
				b.counts[icl.SeverityInfo] = 1
				b.counts[icl.SeverityCritical] = 1
				b.counts[icl.SeverityError] = 1
				b.counts[icl.SeverityDebug] = 1
				b.counts[icl.SeverityWarning] = 1
				b.counts[icl.SeverityUnknown] = 1
				b.total = 7
				return b
			},
			scale:     1.0,
			cellWidth: 10,
			want: []icl.Severity{
				icl.SeverityCritical,
				icl.SeverityError,
				icl.SeverityWarning,
				icl.SeverityInfo,
				icl.SeverityDebug,
				icl.SeverityVerbose,
				icl.SeverityUnknown,
			},
		},
		{
			name: "1:1 scale — counts repeated per severity",
			setup: func() bucket {
				b := bucket{}
				b.counts[icl.SeverityCritical] = 2
				b.counts[icl.SeverityInfo] = 3
				b.total = 5
				return b
			},
			scale:     1.0,
			cellWidth: 10,
			want: []icl.Severity{
				icl.SeverityCritical, icl.SeverityCritical,
				icl.SeverityInfo, icl.SeverityInfo, icl.SeverityInfo,
			},
		},
		{
			name: "1:1 scale — cellWidth truncates tail (flamegraph fallback)",
			setup: func() bucket {
				b := bucket{}
				b.counts[icl.SeverityCritical] = 1
				b.counts[icl.SeverityError] = 1
				b.counts[icl.SeverityVerbose] = 10
				b.total = 12
				return b
			},
			scale:     1.0,
			cellWidth: 3,
			want: []icl.Severity{
				icl.SeverityCritical,
				icl.SeverityError,
				icl.SeverityVerbose,
			},
		},
		{
			name: "width zero yields empty",
			setup: func() bucket {
				b := bucket{}
				b.counts[icl.SeverityError] = 5
				b.total = 5
				return b
			},
			scale:     1.0,
			cellWidth: 0,
			want:      []icl.Severity{},
		},
		{
			name: "scale 0.1 shrinks proportionally but preserves Critical",
			setup: func() bucket {
				b := bucket{}
				b.counts[icl.SeverityCritical] = 1
				b.counts[icl.SeverityVerbose] = 999
				b.total = 1000
				return b
			},
			// Max bucket in timeline is much larger; scale=0.1 ⇒ bar = 100
			scale:     0.1,
			cellWidth: 130,
			want: append(
				[]icl.Severity{icl.SeverityCritical},
				repeat(icl.SeverityVerbose, 99)...,
			),
		},
		{
			name: "tiny scale preserves at least one Critical cell",
			setup: func() bucket {
				b := bucket{}
				b.counts[icl.SeverityCritical] = 1
				b.counts[icl.SeverityVerbose] = 5000
				b.total = 5001
				return b
			},
			scale:     0.001, // bar width = ceil(5001 * 0.001) = 6
			cellWidth: 130,
			// Critical shares: round(1/5001 * 6) = 0, bumped to 1.
			// Verbose shares: round(5000/5001 * 6) = 6. 1 + 6 = 7 > 6 → truncate to 6.
			want: append(
				[]icl.Severity{icl.SeverityCritical},
				repeat(icl.SeverityVerbose, 5)...,
			),
		},
		{
			name: "scale 0.5 — proportional severity mix",
			setup: func() bucket {
				b := bucket{}
				b.counts[icl.SeverityCritical] = 50
				b.counts[icl.SeverityInfo] = 50
				b.total = 100
				return b
			},
			// bar = ceil(100 * 0.5) = 50
			scale:     0.5,
			cellWidth: 130,
			want: append(
				repeat(icl.SeverityCritical, 25),
				repeat(icl.SeverityInfo, 25)...,
			),
		},
		{
			name: "bar width capped by cellWidth even at scale=1",
			setup: func() bucket {
				b := bucket{}
				b.counts[icl.SeverityInfo] = 200
				b.total = 200
				return b
			},
			scale:     1.0,
			cellWidth: 50,
			want:      repeat(icl.SeverityInfo, 50),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := cellSequence(tt.setup(), tt.scale, tt.cellWidth)
			assert.Equal(t, tt.want, got)
		})
	}
}

// repeat returns a slice containing the value repeated n times.
func repeat[T any](v T, n int) []T {
	out := make([]T, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestComputeVisibleIndices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		buckets   []bucket
		hideEmpty bool
		want      []int
	}{
		{
			name:      "empty bucket slice yields empty",
			buckets:   nil,
			hideEmpty: true,
			want:      []int{},
		},
		{
			name: "hideEmpty=false returns every index in order",
			buckets: []bucket{
				{total: 0},
				{total: 3},
				{total: 0},
				{total: 1},
			},
			hideEmpty: false,
			want:      []int{0, 1, 2, 3},
		},
		{
			name: "hideEmpty=true skips zero-total buckets",
			buckets: []bucket{
				{total: 0},
				{total: 1},
				{total: 0},
				{total: 3},
				{total: 0},
			},
			hideEmpty: true,
			want:      []int{1, 3},
		},
		{
			name: "hideEmpty=true with all non-empty buckets returns every index",
			buckets: []bucket{
				{total: 5},
				{total: 2},
				{total: 7},
			},
			hideEmpty: true,
			want:      []int{0, 1, 2},
		},
		{
			name: "hideEmpty=true with all empty buckets returns empty",
			buckets: []bucket{
				{total: 0},
				{total: 0},
			},
			hideEmpty: true,
			want:      []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := computeVisibleIndices(tt.buckets, tt.hideEmpty)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPickLabelLayout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		step int64
		want string
	}{
		{name: "1 hour", step: 3600_000_000, want: layoutSeconds},
		{name: "1 second exactly", step: 1_000_000, want: layoutSeconds},
		{name: "500 ms", step: 500_000, want: layoutMilliseconds},
		{name: "1 ms exactly", step: 1_000, want: layoutMilliseconds},
		{name: "500 µs", step: 500, want: layoutMicroseconds},
		{name: "1 µs", step: 1, want: layoutMicroseconds},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pickLabelLayout(tt.step)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ansiRe strips ANSI escape sequences so tests can assert on the plain content
// of rendered rows.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

func TestRenderRowLabelUsesUTC(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 3, 4, 18, 40, 51, 0, time.UTC).UnixMicro()
	b := bucket{startMicro: ts}
	const w = 80
	canvas := uv.NewScreenBuffer(w, 1)
	m := New(depstest.NewTest(t))
	placeRow(canvas, 0, 0, b, false, rowOpts{
		layout:       layoutSeconds,
		contentWidth: w,
		scale:        1.0,
		styles:       m.buildSeverityStyles(),
	})
	// canvas.Render() drops trailing EmptyCell-equivalent spaces, so the
	// separator's trailing space is gone when no cells follow. Match on the
	// trim-stable prefix " │" instead of " │ ".
	plain := stripANSI(canvas.Render())
	const sep = " │"
	before, _, ok := strings.Cut(plain, sep)
	if !ok {
		t.Fatalf("expected separator in row, got %q", plain)
	}
	assert.Equal(t, "18:40:51", before,
		"label must render in UTC, not the host's local timezone")
}

func TestRenderRow(t *testing.T) {
	t.Parallel()

	const tsMicro = int64(1775556863000000)

	tests := []struct {
		name         string
		setup        func() bucket
		layout       string
		contentWidth int
		scale        float64
		selected     bool
		wantCells    string
	}{
		{
			name:         "empty bucket renders label + separator only",
			setup:        func() bucket { return bucket{startMicro: tsMicro} },
			layout:       layoutSeconds,
			contentWidth: 80,
			scale:        1.0,
			wantCells:    "",
		},
		{
			name: "bucket at scale=1 emits one cell per log",
			setup: func() bucket {
				b := bucket{startMicro: tsMicro}
				b.counts[icl.SeverityCritical] = 1
				b.counts[icl.SeverityError] = 2
				b.counts[icl.SeverityInfo] = 1
				b.total = 4
				return b
			},
			layout:       layoutSeconds,
			contentWidth: 80,
			scale:        1.0,
			wantCells:    "C" + "E" + "E" + "I",
		},
		{
			name: "narrow width truncates low-severity cells first at scale=1",
			setup: func() bucket {
				b := bucket{startMicro: tsMicro}
				b.counts[icl.SeverityCritical] = 1
				b.counts[icl.SeverityVerbose] = 10
				b.total = 11
				return b
			},
			layout:       layoutSeconds,
			contentWidth: 13,
			scale:        1.0,
			wantCells:    "C" + "V",
		},
		{
			name: "selected row emits same cells",
			setup: func() bucket {
				b := bucket{startMicro: tsMicro}
				b.counts[icl.SeverityWarning] = 1
				b.total = 1
				return b
			},
			layout:       layoutSeconds,
			contentWidth: 80,
			scale:        1.0,
			selected:     true,
			wantCells:    "W",
		},
	}

	m := New(depstest.NewTest(t))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			canvas := uv.NewScreenBuffer(tt.contentWidth, 1)
			placeRow(canvas, 0, 0, tt.setup(), tt.selected, rowOpts{
				layout:       tt.layout,
				contentWidth: tt.contentWidth,
				scale:        tt.scale,
				styles:       m.buildSeverityStyles(),
			})
			plain := stripANSI(canvas.Render())
			// canvas.Render() drops trailing EmptyCell-equivalent spaces, so
			// match on the trim-stable separator prefix " │" instead of " │ ".
			const sep = " │"
			idx := strings.LastIndex(plain, sep)
			if idx < 0 {
				t.Fatalf("expected separator %q in row, got %q", sep, plain)
			}
			gotCells := strings.TrimLeft(strings.TrimRight(plain[idx+len(sep):], " "), " ")
			assert.Equal(t, tt.wantCells, gotCells)
		})
	}
}
