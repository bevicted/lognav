package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bevicted/lognav/internal/ui/components/helpoverlay"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/styles"
)

type keyHandlers struct {
	// displayOnly holds bindings that appear in the help overlay but are never
	// dispatched. They are a slice, not a *keys.Handler, precisely so no key
	// path can look them up.
	displayOnly []keys.Binding
	showHelp    *keys.Handler
	regular     *keys.Handler
	tabselector *keys.Handler
}

func bindKeyhandlersToModel(m *Model) {
	kbs := keys.KeyBindsFrom(m.bundle.Config, m.requestQuit)
	m.kh = &keyHandlers{
		// ForceQuit is listed for the help overlay only — it carries no action.
		// ctrl+c must fall through to the active component as Keys.Clear; the
		// two-press exit is counted one layer up, on the runtime loop, so it
		// works even where no component handles the key.
		displayOnly: []keys.Binding{
			kbs.ForceQuit,
		},
		showHelp: keys.New().Bind(
			keys.Binding{
				Keys:    m.bundle.Config.Keys.Accept,
				Context: "execute keybind action",
				Action:  m.doKeybindAction,
			},
			keys.Binding{
				Keys:    m.bundle.Config.Keys.Cancel,
				Context: "hide keybind list",
				Action: func() {
					m.overlay = nil
					m.logger.Debug("help dismissed")
					// Release focus off-loop: the keybind fires on the loop, so a
					// bare on-loop PostCritical would deadlock the unbuffered events
					// channel mid-dispatch.
					m.releaseFocus()
				},
			},
		),
		regular: keys.New().Bind(
			keys.Binding{
				Keys:     m.bundle.Config.Keys.Help,
				Context:  "show keybinds that are available in the current context",
				Category: keys.CatGlobal,
				Label:    "help",
				Priority: 1,
				Action: func() {
					b := m.GetKeybinds()
					final := make([][]list.Segment, len(b))
					var longestKeys int

					for _, b := range b {
						l := len(strings.Join(b.GetKeys(), "/"))
						if longestKeys < l {
							longestKeys = l
						}
					}
					keyStyle := styles.KeyHintCellStyle(m.bundle)
					for i, b := range b {
						// Leading colored category column (hardcoded ANSI),
						// then the aligned key cell, then the description.
						final[i] = []list.Segment{
							{Text: fmt.Sprintf("%-*s ", styles.CategoryColWidth, b.Category.String()), Style: styles.CategoryStyle(b.Category)},
							{Text: fmt.Sprintf("%*s", longestKeys, strings.Join(b.GetKeys(), "/")), Style: keyStyle},
							{Text: " " + b.Context},
						}
					}

					overlay := helpoverlay.New(
						m.bundle,
						"help / "+m.tabs.GetActiveTab().GetTitle(),
						final,
						m.doKeybindAction,
					)
					overlay.SetPoster(m.poster)
					m.overlay = overlay
					// overlay.Focus() posts FocusMsg off-loop via its own poster.
					// assignOverlayRect is layout-only and void; DrawTo re-applies
					// the rect each frame.
					overlay.Focus()
					m.assignOverlayRect()
					m.logger.Debug("help opened")
				},
			},
			keys.Binding{
				Keys:     m.bundle.Config.Keys.TabLeft,
				Context:  "show tab to the left",
				Category: keys.CatGlobal,
				Action: func() {
					m.tabs.SelectPrev()
				},
			},
			keys.Binding{
				Keys:     m.bundle.Config.Keys.TabRight,
				Context:  "show tab to the right",
				Category: keys.CatGlobal,
				Action: func() {
					m.tabs.SelectNext()
				},
			},
			keys.Binding{
				Keys:     m.bundle.Config.Keys.PrevTab,
				Context:  "show previously active tab",
				Category: keys.CatGlobal,
				Action: func() {
					m.tabs.SelectPrevActive()
				},
			},
			keys.Binding{
				Keys:     m.bundle.Config.Keys.ContextMenu,
				Context:  "open context menu for the current selection",
				Category: keys.CatGlobal,
				Label:    "context menu",
				Priority: 6,
				Action:   m.openActiveContextMenu,
			},
			// only here for show
			keys.Binding{
				Context:  "select Nth tab",
				Keys:     []string{"1-9"},
				Category: keys.CatGlobal,
			},
		),
		tabselector: keys.New().Bind(tabSelectBindings(m)...),
	}
}

// tabSelectBindings binds digit keys 1..numTabs to direct tab selection. Digits
// beyond numTabs are intentionally left unbound: the former 5-9 bindings all
// clamped to the last tab (dead). The action for each digit is a no-op when its
// target index is past the currently-registered tab count, so a digit for a
// conditionally-absent tab (e.g. 5 when the opt-in Archive tab is off) does
// nothing rather than clamp-selecting the last tab. m.tabs is read lazily inside
// the action closures because bindKeyhandlersToModel runs before m.tabs is
// assigned (so the tab count is not yet known at bind time).
func tabSelectBindings(m *Model) []keys.Binding {
	bindings := make([]keys.Binding, numTabs)
	for i := range numTabs {
		bindings[i] = keys.Binding{
			Keys: []string{strconv.Itoa(i + 1)},
			Action: func() {
				if i < m.tabs.Count() {
					m.tabs.Select(i)
				}
			},
		}
	}
	return bindings
}
