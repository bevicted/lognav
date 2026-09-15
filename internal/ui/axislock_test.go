package ui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAxisLock_Allow(t *testing.T) {
	t.Parallel()

	base := time.Unix(100, 0)

	type step struct {
		vert     bool
		offsetMs int
		want     bool
	}
	tests := []struct {
		name  string
		gap   time.Duration
		steps []step
	}{
		{
			name: "pure vertical scroll all dispatch",
			gap:  150 * time.Millisecond,
			steps: []step{
				{vert: true, offsetMs: 0, want: true},
				{vert: true, offsetMs: 10, want: true},
				{vert: true, offsetMs: 20, want: true},
			},
		},
		{
			name: "stray leading sideways notch dropped, vertical proceeds",
			gap:  150 * time.Millisecond,
			steps: []step{
				{vert: false, offsetMs: 0, want: false}, // score -1, still vertical-intended -> drop
				{vert: true, offsetMs: 10, want: true},
				{vert: true, offsetMs: 20, want: true},
			},
		},
		{
			name: "two sideways notches lock horizontal, single vertical escapes",
			gap:  150 * time.Millisecond,
			steps: []step{
				{vert: false, offsetMs: 0, want: false}, // -1, vertical-intended -> drop
				{vert: false, offsetMs: 10, want: true}, // -2, horizontal locks -> dispatch
				{vert: true, offsetMs: 20, want: true},  // -1, back to vertical -> dispatch (escape)
				{vert: true, offsetMs: 30, want: true},
			},
		},
		{
			name: "intentional horizontal: first dropped then locks and holds",
			gap:  150 * time.Millisecond,
			steps: []step{
				{vert: false, offsetMs: 0, want: false}, // -1 -> drop
				{vert: false, offsetMs: 10, want: true}, // -2 -> lock, dispatch
				{vert: false, offsetMs: 20, want: true}, // floored at -2 -> dispatch
				{vert: false, offsetMs: 30, want: true},
			},
		},
		{
			name: "stray sideways during firm vertical lock is dropped",
			gap:  150 * time.Millisecond,
			steps: []step{
				{vert: true, offsetMs: 0, want: true},    // 1
				{vert: true, offsetMs: 10, want: true},   // 2
				{vert: true, offsetMs: 20, want: true},   // 3 (depth cap)
				{vert: false, offsetMs: 30, want: false}, // 2, vertical-intended -> drop
				{vert: true, offsetMs: 40, want: true},   // 3 -> dispatch
			},
		},
		{
			name: "vertical escapes horizontal lock at floor even while sideways events continue",
			gap:  150 * time.Millisecond,
			steps: []step{
				{vert: false, offsetMs: 0, want: false}, // -1 drop
				{vert: false, offsetMs: 10, want: true}, // -2 lock floor
				{vert: false, offsetMs: 20, want: true}, // -2 floor (momentum)
				{vert: true, offsetMs: 30, want: true},  // -1 -> vertical dispatched (escape mid-momentum)
				{vert: false, offsetMs: 40, want: true}, // -2 floor -> horizontal-intended, horiz event dispatched
			},
		},
		{
			name: "inactivity gap resets the tally",
			gap:  150 * time.Millisecond,
			steps: []step{
				{vert: false, offsetMs: 0, want: false},   // -1 drop
				{vert: false, offsetMs: 10, want: true},   // -2 horizontal lock
				{vert: false, offsetMs: 300, want: false}, // >150ms pause -> reset 0, then -1, vertical-intended -> drop
				{vert: false, offsetMs: 310, want: true},  // -2 -> locks again
			},
		},
		{
			name: "gap zero disables locking, mixed axes all pass",
			gap:  0,
			steps: []step{
				{vert: true, offsetMs: 0, want: true},
				{vert: false, offsetMs: 1, want: true},
				{vert: true, offsetMs: 2, want: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := axisLock{gap: tt.gap}
			for i, s := range tt.steps {
				got := a.allow(s.vert, base.Add(time.Duration(s.offsetMs)*time.Millisecond))
				assert.Equalf(t, s.want, got, "step %d (vert=%v, +%dms)", i, s.vert, s.offsetMs)
			}
		})
	}
}
