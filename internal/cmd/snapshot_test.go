package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/snapshot/snapshottest"
)

// runSnapshot executes `lognav snapshot <args...>` against an isolated XDG data
// dir and returns stdout, stderr, and the (classified) error.
func runSnapshot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs(append([]string{"snapshot"}, args...))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	err := renderExit(io.Discard, nil, root.Execute())
	return stdout.String(), stderr.String(), err
}

// seedSnapshot writes a real snapshot file with the given name + state to the
// isolated snapshot dir and returns its path.
func seedSnapshot(t *testing.T, name string, snap snapshot.Snapshot, logs map[string][]icl.Log) string {
	t.Helper()
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	p := filepath.Join(dir, name)
	writeContainerAt(t, p, snap, logs)
	return p
}

// writeSnapshotAt writes a valid, minimally populated snapshot container at an
// arbitrary path (outside the snapshot dir) for adopt sources.
func writeSnapshotAt(t *testing.T, path string) {
	t.Helper()
	writeContainerAt(t, path, snapshot.Snapshot{Query: "source logs"}, map[string][]icl.Log{"a": {{}}})
}

// writeContainerAt writes a snapshot container at path: one log frame per
// instance (in name order, so the frame order is deterministic) and the state
// frame last, the way the TUI's incremental save does.
func writeContainerAt(t *testing.T, path string, snap snapshot.Snapshot, logs map[string][]icl.Log) {
	t.Helper()
	// These older test fixtures use compact names as keys. Normalize them here
	// so every produced test container exercises the CRN-only layout.
	remappedLogs := make(map[string][]icl.Log, len(logs))
	for i := range snap.InstancePickerSnapshot.Instances {
		inst := &snap.InstancePickerSnapshot.Instances[i]
		if strings.HasPrefix(inst.CRN, "crn:") {
			continue
		}
		old := inst.CRN
		inst.CRN = "crn:v1:bluemix:public:logs:us-south:a/test:" + old + "::"
		remappedLogs[inst.CRN] = logs[old]
	}
	for name, entries := range logs {
		if _, ok := remappedLogs[name]; !ok {
			remappedLogs[name] = entries
		}
	}
	c := snapshottest.NewContainerFile(t, path)
	sizes := make(map[string]uint64, len(remappedLogs))
	for _, crn := range slices.Sorted(maps.Keys(remappedLogs)) {
		size, err := snapshot.SaveInstanceFrameWithSize(c, crn, remappedLogs[crn])
		require.NoError(t, err)
		sizes[crn] = size
	}
	for i := range snap.InstancePickerSnapshot.Instances {
		inst := &snap.InstancePickerSnapshot.Instances[i]
		if inst.LogCount != 0 && inst.LogsSizeBytes == 0 {
			inst.LogsSizeBytes = sizes[inst.CRN]
			if inst.LogsSizeBytes == 0 {
				inst.LogsSizeBytes = 1
			}
		}
	}
	require.NoError(t, snapshot.SaveStateFrame(c, snap))
	require.NoError(t, c.Close())
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshot_Help_ListsSubcommands(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	out, _, err := runSnapshot(t, "--help")
	require.NoError(t, err)
	for _, sub := range []string{"list", "inspect", "logs", "rm", "prune", "path"} {
		assert.Contains(t, out, sub)
	}
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotList_TextAndJSON(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	// empty dir: header only, no prose (docker-ps idiom).
	out, _, err := runSnapshot(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "IN USE")
	assert.NotContains(t, out, "No snapshots found")

	// one automatic snapshot
	seedSnapshot(t, "auto-20260814-120000.lognav", snapshot.Snapshot{
		Query: "source logs\n| filter x == 1",
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
			{CRN: "alpha", LogCount: 7},
		}},
	}, map[string][]icl.Log{"alpha": {{}}})

	out, _, err = runSnapshot(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "auto-20260814-120000")
	assert.Contains(t, out, "auto")
	assert.NotContains(t, out, "*")
	assert.NotContains(t, out, "temp")
	// The text table omits the query — use `inspect` for it.
	assert.NotContains(t, out, "filter x == 1")
	assert.NotContains(t, out, "source logs")

	// json: array, stable keys; query carries the FULL (multi-line) value.
	jout, _, err := runSnapshot(t, "list", "-o", "json")
	require.NoError(t, err)
	var arr []map[string]any
	require.NoError(t, json.Unmarshal([]byte(jout), &arr))
	require.Len(t, arr, 1)
	assert.Equal(t, "auto-20260814-120000", arr[0]["name"])
	assert.Equal(t, "auto", arr[0]["kind"])
	assert.NotContains(t, arr[0], "temp")
	assert.EqualValues(t, 7, arr[0]["logs"])
	assert.Equal(t, "source logs\n| filter x == 1", arr[0]["query"])
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotList_EmptyJSON_IsArrayNotNull(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	jout, _, err := runSnapshot(t, "list", "-o", "json")
	require.NoError(t, err)
	assert.Equal(t, "[]", strings.TrimSpace(jout))
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestRunSnapshotInspect_TextJSONAndFailures(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	instanceCRN := config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/test:alpha::")
	seedSnapshot(t, "auto-20260814-120000.lognav", snapshot.Snapshot{
		Query: "source logs",
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
			{CRN: instanceCRN.String(), LogCount: 7, State: 6, StartTimeMicro: 0, LastUpdateTimeMicro: 1_000_000},
		}},
	}, map[string][]icl.Log{instanceCRN.String(): {{Data: map[string]any{"message": "sized"}}}})

	var text bytes.Buffer
	err := runSnapshotInspect(&text, nil, "auto-20260814-120000", outputText)
	require.NoError(t, err)
	assert.Contains(t, text.String(), "auto-20260814-120000")
	assert.Contains(t, text.String(), "Kind:      auto")
	assert.NotContains(t, text.String(), "temp")
	assert.Contains(t, text.String(), "source logs")
	assert.Contains(t, text.String(), "us-south/alpha")
	assert.Contains(t, text.String(), "SIZE")
	assert.Contains(t, text.String(), "Total logs: 7")

	cfg := config.New()
	cfg.ICL.Instances = []config.ICLInstanceConfig{{Name: "current-name", CRN: instanceCRN}}
	var jsonOut bytes.Buffer
	err = runSnapshotInspect(&jsonOut, cfg, "latest", outputJSON)
	require.NoError(t, err)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(jsonOut.Bytes(), &obj))
	assert.Equal(t, "auto-20260814-120000", obj["name"])
	assert.NotContains(t, obj, "temp")
	assert.EqualValues(t, 7, obj["total_logs"])
	instances, ok := obj["instances"].([]any)
	require.True(t, ok)
	require.Len(t, instances, 1)
	instance, ok := instances[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "current-name", instance["name"])
	assert.Positive(t, instance["logs_size_bytes"])
	assert.Equal(t, instance["logs_size_bytes"], obj["total_logs_size_bytes"])

	err = runSnapshotInspect(io.Discard, cfg, "ghost", outputText)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitNoInput, ee.Code)

	err = runSnapshotInspect(queryErrWriter{}, cfg, "auto-20260814-120000", outputJSON)
	require.Error(t, err)
	err = runSnapshotInspect(queryErrWriter{}, cfg, "auto-20260814-120000", outputText)
	require.Error(t, err)

	invalid := filepath.Join(t.TempDir(), "invalid.lognav")
	require.NoError(t, os.WriteFile(invalid, []byte("not a snapshot"), 0o600))
	err = runSnapshotInspect(io.Discard, cfg, invalid, outputText)
	require.Error(t, err)
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotLogs(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	twoInst := map[string][]icl.Log{
		"alpha": {{Metadata: icl.Metadata{ID: "a1", Severity: icl.SeverityError, TSMicro: 1_000_000}, Data: map[string]any{"msg": "boom"}}},
		"beta":  {{Metadata: icl.Metadata{ID: "b1"}}},
	}
	seedSnapshot(t, "m_multi.lognav", snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
			{CRN: "alpha", LogCount: 1}, {CRN: "beta", LogCount: 1},
		}},
	}, twoInst)

	// ambiguous (>1 non-empty instance, none chosen) -> ExitUsage
	_, _, err := runSnapshot(t, "logs", "m_multi")
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitUsage, ee.Code)

	// chosen instance: default output is NDJSON of the log's ICL-native Data,
	// one record per line, with no timestamp/severity prefix and no Log wrapper.
	jout, _, err := runSnapshot(t, "logs", "m_multi", "--instance", "us-south/alpha")
	require.NoError(t, err)
	if want := "{\"msg\":\"boom\"}\n"; jout != want {
		t.Errorf("snapshot logs native NDJSON bytes = %q, want %q", jout, want)
	}
	lines := strings.Split(strings.TrimSpace(jout), "\n")
	require.Len(t, lines, 1)
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &rec))
	assert.Equal(t, "boom", rec["msg"])
	// no added severity prefix and no parsed-metadata wrapper around the payload
	assert.NotContains(t, jout, "Error")
	assert.NotContains(t, jout, "metadata")

	// `-o json` is no longer a flag: NDJSON is the only behavior
	_, _, err = runSnapshot(t, "logs", "m_multi", "--instance", "us-south/alpha", "-o", "json")
	require.Error(t, err)

	// unknown instance -> ExitNoInput
	_, _, err = runSnapshot(t, "logs", "m_multi", "--instance", "ghost")
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitNoInput, ee.Code)

	// single-instance snapshot: --instance optional
	seedSnapshot(t, "m_solo.lognav", snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{{CRN: "only", LogCount: 1}}},
	}, map[string][]icl.Log{"only": {{Metadata: icl.Metadata{ID: "z"}, Data: map[string]any{"msg": "solo"}}}})
	tout, _, err := runSnapshot(t, "logs", "m_solo")
	require.NoError(t, err)
	assert.Contains(t, tout, "solo")

	// many instances but only one non-empty: --instance optional, auto-selects it.
	// Empty instances still get a log frame at save time, so this must key off the
	// non-empty set rather than the frame count.
	seedSnapshot(t, "m_sparse.lognav", snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
			{CRN: "empty1"}, {CRN: "filled", LogCount: 1}, {CRN: "empty2"},
		}},
	}, map[string][]icl.Log{
		"empty1": {},
		"filled": {{Metadata: icl.Metadata{ID: "f1", Severity: icl.SeverityError}, Data: map[string]any{"msg": "hit"}}},
		"empty2": {},
	})
	tout, _, err = runSnapshot(t, "logs", "m_sparse")
	require.NoError(t, err)
	assert.Contains(t, tout, "hit")

	// all instances empty -> ExitNoInput
	seedSnapshot(t, "m_bare.lognav", snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
			{CRN: "empty1"}, {CRN: "empty2"},
		}},
	}, map[string][]icl.Log{"empty1": {}, "empty2": {}})
	_, _, err = runSnapshot(t, "logs", "m_bare")
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitNoInput, ee.Code)
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotLogs_AmbiguousCompactNameRequiresCRN(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	first := "duplicateA"
	second := "duplicateB"
	seedSnapshot(t, "m_collision.lognav", snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
			{CRN: first, LogCount: 1}, {CRN: second, LogCount: 1},
		}},
	}, map[string][]icl.Log{
		first:  {{Data: map[string]any{"from": "first"}}},
		second: {{Data: map[string]any{"from": "second"}}},
	})

	_, stderr, err := runSnapshot(t, "logs", "m_collision", "--instance", "us-south/duplicat")
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitNoInput, ee.Code)
	assert.Empty(t, stderr)
	assert.Contains(t, err.Error(), "crn:v1:bluemix:public:logs:us-south:a/test:duplicateA::")
	assert.Contains(t, err.Error(), "crn:v1:bluemix:public:logs:us-south:a/test:duplicateB::")

	out, _, err := runSnapshot(t, "logs", "m_collision", "--instance", "crn:v1:bluemix:public:logs:us-south:a/test:duplicateB::")
	require.NoError(t, err)
	assert.Contains(t, out, `"second"`)
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotRm(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	p1 := seedSnapshot(t, "m_a.lognav", snapshot.Snapshot{}, nil)
	p2 := seedSnapshot(t, "m_b.lognav", snapshot.Snapshot{}, nil)

	// unix-rm: a miss is skipped+reported, the good arg is deleted, exit ExitNoInput
	_, errStr, err := runSnapshot(t, "rm", "m_a", "ghost")
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitNoInput, ee.Code)
	assert.NoFileExists(t, p1) // m_a IS deleted now (was preserved pre-rewrite)
	assert.Contains(t, errStr, "ghost")

	// re-seed m_a (deleted above) for the dedupe check
	p1 = seedSnapshot(t, "m_a.lognav", snapshot.Snapshot{}, nil)

	// dedupe literal duplicate tokens + delete
	out, _, err := runSnapshot(t, "rm", "m_a", "m_a", "m_b")
	require.NoError(t, err)
	assert.NoFileExists(t, p1)
	assert.NoFileExists(t, p2)
	assert.Equal(t, 1, strings.Count(out, "Deleted m_a"))
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotRm_IDPrefixAmbiguousAndDedupe(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	pA := seedSnapshot(t, "m_a.lognav", snapshot.Snapshot{ID: "abcd1111-0000-4000-8000-000000000001"}, nil)
	seedSnapshot(t, "m_b.lognav", snapshot.Snapshot{ID: "abcd2222-0000-4000-8000-000000000002"}, nil)

	// ambiguous "abcd" prefix: skipped (NOT a fan-out delete), reported, ExitNoInput
	_, errStr, err := runSnapshot(t, "rm", "abcd")
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitNoInput, ee.Code)
	assert.FileExists(t, pA)
	assert.Contains(t, errStr, "ambiguous")

	// name + its own ID prefix in one batch -> resolved-path dedupe -> deleted once
	out, _, err := runSnapshot(t, "rm", "m_a", "abcd1111")
	require.NoError(t, err)
	assert.NoFileExists(t, pA)
	assert.Equal(t, 1, strings.Count(out, "Deleted m_a"))
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotPrune(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	// Stale baseline: only an orphaned dead-PID .wip. The automatic snapshot
	// and manual save must survive bare prune.
	stale := writeSnapshotFileForTest(t, ".random_999999.lognav.wip", timeNowForTest())
	autoTmp := seedSnapshot(t, "auto-20260814-120000.lognav", snapshot.Snapshot{}, nil)
	keepM := seedSnapshot(t, "m_keep.lognav", snapshot.Snapshot{}, nil)

	out, _, err := runSnapshot(t, "prune")
	require.NoError(t, err)
	assert.NoFileExists(t, stale)
	assert.FileExists(t, autoTmp, "bare prune must NOT delete automatic snapshots")
	assert.FileExists(t, keepM)
	assert.Contains(t, out, "Deleted")

	// --auto deletes the automatic snapshot; manual untouched.
	out, _, err = runSnapshot(t, "prune", "--auto")
	require.NoError(t, err)
	assert.NoFileExists(t, autoTmp)
	assert.FileExists(t, keepM)
	assert.Contains(t, out, "Deleted")

	// --manual selects manual only.
	a := seedSnapshot(t, "auto-20260814-120100-2.lognav", snapshot.Snapshot{}, nil)
	_, _, err = runSnapshot(t, "prune", "--manual")
	require.NoError(t, err)
	assert.NoFileExists(t, keepM)
	assert.FileExists(t, a)

	// dry-run deletes nothing.
	out, _, err = runSnapshot(t, "prune", "--auto", "--dry-run")
	require.NoError(t, err)
	assert.FileExists(t, a)
	assert.Contains(t, out, "Would delete")

	// bad duration -> ExitUsage
	_, _, err = runSnapshot(t, "prune", "--auto", "--older-than", "1d6h")
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitUsage, ee.Code)

	// negative keep -> ExitUsage
	_, _, err = runSnapshot(t, "prune", "--auto", "--keep", "-1")
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitUsage, ee.Code)
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotPrune_EmptyJSON_DeletedIsArrayNotNull(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	jout, _, err := runSnapshot(t, "prune", "-o", "json")
	require.NoError(t, err)

	// Raw shape: "deleted" is a non-null array.
	assert.Contains(t, jout, `"deleted": []`)
	assert.NotContains(t, jout, `"deleted": null`)

	// Structural: present array, zero count, not a dry run.
	var obj struct {
		Deleted []string `json:"deleted"`
		Count   int      `json:"count"`
		DryRun  bool     `json:"dry_run"`
	}
	require.NoError(t, json.Unmarshal([]byte(jout), &obj))
	assert.NotNil(t, obj.Deleted)
	assert.Empty(t, obj.Deleted)
	assert.Equal(t, 0, obj.Count)
	assert.False(t, obj.DryRun)
}

func timeNowForTest() time.Time { return time.Now().Add(-time.Hour) }

// writeSnapshotFileForTest creates a small file with the given name in the
// isolated snapshot dir, stamps its mtime, and returns its path. Used to seed
// stale (.wip/.tmp) files that Save cannot produce.
func writeSnapshotFileForTest(t *testing.T, name string, mod time.Time) string {
	t.Helper()
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	require.NoError(t, os.Chtimes(p, mod, mod))
	return p
}

//nolint:paralleltest // t.Setenv + pidAlive override; cannot parallelize
func TestSnapshotRm_SkipsInUseDeletesRest(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	free := seedSnapshot(t, "m_free.lognav", snapshot.Snapshot{}, nil)
	held := seedSnapshot(t, "m_held.lognav", snapshot.Snapshot{}, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "9000.inuse"), []byte("m_held.lognav\n"), 0o600))
	restore := snapshot.SetPidAliveForTest(func(pid int) bool { return pid == 9000 })
	t.Cleanup(restore)

	cmd := newSnapshotRm()
	cmd.SetArgs([]string{"m_free", "m_held"})
	cmd.SetOut(io.Discard)
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	execErr := cmd.Execute()

	assert.FileExists(t, held)
	_, statErr := os.Stat(free)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	assert.Equal(t, ExitUnavailable, ExitCode(execErr))
	assert.Contains(t, errBuf.String(), "skipped m_held")
	assert.Contains(t, errBuf.String(), "9000") // holder pid, comma-joined (no brackets)
}

//nolint:paralleltest // t.Setenv + pidAlive override; cannot parallelize
func TestSnapshotPrune_SkipsInUse(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	heldAuto := seedSnapshot(t, "auto-20260814-120000.lognav", snapshot.Snapshot{}, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "9000.inuse"), []byte("auto-20260814-120000.lognav\n"), 0o600))
	restore := snapshot.SetPidAliveForTest(func(pid int) bool { return pid == 9000 })
	t.Cleanup(restore)

	cmd := newSnapshotPrune()
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())

	assert.FileExists(t, heldAuto)
}

func TestParseOlderThan(t *testing.T) {
	t.Parallel()
	ok := map[string]time.Duration{"72h": 72 * time.Hour, "7d": 168 * time.Hour, "90m": 90 * time.Minute}
	for in, want := range ok {
		got, err := parseOlderThan(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"0", "-1h", "1d6h", "abc", "-3d"} {
		_, err := parseOlderThan(bad)
		require.Error(t, err, bad)
	}
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotPath(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)

	// no args -> dir
	out, _, err := runSnapshot(t, "path")
	require.NoError(t, err)
	assert.Equal(t, dir, strings.TrimSpace(out))

	// name + latest
	p := seedSnapshot(t, "m_one.lognav", snapshot.Snapshot{}, nil)
	out, _, err = runSnapshot(t, "path", "m_one")
	require.NoError(t, err)
	assert.Equal(t, p, strings.TrimSpace(out))

	out, _, err = runSnapshot(t, "path", "latest")
	require.NoError(t, err)
	assert.Equal(t, p, strings.TrimSpace(out))

	// too many args -> ExitUsage (cobra validator)
	_, _, err = runSnapshot(t, "path", "a", "b")
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitUsage, ee.Code)
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
//nolint:paralleltest // t.Setenv + pidAlive override; cannot parallelize
func TestSnapshotList_ReportsInUse(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	seedSnapshot(t, "m_held.lognav", snapshot.Snapshot{}, nil)
	seedSnapshot(t, "m_free.lognav", snapshot.Snapshot{}, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "9000.inuse"), []byte("m_held.lognav\n"), 0o600))
	restore := snapshot.SetPidAliveForTest(func(pid int) bool { return pid == 9000 })
	t.Cleanup(restore)

	cmd := newSnapshotList()
	cmd.SetArgs([]string{"-o", "json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.Execute())

	var rows []listRow
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rows))
	byName := map[string]listRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	assert.True(t, byName["m_held"].InUse)
	assert.Equal(t, []int{9000}, byName["m_held"].InUseBy)
	assert.False(t, byName["m_free"].InUse)
	assert.Empty(t, byName["m_free"].InUseBy) // [] not null
}

//nolint:paralleltest // t.Setenv + pidAlive override; cannot parallelize
func TestSnapshotInspect_ReportsInUse(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	seedSnapshot(t, "m_held.lognav", snapshot.Snapshot{}, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "9000.inuse"), []byte("m_held.lognav\n"), 0o600))
	restore := snapshot.SetPidAliveForTest(func(pid int) bool { return pid == 9000 })
	t.Cleanup(restore)

	cmd := newSnapshotInspect()
	cmd.SetArgs([]string{"m_held", "-o", "json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.Execute())

	var got inspectJSON
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, []int{9000}, got.InUseBy)

	// text output contains the in-use line
	cmd2 := newSnapshotInspect()
	cmd2.SetArgs([]string{"m_held"})
	var textBuf bytes.Buffer
	cmd2.SetOut(&textBuf)
	require.NoError(t, cmd2.Execute())
	assert.Contains(t, textBuf.String(), "In use by: 9000")
}

func TestCompleteSnapshotNames(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	seedSnapshot(t, "m_one.lognav", snapshot.Snapshot{}, nil)

	got, directive := completeSnapshotNames(nil, nil, "")
	assert.Contains(t, got, "m_one")
	assert.Contains(t, got, "latest")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestCompleteInstanceNames(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	seedSnapshot(t, "m_one.lognav", snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{{CRN: "alpha"}}},
	}, map[string][]icl.Log{"alpha": {{}}})

	got, directive := completeInstanceNames(newSnapshotLogs(), []string{"m_one"}, "")
	assert.Contains(t, got, "us-south/alpha")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	// no snapshot arg -> no completions, never errors
	got, _ = completeInstanceNames(newSnapshotLogs(), nil, "")
	assert.Empty(t, got)
}

// runAdopt executes the adopt command standalone against the isolated XDG dir
// and returns stdout, stderr, and the (exit-classified) error.
func runAdopt(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := newSnapshotAdopt()
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_MoveAndCopy(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	srcDir := t.TempDir()

	// move (default)
	mv := filepath.Join(srcDir, "x.lognav")
	writeSnapshotAt(t, mv)
	out, _, execErr := runAdopt(t, mv)
	require.NoError(t, execErr)
	assert.Equal(t, ExitOK, ExitCode(execErr))
	assert.Contains(t, out, "Adopted x.lognav")
	assert.FileExists(t, filepath.Join(dir, "x.lognav"))
	_, statErr := os.Stat(mv)
	require.ErrorIs(t, statErr, os.ErrNotExist)

	// copy (-c)
	cp := filepath.Join(srcDir, "y.lognav")
	writeSnapshotAt(t, cp)
	srcBytes, err := os.ReadFile(cp) // #nosec G304 -- test-only path
	require.NoError(t, err)
	_, _, execErr = runAdopt(t, "-c", cp)
	require.NoError(t, execErr)
	assert.FileExists(t, cp) // source preserved
	cpDest := filepath.Join(dir, "y.lognav")
	assert.FileExists(t, cpDest)
	// the adopted copy is byte-identical to the source
	destBytes, err := os.ReadFile(cpDest) // #nosec G304 -- test-only path
	require.NoError(t, err)
	assert.NotEmpty(t, srcBytes)
	assert.Equal(t, srcBytes, destBytes)
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_AppendsExtension(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "backup") // no .lognav extension
	writeSnapshotAt(t, src)

	out, _, execErr := runAdopt(t, src)
	require.NoError(t, execErr)
	assert.Contains(t, out, "Adopted backup.lognav")
	assert.FileExists(t, filepath.Join(dir, "backup.lognav"))
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_MissingAndForce(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	missing := filepath.Join(t.TempDir(), "nope.lognav")

	_, errOut, execErr := runAdopt(t, missing)
	assert.Equal(t, ExitNoInput, ExitCode(execErr))
	assert.Contains(t, errOut, "does not exist")

	// --force silently ignores a missing path (exit 0)
	_, _, execErr = runAdopt(t, "-f", missing)
	require.NoError(t, execErr)
	assert.Equal(t, ExitOK, ExitCode(execErr))
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_BadMagic(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	srcDir := t.TempDir()
	bad := filepath.Join(srcDir, "bad.lognav")
	require.NoError(t, os.WriteFile(bad, []byte("not a snapshot at all"), 0o600))

	_, errOut, execErr := runAdopt(t, bad)
	assert.Equal(t, ExitNoInput, ExitCode(execErr))
	assert.Contains(t, errOut, "not a lognav snapshot")
	assert.NoFileExists(t, filepath.Join(dir, "bad.lognav"))

	// --force skips the magic check and adopts anyway
	_, _, execErr = runAdopt(t, "-f", bad)
	require.NoError(t, execErr)
	assert.FileExists(t, filepath.Join(dir, "bad.lognav"))
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_DirectoryRejected(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	adir := filepath.Join(t.TempDir(), "adir.lognav")
	require.NoError(t, os.MkdirAll(adir, 0o750))

	_, errOut, execErr := runAdopt(t, adir)
	assert.Equal(t, ExitNoInput, ExitCode(execErr))
	assert.Contains(t, errOut, "is a directory")
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_WipRejected(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	srcDir := t.TempDir()
	wip := filepath.Join(srcDir, "a_x_1.lognav.wip")
	writeSnapshotAt(t, wip) // valid magic, but a .wip backing file

	_, errOut, execErr := runAdopt(t, wip)
	assert.Equal(t, ExitNoInput, ExitCode(execErr))
	assert.Contains(t, errOut, ".wip")
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_Collision(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	seedSnapshot(t, "x.lognav", snapshot.Snapshot{}, nil)
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "x.lognav")
	writeSnapshotAt(t, src)

	_, errOut, execErr := runAdopt(t, src)
	assert.Equal(t, ExitGeneral, ExitCode(execErr))
	assert.Contains(t, errOut, "already exists")
	assert.FileExists(t, src) // move did not happen

	// --force overwrites: capture the source bytes first (the move consumes it),
	// then assert the destination was actually replaced with them.
	srcBytes, err := os.ReadFile(src) // #nosec G304 -- test-only path
	require.NoError(t, err)
	require.NotEmpty(t, srcBytes)
	_, _, execErr = runAdopt(t, "-f", src)
	require.NoError(t, execErr)
	dest := filepath.Join(dir, "x.lognav")
	assert.FileExists(t, dest)
	destBytes, err := os.ReadFile(dest) // #nosec G304 -- test-only path
	require.NoError(t, err)
	assert.Equal(t, srcBytes, destBytes) // OLD seeded content was replaced
	_, statErr := os.Stat(src)
	assert.ErrorIs(t, statErr, os.ErrNotExist) // moved
}

//nolint:paralleltest // t.Setenv + pidAlive override; cannot parallelize
func TestSnapshotAdopt_InUseNeverOverwritten(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	seedSnapshot(t, "x.lognav", snapshot.Snapshot{}, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "9000.inuse"), []byte("x.lognav\n"), 0o600))
	restore := snapshot.SetPidAliveForTest(func(pid int) bool { return pid == 9000 })
	t.Cleanup(restore)
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "x.lognav")
	writeSnapshotAt(t, src)

	// even --force must not overwrite an in-use destination
	_, errOut, execErr := runAdopt(t, "-f", src)
	assert.Equal(t, ExitUnavailable, ExitCode(execErr))
	assert.Contains(t, errOut, "in use")
	assert.FileExists(t, src) // source untouched
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_SrcEqualsDestNoOp(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	seedSnapshot(t, "x.lognav", snapshot.Snapshot{}, nil)
	inDir := filepath.Join(dir, "x.lognav")

	out, _, execErr := runAdopt(t, inDir)
	require.NoError(t, execErr)
	assert.Equal(t, ExitOK, ExitCode(execErr))
	assert.Contains(t, out, "already in the snapshot directory")
	assert.FileExists(t, inDir)
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_MixedFailuresCollapseToGeneral(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	seedSnapshot(t, "x.lognav", snapshot.Snapshot{}, nil) // forces a collision (ExitGeneral)
	srcDir := t.TempDir()
	collide := filepath.Join(srcDir, "x.lognav")
	writeSnapshotAt(t, collide)
	missing := filepath.Join(srcDir, "gone.lognav") // missing (ExitNoInput)

	_, _, execErr := runAdopt(t, collide, missing)
	assert.Equal(t, ExitGeneral, ExitCode(execErr)) // {1, 66} -> 1
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_TwoSourcesSameName(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	a := filepath.Join(t.TempDir(), "x.lognav")
	b := filepath.Join(t.TempDir(), "x.lognav")
	writeSnapshotAt(t, a)
	writeSnapshotAt(t, b)

	out, errOut, execErr := runAdopt(t, a, b)
	assert.Equal(t, ExitGeneral, ExitCode(execErr)) // first adopted, second collides
	assert.Contains(t, out, "Adopted x.lognav")
	assert.Contains(t, errOut, "already exists")
	assert.FileExists(t, filepath.Join(dir, "x.lognav"))
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_WiredIntoGroup(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	out, _, err := runSnapshot(t, "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "adopt")
}

func TestInspectShowsFullID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.lognav")
	c := snapshottest.NewContainerFile(t, path)
	require.NoError(t, snapshot.SaveStateFrame(c, snapshot.Snapshot{ID: "1234567-aaaa-bbbb", Query: "q"}))
	require.NoError(t, c.Close())

	cmd := newSnapshotInspect()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{path})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "1234567-aaaa-bbbb") // full ID
}

func TestInspectAndLogsAcceptRawPath(t *testing.T) {
	// Not parallel: exercises CLI reads against a temp snapshot file by absolute path.
	path := filepath.Join(t.TempDir(), "shared.lognav")
	writeContainerAt(t, path, snapshot.Snapshot{
		Query: "source logs last 1h",
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
			{CRN: "only", LogCount: 1},
		}},
	}, map[string][]icl.Log{"only": {{Data: map[string]any{"message": "external"}}}})

	inspect := newSnapshotInspect()
	var inspectOut bytes.Buffer
	inspect.SetOut(&inspectOut)
	inspect.SetArgs([]string{path})
	require.NoError(t, inspect.Execute())
	require.Contains(t, inspectOut.String(), "source logs last 1h")

	logs := newSnapshotLogs()
	var logsOut bytes.Buffer
	logs.SetOut(&logsOut)
	logs.SetArgs([]string{path})
	require.NoError(t, logs.Execute())
	if got, want := logsOut.String(), "{\"message\":\"external\"}\n"; got != want {
		t.Errorf("snapshot logs native NDJSON bytes = %q, want %q", got, want)
	}
}

func TestAdoptDestName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "foo.lognav", adoptDestName("foo.lognav"))
	assert.Equal(t, "foo.lognav", adoptDestName("foo"))
	assert.Equal(t, "auto-20260814-120000.lognav", adoptDestName("auto-20260814-120000.lognav"))
	assert.Equal(t, "backup.lognav", adoptDestName("backup"))
}

func TestAggregateExit(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ExitOK, aggregateExit(nil))
	assert.Equal(t, ExitOK, aggregateExit([]int{0, 0}))
	assert.Equal(t, ExitNoInput, aggregateExit([]int{ExitNoInput}))
	assert.Equal(t, ExitNoInput, aggregateExit([]int{ExitNoInput, ExitNoInput}))
	assert.Equal(t, ExitUnavailable, aggregateExit([]int{ExitUnavailable}))
	assert.Equal(t, ExitGeneral, aggregateExit([]int{ExitNoInput, ExitUnavailable}))
	assert.Equal(t, ExitGeneral, aggregateExit([]int{ExitGeneral, ExitNoInput, ExitUnavailable}))
}

func TestAtomicCopyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst.lognav")
	require.NoError(t, os.WriteFile(src, []byte("hello"), 0o600))

	require.NoError(t, atomicCopyFile(src, dst))

	got, err := os.ReadFile(dst) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
	assert.FileExists(t, src) // copy leaves the source

	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestTransferSnapshot_Move(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.lognav")
	dst := filepath.Join(dir, "dst.lognav")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600))

	require.NoError(t, transferSnapshot(src, dst, false))

	got, err := os.ReadFile(dst) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	assert.Equal(t, "payload", string(got))
	_, statErr := os.Stat(src)
	assert.ErrorIs(t, statErr, os.ErrNotExist) // move removes the source
}

func TestTransferSnapshot_Copy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.lognav")
	dst := filepath.Join(dir, "dst.lognav")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600))

	require.NoError(t, transferSnapshot(src, dst, true))

	assert.FileExists(t, src)
	assert.FileExists(t, dst)
}

func TestTransferSnapshot_OverwritesExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.lognav")
	dst := filepath.Join(dir, "dst.lognav")
	require.NoError(t, os.WriteFile(dst, []byte("OLD"), 0o600))
	require.NoError(t, os.WriteFile(src, []byte("NEW"), 0o600))

	require.NoError(t, transferSnapshot(src, dst, false))

	got, err := os.ReadFile(dst) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	assert.Equal(t, "NEW", string(got))
}

func TestAdoptNameRejectsMultipleSources(t *testing.T) {
	cmd := newSnapshotAdopt()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--name", "foo", "a.lognav", "b.lognav"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Equal(t, ExitUsage, ExitCode(err))
}

func TestAdoptNameRejectsLatest(t *testing.T) {
	cmd := newSnapshotAdopt()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--name", "latest", "a.lognav"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Equal(t, ExitUsage, ExitCode(err))
}

func TestAdoptNameRejectsUnsafeBasenames(t *testing.T) {
	for _, name := range []string{"", "   ", ".", "..", "../escape", "sub/name", `sub\\name`, "a\x00b"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			cmd := newSnapshotAdopt()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--name", name, "a.lognav"})
			err := cmd.Execute()
			require.Error(t, err)
			require.Equal(t, ExitUsage, ExitCode(err))
		})
	}
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotAdopt_NamePreservesAutomaticClassification(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		kind snapshot.Kind
	}{
		{name: "auto-20260814-120000.lognav", kind: snapshot.KindAuto},
		{name: "auto-incident.lognav", kind: snapshot.KindManual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "external.lognav")
			writeSnapshotAt(t, src)

			_, _, execErr := runAdopt(t, src, "--name", tc.name)
			require.NoError(t, execErr)

			entries, scanErr := snapshot.Scan()
			require.NoError(t, scanErr)
			for _, entry := range entries {
				if entry.Path == filepath.Join(dir, tc.name) {
					assert.Equal(t, tc.kind, entry.Kind)
					return
				}
			}
			t.Fatalf("adopted snapshot %q was not scanned", tc.name)
		})
	}
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotResolveByID(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := "9a8b7c6d-0000-4000-8000-00000000000f"
	seedSnapshot(t, "m_target.lognav", snapshot.Snapshot{ID: id, Query: "source logs"}, nil)

	out, _, err := runSnapshot(t, "inspect", id)
	require.NoError(t, err)
	assert.Contains(t, out, "m_target")

	out, _, err = runSnapshot(t, "inspect", id[:7])
	require.NoError(t, err)
	assert.Contains(t, out, "m_target")

	out, _, err = runSnapshot(t, "path", id[:8])
	require.NoError(t, err)
	assert.Contains(t, out, "m_target.lognav")
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestSnapshotLogsByID(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := "7f7f7f7f-0000-4000-8000-000000000011"
	seedSnapshot(t, "m_logs.lognav",
		snapshot.Snapshot{ID: id, InstancePickerSnapshot: snapshot.InstancePickerSnapshot{
			Instances: []snapshot.InstanceSnapshot{{CRN: "only", LogCount: 1}},
		}},
		map[string][]icl.Log{"only": {{Data: map[string]any{"msg": "hello"}}}})
	out, _, err := runSnapshot(t, "logs", id[:7])
	require.NoError(t, err)
	assert.Contains(t, out, "hello")
}

//nolint:paralleltest // t.Setenv (XDG) + clip package-var stubs
func TestSnapshotClipByID(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := "5c5c5c5c-0000-4000-8000-000000000010"
	seedSnapshot(t, "m_clip.lognav", snapshot.Snapshot{ID: id}, nil)

	var copied string
	restoreWrite, restorePlat := clipboardWrite, clipUsesFileObject
	clipboardWrite = func(s string) error { copied = s; return nil }
	clipUsesFileObject = func() bool { return false } // force the text path on all OSes
	t.Cleanup(func() { clipboardWrite = restoreWrite; clipUsesFileObject = restorePlat })

	_, _, err := runSnapshot(t, "clip", id[:7])
	require.NoError(t, err)
	assert.Contains(t, copied, "m_clip.lognav")
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestAdoptYesSkipsPromptAndRenames(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "external.lognav")
	c := snapshottest.NewContainerFile(t, src)
	require.NoError(t, snapshot.SaveStateFrame(c, snapshot.Snapshot{Query: "q"}))
	require.NoError(t, c.Close())

	cmd := newSnapshotAdopt()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--name", "myfav", "-y", "-c", src})
	require.NoError(t, cmd.Execute())

	_, rerr := snapshot.Resolve("myfav")
	require.NoError(t, rerr, "adopted snapshot should be reachable as myfav")
}

func TestFormatAmbiguousCapsCandidates(t *testing.T) {
	t.Parallel()
	matches := make([]snapshot.IDMatch, 12)
	for i := range matches {
		matches[i] = snapshot.IDMatch{
			Entry: snapshot.Entry{Name: fmt.Sprintf("m_%02d", i)},
			ID:    fmt.Sprintf("abcd%04d-0000-4000-8000-000000000000", i),
		}
	}
	got := formatAmbiguous(&snapshot.AmbiguousIDError{Prefix: "abcd", Matches: matches})
	assert.Contains(t, got, "ambiguous (12 matches)")
	assert.Contains(t, got, "…")
	assert.Contains(t, got, "use more characters")
	assert.Equal(t, maxAmbiguousCandidates, strings.Count(got, "m_")) // only 10 candidates shown
}
