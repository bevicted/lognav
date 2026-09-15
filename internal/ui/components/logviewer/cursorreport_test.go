package logviewer

import (
	"testing"

	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The contextual provider reads m.cursor directly. Cursor moves therefore do
// not need a root callback or an event round trip.
func TestCursorMovesAreReadDirectlyByStatusProvider(t *testing.T) {
	t.Parallel()
	m, fp := newViewerModel(t, 10)
	before := len(fp.Posted)

	require.NotZero(t, m.HandleKey(keystest.PressRuneUV(t, 'j')))
	assert.Equal(t, 1, m.cursor.log)
	assert.Len(t, fp.Posted, before, "cursor movement does not post status state")
	assert.Equal(t, "2/10", m.StatusVariants()[0][1].Value)
}

func TestMoveCursorOutOfFilterKeepsRawCursorValid(t *testing.T) {
	t.Parallel()
	m, _ := newViewerModel(t, 10)
	m.store.state.filtered.Set(m.cursor.log, true)

	m.moveCursorOutOfFilter()
	assert.NotEqual(t, 0, m.cursor.log)
}
