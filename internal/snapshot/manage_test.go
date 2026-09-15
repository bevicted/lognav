package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/icl"
)

// writeSnapshotFile creates a zero-content file with the given name in the
// snapshot dir and stamps its mtime. Used to drive Scan/StaleFiles tests.
func writeSnapshotFile(t *testing.T, name string, mod time.Time) string {
	t.Helper()
	dir, err := Dir()
	require.NoError(t, err)
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	require.NoError(t, os.Chtimes(p, mod, mod))
	return p
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestScan_ClassifiesAndSortsNewestFirst(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	writeSnapshotFile(t, "m_old.lognav", base)
	writeSnapshotFile(t, "auto-20260608-140000.lognav", base.Add(2*time.Hour))
	writeSnapshotFile(t, "auto-20260608-130000-2.lognav", base.Add(time.Hour))
	writeSnapshotFile(t, ".random_4821.lognav.wip", base.Add(3*time.Hour)) // excluded
	writeSnapshotFile(t, "notasnapshot.txt", base.Add(4*time.Hour))        // excluded

	got, err := Scan()
	require.NoError(t, err)
	require.Len(t, got, 3)

	// newest-first
	assert.Equal(t, "auto-20260608-140000", got[0].Name)
	assert.Equal(t, KindAuto, got[0].Kind)

	assert.Equal(t, "auto-20260608-130000-2", got[1].Name)
	assert.Equal(t, KindAuto, got[1].Kind)

	assert.Equal(t, "m_old", got[2].Name)
	assert.Equal(t, KindManual, got[2].Kind)
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestScan_OnlyCanonicalAutomaticNames(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	for _, name := range []string{
		"auto-20260814-120000.lognav",
		"auto-20260814-120000-2.lognav",
		"auto-20260814-120000-1.lognav",
		"auto-20260814-120000-02.lognav",
		"auto-20260230-120000.lognav",
		"incident.lognav",
		".random_4821.lognav.wip",
	} {
		writeSnapshotFile(t, name, base)
	}
	entries, err := Scan()
	require.NoError(t, err)
	kinds := make(map[string]Kind, len(entries))
	for _, entry := range entries {
		kinds[entry.Name] = entry.Kind
	}
	assert.Equal(t, KindAuto, kinds["auto-20260814-120000"])
	assert.Equal(t, KindAuto, kinds["auto-20260814-120000-2"])
	assert.Equal(t, KindManual, kinds["auto-20260814-120000-1"])
	assert.Equal(t, KindManual, kinds["auto-20260814-120000-02"])
	assert.Equal(t, KindManual, kinds["auto-20260230-120000"])
	assert.Equal(t, KindManual, kinds["incident"])
	assert.NotContains(t, kinds, ".random_4821.lognav")
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestRetainAutoSnapshots_MixedManagedDirectory(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	origAlive := pidAlive
	pidAlive = func(pid int) bool { return pid == 4821 }
	t.Cleanup(func() { pidAlive = origAlive })

	base := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	for i, name := range []string{
		"auto-20260814-120700.lognav",
		"auto-20260814-120600.lognav",
		"auto-20260814-120500-2.lognav",
		"manual.lognav",
		"not-automatic.lognav",
		"another-manual.lognav",
		"a_live.lognav.wip",
	} {
		writeSnapshotFile(t, name, base.Add(time.Duration(7-i)*time.Minute))
	}
	writeInUse(t, 4821, "auto-20260814-120600.lognav")

	removed, err := RetainAutoSnapshots(1)
	require.NoError(t, err)
	require.Len(t, removed, 1)
	assert.Equal(t, "auto-20260814-120500-2", removed[0].Name)

	entries, err := Scan()
	require.NoError(t, err)
	got := make([]string, 0, len(entries))
	kinds := make(map[string]Kind, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name)
		kinds[entry.Name] = entry.Kind
	}
	assert.ElementsMatch(t, []string{
		"auto-20260814-120700",
		"auto-20260814-120600",
		"manual",
		"not-automatic",
		"another-manual",
	}, got)
	assert.Equal(t, KindManual, kinds["not-automatic"], "a noncanonical name must not become automatic")
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestRetainAutoSnapshots_HonorsLimits(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, tc := range []struct {
		name     string
		limit    uint8
		wantGone []string
		wantKept []string
	}{
		{name: "zero", limit: 0, wantGone: []string{"auto-20260814-120300"}, wantKept: []string{}},
		{name: "one", limit: 1, wantGone: []string{"auto-20260814-120200", "auto-20260814-120100-2"}, wantKept: []string{"auto-20260814-120300"}},
		{name: "two", limit: 2, wantGone: []string{"auto-20260814-120100-2"}, wantKept: []string{"auto-20260814-120300", "auto-20260814-120200"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := Dir()
			require.NoError(t, err)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			for _, entry := range entries {
				require.NoError(t, os.Remove(filepath.Join(dir, entry.Name())))
			}
			for i, name := range []string{"auto-20260814-120300.lognav", "auto-20260814-120200.lognav", "auto-20260814-120100-2.lognav"} {
				writeSnapshotFile(t, name, time.Unix(int64(3-i), 0))
			}

			_, err = RetainAutoSnapshots(tc.limit)
			require.NoError(t, err)
			for _, name := range tc.wantGone {
				assert.NoFileExists(t, filepath.Join(dir, name+FileExt))
			}
			for _, name := range tc.wantKept {
				assert.FileExists(t, filepath.Join(dir, name+FileExt))
			}
		})
	}
}

//nolint:paralleltest // removeFile is a package seam; t.Setenv forbids t.Parallel
func TestRetainAutoSnapshots_ContinuesAfterDeletionError(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	writeSnapshotFile(t, "auto-20260814-120300.lognav", base.Add(2*time.Minute))
	failed := writeSnapshotFile(t, "auto-20260814-120200.lognav", base.Add(time.Minute))
	removedPath := writeSnapshotFile(t, "auto-20260814-120100-2.lognav", base)

	origRemove := removeFile
	removeFile = func(path string) error {
		if path == failed {
			return errors.New("disk error")
		}
		return os.Remove(path)
	}
	t.Cleanup(func() { removeFile = origRemove })

	removed, err := RetainAutoSnapshots(1)
	require.Error(t, err)
	require.ErrorContains(t, err, "auto-20260814-120200.lognav")
	require.Len(t, removed, 1)
	assert.Equal(t, "auto-20260814-120100-2", removed[0].Name)
	assert.FileExists(t, failed)
	assert.NoFileExists(t, removedPath)
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestSummarize_ReadsStateAndSumsLogs(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := Dir()
	require.NoError(t, err)
	p := filepath.Join(dir, "m_test.lognav")

	c := newContainerFile(t, p)
	snap := Snapshot{
		Query:  "source logs",
		Search: "err",
		JQ:     ".data",
		InstancePickerSnapshot: InstancePickerSnapshot{Instances: []InstanceSnapshot{
			{CRN: "alpha", LogCount: 10},
			{CRN: "beta", LogCount: 5},
		}},
	}
	logs := map[string][]icl.Log{"alpha": {{}, {}}, "beta": {{}}}
	saveAll(t, c, snap, logs)
	require.NoError(t, c.Close())

	got, err := Summarize(p)
	require.NoError(t, err)
	assert.Equal(t, "source logs", got.Query)
	assert.Equal(t, "err", got.Search)
	assert.Equal(t, ".data", got.JQ)
	assert.Len(t, got.Instances, 2)
	assert.Equal(t, 15, got.TotalLogs)
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestResolve(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	writeSnapshotFile(t, "m_one.lognav", base)
	writeSnapshotFile(t, "a_two_99.lognav", base.Add(time.Hour))

	tests := []struct {
		name     string
		input    string
		wantName string
		wantErr  bool
	}{
		{"bare name", "m_one", "m_one", false},
		{"with ext", "m_one.lognav", "m_one", false},
		{"latest keyword", "latest", "a_two_99", false},
		{"missing", "nope", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.input)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrSnapshotNotFound)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantName, got.Name)
		})
	}
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestResolve_LatestEmptyDir_ReturnsNotFound(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_, err := Resolve("latest")
	require.ErrorIs(t, err, ErrSnapshotNotFound)
}

func TestSummarizeCarriesID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "x.lognav")
	c := newContainerFile(t, path)
	require.NoError(t, SaveStateFrame(c, Snapshot{ID: "abc1234-rest", Query: "q"}))
	require.NoError(t, c.Close())

	s, err := Summarize(path)
	require.NoError(t, err)
	require.Equal(t, "abc1234-rest", s.ID)
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestFindByID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := Dir()
	require.NoError(t, err)
	// Write a finalized snapshot with a real state frame (so Summarize can read
	// the UUID). Reuse the NewWriter+SaveStateFrame pattern from
	// TestSummarizeCarriesID, but into Dir() so Scan() finds it.
	p := filepath.Join(dir, "m_findme.lognav")
	c := newContainerFile(t, p)
	require.NoError(t, SaveStateFrame(c, Snapshot{Query: "source logs"}))
	require.NoError(t, c.Close())

	sum, err := Summarize(p)
	require.NoError(t, err)
	require.NotEmpty(t, sum.ID)

	got, err := FindByID(sum.ID)
	require.NoError(t, err)
	assert.Equal(t, p, got.Path)

	_, err = FindByID("00000000-0000-0000-0000-000000000000")
	require.ErrorIs(t, err, ErrSnapshotNotFound)

	_, err = FindByID("")
	require.ErrorIs(t, err, ErrSnapshotNotFound)
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestResolveByID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := Dir()
	require.NoError(t, err)

	seedID := func(name, id string) string {
		p := filepath.Join(dir, name)
		c := newContainerFile(t, p)
		require.NoError(t, SaveStateFrame(c, Snapshot{ID: id, Query: "q"}))
		require.NoError(t, c.Close())
		return p
	}
	id1 := "abcd1234-0000-4000-8000-000000000001"
	id3 := "ef019999-0000-4000-8000-000000000003"
	p1 := seedID("m_one.lognav", id1)
	_ = seedID("m_two.lognav", "abcd5678-0000-4000-8000-000000000002")
	p3 := seedID("m_three.lognav", id3)
	// A junk .lognav whose state frame is unreadable -> Summarize fails -> skipped.
	writeSnapshotFile(t, "m_junk.lognav", time.Now())

	// full UUID
	got, err := ResolveByID(id1)
	require.NoError(t, err)
	assert.Equal(t, p1, got.Path)

	// unique 8-char pure-hex prefix
	got, err = ResolveByID("abcd1234")
	require.NoError(t, err)
	assert.Equal(t, p1, got.Path)

	// hyphen-spanning prefix (crosses the index-8 boundary)
	got, err = ResolveByID("abcd1234-0000")
	require.NoError(t, err)
	assert.Equal(t, p1, got.Path)

	// case-insensitive
	got, err = ResolveByID("EF019999")
	require.NoError(t, err)
	assert.Equal(t, p3, got.Path)

	// unique 4-char prefix (the floor)
	got, err = ResolveByID("ef01")
	require.NoError(t, err)
	assert.Equal(t, p3, got.Path)

	// ambiguous 4-char prefix -> AmbiguousIDError carrying both candidates+IDs
	_, err = ResolveByID("abcd")
	var amb *AmbiguousIDError
	require.ErrorAs(t, err, &amb)
	assert.Len(t, amb.Matches, 2)
	assert.NotEmpty(t, amb.Matches[0].ID)

	// below the floor -> plain not found (ID not attempted)
	_, err = ResolveByID("ef0")
	require.ErrorIs(t, err, ErrSnapshotNotFound)

	// no match -> not found
	_, err = ResolveByID("ffff")
	require.ErrorIs(t, err, ErrSnapshotNotFound)
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestResolveNameOrID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := Dir()
	require.NoError(t, err)
	seedID := func(name, id string) string {
		p := filepath.Join(dir, name)
		c := newContainerFile(t, p)
		require.NoError(t, SaveStateFrame(c, Snapshot{ID: id, Query: "q"}))
		require.NoError(t, c.Close())
		return p
	}
	p := seedID("m_named.lognav", "dddddddd-0000-4000-8000-000000000009")

	// resolves by exact name
	got, err := ResolveNameOrID("m_named")
	require.NoError(t, err)
	assert.Equal(t, p, got.Path)

	// name miss -> resolves by ID prefix
	got, err = ResolveNameOrID("dddddddd")
	require.NoError(t, err)
	assert.Equal(t, p, got.Path)

	// name beats an ID prefix that equals an existing name
	pName := seedID("dddd.lognav", "11111111-0000-4000-8000-00000000000a")
	got, err = ResolveNameOrID("dddd")
	require.NoError(t, err)
	assert.Equal(t, pName, got.Path) // the NAME "dddd", not the dddddddd... ID

	// not-found, >=4 chars -> wraps ErrSnapshotNotFound + differentiated message
	_, err = ResolveNameOrID("zzzz")
	require.ErrorIs(t, err, ErrSnapshotNotFound)
	assert.Contains(t, err.Error(), "tried name, then ID prefix")

	// not-found, <4 chars -> plain not found, ID prefix not attempted
	_, err = ResolveNameOrID("zz")
	require.ErrorIs(t, err, ErrSnapshotNotFound)
	assert.NotContains(t, err.Error(), "tried name")
}

//nolint:paralleltest // t.Setenv + pidAlive override; cannot parallelize
func TestStaleFiles(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := time.Now()

	// Pretend PID 4821 is alive, everything else dead.
	orig := pidAlive
	pidAlive = func(pid int) bool { return pid == 4821 }
	t.Cleanup(func() { pidAlive = orig })

	deadWip := writeSnapshotFile(t, ".random_111.lognav.wip", base)   // stale (dead pid)
	liveWip := writeSnapshotFile(t, ".random_4821.lognav.wip", base)  // kept (live pid)
	legacyWip := writeSnapshotFile(t, ".random_bad.lognav.wip", base) // stale (unparsable pid)
	deadAuto := writeSnapshotFile(t, "auto-20260814-120000.lognav", base)
	manual := writeSnapshotFile(t, "m_keep.lognav", base) // never stale

	got, err := StaleFiles()
	require.NoError(t, err)

	// Only orphaned (dead/malformed) .wip files are stale; finalized files are not.
	assert.ElementsMatch(t, []string{deadWip, legacyWip}, got)
	assert.NotContains(t, got, liveWip)
	assert.NotContains(t, got, deadAuto)
	assert.NotContains(t, got, manual)
}
