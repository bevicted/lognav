package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/instancepicker"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

// refetchLogs builds rows shaped like real ICL data ({"data":{"msg":...}}) so
// the default jq (Logs.JqDefaultQuery, ".data") projects to something non-null.
func refetchLogs(lines ...string) []icl.Log {
	out := make([]icl.Log, len(lines))
	for i, s := range lines {
		out[i] = icl.Log{Data: map[string]any{"data": map[string]any{"msg": s}}}
	}
	return out
}

// drainChunks pumps every jq/search/filter chunk the fake poster captured since
// `from` back through the root dispatcher, exactly as the runtime loop does, to
// a fixpoint: handling a JQChunkMsg spawns further search and filter chunks.
// FakePoster.Go runs its body inline, so every post lands on this goroutine and
// fp.Posted needs no lock here.
func drainChunks(m *Model, fp *msgstest.FakePoster, from int) int {
	for {
		evs := fp.Posted
		if from >= len(evs) {
			return from
		}
		batch := evs[from:]
		from = len(evs)
		for _, ev := range batch {
			m.handleLogviewer(ev)
		}
	}
}

// TestRefetch_SearchJumpsUseTheNewQuerysPositions is the regression gate for the
// reported bug:
//
//	run a query -> search -> back to instances -> fetch again -> press n
//
// jumped relative to the PREVIOUS query's logs. Two independent pieces of
// per-row state survived the re-fetch: the logviewer cursor (SetStore only
// reset it when the store *identity* changed, and a re-fetch reuses the
// instance's store) and Instance.savedCursor (the fetch prologue emptied the
// store but left the cursor saved for those now-discarded rows, which the root
// restores verbatim after every lazy reload).
//
// It drives the real root handlers end to end, covering both shapes of "fetch
// again": streamed in place, and flushed-to-snapshot then lazily reloaded.
func TestRefetch_SearchJumpsUseTheNewQuerysPositions(t *testing.T) {
	// Cannot use t.Parallel(): ui.New writes package-level keys.HelpKeyStyle.

	// query 1: the only "target" is at row 5.
	q1 := refetchLogs("a", "b", "c", "d", "e", "target one", "g", "h")
	// query 2 (more logs generated since): the only "target" is at row 2.
	q2 := refetchLogs("a", "b", "target two", "d", "e", "f", "g", "h", "i", "j")

	for _, reload := range []bool{false, true} {
		name := "streamed in place"
		if reload {
			name = "flushed then lazily reloaded"
		}
		t.Run(name, func(t *testing.T) {
			bundle := depstest.NewTest(t)
			bundle.Config.ICL.Instances = []config.ICLInstanceConfig{
				{Name: "test-a", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account:test-a::")},
				{Name: "test-b", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account:test-b::")},
			}
			m, err := New(t.Context(), bundle)
			require.NoError(t, err)
			t.Cleanup(func() { _ = m.Close() })

			fp := &msgstest.FakePoster{}
			m.SetPoster(fp) // wires every instance store's poster too
			m.logviewer.SetRect(component.Rect{X: 0, Y: 0, W: 120, H: 24})

			insts := m.instances.GetInstances()
			require.GreaterOrEqual(t, len(insts), 2, "need two configured instances")
			a, b := insts[0], insts[1]

			// ---- query 1: the user opens the instance, which lazily loads its
			// logs (the load arm is what marks it as the loaded instance).
			a.Store.SetLogs(q1)
			m.handleInstance(instancepicker.InstanceSelectMsg{
				Name: a.Name, CRN: a.CRN, Store: a.Store, OpenLogViewer: true,
			})
			m.handleInstance(instancepicker.InstanceLoadReadyMsg{CRN: a.CRN, Logs: q1})
			require.Equal(t, a.CRN, m.loadedInstance)

			bundle.State.SetSearch("target")
			a.Store.DoSearch()
			w := drainChunks(m, fp, 0)

			// ---- press n: lands on the query-1 match
			_, ok := m.logviewer.CenterNextMatch()
			require.True(t, ok, "n must find the query-1 match")
			_, _, curLog, _, _ := m.logviewer.GetCursor()
			require.Equal(t, 5, curLog, "query-1 match row")

			// ---- back to instances: moving the picker cursor onto another
			// instance is what makes the root save this instance's position.
			m.handleInstance(instancepicker.InstanceSelectMsg{Name: b.Name, CRN: b.CRN, Store: b.Store})
			require.Equal(t, 5, a.GetSavedCursor().Log, "position saved on switch-away")

			// ---- fetch again: the per-instance prologue every fetch runs.
			insts.ResolveTokens(t.Context(), 0, icl.NewAccountManager(config.New().ICL.Environments), "q", fp)

			a.Store.StartStream(len(q2))
			id := a.Store.GetQueryID()
			m.delegateInstance(&msgs.LogStreamMsg{CRN: a.CRN, ID: id, Logs: q2})
			m.delegateInstance(&msgs.LogStreamDoneMsg{CRN: a.CRN, ID: id})

			if reload {
				// The finished query is flushed to the .wip snapshot, then the
				// user reopens the instance and it is lazily reloaded.
				a.Store.ClearData()
				m.handleInstance(instancepicker.InstanceSelectMsg{
					Name: a.Name, CRN: a.CRN, Store: a.Store, OpenLogViewer: true,
				})
				m.handleInstance(instancepicker.InstanceLoadReadyMsg{CRN: a.CRN, Logs: q2})
			} else {
				// Logs stayed in memory; the user returns to the logs tab.
				m.handleInstance(instancepicker.InstanceSelectMsg{
					Name: a.Name, CRN: a.CRN, Store: a.Store, OpenLogViewer: true,
				})
			}
			drainChunks(m, fp, w)

			// The store's search map was never the problem — assert the rebuild
			// so a future regression there stays distinguishable from this one.
			require.Equal(t, 1, m.logviewer.GetSearchMatchCount(),
				"search must have been rebuilt over query-2's logs")

			// ---- press n
			_, _, curLog, _, _ = m.logviewer.GetCursor()
			assert.Equal(t, 0, curLog, "cursor must not carry a query-1 row index onto query-2 logs")

			logIdx, ok := m.logviewer.CenterNextMatch()
			require.True(t, ok, "n must find the query-2 match")
			_, _, curLog, _, _ = m.logviewer.GetCursor()
			assert.Equal(t, 2, curLog, "n jumped relative to the previous query")
			assert.Equal(t, 2, logIdx, "the reported cursor position is query-2's match row")
			assert.Equal(t, 1, m.logviewer.GetSearchMatchCount(), "match count must be query-2's")
		})
	}
}
