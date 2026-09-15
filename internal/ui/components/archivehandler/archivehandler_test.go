package archivehandler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/filehandler"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// fakePoster runs Go inline (goleak-clean) and records PostCritical events.
type fakePoster struct {
	mu     sync.Mutex
	posted []uv.Event
}

func (f *fakePoster) Go(fn func(context.Context)) { fn(context.Background()) }

func (f *fakePoster) PostCritical(_ context.Context, ev uv.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, ev)
	return nil
}

func (f *fakePoster) Post(uv.Event) {}

func (f *fakePoster) events() []uv.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uv.Event(nil), f.posted...)
}

// deferredPoster captures spawned worker bodies WITHOUT running them so a test can
// assert how many goroutines were spawned and that the on-loop entry posted
// nothing (deadlock-safety). Mirrors instancepicker/instances_test.go.
type deferredPoster struct {
	spawned []func(context.Context)
	posted  []uv.Event
}

func (d *deferredPoster) Go(fn func(context.Context)) { d.spawned = append(d.spawned, fn) }

func (d *deferredPoster) PostCritical(_ context.Context, ev uv.Event) error {
	d.posted = append(d.posted, ev)
	return nil
}

func (d *deferredPoster) Post(uv.Event) {}

// newModel builds a *Model wired with a bundle + poster, without going through
// New (which would build the filehandler and resolve the dir). Tests that need the
// row style / poll logic only touch the fields those methods read.
func newModel(t *testing.T, poster msgs.Poster) *Model {
	t.Helper()
	return &Model{
		bundle: depstest.NewTest(t),
		logger: slog.Default(),
		poster: poster,
	}
}

// seedArchive writes one archive file into the (env-confined) archive dir.
func seedArchive(t *testing.T, a *archive.Archive) {
	t.Helper()
	require.NoError(t, archive.Save(a))
}

// runningInstance / etc. build instance entries in each state.
func instance(name, state string) archive.InstanceEntry {
	return archive.InstanceEntry{
		CRN:     "crn:v1:bluemix:public:logs:us-south:a/" + name + ":" + name + "::",
		QueryID: name + "-qid-123456",
		State:   state,
	}
}

// TestArchiveRowStyle_PerState asserts the row color for each aggregate archive
// state: running→InProgress, ready(success)→Success, all-errored→Error,
// expired→Error. Non-parallel: seeds files in an XDG_DATA_HOME tempdir (process-
// global env).
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestArchiveRowStyle_PerState(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newModel(t, &fakePoster{})
	style := m.bundle.Config.Style
	now := time.Now()

	cases := []struct {
		name    string
		arch    *archive.Archive
		wantFg  any
		comment string
	}{
		{
			name:    "running",
			arch:    &archive.Archive{Name: "running", SubmittedAt: now, Instances: []archive.InstanceEntry{instance("a", archive.StateRunning), instance("b", archive.StateSuccess)}},
			wantFg:  style.InProgressColor.Color,
			comment: "an in-progress (not-ready) archive is in-progress colored",
		},
		{
			name:    "ready-success",
			arch:    &archive.Archive{Name: "ready-success", SubmittedAt: now, Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess), instance("b", archive.StateError)}},
			wantFg:  style.SuccessColor.Color,
			comment: "a ready archive with at least one success is success colored (collectable)",
		},
		{
			name:    "all-errored",
			arch:    &archive.Archive{Name: "all-errored", SubmittedAt: now, Instances: []archive.InstanceEntry{instance("a", archive.StateError), instance("b", archive.StateExpired)}},
			wantFg:  style.ErrorColor.Color,
			comment: "a ready archive with no successes (nothing collectable) is error colored",
		},
		{
			name:    "expired",
			arch:    &archive.Archive{Name: "expired", SubmittedAt: now.Add(-2 * archive.TTL), Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)}},
			wantFg:  style.ErrorColor.Color,
			comment: "an archive past its TTL is error colored regardless of instance states",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedArchive(t, tc.arch)
			got := m.archiveRowStyle(tc.arch.Name + archive.FileExt)
			assert.Equal(t, tc.wantFg, got.Fg, tc.comment)
		})
	}
}

// TestArchiveRowStyle_StripsExt proves the RowStyleFn handles the FULL basename
// (incl. ext) the filehandler passes — stripping it before re-deriving the path.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestArchiveRowStyle_StripsExt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newModel(t, &fakePoster{})
	seedArchive(t, &archive.Archive{Name: "x", SubmittedAt: time.Now(), Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)}})

	withExt := m.archiveRowStyle("x" + archive.FileExt)
	assert.Equal(t, m.bundle.Config.Style.SuccessColor.Color, withExt.Fg)

	assert.Equal(t, uv.Style{}, m.archiveRowStyle("does-not-exist"+archive.FileExt),
		"an unreadable archive is unstyled")
}

// TestNewArchiveRowStyler_OneDecodeFeedsBothHooks proves the listing hook decodes
// the registry ONCE and that both the row-style and the preview hook consume that
// decode: after the styler is built, rewriting the archive on disk changes neither
// the row color nor the preview (each hook re-reading the file would have picked
// the rewrite up), while the NEXT listing does pick it up. The staleness window is
// exactly one refresh, and every path that rewrites an archive (poll-done) triggers
// a refresh.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestNewArchiveRowStyler_OneDecodeFeedsBothHooks(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newModel(t, &fakePoster{})
	seedArchive(t, &archive.Archive{
		Name: "wip", Query: "source logs", SubmittedAt: time.Now(),
		Instances: []archive.InstanceEntry{instance("ca-tor", archive.StateRunning)},
	})
	path, err := archive.PathFor("wip")
	require.NoError(t, err)

	styler := m.newArchiveRowStyler() // one archive.Scan for the whole listing
	require.Equal(t, m.bundle.Config.Style.InProgressColor.Color, styler("wip"+archive.FileExt).Fg,
		"setup: the running archive is in-progress colored")

	// The same archive advances on disk, in place, after the listing decoded it.
	seedArchive(t, &archive.Archive{
		Name: "wip", Query: "source logs", SubmittedAt: time.Now(),
		Instances: []archive.InstanceEntry{instance("ca-tor", archive.StateSuccess)},
	})

	assert.Equal(t, m.bundle.Config.Style.InProgressColor.Color, styler("wip"+archive.FileExt).Fg,
		"the row style comes from the listing's decode, not a per-row re-read")
	lines, err := m.previewFor(path)
	require.NoError(t, err)
	assert.Contains(t, flatten(lines), archive.StateRunning,
		"the preview reuses the SAME decode the row style used (no second read)")

	// The next listing rescans, so the advanced state lands on both hooks.
	styler = m.newArchiveRowStyler()
	assert.Equal(t, m.bundle.Config.Style.SuccessColor.Color, styler("wip"+archive.FileExt).Fg,
		"a new listing decodes the registry again")
	lines, err = m.previewFor(path)
	require.NoError(t, err)
	assert.Contains(t, flatten(lines), archive.StateSuccess,
		"the refreshed decode also feeds the preview")
}

// TestPreview_RendersInstanceLines verifies the preview includes the query line,
// the metadata block, and one line per instance with the state label + name.
func TestPreview_RendersInstanceLines(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	a := &archive.Archive{
		Name:        "p",
		Query:       "source logs last 7d",
		SubmittedAt: time.Unix(1_750_000_000, 0).UTC(),
		Instances: []archive.InstanceEntry{
			instance("ca-tor", archive.StateSuccess),
			instance("us-south", archive.StateError),
		},
	}

	lines, err := preview(bundle, a)
	require.NoError(t, err)
	flat := flatten(lines)

	assert.Contains(t, flat, "source", "query is highlighted into the preview")
	assert.Contains(t, flat, "ca-tor", "first instance name is rendered")
	assert.Contains(t, flat, "us-south", "second instance name is rendered")
	assert.Contains(t, flat, archive.StateSuccess, "success state label is rendered")
	assert.Contains(t, flat, archive.StateError, "error state label is rendered")
	assert.Contains(t, flat, "ca-tor-q", "queryId prefix is rendered")
}

// TestPreview_ResolvesCurrentAndFallbackNames proves archive entries persist no
// display label: preview derives a renamed configured label and permits duplicate
// compact fallback labels for distinct unconfigured CRNs.
func TestPreview_ResolvesCurrentAndFallbackNames(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	configured := instance("old-name", archive.StateSuccess)
	bundle.Config.ICL.Instances = []config.ICLInstanceConfig{{
		Name: "renamed", CRN: config.MustCRNFromString(configured.CRN),
	}}
	firstFallback := archive.InstanceEntry{
		CRN: "crn:v1:bluemix:public:logs:us-south:a/account-a:shared::", State: archive.StateSuccess,
	}
	secondFallback := archive.InstanceEntry{
		CRN: "crn:v1:bluemix:public:logs:us-south:a/account-b:shared::", State: archive.StateSuccess,
	}

	lines, err := preview(bundle, &archive.Archive{Instances: []archive.InstanceEntry{
		configured, firstFallback, secondFallback,
	}})

	require.NoError(t, err)
	flat := flatten(lines)
	assert.Contains(t, flat, "renamed")
	assert.Equal(t, 2, strings.Count(flat, "us-south/shared"))
	assert.NotContains(t, flat, configured.CRN)
}

// TestPreview_QueryIDColumnAligned asserts the queryId column starts at the same
// display offset on every instance row even when the instance names differ in
// width (e.g. "ca-tor" vs "eu-de"). The queryId is segment index 2; its column
// offset is the summed display width of the two preceding segments.
func TestPreview_QueryIDColumnAligned(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	a := &archive.Archive{
		Name:        "p",
		Query:       "source logs last 7d",
		SubmittedAt: time.Unix(1_750_000_000, 0).UTC(),
		Instances: []archive.InstanceEntry{
			instance("ca-tor", archive.StateSuccess),     // 6-wide name
			instance("eu-de", archive.StateRunning),      // 5-wide name
			instance("eu-gb-foobar", archive.StateError), // 12-wide name
		},
	}

	lines, err := preview(bundle, a)
	require.NoError(t, err)
	rows := instanceLines(t, lines, len(a.Instances))

	offsets := make([]int, len(rows))
	for i, row := range rows {
		require.GreaterOrEqual(t, len(row), 3, "row %d should have state+name+queryId segments", i)
		offsets[i] = uniseg.StringWidth(row[0].Text) + uniseg.StringWidth(row[1].Text)
	}
	for i := 1; i < len(offsets); i++ {
		assert.Equal(t, offsets[0], offsets[i],
			"queryId column offset for row %d (%q) must match row 0 (%q)",
			i, a.Instances[i].CRN, a.Instances[0].CRN)
	}
	// And the queryId segment text itself must be exactly the prefix (no leading
	// pad smuggled into the wrong segment).
	for i, row := range rows {
		assert.True(t, strings.HasSuffix(row[2].Text, queryIDPrefix(a.Instances[i].QueryID)),
			"row %d queryId segment %q ends with the queryId prefix", i, row[2].Text)
	}
}

// instanceLines returns the last n lines of a preview (the per-instance rows).
func instanceLines(t *testing.T, lines [][]list.Segment, n int) [][]list.Segment {
	t.Helper()
	require.GreaterOrEqual(t, len(lines), n, "preview must have at least %d instance rows", n)
	return lines[len(lines)-n:]
}

// flatten joins every segment of every line into one string for substring checks.
func flatten(lines [][]list.Segment) string {
	var sb []byte
	for _, line := range lines {
		for _, s := range line {
			sb = append(sb, s.Text...)
			sb = append(sb, ' ')
		}
		sb = append(sb, '\n')
	}
	return string(sb)
}

// staticResolver returns a fixed token/url for every instance.
func staticResolver(token, url string) TokenResolver {
	return func(context.Context, string) (string, string, error) { return token, url, nil }
}

// stubStatus installs a deterministic status-fetch seam returning st for every
// queryId, restoring the real seam on cleanup.
func stubStatus(t *testing.T, st icl.BackgroundState) {
	t.Helper()
	prev := pollStatusFn
	t.Cleanup(func() { pollStatusFn = prev })
	pollStatusFn = func(context.Context, string, string, string) (icl.BackgroundStatus, error) {
		return icl.BackgroundStatus{State: st}, nil
	}
}

// TestPollArchive_FlipsRunningInstance proves a running instance is advanced to
// the mapped terminal state given a stubbed status, and the archive is re-saved.
// A terminal instance is FROZEN (never re-polled / overwritten).
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestPollArchive_FlipsRunningInstance(t *testing.T) {
	cases := []struct {
		name string
		stub icl.BackgroundState
		want string
	}{
		{"success", icl.BackgroundSuccess, archive.StateSuccess},
		{"error", icl.BackgroundError, archive.StateError},
		{"notfound-expired", icl.BackgroundNotFound, archive.StateExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			stubStatus(t, tc.stub)

			f := &fakePoster{} // runs Go inline -> the off-loop worker runs here
			m := newModel(t, f)
			m.SetTokenResolver(staticResolver("tok", "https://x.invalid"))

			a := &archive.Archive{
				Name: "poll", SubmittedAt: time.Now(),
				Instances: []archive.InstanceEntry{
					instance("running-one", archive.StateRunning),
					instance("frozen-one", archive.StateSuccess), // terminal: must not change
				},
			}
			seedArchive(t, a)

			m.pollArchive("poll" + archive.FileExt)

			// Reload from disk to confirm the re-save persisted the advance.
			p, err := archive.PathFor("poll")
			require.NoError(t, err)
			got, err := archive.Load(p)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Instances[0].State, "running instance advanced to the mapped state")
			assert.Equal(t, archive.StateSuccess, got.Instances[1].State, "terminal instance is frozen")
			assert.False(t, got.Instances[0].LastPolledAt.IsZero(), "polled instance records LastPolledAt")

			// The worker posts an ArchivePollDoneMsg for the same name.
			var done bool
			for _, ev := range f.events() {
				if d, ok := ev.(ArchivePollDoneMsg); ok && d.Name == "poll"+archive.FileExt {
					done = true
				}
			}
			assert.True(t, done, "poll worker posts ArchivePollDoneMsg")
		})
	}
}

// TestPollArchive_RunningStaysRunning proves a still-running status is not a
// change: the state is untouched and (since nothing changed) the save seam is not
// invoked, but the done message is still posted.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestPollArchive_RunningStaysRunning(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	stubStatus(t, icl.BackgroundRunning)

	saved := false
	prevSave := pollSaveFn
	t.Cleanup(func() { pollSaveFn = prevSave })
	pollSaveFn = func(*archive.Archive) error { saved = true; return nil }

	f := &fakePoster{}
	m := newModel(t, f)
	m.SetTokenResolver(staticResolver("tok", "https://x.invalid"))
	seedArchive(t, &archive.Archive{Name: "still", SubmittedAt: time.Now(), Instances: []archive.InstanceEntry{instance("a", archive.StateRunning)}})

	m.pollArchive("still" + archive.FileExt)

	assert.False(t, saved, "an unchanged poll must not re-save")
}

// TestPollInstances_AdvancesAndTallies proves pollInstances advances a running
// instance on an authoritative Success status and reports the tally (1 running, 1
// advanced, 0 errors), while leaving terminal instances frozen.
//
//nolint:paralleltest // mutates the package-level pollStatusFn seam.
func TestPollInstances_AdvancesAndTallies(t *testing.T) {
	stubStatus(t, icl.BackgroundSuccess)
	a := &archive.Archive{Instances: []archive.InstanceEntry{
		instance("running-one", archive.StateRunning),
		instance("frozen-one", archive.StateSuccess),
	}}

	res := pollInstances(t.Context(), a, staticResolver("tok", "https://x.invalid"), slog.Default(), nil)

	assert.Equal(t, archive.StateSuccess, a.Instances[0].State, "running instance advanced")
	assert.Equal(t, pollResult{running: 1, advanced: 1, errors: 0}, res)
}

// TestPollInstances_UnconfiguredCRNUsesCRNResolver proves poll sends the exact
// archive CRN to its resolver even when no effective-config row exists.
//
//nolint:paralleltest // mutates the package-level pollStatusFn seam.
func TestPollInstances_UnconfiguredCRNUsesCRNResolver(t *testing.T) {
	stubStatus(t, icl.BackgroundRunning)
	entry := instance("unconfigured", archive.StateRunning)
	var gotCRN string
	resolver := func(_ context.Context, crn string) (string, string, error) {
		gotCRN = crn
		return "token", "https://unconfigured.api.us-south.logs.cloud.ibm.com", nil
	}

	res := pollInstances(t.Context(), &archive.Archive{Instances: []archive.InstanceEntry{entry}}, resolver, slog.Default(), nil)

	assert.Equal(t, entry.CRN, gotCRN)
	assert.Equal(t, pollResult{running: 1}, res)
}

// TestPollInstances_ResolverErrorIsLogged proves a resolver error is LOGGED (not
// silently dropped) and counted as an error while the instance's prior state is
// preserved. This is the Bug-4 regression: the swallowed-error path made a stuck
// poll undiagnosable.
//
//nolint:paralleltest // captures the default slog logger.
func TestPollInstances_ResolverErrorIsLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	failResolver := func(context.Context, string) (string, string, error) {
		return "", "", errors.New("boom-auth")
	}
	a := &archive.Archive{Instances: []archive.InstanceEntry{instance("ca-tor", archive.StateRunning)}}

	res := pollInstances(t.Context(), a, failResolver, logger, nil)

	assert.Equal(t, archive.StateRunning, a.Instances[0].State, "errored instance keeps its prior state")
	assert.Equal(t, pollResult{running: 1, advanced: 0, errors: 1}, res)
	out := buf.String()
	assert.Contains(t, out, "token resolve failed", "resolver error is logged distinctly")
	assert.Contains(t, out, "ca-tor", "the instance name is in the log")
	assert.Contains(t, out, "boom-auth", "the underlying error is in the log")
}

// TestPollInstances_StatusErrorIsLogged proves a status-fetch error is logged
// distinctly from a resolver error and counted, with state preserved.
//
//nolint:paralleltest // captures the default slog logger and mutates pollStatusFn.
func TestPollInstances_StatusErrorIsLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	prev := pollStatusFn
	t.Cleanup(func() { pollStatusFn = prev })
	pollStatusFn = func(context.Context, string, string, string) (icl.BackgroundStatus, error) {
		return icl.BackgroundStatus{}, errors.New("boom-status")
	}
	a := &archive.Archive{Instances: []archive.InstanceEntry{instance("eu-de", archive.StateRunning)}}

	res := pollInstances(t.Context(), a, staticResolver("tok", "https://x.invalid"), logger, nil)

	assert.Equal(t, archive.StateRunning, a.Instances[0].State, "errored instance keeps its prior state")
	assert.Equal(t, pollResult{running: 1, advanced: 0, errors: 1}, res)
	out := buf.String()
	assert.Contains(t, out, "status fetch failed", "status error is logged distinctly")
	assert.Contains(t, out, "boom-status", "the underlying error is in the log")
}

// TestPollArchive_OnLoopEntry_DoesNotPostDirectly is the deadlock-safety guard:
// pollArchive runs ON the loop goroutine (a list confirm action), so the
// unbuffered events channel would self-deadlock if it posted directly. With a
// deferredPoster (which captures the worker WITHOUT running it), the on-loop
// prologue must spawn exactly one poster.Go and post nothing.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestPollArchive_OnLoopEntry_DoesNotPostDirectly(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dp := &deferredPoster{}
	m := newModel(t, dp)
	m.SetTokenResolver(staticResolver("tok", "https://x.invalid"))
	seedArchive(t, &archive.Archive{Name: "dl", SubmittedAt: time.Now(), Instances: []archive.InstanceEntry{instance("a", archive.StateRunning)}})

	m.pollArchive("dl" + archive.FileExt)

	require.Len(t, dp.spawned, 1, "poll must spawn exactly one off-loop worker")
	assert.Empty(t, dp.posted, "the on-loop entry must NOT post directly (deadlock-safety)")
}

// TestArchiveRowStyle_BundleHasArchiveColors is a guard that the bundle's style
// exposes the three archive colors (so a config change that drops one is caught).
func TestArchiveRowStyle_BundleHasArchiveColors(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	assertNonNilColor(t, bundle, "SuccessColor", bundle.Config.Style.SuccessColor.Color)
	assertNonNilColor(t, bundle, "InProgressColor", bundle.Config.Style.InProgressColor.Color)
	assertNonNilColor(t, bundle, "ErrorColor", bundle.Config.Style.ErrorColor.Color)
}

func assertNonNilColor(t *testing.T, _ deps.Bundle, name string, c any) {
	t.Helper()
	assert.NotNil(t, c, "%s must be configured", name)
}

// drainFileIO forwards every posted filehandler.IOMsg back into the model so the
// inner list reflects the seeded directory.
func drainFileIO(m *Model, f *fakePoster) {
	for _, ev := range f.events() {
		if io, ok := ev.(filehandler.IOMsg); ok {
			m.OnFileIO(io)
		}
	}
}

// realModel builds a *Model via New (with a real filehandler) and seeds it with
// the given archives, returning the model + poster so callers can drain IO and
// inspect posted events. The cursor lands on the first listed row.
func realModel(t *testing.T, archives ...*archive.Archive) (*Model, *fakePoster) {
	t.Helper()
	f := &fakePoster{}
	m := New(context.Background(), depstest.NewTest(t))
	m.SetPoster(f)
	m.SetTokenResolver(staticResolver("tok", "https://x.invalid"))
	for _, a := range archives {
		seedArchive(t, a)
	}
	m.fh.ListFiles()
	drainFileIO(m, f)
	return m, f
}

// TestSelectArchive_LandsCursorOnNamedRow proves SelectArchive refreshes the list
// and moves the cursor onto the named archive row once the refresh populates it
// (the deferred selection). Three archives are seeded; the cursor starts on the
// newest, and SelectArchive moves it onto a specific older one by name.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestSelectArchive_LandsCursorOnNamedRow(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	// Distinct SubmittedAt so the newest-first ordering is deterministic and the
	// target row is NOT already under the cursor.
	m, f := realModel(t,
		&archive.Archive{Name: "arch-old", SubmittedAt: time.Unix(1000, 0), Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)}},
		&archive.Archive{Name: "arch-mid", SubmittedAt: time.Unix(2000, 0), Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)}},
		&archive.Archive{Name: "arch-new", SubmittedAt: time.Unix(3000, 0), Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)}},
	)
	require.Equal(t, "arch-new", m.fh.GetItemUnderCursor(), "setup: cursor starts on the newest row")

	m.SelectArchive("arch-old")
	drainFileIO(m, f) // apply the deferred-select refresh's ListOP

	assert.Equal(t, "arch-old", m.fh.GetItemUnderCursor(),
		"SelectArchive lands the cursor on the named archive row after the refresh")
}

// TestApplyStatus_SetsErrorMessage proves applyStatus records a concise reason on
// a non-success terminal (Fix C: the preview only shows a message when non-empty,
// so without this a red row gave no reason), folds cancelled→error and
// not-found→expired, and clears any prior message on success.
func TestApplyStatus_SetsErrorMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       icl.BackgroundState
		want     string
		wantMsg  string
		advanced bool
	}{
		{"error", icl.BackgroundError, archive.StateError, "query failed (or cancelled)", true},
		{"notfound-expired", icl.BackgroundNotFound, archive.StateExpired, "expired — results no longer available", true},
		{"running-no-change", icl.BackgroundRunning, archive.StateRunning, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ie := &archive.InstanceEntry{State: archive.StateRunning}
			adv := applyStatus(ie, tc.in)
			assert.Equal(t, tc.advanced, adv)
			assert.Equal(t, tc.want, ie.State)
			assert.Equal(t, tc.wantMsg, ie.ErrorMessage)
		})
	}

	t.Run("success clears a prior message", func(t *testing.T) {
		t.Parallel()
		ie := &archive.InstanceEntry{State: archive.StateRunning, ErrorMessage: "stale"}
		assert.True(t, applyStatus(ie, icl.BackgroundSuccess))
		assert.Equal(t, archive.StateSuccess, ie.State)
		assert.Empty(t, ie.ErrorMessage, "success clears any prior error message")
	})
}

// TestOnArchivePollDone_RefreshesStalePreview is the regression for "preview does
// not refresh after a poll": the filehandler caches previews by basename with no
// freshness check, and a poll rewrites the archive file in place, so without
// dropping the cached preview the RHS keeps showing the pre-poll states. This test
// renders the archive handler (LHS list + RHS preview), advances the archive on
// disk, runs OnArchivePollDone, and asserts the rendered preview now reflects the
// advanced state + its error message — proving the cache was invalidated.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestOnArchivePollDone_RefreshesStalePreview(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, f := realModel(t, &archive.Archive{
		Name: "wip", Query: "source logs", SubmittedAt: time.Now(),
		Instances: []archive.InstanceEntry{instance("ca-tor", archive.StateRunning)},
	})
	// The ListOP triggers an async preview recompute (posted during the realModel
	// drain); drain again so the cached preview is applied before the first render.
	drainFileIO(m, f)

	// Wide enough that the preview's RHS column fits the full per-instance error
	// message without truncation at the screen edge.
	rect := component.Rect{X: 0, Y: 0, W: 200, H: 16}
	m.SetRect(rect)
	require.Contains(t, render(m, rect), archive.StateRunning,
		"setup: the cached preview shows the running state")

	// A poll advanced the instance to expired (with an error message), persisted on
	// disk in place — the same name the cached preview is keyed by.
	advanced := &archive.Archive{
		Name: "wip", Query: "source logs", SubmittedAt: time.Now(),
		Instances: []archive.InstanceEntry{{
			CRN: "crn:v1:bluemix:public:logs:us-south:a/ca-tor:ca-tor::", QueryID: "ca-tor-qid-123456",
			State: archive.StateExpired, ErrorMessage: "expired — results no longer available",
		}},
	}
	require.NoError(t, archive.Save(advanced))

	m.OnArchivePollDone(ArchivePollDoneMsg{Name: "wip" + archive.FileExt})
	drainFileIO(m, f) // apply the ListOP (which posts the recomputed Preview)
	drainFileIO(m, f) // apply the recomputed Preview posted during the prior drain

	out := render(m, rect)
	assert.Contains(t, out, archive.StateExpired,
		"after a poll, the preview reflects the advanced state (cache was invalidated)")
	assert.Contains(t, out, "expired — results no longer available",
		"the advanced instance's error message is shown in the refreshed preview")
	assert.NotContains(t, out, archive.StateRunning,
		"the stale running state must no longer appear")
}

// render draws the archive handler into a fresh screen buffer of the given rect and
// returns the rendered text (LHS list + RHS preview), for asserting preview content.
func render(m *Model, rect component.Rect) string {
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)
	return canvas.Render()
}

// TestHandleKey_EnterUnfocused_RoutesToOnEnter is the regression for "Enter does
// nothing on the archive tab (only double-click worked)": pressing Enter while the
// fuzzy filter is NOT focused must reach onEnter (the collect/poll/expired router),
// not the filehandler's no-op Accept. A ready archive routes to a collect emit.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestHandleKey_EnterUnfocused_RoutesToOnEnter(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, f := realModel(t, &archive.Archive{
		Name: "ready", Query: "source logs", SubmittedAt: time.Now(),
		Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)},
	})
	require.False(t, m.fh.IsFocused(), "setup: fuzzy filter is not focused")
	require.Equal(t, "ready", m.fh.GetItemUnderCursor())

	res := m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	assert.Equal(t, component.KeyHandled, res, "Enter is consumed by the archive Accept binding")

	var collect *ArchiveCollectMsg
	for _, ev := range f.events() {
		if c, ok := ev.(ArchiveCollectMsg); ok {
			collect = &c
		}
	}
	require.NotNil(t, collect, "unfocused Enter must route to onEnter (collect emit for a ready archive)")
	assert.Equal(t, "ready", collect.Archive.Name)
}

// TestHandleKey_EnterUnfocused_InProgressPolls proves an unfocused Enter over an
// in-progress archive runs the poll path (the other onEnter branch), observable as
// a posted ArchivePollDoneMsg.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestHandleKey_EnterUnfocused_InProgressPolls(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	stubStatus(t, icl.BackgroundRunning) // poll runs but advances nothing
	m, f := realModel(t, &archive.Archive{
		Name: "wip", SubmittedAt: time.Now(),
		Instances: []archive.InstanceEntry{instance("a", archive.StateRunning)},
	})
	require.False(t, m.fh.IsFocused(), "setup: fuzzy filter is not focused")

	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})

	var polled bool
	for _, ev := range f.events() {
		if d, ok := ev.(ArchivePollDoneMsg); ok && d.Name == "wip" {
			polled = true
		}
	}
	assert.True(t, polled, "unfocused Enter on an in-progress archive runs the poll path")
}

// TestHandleKey_EnterFiresOnEnterExactlyOnce guards against a double-fire: the
// archive Accept binding (m.kh) is gated on !IsFocused(), and the focused path
// reaches onEnter via the list's confirm — so an unfocused Enter must emit exactly
// one collect trigger, not two.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestHandleKey_EnterFiresOnEnterExactlyOnce(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, f := realModel(t, &archive.Archive{
		Name: "ready", Query: "q", SubmittedAt: time.Now(),
		Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)},
	})
	require.False(t, m.fh.IsFocused())

	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})

	count := 0
	for _, ev := range f.events() {
		if _, ok := ev.(ArchiveCollectMsg); ok {
			count++
		}
	}
	assert.Equal(t, 1, count, "Enter routes to onEnter exactly once (no double-fire)")
}

// TestOnEnter_ReadyArchive_EmitsCollect proves Enter over a ready, not-expired
// archive emits an ArchiveCollectMsg carrying the archive (the root collect trigger).
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestOnEnter_ReadyArchive_EmitsCollect(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, f := realModel(t, &archive.Archive{
		Name: "ready", Query: "source logs", SubmittedAt: time.Now(),
		Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)},
	})
	// The filehandler list stores the name WITHOUT the extension; onEnter's trimExt
	// is therefore a no-op on this path (idempotent), but archiveRowStyle still
	// receives the full basename incl. ext from ListFiles.
	require.Equal(t, "ready", m.fh.GetItemUnderCursor())

	m.onEnter()

	var got *ArchiveCollectMsg
	for _, ev := range f.events() {
		if c, ok := ev.(ArchiveCollectMsg); ok {
			got = &c
		}
	}
	require.NotNil(t, got, "Enter on a ready archive must emit ArchiveCollectMsg")
	assert.Equal(t, "ready", got.Archive.Name)
}

// TestOnEnter_ExpiredArchive_ShowsNoticeNoCollect proves Enter over an expired
// archive shows a notice and does NOT emit a collect trigger.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestOnEnter_ExpiredArchive_ShowsNoticeNoCollect(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, f := realModel(t, &archive.Archive{
		Name: "old", SubmittedAt: time.Now().Add(-2 * archive.TTL),
		Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)},
	})

	m.onEnter()

	var notice bool
	for _, ev := range f.events() {
		if d, ok := ev.(msgs.ShowDialogMsg); ok && d.Title == "Archive" {
			notice = true
		}
		_, isCollect := ev.(ArchiveCollectMsg)
		assert.False(t, isCollect, "an expired archive must NOT emit a collect trigger")
	}
	assert.True(t, notice, "an expired archive shows a notice")
}

// TestHandleKey_QuitWhenNotFocused proves the Archive tab owns its own Quit
// binding: a `q` press with the fuzzy filter unfocused posts msgs.QuitMsg. This
// is the regression for "q does not quit on the archive tab" — the generic
// filehandler binds only file ops and never Quit, so without archivehandler's
// own m.kh the key was silently dropped.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestHandleKey_QuitWhenNotFocused(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, f := realModel(t)
	require.False(t, m.fh.IsFocused(), "setup: fuzzy filter is not focused")

	res := m.HandleKey(quitKey(t, m))
	assert.Equal(t, component.KeyHandled, res, "q is consumed by the Quit binding")

	var quit bool
	for _, ev := range f.events() {
		if _, ok := ev.(msgs.QuitMsg); ok {
			quit = true
		}
	}
	assert.True(t, quit, "q on the Archive tab (unfocused) must post msgs.QuitMsg")
}

// TestHandleKey_QuitSuppressedWhenFocused proves the Quit binding is guarded by
// !IsFocused(): once the fuzzy filter is focused, a `q` press is typed into the
// filter instead of quitting (mirroring snapshothandler), so it must NOT post
// msgs.QuitMsg.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestHandleKey_QuitSuppressedWhenFocused(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, f := realModel(t)
	// Focus the fuzzy filter with the Search key; m.fh.Blur drops it on cleanup
	// via the model's normal lifecycle (not needed here — test-local model).
	m.HandleKey(searchKey(t, m))
	require.True(t, m.fh.IsFocused(), "setup: Search key focuses the fuzzy filter")

	before := len(f.events())
	m.HandleKey(quitKey(t, m))

	for _, ev := range f.events()[before:] {
		_, isQuit := ev.(msgs.QuitMsg)
		assert.False(t, isQuit, "q must NOT quit while the fuzzy filter is focused (it filters)")
	}
}

// TestReleaseFocus_BlursFilter proves ReleaseFocus blurs the fuzzy filter so the
// root's mouse-driven tab switch (which clears the focus gate without posting
// FocusMsg) does not leave the list input stuck focused — which would otherwise
// make the filehandler swallow every subsequent key, including Quit, on return.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestReleaseFocus_BlursFilter(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := realModel(t)
	m.HandleKey(searchKey(t, m))
	require.True(t, m.fh.IsFocused(), "setup: filter focused")

	m.ReleaseFocus()
	assert.False(t, m.fh.IsFocused(), "ReleaseFocus blurs the fuzzy filter")
}

// quitKey / searchKey build the configured Quit / Search key press for the model.
func quitKey(t *testing.T, m *Model) uv.KeyPressEvent {
	t.Helper()
	return runeKey(t, m.bundle.Config.Keys.Quit[0])
}

func searchKey(t *testing.T, m *Model) uv.KeyPressEvent {
	t.Helper()
	return runeKey(t, m.bundle.Config.Keys.Search[0])
}

// runeKey builds a single-rune KeyPressEvent matching how the runtime delivers
// printable keys (Code = rune, Text = the rune). Only valid for single-rune
// bindings (q, /), which the defaults use.
func runeKey(t *testing.T, s string) uv.KeyPressEvent {
	t.Helper()
	r := []rune(s)
	require.Len(t, r, 1, "runeKey only supports single-rune bindings, got %q", s)
	return uv.KeyPressEvent{Code: r[0], Text: s}
}

// TestOnEnter_InProgressArchive_Polls proves Enter over an in-progress archive
// runs the poll path (a status fetch advances the running instance + posts done),
// not a collect trigger.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestOnEnter_InProgressArchive_Polls(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	stubStatus(t, icl.BackgroundSuccess)
	m, f := realModel(t, &archive.Archive{
		Name: "wip", SubmittedAt: time.Now(),
		Instances: []archive.InstanceEntry{instance("a", archive.StateRunning)},
	})

	m.onEnter()

	var polled bool
	for _, ev := range f.events() {
		// onEnter passes the cursor name (extension already stripped by the list).
		if d, ok := ev.(ArchivePollDoneMsg); ok && d.Name == "wip" {
			polled = true
		}
		_, isCollect := ev.(ArchiveCollectMsg)
		assert.False(t, isCollect, "an in-progress archive must NOT emit a collect trigger")
	}
	assert.True(t, polled, "an in-progress archive Enter runs poll (posts ArchivePollDoneMsg)")
}
