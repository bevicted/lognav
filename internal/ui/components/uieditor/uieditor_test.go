package uieditor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEditor_ValueRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "single rune", in: "x"},
		{name: "single line", in: "hello world"},
		{name: "multi line", in: "a\nb\nc"},
		{name: "trailing newline", in: "a\n"},
		{name: "leading newline", in: "\nb"},
		{name: "cjk", in: "日本語\nテスト"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := New()
			e.SetValue(tt.in)
			assert.Equal(t, tt.in, e.Value())
		})
	}
}

func TestEditor_New_Defaults(t *testing.T) {
	t.Parallel()
	e := New()
	assert.Empty(t, e.Value())
	assert.False(t, e.Focused())
	assert.Equal(t, 0, e.Line())
}

func TestEditor_SetValue_CursorAtEnd(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetValue("a\nbc")
	assert.Equal(t, 1, e.Line(), "cursor row should be the last logical line")
}

func TestEditor_FocusBlur(t *testing.T) {
	t.Parallel()
	e := New()
	assert.False(t, e.Focused())
	e.Focus()
	assert.True(t, e.Focused())
	e.Blur()
	assert.False(t, e.Focused())
}

func TestEditor_SizeClamps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		set        func(*Editor)
		wantWidth  int
		wantHeight int
	}{
		{name: "width zero clamps to 1", set: func(e *Editor) { e.SetWidth(0) }, wantWidth: 1, wantHeight: 1},
		{name: "width negative clamps to 1", set: func(e *Editor) { e.SetWidth(-5) }, wantWidth: 1, wantHeight: 1},
		{name: "width positive", set: func(e *Editor) { e.SetWidth(40) }, wantWidth: 40, wantHeight: 1},
		{name: "height zero clamps to 1", set: func(e *Editor) { e.SetHeight(0) }, wantWidth: 1, wantHeight: 1},
		{name: "height positive", set: func(e *Editor) { e.SetHeight(5) }, wantWidth: 1, wantHeight: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := New()
			tt.set(e)
			assert.Equal(t, tt.wantWidth, e.Width())
			assert.Equal(t, tt.wantHeight, e.Height())
		})
	}
}

func TestEditor_PromptPlaceholderRoundTrip(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetPrompt("> ")
	e.SetPlaceholder("dataprime query")
	assert.Equal(t, "> ", e.Prompt())
	assert.Equal(t, "dataprime query", e.Placeholder())
}
