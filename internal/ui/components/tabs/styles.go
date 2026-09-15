package tabs

import (
	"github.com/bevicted/lognav/internal/config"
	uv "github.com/charmbracelet/ultraviolet"
)

const (
	TabSeparator = "|"
	tabPadding   = 2
)

// tablineCellStyle returns the base uv.Style for tabline cells (background +
// foreground from cfg.Style).
func tablineCellStyle(cfg *config.Config) uv.Style {
	return uv.Style{
		Fg: cfg.Style.TablineFg.Color,
		Bg: cfg.Style.TablineBg.Color,
	}
}

// activeTabCellStyle returns the uv.Style for the active tab — base style
// with bold + reverse attributes.
func activeTabCellStyle(cfg *config.Config) uv.Style {
	s := tablineCellStyle(cfg)
	s.Attrs |= uv.AttrBold | uv.AttrReverse
	return s
}
