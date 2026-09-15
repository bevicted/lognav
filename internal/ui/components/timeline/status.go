package timeline

import "time"

// SelectedStatus is the display data for the currently selected visible bucket.
// EndUTC represents the bucket's existing exclusive end boundary.
type SelectedStatus struct {
	Position int
	Count    int
	StartUTC string
	EndUTC   string
	Total    int
}

// Status returns display data for the selected visible bucket. It returns false
// when the timeline has no valid selection, so callers cannot reuse stale data
// after Close or an invalid state transition.
func (m *Model) Status() (SelectedStatus, bool) {
	if len(m.buckets) == 0 || m.cursor < 0 || m.cursor >= len(m.visibleIndices) {
		return SelectedStatus{}, false
	}

	bucketIdx := m.visibleIndices[m.cursor]
	if bucketIdx < 0 || bucketIdx >= len(m.buckets) {
		return SelectedStatus{}, false
	}

	step := max(m.buckets[0].endMicro-m.buckets[0].startMicro, 1)
	layout := pickLabelLayout(step)
	b := m.buckets[bucketIdx]
	return SelectedStatus{
		Position: m.cursor + 1,
		Count:    len(m.visibleIndices),
		StartUTC: time.UnixMicro(b.startMicro).UTC().Format(layout),
		EndUTC:   time.UnixMicro(b.endMicro).UTC().Format(layout),
		Total:    b.total,
	}, true
}
