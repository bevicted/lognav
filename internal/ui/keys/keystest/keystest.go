package keystest

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// PressRuneUV builds a uv key-press event for a plain rune (for HandleKey tests).
func PressRuneUV(t *testing.T, r rune) uv.KeyPressEvent {
	t.Helper()
	return uv.KeyPressEvent{Code: r, Text: string(r)}
}

// PressShiftRuneUV builds the legacy-terminal encoding of a Shift+<letter>
// press (uppercase Text, no ModShift) — normalize collapses it to "shift+<l>".
func PressShiftRuneUV(t *testing.T, upper rune) uv.KeyPressEvent {
	t.Helper()
	return uv.KeyPressEvent{Code: upper, Text: string(upper)}
}

// PressKeyUV builds a named-key press (e.g. uv.KeyEnter, uv.KeyPgDown).
func PressKeyUV(t *testing.T, code rune) uv.KeyPressEvent {
	t.Helper()
	return uv.KeyPressEvent{Code: code}
}

// PressCtrlUV builds a Ctrl+<rune> press.
func PressCtrlUV(t *testing.T, r rune) uv.KeyPressEvent {
	t.Helper()
	return uv.KeyPressEvent{Code: r, Mod: uv.ModCtrl}
}
