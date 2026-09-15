package dialog

import (
	"strings"

	"github.com/bevicted/lognav/internal/deps"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/buttonstrip"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// inputDesiredW is the minimum inner width reserved for the input field so the
// dialog stays usable even when the title/message are short.
const inputDesiredW = 30

type keyHandlers struct {
	alwaysHandle *keys.Handler
	focused      *keys.Handler
	unfocused    *keys.Handler
}

// Compile-time seam opt-ins: the authoritative list of the optional component
// seams *Model implements. Every implemented seam belongs here — a seam rename
// or signature change would otherwise silently demote its handler to an
// ordinary method and drop that input with no build error.
var (
	_ component.KeyTarget        = (*Model)(nil)
	_ component.MouseTarget      = (*Model)(nil)
	_ component.MousePasteTarget = (*Model)(nil)
	_ component.MouseHoverTarget = (*Model)(nil)
	_ component.PasteTarget      = (*Model)(nil)
	_ component.Sizer            = (*Model)(nil)
)

// Model represents a dialog overlay with a title, message, and buttons.
// It implements component.Component so it can occupy the root ui.Model's
// shared overlay slot alongside helpoverlay.
type Model struct {
	bundle         deps.Bundle
	drawRect       component.Rect
	Title          string
	Message        string
	linkURL        string
	Buttons        []msgs.DialogButton
	selectedButton int
	kh             keyHandlers
	input          *uiinput.Input
	onCancel       func()
	errorText      string
	pillChip       string

	// onClose is the host seam: the root's overlay-slot teardown. Every close
	// path calls it synchronously (dialog input handling all runs on the loop
	// goroutine, and so does the root that owns the slot).
	onClose func()
}

// New creates a dialog Model from a ShowDialogMsg.
func New(bundle deps.Bundle, msg msgs.ShowDialogMsg) *Model {
	m := &Model{
		bundle:   bundle,
		Title:    msg.Title,
		Message:  msg.Message,
		linkURL:  msg.LinkURL,
		Buttons:  msg.Buttons,
		onCancel: msg.OnCancel,
		pillChip: msg.PillChip,
	}
	m.kh.alwaysHandle = keys.New().Bind(
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Accept,
			Context: "confirm",
			Action: func() {
				// While the input is focused there is no visible button
				// selection, so Enter means "accept the form" = the primary
				// button. With focus on the button row (input blurred), Enter
				// fires whatever button is selected.
				if m.input != nil && m.input.Focused() {
					m.selectedButton = 0
				}
				m.confirm()
			},
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Cancel,
			Context: "dismiss",
			Action: func() {
				// Esc cancels unconditionally (no blur-first stage): the unified
				// nav contract makes Esc == Cancel everywhere, and the focused
				// input has no separate "commit" that a first Esc should abort.
				m.Dismiss()
			},
		},
		keys.Binding{
			Keys:    []string{"down"},
			Context: "to buttons",
			Action: func() {
				if m.input != nil && m.input.Focused() {
					m.input.Blur()
					m.selectedButton = 0
				}
			},
		},
		keys.Binding{
			Keys:    []string{"up"},
			Context: "to input",
			Action: func() {
				if m.input != nil && !m.input.Focused() {
					m.input.Focus()
					m.selectedButton = 0
				}
			},
		},
	)
	m.kh.focused = keys.New().Bind(
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Clear,
			Context: "clear input",
			Action: func() {
				m.input.SetValue("")
			},
		},
	)
	m.kh.unfocused = keys.New().Bind(
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Next,
			Context: "next button",
			Action:  m.nextButton,
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.MoveRight,
			Context: "next button",
			Action:  m.nextButton,
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Prev,
			Context: "previous button",
			Action:  m.prevButton,
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.MoveLeft,
			Context: "previous button",
			Action:  m.prevButton,
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Quit,
			Context: "dismiss",
			Action:  m.Dismiss,
		},
	)
	if msg.Input != nil {
		m.input = msg.Input
		m.input.Focus()
		if m.pillChip != "" {
			m.input.SetPrompt(m.pillChip + " ")
			_, line := m.pillStyles()
			m.input.SetCellStyle(line)
		}
	}
	return m
}

// pillStyles returns the chip and line band styles for the dialog's pill input.
// It reads Style.PillChipBg/PillLineBg (with the shared PillFg foreground) when
// those background fields are set, otherwise returns zero styles (a transparent,
// un-banded input).
func (m *Model) pillStyles() (chip, line uv.Style) {
	st := m.bundle.Config.Style
	if st.PillChipBg.IsSet() {
		chip = uv.Style{Bg: st.PillChipBg.Color, Fg: st.PillFg.Color, Attrs: uv.AttrBold}
	}
	if st.PillLineBg.IsSet() {
		line = uv.Style{Bg: st.PillLineBg.Color, Fg: st.PillFg.Color}
	}
	return chip, line
}

// SetOnClose injects the host close seam. The root passes the callback that
// clears its overlay slot; the dialog calls it on confirm/dismiss.
func (m *Model) SetOnClose(f func()) {
	m.onClose = f
}

func (m *Model) nextButton() {
	if n := len(m.Buttons); n > 0 {
		m.selectedButton = (m.selectedButton + 1) % n
	}
}

func (m *Model) prevButton() {
	if n := len(m.Buttons); n > 0 {
		m.selectedButton = (m.selectedButton - 1 + n) % n
	}
}

func (m *Model) inputValue() string {
	if m.input != nil {
		return m.input.Value()
	}
	return ""
}

// confirm invokes the selected button's Cmd. A non-nil error keeps the dialog
// open and stores the message in errorText (the jq dialog's parse/compile veto);
// nil (or no Cmd) closes the dialog via the host seam.
func (m *Model) confirm() {
	if n := len(m.Buttons); n > 0 && m.Buttons[m.selectedButton].Cmd != nil {
		if err := m.Buttons[m.selectedButton].Cmd(m.inputValue()); err != nil {
			m.errorText = err.Error()
			return
		}
	}
	m.errorText = ""
	m.close()
}

// Dismiss cancels and closes the dialog: it runs OnCancel for its side effect,
// then closes. It is both the keyboard Cancel action and the root ui.Model's
// entry point for a click outside the dialog rect (bypassing the input-focused
// blur stage).
func (m *Model) Dismiss() {
	if m.onCancel != nil {
		m.onCancel()
	}
	m.close()
}

// close invokes the host close seam. confirm/Dismiss run on the loop goroutine
// (key/mouse dispatch, or the root calling in directly), and so does the root's
// slot teardown, so the call is a plain synchronous one.
func (m *Model) close() {
	if m.onClose != nil {
		m.onClose()
	}
}

// Init satisfies component.Component. Dialog has no startup work.
func (m *Model) Init() {}

// GetKeybinds returns the dialog's bindings: alwaysHandle plus either the
// focused or unfocused group depending on whether the input currently holds
// focus. The list is flat so callers (e.g. the help overlay) can render every
// applicable binding.
func (m *Model) GetKeybinds() []keys.Binding {
	b := append([]keys.Binding{}, m.kh.alwaysHandle.GetKeybinds()...)
	if m.input != nil && m.input.Focused() {
		b = append(b, m.kh.focused.GetKeybinds()...)
	} else {
		b = append(b, m.kh.unfocused.GetKeybinds()...)
	}
	return b
}

// HandleKey dispatches a key event using the dialog's precedence:
// alwaysHandle → (when focused) focused group then input feed → unfocused.
//
// When the input is focused and no action matches, the key is fed to m.input
// via HandleKey (the "typing in dialogs" path); omitting it would silently drop
// characters.
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if m.kh.alwaysHandle.Run(ev) {
		return component.KeyHandled
	}

	if m.input != nil && m.input.Focused() {
		if m.kh.focused.Run(ev) {
			return component.KeyHandled
		}
		m.input.HandleKey(ev)
		return component.KeyHandled
	}

	if m.kh.unfocused.Run(ev) {
		return component.KeyHandled
	}
	return component.KeyIgnored
}

// OnPaste inserts bracketed-paste content into the dialog's input (when one is
// present). Mirrors the previous Update paste arm. The root calls it directly
// when the dialog is the active overlay.
func (m *Model) OnPaste(ev uv.Event) {
	if p, ok := ev.(uv.PasteEvent); ok && m.input != nil {
		m.input.InsertText(p.Content)
	}
}

// OnMouseClick implements component.MouseTarget. A click on the input row focuses
// the input and places the caret at the clicked offset; a click on a button
// selects it (when not already selected) or confirms it (when already selected).
// Other clicks inside the box are consumed by the modal (the root dismisses on
// clicks OUTSIDE the box). It mirrors Draw's geometry: inner = Pad(drawRect,
// 2,3,2,3); the input draws at left origin inner.X and the button strip is the
// last content line, centered within inner.
func (m *Model) OnMouseClick(x, y int, _ uv.MouseButton) bool {
	inner := uicanvas.Pad(m.drawRect, 2, 3, 2, 3)
	if inner.W < 1 || inner.H < 1 {
		return false
	}
	lines, inputRow, _, _ := m.sections()
	if m.input != nil && inputRow >= 0 && y == inner.Y+inputRow {
		if !m.input.Focused() {
			m.input.Focus()
		}
		// RuneIndexAtViewportX wants x relative to the input's drawn origin (inner.X).
		m.input.SetCursor(m.input.RuneIndexAtViewportX(x - inner.X))
		return true
	}
	if len(m.Buttons) > 0 && y == inner.Y+len(lines)-1 {
		if idx := m.strip(inner).HitTest(x); idx >= 0 {
			if idx == m.selectedButton {
				m.confirm()
			} else {
				m.selectedButton = idx
			}
			return true
		}
	}
	return false
}

// OnMousePaste implements component.MousePasteTarget: a middle-click on the
// dialog's input row focuses it, places the caret at the clicked offset, and
// inserts the clipboard content there. Clicks on any other row are ignored
// (the dialog has no other text input). Mirrors OnMouseClick's input-row
// geometry. Returns true when the content was inserted.
func (m *Model) OnMousePaste(x, y int, content string) bool {
	inner := uicanvas.Pad(m.drawRect, 2, 3, 2, 3)
	if inner.W < 1 || inner.H < 1 || m.input == nil {
		return false
	}
	_, inputRow, _, _ := m.sections()
	if inputRow < 0 || y != inner.Y+inputRow {
		return false
	}
	if !m.input.Focused() {
		m.input.Focus()
	}
	m.input.SetCursor(m.input.RuneIndexAtViewportX(x - inner.X))
	m.input.InsertText(content)
	return true
}

// OnMouseHover implements component.MouseHoverTarget: moving the pointer over a
// button moves the selection onto it (so a subsequent click confirms), so
// keyboard nav follows the pointer. Hovering any non-button cell leaves the
// current selection untouched — a stray pointer drift must not wreck the keyboard
// state. Mirrors OnMouseClick's button-row geometry. Returns true when the
// selection moved (so callers may gate a redraw to actual changes); the
// move-and-report rule itself lives in buttonstrip.Strip.Hover.
func (m *Model) OnMouseHover(x, y int) bool {
	inner := uicanvas.Pad(m.drawRect, 2, 3, 2, 3)
	if inner.W < 1 || inner.H < 1 || len(m.Buttons) == 0 {
		return false
	}
	lines, _, _, _ := m.sections()
	if y != inner.Y+len(lines)-1 {
		return false
	}
	return m.strip(inner).Hover(x, &m.selectedButton)
}

// ClearMouseHover is a no-op because hover moves the keyboard button selection;
// the dialog has no separate pointer-only highlight to clear.
func (*Model) ClearMouseHover() bool { return false }

// buttonLabels returns the button labels in order, for the shared strip helper.
func (m *Model) buttonLabels() []string {
	labels := make([]string, len(m.Buttons))
	for i, b := range m.Buttons {
		labels[i] = b.Label
	}
	return labels
}

// strip builds the shared button strip for the dialog's padded inner rect: it is
// centred within the inner rect and clipped at its right edge.
func (m *Model) strip(inner component.Rect) buttonstrip.Strip {
	return buttonstrip.Strip{Labels: m.buttonLabels(), X: inner.X, MaxX: inner.X + inner.W}
}

// sections builds the dialog's content lines top-to-bottom. The input and
// buttons rows are emitted as empty placeholder strings at known indices: Draw
// fills them with the live input/button strip. Groups (message, input, buttons)
// are separated by a SINGLE blank line, and absent groups (e.g. an empty
// Message) add no rows — so a titled input dialog renders compactly instead of
// with stacked empty lines. The layout, by combination, is:
//
//	[message lines...]     when Message != ""
//	["", input("")]        when input != nil
//	[errorText]            when errorText != ""
//	["", buttons("")]      when len(Buttons) > 0
//
// The Title is NOT a content line: it is painted onto the top border in Draw
// (like the filter menu), so it never consumes an inner row. Its width is still
// folded into contentW so the box stays wide enough to show the bordered title.
//
// It returns the assembled lines, the index of the input row (-1 when there is
// no input), the index of the error row (-1 when errorText is empty), and the
// content width (the max display width across all lines, the title, the button
// row, and inputDesiredW when an input is present).
func (m *Model) sections() (lines []string, inputRow, errRow, contentW int) {
	inputRow = -1
	errRow = -1

	// gap appends a single blank separator before the next group, but only when
	// a row already precedes it (no leading blank line).
	gap := func() {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
	}

	if m.Message != "" {
		lines = append(lines, strings.Split(m.Message, "\n")...)
	}

	if m.input != nil {
		gap()
		inputRow = len(lines)
		lines = append(lines, "")
	}

	if m.errorText != "" {
		errRow = len(lines)
		lines = append(lines, m.errorText)
	}

	if len(m.Buttons) > 0 {
		gap()
		lines = append(lines, "")
	}

	contentW = 0
	for _, line := range lines {
		if w := uniseg.StringWidth(line); w > contentW {
			contentW = w
		}
	}
	if bw := buttonstrip.Width(m.buttonLabels()); bw > contentW {
		contentW = bw
	}
	if m.input != nil && inputDesiredW > contentW {
		contentW = inputDesiredW
	}
	// The title sits on the top border (drawn in Draw): reserve enough inner
	// width that " Title " fits between the rounded corners.
	if tw := uniseg.StringWidth(m.Title); tw > contentW {
		contentW = tw
	}
	return lines, inputRow, errRow, contentW
}

// naturalSize returns the dialog's full box size (content + rounded border +
// padding(1,2)): width = contentW + 2 (border) + 4 (horiz padding); height =
// len(lines) + 2 (border) + 2 (vert padding). sections() is cheap (1-7 short
// lines), so it is recomputed each call rather than memoised.
func (m *Model) naturalSize() (w, h int) {
	lines, _, _, contentW := m.sections()
	w = contentW + 2 + 4
	h = len(lines) + 2 + 2
	return w, h
}

// PreferredSize implements component.Sizer. It returns the dialog's natural
// width and height, clamped to maxW/maxH. Parents (the root ui.Model) call this
// before assigning a rect so the dialog can be centered.
func (m *Model) PreferredSize(maxW, maxH int) (w, h int) {
	w, h = m.naturalSize()
	return min(w, maxW), min(h, maxH)
}

// SetRect stores the assigned rect. Rendering happens directly in Draw.
func (m *Model) SetRect(r component.Rect) {
	m.drawRect = r
}

// Draw renders the dialog directly onto the screen: a rounded box at drawRect,
// then each content line centered within the padded inner rect. The input row
// is drawn by uiinput (which renders its own reverse-video cursor cell) and the
// buttons row by the shared buttonstrip. Draw returns nil — the dialog owns no
// terminal cursor (the focused input's cursor is a reverse-video cell).
func (m *Model) Draw(s component.Screen) *component.Cursor {
	r := m.drawRect
	if r.W < 1 || r.H < 1 {
		return nil
	}
	uicanvas.DrawBox(s, r, uicanvas.RoundedSet, uv.Style{})
	// Title drawn onto the top border ("╭ Title ──╮"), like the filter menu, so
	// it labels the dialog without consuming an inner content row. Clipped before
	// the top-right corner.
	if m.Title != "" {
		uicanvas.PlaceText(s, r.X+2, r.Y, " "+m.Title+" ", uv.Style{Attrs: uv.AttrBold}, r.X+r.W-1)
	}

	// Inset by border (1) + padding (vert 1, horiz 2) on each side.
	inner := uicanvas.Pad(r, 2, 3, 2, 3)
	if inner.W < 1 || inner.H < 1 {
		return nil
	}

	lines, inputRow, errRow, _ := m.sections()
	buttonsRow := -1
	if len(m.Buttons) > 0 {
		buttonsRow = len(lines) - 1
	}

	for y, line := range lines {
		row := inner.Y + y
		if row < inner.Y || row >= inner.Y+inner.H {
			continue
		}
		m.drawRow(s, inner, y, row, line, inputRow, errRow, buttonsRow)
	}
	return nil
}

// drawRow draws one content row at screen row `row` (inner index `y`): the
// input field, the error row, the button strip, a hyperlink line, or a centered
// plain line, according to which placeholder index `y` matches. (The title is
// not a content row — Draw paints it on the top border.)
func (m *Model) drawRow(s component.Screen, inner component.Rect, y, row int, line string, inputRow, errRow, buttonsRow int) {
	switch {
	case m.input != nil && y == inputRow:
		m.input.SetWidth(inner.W)
		m.input.Draw(s, component.Rect{X: inner.X, Y: row, W: inner.W, H: 1})
	case y == errRow:
		x := inner.X + uicanvas.CenterX(inner.W, uniseg.StringWidth(line))
		uicanvas.PlaceText(s, x, row, line, uv.Style{Fg: ansi.Red}, inner.X+inner.W)
	case y == buttonsRow:
		m.strip(inner).Draw(s, row, m.selectedButton)
	case m.linkURL != "" && line == m.linkURL:
		x := inner.X + uicanvas.CenterX(inner.W, uniseg.StringWidth(line))
		uicanvas.PlaceLink(s, x, row, line, m.linkStyle(), uv.Link{URL: line}, inner.X+inner.W)
	default:
		x := inner.X + uicanvas.CenterX(inner.W, uniseg.StringWidth(line))
		uicanvas.PlaceText(s, x, row, line, uv.Style{}, inner.X+inner.W)
	}
}

// linkStyle is the style for a hyperlink line: underlined, and colored with the
// configured UrlFg when it is set (mirroring how a terminal renders a link).
func (m *Model) linkStyle() uv.Style {
	style := uv.Style{Underline: uv.UnderlineSingle}
	if fg := m.bundle.Config.Style.UrlFg; fg.IsSet() {
		style.Fg = fg.Color
	}
	return style
}
