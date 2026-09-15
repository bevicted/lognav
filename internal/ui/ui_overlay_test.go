package ui

import (
	"testing"

	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/ui/components/filtermenu"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUI_ShowFilterMenu_OpensOverlay(t *testing.T) {
	t.Parallel()
	m, fp := newSizedModelWithPoster(t)
	m.Update(msgs.ShowFilterMenuMsg{Rules: []filter.Rule{{Type: filter.Include, Value: "x"}}})
	_, ok := m.overlay.(*filtermenu.Model)
	require.True(t, ok, "overlay should be a *filtermenu.Model")
	_ = fp
}

func TestUI_FilterMenuCloseSeam_ClearsOverlay(t *testing.T) {
	t.Parallel()
	m, _ := newSizedModelWithPoster(t)
	m.Update(msgs.ShowFilterMenuMsg{})
	require.NotNil(t, m.overlay)
	m.closeFilterMenu()
	assert.Nil(t, m.overlay)
}

func TestUI_FilterApplied_RoutesToLogviewer(t *testing.T) {
	t.Parallel()
	m, _ := newSizedModelWithPoster(t)
	// Should not panic and should reach the logviewer recompute path.
	require.NotPanics(t, func() { m.Update(msgs.FilterAppliedMsg{}) })
}

func TestUI_DismissOverlay_FilterMenu_ClearsSlotAndReleasesFocus(t *testing.T) {
	t.Parallel()
	m, fp := newSizedModelWithPoster(t)
	m.Update(msgs.ShowFilterMenuMsg{})
	require.NotNil(t, m.overlay, "setup: filter menu must be open")
	fp.Posted = nil

	m.dismissOverlay()

	assert.Nil(t, m.overlay, "dismissOverlay on the filter menu must clear the slot")
	var released bool
	for _, ev := range fp.Posted {
		if f, ok := ev.(msgs.FocusMsg); ok && !f.GrabFocus {
			released = true
		}
	}
	assert.True(t, released, "closing the filter menu must release the focus it grabbed")
}
