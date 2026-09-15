package snapshothandler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/snapshot/snapshottest"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/components/queryeditor/highlight"
)

// TestPreview_HighlightsQuery verifies that the snapshot preview colors the
// query portion with the same Dataprime syntax highlighting as the editor:
// the leading "source" keyword renders as its own keyword-styled segment.
func TestPreview_HighlightsQuery(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)

	lines, err := preview(bundle, &snapshot.Snapshot{Query: "source logs"})
	require.NoError(t, err)

	require.NotEmpty(t, lines, "preview must include the query line")
	require.NotEmpty(t, lines[0], "query line must have at least one segment")
	assert.Equal(t, "source", lines[0][0].Text, "first segment is the keyword run")
	want := highlight.TokenCellStyle(bundle.Config, highlight.TokenKeyword)
	got := lines[0][0].Style
	assert.True(t, got.Equal(&want), "query keyword uses the keyword style")
	assert.Equal(t, "source logs", list.ItemText(lines[0]), "query text round-trips")
}

// TestLoadPreview_KnownContainer_ReturnsFormattedSummary builds a real
// snapshot container with a valid state frame and verifies that loadPreview
// returns a non-empty summary string without error.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestLoadPreview_KnownContainer_ReturnsFormattedSummary(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)

	dir := t.TempDir()
	p := filepath.Join(dir, "snap"+snapshot.FileExt)

	// Create a writable container and write a valid snapshot state frame.
	c := snapshottest.NewContainerFile(t, p)
	require.NoError(t, snapshot.SaveStateFrame(c, snapshot.Snapshot{Query: "test query"}), "state frame must be written")
	require.NoError(t, c.Close(), "Close must succeed after writing")

	// Re-open the container as read-only for loadPreview.
	c2, err := snapshot.OpenContainerReadOnly(p)
	require.NoError(t, err, "OpenContainerReadOnly must succeed for a written file")
	defer func() { _ = c2.Close() }()

	summary, err := loadPreview(bundle, c2)
	require.NoError(t, err, "loadPreview must not return an error for a valid container")
	assert.NotEmpty(t, summary, "loadPreview must return a non-empty summary for a known container")
}

// TestLoadPreview_EmptyContainer_ReturnsError verifies that loadPreview
// returns an error when the container has no state frame (empty container).
// snapshot.LoadState fails with ErrFrameDoesNotExist for a container without
// the "state" frame, so loadPreview propagates that error.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestLoadPreview_EmptyContainer_ReturnsError(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)

	dir := t.TempDir()
	p := filepath.Join(dir, "empty"+snapshot.FileExt)

	// Create an empty writable container and close it without writing any frames.
	c := snapshottest.NewContainerFile(t, p)
	require.NoError(t, c.Close(), "Close must succeed on an empty container")

	// Re-open read-only: the container exists but has no state frame.
	c2, err := snapshot.OpenContainerReadOnly(p)
	require.NoError(t, err, "OpenContainerReadOnly must succeed for an existing empty container")
	defer func() { _ = c2.Close() }()

	_, loadErr := loadPreview(bundle, c2)
	assert.Error(t, loadErr, "loadPreview must return an error when the state frame is absent")
}

// TestLoadPreview_CorruptContainer_ReturnsError verifies that loadPreview
// surfaces an error when the container file is corrupt (truncated mid-frame).
// If OpenContainerReadOnly rejects the corrupt file immediately, that is also
// a valid "corrupt → error" outcome per spec D11.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestLoadPreview_CorruptContainer_ReturnsError(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)

	dir := t.TempDir()
	p := filepath.Join(dir, "corrupt"+snapshot.FileExt)

	// Create a writable container, write some data, close it cleanly, then
	// truncate mid-file to simulate corruption.
	c := snapshottest.NewContainerFile(t, p)
	require.NoError(t, snapshot.SaveStateFrame(c, snapshot.Snapshot{Query: "will be corrupted"}))
	require.NoError(t, c.Close())

	// Truncate to 8 bytes: keeps the magic header (8 bytes) but removes the
	// version and all frame data, producing a truncated / corrupt container.
	require.NoError(t, os.Truncate(p, 8))

	c2, err := snapshot.OpenContainerReadOnly(p)
	if err != nil {
		// OpenContainerReadOnly rejected the corrupt file — valid outcome.
		return
	}
	defer func() { _ = c2.Close() }()

	_, loadErr := loadPreview(bundle, c2)
	assert.Error(t, loadErr, "corrupt container must surface an error from loadPreview")
}
