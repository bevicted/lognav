package queryeditor

import (
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/queryeditor/highlight"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

// placeEmptyQuery draws the placeholder text as faint cells at the rect origin
// (Prompt is "" so content starts at r.X) and returns a real cursor at the origin
// when the editor is focused. The cursor is gated on focus to match the
// non-empty path — an unfocused editor must not park a visible cursor at the
// query-editor origin. The placeholder is drawn regardless.
func (m *Model) placeEmptyQuery(s component.Screen, r component.Rect, w int) *component.Cursor {
	uicanvas.PlaceText(s, r.X, r.Y, m.editor.Placeholder(), uv.Style{Attrs: uv.AttrFaint}, r.X+w)
	if m.editor.Focused() {
		return component.NewCursor(r.X, r.Y)
	}
	return nil
}

// placeQuery writes the query editor's cells directly onto the canvas at drawRect
// and returns the cursor in absolute screen coordinates when the editor is
// focused. It consumes the editor's display-line model (the single wrap
// authority): each visible row is one DisplayLine, and the cursor is derived
// from CursorDisplayLine/LineInfo — so the drawn text and the cursor cannot
// drift apart. The empty-value path delegates to placeEmptyQuery.
//
//nolint:gocyclo // hot rendering path with per-grapheme highlight token lookup; branches reflect token states.
func (m *Model) placeQuery(s component.Screen) *component.Cursor {
	r := m.drawRect
	w, h := r.W, r.H
	if w < 1 || h < 1 {
		return nil
	}
	if m.editor.Width() < 1 {
		return nil
	}
	if m.editor.Value() == "" {
		return m.placeEmptyQuery(s, r, w)
	}

	dls := m.editor.DisplayLines()
	scrollY := m.editor.ScrollYOffset()
	prompt := m.editor.Prompt()
	promptWidth := uniseg.StringWidth(prompt)

	for screenY := range h {
		uicanvas.FillRowAt(s, r.X, r.Y+screenY, w, uv.Style{})

		// Prompt is drawn on every row (matching the prior textarea render).
		x := 0
		for _, ru := range prompt {
			rw := uniseg.StringWidth(string(ru))
			s.SetCell(r.X+x, r.Y+screenY, &uv.Cell{Content: string(ru), Width: rw})
			x += rw
		}

		displayIdx := scrollY + screenY
		if displayIdx >= len(dls) {
			continue
		}
		dl := dls[displayIdx]

		col := promptWidth
		lineCol := 0
		gr := uniseg.NewGraphemes(string(dl.Runes))
		for gr.Next() {
			grapheme := gr.Str()
			gw := gr.Width()
			if gw == 0 {
				continue
			}
			if col >= w {
				break
			}

			logicalCol := dl.ColWidth + lineCol
			var style uv.Style
			if dl.LogicalIdx < len(m.highlightedTokens) {
				style = highlight.CellStyle(m.bundle.Config, m.highlightedTokens[dl.LogicalIdx], logicalCol)
			}

			s.SetCell(r.X+col, r.Y+screenY, &uv.Cell{Content: grapheme, Width: gw, Style: style})
			col += gw
			lineCol += gw
		}
	}

	cursorY := m.editor.CursorDisplayLine() - scrollY
	cursorX := promptWidth + m.editor.LineInfo().CharOffset
	if m.editor.Focused() && cursorY >= 0 && cursorY < h {
		return component.NewCursor(r.X+cursorX, r.Y+cursorY)
	}
	return nil
}
