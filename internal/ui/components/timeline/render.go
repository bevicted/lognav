package timeline

import (
	"image/color"
	"math"
	"time"

	"github.com/bevicted/lognav/internal/icl"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

// severityOrder is the order cells are emitted from left to right.
// Highest severity first so that horizontal truncation drops the least
// important cells.
var severityOrder = [...]icl.Severity{
	icl.SeverityCritical,
	icl.SeverityError,
	icl.SeverityWarning,
	icl.SeverityInfo,
	icl.SeverityDebug,
	icl.SeverityVerbose,
	icl.SeverityUnknown,
}

// cellSequence returns the severity of every cell in a bucket's row, in
// display order (severity descending). With scale=1.0 each log in the
// bucket emits one cell; smaller scales compress the row proportionally
// so the busiest bucket across the whole timeline fits inside cellWidth.
// Severities with a non-zero count always receive at least one cell
// (subject to the cellWidth cap), preserving the highest-severity
// signal under heavy compression.
func cellSequence(b bucket, scale float64, cellWidth int) []icl.Severity {
	if b.total == 0 || cellWidth <= 0 {
		return []icl.Severity{}
	}
	// Target bar width for this row: the bucket's count scaled to the
	// flamegraph range, capped by the row's available cells.
	barWidth := max(min(int(math.Ceil(float64(b.total)*scale)), cellWidth), 1)
	out := make([]icl.Severity, 0, barWidth)
	for _, sev := range severityOrder {
		n := b.counts[sev]
		if n == 0 {
			continue
		}
		// Proportional share of this severity in the bar. Rounded, with
		// a min of 1 so a rare high-severity log (e.g. a single Critical
		// in a thousand-log bucket) still shows under heavy compression.
		share := int(math.Round(float64(n) / float64(b.total) * float64(barWidth)))
		if share == 0 {
			share = 1
		}
		for range share {
			if len(out) >= barWidth {
				return out
			}
			out = append(out, sev)
		}
	}
	return out
}

// Time-format layout constants for pickLabelLayout.
const (
	layoutSeconds      = "15:04:05"
	layoutMilliseconds = "15:04:05.000"
	layoutMicroseconds = "15:04:05.000000"
)

// pickLabelLayout returns a Go time.Format layout suited to the bucket step.
// 1s+ steps use HH:MM:SS, 1ms+ steps use millisecond precision, and sub-ms
// steps use microsecond precision.
func pickLabelLayout(stepMicro int64) string {
	switch {
	case stepMicro >= 1_000_000:
		return layoutSeconds
	case stepMicro >= 1_000:
		return layoutMilliseconds
	default:
		return layoutMicroseconds
	}
}

// severityStyleEntry caches the resolved uv.Style and gutter sign for a
// severity, so the per-cell render loop does not re-resolve them.
type severityStyleEntry struct {
	style uv.Style
	sign  string
}

// severityStyles is a [7]severityStyleEntry indexed by icl.Severity, rebuilt
// once per render() so cell runs do not re-resolve config colours per cell.
type severityStyles [7]severityStyleEntry

// buildSeverityStyles snapshots the current gutter styles into a uv.Style
// cache keyed by severity. Called once per render to avoid per-cell
// allocations while still picking up config hot-reloads.
func (m *Model) buildSeverityStyles() severityStyles {
	var ss severityStyles
	fg := m.bundle.Config.Style.GutterFg.Color
	for _, sev := range severityOrder {
		bg, sign := uicanvas.GutterSeverityStyle(m.bundle, sev)
		ss[sev] = severityStyleEntry{
			style: uv.Style{Fg: fg, Bg: bg},
			sign:  sign,
		}
	}
	return ss
}

// rowOpts holds the per-render invariants shared by every placeRow call.
type rowOpts struct {
	layout       string
	contentWidth int
	scale        float64
	styles       severityStyles
}

// placeRow writes a bucket row to the screen starting at column x of row y.
// Layout: "HH:MM:SS.mmm │ <cells>". When selected is true, the label part
// receives uv.AttrReverse. Bar cells keep their severity background.
//
// Labels are rendered in UTC to match the raw log timestamp strings the
// logviewer displays (ICL returns UTC timestamps with no zone suffix).
func placeRow(s component.Screen, x, y int, b bucket, selected bool, opts rowOpts) {
	label := time.UnixMicro(b.startMicro).UTC().Format(opts.layout)
	const separator = " │ "
	labelPart := label + separator
	labelWidth := uniseg.StringWidth(labelPart)
	cellWidth := max(opts.contentWidth-labelWidth, 0)
	maxX := x + opts.contentWidth

	labelStyle := uv.Style{}
	if selected {
		labelStyle.Attrs |= uv.AttrReverse
	}

	col := uicanvas.PlaceText(s, x, y, labelPart, labelStyle, maxX)

	cells := cellSequence(b, opts.scale, cellWidth)
	for _, sev := range cells {
		if col >= maxX {
			return
		}
		e := opts.styles[sev]
		s.SetCell(col, y, &uv.Cell{Content: e.sign, Width: 1, Style: e.style})
		col++
	}
}

// placeOnScreen writes the timeline's visible rows onto the screen at the given rect.
// rect is in absolute screen coordinates.
func (m *Model) placeOnScreen(s component.Screen, rect component.Rect) {
	if len(m.visibleIndices) == 0 || rect.W < 1 || rect.H < 1 {
		return
	}

	step := max(m.buckets[0].endMicro-m.buckets[0].startMicro, 1)
	layout := pickLabelLayout(step)

	cellWidth := computeCellWidth(rect.W, layout)
	scale := 1.0
	if m.maxTotal > cellWidth && cellWidth > 0 {
		scale = float64(cellWidth) / float64(m.maxTotal)
	}

	opts := rowOpts{
		layout:       layout,
		contentWidth: rect.W,
		scale:        scale,
		styles:       m.buildSeverityStyles(),
	}

	end := min(m.yOff+rect.H, len(m.visibleIndices))
	hoverBg, tintHover := color.Color(nil), false
	if m.bundle.Config.Style.HoverRowBg.IsSet() {
		hoverBg, tintHover = m.bundle.Config.Style.HoverRowBg.Color, true
	}
	for i := m.yOff; i < end; i++ {
		y := rect.Y + (i - m.yOff)
		placeRow(s, rect.X, y, m.buckets[m.visibleIndices[i]], i == m.cursor, opts)

		// Hover tint — bg wash on the hovered, non-cursor bucket row.
		if tintHover && i == m.hoverIdx && i != m.cursor {
			for x := rect.X; x < rect.X+rect.W; x++ {
				if cell := s.CellAt(x, y); cell != nil {
					cell.Style.Bg = hoverBg
				}
			}
		}
	}
}

// computeCellWidth returns the number of columns available for cells after
// the fixed-width label + separator is subtracted from the content width.
// It computes the width of a sample label rendered with the given layout.
func computeCellWidth(contentWidth int, layout string) int {
	sample := time.Unix(0, 0).UTC().Format(layout) + " │ "
	cw := contentWidth - uniseg.StringWidth(sample)
	if cw < 0 {
		return 0
	}
	return cw
}

// clampScroll keeps the cursor inside the visible window by adjusting yOff.
// The cursor lives in visibleIndices space, not bucket space.
func (m *Model) clampScroll() {
	if len(m.visibleIndices) == 0 {
		m.yOff = 0
		m.cursor = 0
		return
	}
	h := m.drawRect.H
	if h <= 0 {
		h = 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.visibleIndices) {
		m.cursor = len(m.visibleIndices) - 1
	}
	if m.cursor < m.yOff {
		m.yOff = m.cursor
	}
	if m.cursor >= m.yOff+h {
		m.yOff = m.cursor - h + 1
	}
	if m.yOff < 0 {
		m.yOff = 0
	}
}

// timelineBuckets returns the configured number of buckets, clamped to a
// minimum of 1 so Build() never receives zero.
func (m *Model) timelineBuckets() int {
	n := int(m.bundle.Config.Logs.TimelineBuckets)
	if n < 1 {
		return 1
	}
	return n
}

// computeVisibleIndices returns the indices into buckets that should actually
// be rendered. When hideEmpty is true, buckets whose total is zero are
// omitted so quiet time slices collapse out of the view. When false, every
// bucket index is returned in order.
func computeVisibleIndices(buckets []bucket, hideEmpty bool) []int {
	out := make([]int, 0, len(buckets))
	for i, b := range buckets {
		if hideEmpty && b.total == 0 {
			continue
		}
		out = append(out, i)
	}
	return out
}
