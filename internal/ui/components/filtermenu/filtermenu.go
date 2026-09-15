// Package filtermenu implements the filter-menu overlay: a vertical list of
// editable rule pills (each a mode chip + value input) plus an Apply/Clear/Cancel
// button row, navigated by the unified content/buttons scheme. It occupies the
// root ui.Model's shared overlay slot like dialog/contextmenu and grabs focus on
// open. Every close calls the host close seam; Apply additionally posts
// msgs.FilterAppliedMsg after writing state.SetFilters.
package filtermenu

import (
	"slices"

	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/filter"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/buttonstrip"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

// focusZone is which of the two nav zones holds the cursor.
type focusZone uint8

const (
	focusRow     focusZone = iota // cursor is on a rule row (its input is focused)
	focusButtons                  // cursor is on the Apply/Clear/Cancel button row
)

// Button indices on the button row (Apply is the primary/default).
const (
	btnApply = iota
	btnClear
	btnCancel
)

var buttonLabels = [...]string{btnApply: "Apply", btnClear: "Clear", btnCancel: "Cancel"}

// chipPadW is the cell width every mode chip is padded to in the menu so the
// value columns line up across rows. "lacks field" (11 cells) is the longest
// mode label.
const chipPadW = len("lacks field")

// row is one editable rule pill: a mode plus an always-present value input.
type row struct {
	typ   filter.Type
	input *uiinput.Input
}

// Compile-time seam opt-ins: the authoritative list of the optional component
// seams *Model implements. Every implemented seam belongs here — a seam rename
// or signature change would otherwise silently demote its handler to an
// ordinary method and drop that input with no build error.
var (
	_ component.Component        = (*Model)(nil)
	_ component.KeyTarget        = (*Model)(nil)
	_ component.MouseTarget      = (*Model)(nil)
	_ component.MouseHoverTarget = (*Model)(nil)
	_ component.Sizer            = (*Model)(nil)
)

// Model is the filter-menu overlay.
type Model struct {
	bundle   deps.Bundle
	drawRect component.Rect

	rows           []row
	focus          focusZone
	cursorRow      int
	selectedButton int

	kh     *keys.Handler
	poster msgs.Poster

	// onClose is the host seam: the root's overlay-slot teardown. Every close
	// path calls it synchronously (menu input handling all runs on the loop
	// goroutine, and so does the root that owns the slot).
	onClose func()
}

// New builds a filter menu prefilled with rules plus a trailing blank Unknown
// placeholder. The cursor starts on the first row with its input focused
// (always-editing). bundle carries config + state.
func New(bundle deps.Bundle, rules []filter.Rule) *Model {
	m := &Model{bundle: bundle, focus: focusRow}
	for _, r := range rules {
		m.rows = append(m.rows, m.newRow(r.Type, r.Value))
	}
	m.rows = append(m.rows, m.newRow(filter.Unknown, "")) // trailing placeholder
	m.rows[0].input.Focus()
	m.bindKeys()
	return m
}

// newRow makes a row with an input prefilled to value.
func (m *Model) newRow(t filter.Type, value string) row {
	in := uiinput.New()
	in.SetValue(value)
	return row{typ: t, input: in}
}

// bindKeys registers the menu's row-command and nav bindings. Tab/Shift+Tab are
// bound literally (not via Keys.Next/Prev, which conflate tab with the arrows);
// vertical nav uses the bare arrow keys (not Keys.MoveUp, whose `k` alias must
// remain typeable in a row input). ctrl+c (Keys.Clear), Enter (Keys.Accept), and
// Esc (Keys.Cancel) reuse the config bindings.
func (m *Model) bindKeys() {
	m.kh = keys.New().Bind(
		keys.Binding{Keys: []string{"tab"}, Context: "next rule type", Action: func() { m.cycleType(+1) }},
		keys.Binding{Keys: []string{"shift+tab"}, Context: "previous rule type", Action: func() { m.cycleType(-1) }},
		keys.Binding{Keys: []string{"down"}, Context: "next row / buttons", Action: func() { m.moveRow(+1) }},
		keys.Binding{Keys: []string{"up"}, Context: "previous row / content", Action: func() { m.moveRow(-1) }},
		keys.Binding{Keys: m.bundle.Config.Keys.Clear, Context: "clear rule value", Action: m.clearRow},
		keys.Binding{Keys: m.bundle.Config.Keys.Accept, Context: "press button", Action: m.pressButton},
		keys.Binding{Keys: m.bundle.Config.Keys.Cancel, Context: "cancel", Action: m.Dismiss},
	)
}

// Init satisfies component.Component.
func (m *Model) Init() {}

// SetPoster injects the runtime poster used to post FilterAppliedMsg off-loop.
func (m *Model) SetPoster(p msgs.Poster) { m.poster = p }

// SetOnClose injects the host close seam. The root passes the callback that
// clears its overlay slot and releases focus; the menu calls it on Apply/Cancel.
func (m *Model) SetOnClose(f func()) { m.onClose = f }

// Focus is the overlay focus-grab hook (the root calls it on open). The active
// row input is already focused in New; this is a no-op placeholder kept for the
// contextmenu-style wiring symmetry. It satisfies the root's Focus() expectation.
//
// Unlike contextmenu.Focus, this does NOT post GrabFocus:true. The close arm in
// ui.go still calls releaseFocus(), which is safe only because nothing else holds
// root focus when the f/pill-click open path is reachable. If a future component
// grabs focus before opening this menu, add an explicit GrabFocus post here and
// update the close arm accordingly.
func (m *Model) Focus() {}

// GetKeybinds satisfies component.Component.
func (m *Model) GetKeybinds() []keys.Binding { return m.kh.GetKeybinds() }

// Box chrome: rounded border (1 each side) + one column of horizontal padding.
const (
	boxBorderW = 1
	boxHPad    = 1
)

// chipSpace is the cell gap between a row's chip column and its value line.
const chipSpace = 1

// chipLabel returns the row's mode label padded (right) to chipPadW so value
// columns line up. Unknown renders as an all-blank chip column.
func chipLabel(t filter.Type) string {
	label := t.Label() // "" for Unknown
	for len(label) < chipPadW {
		label += " "
	}
	return label
}

// chipStyle / lineStyle are the pill band styles from config: distinct
// backgrounds, one shared foreground (PillFg).
func (m *Model) chipStyle() uv.Style {
	st := m.bundle.Config.Style
	return uv.Style{Bg: st.PillChipBg.Color, Fg: st.PillFg.Color, Attrs: uv.AttrBold}
}

func (m *Model) lineStyle() uv.Style {
	st := m.bundle.Config.Style
	return uv.Style{Bg: st.PillLineBg.Color, Fg: st.PillFg.Color}
}

// contentW is the inner content width: the chip column + a gap + the widest value
// (or a small floor) + one cell for the end-of-line caret, clamped later by the
// box. Without the caret cell the value input scrolls one column when the caret
// sits just past the last char of the widest value, cropping its first character.
func (m *Model) contentW() int {
	valueW := 10
	for _, r := range m.rows {
		if w := uniseg.StringWidth(r.input.Value()); w > valueW {
			valueW = w
		}
	}
	return chipPadW + chipSpace + valueW + 1
}

// PreferredSize fits the box to content: width = contentW + chrome; height = one
// row per rule + a button row + chrome. Clamped to maxW/maxH.
func (m *Model) PreferredSize(maxW, maxH int) (w, h int) {
	w = m.contentW() + 2*(boxBorderW+boxHPad)
	h = len(m.rows) + 1 + 2*boxBorderW // rows + button row + top/bottom border
	return min(w, maxW), min(h, maxH)
}

// SetRect stores the assigned box rect.
func (m *Model) SetRect(r component.Rect) { m.drawRect = r }

// Draw paints the rounded box, then one pill per rule row (chip + value-line
// input), then the button row. The active row's input renders its own
// reverse-video caret; Draw returns nil (no terminal cursor).
func (m *Model) Draw(s component.Screen) *component.Cursor {
	r := m.drawRect
	if r.W < 1 || r.H < 1 {
		return nil
	}
	uicanvas.DrawBox(s, r, uicanvas.RoundedSet, uv.Style{})
	// Title drawn onto the top border ("╭ Filter ──╮") so it costs no inner row
	// and leaves the rule/button geometry untouched. Clipped before the corner.
	uicanvas.PlaceText(s, r.X+2, r.Y, " Filter ", uv.Style{Attrs: uv.AttrBold}, r.X+r.W-1)
	inner := uicanvas.Pad(r, boxBorderW, boxBorderW+boxHPad, boxBorderW, boxBorderW+boxHPad)
	if inner.W < 1 || inner.H < 1 {
		return nil
	}
	maxX := inner.X + inner.W

	for i := range m.rows {
		y := inner.Y + i
		if y >= inner.Y+inner.H {
			break
		}
		m.drawRuleRow(s, inner.X, y, i, maxX)
	}

	// The selected button is always drawn bold+reverse, so the default (Apply)
	// stays visibly highlighted even while a rule row is being edited.
	buttonsY := inner.Y + len(m.rows)
	if buttonsY < inner.Y+inner.H {
		m.strip(inner).Draw(s, buttonsY, m.selectedButton)
	}
	return nil
}

// drawRuleRow draws the chip (via uicanvas.DrawPill) then the value input on its
// PillLine band, sized to fill the remaining width. The active row's input
// renders the caret (it is focused via focusActiveRowInput).
func (m *Model) drawRuleRow(s component.Screen, x, y, i, maxX int) {
	// Draw the chip pill; DrawPill returns the column reached after the chip
	// (line argument empty — the value is the focusable input drawn next).
	chipEnd := uicanvas.DrawPill(s, x, y, chipLabel(m.rows[i].typ), "", m.chipStyle(), m.lineStyle(), maxX)
	lineX := chipEnd + chipSpace
	if lineX >= maxX {
		return
	}
	// Paint the chip→value separator with the line band style so the pill reads
	// as one continuous band (matches canvas.DrawPill's styled separator); without
	// it the cell shows the terminal default as a gap between the two bands.
	uicanvas.FillSpan(s, chipEnd, y, chipSpace, m.lineStyle(), maxX)
	in := m.rows[i].input
	in.SetCellStyle(m.lineStyle())
	in.SetWidth(maxX - lineX)
	in.Draw(s, component.Rect{X: lineX, Y: y, W: maxX - lineX, H: 1})
}

// strip builds the shared Apply/Clear/Cancel button strip for the menu's padded
// inner rect: centered within it and clipped at its right edge.
func (m *Model) strip(inner component.Rect) buttonstrip.Strip {
	return buttonstrip.Strip{Labels: buttonLabels[:], X: inner.X, MaxX: inner.X + inner.W}
}

// OnMouseClick implements component.MouseTarget. On a rule row, a click on the
// mode chip (its column plus the gap) cycles to the next mode, while a click on
// the value band focuses the input at the clicked offset; a click on the button
// row selects+presses that button; any other in-box click is a consumed no-op.
func (m *Model) OnMouseClick(x, y int, _ uv.MouseButton) bool {
	inner := uicanvas.Pad(m.drawRect, boxBorderW, boxBorderW+boxHPad, boxBorderW, boxBorderW+boxHPad)
	if inner.W < 1 || inner.H < 1 {
		return false
	}
	row := y - inner.Y
	if row >= 0 && row < len(m.rows) {
		m.focus = focusRow
		m.cursorRow = row
		m.focusActiveRowInput()
		// Chip column (+ its gap) cycles the rule mode; value band places the caret.
		lineX := inner.X + chipPadW + chipSpace
		if x < lineX {
			m.cycleType(+1)
		} else {
			m.rows[row].input.SetCursor(m.rows[row].input.RuneIndexAtViewportX(x - lineX))
		}
		return true
	}
	if row == len(m.rows) { // button row
		if idx := m.strip(inner).HitTest(x); idx >= 0 {
			m.focus = focusButtons
			m.selectedButton = idx
			m.pressButton()
		}
		return true
	}
	return true // in-box no-op
}

// OnMouseHover implements component.MouseHoverTarget: moving the pointer over a
// button moves the selection onto it (so it highlights and a click presses it).
// Hovering any non-button cell leaves the selection untouched. Returns true when
// the selection moved; the move-and-report rule itself lives in
// buttonstrip.Strip.Hover. Dispatch is gated upstream by core.enableHover, like
// every other overlay's hover.
func (m *Model) OnMouseHover(x, y int) bool {
	inner := uicanvas.Pad(m.drawRect, boxBorderW, boxBorderW+boxHPad, boxBorderW, boxBorderW+boxHPad)
	if inner.W < 1 || inner.H < 1 {
		return false
	}
	if y != inner.Y+len(m.rows) { // not the button row
		return false
	}
	return m.strip(inner).Hover(x, &m.selectedButton)
}

// ClearMouseHover is a no-op because hover moves the keyboard button selection;
// the menu has no separate pointer-only highlight to clear.
func (*Model) ClearMouseHover() bool { return false }

// HandleKey dispatches a key with the menu's precedence: row-command bindings
// (Tab/arrows/ctrl+d/Enter/Esc) first; then, on the button row, Left/Right move
// the selection; then the empty-row Backspace/Del commands (tested via the
// active row's value being empty) BEFORE delegating to the focused input — the
// input consumes Backspace/Del even on an empty value, so these must intercept.
// Otherwise an unmatched key is fed to the active row's input; the first
// character typed on the trailing placeholder commits it to Include and appends a
// fresh placeholder (auto-grow).
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if m.kh.Run(ev) {
		return component.KeyHandled
	}

	if m.focus == focusButtons {
		switch ev.Code {
		case uv.KeyLeft:
			m.moveButton(-1)
		case uv.KeyRight:
			m.moveButton(+1)
		}
		return component.KeyHandled
	}

	// Empty-row Backspace/Del are row commands; non-empty rows pass them to the
	// input as normal character edits.
	if len(m.rows) > 0 && m.rows[m.cursorRow].input.Value() == "" {
		switch ev.Code {
		case uv.KeyBackspace:
			m.deletePrev()
			return component.KeyHandled
		case uv.KeyDelete:
			m.deleteHere()
			return component.KeyHandled
		}
	}

	// Auto-grow: first input on the trailing placeholder commits it to Include.
	if len(m.rows) > 0 && m.isPlaceholder(m.cursorRow) && ev.Text != "" {
		m.commitPlaceholder(filter.Include)
	}

	if len(m.rows) > 0 {
		m.rows[m.cursorRow].input.HandleKey(ev)
	}
	return component.KeyHandled
}

// pressButton activates the selected button. A row-focused Enter (the field is
// the form) always submits the default Apply, regardless of which button the
// pointer last hovered onto; only a button-row press honors selectedButton.
func (m *Model) pressButton() {
	if m.focus != focusButtons {
		m.apply()
		return
	}
	switch m.selectedButton {
	case btnClear:
		m.clearAll()
	case btnCancel:
		m.Dismiss()
	default: // btnApply
		m.apply()
	}
}

// clearAll empties the whole rule list in place, leaving only a blank
// placeholder, and parks focus on the Apply button (the conventional
// clear -> apply flow) so a button stays visibly selected. Does not itself
// apply. focusActiveRowInput blurs the row inputs since focus is on the buttons.
func (m *Model) clearAll() {
	m.rows = []row{m.newRow(filter.Unknown, "")}
	m.cursorRow = 0
	m.focus = focusButtons
	m.selectedButton = btnApply
	m.focusActiveRowInput()
}

// prunedRules returns the applicable rules: every real (non-Unknown) row with a
// non-empty value, in order. Unknown/empty rows are dropped.
func (m *Model) prunedRules() []filter.Rule {
	var out []filter.Rule
	for _, r := range m.rows {
		if r.typ == filter.Unknown || r.input.Value() == "" {
			continue
		}
		out = append(out, filter.Rule{Type: r.typ, Value: r.input.Value()})
	}
	return out
}

// apply prunes empty/unknown rows, writes the rule set to shared state, posts
// FilterAppliedMsg (triggers the recompute) off-loop, then closes the overlay
// through the host seam.
func (m *Model) apply() {
	m.bundle.State.SetFilters(m.prunedRules())
	m.postApplied()
	m.close()
}

// Dismiss closes the menu without applying (Cancel button / Esc / click-outside).
// The previously-applied rules in state are untouched.
func (m *Model) Dismiss() { m.close() }

// close invokes the host close seam. Every close path (key handling, mouse
// click, the root calling Dismiss directly) runs on the loop goroutine, and so
// does the root's slot teardown, so the call is a plain synchronous one.
func (m *Model) close() {
	if m.onClose != nil {
		m.onClose()
	}
}

// postApplied posts FilterAppliedMsg from the loop goroutine (button Cmds and
// keybind actions), hence msgs.PostAsync.
func (m *Model) postApplied() {
	msgs.PostAsync(m.poster, msgs.FilterAppliedMsg{})
}

// focusActiveRowInput focuses the input of the active row and blurs all others,
// keeping the always-editing invariant. A no-op when focus is on the buttons.
func (m *Model) focusActiveRowInput() {
	for i := range m.rows {
		if m.focus == focusRow && i == m.cursorRow {
			m.rows[i].input.Focus()
		} else {
			m.rows[i].input.Blur()
		}
	}
}

// moveRow moves vertically by dir (+1 down, -1 up). Down off the last row enters
// the button row; Up from the button row returns to the last row and resets the
// selected button to Apply. Within the rows it clamps at the first row.
func (m *Model) moveRow(dir int) {
	if m.focus == focusButtons {
		if dir < 0 { // up: back into the rows, last row
			m.focus = focusRow
			m.cursorRow = len(m.rows) - 1
			m.selectedButton = btnApply
			m.focusActiveRowInput()
		}
		return
	}
	next := m.cursorRow + dir
	if next >= len(m.rows) { // off the last row -> buttons
		m.focus = focusButtons
		m.selectedButton = btnApply
		m.focusActiveRowInput()
		return
	}
	if next < 0 {
		next = 0
	}
	m.cursorRow = next
	m.focusActiveRowInput()
}

// moveButton moves the selected button by dir, clamped to [btnApply, btnCancel].
func (m *Model) moveButton(dir int) {
	if m.focus != focusButtons {
		return
	}
	m.selectedButton = min(max(m.selectedButton+dir, btnApply), btnCancel)
}

// clearRow empties the active row's value (ctrl+d). It does not delete the row.
func (m *Model) clearRow() {
	if m.focus != focusRow || len(m.rows) == 0 {
		return
	}
	m.rows[m.cursorRow].input.SetValue("")
}

// removeRowAt deletes row i.
func (m *Model) removeRowAt(i int) {
	m.rows = slices.Delete(m.rows, i, i+1)
}

// deletePrev handles Backspace on an empty active row: delete it and land on the
// previous row (caret at end). On the first row, land on the new first row, or on
// the sole remaining placeholder if the list emptied. On the trailing
// placeholder, just move up (never delete it).
func (m *Model) deletePrev() {
	if m.focus != focusRow || len(m.rows) == 0 {
		return
	}
	i := m.cursorRow
	if m.isPlaceholder(i) {
		if i > 0 {
			m.cursorRow = i - 1
			m.rows[m.cursorRow].input.SetCursor(len(m.rows[m.cursorRow].input.Value()))
		}
		m.focusActiveRowInput()
		return
	}
	m.removeRowAt(i)
	if i > 0 {
		m.cursorRow = i - 1
		m.rows[m.cursorRow].input.SetCursor(len(m.rows[m.cursorRow].input.Value()))
	} else {
		m.cursorRow = 0
	}
	m.focusActiveRowInput()
}

// deleteHere handles Del on an empty active row: delete it; following rows shift
// up and the cursor lands on the next (now at the same index). The trailing
// placeholder is never deleted (no-op).
func (m *Model) deleteHere() {
	if m.focus != focusRow || len(m.rows) == 0 {
		return
	}
	i := m.cursorRow
	if m.isPlaceholder(i) {
		return
	}
	m.removeRowAt(i)
	if m.cursorRow >= len(m.rows) {
		m.cursorRow = len(m.rows) - 1
	}
	m.focusActiveRowInput()
}

// realTypes is the cycle order for real (applicable) rule types. Unknown is the
// untouched-placeholder sentinel and is never in the cycle.
var realTypes = [...]filter.Type{filter.Include, filter.Exclude, filter.HasField, filter.LacksField}

// isPlaceholder reports whether row i is the trailing untouched placeholder
// (Unknown type, empty value).
func (m *Model) isPlaceholder(i int) bool {
	return m.rows[i].typ == filter.Unknown && m.rows[i].input.Value() == ""
}

// commitPlaceholder turns the active placeholder row into a real row of type t
// and appends a fresh trailing placeholder. Caller guarantees the active row is
// the placeholder.
func (m *Model) commitPlaceholder(t filter.Type) {
	m.rows[m.cursorRow].typ = t
	m.rows = append(m.rows, m.newRow(filter.Unknown, ""))
}

// cycleType advances the active row's type by dir (+1 forward, -1 back) through
// realTypes, wrapping. On the untouched placeholder, the first cycle commits it
// (defaulting to Include forward, LacksField back) and appends a new placeholder.
func (m *Model) cycleType(dir int) {
	if m.focus != focusRow || len(m.rows) == 0 {
		return
	}
	i := m.cursorRow
	if m.isPlaceholder(i) {
		if dir < 0 {
			m.commitPlaceholder(filter.LacksField)
		} else {
			m.commitPlaceholder(filter.Include)
		}
		return
	}
	cur := 0
	for k, t := range realTypes {
		if t == m.rows[i].typ {
			cur = k
			break
		}
	}
	next := (cur + dir + len(realTypes)) % len(realTypes)
	m.rows[i].typ = realTypes[next]
}
