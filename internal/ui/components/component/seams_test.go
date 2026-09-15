package component

import (
	"bytes"
	"log/slog"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hoverOnly implements exactly one optional seam, so Seams must report one
// implemented and the rest missing.
type hoverOnly struct{}

func (hoverOnly) OnMouseHover(int, int) bool { return true }
func (hoverOnly) ClearMouseHover() bool      { return true }

// everySeam implements the whole catalog.
type everySeam struct{}

func (everySeam) HandleKey(uv.KeyPressEvent) KeyResult             { return KeyIgnored }
func (everySeam) OnMouseClick(int, int, uv.MouseButton) bool       { return false }
func (everySeam) OnMouseRight(int, int) bool                       { return false }
func (everySeam) OnMouseDoubleClick(int, int, uv.MouseButton) bool { return false }
func (everySeam) OnMousePaste(int, int, string) bool               { return false }
func (everySeam) OnMouseScroll(int, int, int) bool                 { return false }
func (everySeam) OnMouseHover(int, int) bool                       { return false }
func (everySeam) ClearMouseHover() bool                            { return false }
func (everySeam) OnPaste(uv.Event)                                 {}
func (everySeam) ReleaseFocus()                                    {}
func (everySeam) OpenContextMenu()                                 {}

// TestSeams_PartitionsTheCatalog proves Seams splits the catalog into exactly
// the seams the target implements and the ones it does not, with nothing lost or
// duplicated between the two.
func TestSeams_PartitionsTheCatalog(t *testing.T) {
	t.Parallel()

	implemented, missing := Seams(hoverOnly{})

	assert.Equal(t, []string{"MouseHoverTarget"}, implemented,
		"the single implemented seam is reported")
	assert.Len(t, missing, len(inputSeams)-1, "every other catalog seam is reported missing")
	assert.NotContains(t, missing, "MouseHoverTarget", "a seam is never in both halves")
}

// TestSeams_FullAndEmptyTargets covers the two extremes: a type implementing the
// whole catalog reports nothing missing, and a nil target implements nothing.
func TestSeams_FullAndEmptyTargets(t *testing.T) {
	t.Parallel()

	implemented, missing := Seams(everySeam{})
	assert.Len(t, implemented, len(inputSeams), "a type implementing every seam reports them all")
	assert.Empty(t, missing, "nothing is missing when every seam is implemented")

	implemented, missing = Seams(nil)
	assert.Empty(t, implemented, "a nil target implements no seam")
	assert.Len(t, missing, len(inputSeams), "a nil target reports the whole catalog missing")
}

// TestLogSeams_OneDebugLine proves the inventory emits exactly ONE line, at debug
// level (matching the root's unhandled-msg log, so both are visible under the
// same logging flag), naming both the implemented and the missing seams.
func TestLogSeams_OneDebugLine(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	LogSeams(logger, "snapshots", hoverOnly{})

	out := buf.String()
	require.Equal(t, 1, bytes.Count([]byte(out), []byte("\n")), "exactly one line per component")
	assert.Contains(t, out, "level=DEBUG", "the inventory is debug level")
	assert.Contains(t, out, "target=snapshots", "the line names the component")
	assert.Contains(t, out, "implements=MouseHoverTarget", "the line names the implemented seams")
	assert.Contains(t, out, "KeyTarget", "the line names the missing seams too")
}

// TestLogSeams_LevelGated proves the inventory is silent when the handler's level
// is above debug — it must not be a startup line users see at info.
func TestLogSeams_LevelGated(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	LogSeams(logger, "snapshots", hoverOnly{})

	assert.Empty(t, buf.String(), "no inventory output above debug level")
}
