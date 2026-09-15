package ui

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

// TestModel_OnPaste_DoesNotLeakToInactiveTab pins the paste-routing bug: a
// bracketed paste was broadcast to every tab component instead of being routed
// to the active one, so pasting while the Instances tab was active also inserted
// the clipboard text into the query editor (and updated the shared query state).
// The query state is the public, cross-component observation of that leak.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnPaste_DoesNotLeakToInactiveTab(t *testing.T) {
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	m.SetPoster(&msgstest.FakePoster{})
	t.Cleanup(func() { _ = m.Close() })
	m.OnResize(80, 24)

	m.tabs.Select(InstancesTab)
	before := bundle.State.Query()
	require.NotContains(t, before, "leaked", "query must not contain the paste before it happens")

	m.OnPaste(uv.PasteEvent{Content: "leaked"})

	assert.Equal(t, before, bundle.State.Query(),
		"paste on the Instances tab must not reach the query editor on the Query tab")
}

// bracketPasteSpy is a minimal component.Component that records bracketed-paste
// events, used as a stand-in tab component to assert OnPaste routing.
type bracketPasteSpy struct {
	hits        int
	lastContent string
}

func (s *bracketPasteSpy) Init()                                   {}
func (s *bracketPasteSpy) SetRect(component.Rect)                  {}
func (s *bracketPasteSpy) Draw(component.Screen) *component.Cursor { return nil }
func (s *bracketPasteSpy) GetKeybinds() []keys.Binding             { return nil }
func (s *bracketPasteSpy) OnPaste(ev uv.Event) {
	if p, ok := ev.(uv.PasteEvent); ok {
		s.hits++
		s.lastContent = p.Content
	}
}

// TestModel_OnPaste_DeliveredToActiveTabOnly pins the routing contract directly:
// the active tab's component receives the paste exactly once and no other tab
// component is touched.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnPaste_DeliveredToActiveTabOnly(t *testing.T) {
	m := newLeftTabsModel(t)
	active, inactive := &bracketPasteSpy{}, &bracketPasteSpy{}
	m.tabs.ReplaceComponent(QueryTab, active)
	m.tabs.ReplaceComponent(InstancesTab, inactive)
	m.tabs.Select(QueryTab)

	m.OnPaste(uv.PasteEvent{Content: "hello"})

	assert.Equal(t, 1, active.hits, "the active tab's component must receive the paste exactly once")
	assert.Equal(t, "hello", active.lastContent, "the pasted content must be handed down verbatim")
	assert.Zero(t, inactive.hits, "a non-active tab's component must not receive the paste")
}

// TestModel_OnPaste_ReachesArchiveTab covers the other half of the broadcast
// bug: the Archive tab was never in the hardcoded broadcast list, so paste into
// the archive file search was silently dropped even though archivehandler
// implements OnPaste. Routing through the active tab makes it reachable.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnPaste_ReachesArchiveTab(t *testing.T) {
	bundle := depstest.NewTest(t)
	bundle.Config.Core.EnableExperimental = true // the Archive tab is opt-in
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	m.SetPoster(&msgstest.FakePoster{})
	t.Cleanup(func() { _ = m.Close() })
	m.OnResize(80, 24)

	spy := &bracketPasteSpy{}
	m.tabs.ReplaceComponent(ArchiveTab, spy)
	m.tabs.Select(ArchiveTab)
	require.Equal(t, ArchiveTab+1, m.tabs.Count(), "the Archive tab must be present when experimental is on")
	require.Same(t, component.Component(spy), m.tabs.GetActiveComponent(), "the Archive tab must be selectable")

	m.OnPaste(uv.PasteEvent{Content: "archived"})

	assert.Equal(t, 1, spy.hits, "paste must reach the Archive tab's component when it is active")
	assert.Equal(t, "archived", spy.lastContent, "the pasted content must be handed down verbatim")
}
