package timeline

import (
	"github.com/bevicted/lognav/internal/ui/keys"
)

func bindKeyhandlersToModel(m *Model) {
	kbs := keys.KeyBindsFrom(m.bundle.Config, m.requestQuit)
	m.kh = keys.New().Bind(
		kbs.Quit,

		kbs.MoveToTop.WithAction(func() {
			m.cursor = 0
			m.clampScroll()
		}),
		kbs.MovePageUp.WithAction(func() {
			h := m.drawRect.H
			m.cursor -= h
			m.clampScroll()
		}),
		kbs.MoveHalfPageUp.WithAction(func() {
			h := m.drawRect.H
			m.cursor -= h / 2
			m.clampScroll()
		}),
		kbs.MoveLineUp.WithAction(func() {
			m.cursor--
			m.clampScroll()
		}),
		kbs.MoveLineDown.WithAction(func() {
			m.cursor++
			m.clampScroll()
		}),
		kbs.MoveHalfPageDown.WithAction(func() {
			h := m.drawRect.H
			m.cursor += h / 2
			m.clampScroll()
		}),
		kbs.MovePageDown.WithAction(func() {
			h := m.drawRect.H
			m.cursor += h
			m.clampScroll()
		}),
		kbs.MoveToBottom.WithAction(func() {
			m.cursor = len(m.visibleIndices) - 1
			m.clampScroll()
		}),

		keys.Binding{
			Keys:     m.bundle.Config.Keys.Accept,
			Context:  "jump to selected bucket",
			Label:    "jump",
			Priority: 2,
			Action:   m.requestJump,
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Cancel,
			Context: "close timeline",
			Action: func() {
				m.wantClose = true
			},
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Timeline,
			Context:  "close timeline",
			Label:    "close",
			Priority: 3,
			Action: func() {
				m.wantClose = true
			},
		},
	)
}
