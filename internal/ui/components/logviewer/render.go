package logviewer

import (
	"image/color"
	"strings"

	"github.com/bevicted/lognav/internal/icl"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

type renderLineOpts struct {
	line string
	// searchMatches is the WHOLE log's match slice (m.store.state.search[logIdx]),
	// not a per-line copy. renderLine selects the ones on lineIdx by index, so no
	// filtered slice and no per-line closure is allocated on the draw path.
	searchMatches []SearchMatch
	logIdx        int
	// lineIdx is the index of this rendered line within logIdx's wrapped line
	// list — the value SearchMatch.line is compared against.
	lineIdx       int
	width         int
	sideBarWidth  int
	colOffset     int
	rowOffset     int
	isUnderCursor bool
	isUnderHover  bool
	isExpanded    bool
	isMarked      bool
	severity      icl.Severity
	tokens        []Token
	matchSpans    []matchSpan
}

// tokenCursor resolves "which token covers column N" for a single line in
// amortized O(1) per query, replacing a full rescan of the token list per
// grapheme. It holds two invariants, both already true on the draw path:
//
//   - tokenize() emits a line's tokens in ascending, non-overlapping column
//     order (single left-to-right lexer pass, see jsonlex.go);
//   - renderLine visits columns monotonically, so col never decreases between
//     at() calls.
//
// A cursor is valid for exactly one line; renderLine constructs a fresh one
// per line.
type tokenCursor struct {
	tokens []Token
	i      int
}

// at returns the token covering column col, or nil when col lands in a gap
// between tokens or past the last one. Zero-width tokens (Start == End) never
// cover a column, matching the `col >= Start && col < End` test it replaces.
// col must not decrease across calls on the same cursor.
func (tc *tokenCursor) at(col int) *Token {
	for tc.i < len(tc.tokens) && tc.tokens[tc.i].End <= col {
		tc.i++
	}
	if tc.i < len(tc.tokens) && col >= tc.tokens[tc.i].Start {
		return &tc.tokens[tc.i]
	}
	return nil
}

// cursorRange returns the column range [start, end) within the full line that
// should be highlighted by the cursor. If a TokenKey exists, returns its span.
// Otherwise returns the span from the first non-whitespace grapheme to just past
// the last non-whitespace grapheme. Returns -1, -1 if the line is empty/whitespace.
func cursorRange(line string, tokens []Token, expanded bool) (int, int) {
	// In expanded mode, highlight the key token if present
	if expanded {
		for _, tok := range tokens {
			if tok.Type == TokenKey {
				return tok.Start, tok.End
			}
		}
	}

	// No key: find first and last non-whitespace by grapheme
	start := -1
	end := -1
	col := 0
	gr := uniseg.NewGraphemes(line)
	for gr.Next() {
		grapheme := gr.Str()
		w := gr.Width()
		if strings.TrimSpace(grapheme) != "" {
			if start == -1 {
				start = col
			}
			end = col + w
		}
		col += w
	}
	return start, end
}

// setSeverityCell places a severity indicator cell on the screen at absolute
// position (x, y).
func (m *Model) setSeverityCell(s component.Screen, x, y int, sev icl.Severity, isUnderCursor bool) {
	bg, sign := uicanvas.GutterSeverityStyle(m.bundle, sev)
	style := uv.Style{Fg: m.bundle.Config.Style.GutterFg.Color, Bg: bg}
	if isUnderCursor {
		style.Attrs |= uv.AttrReverse
	}
	s.SetCell(x, y, &uv.Cell{Content: sign, Width: 1, Style: style})
}

// renderLine places a single log line onto the canvas at row y (relative to
// rowOffset) using a 7-layer approach:
// 0) pre-fill row with space cells and base background
// 1) gutter severity indicator
// 2) base text with token colors
// 2.5) color-rule overlay (configured matchColors)
// 3) cursor highlighting (mutate cell styles)
// 4) search match highlighting (mutate cell styles)
// 5) mark highlighting (mutate first content cell).
//
// All cell writes are translated to absolute screen coordinates by adding
// opts.colOffset to the X axis and opts.rowOffset to the Y axis.
//
//nolint:gocyclo // hot rendering path with cursor/search/mark overlays; extraction adds call overhead.
func (m *Model) renderLine(s component.Screen, y int, opts renderLineOpts) {
	totalWidth := opts.width + opts.sideBarWidth
	row := y + opts.rowOffset

	// Step 0: Pre-fill row with space cells and base background
	var baseBg color.Color
	if opts.logIdx&1 == 1 && m.bundle.Config.Style.EverySecondListItemBg.IsSet() {
		baseBg = m.bundle.Config.Style.EverySecondListItemBg.Color
	}
	uicanvas.FillRowAt(s, opts.colOffset, row, totalWidth, uv.Style{Bg: baseBg})

	// Step 1: Gutter
	m.setSeverityCell(s, opts.colOffset, row, opts.severity, opts.isUnderCursor)

	// Step 2: Base text with token colors
	col := opts.sideBarWidth
	lineCol := 0 // column position in the original line (display width)
	// Hoisted out of the grapheme loop: lineCol only ever advances, so the
	// covering token is found by advancing a cursor instead of rescanning the
	// whole token list per grapheme. Skipped (off-screen-left) graphemes still
	// advance lineCol, and at() catches up lazily, so the jump is free.
	tc := tokenCursor{tokens: opts.tokens}
	gr := uniseg.NewGraphemes(opts.line)
	for gr.Next() {
		grapheme := gr.Str()
		w := gr.Width()
		if w == 0 {
			continue
		}

		if lineCol+w <= m.xOffset {
			lineCol += w
			continue
		}
		if col >= totalWidth {
			break
		}

		// Find token style for this column position in line coordinate space
		style := m.tokenCellStyle(TokenSyntax, baseBg) // default: just baseBg, no fg
		if tok := tc.at(lineCol); tok != nil {
			style = m.tokenCellStyle(tok.Type, baseBg)
		}

		s.SetCell(col+opts.colOffset, row, &uv.Cell{Content: grapheme, Width: w, Style: style})
		col += w
		lineCol += w
	}

	// Step 2.5: Color-rule overlay
	applyMatchSpans(s, row, opts.colOffset, m.xOffset, opts.sideBarWidth, totalWidth, opts.matchSpans)

	// Step 3: Cursor highlighting
	//nolint:nestif // overlay step: bound check, viewport clip, cell-exists guard each are necessary.
	if opts.isUnderCursor {
		cStart, cEnd := cursorRange(opts.line, opts.tokens, opts.isExpanded)
		if cStart >= 0 && cEnd >= 0 {
			for x := cStart - m.xOffset + opts.sideBarWidth; x < cEnd-m.xOffset+opts.sideBarWidth; x++ {
				if x >= opts.sideBarWidth && x < totalWidth {
					cell := s.CellAt(x+opts.colOffset, row)
					if cell != nil {
						cell.Style.Attrs |= uv.AttrReverse
					}
				}
			}
		}
	}

	// Step 3.5: Hover tint (experimental) — wash the whole text row under the
	// mouse pointer with the configured hover background, but never the cursor row
	// (the cursor highlight wins). Independent of token attrs, so the already-bold
	// JSON keys stay legible. Skipped when no hover color is set.
	if opts.isUnderHover && !opts.isUnderCursor && m.bundle.Config.Style.HoverRowBg.IsSet() {
		hoverBg := m.bundle.Config.Style.HoverRowBg.Color
		for x := opts.sideBarWidth; x < totalWidth; x++ {
			if cell := s.CellAt(x+opts.colOffset, row); cell != nil {
				cell.Style.Bg = hoverBg
			}
		}
	}

	// Step 4: Search match highlighting
	//
	// searchMatches covers the whole log, so len(...) > 0 only means "this LOG
	// matches somewhere" — it is a cheap skip of the style lookup, never a
	// licence to paint. Every span is still gated on match.line == lineIdx, so a
	// line whose log matches only on other lines paints nothing.
	//nolint:nestif // overlay step: bound check, viewport clip, cell-exists guard, and cursor-row reverse each are necessary.
	if len(opts.searchMatches) > 0 {
		matchFg, matchBg := m.bundle.Config.Style.SearchMatchFg.Color, m.bundle.Config.Style.SearchMatchBg.Color
		for _, match := range opts.searchMatches {
			if match.line != opts.lineIdx {
				continue
			}
			for x := match.start - m.xOffset + opts.sideBarWidth; x < match.end-m.xOffset+opts.sideBarWidth; x++ {
				if x >= opts.sideBarWidth && x < totalWidth {
					cell := s.CellAt(x+opts.colOffset, row)
					if cell != nil {
						cell.Style.Fg = matchFg
						cell.Style.Bg = matchBg
						cell.Style.Attrs |= uv.AttrBold
						// On the cursor row: apply reverse so every search match
						// cell flips fg/bg consistently. Cursor's own AttrReverse
						// only covers cursorRange (key token in expanded mode, or
						// non-whitespace span otherwise), so without this OR a
						// search match outside cursorRange would render as a
						// normal match while one inside renders inverted.
						if opts.isUnderCursor {
							cell.Style.Attrs |= uv.AttrReverse
						}
					}
				}
			}
		}
	}

	// Step 5: Mark highlighting
	if opts.isMarked && opts.sideBarWidth < totalWidth {
		cell := s.CellAt(opts.sideBarWidth+opts.colOffset, row)
		if cell != nil {
			cell.Style.Fg = m.bundle.Config.Style.MarkFg.Color
			cell.Style.Bg = m.bundle.Config.Style.MarkBg.Color
		}
	}
}

// applyMatchSpans paints color-rule fg/bg over already-rendered cells on
// absolute row. xOffset/sideBarWidth/totalWidth match the renderLine math;
// cells that fall outside [sideBarWidth, totalWidth) are clipped. nil-Fg or
// nil-Bg axes are skipped so a fg-only or bg-only rule does not stomp the
// other axis.
func applyMatchSpans(s component.Screen, row, colOffset, xOffset, sideBarWidth, totalWidth int, spans []matchSpan) {
	for _, ms := range spans {
		for x := ms.Start - xOffset + sideBarWidth; x < ms.End-xOffset+sideBarWidth; x++ {
			if x < sideBarWidth || x >= totalWidth {
				continue
			}
			cell := s.CellAt(x+colOffset, row)
			if cell == nil {
				continue
			}
			if ms.Style.Fg != nil {
				cell.Style.Fg = ms.Style.Fg
			}
			if ms.Style.Bg != nil {
				cell.Style.Bg = ms.Style.Bg
			}
		}
	}
}

// placeLogs writes log row cells directly onto the parent canvas at logsRect.
// logsRect.W is the post-sidebar text width (subRects already subtracts the
// sidebar). sideBarWidth is the gutter width reserved on the left of the logs
// area (severity indicator, mark color); placeLogs reconstructs the full
// row width as w + sideBarWidth for renderLine. All cell writes are translated
// to absolute screen coordinates using logsRect.X/Y as offsets.
//
//nolint:gocyclo // viewport scan splits current log around the cursor; extracting halves loses readability.
func (m *Model) placeLogs(s component.Screen, logsRect component.Rect, sideBarWidth int) {
	w := logsRect.W // logsRect.W already excludes sidebar (see subRects)
	h := logsRect.H
	if h < 1 || w < 1 {
		return
	}

	if m.store == nil || m.store.GetLogCount() < 1 {
		return
	}

	colOffset := logsRect.X - sideBarWidth // sidebar sits at logsRect.X - sidebarWidth
	rowOffset := logsRect.Y

	// add lines needed up, split current log into two, do not handle current logLine
	linesIdx := max(m.cursor.y-1, 0)
	for i := range m.IterLogs(m.cursor.log, -1) {
		logLines := m.getRenderCacheEntry(i)
		from := min(m.cursor.logLine-1, len(logLines)-1)
		if i != m.cursor.log {
			from = len(logLines) - 1
		}
		for j := from; j >= 0 && linesIdx >= 0; j-- {
			m.renderLine(s, linesIdx, renderLineOpts{
				logIdx:        i,
				lineIdx:       j,
				line:          logLines[j].text,
				width:         w,
				sideBarWidth:  sideBarWidth,
				colOffset:     colOffset,
				rowOffset:     rowOffset,
				isUnderHover:  m.hasHover && i == m.hoverLog && j == m.hoverLine,
				isExpanded:    m.store.state.expanded[i],
				isMarked:      m.IsMarked(i),
				searchMatches: m.store.state.search[i],
				severity:      m.store.logs[i].metadata.Severity,
				tokens:        logLines[j].tokens,
				matchSpans:    logLines[j].matchSpans,
			})
			linesIdx--
		}
		if linesIdx < 0 {
			break
		}
	}

	// add lines needed down, split current log into two, handle current logLine
	linesIdx = m.cursor.y
	for i := range m.IterLogs(m.cursor.log, 1) {
		logLines := m.getRenderCacheEntry(i)
		from := min(m.cursor.logLine, len(logLines)-1)
		if i != m.cursor.log {
			from = 0
		}
		for j := from; j < len(logLines) && linesIdx < h; j++ {
			m.renderLine(s, linesIdx, renderLineOpts{
				logIdx:        i,
				lineIdx:       j,
				line:          logLines[j].text,
				width:         w,
				sideBarWidth:  sideBarWidth,
				colOffset:     colOffset,
				rowOffset:     rowOffset,
				isUnderCursor: i == m.cursor.log && j == m.cursor.logLine,
				isUnderHover:  m.hasHover && i == m.hoverLog && j == m.hoverLine,
				isExpanded:    m.store.state.expanded[i],
				isMarked:      m.IsMarked(i),
				searchMatches: m.store.state.search[i],
				severity:      m.store.logs[i].metadata.Severity,
				tokens:        logLines[j].tokens,
				matchSpans:    logLines[j].matchSpans,
			})
			linesIdx++
		}
		if linesIdx >= h {
			break
		}
	}
}
