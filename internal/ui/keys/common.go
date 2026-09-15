package keys

import (
	"github.com/bevicted/lognav/internal/config"
)

const ConfigSection = "common"

// KeyBindsT is the common keybind set shared across all components.
type KeyBindsT struct {
	Quit             Binding
	ForceQuit        Binding
	FocusTextInput   Binding
	ConfirmTextInput Binding
	CancelTextInput  Binding

	// vertical movement

	MoveToTop        Binding
	MovePageUp       Binding
	MoveHalfPageUp   Binding
	MoveLineUp       Binding
	MoveLineDown     Binding
	MoveHalfPageDown Binding
	MovePageDown     Binding
	MoveToBottom     Binding

	// horizontal movement

	MoveToFirstCol  Binding
	MoveToFirstChar Binding
	MoveLeftN       Binding
	MoveLeft        Binding
	MoveRight       Binding
	MoveRightN      Binding
	MoveToLastChar  Binding
}

// doublePressKeys renders each key as the two-press gesture that triggers it
// ("ctrl+c" -> "ctrl+c ctrl+c"), so the help overlay spells out that one press
// is not enough. The result is display text: it can never match a Keystroke().
func doublePressKeys(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k + " " + k
	}
	return out
}

// KeyBindsFrom builds a KeyBindsT populated from the given config. The quit
// hook is invoked by the Quit action to request a clean exit; each component's
// bindKeyhandlersToModel passes a hook that posts msgs.QuitMsg off-loop via its
// poster. ForceQuit is display-only (see below) and does not use the hook.
func KeyBindsFrom(cfg *config.Config, quit func()) KeyBindsT {
	return KeyBindsT{
		Quit: Binding{
			Keys:     cfg.Keys.Quit,
			Context:  "quit component",
			Category: CatGlobal,
			Action: func() {
				quit()
			},
		},
		ForceQuit: Binding{
			// Display only, hence no Action: ctrl+c is Keys.Clear, and the exit
			// gesture is two presses counted by the runtime loop
			// (runtime.trackCtrlC) above every model keybind. Giving this binding
			// an action would exit on the FIRST press and clear nothing.
			Keys:     doublePressKeys(cfg.Keys.ForceQuit),
			Context:  "exit program",
			Category: CatGlobal,
		},
		FocusTextInput: Binding{
			Keys:    cfg.Keys.Next,
			Context: "focus text input",
		},
		ConfirmTextInput: Binding{
			Keys:    cfg.Keys.Accept,
			Context: "confirm text input",
		},
		CancelTextInput: Binding{
			Keys:    cfg.Keys.Cancel,
			Context: "cancel text input",
		},

		// vertical movement

		MoveToTop: Binding{
			Keys:     cfg.Keys.MoveToTop,
			Context:  "move to top",
			Category: CatNavigation,
		},
		MovePageUp: Binding{
			Keys:     cfg.Keys.MovePageUp,
			Context:  "move one page up",
			Category: CatNavigation,
		},
		MoveHalfPageUp: Binding{
			Keys:     cfg.Keys.MoveHalfPageUp,
			Context:  "move one page up",
			Category: CatNavigation,
		},
		MoveLineUp: Binding{
			Keys:     cfg.Keys.MoveUp,
			Context:  "move one line up",
			Category: CatNavigation,
		},
		MoveLineDown: Binding{
			Keys:     cfg.Keys.MoveDown,
			Context:  "move one line down",
			Category: CatNavigation,
		},
		MoveHalfPageDown: Binding{
			Keys:     cfg.Keys.MoveHalfPageDown,
			Context:  "move half page down",
			Category: CatNavigation,
		},
		MovePageDown: Binding{
			Keys:     cfg.Keys.MovePageDown,
			Context:  "move page down",
			Category: CatNavigation,
		},
		MoveToBottom: Binding{
			Keys:     cfg.Keys.MoveToBottom,
			Context:  "move to bottom",
			Category: CatNavigation,
		},

		// horizontal movement

		MoveToFirstCol: Binding{
			Keys:     cfg.Keys.MoveToFirstCol,
			Context:  "move to first column",
			Category: CatNavigation,
		},
		MoveToFirstChar: Binding{
			Keys:     cfg.Keys.MoveToFirstChar,
			Context:  "move to first character",
			Category: CatNavigation,
		},
		MoveLeftN: Binding{
			Keys:     cfg.Keys.MoveLeftN,
			Context:  "move N columns left",
			Category: CatNavigation,
		},
		MoveLeft: Binding{
			Keys:     cfg.Keys.MoveLeft,
			Context:  "move 1 column left",
			Category: CatNavigation,
		},
		MoveRight: Binding{
			Keys:     cfg.Keys.MoveRight,
			Context:  "move 1 column right",
			Category: CatNavigation,
		},
		MoveRightN: Binding{
			Keys:     cfg.Keys.MoveRightN,
			Context:  "move N columns right",
			Category: CatNavigation,
		},
		MoveToLastChar: Binding{
			Keys:     cfg.Keys.MoveToLastChar,
			Context:  "move to last character",
			Category: CatNavigation,
		},
	}
}
