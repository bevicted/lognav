package logviewer

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

//nolint:gocyclo,funlen // single fluent keybind registration; splitting fragments the binding chain.
func bindKeyhandlersToModel(m *Model) {
	kbs := keys.KeyBindsFrom(m.bundle.Config, m.requestQuit)
	m.kh = keys.New().Bind(
		kbs.Quit,

		// vertical movement

		kbs.MoveToTop.WithAction(func() { m.CursorToTop() }),
		kbs.MovePageUp.WithAction(func() {
			_, logsR, _ := m.subRects()
			m.CursorUp(logsR.H)
		}),
		kbs.MoveHalfPageUp.WithAction(func() {
			_, logsR, _ := m.subRects()
			m.CursorUp(logsR.H >> 1)
		}),
		kbs.MoveLineUp.WithAction(func() { m.CursorUp(1) }),
		kbs.MoveLineDown.WithAction(func() { m.CursorDown(1) }),
		kbs.MoveHalfPageDown.WithAction(func() {
			_, logsR, _ := m.subRects()
			m.CursorDown(logsR.H >> 1)
		}),
		kbs.MovePageDown.WithAction(func() {
			_, logsR, _ := m.subRects()
			m.CursorDown(logsR.H)
		}),
		kbs.MoveToBottom.WithAction(func() { m.CursorToBottom() }),

		// horizontal movement

		kbs.MoveToFirstCol.WithAction(func() {
			m.ScrollHorizontally(-m.xOffset)
		}),
		kbs.MoveToFirstChar.WithAction(func() {
			m.moveToFirstChar()
		}),
		kbs.MoveLeftN.WithAction(func() {
			m.ScrollHorizontally(-int(m.bundle.Config.Logs.HorizontalMoveNAmount))
		}),
		kbs.MoveLeft.WithAction(func() {
			m.ScrollHorizontally(-1)
		}),
		kbs.MoveRight.WithAction(func() {
			m.ScrollHorizontally(1)
		}),
		kbs.MoveRightN.WithAction(func() {
			m.ScrollHorizontally(int(m.bundle.Config.Logs.HorizontalMoveNAmount))
		}),
		kbs.MoveToLastChar.WithAction(func() {
			_, logsR, _ := m.subRects()
			m.ScrollHorizontally(len(m.GetCurrentLine()) - logsR.W - m.xOffset)
		}),

		// other

		keys.Binding{
			Keys:     m.bundle.Config.Keys.Select,
			Context:  "toggle log expansion on log under cursor",
			Label:    "expand",
			Priority: 2,
			Action: func() {
				if m.store == nil || m.store.GetLogCount() == 0 {
					return
				}
				m.SetExpand(m.cursor.log, !m.store.state.expanded[m.cursor.log])
			},
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.All,
			Context:  "toggle log expansion on all logs",
			Label:    "all",
			Priority: 3,
			Action:   m.ToggleExpandAll,
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Search,
			Context:  "open search dialog",
			Label:    "search",
			Priority: 4,
			Action:   m.openSearchDialog,
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Jq,
			Context:  "open jq dialog",
			Label:    "jq",
			Priority: 5,
			Action:   m.openJQDialog,
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.FilterMenu,
			Context:  "open filter menu",
			Label:    "filter",
			Priority: 6,
			Action:   m.openFilterMenu,
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.CenterPrevSearchMatch,
			Context: "center previous search match",
			Action:  func() { m.CenterPrevMatch() },
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.CenterNextSearchMatch,
			Context: "center next search match",
			Action:  func() { m.CenterNextMatch() },
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Export,
			Context: "export logs to file",
			Action: func() {
				in := uiinput.New()
				in.SetValue(time.Now().Format("2006-01-02T15-04-05"))

				dlg := msgs.ShowDialogMsg{
					Title:   "Export Logs",
					Message: "Enter filename:",
					Input:   in,
					Buttons: []msgs.DialogButton{
						{Label: "OK", Cmd: func(filename string) error {
							if !strings.HasSuffix(filename, ".jsonl") {
								filename += ".jsonl"
							}
							// Build the payload on the loop goroutine so the
							// store read keeps its on-loop invariant, then move
							// only the file IO off-loop via the poster (the
							// dialog confirm path runs on the loop, where a
							// blocking write would stall dispatch).
							var sb strings.Builder
							for i := range m.store.IterLogs(0, 1) {
								sb.WriteString(string(m.store.logs[i].Bytes(m.store.state.expanded[i])) + "\n")
							}
							data := []byte(sb.String())
							if m.poster != nil {
								m.poster.Go(func(context.Context) {
									if err := os.WriteFile(filename, data, 0o600); err != nil {
										m.logger.Error("log export failed", logging.KeyError, err)
									}
								})
							}
							return nil
						}},
						{Label: "Cancel"},
					},
				}
				msgs.PostAsync(m.poster, dlg)
			},
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.CopyValue,
			Context: "copy field value under cursor to clipboard",
			Action: func() {
				if m.store.GetLogCount() > 0 && m.store.state.expanded[m.cursor.log] {
					if v := m.GetCurrentValue(); v != "" {
						if err := clipboard.WriteAll(v); err != nil {
							m.logger.Error("clipboard copy failed", logging.KeyError, err)
						}
						return
					}
				}
				if ll := m.GetCurrentLine(); ll != "" {
					if err := clipboard.WriteAll(strings.TrimSpace(ll)); err != nil {
						m.logger.Error("clipboard copy failed", logging.KeyError, err)
					}
				}
			},
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.CopyEntire,
			Context: "copy entire log to clipboard",
			Action: func() {
				if m.store.GetLogCount() > 0 {
					l := m.store.logs[m.cursor.log]
					b, err := l.marshal(m.store.state.expanded[m.cursor.log])
					if err != nil {
						m.logger.Error("clipboard marshal failed", logging.KeyError, err)
						return
					}
					if err := clipboard.WriteAll(string(b)); err != nil {
						m.logger.Error("clipboard copy failed", logging.KeyError, err)
					}
				}
			},
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.ToggleLogMark,
			Context: "toggle mark on log",
			Action: func() {
				m.store.state.marked.Set(m.cursor.log, !m.IsMarked(m.cursor.log))
			},
		},
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Timeline,
			Context:  "toggle timeline view",
			Label:    "timeline",
			Priority: 7,
			Action: func() {
				if m.store == nil || m.store.GetLogCount() == 0 {
					return
				}
				ts, ok := m.store.GetTSMicro(m.cursor.log)
				m.timeline.SetRect(m.drawRect)
				m.timeline.Open(m.store, ts, ok)
				m.timelineActive = true
			},
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.JqField,
			Context: "jq the field under the cursor",
			Action: func() {
				if m.store != nil && m.store.GetLogCount() > 0 {
					m.onCursorFieldJQ()
				}
			},
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.SearchValue,
			Context: "search the value under the cursor",
			Action: func() {
				if m.store != nil && m.store.GetLogCount() > 0 {
					m.onCursorFieldSearch()
				}
			},
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Center,
			Context: "center line",
			Action: func() {
				if m.store.GetLogCount() > 0 {
					m.Center(m.cursor.log, m.cursor.logLine, 0)
				}
			},
		},
	)
	m.alwaysHandleKH = keys.New().Bind(
		keys.Binding{
			Keys:     m.bundle.Config.Keys.Snapshot,
			Context:  "save snapshot",
			Label:    "save",
			Priority: 8,
			Action: func() {
				msgs.PostAsync(m.poster, msgs.ManualSaveSnapshotMsg{})
			},
		},
	)
}
