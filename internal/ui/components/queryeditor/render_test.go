package queryeditor

import (
	"testing"

	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlaceQuery_CursorTracksGlyphAcrossWraps is the direct regression test for
// the master-flagged drift risk: it drives the editor's cursor to positions that
// span a soft-wrap boundary and asserts placeQuery returns a *component.Cursor whose
// absolute (X,Y) matches the hand-computed glyph position. Because the editor is
// the single wrap authority (render and cursor share one wrapLine), these must
// agree.
//
// Fixture: value "hello world" at width 8 wraps to display lines ["hello ",
// "world "]; prompt is "" so promptWidth is 0; rect origin is (2,3) and the
// content fits the height so scrollY is 0.
//
// No t.Parallel: newHandleKeyTestModel uses t.Setenv.
func TestPlaceQuery_CursorTracksGlyphAcrossWraps(t *testing.T) {
	const (
		rectX = 2
		rectY = 3
		rectW = 8
		rectH = 10
	)

	tests := []struct {
		name  string
		col   int // cursor rune offset within the single logical line
		wantX int
		wantY int
	}{
		{name: "start of first display line", col: 0, wantX: 2, wantY: 3},
		{name: "mid first display line", col: 4, wantX: 6, wantY: 3},
		{name: "wrap break belongs to second display line", col: 6, wantX: 2, wantY: 4},
		{name: "mid second display line", col: 8, wantX: 4, wantY: 4},
		{name: "end of line", col: 11, wantX: 7, wantY: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newHandleKeyTestModel(t)
			m.SetRect(component.Rect{X: rectX, Y: rectY, W: rectW, H: rectH})
			m.setQueryInEditor("hello world")
			m.editor.Focus()

			// Position the cursor at column tt.col: Home, then Right tt.col times.
			require.False(t, m.editor.HandleKey(uv.KeyPressEvent{Code: uv.KeyHome}))
			for range tt.col {
				m.editor.HandleKey(uv.KeyPressEvent{Code: uv.KeyRight})
			}

			canvas := uv.NewScreenBuffer(rectX+rectW, rectY+rectH)
			cursor := m.Draw(canvas)

			require.NotNil(t, cursor, "focused editor must return a cursor")
			assert.Equal(t, tt.wantX, cursor.X, "cursor X")
			assert.Equal(t, tt.wantY, cursor.Y, "cursor Y")
		})
	}
}

// TestPlaceQuery_UnfocusedReturnsNoCursor confirms an unfocused non-empty editor
// parks no terminal cursor (matching the empty-path behavior).
func TestPlaceQuery_UnfocusedReturnsNoCursor(t *testing.T) {
	m, _ := newHandleKeyTestModel(t)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 8, H: 10})
	m.setQueryInEditor("hello world")
	m.editor.Blur()

	canvas := uv.NewScreenBuffer(8, 10)
	assert.Nil(t, m.Draw(canvas), "unfocused editor must not return a cursor")
}
