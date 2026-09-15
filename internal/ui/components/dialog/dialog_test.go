package dialog

import (
	"errors"
	"strings"
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	btnLabelCancel = "Cancel"
	testTitle      = "Test"
)

func newTestInput(value string) *uiinput.Input {
	in := uiinput.New()
	in.SetValue(value)
	return in
}

// closeRec stands in for the root ui.Model as the host close seam, recording
// every close the dialog performs. confirm/Dismiss call the seam synchronously,
// so a close lands during HandleKey.
type closeRec struct{ calls int }

func (c *closeRec) onClose()     { c.calls++ }
func (c *closeRec) closed() bool { return c.calls > 0 }

func TestUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		buttons       func(called *bool) []msgs.DialogButton
		input         *uiinput.Input
		blurInput     bool
		startSelected int
		key           uv.KeyPressEvent
		wantSelected  int
		wantClosed    bool
		wantButtonCmd bool
	}{
		{
			name:          "tab moves selection forward",
			buttons:       func(*bool) []msgs.DialogButton { return []msgs.DialogButton{{Label: "A"}, {Label: "B"}} },
			input:         nil,
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: uv.KeyTab},
			wantSelected:  1,
		},
		{
			// Down/Up are content<->button-row nav; with no input row they are
			// consumed as a no-op (button cycling stays on tab/left/right).
			name:          "no-input dialog: down does not cycle buttons",
			buttons:       func(*bool) []msgs.DialogButton { return []msgs.DialogButton{{Label: "A"}, {Label: "B"}} },
			input:         nil,
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: uv.KeyDown},
			wantSelected:  0,
		},
		{
			name:          "shift+tab moves selection backward",
			buttons:       func(*bool) []msgs.DialogButton { return []msgs.DialogButton{{Label: "A"}, {Label: "B"}} },
			input:         nil,
			startSelected: 1,
			key:           uv.KeyPressEvent{Code: uv.KeyTab, Mod: uv.ModShift},
			wantSelected:  0,
		},
		{
			name:          "right arrow moves selection forward",
			buttons:       func(*bool) []msgs.DialogButton { return []msgs.DialogButton{{Label: "A"}, {Label: "B"}} },
			input:         nil,
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: uv.KeyRight},
			wantSelected:  1,
		},
		{
			name:          "left arrow moves selection backward",
			buttons:       func(*bool) []msgs.DialogButton { return []msgs.DialogButton{{Label: "A"}, {Label: "B"}} },
			input:         nil,
			startSelected: 1,
			key:           uv.KeyPressEvent{Code: uv.KeyLeft},
			wantSelected:  0,
		},
		{
			name: "q dismisses dialog",
			buttons: func(*bool) []msgs.DialogButton {
				return []msgs.DialogButton{{Label: "OK", Cmd: func(_ string) error { return nil }}}
			},
			input:         nil,
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: 'q', Text: "q"},
			wantClosed:    true,
			wantButtonCmd: false,
		},
		{
			name:          "next wraps to first button",
			buttons:       func(*bool) []msgs.DialogButton { return []msgs.DialogButton{{Label: "A"}, {Label: "B"}} },
			input:         nil,
			startSelected: 1,
			key:           uv.KeyPressEvent{Code: uv.KeyTab},
			wantSelected:  0,
		},
		{
			name:          "prev wraps to last button",
			buttons:       func(*bool) []msgs.DialogButton { return []msgs.DialogButton{{Label: "A"}, {Label: "B"}} },
			input:         nil,
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: uv.KeyTab, Mod: uv.ModShift},
			wantSelected:  1,
		},
		{
			name:          "enter closes dialog",
			buttons:       func(*bool) []msgs.DialogButton { return []msgs.DialogButton{{Label: "OK"}} },
			input:         nil,
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: uv.KeyEnter},
			wantClosed:    true,
		},
		{
			name:          "enter invokes button cmd",
			buttons:       dummyButtons,
			input:         nil,
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: uv.KeyEnter},
			wantClosed:    true,
			wantButtonCmd: true,
		},
		{
			name:          "escape closes dialog without invoking button cmd",
			buttons:       dummyButtons,
			input:         nil,
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: uv.KeyEscape},
			wantClosed:    true,
			wantButtonCmd: false,
		},
		{
			name:          "unrelated key is swallowed",
			buttons:       func(*bool) []msgs.DialogButton { return []msgs.DialogButton{{Label: "A"}, {Label: "B"}} },
			input:         nil,
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: 'x', Text: "x"},
			wantSelected:  0,
		},
		{
			name: "input focused: enter confirms with Buttons[0] and input value",
			buttons: func(called *bool) []msgs.DialogButton {
				return []msgs.DialogButton{
					{Label: "OK", Cmd: markCalled(called)},
					{Label: btnLabelCancel},
				}
			},
			input:         newTestInput("myfile.txt"),
			startSelected: 1, // should be overridden to 0
			key:           uv.KeyPressEvent{Code: uv.KeyEnter},
			wantClosed:    true,
			wantButtonCmd: true,
		},
		{
			name:          "input focused: esc closes the dialog (unconditional)",
			buttons:       dummyButtons,
			input:         newTestInput("myfile.txt"),
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: uv.KeyEscape},
			wantClosed:    true,
			wantButtonCmd: false,
		},
		{
			name:          "input blurred: esc closes dialog",
			buttons:       dummyButtons,
			input:         newTestInput("myfile.txt"),
			blurInput:     true,
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: uv.KeyEscape},
			wantClosed:    true,
			wantButtonCmd: false,
		},
		{
			name: "input blurred: enter confirms with selectedButton",
			buttons: func(called *bool) []msgs.DialogButton {
				return []msgs.DialogButton{
					{Label: "OK"},
					{Label: "Do it", Cmd: markCalled(called)},
				}
			},
			input:         newTestInput("myfile.txt"),
			blurInput:     true,
			startSelected: 1,
			key:           uv.KeyPressEvent{Code: uv.KeyEnter},
			wantSelected:  1,
			wantClosed:    true,
			wantButtonCmd: true,
		},
		{
			name:          "input focused: tab forwards to input not buttons",
			buttons:       func(*bool) []msgs.DialogButton { return []msgs.DialogButton{{Label: "A"}, {Label: "B"}} },
			input:         newTestInput("myfile.txt"),
			startSelected: 0,
			key:           uv.KeyPressEvent{Code: uv.KeyTab},
			wantSelected:  0, // button selection should NOT change
			wantClosed:    false,
			wantButtonCmd: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buttonCalled bool
			cr := &closeRec{}
			m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
				Title:   testTitle,
				Message: "test message",
				Buttons: tt.buttons(&buttonCalled),
				Input:   tt.input,
			})
			m.SetOnClose(cr.onClose)
			m.selectedButton = tt.startSelected
			if tt.blurInput && m.input != nil {
				m.input.Blur()
			}

			// Keys now dispatch via HandleKey (R2 cutover); the dialog Model is
			// a pointer receiver, so m is mutated in place. confirm/Dismiss call the
			// host close seam synchronously, so a close lands here.
			m.HandleKey(tt.key)

			assert.Equal(t, tt.wantSelected, m.selectedButton, "selectedButton")
			assert.Equal(t, tt.wantClosed, cr.closed(), "dialog closed")
			assert.Equal(t, tt.wantButtonCmd, buttonCalled, "button cmd invoked")
		})
	}
}

// markCalled returns a button Cmd that records its invocation by setting
// *called. The Cmd performs its effect directly; no return value.
func markCalled(called *bool) func(string) error {
	return func(string) error {
		*called = true
		return nil
	}
}

// dummyButtons builds a single OK button whose Cmd records invocation.
func dummyButtons(called *bool) []msgs.DialogButton {
	return []msgs.DialogButton{{Label: "OK", Cmd: markCalled(called)}}
}

func TestConfirmPassesInputValue(t *testing.T) {
	t.Parallel()
	// confirm now invokes Buttons[0].Cmd for its effect (R5a A6) and discards
	// the returned cmd, so the fixture captures the passed value directly rather
	// than via batch execution.
	var captured string
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   testTitle,
		Message: "enter value:",
		Input:   newTestInput("myfile.txt"),
		Buttons: []msgs.DialogButton{
			{Label: "OK", Cmd: func(s string) error {
				captured = s
				return nil
			}},
			{Label: btnLabelCancel},
		},
	})

	// Confirm with focused input — should pass input.Value() to Buttons[0].Cmd.
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	assert.Equal(t, "myfile.txt", captured, "Cmd should receive the input value")
}

func TestPreferredSize_ClampsToMax(t *testing.T) {
	t.Parallel()

	d := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   "T",
		Message: strings.Repeat("a very long message ", 20),
		Buttons: []msgs.DialogButton{{Label: "OK"}},
	})
	w, h := d.PreferredSize(20, 5)
	if w > 20 || h > 5 {
		t.Fatalf("expected clamp to 20x5, got %dx%d", w, h)
	}
	if w < 1 || h < 1 {
		t.Fatalf("expected non-zero size, got %dx%d", w, h)
	}
}

// drawDialog assigns rect, draws onto a fresh canvas large enough to hold the
// rect's far corner, and returns the canvas for per-cell assertions.
func drawDialog(t *testing.T, m *Model, rect component.Rect) uv.ScreenBuffer {
	t.Helper()
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.X+rect.W, rect.Y+rect.H)
	cursor := m.Draw(canvas)
	require.Nil(t, cursor, "dialog Draw must return a nil cursor (uiinput renders its own cell)")
	return canvas
}

// content collects the visible glyphs of the canvas (whole rect) as a string
// for substring assertions.
func content(t *testing.T, canvas uv.ScreenBuffer, rect component.Rect) string {
	t.Helper()
	var sb strings.Builder
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			if c := canvas.CellAt(x, y); c != nil && c.Content != "" {
				sb.WriteString(c.Content)
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestDraw_BoxAndContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		title    string
		message  string
		buttons  []msgs.DialogButton
		input    *uiinput.Input
		contains []string
	}{
		{
			name:     "renders title and message",
			title:    "Error",
			message:  "something went wrong",
			buttons:  []msgs.DialogButton{{Label: "OK"}},
			input:    nil,
			contains: []string{"Error", "something went wrong", "OK"},
		},
		{
			name:     "renders without title",
			title:    "",
			message:  "just a message",
			buttons:  []msgs.DialogButton{{Label: "OK"}},
			input:    nil,
			contains: []string{"just a message", "OK"},
		},
		{
			name:     "renders multiple buttons",
			title:    "Confirm",
			message:  "are you sure?",
			buttons:  []msgs.DialogButton{{Label: "Yes"}, {Label: "No"}},
			input:    nil,
			contains: []string{"Yes", "No"},
		},
		{
			name:     "renders input with value",
			title:    "Export",
			message:  "Enter filename:",
			buttons:  []msgs.DialogButton{{Label: "OK"}, {Label: btnLabelCancel}},
			input:    newTestInput("test.lognav"),
			contains: []string{"Export", "Enter filename:", "test.lognav", "OK", btnLabelCancel},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
				Title:   tt.title,
				Message: tt.message,
				Buttons: tt.buttons,
				Input:   tt.input,
			})
			w, h := m.PreferredSize(80, 20)
			rect := component.Rect{X: 4, Y: 2, W: w, H: h}
			canvas := drawDialog(t, m, rect)

			// Box corners (rounded set) at the rect perimeter.
			assert.Equal(t, "╭", cellContent(canvas, rect.X, rect.Y), "top-left corner")
			assert.Equal(t, "╮", cellContent(canvas, rect.X+rect.W-1, rect.Y), "top-right corner")
			assert.Equal(t, "╰", cellContent(canvas, rect.X, rect.Y+rect.H-1), "bottom-left corner")
			assert.Equal(t, "╯", cellContent(canvas, rect.X+rect.W-1, rect.Y+rect.H-1), "bottom-right corner")

			got := content(t, canvas, rect)
			for _, s := range tt.contains {
				assert.Contains(t, got, s, "drawn content missing %q\ngot:\n%s", s, got)
			}
		})
	}
}

// cellContent returns the content of the cell at (x, y), or "" if absent.
func cellContent(canvas uv.ScreenBuffer, x, y int) string {
	if c := canvas.CellAt(x, y); c != nil {
		return c.Content
	}
	return ""
}

// TestDraw_LinkURL_RendersColoredUnderlinedHyperlink asserts that the Message
// line equal to LinkURL renders as a real hyperlink: UrlFg-colored, underlined,
// and carrying an OSC8 link on every glyph cell — without any escape bytes in
// the Message itself (the passcode-dialog regression fix).
func TestDraw_LinkURL_RendersColoredUnderlinedHyperlink(t *testing.T) {
	t.Parallel()
	const url = "https://example.invalid/passcode"
	bundle := depstest.NewTest(t)
	m := New(bundle, msgs.ShowDialogMsg{
		Title:   "Passcode Required",
		Message: "Get passcode from:\n" + url,
		LinkURL: url,
		Buttons: []msgs.DialogButton{{Label: "OK"}},
	})
	w, h := m.PreferredSize(80, 20)
	rect := component.Rect{X: 4, Y: 2, W: w, H: h}
	canvas := drawDialog(t, m, rect)

	var linkCell *uv.Cell
	for y := rect.Y; y < rect.Y+rect.H && linkCell == nil; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			if c := canvas.CellAt(x, y); c != nil && c.Link.URL == url {
				linkCell = c
				break
			}
		}
	}
	require.NotNil(t, linkCell, "URL line must render cells carrying the OSC8 hyperlink")
	assert.Equal(t, uv.UnderlineSingle, linkCell.Style.Underline, "hyperlink line must be underlined")
	if fg := bundle.Config.Style.UrlFg; fg.IsSet() {
		assert.Equal(t, fg.Color, linkCell.Style.Fg, "hyperlink line uses the configured UrlFg color")
	}
	assert.Contains(t, content(t, canvas, rect), url, "the URL text must render intact")
}

func TestDraw_SelectedButtonReversed(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   "Confirm",
		Message: "pick one",
		Buttons: []msgs.DialogButton{{Label: "Yes"}, {Label: "No"}},
	})
	m.selectedButton = 0
	w, h := m.PreferredSize(80, 20)
	rect := component.Rect{X: 0, Y: 0, W: w, H: h}
	canvas := drawDialog(t, m, rect)

	// Scan the whole rect for a cell whose content is "Y" (start of "Yes")
	// rendered with reverse video — that confirms the selected button is styled.
	foundReversed := false
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			c := canvas.CellAt(x, y)
			if c != nil && c.Content == "Y" && c.Style.Attrs&uv.AttrReverse != 0 {
				foundReversed = true
			}
		}
	}
	assert.True(t, foundReversed, "selected button label should be drawn with reverse video")
}

func TestDraw_MaskedPasscodeInput(t *testing.T) {
	t.Parallel()
	in := uiinput.New()
	in.SetMasked(true)
	in.SetMaxLen(10)
	in.SetValue("1234")

	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   "Passcode Required",
		Message: "Enter passcode:",
		Input:   in,
		Buttons: []msgs.DialogButton{{Label: "OK"}, {Label: btnLabelCancel}},
	})
	w, h := m.PreferredSize(80, 20)
	rect := component.Rect{X: 0, Y: 0, W: w, H: h}
	canvas := drawDialog(t, m, rect)

	// The masked value must render as bullet glyphs, never the digits.
	got := content(t, canvas, rect)
	assert.Contains(t, got, "•", "masked input should render bullet glyphs")
	assert.NotContains(t, got, "1234", "masked input must not leak the raw value")

	// Count bullets: 4 chars masked.
	bullets := 0
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			if c := canvas.CellAt(x, y); c != nil && c.Content == "•" {
				bullets++
			}
		}
	}
	assert.Equal(t, 4, bullets, "expected one bullet per masked rune")
}

// newTestDialog creates a dialog without an input for HandleKey tests, wired to
// a closeRec standing in for the root's close seam.
func newTestDialog(t *testing.T, buttons []msgs.DialogButton) (*Model, *closeRec) {
	t.Helper()
	cr := &closeRec{}
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   testTitle,
		Message: "test message",
		Buttons: buttons,
	})
	m.SetOnClose(cr.onClose)
	return m, cr
}

// newTestDialogWithInput creates a dialog with a focused input, wired to a
// closeRec standing in for the root's close seam.
func newTestDialogWithInput(t *testing.T, buttons []msgs.DialogButton, value string) (*Model, *closeRec) {
	t.Helper()
	in := uiinput.New()
	in.SetValue(value)
	cr := &closeRec{}
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   testTitle,
		Message: "test message",
		Buttons: buttons,
		Input:   in,
	})
	m.SetOnClose(cr.onClose)
	return m, cr
}

func TestHandleKey_AlwaysHandle_EscapeCloses(t *testing.T) {
	t.Parallel()
	m, cr := newTestDialog(t, []msgs.DialogButton{{Label: "OK"}})
	result := m.HandleKey(keystest.PressKeyUV(t, uv.KeyEscape))

	assert.Equal(t, component.KeyHandled, result, "escape should be handled")
	assert.True(t, cr.closed(), "escape should close the dialog")
}

func TestHandleKey_AlwaysHandle_EnterConfirms(t *testing.T) {
	t.Parallel()
	m, cr := newTestDialog(t, []msgs.DialogButton{{Label: "OK"}})
	result := m.HandleKey(keystest.PressKeyUV(t, uv.KeyEnter))

	assert.Equal(t, component.KeyHandled, result, "enter should be handled")
	assert.True(t, cr.closed(), "enter should confirm/close the dialog")
}

func TestHandleKey_Unfocused_TabMovesButton(t *testing.T) {
	t.Parallel()
	m, _ := newTestDialog(t, []msgs.DialogButton{{Label: "A"}, {Label: "B"}})
	m.selectedButton = 0

	result := m.HandleKey(keystest.PressKeyUV(t, uv.KeyTab))

	assert.Equal(t, component.KeyHandled, result, "tab should be handled")
	assert.Equal(t, 1, m.selectedButton, "tab should advance selection")
}

func TestHandleKey_Unfocused_UnboundKey_ReturnsIgnored(t *testing.T) {
	t.Parallel()
	m, _ := newTestDialog(t, []msgs.DialogButton{{Label: "OK"}})
	result := m.HandleKey(keystest.PressRuneUV(t, 'z'))

	assert.Equal(t, component.KeyIgnored, result, "unbound key should be ignored")
}

func TestHandleKey_FocusedInput_CharTyped(t *testing.T) {
	t.Parallel()
	m, _ := newTestDialogWithInput(t, []msgs.DialogButton{{Label: "OK"}}, "")
	// Verify the input is focused as a precondition.
	require.True(t, m.input.Focused(), "input must be focused for this test branch")
	// Type a character — should be consumed by the focused input.
	result := m.HandleKey(keystest.PressRuneUV(t, 'a'))

	assert.Equal(t, component.KeyHandled, result, "char key must be handled when input is focused")
	assert.Equal(t, "a", m.input.Value(), "typed char should be inserted into the input")
}

func TestHandleKey_FocusedInput_ClearAction(t *testing.T) {
	t.Parallel()
	m, _ := newTestDialogWithInput(t, []msgs.DialogButton{{Label: "OK"}}, "some text")
	require.True(t, m.input.Focused(), "input must be focused for this test branch")

	// ctrl+c is the default Clear binding — fires the focused.GetAction path.
	result := m.HandleKey(keystest.PressCtrlUV(t, 'c'))

	assert.Equal(t, component.KeyHandled, result, "clear action should be handled")
	assert.Empty(t, m.input.Value(), "clear should empty the input")
}

func TestConfirm_CmdErrorKeepsOpenAndSetsErrorText(t *testing.T) {
	t.Parallel()
	cr := &closeRec{}
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   testTitle,
		Message: "edit value:",
		Input:   newTestInput("bad("),
		Buttons: []msgs.DialogButton{
			{Label: "Apply", Cmd: func(string) error { return errors.New("syntax error") }},
			{Label: btnLabelCancel},
		},
	})
	m.SetOnClose(cr.onClose)
	m.selectedButton = 0

	// Enter on the focused input fires Apply (selected forced to primary).
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})

	assert.False(t, cr.closed(), "a Cmd error must suppress the dialog close")
	assert.Equal(t, "syntax error", m.errorText, "the Cmd error message is stored")
}

func TestConfirm_CmdNilClosesAndClearsError(t *testing.T) {
	t.Parallel()
	cr := &closeRec{}
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   testTitle,
		Message: "edit value:",
		Input:   newTestInput(".data"),
		Buttons: []msgs.DialogButton{
			{Label: "Apply", Cmd: func(string) error { return nil }},
			{Label: btnLabelCancel},
		},
	})
	m.SetOnClose(cr.onClose)
	m.errorText = "stale"
	m.selectedButton = 0

	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})

	assert.True(t, cr.closed(), "a nil Cmd result closes the dialog")
}

func TestDraw_ErrorRow(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   "JQ",
		Message: "Edit expression:",
		Input:   newTestInput("bad("),
		Buttons: []msgs.DialogButton{{Label: "Apply"}, {Label: btnLabelCancel}},
	})
	m.errorText = "unexpected token"

	w, h := m.PreferredSize(80, 20)
	rect := component.Rect{X: 0, Y: 0, W: w, H: h}
	canvas := drawDialog(t, m, rect)

	got := content(t, canvas, rect)
	assert.Contains(t, got, "unexpected token", "error row text must render")

	// At least one cell of the error text is red.
	foundRed := false
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			c := canvas.CellAt(x, y)
			if c != nil && c.Content == "u" && c.Style.Fg == ansi.Red {
				foundRed = true
			}
		}
	}
	assert.True(t, foundRed, "error row must be red")
}

func TestSections_NoErrorRowWhenEmpty(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   "JQ",
		Message: "Edit expression:",
		Input:   newTestInput(".data"),
		Buttons: []msgs.DialogButton{{Label: "Apply"}},
	})
	linesNoErr, _, _, _ := m.sections()
	m.errorText = "boom"
	linesWithErr, _, _, _ := m.sections()
	assert.Len(t, linesWithErr, len(linesNoErr)+1,
		"errorText must add exactly one row")
}

func TestEnter_FocusedFiresPrimary_BlurredFiresSelected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		blur      bool
		selected  int
		wantPrim  bool // expect Buttons[0].Cmd called
		wantOther bool // expect Buttons[1].Cmd called
	}{
		{"focused enter fires primary regardless of selected", false, 1, true, false},
		{"blurred enter fires the selected button", true, 1, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var prim, other bool
			cr := &closeRec{}
			m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
				Title:   testTitle,
				Message: "msg",
				Input:   newTestInput("x"),
				Buttons: []msgs.DialogButton{
					{Label: "Apply", Cmd: func(string) error { prim = true; return nil }},
					{Label: "Other", Cmd: func(string) error { other = true; return nil }},
				},
			})
			m.SetOnClose(cr.onClose)
			m.selectedButton = tt.selected
			if tt.blur {
				m.input.Blur()
			}
			m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
			assert.Equal(t, tt.wantPrim, prim, "primary fired")
			assert.Equal(t, tt.wantOther, other, "other fired")
		})
	}
}

func TestNav_DownBlursToButtons_UpRefocusesAndResets(t *testing.T) {
	t.Parallel()
	cr := &closeRec{}
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   testTitle,
		Message: "msg",
		Input:   newTestInput("x"),
		Buttons: []msgs.DialogButton{{Label: "Apply"}, {Label: "Clear"}, {Label: btnLabelCancel}},
	})
	m.SetOnClose(cr.onClose)
	require.True(t, m.input.Focused(), "starts focused on content")

	// Down: blur input, select button row at primary.
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyDown})
	assert.False(t, m.input.Focused(), "Down blurs the input")
	assert.Equal(t, 0, m.selectedButton, "Down lands on the primary button")
	assert.False(t, cr.closed(), "Down must not close")

	// Right then Right: move to Cancel (index 2).
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyRight})
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyRight})
	assert.Equal(t, 2, m.selectedButton, "Right moves button selection")

	// Up: re-focus content AND reset selection to primary.
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyUp})
	assert.True(t, m.input.Focused(), "Up re-focuses the input")
	assert.Equal(t, 0, m.selectedButton, "Up resets selectedButton to primary")
}

func TestNav_DownUp_NotFedToInput(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   testTitle,
		Message: "msg",
		Input:   newTestInput("abc"),
		Buttons: []msgs.DialogButton{{Label: "Apply"}},
	})
	before := m.input.Value()
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyDown})
	assert.Equal(t, before, m.input.Value(), "Down must not edit the input value")
}

func TestDraw_PillChip_RendersBeforeInput(t *testing.T) {
	t.Parallel()
	// Title/message/value/buttons deliberately avoid the letter "S" so the
	// Contains("S") assertion below proves the CHIP rendered (not the title).
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:    "Find",
		Message:  "Enter term:",
		PillChip: "S",
		Input:    newTestInput("error"),
		Buttons:  []msgs.DialogButton{{Label: "Apply"}, {Label: btnLabelCancel}},
	})
	w, h := m.PreferredSize(80, 20)
	rect := component.Rect{X: 0, Y: 0, W: w, H: h}
	canvas := drawDialog(t, m, rect)

	got := content(t, canvas, rect)
	assert.Contains(t, got, "S", "the pill chip must render")
	assert.Contains(t, got, "error", "the input value must render after the chip")
}

func TestDraw_NoPillChip_PlainInput(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t), msgs.ShowDialogMsg{
		Title:   "Export",
		Message: "filename:",
		Input:   newTestInput("out.lognav"),
		Buttons: []msgs.DialogButton{{Label: "OK"}},
	})
	w, h := m.PreferredSize(80, 20)
	rect := component.Rect{X: 0, Y: 0, W: w, H: h}
	canvas := drawDialog(t, m, rect)
	assert.Contains(t, content(t, canvas, rect), "out.lognav")
}
