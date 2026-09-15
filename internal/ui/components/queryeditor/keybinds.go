package queryeditor

import (
	"errors"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

//nolint:funlen // single fluent keybind registration; splitting fragments the binding chain.
func bindKeyhandlersToModel(m *Model) {
	kbs := keys.KeyBindsFrom(m.bundle.Config, m.requestQuit)
	m.kh = keys.New().Bind(
		kbs.Quit,
		kbs.FocusTextInput.WithAction(m.focusEditor),
		kbs.ConfirmTextInput.WithAction(func() {
			m.eventHandler.OnConfirmPressed()
		}),
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Editor,
			Context:  "edit the query with $EDITOR",
			Label:    "edit",
			Priority: 2,
			Action: func() {
				openEditor(m.ctx, m.poster, m.editor.Value(), m.bundle.Config.Core.IncludeSnippetsInEditor, m.snippets)
			},
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.FilesMenu,
			Context:  "show dataprime files menu",
			Label:    "files",
			Priority: 4,
			Action: func() {
				m.showSnippets = false
				m.showFiles = true
				m.logger.Debug("file browser toggled", "visible", m.showFiles)
			},
		},
		keys.Binding{
			Keys:    append(m.bundle.Config.Keys.CopyValue, m.bundle.Config.Keys.CopyEntire...),
			Context: "copy query to clipboard",
			Action: func() {
				q := m.getQuery()
				if q == "" {
					return
				}
				if err := clipboard.WriteAll(q); err != nil {
					m.logger.Error("clipboard copy failed", logging.KeyError, err)
				}
			},
		},
	)

	m.textAreaKH = keys.New().Bind(
		kbs.CancelTextInput.WithAction(func() {
			m.editor.Blur()
			msgs.PostAsync(m.poster, msgs.FocusMsg{GrabFocus: false})
		}),
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Clear,
			Context: "clear the query",
			Action: func() {
				m.setQueryInEditor("")
			},
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Snippets,
			Context:  "show snippets menu",
			Label:    "snippets",
			Priority: 3,
			Action:   m.openSnippets,
		},
	)

	m.fhKeyKH = keys.New().Bind(
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Cancel,
			Context: "hide dataprime files menu",
			Action: func() {
				m.showFiles = false
				m.logger.Debug("file browser toggled", "visible", m.showFiles)
			},
		},
	)

	hideSnippets := func() {
		m.showSnippets = false
		if m.snippetList.IsFocused() {
			m.snippetList.Unfocus() // posts ReleaseFocus off-loop via poster (A5)
		}
		m.snippetList.ClearFuzzy()
		m.logger.Debug("snippets toggled", "visible", m.showSnippets)
		m.focusEditor()
	}
	// insertSelectedSnippet inserts the snippet under the snippet-list cursor into
	// the editor, exits snippet mode, and re-grabs editor focus. Shared by the
	// fuzzy-confirm key path (Enter) and the click-to-activate path (double-click,
	// via WithActivate) so the mouse behaves like other tab-content lists.
	insertSelectedSnippet := func() {
		idx := m.snippetList.GetCursor()
		if idx >= 0 && idx < len(m.snippets) {
			m.editor.InsertString(m.snippets[idx].Snippet)
			m.bundle.State.SetQuery(m.getQuery())
			m.rehighlight()
		}
		m.showSnippets = false
		m.snippetList.ClearFuzzy()
		m.focusEditor()
	}
	m.snippetList.WithFuzzyConfirmAction(insertSelectedSnippet)
	m.snippetList.WithActivate(insertSelectedSnippet)
	m.snippetListKH = keys.New().Bind(
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Cancel,
			Context: "hide snippets menu",
			Action:  hideSnippets,
		},
	)

	m.alwaysHandleKH = keys.New().Bind(
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Snapshot,
			Context:  "save query to file",
			Label:    "save",
			Priority: 5,
			Action: func() {
				in := uiinput.New()
				msgs.PostAsync(m.poster, msgs.ShowDialogMsg{
					Title:   "Save Query",
					Message: "Enter filename:",
					Input:   in,
					Buttons: []msgs.DialogButton{
						{Label: "OK", Cmd: func(filename string) error {
							if strings.TrimSpace(filename) == "" {
								return errors.New("enter a filename")
							}
							if !strings.HasSuffix(filename, queryFileExt) {
								filename += queryFileExt
							}
							name := strings.TrimSuffix(filename, queryFileExt)
							m.fh.WriteFile(name) // void: spawns a tracked poster goroutine
							return nil
						}},
						{Label: "Cancel"},
					},
				})
			},
		},
	)
}
