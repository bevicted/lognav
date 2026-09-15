package instancepicker

import (
	"strings"
	"testing"
	"time"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/status"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatElapsed_HonorsFormat pins that the shared timer formatter applies
// the given fetch-time format string (the live picker and the snapshot preview
// both route through it so they cannot drift).
func TestFormatElapsed_HonorsFormat(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "1.50s", FormatElapsed("%.2fs", 1500*time.Millisecond))
	assert.Equal(t, "2.0s", FormatElapsed("%.1fs", 2*time.Second))
	assert.Equal(t, "12s", FormatElapsed("%.0fs", 12*time.Second))
}

// TestInstanceRowSegments_Format pins the shared instance-row format used by
// both the live picker (refreshList) and the snapshot preview: a padded,
// styled phase label, then the plain tab-spaced (two-space gap) name/count/timer.
func TestInstanceRowSegments_Format(t *testing.T) {
	t.Parallel()
	style := uv.Style{Attrs: uv.AttrBold}
	segs := InstanceRowSegments("DONE", style, 8, "prod-eu", 8, 1234, 6, "12.30s")

	require.Len(t, segs, 2)
	// Label segment — left-justified to labelWidth (8), carries the phase style.
	assert.Equal(t, "DONE    ", segs[0].Text)
	assert.Equal(t, style, segs[0].Style)
	// Name + count + timer segment — plain; two-space column gaps (no pipe bars),
	// name LEFT-justified to nameWidth (8), %5d count, %*s (width 6) timer.
	assert.Equal(t, "  prod-eu    1234  12.30s", segs[1].Text)
	assert.Equal(t, uv.Style{}, segs[1].Style)
}

// TestInstanceRowSegments_GraphemePadding pins that the label is padded by
// display width, not byte length: a wide emoji label (4 bytes, 2 display cells)
// padded to labelWidth 8 must yield 6 trailing spaces (8-2), so the rendered
// column is 8 cells wide. Byte padding ("%-*s") would add only 4 spaces (8-4
// bytes), under-filling the column to 6 cells and misaligning the row.
func TestInstanceStatus_UsesSelectedPhaseStyle(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	inst := &Instance{Name: "prod", CRN: "crn", state: status.Success}
	m := &Model{bundle: bundle, instances: Instances{inst}}

	got, ok := m.InstanceStatus("crn")

	require.True(t, ok)
	assert.Equal(t, status.Success.LabelStyle(bundle).Fg, got.PhaseStyle.Fg)
	assert.Nil(t, got.PhaseStyle.Bg)
	assert.NotZero(t, got.PhaseStyle.Attrs&uv.AttrReverse)
}

func TestInstanceRowSegments_GraphemePadding(t *testing.T) {
	t.Parallel()
	segs := InstanceRowSegments("🔥", uv.Style{}, 8, "prod-eu", 8, 1, 6, "1.0s")

	require.Len(t, segs, 2)
	assert.Equal(t, "🔥"+strings.Repeat(" ", 6), segs[0].Text,
		"label must be grapheme-padded: 8 cells - 2-cell emoji = 6 spaces")
	assert.Equal(t, 8, uniseg.StringWidth(segs[0].Text),
		"padded label must occupy exactly labelWidth display cells")
}
