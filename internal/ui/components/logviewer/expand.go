package logviewer

import (
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// SetExpand toggles expansion for a single log entry at the given index. It
// invalidates the render cache entry and re-runs the search over the affected
// log if it had prior search results, so that match offsets stay consistent
// with the newly expanded/collapsed byte representation.
func (m *Model) SetExpand(idx int, b bool) {
	m.store.SetExpand(idx, b)
	m.renderCache.Remove(idx)

	if idx == m.cursor.log && !b {
		// we just closed the log on ourselves
		m.cursor.y -= m.cursor.logLine
		m.cursor.logLine = 0
	}

	m.updateDisplay()
}

// ToggleExpandAll expands all logs if any are collapsed, or collapses all logs
// if every log is already expanded.
func (m *Model) ToggleExpandAll() {
	wasExpanded := m.store.state.expanded[m.cursor.log]
	m.store.ToggleExpandAll()
	m.renderCache.Purge()

	if wasExpanded && !m.store.state.expanded[m.cursor.log] {
		m.cursor.y -= m.cursor.logLine
		m.cursor.logLine = 0
	}

	m.updateDisplay()
}

// viewInContext posts a ViewInContextMsg off-loop for the log currently under
// the cursor. It resolves the Kubernetes pod ID from the log's
// .data.kubernetes.pod_id field via podIDFromData. It no-ops if the log lacks
// the required fields.
func (m *Model) viewInContext() {
	if m.store.GetLogCount() < 1 {
		return
	}
	l := m.store.logs[m.cursor.log]
	d, _, u := l.GetData()
	defer u()

	podID, ok := podIDFromData(d)
	if !ok {
		m.logger.Debug("log has no resolvable .data.kubernetes.pod_id (missing or wrong type)")
		return
	}
	if l.metadata.ID == "" {
		m.logger.Debug("log has no id in metadata")
		return
	}
	if l.metadata.TSMicro == 0 {
		m.logger.Debug("log has no timestamp in metadata")
		return
	}

	msgs.PostAsync(m.poster, msgs.ViewInContextMsg{
		CRN:       m.store.GetInstance(),
		PodID:     podID,
		LogID:     l.metadata.ID,
		Timestamp: l.metadata.TSMicro,
	})
}
