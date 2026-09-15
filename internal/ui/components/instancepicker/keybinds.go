package instancepicker

import (
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
)

func bindKeyhandlersToModel(m *Model) {
	kbs := keys.KeyBindsFrom(m.bundle.Config, m.requestQuit)
	m.kh = keys.New().Bind(
		kbs.Quit,
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Select,
			Context:  "toggle selection on instance",
			Label:    "select",
			Priority: 2,
			Action:   m.toggleSelected,
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.All,
			Context:  "toggle unified selection on every visible instance, prefer all selected",
			Label:    "all",
			Priority: 5,
			Action:   m.toggleAllVisible,
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.FetchLogs,
			Context:  "fetch logs using query",
			Label:    "fetch",
			Priority: 3,
			Action: func() {
				if !m.instances.AreAllQueriesDone() {
					m.logger.Warn("fetch blocked, queries still in progress")
					return
				}

				if err := m.startFetch(); err != nil {
					m.logger.Error("failed to start fetch", "error", err)
				}
			},
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.FirstFetch,
			Context:  "race enabled instances until the first logs arrive",
			Label:    "first",
			Priority: 6,
			Action: func() {
				if !m.instances.AreAllQueriesDone() {
					m.logger.Warn("first fetch blocked, queries still in progress")
					return
				}
				if err := m.startFirstFetch(); err != nil {
					m.logger.Error("failed to start first fetch", "error", err)
				}
			},
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.RetryFailedFetch,
			Context: "retry failed fetch under cursor",
			// Bypasses startFetch — retried instance logs stay in memory
			// (no backing container). Acceptable for single-instance retry.
			Action: func() {
				r := m.instances[m.list.GetCursor()]
				if r.state != status.Error {
					return
				}
				r.ClearStore()
				r.StartAuthTimer() // AuthInProgress + auth-run clock
				m.instances.resolveEnvMembers(m.authCtx, m.authGeneration, m.authManager, r.env, []string{r.CRN}, m.bundle.State.Query(), m.poster)
			},
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Accept,
			Context:  "open instance in log viewer",
			Label:    "load",
			Priority: 4,
			Action:   m.OpenInLogViewer,
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.CancelAllFetches,
			Context:  "cancel all ongoing fetches",
			Label:    "cancel all",
			Priority: 8,
			Action:   m.cancelAllFetches,
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Watch,
			Context:  "watch enabled instances, retry until logs found",
			Label:    "watch",
			Priority: 7,
			Action:   m.ToggleWatch,
		},
	)
	m.alwaysHandleKH = keys.New().Bind(
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Snapshot,
			Context:  "save snapshot",
			Label:    "save",
			Priority: 9,
			Action:   func() { m.emit(msgs.ManualSaveSnapshotMsg{}) },
		},
	)
}
