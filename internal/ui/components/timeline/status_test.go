package timeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatus_SelectedVisibleBucket(t *testing.T) {
	t.Parallel()

	m := &Model{
		buckets: []bucket{
			{startMicro: 0, endMicro: 1_000_000, total: 1},
			{startMicro: 1_000_000, endMicro: 2_000_000},
			{startMicro: 2_000_000, endMicro: 3_000_000, total: 7},
			{startMicro: 3_000_000, endMicro: 4_000_001, total: 3},
		},
		visibleIndices: []int{0, 2, 3},
		cursor:         2,
	}

	got, ok := m.Status()

	require.True(t, ok)
	assert.Equal(t, SelectedStatus{
		Position: 3,
		Count:    3,
		StartUTC: "00:00:03",
		EndUTC:   "00:00:04",
		Total:    3,
	}, got, "position counts visible buckets and end remains exclusive")
}

func TestStatus_UsesAdaptiveUTCPrecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		step      int64
		wantStart string
		wantEnd   string
	}{
		{
			name:      "seconds",
			step:      1_000_000,
			wantStart: "00:00:01",
			wantEnd:   "00:00:02",
		},
		{
			name:      "milliseconds",
			step:      1_000,
			wantStart: "00:00:00.001",
			wantEnd:   "00:00:00.002",
		},
		{
			name:      "microseconds",
			step:      1,
			wantStart: "00:00:00.000001",
			wantEnd:   "00:00:00.000002",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &Model{
				buckets:        []bucket{{startMicro: tt.step, endMicro: 2 * tt.step, total: 1}},
				visibleIndices: []int{0},
			}

			got, ok := m.Status()

			require.True(t, ok)
			assert.Equal(t, tt.wantStart, got.StartUTC)
			assert.Equal(t, tt.wantEnd, got.EndUTC)
		})
	}
}

func TestStatus_InvalidStateIsEmpty(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		model Model
	}{
		{name: "empty"},
		{
			name: "cursor outside visible buckets",
			model: Model{
				buckets:        []bucket{{endMicro: 1}},
				visibleIndices: []int{0},
				cursor:         1,
			},
		},
		{
			name: "visible index outside buckets",
			model: Model{
				buckets:        []bucket{{endMicro: 1}},
				visibleIndices: []int{1},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := tt.model.Status()
			assert.False(t, ok)
			assert.Equal(t, SelectedStatus{}, got)
		})
	}
}
