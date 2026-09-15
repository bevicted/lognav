package dialog

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialog_Dismiss_ClosesEvenWithFocusedInput(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	in := uiinput.New()
	cancelled := false
	cr := &closeRec{}
	m := New(bundle, msgs.ShowDialogMsg{
		Title:    "Save",
		Input:    in,
		OnCancel: func() { cancelled = true },
		Buttons:  []msgs.DialogButton{{Label: "OK"}, {Label: "Cancel"}},
	})
	m.SetOnClose(cr.onClose)
	require.True(t, in.Focused(), "New() focuses the input") // precondition

	m.Dismiss()

	assert.True(t, cancelled, "Dismiss must run onCancel")
	assert.True(t, cr.closed(), "Dismiss must close the dialog")
}

// sizedMouseDialog builds a dialog with a Title, Input, and the given buttons,
// places it at a known origin sized to its natural dimensions, and returns the
// model, its inner rect, the input/buttons row indices, and the absolute start
// column of the button strip — all computed the SAME way Draw does (see
// dialog.go Draw/drawRow/sections). Callers add per-button chunk widths
// (1 + StringWidth(label) + 1) onto buttonStripX to hit a specific button.
func sizedMouseDialog(t *testing.T, in *uiinput.Input, buttons []msgs.DialogButton) (m *Model, inner component.Rect, inputRow, buttonsRow, buttonStripX int) {
	t.Helper()
	bundle := depstest.NewTest(t)
	m = New(bundle, msgs.ShowDialogMsg{
		Title:   "Save snapshot",
		Input:   in,
		Buttons: buttons,
	})
	// Size the box to its natural dimensions at a known origin, exactly like the
	// root overlay path (PreferredSize then SetRect).
	w, h := m.PreferredSize(200, 50)
	m.SetRect(component.Rect{X: 5, Y: 2, W: w, H: h})

	// Mirror Draw: inner = Pad(drawRect, 2,3,2,3); buttons row = last content line.
	inner = uicanvas.Pad(m.drawRect, 2, 3, 2, 3)
	lines, ir, _, _ := m.sections()
	inputRow = ir
	buttonsRow = len(lines) - 1
	buttonStripX = m.strip(inner).StartX()
	return m, inner, inputRow, buttonsRow, buttonStripX
}

func TestDialog_OnMouseClick_NonSelectedButton_SelectsWithoutConfirm(t *testing.T) {
	t.Parallel()
	cr := &closeRec{}
	in := uiinput.New()
	buttons := []msgs.DialogButton{{Label: "OK"}, {Label: btnLabelCancel}}
	m, inner, _, buttonsRow, stripX := sizedMouseDialog(t, in, buttons)
	m.SetOnClose(cr.onClose)
	require.Equal(t, 0, m.selectedButton, "selection starts at OK") // precondition

	// "Cancel" is the second chunk: its start column is stripX + the OK chunk
	// width (1 + StringWidth("OK") + 1). Click a column inside the Cancel chunk.
	cancelX := stripX + (1 + uniseg.StringWidth("OK") + 1) + 1
	row := inner.Y + buttonsRow

	consumed := m.OnMouseClick(cancelX, row, uv.MouseLeft)

	assert.True(t, consumed, "click on a button must be consumed")
	assert.Equal(t, 1, m.selectedButton, "clicking Cancel selects it")
	assert.False(t, cr.closed(), "selecting a non-selected button must NOT confirm")
}

func TestDialog_OnMouseClick_SelectedButton_Confirms(t *testing.T) {
	t.Parallel()
	cr := &closeRec{}
	in := uiinput.New()
	in.SetValue("snap-name")
	var gotValue string
	called := false
	buttons := []msgs.DialogButton{
		{Label: "OK", Cmd: func(v string) error { called = true; gotValue = v; return nil }},
		{Label: btnLabelCancel},
	}
	m, inner, _, buttonsRow, stripX := sizedMouseDialog(t, in, buttons)
	m.SetOnClose(cr.onClose)
	require.Equal(t, 0, m.selectedButton, "selection starts at OK") // precondition

	// "OK" is the first chunk: click inside it (skip the leading pad space).
	okX := stripX + 1
	row := inner.Y + buttonsRow

	consumed := m.OnMouseClick(okX, row, uv.MouseLeft)

	assert.True(t, consumed, "click on the selected button must be consumed")
	assert.True(t, called, "clicking the already-selected button must run its Cmd")
	assert.Equal(t, "snap-name", gotValue, "Cmd receives the input value")
	assert.True(t, cr.closed(), "confirming must close the dialog")
}

func TestDialog_OnMouseClick_InputRow_FocusesAndPlacesCaret(t *testing.T) {
	t.Parallel()
	cr := &closeRec{}
	in := uiinput.New()
	in.SetValue("hello")
	buttons := []msgs.DialogButton{{Label: "OK"}, {Label: btnLabelCancel}}
	m, inner, inputRow, _, _ := sizedMouseDialog(t, in, buttons)
	m.SetOnClose(cr.onClose)
	// Mirror Draw's input sizing (drawRow calls SetWidth(inner.W)); without it the
	// unsized input scrolls its viewport to the cursor, breaking the x->index map.
	in.SetWidth(inner.W)
	in.SetCursor(0) // scroll back to the start so viewOffset is 0
	// New() focuses the input; blur it here so we observe Focus() taking effect.
	in.Blur()
	require.GreaterOrEqual(t, inputRow, 0, "dialog has an input row") // precondition
	require.False(t, in.Focused(), "input starts blurred")            // precondition

	// The input draws at left origin inner.X (Draw passes Rect{X: inner.X,...}).
	// Click absolute column inner.X+3 -> relative x 3 -> rune index 3 ("hel|lo").
	row := inner.Y + inputRow
	consumed := m.OnMouseClick(inner.X+3, row, uv.MouseLeft)

	assert.True(t, consumed, "click on the input row must be consumed")
	assert.True(t, in.Focused(), "clicking the input row focuses it")
	assert.Equal(t, 3, in.Position(), "caret placed at the clicked offset")
	assert.False(t, cr.closed(), "clicking the input must not confirm")
}

// TestDialog_OnMousePaste_InputRow_InsertsAtCaret verifies a middle-click on the
// input row focuses the input and inserts the clipboard content at the clicked
// caret offset.
func TestDialog_OnMousePaste_InputRow_InsertsAtCaret(t *testing.T) {
	t.Parallel()
	cr := &closeRec{}
	in := uiinput.New()
	in.SetValue("hello")
	buttons := []msgs.DialogButton{{Label: "OK"}, {Label: btnLabelCancel}}
	m, inner, inputRow, _, _ := sizedMouseDialog(t, in, buttons)
	m.SetOnClose(cr.onClose)
	in.SetWidth(inner.W)
	in.SetCursor(0)                                                   // viewOffset 0 so x->index maps cleanly
	in.Blur()                                                         // observe Focus() taking effect
	require.GreaterOrEqual(t, inputRow, 0, "dialog has an input row") // precondition

	// Middle-click absolute column inner.X+3 -> caret offset 3 ("hel|lo").
	row := inner.Y + inputRow
	ok := m.OnMousePaste(inner.X+3, row, "XX")

	assert.True(t, ok, "middle-click on the input row must insert and consume")
	assert.True(t, in.Focused(), "middle-click paste focuses the input")
	assert.Equal(t, "helXXlo", in.Value(), "content inserted at the clicked caret offset")
}

// TestDialog_OnMousePaste_NonInputRow_Ignored verifies a middle-click on a
// non-input row inserts nothing and is not consumed.
func TestDialog_OnMousePaste_NonInputRow_Ignored(t *testing.T) {
	t.Parallel()
	cr := &closeRec{}
	in := uiinput.New()
	in.SetValue("hello")
	buttons := []msgs.DialogButton{{Label: "OK"}, {Label: btnLabelCancel}}
	m, inner, _, _, _ := sizedMouseDialog(t, in, buttons)
	m.SetOnClose(cr.onClose)

	ok := m.OnMousePaste(inner.X+1, inner.Y+1, "XX") // gap row, not the input row

	assert.False(t, ok, "middle-click on a non-input row must be ignored")
	assert.Equal(t, "hello", in.Value(), "no insertion on a non-input row")
}

func TestDialog_OnMouseClick_BlankRow_ReturnsFalseNoChange(t *testing.T) {
	t.Parallel()
	cr := &closeRec{}
	in := uiinput.New()
	buttons := []msgs.DialogButton{{Label: "OK"}, {Label: btnLabelCancel}}
	m, inner, _, _, _ := sizedMouseDialog(t, in, buttons)
	m.SetOnClose(cr.onClose)

	// Row 1 of inner is the blank gap between input and buttons — neither the
	// input row nor the button strip (the title now lives on the top border).
	consumed := m.OnMouseClick(inner.X+1, inner.Y+1, uv.MouseLeft)

	assert.False(t, consumed, "click on a non-interactive row must not be consumed")
	assert.Equal(t, 0, m.selectedButton, "selection unchanged")
	assert.False(t, cr.closed(), "no confirm on a blank-row click")
}

// TestDialog_OnMouseHover_OverButton_MovesSelection verifies that moving the
// pointer over a button moves selectedButton onto it (hover follows the pointer,
// experimental) and reports a change.
func TestDialog_OnMouseHover_OverButton_MovesSelection(t *testing.T) {
	t.Parallel()
	in := uiinput.New()
	buttons := []msgs.DialogButton{{Label: "OK"}, {Label: btnLabelCancel}}
	m, inner, _, buttonsRow, stripX := sizedMouseDialog(t, in, buttons)
	require.Equal(t, 0, m.selectedButton, "selection starts at OK") // precondition

	// "Cancel" is the second chunk (start = stripX + the OK chunk width).
	cancelX := stripX + (1 + uniseg.StringWidth("OK") + 1) + 1
	row := inner.Y + buttonsRow

	changed := m.OnMouseHover(cancelX, row)

	assert.True(t, changed, "hovering a new button reports a change")
	assert.Equal(t, 1, m.selectedButton, "hover moves selection onto the hovered button")
}

// TestDialog_OnMouseHover_OffButtonRow_KeepsSelection verifies that hovering any
// non-button cell (e.g. the title row) leaves the current selection untouched and
// reports no change — so a stray pointer drift never wrecks the keyboard state.
func TestDialog_OnMouseHover_OffButtonRow_KeepsSelection(t *testing.T) {
	t.Parallel()
	in := uiinput.New()
	buttons := []msgs.DialogButton{{Label: "OK"}, {Label: btnLabelCancel}}
	m, inner, _, _, _ := sizedMouseDialog(t, in, buttons)
	require.Equal(t, 0, m.selectedButton) // precondition

	changed := m.OnMouseHover(inner.X+1, inner.Y) // input row, off the button strip

	assert.False(t, changed, "hovering off the button row reports no change")
	assert.Equal(t, 0, m.selectedButton, "hover off buttons leaves selection untouched")
}

// TestDialog_OnMouseHover_SameButton_NoChange verifies that hovering the
// already-selected button reports no change (gate to selection moves only).
func TestDialog_OnMouseHover_SameButton_NoChange(t *testing.T) {
	t.Parallel()
	in := uiinput.New()
	buttons := []msgs.DialogButton{{Label: "OK"}, {Label: btnLabelCancel}}
	m, inner, _, buttonsRow, stripX := sizedMouseDialog(t, in, buttons)
	row := inner.Y + buttonsRow

	changed := m.OnMouseHover(stripX+1, row) // OK chunk, already selected

	assert.False(t, changed, "hovering the already-selected button reports no change")
	assert.Equal(t, 0, m.selectedButton, "selection unchanged")
}
