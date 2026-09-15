package status_test

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/status"
)

func TestPhase_Label_ReadsConfig(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.Config.Style.WatchingLabel = "W8"

	assert.Equal(t, "watching", status.Watching.String(), "String() stays lowercase for logs/CLI")
	assert.Equal(t, "W8", status.Watching.Label(bundle))
	assert.Equal(t, "FETCHING", status.InProgress.Label(bundle))
}

func TestPhase_LabelStyle_ForegroundOnly(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	// Distinct from the default WatchingColor (ansi.Cyan) so the assertion proves
	// the WatchingColor field is read, not a coincidental default match.
	bundle.Config.Style.WatchingColor = config.ConfigColor{Color: ansi.Magenta}

	got := status.Watching.LabelStyle(bundle)
	assert.Equal(t, ansi.Magenta, got.Fg)
	assert.Nil(t, got.Bg, "label sets foreground only; background stays default")
}

func TestPhase_LabelStyle_AuthDistinctFromInProgress(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	got := status.AuthInProgress.LabelStyle(bundle)
	assert.Equal(t, bundle.Config.Style.AuthInProgressColor.Color, got.Fg)
	assert.NotEqual(t, bundle.Config.Style.InProgressColor.Color, got.Fg, "auth color must be distinct from in-progress color")
}

func TestPhase_LabelStyle_DisabledAndCancelledGrey(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	assert.Equal(t, ansi.BrightBlack, status.Disabled.LabelStyle(bundle).Fg)
	assert.Equal(t, ansi.BrightBlack, status.Cancelled.LabelStyle(bundle).Fg)
	assert.Equal(t, ansi.BrightBlack, status.ReadOnly.LabelStyle(bundle).Fg)
	assert.Equal(t, "READ ONLY", status.ReadOnly.Label(bundle))
}

func TestPhase_MaxLabelWidth_LongestLabel(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	// Default longest label is "READ ONLY" (9).
	assert.Equal(t, 9, status.MaxLabelWidth(bundle))

	bundle.Config.Style.SuccessLabel = "COMPLETED" // 9
	assert.Equal(t, 9, status.MaxLabelWidth(bundle))
}

// TestPhase_MaxLabelWidth_GraphemeWidth pins that the label column is sized by
// display width, not byte length: a ZWJ-emoji label (25 bytes, 2 display cells)
// must not blow the column out to 25 — the widest ASCII label still wins.
func TestPhase_MaxLabelWidth_GraphemeWidth(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.Config.Style.SuccessLabel = "👨‍👩‍👧‍👦" // 25 bytes, 2 display cells
	assert.Equal(t, 9, status.MaxLabelWidth(bundle),
		"emoji label must count display width (2), not bytes (25); READ ONLY (9) stays widest")
}
