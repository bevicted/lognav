package logviewer

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
)

// TestHandleKey_AlwaysHandleKH_Hit verifies that a key bound to alwaysHandleKH
// (ctrl+s = snapshot) is consumed as KeyHandled before any focus check and that
// the ManualSaveSnapshotMsg is posted off-loop via the poster.
func TestHandleKey_AlwaysHandleKH_Hit(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)

	ev := keystest.PressCtrlUV(t, 's')
	r := m.HandleKey(ev)
	assert.Equal(t, component.KeyHandled, r, "alwaysHandleKH key must return KeyHandled")

	// The snapshot action posts ManualSaveSnapshotMsg off-loop via the poster.
	var found bool
	for _, ev := range fp.Posted {
		if _, ok := ev.(msgs.ManualSaveSnapshotMsg); ok {
			found = true
			break
		}
	}
	assert.True(t, found, "poster must record a ManualSaveSnapshotMsg when snapshot key is pressed")
}

// TestHandleKey_RegularKH_Hit verifies that a default m.kh movement key ('j'
// = MoveLineDown) returns KeyHandled when timelineActive is false.
func TestHandleKey_RegularKH_Hit(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	assert.False(t, m.timelineActive)

	ev := keystest.PressRuneUV(t, 'j')
	r := m.HandleKey(ev)
	assert.Equal(t, component.KeyHandled, r, "regular kh movement key must return KeyHandled")
}

// TestHandleKey_TimelineActive_ConsumesUnboundKey verifies the unconditional
// consume: even a key the timeline doesn't have a binding for must return
// KeyHandled when timelineActive is true (mirrors the old `break` behaviour).
func TestHandleKey_TimelineActive_ConsumesUnboundKey(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.timelineActive = true
	m.timeline.Open(fakeTimelineStore{}, 0, false)

	// Use an arbitrary key that is not bound by the timeline (e.g. 'x').
	ev := uv.KeyPressEvent{Code: 'x', Text: "x"}
	r := m.HandleKey(ev)
	assert.Equal(t, component.KeyHandled, r,
		"timelineActive must consume ALL keys unconditionally, even unbound ones")
}

// TestHandleKey_Unbound_ReturnsKeyIgnored verifies that a key not bound by any
// handler falls through to KeyIgnored when no bar is focused and timeline is
// inactive.
func TestHandleKey_Unbound_ReturnsKeyIgnored(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))

	// Use a key that is not bound in any handler (e.g. F9).
	ev := keystest.PressKeyUV(t, uv.KeyF9)
	r := m.HandleKey(ev)
	assert.Equal(t, component.KeyIgnored, r, "unbound key must return KeyIgnored")
}
