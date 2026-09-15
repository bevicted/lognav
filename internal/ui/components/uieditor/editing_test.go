package uieditor

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
)

func key(code rune) uv.KeyPressEvent { return uv.KeyPressEvent{Code: code} }
func keyMod(code rune, m uv.KeyMod) uv.KeyPressEvent {
	return uv.KeyPressEvent{Code: code, Mod: m}
}
func text(s string) uv.KeyPressEvent { return uv.KeyPressEvent{Code: []rune(s)[0], Text: s} }

func TestEditor_Insert_Printable(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	assert.True(t, e.HandleKey(text("h")))
	assert.True(t, e.HandleKey(text("i")))
	assert.Equal(t, "hi", e.Value())
}

func TestEditor_InsertString_MultiLine(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.InsertString("line1\nline2\nline3")
	assert.Equal(t, "line1\nline2\nline3", e.Value())
	assert.Equal(t, 2, e.Line())
}

func TestEditor_InsertString_MidLineSnippet(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetValue("ad")
	atRowCol(e, 0, 1)
	e.InsertString("bc")
	assert.Equal(t, "abcd", e.Value())
	assert.Equal(t, 3, e.col)
}

func TestEditor_Enter_SplitsLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   string
		col     int
		want    string
		wantRow int
		wantCol int
	}{
		{name: "split mid line", value: "hello", col: 2, want: "he\nllo", wantRow: 1, wantCol: 0},
		{name: "split at end", value: "hello", col: 5, want: "hello\n", wantRow: 1, wantCol: 0},
		{name: "split at start", value: "hello", col: 0, want: "\nhello", wantRow: 1, wantCol: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := New()
			e.SetWidth(40)
			e.SetValue(tt.value)
			atRowCol(e, 0, tt.col)
			assert.True(t, e.HandleKey(key(uv.KeyEnter)))
			assert.Equal(t, tt.want, e.Value())
			assert.Equal(t, tt.wantRow, e.Line())
			assert.Equal(t, tt.wantCol, e.col)
		})
	}
}

func TestEditor_Backspace_MergesLine(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetValue("a\nb")
	atRowCol(e, 1, 0) // start of second line
	assert.True(t, e.HandleKey(key(uv.KeyBackspace)))
	assert.Equal(t, "ab", e.Value())
	assert.Equal(t, 0, e.Line())
	assert.Equal(t, 1, e.col, "cursor at the seam")
}

func TestEditor_Delete_MergesLineBelow(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetValue("a\nb")
	atRowCol(e, 0, 1) // end of first line
	assert.True(t, e.HandleKey(key(uv.KeyDelete)))
	assert.Equal(t, "ab", e.Value())
	assert.Equal(t, 0, e.Line())
}

func TestEditor_WordDelete_Backward(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ev   uv.KeyPressEvent
	}{
		{name: "alt+backspace", ev: keyMod(uv.KeyBackspace, uv.ModAlt)},
		{name: "ctrl+w", ev: keyMod('w', uv.ModCtrl)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := New()
			e.SetWidth(40)
			e.SetValue("hello world") // cursor at end
			assert.True(t, e.HandleKey(tt.ev))
			assert.Equal(t, "hello ", e.Value())
		})
	}
}

func TestEditor_WordDelete_Forward(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetValue("hello world")
	atRowCol(e, 0, 0)
	assert.True(t, e.HandleKey(keyMod(uv.KeyDelete, uv.ModAlt)))
	assert.Equal(t, " world", e.Value())
}

func TestEditor_WordMotion(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetValue("hello world") // cursor at end (col 11)

	// alt+Left from end → start of "world" (col 6).
	assert.False(t, e.HandleKey(keyMod(uv.KeyLeft, uv.ModAlt)))
	assert.Equal(t, 6, e.col)
	// alt+Left again → start of "hello" (col 0).
	assert.False(t, e.HandleKey(keyMod(uv.KeyLeft, uv.ModAlt)))
	assert.Equal(t, 0, e.col)
	// alt+Right → after "hello" (col 5).
	assert.False(t, e.HandleKey(keyMod(uv.KeyRight, uv.ModAlt)))
	assert.Equal(t, 5, e.col)
}

func TestEditor_LineMotion_CrossesLines(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetValue("ab\ncd")

	// Home / End on the current (second) line.
	assert.False(t, e.HandleKey(key(uv.KeyHome)))
	assert.Equal(t, 0, e.col)
	assert.False(t, e.HandleKey(key(uv.KeyEnd)))
	assert.Equal(t, 2, e.col)
	// Left at col 0 of line 1 → end of line 0.
	atRowCol(e, 1, 0)
	assert.False(t, e.HandleKey(key(uv.KeyLeft)))
	assert.Equal(t, 0, e.Line())
	assert.Equal(t, 2, e.col)
	// Right at end of line 0 → start of line 1.
	assert.False(t, e.HandleKey(key(uv.KeyRight)))
	assert.Equal(t, 1, e.Line())
	assert.Equal(t, 0, e.col)
}

func TestEditor_VerticalMove_AcrossWrap(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(8)
	e.SetValue("hello world") // wraps to 2 display lines
	atRowCol(e, 0, 2)         // display line 0, near col 2
	e.repositionView()
	assert.Equal(t, 0, e.CursorDisplayLine())

	assert.False(t, e.HandleKey(key(uv.KeyDown)))
	assert.Equal(t, 1, e.CursorDisplayLine(), "Down crosses the soft-wrap")
	assert.False(t, e.HandleKey(key(uv.KeyUp)))
	assert.Equal(t, 0, e.CursorDisplayLine(), "Up returns across the soft-wrap")
}

func TestEditor_PasteWithNewlines(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetValue("a\nb")
	atRowCol(e, 1, 1) // end of "b"
	e.InsertString("\nc\n")
	assert.Equal(t, "a\nb\nc\n", e.Value())
}

func TestEditor_HandleKey_MutatedBool(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetValue("abc")
	// Motion → not mutated.
	assert.False(t, e.HandleKey(key(uv.KeyLeft)))
	// Edit → mutated.
	assert.True(t, e.HandleKey(key(uv.KeyBackspace)))
}

func TestEditor_DroppedKeysAreInert(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ev   uv.KeyPressEvent
	}{
		{name: "ctrl+k kill-line", ev: keyMod('k', uv.ModCtrl)},
		{name: "ctrl+u kill-to-start", ev: keyMod('u', uv.ModCtrl)},
		{name: "ctrl+a line-start alias", ev: keyMod('a', uv.ModCtrl)},
		{name: "ctrl+Home doc-begin", ev: keyMod(uv.KeyHome, uv.ModCtrl)},
		{name: "pgdown", ev: key(uv.KeyPgDown)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := New()
			e.SetWidth(40)
			e.SetValue("abc")
			before := e.Value()
			beforeCol := e.col
			assert.False(t, e.HandleKey(tt.ev), "dropped key must report not-mutated")
			assert.Equal(t, before, e.Value(), "dropped key must not change the buffer")
			assert.Equal(t, beforeCol, e.col, "dropped key must not move the cursor")
		})
	}
}

func TestEditor_FocusedTabIsNoOp(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetValue("abc")
	// A focused Tab arrives as printable "\t"; sanitize strips it → no change.
	e.HandleKey(uv.KeyPressEvent{Code: '\t', Text: "\t"})
	assert.Equal(t, "abc", e.Value())
}
