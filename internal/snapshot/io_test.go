package snapshot

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test instance name and query constants shared across io tests.
const (
	testInstProdEU   = "prod-eu"
	testInstAuSyd    = "au-syd"
	testInstEuDe     = "eu-de"
	testQuerySrcLogs = "source logs"
	testMsgHello     = "hello"
	fieldMsg         = "msg"
)

// saveAll composes the production save write path -- a SaveInstanceFrame per
// instance followed by SaveStateFrame last -- the way the instancepicker's
// incremental save does, so round-trip tests can state their fixture as one
// snapshot plus a logs-by-instance map. Instances are written in name order so
// the resulting frame order is deterministic.
func saveAll(t *testing.T, c *Container, snap Snapshot, logs map[string][]icl.Log) {
	t.Helper()
	sizes := make(map[string]uint64, len(logs))
	for _, name := range slices.Sorted(maps.Keys(logs)) {
		size, err := SaveInstanceFrameWithSize(c, name, logs[name])
		require.NoError(t, err)
		sizes[name] = size
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
	require.NoError(t, SaveStateFrame(c, snap))
}

// loadAll composes the production restore read path -- LoadState plus a
// per-instance LoadInstanceLogs over InstanceNames -- the way the live TUI
// restore does, returning the (snapshot, logs-by-instance) shape the round-trip
// tests assert on.
func loadAll(t *testing.T, c *Container) (Snapshot, map[string][]icl.Log) {
	t.Helper()
	snap, err := LoadState(c)
	require.NoError(t, err)
	logs := map[string][]icl.Log{}
	for _, name := range InstanceCRNs(c) {
		l, err := LoadInstanceLogs(c, name)
		require.NoError(t, err)
		logs[name] = l
	}
	return snap, logs
}

func TestPrepareInstanceFrame_NativeNDJSONSize(t *testing.T) {
	t.Parallel()
	logs := []icl.Log{
		{Data: map[string]any{"z": "last", "a": "first"}},
		{},
	}

	compressed, size, err := PrepareInstanceFrame(logs)
	require.NoError(t, err)
	assert.NotEmpty(t, compressed)
	assert.Equal(t, uint64(len("{\"a\":\"first\",\"z\":\"last\"}\nnull\n")), size)

	_, emptySize, err := PrepareInstanceFrame(nil)
	require.NoError(t, err)
	assert.Zero(t, emptySize)
	_, emptySize, err = PrepareInstanceFrame([]icl.Log{})
	require.NoError(t, err)
	assert.Zero(t, emptySize)
}

func TestStateLogSizeInvariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		inst InstanceSnapshot
	}{
		{name: "zero logs with bytes", inst: InstanceSnapshot{CRN: "zero", LogsSizeBytes: 1}},
		{name: "logs without bytes", inst: InstanceSnapshot{CRN: "stale", LogCount: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			assert.Error(t, SaveStateFrame(NewWriter(&buf), Snapshot{InstancePickerSnapshot: InstancePickerSnapshot{Instances: []InstanceSnapshot{tt.inst}}}))
		})
	}
}

func TestLoadState_RejectsPreSizeNonEmptyVersionOne(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	c := NewWriter(&buf)
	state, err := gzipBytes([]byte(`{"instance_picker":{"instances":[{"crn":"stale","log_count":1}]}}`))
	require.NoError(t, err)
	require.NoError(t, c.AppendFrame(frameState, state))
	require.NoError(t, c.Close())

	read, err := openBytes(t, buf.Bytes())
	require.NoError(t, err)
	defer func() { _ = read.Close() }()
	_, err = LoadState(read)
	require.ErrorContains(t, err, "non-zero logs but zero logs_size_bytes")
}

func TestSaveAndLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lognav")

	snap := Snapshot{
		Query:  "source logs | filter severity == 'error'",
		Search: "timeout",
		JQ:     ".data.message",
		InstancePickerSnapshot: InstancePickerSnapshot{
			Instances: []InstanceSnapshot{
				{
					CRN:                 "crn:v1:bluemix:public:logs:eu-de:a/123:inst-1::",
					State:               2,
					LogCount:            2,
					StartTimeMicro:      1000000,
					LastUpdateTimeMicro: 2000000,
				},
				{
					CRN:                 "crn:v1:bluemix:public:logs:us-south:a/123:inst-2::",
					State:               2,
					LogCount:            1,
					StartTimeMicro:      3000000,
					LastUpdateTimeMicro: 4000000,
				},
			},
		},
	}

	logs := map[string][]icl.Log{
		testInstProdEU: {
			{Data: map[string]any{fieldMsg: testMsgHello}, Metadata: icl.Metadata{ID: "a", TSMicro: 1000, Severity: icl.SeverityInfo}},
			{Data: map[string]any{fieldMsg: "world"}, Metadata: icl.Metadata{ID: "b", TSMicro: 2000, Severity: icl.SeverityError}},
		},
		"prod-us": {
			{Data: map[string]any{fieldMsg: "foo"}, Metadata: icl.Metadata{ID: "c", TSMicro: 3000, Severity: icl.SeverityWarning}},
		},
	}

	// Save
	c := newContainerFile(t, path)
	saveAll(t, c, snap, logs)
	require.NoError(t, c.Close())

	// Load
	c2, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()

	gotSnap, gotLogs := loadAll(t, c2)

	assert.Equal(t, snap.Query, gotSnap.Query)
	assert.Equal(t, snap.Search, gotSnap.Search)
	assert.Equal(t, snap.JQ, gotSnap.JQ)
	assert.Len(t, gotSnap.InstancePickerSnapshot.Instances, 2)
	assert.Equal(t, "crn:v1:bluemix:public:logs:eu-de:a/123:inst-1::", gotSnap.InstancePickerSnapshot.Instances[0].CRN)
	assert.Equal(t, "crn:v1:bluemix:public:logs:eu-de:a/123:inst-1::", gotSnap.InstancePickerSnapshot.Instances[0].CRN)
	assert.Equal(t, 2, gotSnap.InstancePickerSnapshot.Instances[0].LogCount)

	assert.Len(t, gotLogs[testInstProdEU], 2)
	assert.Equal(t, testMsgHello, gotLogs[testInstProdEU][0].Data[fieldMsg])
	assert.Len(t, gotLogs["prod-us"], 1)
}

func TestLoadState(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lognav")

	snap := Snapshot{
		Query: testQuerySrcLogs,
		InstancePickerSnapshot: InstancePickerSnapshot{
			Instances: []InstanceSnapshot{
				{
					CRN:                 testInstProdEU,
					LogCount:            42,
					StartTimeMicro:      1000000,
					LastUpdateTimeMicro: 1500000,
				},
			},
		},
	}

	c := newContainerFile(t, path)
	saveAll(t, c, snap, nil)
	require.NoError(t, c.Close())

	c2, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()

	got, err := LoadState(c2)
	require.NoError(t, err)
	assert.Equal(t, testQuerySrcLogs, got.Query)
	assert.Equal(t, testInstProdEU, got.InstancePickerSnapshot.Instances[0].CRN)
	assert.Equal(t, 42, got.InstancePickerSnapshot.Instances[0].LogCount)
}

// TestSaveAndLoadLargeFile exercises the production save/restore flow with
// large gzip frames: NewWriter -> file -> OpenContainerReadOnly -> Load.
func TestSaveAndLoadLargeFile(t *testing.T) {
	t.Parallel()

	const logCount = 5000
	makeLogs := func(instance string) []icl.Log {
		logs := make([]icl.Log, logCount)
		for i := range logs {
			logs[i] = icl.Log{
				Data: map[string]any{
					"message": fmt.Sprintf("log entry %d from %s with padding", i, instance),
					"nested":  map[string]any{"trace_id": fmt.Sprintf("t-%d", i)},
				},
				Metadata: icl.Metadata{
					ID:       fmt.Sprintf("%s-%d", instance, i),
					TSMicro:  int64(1000000 + i),
					Severity: icl.SeverityInfo,
				},
			}
		}
		return logs
	}

	snap := Snapshot{
		Query: testQuerySrcLogs,
		InstancePickerSnapshot: InstancePickerSnapshot{
			Instances: []InstanceSnapshot{
				{CRN: testInstAuSyd, LogCount: logCount},
				{CRN: testInstEuDe, LogCount: logCount},
			},
		},
	}
	logs := map[string][]icl.Log{
		testInstAuSyd: makeLogs(testInstAuSyd),
		testInstEuDe:  makeLogs(testInstEuDe),
	}

	// Save via NewWriter -> file (matches production save path)
	path := filepath.Join(t.TempDir(), "test.lognav")
	c := newContainerFile(t, path)
	saveAll(t, c, snap, logs)
	require.NoError(t, c.Close())

	c2, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()

	gotSnap, gotLogs := loadAll(t, c2)

	assert.Equal(t, snap.Query, gotSnap.Query)
	assert.Len(t, gotLogs[testInstAuSyd], logCount)
	assert.Len(t, gotLogs[testInstEuDe], logCount)
	assert.Equal(t, logs[testInstAuSyd][0].Data["message"], gotLogs[testInstAuSyd][0].Data["message"])
	assert.Equal(t, logs[testInstAuSyd][logCount-1].Metadata.ID, gotLogs[testInstAuSyd][logCount-1].Metadata.ID)
}

func TestSaveInstanceFrame(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	c := NewWriter(&buf)
	logs := []icl.Log{
		{Data: map[string]any{fieldMsg: testMsgHello}, Metadata: icl.Metadata{ID: "a", TSMicro: 1000}},
		{Data: map[string]any{fieldMsg: "world"}, Metadata: icl.Metadata{ID: "b", TSMicro: 2000}},
	}
	require.NoError(t, SaveInstanceFrame(c, testInstProdEU, logs))
	require.NoError(t, c.Close())
	c2, err := openBytes(t, buf.Bytes())
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()
	got, err := LoadInstanceLogs(c2, testInstProdEU)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, testMsgHello, got[0].Data[fieldMsg])
}

// TestSaveInstanceFrame_WrapsWriteFailure pins the frame-write error path: a
// failed AppendFrame surfaces from SaveInstanceFrame naming the instance.
func TestSaveInstanceFrame_WrapsWriteFailure(t *testing.T) {
	t.Parallel()
	c := NewWriter(&failingWriter{failAt: 0, err: errors.New("disk full")})
	err := SaveInstanceFrame(c, testInstProdEU, []icl.Log{{Metadata: icl.Metadata{ID: "1"}}})
	require.ErrorContains(t, err, "write log frame for "+testInstProdEU)
	require.ErrorContains(t, err, "disk full")
}

func TestLoadInstanceLogs_Missing(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	c := NewWriter(&buf)
	require.NoError(t, c.Close())
	c2, err := openBytes(t, buf.Bytes())
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()
	_, err = LoadInstanceLogs(c2, "nope")
	assert.Error(t, err)
}

func TestIncrementalSaveAndLazyLoad(t *testing.T) {
	t.Parallel()

	makeLogs := func(instance string, n int) []icl.Log {
		logs := make([]icl.Log, n)
		for i := range logs {
			logs[i] = icl.Log{
				Data:     map[string]any{fieldMsg: fmt.Sprintf("%s-%d", instance, i)},
				Metadata: icl.Metadata{ID: fmt.Sprintf("%s-%d", instance, i), TSMicro: int64(1000 + i)},
			}
		}
		return logs
	}

	// Simulate incremental fetch: write instance frames one by one, state last.
	path := filepath.Join(t.TempDir(), "test.lognav")
	f, err := os.Create(path) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	c := NewWriter(f)

	// Instance 1 finishes first.
	require.NoError(t, SaveInstanceFrame(c, testInstEuDe, makeLogs(testInstEuDe, 100)))
	// Instance 2 finishes second.
	require.NoError(t, SaveInstanceFrame(c, "us-south", makeLogs("us-south", 200)))

	// All done — write state.
	snap := Snapshot{
		Query: testQuerySrcLogs,
		InstancePickerSnapshot: InstancePickerSnapshot{
			Instances: []InstanceSnapshot{
				{CRN: testInstEuDe, LogCount: 100, LogsSizeBytes: 1},
				{CRN: "us-south", LogCount: 200, LogsSizeBytes: 1},
			},
		},
	}
	require.NoError(t, SaveStateFrame(c, snap))
	require.NoError(t, c.Close())
	require.NoError(t, f.Close())

	// Lazy load: read state only, then load individual instances on demand.
	c2, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()

	gotSnap, err := LoadState(c2)
	require.NoError(t, err)
	assert.Equal(t, testQuerySrcLogs, gotSnap.Query)
	assert.Len(t, gotSnap.InstancePickerSnapshot.Instances, 2)

	// Load only eu-de.
	euLogs, err := LoadInstanceLogs(c2, testInstEuDe)
	require.NoError(t, err)
	assert.Len(t, euLogs, 100)
	assert.Equal(t, "eu-de-0", euLogs[0].Data[fieldMsg])

	// Load only us-south.
	usLogs, err := LoadInstanceLogs(c2, "us-south")
	require.NoError(t, err)
	assert.Len(t, usLogs, 200)
}

// TestAtomicSave verifies that writing via temp file + rename produces a valid
// snapshot even when an older file at the target path already exists.
// This exercises the fix for concurrent-save corruption where two goroutines
// calling os.Create on the same path would interleave writes.
func TestAtomicSave(t *testing.T) {
	t.Parallel()

	const logCount = 2000
	makeLogs := func(instance string) []icl.Log {
		logs := make([]icl.Log, logCount)
		for i := range logs {
			logs[i] = icl.Log{
				Data: map[string]any{
					"message": fmt.Sprintf("log entry %d from %s", i, instance),
				},
				Metadata: icl.Metadata{
					ID:       fmt.Sprintf("%s-%d", instance, i),
					TSMicro:  int64(1000000 + i),
					Severity: icl.SeverityInfo,
				},
			}
		}
		return logs
	}

	snap := Snapshot{
		Query: testQuerySrcLogs,
		InstancePickerSnapshot: InstancePickerSnapshot{
			Instances: []InstanceSnapshot{
				{CRN: testInstAuSyd, LogCount: logCount},
				{CRN: testInstEuDe, LogCount: logCount},
			},
		},
	}
	logs := map[string][]icl.Log{
		testInstAuSyd: makeLogs(testInstAuSyd),
		testInstEuDe:  makeLogs(testInstEuDe),
	}

	dir := t.TempDir()
	finalPath := filepath.Join(dir, "test.lognav")

	// Write the first version (simulates an existing file at the target path).
	c1 := newContainerFile(t, finalPath)
	saveAll(t, c1, snap, logs)
	require.NoError(t, c1.Close())

	// Write a second version via temp file + rename (the fixed save path).
	tmp, err := os.CreateTemp(dir, ".save-*.tmp")
	require.NoError(t, err)
	tmpPath := tmp.Name()

	c2 := NewWriter(tmp)
	saveAll(t, c2, snap, logs)
	require.NoError(t, c2.Close())
	require.NoError(t, tmp.Close())
	require.NoError(t, os.Rename(tmpPath, finalPath))

	// Verify the final file is valid.
	c3, err := OpenContainerReadOnly(finalPath)
	require.NoError(t, err)
	defer func() { _ = c3.Close() }()

	gotSnap, gotLogs := loadAll(t, c3)

	assert.Equal(t, snap.Query, gotSnap.Query)
	assert.Len(t, gotLogs[testInstAuSyd], logCount)
	assert.Len(t, gotLogs[testInstEuDe], logCount)
}

func TestStreamInstanceLogs_DecodesInOrderAndReportsMissing(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	c := NewWriter(&buf)
	require.NoError(t, SaveInstanceFrame(c, "alpha", []icl.Log{
		{Metadata: icl.Metadata{ID: "1"}},
		{Metadata: icl.Metadata{ID: "2"}},
		{Metadata: icl.Metadata{ID: "3"}},
	}))
	require.NoError(t, c.Close())

	rc, err := openBytes(t, buf.Bytes())
	require.NoError(t, err)

	var got []string
	require.NoError(t, StreamInstanceLogs(rc, "alpha", func(l icl.Log) error {
		got = append(got, l.Metadata.ID)
		return nil
	}))
	assert.Equal(t, []string{"1", "2", "3"}, got)

	// missing instance -> ErrInstanceNotFound
	err = StreamInstanceLogs(rc, "ghost", func(icl.Log) error { return nil })
	require.ErrorIs(t, err, ErrInstanceNotFound)

	// fn error stops iteration and propagates
	sentinel := errors.New("stop")
	err = StreamInstanceLogs(rc, "alpha", func(icl.Log) error { return sentinel })
	require.ErrorIs(t, err, sentinel)

	assert.ElementsMatch(t, []string{"alpha"}, InstanceCRNs(rc))
}

func TestNonEmptyInstanceNames_FiltersByStateLogCountInFrameOrder(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	c := NewWriter(&buf)
	// Empty instances still get a log frame written at save time, so the helper
	// must distinguish them via the state frame's per-instance LogCount.
	require.NoError(t, SaveInstanceFrame(c, "empty1", []icl.Log{}))
	require.NoError(t, SaveInstanceFrame(c, "filled", []icl.Log{{Metadata: icl.Metadata{ID: "1"}}}))
	require.NoError(t, SaveInstanceFrame(c, "empty2", []icl.Log{}))
	require.NoError(t, SaveInstanceFrame(c, "filled2", []icl.Log{{Metadata: icl.Metadata{ID: "2"}}}))
	require.NoError(t, SaveStateFrame(c, Snapshot{
		InstancePickerSnapshot: InstancePickerSnapshot{Instances: []InstanceSnapshot{
			{CRN: "empty1", LogCount: 0},
			{CRN: "filled", LogCount: 1, LogsSizeBytes: 1},
			{CRN: "empty2", LogCount: 0},
			{CRN: "filled2", LogCount: 2, LogsSizeBytes: 1},
		}},
	}))
	require.NoError(t, c.Close())

	rc, err := openBytes(t, buf.Bytes())
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, err := NonEmptyInstanceCRNs(rc)
	require.NoError(t, err)
	// Equal (not ElementsMatch) pins the frame-order contract; empties dropped.
	assert.Equal(t, []string{"filled", "filled2"}, got)
}

func TestInstanceNames_PreservesContainerOrder(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	c := NewWriter(&buf)
	require.NoError(t, SaveInstanceFrame(c, "alpha", []icl.Log{{Metadata: icl.Metadata{ID: "1"}}}))
	require.NoError(t, SaveInstanceFrame(c, "beta", []icl.Log{{Metadata: icl.Metadata{ID: "2"}}}))
	require.NoError(t, c.Close())

	rc, err := openBytes(t, buf.Bytes())
	require.NoError(t, err)

	// Equal (not ElementsMatch) pins the order contract: names come back in the
	// order the frames were appended.
	assert.Equal(t, []string{"alpha", "beta"}, InstanceCRNs(rc))
}

func TestSaveAndLoad_FiltersRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "filters.lognav")

	snap := Snapshot{
		Query:  testQuerySrcLogs,
		Search: "term",
		JQ:     ".data",
		Filters: []filter.Rule{
			{Type: filter.Include, Value: "keep"},
			{Type: filter.Exclude, Value: "drop"},
			{Type: filter.HasField, Value: ".data.status"},
			{Type: filter.LacksField, Value: ".data.debug"},
		},
	}

	c := newContainerFile(t, path)
	saveAll(t, c, snap, nil)
	require.NoError(t, c.Close())

	c2, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()

	got, err := LoadState(c2)
	require.NoError(t, err)
	assert.Equal(t, snap.Filters, got.Filters)
}

func TestLoadState_OldSnapshotWithoutFilters_LoadsEmpty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "old.lognav")

	// A pre-redesign snapshot: no Filters field populated.
	snap := Snapshot{Query: testQuerySrcLogs, Search: "term", JQ: ".data"}

	c := newContainerFile(t, path)
	saveAll(t, c, snap, nil)
	require.NoError(t, c.Close())

	c2, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()

	got, err := LoadState(c2)
	require.NoError(t, err)
	assert.Empty(t, got.Filters, "a snapshot without filters must load an empty slice")
}

func TestSaveStateFrameAssignsID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "x.lognav")
	c := newContainerFile(t, path)
	require.NoError(t, SaveStateFrame(c, Snapshot{Query: "q"}))
	require.NoError(t, c.Close())

	rc, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := LoadState(rc)
	require.NoError(t, err)
	require.NotEmpty(t, got.ID, "SaveStateFrame must mint an ID")
	_, perr := uuid.Parse(got.ID)
	require.NoError(t, perr, "ID must be a valid UUID")
}

func TestSaveStateFramePreservesID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "x.lognav")
	c := newContainerFile(t, path)
	require.NoError(t, SaveStateFrame(c, Snapshot{ID: "fixed-id-123"}))
	require.NoError(t, c.Close())

	rc, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := LoadState(rc)
	require.NoError(t, err)
	require.Equal(t, "fixed-id-123", got.ID, "an existing ID must be preserved, not regenerated")
}

func TestCopyWithState_CopiesFramesVerbatimAndWritesFreshState(t *testing.T) {
	t.Parallel()
	srcPath := filepath.Join(t.TempDir(), "src.lognav")
	sf, err := os.Create(srcPath) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	src := NewWriter(sf)
	prodSize, err := SaveInstanceFrameWithSize(src, testInstProdEU, []icl.Log{{Data: map[string]any{fieldMsg: "a"}, Metadata: icl.Metadata{ID: "1"}}})
	require.NoError(t, err)
	auSize, err := SaveInstanceFrameWithSize(src, testInstAuSyd, []icl.Log{{Data: map[string]any{fieldMsg: "b"}, Metadata: icl.Metadata{ID: "2"}}})
	require.NoError(t, err)
	require.NoError(t, SaveStateFrame(src, Snapshot{Query: "old"}))
	require.NoError(t, src.Close())
	require.NoError(t, sf.Close())

	srcRO, err := OpenContainerReadOnly(srcPath)
	require.NoError(t, err)
	defer func() { _ = srcRO.Close() }()

	snap := Snapshot{
		Query: "new query",
		InstancePickerSnapshot: InstancePickerSnapshot{Instances: []InstanceSnapshot{
			{CRN: testInstProdEU, LogCount: 1, LogsSizeBytes: prodSize},
			{CRN: testInstAuSyd, LogCount: 1, LogsSizeBytes: auSize},
		}},
	}

	dstPath := filepath.Join(t.TempDir(), "dst.lognav")
	df, err := os.Create(dstPath) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	dst := NewWriter(df)
	require.NoError(t, CopyWithState(dst, srcRO, snap))
	require.NoError(t, dst.Close())
	require.NoError(t, df.Close())

	dstRO, err := OpenContainerReadOnly(dstPath)
	require.NoError(t, err)
	defer func() { _ = dstRO.Close() }()

	gotSnap, gotLogs := loadAll(t, dstRO)
	assert.Equal(t, "new query", gotSnap.Query, "state frame must be the fresh metadata, not the source's")
	assert.Len(t, gotLogs[testInstProdEU], 1)
	assert.Len(t, gotLogs[testInstAuSyd], 1)
	assert.Equal(t, prodSize, gotSnap.InstancePickerSnapshot.Instances[0].LogsSizeBytes)
	assert.Equal(t, auSize, gotSnap.InstancePickerSnapshot.Instances[1].LogsSizeBytes)

	rawSrc, err := srcRO.ReadFrame(frameInstancePrefix + testInstProdEU)
	require.NoError(t, err)
	rawDst, err := dstRO.ReadFrame(frameInstancePrefix + testInstProdEU)
	require.NoError(t, err)
	assert.Equal(t, rawSrc, rawDst, "instance frame must be copied verbatim (no re-gzip)")
}

func TestEnsureInstanceFrames_AddsOnlyMissingDeclaredFrames(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "frames.lognav")
	f, err := os.Create(path) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	c := NewWriter(f)
	require.NoError(t, SaveInstanceFrame(c, testInstProdEU, []icl.Log{{Metadata: icl.Metadata{ID: "kept"}}}))
	require.NoError(t, EnsureInstanceFrames(c, []InstanceSnapshot{
		{CRN: testInstProdEU}, {CRN: testInstEuDe},
	}))
	require.NoError(t, SaveStateFrame(c, Snapshot{InstancePickerSnapshot: InstancePickerSnapshot{Instances: []InstanceSnapshot{
		{CRN: testInstProdEU, LogCount: 1, LogsSizeBytes: 1}, {CRN: testInstEuDe},
	}}}))
	require.NoError(t, c.Close())
	require.NoError(t, f.Close())

	read, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = read.Close() }()
	assert.Equal(t, []string{testInstProdEU, testInstEuDe}, InstanceCRNs(read))
	kept, err := LoadInstanceLogs(read, testInstProdEU)
	require.NoError(t, err)
	assert.Len(t, kept, 1, "an existing log frame must not be replaced")
	empty, err := LoadInstanceLogs(read, testInstEuDe)
	require.NoError(t, err)
	assert.Empty(t, empty, "a missing declared frame must be empty")
}

func TestCopyWithState_BackfillsEmptyFrameForDeclaredMissingInstance(t *testing.T) {
	t.Parallel()
	srcPath := filepath.Join(t.TempDir(), "src.lognav")
	sf, err := os.Create(srcPath) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	src := NewWriter(sf)
	require.NoError(t, SaveInstanceFrame(src, testInstProdEU, []icl.Log{{Metadata: icl.Metadata{ID: "1"}}}))
	require.NoError(t, SaveStateFrame(src, Snapshot{}))
	require.NoError(t, src.Close())
	require.NoError(t, sf.Close())
	srcRO, err := OpenContainerReadOnly(srcPath)
	require.NoError(t, err)
	defer func() { _ = srcRO.Close() }()

	snap := Snapshot{InstancePickerSnapshot: InstancePickerSnapshot{Instances: []InstanceSnapshot{
		{CRN: testInstProdEU, LogCount: 1, LogsSizeBytes: 1},
		{CRN: testInstEuDe, LogCount: 0}, // declared but has no frame in src
	}}}

	dstPath := filepath.Join(t.TempDir(), "dst.lognav")
	df, err := os.Create(dstPath) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	dst := NewWriter(df)
	require.NoError(t, CopyWithState(dst, srcRO, snap))
	require.NoError(t, dst.Close())
	require.NoError(t, df.Close())
	dstRO, err := OpenContainerReadOnly(dstPath)
	require.NoError(t, err)
	defer func() { _ = dstRO.Close() }()

	euLogs, err := LoadInstanceLogs(dstRO, testInstEuDe)
	require.NoError(t, err, "declared-but-missing instance must still have an (empty) frame")
	assert.Empty(t, euLogs)
	assert.ElementsMatch(t, []string{testInstProdEU, testInstEuDe}, InstanceCRNs(dstRO))
}

func TestCopyWithState_SkipsOrphanSourceFrame(t *testing.T) {
	t.Parallel()
	srcPath := filepath.Join(t.TempDir(), "src.lognav")
	sf, err := os.Create(srcPath) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	src := NewWriter(sf)
	require.NoError(t, SaveInstanceFrame(src, testInstProdEU, []icl.Log{{Metadata: icl.Metadata{ID: "1"}}}))
	require.NoError(t, SaveInstanceFrame(src, testInstAuSyd, []icl.Log{{Metadata: icl.Metadata{ID: "2"}}})) // orphan
	require.NoError(t, SaveStateFrame(src, Snapshot{}))
	require.NoError(t, src.Close())
	require.NoError(t, sf.Close())
	srcRO, err := OpenContainerReadOnly(srcPath)
	require.NoError(t, err)
	defer func() { _ = srcRO.Close() }()

	snap := Snapshot{InstancePickerSnapshot: InstancePickerSnapshot{Instances: []InstanceSnapshot{
		{CRN: testInstProdEU, LogCount: 1, LogsSizeBytes: 1}, // au-syd intentionally not declared
	}}}

	dstPath := filepath.Join(t.TempDir(), "dst.lognav")
	df, err := os.Create(dstPath) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	dst := NewWriter(df)
	require.NoError(t, CopyWithState(dst, srcRO, snap))
	require.NoError(t, dst.Close())
	require.NoError(t, df.Close())
	dstRO, err := OpenContainerReadOnly(dstPath)
	require.NoError(t, err)
	defer func() { _ = dstRO.Close() }()

	assert.Equal(t, []string{testInstProdEU}, InstanceCRNs(dstRO), "orphan source frame must not be copied")
}

func TestCopyWithState_NilSrcWritesEmptyFrames(t *testing.T) {
	t.Parallel()
	snap := Snapshot{Query: "q", InstancePickerSnapshot: InstancePickerSnapshot{Instances: []InstanceSnapshot{
		{CRN: testInstProdEU}, {CRN: testInstAuSyd},
	}}}
	dstPath := filepath.Join(t.TempDir(), "dst.lognav")
	df, err := os.Create(dstPath) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	dst := NewWriter(df)
	require.NoError(t, CopyWithState(dst, nil, snap))
	require.NoError(t, dst.Close())
	require.NoError(t, df.Close())
	dstRO, err := OpenContainerReadOnly(dstPath)
	require.NoError(t, err)
	defer func() { _ = dstRO.Close() }()

	gotSnap, gotLogs := loadAll(t, dstRO)
	assert.Equal(t, "q", gotSnap.Query)
	assert.Empty(t, gotLogs[testInstProdEU])
	assert.Empty(t, gotLogs[testInstAuSyd])
	assert.ElementsMatch(t, []string{testInstProdEU, testInstAuSyd}, InstanceCRNs(dstRO))
}
