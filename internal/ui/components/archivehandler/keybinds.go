package archivehandler

import (
	"github.com/bevicted/lognav/internal/ui/keys"
)

// bindKeyhandlersToModel wires the archive tab's own keyhandler. It mirrors
// snapshothandler: the generic filehandler binds only the file ops (accept/
// delete/rename) and never binds Quit, so without this the global Quit (`q`)
// has no binding anywhere reachable on the Archive tab and a bare `q` press is
// silently dropped (the other tabs each own a Quit binding). Both bindings fire
// only when the fuzzy filter is NOT focused (see HandleKey's guard), so typing
// `q` into the filter still filters.
//
// Accept (Enter) is ALSO bound here, to m.onEnter. The filehandler's own Accept
// binding runs ReadFileUnderCursor → the archive tab's no-op fhRead, so without
// this an unfocused Enter would do nothing (only double-click — WithActivate —
// reached onEnter). Binding Accept here routes an unfocused Enter to the
// collect/poll/expired router. When the filter IS focused the Enter instead
// reaches onEnter via the list's focused confirm (WithConfirmAction →
// onFuzzyConfirm), so onEnter fires exactly once in either case (no double-fire:
// the m.kh arm is gated on !IsFocused()).
func bindKeyhandlersToModel(m *Model) {
	kbs := keys.KeyBindsFrom(m.bundle.Config, m.requestQuit)
	m.kh = keys.New().Bind(
		kbs.Quit,
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Accept,
			Context: "open/collect/poll the selected archive",
			Action:  m.onEnter,
		},
	)
}
