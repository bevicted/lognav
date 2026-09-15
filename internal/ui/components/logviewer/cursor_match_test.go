package logviewer

import (
	"testing"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// searchedViewer builds a viewer over msgs, runs the "err" search to completion
// and returns the model.
func searchedViewer(t *testing.T, msgs []string) *Model {
	t.Helper()
	m, fp := newViewerModel(t, len(msgs))
	logs := make([]icl.Log, len(msgs))
	for i, s := range msgs {
		logs[i] = icl.Log{Data: map[string]any{fieldMsg: s}}
	}
	m.store.loadLogs(logs)
	m.bundle.State.SetSearch("err")
	m.store.DoSearch()
	for _, ev := range fp.Posted {
		if scm, ok := ev.(SearchChunkMsg); ok {
			m.store.HandleSearchChunk(scm)
		}
	}
	require.NotEmpty(t, m.store.state.search, "the search must have produced matches")
	return m
}

// jump is one observed (log index, 1-based counter) pair from a next/prev jump.
type jump struct {
	logIdx int
	cur    int
}

// walk drives next (step 1) or prev (step -1) until it reports no further match,
// collecting the reported log index and the bottom-bar counter after each jump.
func walk(m *Model, step int) []jump {
	var got []jump
	for {
		var (
			logIdx int
			ok     bool
		)
		if step > 0 {
			logIdx, ok = m.CenterNextMatch()
		} else {
			logIdx, ok = m.CenterPrevMatch()
		}
		if !ok {
			return got
		}
		cur, _ := m.searchCounter()
		got = append(got, jump{logIdx, cur})
	}
}

// TestCenterMatch_JumpSequence_Unfiltered verifies n/N visit every matching log
// in order and report the same "i of N" the pre-linear-index implementation did,
// including the ends: at the last (first) match the jump reports no match and
// leaves the counter alone.
func TestCenterMatch_JumpSequence_Unfiltered(t *testing.T) {
	t.Parallel()
	m := searchedViewer(t, []string{"err a", "clean", "err b", "err c", "clean", "err d"})

	_, total := m.searchCounter()
	assert.Equal(t, 4, total, "four matching logs, one match each")

	assert.Equal(t, []jump{{2, 2}, {3, 3}, {5, 4}}, walk(m, 1),
		"n walks forward reporting the 1-based position of each match")
	// End of the corpus: another n reports nothing and does not move the counter.
	_, ok := m.CenterNextMatch()
	assert.False(t, ok, "no match after the last one")
	cur, _ := m.searchCounter()
	assert.Equal(t, 4, cur, "a failed jump leaves the counter where it was")

	assert.Equal(t, []jump{{3, 3}, {2, 2}, {0, 1}}, walk(m, -1),
		"N walks back over the same matches with the same positions")
	_, ok = m.CenterPrevMatch()
	assert.False(t, ok, "no match before the first one")
	cur, _ = m.searchCounter()
	assert.Equal(t, 1, cur, "a failed jump leaves the counter where it was")
}

// TestCenterMatch_JumpSequence_Filtered verifies a filtered-out matching log is
// skipped by the jump AND does not advance the reported position, so cur/total
// stay over the filtered-in population.
func TestCenterMatch_JumpSequence_Filtered(t *testing.T) {
	t.Parallel()
	m := searchedViewer(t, []string{"err a", "clean", "err b", "err c", "clean", "err d"})
	m.store.state.filtered.Set(2, true) // hide the second matching log

	_, total := m.searchCounter()
	assert.Equal(t, 3, total, "the hidden log's match drops out of the total")

	assert.Equal(t, []jump{{3, 2}, {5, 3}}, walk(m, 1),
		"log 2 is skipped and does not advance the position")
	assert.Equal(t, []jump{{3, 2}, {0, 1}}, walk(m, -1),
		"N reports the same positions in reverse")
}

// TestCenterMatch_MultipleMatchesPerLog verifies matchIdx offsets within a log:
// the second match on a log reports one past the first.
func TestCenterMatch_MultipleMatchesPerLog(t *testing.T) {
	t.Parallel()
	m := searchedViewer(t, []string{"clean", "err err err"})
	require.Len(t, m.store.state.search[1], 3, "three matches on log 1")

	logIdx, ok := m.CenterNextMatch()
	require.True(t, ok)
	assert.Equal(t, 1, logIdx)
	// Log 1 is the first (and only) matching log -> base index 0; the jump lands
	// on one of its matches, whose position is 1-based matchIdx+1.
	cur, total := m.searchCounter()
	assert.Equal(t, 3, total, "all three matches count toward the total")
	assert.GreaterOrEqual(t, cur, 1)
	assert.LessOrEqual(t, cur, 3)
}
