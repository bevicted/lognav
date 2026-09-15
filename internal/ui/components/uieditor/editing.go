package uieditor

import (
	"strings"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

// HandleKey applies one editing key from the daily-driver keymap and reports
// whether the buffer was mutated (so the parent rehighlights only on real edits;
// pure motion and unmapped keys return false). The widget is a leaf — the parent
// returns KeyHandled regardless — so this bool gates rehighlight, not consumption.
//
// Scope is deliberately small (see package doc): arrows, alt+arrows (word
// motion), home/end, backspace/delete (+ alt/ctrl+w word delete), enter, and
// printable insertion. Every other key (emacs ctrl-aliases, kill-line, page
// keys, etc.) falls through to the printable check, which rejects it because a
// Ctrl/Alt modifier is set or ev.Text is empty.
//
//nolint:gocyclo // multi-line editing-key state machine; one branch per key, splitting would hide the table.
func (e *Editor) HandleKey(ev uv.KeyPressEvent) bool {
	alt := ev.Mod&uv.ModAlt != 0
	ctrl := ev.Mod&uv.ModCtrl != 0

	switch ev.Code {
	case uv.KeyLeft:
		switch {
		case alt:
			e.wordLeft()
		case !ctrl:
			e.characterLeft()
		}
		return false
	case uv.KeyRight:
		switch {
		case alt:
			e.wordRight()
		case !ctrl:
			e.characterRight()
		}
		return false
	case uv.KeyUp:
		if !alt && !ctrl {
			e.moveVertical(-1)
		}
		return false
	case uv.KeyDown:
		if !alt && !ctrl {
			e.moveVertical(1)
		}
		return false
	case uv.KeyHome:
		if !alt && !ctrl {
			e.lineStart()
		}
		return false
	case uv.KeyEnd:
		if !alt && !ctrl {
			e.lineEnd()
		}
		return false
	case uv.KeyBackspace:
		if alt {
			return e.deleteWordLeft()
		}
		if !ctrl {
			return e.deleteCharBackward()
		}
		return false
	case uv.KeyDelete:
		if alt {
			return e.deleteWordRight()
		}
		if !ctrl {
			return e.deleteCharForward()
		}
		return false
	case uv.KeyEnter:
		if !alt && !ctrl {
			e.insertString("\n")
			return true
		}
		return false
	}

	// ctrl+w — the one ctrl alias kept (delete word backward).
	if ctrl && !alt && ev.Code == 'w' {
		return e.deleteWordLeft()
	}

	// Printable insertion: no Ctrl/Alt modifier. insertString sanitizes control
	// runes, so a focused Tab (or other control key) is a harmless no-op.
	if !ctrl && !alt && ev.Text != "" {
		e.insertString(ev.Text)
		return true
	}
	return false
}

// insertString inserts s at the cursor, splitting on "\n" into new logical lines
// (the single path for keystrokes, snippets, paste, and Enter). Control runes
// other than "\n" are stripped, matching textarea's sanitizer. The cursor lands
// after the inserted text; the original line's tail re-appends after it.
func (e *Editor) insertString(s string) {
	s = sanitize(s)
	if s == "" {
		return
	}
	parts := strings.Split(s, "\n")
	line := e.value[e.row]
	head := append([]rune(nil), line[:e.col]...)
	tail := append([]rune(nil), line[e.col:]...)

	newLines := make([][]rune, len(parts))
	for i, p := range parts {
		newLines[i] = []rune(p)
	}
	newLines[0] = append(head, newLines[0]...)
	last := len(newLines) - 1
	e.col = len(newLines[last])
	newLines[last] = append(newLines[last], tail...)

	// Rebuild value with a fresh backing slice to avoid append aliasing.
	rebuilt := make([][]rune, 0, len(e.value)+last)
	rebuilt = append(rebuilt, e.value[:e.row]...)
	rebuilt = append(rebuilt, newLines...)
	rebuilt = append(rebuilt, e.value[e.row+1:]...)
	e.value = rebuilt
	e.row += last
	e.repositionView()
}

// InsertString inserts s at the cursor (snippets and paste route here). It is
// the exported entry to the one insertion path; the caller rehighlights after.
func (e *Editor) InsertString(s string) { e.insertString(s) }

// sanitize drops control runes except "\n" (which insertString splits on),
// mirroring textarea's input sanitizer so stray Tab/CR keystrokes are no-ops.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// --- horizontal motion ---

func (e *Editor) characterLeft() {
	switch {
	case e.col > 0:
		e.col--
	case e.row > 0:
		e.row--
		e.col = len(e.value[e.row])
	}
	e.repositionView()
}

func (e *Editor) characterRight() {
	switch {
	case e.col < len(e.value[e.row]):
		e.col++
	case e.row < len(e.value)-1:
		e.row++
		e.col = 0
	}
	e.repositionView()
}

func (e *Editor) lineStart() { e.col = 0; e.repositionView() }
func (e *Editor) lineEnd()   { e.col = len(e.value[e.row]); e.repositionView() }

// wordLeft moves over any spaces immediately left of the cursor, then over the
// preceding word (boundary = unicode.IsSpace). At line start it hops to the end
// of the previous line.
func (e *Editor) wordLeft() {
	line := e.value[e.row]
	moved := false
	for e.col > 0 && unicode.IsSpace(line[e.col-1]) {
		e.col--
		moved = true
	}
	for e.col > 0 && !unicode.IsSpace(line[e.col-1]) {
		e.col--
		moved = true
	}
	if !moved && e.row > 0 {
		e.row--
		e.col = len(e.value[e.row])
	}
	e.repositionView()
}

// wordRight moves over any spaces at the cursor, then over the following word.
// At line end it hops to the start of the next line.
func (e *Editor) wordRight() {
	line := e.value[e.row]
	moved := false
	for e.col < len(line) && unicode.IsSpace(line[e.col]) {
		e.col++
		moved = true
	}
	for e.col < len(line) && !unicode.IsSpace(line[e.col]) {
		e.col++
		moved = true
	}
	if !moved && e.row < len(e.value)-1 {
		e.row++
		e.col = 0
	}
	e.repositionView()
}

// moveVertical moves the cursor one display line up (delta -1) or down (+1),
// preserving the horizontal display column. This is the wrap-aware navigation
// that is the migration's highest cursor-drift risk: it maps (row,col) to a
// display position via the shared wrap model, steps the display index, then maps
// the target display column back to (row,col). No sticky-column memory across
// repeated moves (accepted divergence from textarea).
func (e *Editor) moveVertical(delta int) {
	dls := e.DisplayLines()
	target := e.CursorDisplayLine() + delta
	if target < 0 || target >= len(dls) {
		return
	}
	wantX := e.LineInfo().CharOffset
	e.placeAtDisplayLine(dls[target], wantX)
	e.repositionView()
}

// placeAtDisplayLine sets the caret onto display line dl at the target display
// column wantX (measured from the segment's text start, prompt excluded). It is
// the shared display->logical walk used by both moveVertical and
// SetCursorAtDisplay: the row becomes dl's logical index, then dl's runes are
// walked from StartCol accumulating display width until the next rune would pass
// wantX. The column is clamped to the logical line length so the caret never
// lands in a wrap-added trailing space past the line end.
func (e *Editor) placeAtDisplayLine(dl DisplayLine, wantX int) {
	e.row = dl.LogicalIdx
	col := dl.StartCol
	w := 0
	for _, r := range dl.Runes {
		rwid := uniseg.StringWidth(string(r))
		if w+rwid > wantX {
			break
		}
		w += rwid
		col++
	}
	e.col = min(col, len(e.value[e.row]))
}

// --- deletion ---

func (e *Editor) deleteCharBackward() bool {
	if e.col > 0 {
		line := e.value[e.row]
		e.value[e.row] = append(line[:e.col-1], line[e.col:]...)
		e.col--
		e.repositionView()
		return true
	}
	if e.row > 0 {
		e.mergeLineAbove()
		e.repositionView()
		return true
	}
	return false
}

func (e *Editor) deleteCharForward() bool {
	line := e.value[e.row]
	if e.col < len(line) {
		e.value[e.row] = append(line[:e.col], line[e.col+1:]...)
		e.repositionView()
		return true
	}
	if e.row < len(e.value)-1 {
		e.mergeLineBelow()
		e.repositionView()
		return true
	}
	return false
}

// deleteWordLeft deletes from the start of the word left of the cursor (skipping
// any intervening spaces) up to the cursor. At column 0 it falls back to a
// backward character delete (merging the line above).
func (e *Editor) deleteWordLeft() bool {
	if e.col == 0 {
		return e.deleteCharBackward()
	}
	line := e.value[e.row]
	start := e.col
	for start > 0 && unicode.IsSpace(line[start-1]) {
		start--
	}
	for start > 0 && !unicode.IsSpace(line[start-1]) {
		start--
	}
	if start == e.col {
		return false
	}
	e.value[e.row] = append(line[:start], line[e.col:]...)
	e.col = start
	e.repositionView()
	return true
}

// deleteWordRight deletes from the cursor over any spaces and the following word.
// At line end it falls back to a forward character delete (merging the line below).
func (e *Editor) deleteWordRight() bool {
	line := e.value[e.row]
	if e.col >= len(line) {
		return e.deleteCharForward()
	}
	end := e.col
	for end < len(line) && unicode.IsSpace(line[end]) {
		end++
	}
	for end < len(line) && !unicode.IsSpace(line[end]) {
		end++
	}
	if end == e.col {
		return false
	}
	e.value[e.row] = append(line[:e.col], line[end:]...)
	e.repositionView()
	return true
}

// mergeLineAbove joins the cursor's line onto the previous one, placing the
// cursor at the seam.
func (e *Editor) mergeLineAbove() {
	prev := e.value[e.row-1]
	cur := e.value[e.row]
	e.col = len(prev)
	e.value[e.row-1] = append(prev, cur...)
	e.value = append(e.value[:e.row], e.value[e.row+1:]...)
	e.row--
}

// mergeLineBelow joins the next line onto the cursor's line; the cursor is left
// where it was (at the seam).
func (e *Editor) mergeLineBelow() {
	cur := e.value[e.row]
	next := e.value[e.row+1]
	e.value[e.row] = append(cur, next...)
	e.value = append(e.value[:e.row+1], e.value[e.row+2:]...)
}

// repositionView scrolls the viewport (in display lines) so the cursor's display
// line stays within [yOffset, yOffset+height-1], then clamps to valid bounds.
func (e *Editor) repositionView() {
	cur := e.CursorDisplayLine()
	switch {
	case cur < e.yOffset:
		e.yOffset = cur
	case cur > e.yOffset+e.height-1:
		e.yOffset = cur - e.height + 1
	}
	e.clampScroll()
}
