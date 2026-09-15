package timeline

import (
	"iter"
	"testing"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore is a minimal in-memory Store for testing Build() without
// constructing a real logviewer.LogStore.
type fakeStore struct {
	earliest int64
	latest   int64
	logs     []LogMeta
}

func (f *fakeStore) GetTimeRange() (int64, int64) { return f.earliest, f.latest }
func (f *fakeStore) GetLogCount() int             { return len(f.logs) }
func (f *fakeStore) IterLogMeta() iter.Seq[LogMeta] {
	return func(yield func(LogMeta) bool) {
		for _, l := range f.logs {
			if !yield(l) {
				return
			}
		}
	}
}

func newFakeStore(logs []LogMeta) *fakeStore {
	if len(logs) == 0 {
		return &fakeStore{}
	}
	earliest, latest := logs[0].TSMicro, logs[0].TSMicro
	for _, l := range logs {
		if l.TSMicro < earliest {
			earliest = l.TSMicro
		}
		if l.TSMicro > latest {
			latest = l.TSMicro
		}
	}
	return &fakeStore{earliest: earliest, latest: latest, logs: logs}
}

func TestBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		logs  []LogMeta
		n     int
		check func(t *testing.T, buckets []bucket)
	}{
		{
			name: "empty store returns nil",
			logs: nil,
			n:    60,
			check: func(t *testing.T, buckets []bucket) {
				assert.Nil(t, buckets)
			},
		},
		{
			name: "single log yields single bucket",
			logs: []LogMeta{{Idx: 0, TSMicro: 1000, Severity: icl.SeverityInfo}},
			n:    60,
			check: func(t *testing.T, buckets []bucket) {
				require.Len(t, buckets, 1)
				assert.Equal(t, 1, buckets[0].total)
				assert.Equal(t, 1, buckets[0].counts[icl.SeverityInfo])
				assert.Equal(t, 0, buckets[0].firstLogIdx)
			},
		},
		{
			name: "all logs at identical timestamps collapse to one bucket",
			logs: []LogMeta{
				{Idx: 0, TSMicro: 5000, Severity: icl.SeverityInfo},
				{Idx: 1, TSMicro: 5000, Severity: icl.SeverityError},
				{Idx: 2, TSMicro: 5000, Severity: icl.SeverityWarning},
			},
			n: 60,
			check: func(t *testing.T, buckets []bucket) {
				require.Len(t, buckets, 1)
				assert.Equal(t, 3, buckets[0].total)
				assert.Equal(t, 1, buckets[0].counts[icl.SeverityInfo])
				assert.Equal(t, 1, buckets[0].counts[icl.SeverityError])
				assert.Equal(t, 1, buckets[0].counts[icl.SeverityWarning])
				assert.Equal(t, 0, buckets[0].firstLogIdx)
			},
		},
		{
			name: "log at exact latest lands in last bucket",
			logs: []LogMeta{
				{Idx: 0, TSMicro: 0, Severity: icl.SeverityInfo},
				{Idx: 1, TSMicro: 600, Severity: icl.SeverityError}, // exactly == latest
			},
			n: 60,
			check: func(t *testing.T, buckets []bucket) {
				require.Len(t, buckets, 60)
				assert.Equal(t, 1, buckets[0].counts[icl.SeverityInfo], "first log goes to bucket 0")
				assert.Equal(t, 1, buckets[59].counts[icl.SeverityError], "latest log clamped to last bucket")
			},
		},
		{
			name: "logs distributed across buckets record firstLogIdx per bucket",
			logs: []LogMeta{
				{Idx: 0, TSMicro: 0, Severity: icl.SeverityInfo},
				{Idx: 1, TSMicro: 100, Severity: icl.SeverityInfo}, // same bucket as Idx 0
				{Idx: 2, TSMicro: 1500, Severity: icl.SeverityError},
			},
			n: 2, // span 1500, step 750
			check: func(t *testing.T, buckets []bucket) {
				require.Len(t, buckets, 2)
				assert.Equal(t, 2, buckets[0].total)
				assert.Equal(t, 0, buckets[0].firstLogIdx)
				assert.Equal(t, 1, buckets[1].total)
				assert.Equal(t, 2, buckets[1].firstLogIdx)
			},
		},
		{
			name: "step-zero fallback: span smaller than n",
			logs: []LogMeta{
				{Idx: 0, TSMicro: 100, Severity: icl.SeverityInfo},
				{Idx: 1, TSMicro: 105, Severity: icl.SeverityError},
				{Idx: 2, TSMicro: 110, Severity: icl.SeverityWarning},
			},
			n: 60, // span 10, 60 buckets requested
			check: func(t *testing.T, buckets []bucket) {
				require.Len(t, buckets, 11, "span+1 buckets at 1 µs each")
				assert.Equal(t, 1, buckets[0].counts[icl.SeverityInfo])
				assert.Equal(t, 1, buckets[5].counts[icl.SeverityError])
				assert.Equal(t, 1, buckets[10].counts[icl.SeverityWarning])
			},
		},
		{
			name: "mixed severities across contiguous timestamps",
			logs: []LogMeta{
				{Idx: 0, TSMicro: 0, Severity: icl.SeverityCritical},
				{Idx: 1, TSMicro: 30, Severity: icl.SeverityError},
				{Idx: 2, TSMicro: 60, Severity: icl.SeverityWarning},
				{Idx: 3, TSMicro: 90, Severity: icl.SeverityInfo},
				{Idx: 4, TSMicro: 120, Severity: icl.SeverityDebug},
				{Idx: 5, TSMicro: 150, Severity: icl.SeverityVerbose},
			},
			n: 3,
			check: func(t *testing.T, buckets []bucket) {
				require.Len(t, buckets, 3)
				// span 150, step 50. Buckets: [0,50), [50,100), [100,150]
				assert.Equal(t, 2, buckets[0].total)
				assert.Equal(t, 2, buckets[1].total)
				assert.Equal(t, 2, buckets[2].total)
				assert.Equal(t, 1, buckets[0].counts[icl.SeverityCritical])
				assert.Equal(t, 1, buckets[2].counts[icl.SeverityVerbose])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore(tt.logs)
			buckets := Build(store, tt.n)
			tt.check(t, buckets)
		})
	}
}
