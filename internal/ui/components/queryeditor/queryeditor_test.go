package queryeditor

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModel_OnViewInContext_InstallsQuery verifies the typed OnViewInContext
// method (the R5a B1 direct-dispatch target) builds the view-in-context
// Dataprime query from the message's timestamp + pod id and installs it in the
// editor — matching the behavior of the former Update arm.
func TestModel_OnViewInContext_InstallsQuery(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, err := New(t.Context(), depstest.NewTest(t), &EventHandler{})
	require.NoError(t, err)

	m.OnViewInContext(msgs.ViewInContextMsg{Timestamp: 0, PodID: "pod-xyz", LogID: "log-1"})

	got := m.getQuery()
	assert.Contains(t, got, "pod-xyz", "in-context query must embed the pod id")
	assert.True(t, strings.HasPrefix(got, "source logs around"),
		"in-context query must use the view-in-context template")
}

func TestNew_ReturnsErrorWhenDataDirCannotBeCreated(t *testing.T) {
	// /dev/null is a character device; os.MkdirAll under it fails reliably
	// on Unix-like systems with ENOTDIR. Use it as the XDG_DATA_HOME root.
	t.Setenv("XDG_DATA_HOME", "/dev/null")
	_, err := New(t.Context(), depstest.NewTest(t), &EventHandler{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve query data dir")
}

func TestNewAt_ResolvesStartupTemplates(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	bundle.Config.ICL.DefaultQuery = "source logs {{ date }} {{ timestamp }}"
	bundle.Config.Core.ExtraSnippets = []config.Snippet{{Snippet: "extra {{ date -1 }}", Desc: "extra description"}}
	bundle.Config.Core.DefaultSnippets = []config.Snippet{{Snippet: "built-in {{ timestamp \"1h\" }}", Desc: "built-in description"}}
	bundle.Config.Core.IncludeDefaultSnippets = true
	at := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)

	m, err := newAt(t.Context(), bundle, &EventHandler{}, at)
	require.NoError(t, err)
	assert.Equal(t, "source logs 2025-01-02 2025-01-02T03:04:05Z", m.Query())
	assert.Equal(t, []snippet{
		{Snippet: "extra 2025-01-01", Description: "extra description"},
		{Snippet: "built-in 2025-01-02T04:04:05Z", Description: "built-in description"},
	}, m.snippets)
	assert.Equal(t, "source logs {{ date }} {{ timestamp }}", bundle.Config.ICL.DefaultQuery, "configured template remains unchanged")

	poster := &msgstest.FakePoster{}
	m.SetPoster(poster)
	m.setQueryInEditor("")
	m.openSnippets()
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	assert.Equal(t, "extra 2025-01-01", m.Query(), "snippet insertion uses resolved content")

	poster.Posted = nil
	openEditor(t.Context(), poster, m.Query(), true, m.snippets)
	require.Len(t, poster.Posted, 1)
	req, ok := poster.Posted[0].(msgs.ExecRequest)
	require.True(t, ok)
	seed, err := os.ReadFile(req.Cmd.Args[len(req.Cmd.Args)-1])
	require.NoError(t, err)
	assert.Contains(t, string(seed), "extra 2025-01-01")
	assert.Contains(t, string(seed), "built-in 2025-01-02T04:04:05Z")
	assert.Contains(t, string(seed), "extra description")
	assert.Contains(t, string(seed), "built-in description")
	assert.IsType(t, EditorFinishedMsg{}, req.Done(assert.AnError))
}

func TestNewAt_UsesPublicDefaultQueryAndGenericSnippets(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	at := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)

	m, err := newAt(t.Context(), depstest.NewTest(t), &EventHandler{}, at)

	require.NoError(t, err)
	assert.Equal(t, "source logs between @'2025-01-01' and @'now'\n| orderby $m.timestamp asc\n", m.Query())
	assert.Contains(t, m.snippets, snippet{Snippet: "| filter $l.subsystemname == 'service'", Description: "keep rows matching condition (alias: f)"})
	assert.NotContains(t, m.Query(), "subsystemname")
}

func TestNewAt_TemplateErrorIncludesSource(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	bundle.Config.ICL.DefaultQuery = "{{ date"

	_, err := newAt(t.Context(), bundle, &EventHandler{}, time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "icl.defaultQuery")
}

func TestQueryLoadsRemainVerbatimAfterStartup(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, err := newAt(t.Context(), depstest.NewTest(t), &EventHandler{}, time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC))
	require.NoError(t, err)

	m.OnQueryLoaded(QueryLoadMsg{Q: "{{ date }}"})
	assert.Equal(t, "{{ date }}", m.Query(), "saved query must remain literal")
	m.OnSnapshotRestore(msgs.SnapshotRestoreMsg{Snapshot: snapshot.Snapshot{Query: "{{ date }}"}})
	assert.Equal(t, "{{ date }}", m.Query(), "restored snapshot must remain literal")
	m.OnInjectQuery(msgs.InjectQueryMsg{Snippet: "| filter $d == '{{ timestamp }}'"})
	assert.Contains(t, m.Query(), "{{ timestamp }}", "injected clause must remain literal")
	m.OnEditorFinished(EditorFinishedMsg{out: "{{ timestamp }}"})
	assert.Equal(t, "{{ timestamp }}", m.Query(), "editor result must remain literal")
}

func TestOnInjectQuery_AppendsClauseOnNewLine(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle, &EventHandler{})
	require.NoError(t, err)
	m.setQueryInEditor("source logs last 1h\n")

	m.OnInjectQuery(msgs.InjectQueryMsg{Snippet: "| filter $d.log.status_code == 200"})

	want := "source logs last 1h\n| filter $d.log.status_code == 200"
	assert.Equal(t, want, m.getQuery())
	assert.Equal(t, want, bundle.State.Query())
}

func TestOnInjectQuery_InsertsAfterLastFilterLine(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle, &EventHandler{})
	require.NoError(t, err)
	m.setQueryInEditor("source logs last 5h\n" +
		"| filter $l.subsystemname == 'example-service'\n" +
		"| f $d ~~ 'request-token' || $d.log.trace_id == 'trace-token'\n" +
		"| orderby $m.timestamp asc")

	m.OnInjectQuery(msgs.InjectQueryMsg{Snippet: "| filter $d.log.req-id == '73bef3e7-9ea3-49b9-939e-request-token'"})

	want := "source logs last 5h\n" +
		"| filter $l.subsystemname == 'example-service'\n" +
		"| f $d ~~ 'request-token' || $d.log.trace_id == 'trace-token'\n" +
		"| filter $d.log.req-id == '73bef3e7-9ea3-49b9-939e-request-token'\n" +
		"| orderby $m.timestamp asc"
	assert.Equal(t, want, m.getQuery())
	assert.Equal(t, want, bundle.State.Query())
}

func TestOnInjectQuery_NoFilterLineAppendsAtEnd(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle, &EventHandler{})
	require.NoError(t, err)
	m.setQueryInEditor("source logs last 5h\n| orderby $m.timestamp asc")

	m.OnInjectQuery(msgs.InjectQueryMsg{Snippet: "| filter $d.x == 1"})

	want := "source logs last 5h\n| orderby $m.timestamp asc\n| filter $d.x == 1"
	assert.Equal(t, want, m.getQuery())
}

func TestOnInjectQuery_EmptyQueryAppendsAnyway(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle, &EventHandler{})
	require.NoError(t, err)
	m.setQueryInEditor("")

	m.OnInjectQuery(msgs.InjectQueryMsg{Snippet: "| filter $d.x == 1"})

	assert.Equal(t, "| filter $d.x == 1", m.getQuery())
}

// TestPlaceQuery_EmptyValue_PlaceholderAndCursor asserts that for an empty
// textarea value placeQuery always draws the placeholder text with AttrFaint at
// the rect origin, but only returns a cursor when the textarea is focused (an
// unfocused empty editor must not park a visible terminal cursor at the origin).
//
// No t.Parallel: t.Setenv (XDG_DATA_HOME) forbids it.
func TestPlaceQuery_EmptyValue_PlaceholderAndCursor(t *testing.T) {
	const (
		rectX = 2
		rectY = 3
		rectW = 80
		rectH = 5
	)

	tests := []struct {
		name       string
		focus      bool
		wantCursor bool
	}{
		{name: "focused returns real cursor at origin", focus: true, wantCursor: true},
		{name: "unfocused returns nil cursor", focus: false, wantCursor: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			m, err := New(t.Context(), depstest.NewTest(t), &EventHandler{})
			require.NoError(t, err)
			// New sets the default query; clear it to exercise the empty-value path.
			m.setQueryInEditor("")
			if tt.focus {
				m.editor.Focus()
			}
			require.Equal(t, tt.focus, m.editor.Focused(), "setup: focus state")

			r := component.Rect{X: rectX, Y: rectY, W: rectW, H: rectH}
			m.SetRect(r)

			canvas := uv.NewScreenBuffer(rectX+rectW, rectY+rectH)
			cursor := m.Draw(canvas)

			// Placeholder first glyph ('d' from "dataprime query") sits at (r.X, r.Y)
			// with AttrFaint set — drawn regardless of focus.
			c := canvas.CellAt(rectX, rectY)
			require.NotNil(t, c, "cell at placeholder origin must not be nil")
			assert.Equal(t, "d", c.Content, "first glyph of placeholder must be 'd'")
			assert.NotZero(t, c.Style.Attrs&uv.AttrFaint, "placeholder cell must carry AttrFaint")

			if tt.wantCursor {
				// SetVirtualCursor(false) is why a REAL cursor is returned (not a
				// virtual one); (r.X, r.Y) is the empty-path placeholder origin.
				wantCursor := component.NewCursor(rectX, rectY)
				require.NotNil(t, cursor, "focused empty editor must return a cursor")
				assert.Equal(t, wantCursor.X, cursor.X)
				assert.Equal(t, wantCursor.Y, cursor.Y)
			} else {
				assert.Nil(t, cursor, "unfocused empty editor must not return a cursor")
			}
		})
	}
}
