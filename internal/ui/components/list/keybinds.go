package list

import (
	"github.com/bevicted/lognav/internal/ui/keys"
)

type keyhandler struct {
	alwaysHandle *keys.Handler
	regular      *keys.Handler
	ti           *keys.Handler
}

func bindKeyhandlersToModel(m *Model) {
	kbs := keys.KeyBindsFrom(m.bundle.Config, m.requestQuit)
	m.kh = &keyhandler{
		alwaysHandle: keys.New().Bind(
			kbs.ConfirmTextInput.WithAction(func() {
				// m.Unfocus() posts ReleaseFocus off-loop via the poster.
				// onFuzzyConfirm carries the business effect (and any re-grab focus
				// signal from the caller, e.g. queryeditor posts GrabFocus off-loop).
				m.Unfocus()
				if m.onFuzzyConfirm != nil {
					m.onFuzzyConfirm()
				}
			}),
			keys.Binding{
				Keys:     m.bundle.Config.Keys.Prev,
				Context:  "move cursor to the previous item",
				Category: keys.CatNavigation,
				Action: func() {
					m.MoveCursor(-1)
				},
			},
			keys.Binding{
				Keys:     m.bundle.Config.Keys.Next,
				Context:  "move cursor to the next item",
				Category: keys.CatNavigation,
				Action: func() {
					m.MoveCursor(1)
				},
			},
		),
		regular: keys.New().Bind(
			// vertical movement

			kbs.MoveToTop.WithAction(func() {
				m.SetCursor(0)
			}),
			kbs.MovePageUp.WithAction(func() {
				m.MoveCursor(-m.listAreaHeight())
			}),
			kbs.MoveHalfPageUp.WithAction(func() {
				m.MoveCursor(-m.listAreaHeight() >> 1)
			}),
			kbs.MoveLineUp.WithAction(func() {
				m.MoveCursor(-1)
			}),
			kbs.MoveLineDown.WithAction(func() {
				m.MoveCursor(1)
			}),
			kbs.MoveHalfPageDown.WithAction(func() {
				m.MoveCursor(m.listAreaHeight() >> 1)
			}),
			kbs.MovePageDown.WithAction(func() {
				m.MoveCursor(m.listAreaHeight())
			}),
			// Last *visible* row: with a fuzzy filter active len(m.items) overshoots
			// the visible count, and only MoveCursor's clamp rescued the old
			// relative form.
			kbs.MoveToBottom.WithAction(func() {
				m.SetCursor(m.VisibleLen() - 1)
			}),

			// fuzzy

			keys.Binding{
				Keys:    m.bundle.Config.Keys.Search,
				Context: "focus fuzzy finder",
				Action:  m.Focus,
			},
		),
		ti: keys.New().Bind(
			kbs.CancelTextInput.WithAction(func() {
				m.Unfocus()
			}),
			keys.Binding{
				Keys:    m.bundle.Config.Keys.Clear,
				Context: "clear fuzzy filter",
				Action:  m.ClearFuzzy,
			},
		),
	}
}
