package ui

import (
	"context"
	"path/filepath"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/snapshot/snapshottest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/contextmenu"
	"github.com/bevicted/lognav/internal/ui/components/dialog"
	"github.com/bevicted/lognav/internal/ui/components/helpoverlay"
	"github.com/bevicted/lognav/internal/ui/components/instancepicker"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

// TestModel_HandleInstance_StaleLoadDropped is the single B.1 regression
// gate: verifies that an InstanceLoadReadyMsg whose Name does not match
// the user's most-recent selection (intendedInstance) is silently
// dropped — no store mutation, no loadedInstance update, no statusline
// noise. The copy-move from inline switch arm to handleInstance method
// is the kind of refactor that can silently invert the stale-load
// guard's polarity; this test pins it.
func TestModel_HandleInstance_StaleLoadDropped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		intended        string
		incoming        string
		wantLoadedAfter string
	}{
		{name: "stale load is dropped", intended: "B", incoming: "A", wantLoadedAfter: ""},
		{name: "fresh load is applied", intended: "A", incoming: "A", wantLoadedAfter: "A"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Subtests cannot be parallelized: ui.New calls styles.InitHelpKeyStyle
			// which writes to the package-level keys.HelpKeyStyle variable.
			m, err := New(t.Context(), depstest.NewTest(t))
			require.NoError(t, err)
			m.intendedInstance = tc.intended

			// Construct a minimal InstanceLoadReadyMsg with the test's
			// target name. Err == nil so the success path executes; Logs
			// nil so SetLogsRaw is a no-op. instances list is empty so
			// FindByName(name) returns nil and the if-block at the heart
			// of the handler short-circuits without further effect.
			msg := instancepicker.InstanceLoadReadyMsg{CRN: tc.incoming}
			m.handleInstance(msg)

			assert.Equal(t, tc.wantLoadedAfter, m.loadedInstance,
				"loadedInstance must mirror stale-drop semantics")
		})
	}
}

func TestUIModel_Close_Idempotent(t *testing.T) {
	// Cannot use t.Parallel(): ui.New writes package-level keys.HelpKeyStyle
	// (race under -race). Same constraint as the B.1 regression test.

	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)

	require.NoError(t, m.Close())
	t.Run("second_call_is_noop", func(t *testing.T) {
		require.NoError(t, m.Close())
	})
}

func TestModel_ApplyEnvAPIKey(t *testing.T) {
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })

	m.ApplyEnv([]string{"IC_API_KEY=environment-key"})

	assert.Equal(t, "environment-key", bundle.Config.ICL.Environments["bluemix"].APIKey)
}

func TestModel_DrawTo_ReturnsNilOnEmptySize(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)

	canvas := uv.NewScreenBuffer(0, 0)
	assert.Nil(t, m.DrawTo(canvas),
		"DrawTo must no-op (nil cursor) before a WindowSizeMsg sets a size")
}

func TestModel_DrawTo_ComposesWithoutPanic(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)

	// Drive a resize so OnResize sizes the tabs/components before the draw pass
	// composes them.
	m.OnResize(80, 24)

	canvas := uv.NewScreenBuffer(80, 24)
	assert.NotPanics(t, func() { _ = m.DrawTo(canvas) })
}

// TestModel_Update_TabSelectMsg_SwitchesActiveTab verifies that the tabselector
// key handler routes a digit key press to tabs.Select and changes the active tab.
// Digit "2" is bound to InstancesTab (index 1) in bindKeyhandlersToModel.
//
//nolint:paralleltest // mutates package-level keys.HelpKeyStyle via ui.New
func TestModel_Update_TabSelectMsg_SwitchesActiveTab(t *testing.T) {
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })

	// Initial tab is QueryTab (index 0) — title is "query".
	assert.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
		"initial active tab should be query")

	// Send the "2" key press, which the tabselector binds to tabs.Select(1).
	// Keystroke() resolves the printable rune to "2", so the handler map lookup
	// succeeds with key "2".
	m.HandleKey(uv.KeyPressEvent{Code: '2', Text: "2"})

	assert.Equal(t, "instances", m.tabs.GetActiveTab().GetTitle(),
		"active tab should switch to instances after pressing '2'")
}

// TestModel_Update_FocusMsg_RoutesToTargetComponent verifies that FocusMsg with
// GrabFocus=true sets isFocusTaken on the model, and GrabFocus=false clears it.
//
//nolint:paralleltest // mutates package-level keys.HelpKeyStyle via ui.New
func TestModel_Update_FocusMsg_RoutesToTargetComponent(t *testing.T) {
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })

	assert.False(t, m.isFocusTaken, "isFocusTaken should be false initially")

	m.Update(msgs.FocusMsg{GrabFocus: true})
	assert.True(t, m.isFocusTaken, "isFocusTaken should be true after GrabFocus=true")

	m.Update(msgs.FocusMsg{GrabFocus: false})
	assert.False(t, m.isFocusTaken, "isFocusTaken should be false after GrabFocus=false")
}

// TestModel_FocusMsg_KeyRoutingEffect asserts the R2 §Spike outcome: FocusMsg
// stays a posted event (not a direct call), and the root consumes it to gate
// the key-dispatch precedence chain. Specifically:
//   - After FocusMsg{GrabFocus:true}, a tabselector digit key is routed to the
//     active component (which ignores it) rather than switching tabs.
//   - After FocusMsg{GrabFocus:false}, the same digit switches tabs again.
//
// We assert the routing EFFECT (tab title unchanged / changed), not the
// unexported isFocusTaken field, so the test survives field renames and
// remains a meaningful integration check.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_FocusMsg_KeyRoutingEffect(t *testing.T) {
	m := newSizedModel(t)
	require.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
		"setup: active tab must be query before focus manipulation")

	// 1. Grab focus via a posted FocusMsg (the spike outcome: it stays an event).
	m.Update(msgs.FocusMsg{GrabFocus: true})

	// With focus taken, the tabselector's '2' binding must NOT fire — the key
	// bypasses tabselector and reaches the active component (queryeditor), which
	// does not switch tabs.
	m.HandleKey(uv.KeyPressEvent{Code: '2', Text: "2"})
	assert.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
		"tab must NOT switch while FocusMsg{GrabFocus:true} is in effect")

	// 2. Release focus via FocusMsg{GrabFocus:false}.
	m.Update(msgs.FocusMsg{GrabFocus: false})

	// Now the tabselector fires again: '2' switches to the instances tab.
	m.HandleKey(uv.KeyPressEvent{Code: '2', Text: "2"})
	assert.Equal(t, "instances", m.tabs.GetActiveTab().GetTitle(),
		"tab must switch after FocusMsg{GrabFocus:false} restores normal routing")
}

// pressKey drives a key press through the model's public HandleKey entry (the
// runtime's key entry point). Helper for the precedence-chain test.
func pressKey(t *testing.T, m *Model, kp uv.KeyPressEvent) {
	t.Helper()
	m.HandleKey(kp)
}

// TestModel_HandleKey_PrecedenceChain pins the load-bearing key dispatch
// precedence after the R2 cutover (alwaysHandle -> overlay -> isFocusTaken ->
// tabselector -> regular -> active component via tabs.HandleKey). It asserts
// observable effects, not internal calls, driving keys through the public
// Update entry the runtime uses.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_HandleKey_PrecedenceChain(t *testing.T) {
	t.Run("ctrl+c does not quit the model: it is the clear key", func(t *testing.T) {
		// ctrl+c is Keys.Clear now, and quitting is the runtime's double-press
		// gesture (runtime.trackCtrlC), so no model path may short-circuit it
		// into an exit — that would clear nothing and quit on the first press.
		m, fp := newSizedModelWithPoster(t)
		pressKey(t, m, uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl})
		requireNoQuitPosted(t, fp, "ctrl+c must not quit in default flow")

		mFocus, fpFocus := newSizedModelWithPoster(t)
		mFocus.isFocusTaken = true
		pressKey(t, mFocus, uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl})
		requireNoQuitPosted(t, fpFocus, "ctrl+c must not quit when focus is taken")

		mOverlay, fpOverlay := newSizedModelWithPoster(t)
		pressKey(t, mOverlay, uv.KeyPressEvent{Code: '?', Text: "?"})
		require.NotNil(t, mOverlay.overlay, "setup: '?' must open the help overlay")
		pressKey(t, mOverlay, uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl})
		requireNoQuitPosted(t, fpOverlay, "ctrl+c must not quit with an overlay open")
	})

	t.Run("ctrl+c reaches the focused component and clears its input", func(t *testing.T) {
		m := newSizedModel(t)

		// 'tab' (Next) focuses the query editor's textarea; a typed rune then
		// lands in the shared query state.
		pressKey(t, m, uv.KeyPressEvent{Code: uv.KeyTab})
		m.isFocusTaken = true
		pressKey(t, m, uv.KeyPressEvent{Code: 'x', Text: "x"})
		require.Contains(t, m.bundle.State.Query(), "x", "setup: the typed rune must reach the editor")

		pressKey(t, m, uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl})
		assert.Empty(t, m.bundle.State.Query(),
			"ctrl+c must reach the focused editor as Keys.Clear and empty the query")
	})

	t.Run("with overlay open, a non-dismiss key routes to the overlay (not the tabselector)", func(t *testing.T) {
		m := newSizedModel(t)

		// '?' opens the help overlay.
		pressKey(t, m, uv.KeyPressEvent{Code: '?', Text: "?"})
		require.NotNil(t, m.overlay, "setup: '?' must open the help overlay")
		_, isHelp := m.overlay.(*helpoverlay.Model)
		require.True(t, isHelp, "setup: overlay must be the help overlay")
		require.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
			"setup: active tab is query before the overlay key")

		// Pressing '2' (a tabselector digit) while the overlay is open must be
		// swallowed by the overlay — the tab must NOT switch and the overlay
		// must remain open. Without overlay precedence, '2' would select the
		// instances tab.
		pressKey(t, m, uv.KeyPressEvent{Code: '2', Text: "2"})
		assert.NotNil(t, m.overlay, "overlay must stay open; the digit is consumed by the overlay")
		assert.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
			"tab must NOT switch while the overlay swallows the digit")
	})

	t.Run("with isFocusTaken, a tabselector digit does NOT switch tabs (reaches active component)", func(t *testing.T) {
		m := newSizedModel(t)
		m.isFocusTaken = true
		require.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
			"setup: active tab is query")

		// '2' would normally select the instances tab via the tabselector, but
		// isFocusTaken routes the key straight to the active component, which
		// does not switch tabs.
		pressKey(t, m, uv.KeyPressEvent{Code: '2', Text: "2"})
		assert.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
			"tab must NOT switch when focus is taken; the digit reaches the active component")
	})

	t.Run("default flow: tabselector digit switches tab", func(t *testing.T) {
		m := newSizedModel(t)
		require.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(), "setup: active tab is query")

		pressKey(t, m, uv.KeyPressEvent{Code: '2', Text: "2"})
		assert.Equal(t, "instances", m.tabs.GetActiveTab().GetTitle(),
			"digit '2' must switch to the instances tab in default flow")
	})

	t.Run("default flow: help key opens the overlay", func(t *testing.T) {
		m := newSizedModel(t)
		require.Nil(t, m.overlay, "setup: no overlay initially")

		pressKey(t, m, uv.KeyPressEvent{Code: '?', Text: "?"})
		assert.NotNil(t, m.overlay, "'?' (regular Help) must open the overlay")
	})

	t.Run("default flow: a key reaches the active component via tabs.HandleKey", func(t *testing.T) {
		m := newSizedModel(t)
		before := m.bundle.State.Query()

		// 'tab' (Next) is not bound in tabselector or the root's regular handler
		// (TabLeft/TabRight are '<'/'>'), so it falls through to the active
		// component (queryeditor), which focuses its textarea on Next.
		pressKey(t, m, uv.KeyPressEvent{Code: uv.KeyTab})

		// Type a printable rune; with focus taken it routes to the active
		// component, whose focused textarea writes the rune into the shared
		// query state — observable proof the key reached the active component.
		m.isFocusTaken = true
		pressKey(t, m, uv.KeyPressEvent{Code: 'x', Text: "x"})
		assert.NotEqual(t, before, m.bundle.State.Query(),
			"a typed rune must reach the active queryeditor and mutate the query state")
		assert.Contains(t, m.bundle.State.Query(), "x",
			"the typed rune 'x' must appear in the query state")
	})
}

// newSizedModel builds a fresh root model sized via a WindowSizeMsg so the
// tabs/components have rects before the precedence-chain test drives keys.
// A FakePoster is injected so that any list.Focus/Unfocus calls (e.g. via the
// helpoverlay) do not panic on a nil poster.
func newSizedModel(t *testing.T) *Model {
	t.Helper()
	m, _ := newSizedModelWithPoster(t)
	return m
}

// newSizedModelWithPoster builds a sized Model and returns it alongside the
// FakePoster wired into it, so tests can assert events posted off-loop (e.g.
// the QuitMsg from the ForceQuit keybind).
func newSizedModelWithPoster(t *testing.T) (*Model, *msgstest.FakePoster) {
	t.Helper()
	m, err := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, err)
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	t.Cleanup(func() { _ = m.Close() })

	m.OnResize(120, 40)
	return m, fp
}

// quitPosted reports whether msgs.QuitMsg{} was posted off-loop via the poster
// (the quit hook posts off-loop after R5a B2a).
func quitPosted(fp *msgstest.FakePoster) bool {
	for _, ev := range fp.Posted {
		if _, ok := ev.(msgs.QuitMsg); ok {
			return true
		}
	}
	return false
}

// requireNoQuitPosted asserts that no msgs.QuitMsg{} was posted off-loop.
func requireNoQuitPosted(t *testing.T, fp *msgstest.FakePoster, msgAndArgs string) {
	t.Helper()
	assert.False(t, quitPosted(fp), msgAndArgs+" (no msgs.QuitMsg may be posted)")
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) + ui.New writes package-level keys.HelpKeyStyle
func TestUpdate_RoutesWatchTickMsg(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, err := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, err)
	m.SetPoster(&msgstest.FakePoster{}) // threads to the picker
	t.Cleanup(func() { _ = m.instances.Close() })

	m.instances.GetInstances().SetAll(false) // zero enabled → no auth on startFetch
	m.instances.ToggleWatch()
	require.True(t, m.instances.IsWatching(), "watch started (epoch is now 1)")

	m.Update(instancepicker.WatchTickMsg{Epoch: 1}) // first-start epoch

	assert.False(t, m.instances.IsWatching(),
		"a WatchTickMsg must reach OnWatchTick (a valid tick with no Watching members ends the watch); "+
			"if it stays true, Update dropped the message via its default arm")
}

// TestModel_LoadSnapshot_OffLoad_PostsErrorOnBadPath pins R5a B1b: loadSnapshot
// runs its container IO off-loop via the poster (returning nil) and posts a
// SnapshotRestoreMsg carrying the error when the path cannot be opened.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_LoadSnapshot_OffLoad_PostsErrorOnBadPath(t *testing.T) {
	m, err := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })

	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)

	// A nonexistent path makes snapshot.OpenContainer fail, so the off-loop body
	// posts SnapshotRestoreMsg{Err}. FakePoster.Go runs the body inline.
	m.loadSnapshot(filepath.Join(t.TempDir(), "does-not-exist.lognav"))

	require.Len(t, fp.Posted, 1, "loadSnapshot must post exactly one SnapshotRestoreMsg")
	restore, ok := fp.Posted[0].(msgs.SnapshotRestoreMsg)
	require.True(t, ok, "posted message must be a SnapshotRestoreMsg")
	assert.Error(t, restore.Err, "SnapshotRestoreMsg must carry the open error")
}

// TestModel_LoadSnapshot_RejectsInvalidStateCRNs verifies the receiver-open
// path validates state before it can reach Instances.RestoreSnapshots, whose
// CRN map would otherwise silently collapse duplicate entries.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_LoadSnapshot_RejectsInvalidStateCRNs(t *testing.T) {
	const crn = "crn:v1:bluemix:public:logs:us-south:a/123:inst-1::"
	tests := []struct {
		name      string
		instances []snapshot.InstanceSnapshot
		wantErr   string
	}{
		{
			name:      "malformed CRN",
			instances: []snapshot.InstanceSnapshot{{CRN: "not-a-crn"}},
			wantErr:   "invalid snapshot instance CRN",
		},
		{
			name:      "duplicate CRN",
			instances: []snapshot.InstanceSnapshot{{CRN: crn}, {CRN: crn}},
			wantErr:   "duplicate snapshot instance CRN",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.lognav")
			c := snapshottest.NewContainerFile(t, path)
			require.NoError(t, snapshot.SaveStateFrame(c, snapshot.Snapshot{
				InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: tc.instances},
			}))
			require.NoError(t, c.Close())

			m, err := New(t.Context(), depstest.NewTest(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = m.Close() })
			fp := &msgstest.FakePoster{}
			m.SetPoster(fp)

			m.loadSnapshot(path)

			require.Len(t, fp.Posted, 1, "receiver-open must post exactly one restore result")
			restore, ok := fp.Posted[0].(msgs.SnapshotRestoreMsg)
			require.True(t, ok, "posted message must be a SnapshotRestoreMsg")
			require.ErrorContains(t, restore.Err, tc.wantErr)
			assert.Equal(t, snapshot.Snapshot{}, restore.Snapshot, "invalid state must not be restored")
			assert.Empty(t, restore.BackingPath, "invalid state must not install a backing path")
		})
	}
}

// TestModel_ManualSaveSnapshot_PostsDialogOffLoop pins R5a B1b: the
// ManualSaveSnapshotMsg arm builds the "Save Snapshot" filename dialog and posts
// it off-loop via the poster, returning nil from handleSnapshot (no fetch in
// progress in a fresh model).
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_ManualSaveSnapshot_PostsDialogOffLoop(t *testing.T) {
	m, err := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })

	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)

	m.handleSnapshot(msgs.ManualSaveSnapshotMsg{})

	require.Len(t, fp.Posted, 1, "the save-dialog ShowDialogMsg must be posted off-loop")
	dlg, ok := fp.Posted[0].(msgs.ShowDialogMsg)
	require.True(t, ok, "posted message must be a ShowDialogMsg")
	assert.Equal(t, "Save Snapshot", dlg.Title, "dialog must be the save-snapshot dialog")
	assert.NotNil(t, dlg.Input, "save dialog must carry a filename Input")
}

// TestModel_DoKeybindAction_ReleasesFocusOffLoop pins R5a B1b: doKeybindAction
// dismisses the overlay and posts ReleaseFocus (FocusMsg{GrabFocus:false})
// off-loop via the poster.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_DoKeybindAction_ReleasesFocusOffLoop(t *testing.T) {
	m := newSizedModel(t)
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)

	// Open the help overlay so doKeybindAction has a focused entry to resolve.
	pressKey(t, m, uv.KeyPressEvent{Code: '?', Text: "?"})
	require.NotNil(t, m.overlay, "setup: '?' must open the help overlay")

	// Drop any posts made during overlay open (e.g. FocusMsg from Focus()).
	fp.Posted = nil

	m.doKeybindAction()
	assert.Nil(t, m.overlay, "doKeybindAction must dismiss the overlay")

	var releaseFound bool
	for _, ev := range fp.Posted {
		if fm, ok := ev.(msgs.FocusMsg); ok && !fm.GrabFocus {
			releaseFound = true
		}
	}
	assert.True(t, releaseFound, "doKeybindAction must post ReleaseFocus off-loop")
}

func TestHandleOverlay_ShowContextMenu_OpensCentered(t *testing.T) {
	t.Parallel()
	m, _ := newSizedModelWithPoster(t)

	m.Update(msgs.ShowContextMenuMsg{Items: []msgs.ContextMenuItem{
		{Label: "copy log", Action: func() {}},
	}})

	_, ok := m.overlay.(*contextmenu.Model)
	require.True(t, ok, "overlay must be a *contextmenu.Model")
}

func TestDismissOverlay_ContextMenu_ClearsOverlayAndReleasesFocus(t *testing.T) {
	t.Parallel()
	m, fp := newSizedModelWithPoster(t)
	m.Update(msgs.ShowContextMenuMsg{Items: []msgs.ContextMenuItem{
		{Label: "copy log", Action: func() {}},
	}})
	require.NotNil(t, m.overlay, "setup: menu open")
	var grabbed bool
	for _, ev := range fp.Posted {
		if fm, ok := ev.(msgs.FocusMsg); ok && fm.GrabFocus {
			grabbed = true
		}
	}
	assert.True(t, grabbed, "open must grab the focus gate")
	fp.Posted = nil // ignore the open-time FocusMsg

	m.dismissOverlay()

	assert.Nil(t, m.overlay, "close must clear the overlay slot")
	assert.True(t, releaseFocusPosted(fp), "close must release the focus gate (no leak to next tab)")
}

func TestValidateSaveFilename(t *testing.T) {
	t.Parallel()
	got, err := validateSaveFilename("myfav")
	require.NoError(t, err)
	require.Equal(t, "myfav.lognav", got)

	_, err = validateSaveFilename("latest")
	require.Error(t, err)
	_, err = validateSaveFilename("latest.lognav")
	require.Error(t, err)
}

// TestModel_GlobalContextMenuKeybind_RegisteredAsGlobal verifies the ContextMenu
// key is registered once in the root regular handler under the Global category.
func TestModel_GlobalContextMenuKeybind_RegisteredAsGlobal(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)

	var found bool
	for _, b := range m.GetKeybinds() {
		if b.Context == "open context menu for the current selection" {
			found = true
			assert.Equal(t, keys.CatGlobal, b.Category)
		}
	}
	assert.True(t, found, "global context-menu keybind must be registered")
}

// TestModel_QuitKeybind_IsListedAsATwoPressGestureWithNoAction pins what the
// help overlay tells the user about quitting, and the reason that entry is
// inert. ctrl+c is Keys.Clear; the exit is two presses counted by the runtime
// loop. If this binding ever regained an action it would fire on the FIRST
// press — quitting instead of clearing — so the missing action is the behavior
// under test, not an oversight.
func TestModel_QuitKeybind_IsListedAsATwoPressGestureWithNoAction(t *testing.T) {
	t.Parallel()
	m, err := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })

	var found bool
	for _, b := range m.GetKeybinds() {
		if b.Context == "exit program" {
			found = true
			assert.Equal(t, []string{"ctrl+c ctrl+c"}, b.GetKeys(),
				"help must spell out that one ctrl+c is not enough to exit")
			assert.Nil(t, b.Action, "the exit entry is display-only; an action would quit on the first press")
		}
	}
	assert.True(t, found, "the help overlay must still document how to exit")
}

// TestModel_OpenActiveContextMenu_DispatchesToOpener verifies the dispatch helper
// routes to the active component when it implements ContextMenuOpener and is a
// safe no-op otherwise. The Logs tab's logviewer is a ContextMenuOpener; with no
// logs loaded the call is a no-op (no panic, no post).
func TestModel_OpenActiveContextMenu_DispatchesToOpener(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)

	m.tabs.Select(2) // Logs tab (logviewer implements ContextMenuOpener)
	_, ok := m.tabs.GetActiveComponent().(component.ContextMenuOpener)
	require.True(t, ok, "active Logs component must implement ContextMenuOpener")

	m.openActiveContextMenu() // no logs → no-op
	assert.Empty(t, fp.Posted, "no-op opener must not post")
}

// TestModel_ContextMenuItem_OpensDialogWithoutClobbering pins the end of the bug
// where selecting a rename/delete context-menu item did nothing: the item's
// Action posts ShowDialogMsg while the menu's own close was ALSO in flight, so
// the two could land in either order and the close could clobber the dialog.
//
// The close is now synchronous, so it always lands first and the dialog is
// installed by a later dispatch into an empty slot. This drives the real
// sequence: activate the item, then deliver what its Action posted.
func TestModel_ContextMenuItem_OpensDialogWithoutClobbering(t *testing.T) {
	t.Parallel()
	m, fp := newSizedModelWithPoster(t)

	m.Update(msgs.ShowContextMenuMsg{Items: []msgs.ContextMenuItem{{
		Label: "rename",
		Action: func() {
			// Real menu items post off-loop, exactly like this.
			fp.Go(func(ctx context.Context) {
				_ = fp.PostCritical(ctx, msgs.ShowDialogMsg{
					Title:   "Rename",
					Buttons: []msgs.DialogButton{{Label: "OK"}, {Label: "Cancel"}},
				})
			})
		},
	}}})
	require.NotNil(t, m.overlay, "setup: context menu must be open")
	fp.Posted = nil

	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter}) // activate "rename"

	assert.Nil(t, m.overlay, "the menu's own close must clear the slot synchronously")

	// Now the loop delivers what the Action posted.
	for _, ev := range fp.Posted {
		m.Update(ev)
	}
	_, isDialog := m.overlay.(*dialog.Model)
	assert.True(t, isDialog, "the item's dialog must survive the menu close")
}

// releaseFocusPosted reports whether a FocusMsg{GrabFocus:false} (the release)
// is among the poster's recorded events.
func releaseFocusPosted(fp *msgstest.FakePoster) bool {
	for _, ev := range fp.Posted {
		if fm, ok := ev.(msgs.FocusMsg); ok && !fm.GrabFocus {
			return true
		}
	}
	return false
}

// TestModel_CloseContextMenu_ClearsSlotAndObeysReleaseFocus pins the root half
// of the close seam: the slot is cleared unconditionally, and the focus grab the
// menu took on open is released exactly when the menu asks for it. Which of the
// two the menu asks for (suppress applies to an item activation only, never to a
// bare Esc/click-outside dismiss — the `c`→Esc→`c` bug where isFocusTaken stayed
// stuck true) is pinned in the contextmenu package's own seam test.
func TestModel_CloseContextMenu_ClearsSlotAndObeysReleaseFocus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		releaseFocus bool
	}{
		{name: "release requested", releaseFocus: true},
		{name: "release suppressed", releaseFocus: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, fp := newSizedModelWithPoster(t)

			m.Update(msgs.ShowContextMenuMsg{Items: []msgs.ContextMenuItem{{Label: "x"}}})
			require.NotNil(t, m.overlay, "context menu must be the overlay after ShowContextMenuMsg")
			// Drop the open's focus-grab so only the close's posts are inspected.
			fp.Posted = nil

			m.closeContextMenu(tt.releaseFocus)

			assert.Nil(t, m.overlay, "closing the menu must clear the overlay slot")
			assert.Equal(t, tt.releaseFocus, releaseFocusPosted(fp),
				"FocusMsg{GrabFocus:false} posted = %v, want %v", releaseFocusPosted(fp), tt.releaseFocus)
		})
	}
}
