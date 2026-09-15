package ui

import (
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/ui/components/archivehandler"
	"github.com/bevicted/lognav/internal/ui/components/filehandler"
	"github.com/bevicted/lognav/internal/ui/components/instancepicker"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

// TestUI_ArchiveCollectMsg_RoutesToStartCollect is the root-wiring gate for
// Phase 8: an ArchiveCollectMsg (emitted by the Archive tab on Enter over a ready
// archive) must reach instancepicker.StartCollect and, on success, switch to the
// Instances tab. We drive the REAL root Update routing and assert the observable
// side effects of a successful collect: the query is set on State (startCollect's
// first action) and the active tab becomes "instances". An empty archive enables
// no instances, so ResolveTokens spawns no auth workers — the test makes no
// network calls.
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestUI_ArchiveCollectMsg_RoutesToStartCollect(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := newSizedArchiveModelWithPoster(t)

	require.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(), "setup: active tab is query")

	const wantQuery = "source logs last 7d | filter $d.kubernetes.namespace_name == 'archived'"
	a := &archive.Archive{Query: wantQuery} // no instances: hermetic (no auth workers)

	m.Update(archivehandler.ArchiveCollectMsg{Archive: a})

	assert.Equal(t, wantQuery, m.bundle.State.Query(),
		"ArchiveCollectMsg must route through StartCollect, which sets the archive query on State")
	assert.Equal(t, "instances", m.tabs.GetActiveTab().GetTitle(),
		"a successful collect must switch to the Instances tab")
}

// TestUI_ArchivePollDoneMsg_RoutesToArchiveHandler verifies that
// ArchivePollDoneMsg (posted by the Archive tab's manual poll worker) reaches the
// archive handler, whose OnArchivePollDone refreshes the file list — observable as
// a freshly posted filehandler.IOMsg (the ListOP refresh) on the poster.
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestUI_ArchivePollDoneMsg_RoutesToArchiveHandler(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, fp := newSizedArchiveModelWithPoster(t)

	before := len(fp.Posted)
	m.Update(archivehandler.ArchivePollDoneMsg{Name: "any" + archive.FileExt})

	var sawIO bool
	for _, ev := range fp.Posted[before:] {
		if _, ok := ev.(filehandler.IOMsg); ok {
			sawIO = true
			break
		}
	}
	assert.True(t, sawIO,
		"ArchivePollDoneMsg must route to the archive handler, which posts a filehandler IOMsg to refresh the list")
}

// TestUI_DispatchSuccess_SwitchesToArchiveTabAndSelects proves the dispatch
// SUCCESS path: an ArchiveDispatchedMsg carrying a Saved name + Succeeded>0 must
// switch to the Archive tab unconditionally and
// trigger the archive list to refresh-and-select the saved row (observed as a
// freshly posted filehandler.IOMsg from the archive handler). No success dialog is
// shown.
//
//nolint:paralleltest // ui.New writes package-level keys state; t.Setenv for XDG.
func TestUI_DispatchSuccess_SwitchesToArchiveTabAndSelects(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, fp := newSizedArchiveModelWithPoster(t)
	// The dispatch worker writes the archive file off-loop BEFORE the message; mirror
	// that by seeding the file so the deferred select has a row to land on.
	require.NoError(t, archive.Save(&archive.Archive{
		Name: "saved-arch", SubmittedAt: timeNowForTest(),
		Instances: []archive.InstanceEntry{{CRN: testArchiveCRN("a"), State: archive.StateRunning}},
	}))
	require.NotEqual(t, "archive", m.tabs.GetActiveTab().GetTitle(), "setup: not on the archive tab")

	before := len(fp.Posted)
	m.Update(instancepicker.ArchiveDispatchedMsg{Saved: "saved-arch", Succeeded: 1, Attempted: 1})

	assert.Equal(t, "archive", m.tabs.GetActiveTab().GetTitle(),
		"a successful dispatch switches to the Archive tab")

	var sawIO, sawDialog bool
	for _, ev := range fp.Posted[before:] {
		if _, ok := ev.(filehandler.IOMsg); ok {
			sawIO = true
		}
		if _, ok := ev.(msgs.ShowDialogMsg); ok {
			sawDialog = true
		}
	}
	assert.True(t, sawIO, "dispatch success refreshes the archive list (to select the new row)")
	assert.False(t, sawDialog, "dispatch success shows NO dialog (the success notice was removed)")
}

// timeNowForTest returns a fixed non-zero time for seeding test archives.
func timeNowForTest() time.Time { return time.Unix(1_750_000_000, 0).UTC() }

func testArchiveCRN(name string) string {
	return "crn:v1:bluemix:public:logs:us-south:a/" + name + ":" + name + "::"
}

// TestUI_QQuitsOnArchiveTab is the root-level regression for "q does not quit on
// the archive tab": with the Archive tab active and the fuzzy filter unfocused, a
// `q` press must post msgs.QuitMsg, exactly as it does on the snapshot tab. The
// Archive tab previously bound no Quit key (the generic filehandler binds only
// file ops), so `q` was silently dropped. The snapshot subtest pins that the fix
// did not regress the existing tabs.
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestUI_QQuitsOnArchiveTab(t *testing.T) {
	for _, tc := range []struct {
		name string
		tab  int
	}{
		{"archive", ArchiveTab},
		{"snapshot", SnapshotTab},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			m, fp := newSizedArchiveModelWithPoster(t)
			m.tabs.Select(tc.tab)
			require.False(t, m.isFocusTaken, "setup: focus is not taken")

			before := len(fp.Posted)
			m.HandleKey(uv.KeyPressEvent{Code: 'q', Text: "q"})

			var quit bool
			for _, ev := range fp.Posted[before:] {
				if _, ok := ev.(msgs.QuitMsg); ok {
					quit = true
				}
			}
			assert.True(t, quit, "q with no focus must post msgs.QuitMsg on the %s tab", tc.name)
		})
	}
}

// TestUI_EnterOnArchiveTab_RoutesThroughRealKeyPath is the root-level regression
// for "Enter does nothing on the archive tab": with a ready archive selected and
// the filter unfocused, an Enter delivered through the REAL root key path
// (m.HandleKey) must reach the archive tab's onEnter and emit a collect. We re-feed
// the posted events (the FakePoster records but does not re-deliver them) and assert
// the collect side effect: a successful collect switches to the Instances tab.
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestUI_EnterOnArchiveTab_RoutesThroughRealKeyPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	// A ready, NOT-expired archive: onEnter emits a collect (a stale SubmittedAt
	// would route to the expired branch instead). The single success instance is
	// not in the picker's config, so startCollect enables zero instances and spawns
	// no auth/network workers (hermetic).
	require.NoError(t, archive.Save(&archive.Archive{
		Name: "ready-arch", Query: "source logs last 7d", SubmittedAt: time.Now(),
		Instances: []archive.InstanceEntry{{CRN: testArchiveCRN("a"), State: archive.StateSuccess}},
	}))
	m, fp := newSizedArchiveModelWithPoster(t)
	m.tabs.Select(ArchiveTab) // WithOnLeave fires ah.ListFiles()
	// Drain the archive list refresh so a row exists and the cursor lands on it.
	feedPosted(m, fp, func(ev uv.Event) bool { _, ok := ev.(filehandler.IOMsg); return ok })
	require.Equal(t, "archive", m.tabs.GetActiveTab().GetTitle(), "setup: on the archive tab")

	before := len(fp.Posted)
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	// onEnter emits ArchiveCollectMsg off-loop; re-feed it so the root routes it to
	// StartCollect. This fixture CRN is intentionally unconfigured, so collect
	// refuses visibly and leaves the archive tab selected.
	var sawCollect bool
	for _, ev := range fp.Posted[before:] {
		if c, ok := ev.(archivehandler.ArchiveCollectMsg); ok {
			sawCollect = true
			m.Update(c)
		}
	}
	require.True(t, sawCollect, "unfocused Enter through the real key path must emit a collect")
	assert.Equal(t, "archive", m.tabs.GetActiveTab().GetTitle(),
		"an archive with an unconfigured CRN must stay on the Archive tab")
	var sawRefusal bool
	for _, ev := range fp.Posted[before:] {
		if dialog, ok := ev.(msgs.ShowDialogMsg); ok && strings.Contains(dialog.Message, "not configured") {
			sawRefusal = true
		}
	}
	assert.True(t, sawRefusal, "an unconfigured archive collect refusal is visible")
}

// feedPosted re-delivers to m.Update every event the poster has recorded so far
// that matches pred (the runtime loop would normally deliver these). Used by root
// tests to apply async side effects (e.g. the archive list refresh) deterministically.
func feedPosted(m *Model, fp *msgstest.FakePoster, pred func(uv.Event) bool) {
	for _, ev := range fp.Posted {
		if pred(ev) {
			m.Update(ev)
		}
	}
}

// TestUI_ArchiveTab_Registered asserts the Archive tab is registered as the 5th
// tab with the lowercase "archive" title (matching the other file tabs) and that
// numTabs accounts for it.
//
//nolint:paralleltest // ui.New writes package-level keys state (shared across the package).
func TestUI_ArchiveTab_Registered(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := newSizedArchiveModelWithPoster(t)

	require.Equal(t, 5, numTabs, "five tabs after wiring the Archive tab")
	m.tabs.Select(ArchiveTab)
	assert.Equal(t, "archive", m.tabs.GetActiveTab().GetTitle(),
		"the 5th tab is the lowercase-titled Archive tab")
}
