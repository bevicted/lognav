package styles

import (
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/ui/keys"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

const (
	ConfigSection = "style"
)

// KeyHintCellStyle returns a uv.Style for key hint rendering in canvas-based lists.
func KeyHintCellStyle(bundle deps.Bundle) uv.Style {
	return uv.Style{Fg: bundle.Config.Style.KeyHintFg.Color, Attrs: uv.AttrBold}
}

// CategoryStyle returns the uv.Style for the sectioned-help category column.
// Colors are hardcoded terminal ANSI (not config-driven) so the column stays a
// stable visual anchor: bright green for the active tab's actions, bright cyan
// for global bindings, and a dim bright-black for movement (the de-emphasized
// section).
func CategoryStyle(c keys.Category) uv.Style {
	switch c {
	case keys.CatGlobal:
		return uv.Style{Fg: ansi.BrightCyan}
	case keys.CatNavigation:
		return uv.Style{Fg: ansi.BrightBlack}
	default: // keys.CatThisView
		return uv.Style{Fg: ansi.BrightGreen}
	}
}

// CategoryColWidth is the fixed width of the help-menu category column, sized to
// the widest label ("Navigation").
const CategoryColWidth = 10
