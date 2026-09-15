package timeline

import (
	"iter"

	"github.com/bevicted/lognav/internal/icl"
)

// LogMeta is the minimal log metadata needed for bucketing.
// Defined here so the logviewer package can implement IterLogMeta
// without the timeline package having to depend on logviewer.
type LogMeta struct {
	Idx      int
	TSMicro  int64
	Severity icl.Severity
}

// Store is the minimal log data source the timeline needs.
// *logviewer.LogStore satisfies this interface structurally, avoiding
// an import cycle (logviewer imports timeline, not the other way around).
type Store interface {
	GetTimeRange() (earliest, latest int64)
	GetLogCount() int
	IterLogMeta() iter.Seq[LogMeta]
}

// bucket is one row in the timeline view: a time range and the per-severity
// log counts that fell inside it.
type bucket struct {
	startMicro  int64  // inclusive
	endMicro    int64  // exclusive, clamped to latest+1 for the last bucket
	counts      [7]int // indexed by icl.Severity (0..6)
	firstLogIdx int    // first log index that landed in this bucket, -1 if empty
	total       int    // sum of counts, cached for rendering
}

// Build constructs a []bucket of log counts by severity across `n` uniform
// time buckets spanning [earliest, latest]. Empty stores return nil. Stores
// with all logs at identical timestamps collapse to a single bucket. When the
// requested bucket count exceeds the span in microseconds, Build falls back
// to `span+1` buckets at 1 µs each.
func Build(store Store, n int) []bucket {
	if store.GetLogCount() == 0 {
		return nil
	}
	earliest, latest := store.GetTimeRange()

	// All logs at one instant: single bucket.
	if earliest == latest {
		b := bucket{
			startMicro:  earliest,
			endMicro:    earliest + 1,
			firstLogIdx: -1,
		}
		for m := range store.IterLogMeta() {
			b.counts[m.Severity]++
			b.total++
			if b.firstLogIdx == -1 {
				b.firstLogIdx = m.Idx
			}
		}
		return []bucket{b}
	}

	span := latest - earliest
	step := span / int64(n)
	if step == 0 {
		// Span smaller than requested bucket count; collapse to 1-µs buckets.
		n = int(span) + 1
		step = 1
	}

	buckets := make([]bucket, n)
	for i := range buckets {
		start := earliest + int64(i)*step
		end := start + step
		if i == n-1 {
			end = latest + 1
		}
		buckets[i] = bucket{
			startMicro:  start,
			endMicro:    end,
			firstLogIdx: -1,
		}
	}

	for m := range store.IterLogMeta() {
		b := int((m.TSMicro - earliest) / step)
		if b >= n {
			b = n - 1
		}
		buckets[b].counts[m.Severity]++
		buckets[b].total++
		if buckets[b].firstLogIdx == -1 {
			buckets[b].firstLogIdx = m.Idx
		}
	}

	return buckets
}
