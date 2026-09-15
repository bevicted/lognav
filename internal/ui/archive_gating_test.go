package ui

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/state"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

// modelWithExperimental builds a root Model whose config has experimental
// features enabled or disabled, so the tab-gating tests can drive ui.New with a
// controlled flag.
func modelWithExperimental(t *testing.T, enabled bool) *Model {
	t.Helper()
	cfg := config.New()
	cfg.Core.EnableExperimental = enabled
	m, err := New(t.Context(), deps.New(cfg, state.New()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// newSizedArchiveModelWithPoster is the experimental-enabled sibling of
// newSizedModelWithPoster: the archive integration tests need experimental
// features ON (Archive tab present, dispatch bound) to exercise the archive
// paths that are otherwise gated off by default.
func newSizedArchiveModelWithPoster(t *testing.T) (*Model, *msgstest.FakePoster) {
	t.Helper()
	m := modelWithExperimental(t, true)
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	m.OnResize(120, 40)
	return m, fp
}

// With archive disabled (the default), the Archive tab must not exist: the tab
// bar carries only query/instances/logs/snapshots.
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestNew_ArchiveDisabled_HidesArchiveTab(t *testing.T) {
	m := modelWithExperimental(t, false)

	titles := m.tabs.Titles()
	assert.NotContains(t, titles, "archive", "archive tab must be absent when core.enableExperimental is false")
	assert.Equal(t, []string{"query", "instances", "logs", "snapshots"}, titles,
		"exactly the four non-experimental tabs remain")
}

// With archive enabled, the Archive tab is present as the last tab (so no other
// tab index shifts).
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestNew_ArchiveEnabled_ShowsArchiveTab(t *testing.T) {
	m := modelWithExperimental(t, true)

	titles := m.tabs.Titles()
	assert.Equal(t, []string{"query", "instances", "logs", "snapshots", "archive"}, titles,
		"archive tab present and last when enabled")
}

// With archive off there is no 5th tab, so the digit "5" must be a no-op — it
// must NOT clamp-select the last tab (snapshots), which would make 4 and 5 both
// jump to snapshots.
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestDigitTabSelect_ArchiveOff_Digit5IsNoOp(t *testing.T) {
	m := modelWithExperimental(t, false)
	m.OnResize(120, 40)
	m.tabs.Select(LogsTab)
	require.Equal(t, "logs", m.tabs.GetActiveTab().GetTitle(), "setup: on logs tab")

	m.HandleKey(uv.KeyPressEvent{Code: '5', Text: "5"})
	assert.Equal(t, "logs", m.tabs.GetActiveTab().GetTitle(),
		"digit 5 must not move when there is no 5th tab")

	m.HandleKey(uv.KeyPressEvent{Code: '4', Text: "4"})
	assert.Equal(t, "snapshots", m.tabs.GetActiveTab().GetTitle(),
		"digit 4 still selects snapshots")
}

// With archive on, digit "5" selects the Archive tab.
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestDigitTabSelect_ArchiveOn_Digit5SelectsArchive(t *testing.T) {
	m := modelWithExperimental(t, true)
	m.OnResize(120, 40)

	m.HandleKey(uv.KeyPressEvent{Code: '5', Text: "5"})
	assert.Equal(t, "archive", m.tabs.GetActiveTab().GetTitle(),
		"digit 5 selects the archive tab when present")
}
