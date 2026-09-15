package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/ui/components/queryeditor"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// TestUI_SetEditorQueryMsg_RoutesToEditor is the regression gate for the Phase 6
// routing bug: SetEditorQueryMsg (emitted by collect to show the archive's stored
// query) was missing from the Update outer dispatch case list, so it fell through
// to the "unhandled msg" default and never reached the query editor — the visible
// buffer never updated on collect. This drives the REAL root Update routing (not
// the isolated instancepicker Model the collect tests use) and asserts the editor
// buffer actually changed, which is precisely the gap the instancepicker tests
// could not catch.
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestUI_SetEditorQueryMsg_RoutesToEditor(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newSizedModel(t)

	const want = "source logs last 30d | filter $d.kubernetes.namespace_name == 'archived'"
	// Sanity: the editor does not already hold the target query (so the assertion
	// proves the route set it, not a pre-existing value).
	require.NotEqual(t, want, m.queryeditor.Query())

	m.Update(msgs.SetEditorQueryMsg{Query: want})

	assert.Equal(t, want, m.queryeditor.Query(),
		"SetEditorQueryMsg must route through Update to the query editor and replace its buffer")
}

// TestUI_EditorFinishedMsg_RoutesThroughUpdate gates the single entry point for
// external-editor completion: the runtime dispatches ExecRequest.Done()'s result
// into Update (there is no dedicated OnEditorFinished method on the root any
// more), so the message must reach the query editor through the normal
// dispatcher — the same arm the temp-file error posts already used.
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestUI_EditorFinishedMsg_RoutesThroughUpdate(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newSizedModel(t)

	m.Update(msgs.SetEditorQueryMsg{Query: "source logs | limit 1"})
	require.NotEmpty(t, m.queryeditor.Query())

	// The zero message is the "editor returned nothing" case; the assertion is
	// about the route, not the payload (the payload fields are unexported).
	m.Update(queryeditor.EditorFinishedMsg{})

	assert.Empty(t, m.queryeditor.Query(),
		"EditorFinishedMsg must route through Update to the query editor")
}
