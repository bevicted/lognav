package snapshothandler

import (
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

func bindKeyhandlersToModel(m *Model) {
	kbs := keys.KeyBindsFrom(m.bundle.Config, m.requestQuit)
	m.kh = keys.New().Bind(
		kbs.Quit,
	)
	m.alwaysHandleKH = keys.New().Bind(
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Snapshot,
			Context:  "save snapshot",
			Label:    "save",
			Priority: 5,
			Action: func() {
				msgs.PostAsync(m.poster, msgs.ManualSaveSnapshotMsg{})
			},
		},
	)
}
