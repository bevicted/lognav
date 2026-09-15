package logviewer

import (
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
)

// tokenCellStyle returns a uv.Style for a token type in Canvas rendering.
func (m *Model) tokenCellStyle(t TokenType, baseBg color.Color) uv.Style {
	s := uv.Style{Bg: baseBg}
	switch t {
	case TokenKey:
		s.Fg = m.bundle.Config.Style.JsonKeyFg.Color
		s.Attrs |= uv.AttrBold
	case TokenString:
		s.Fg = m.bundle.Config.Style.JsonStringFg.Color
	case TokenNumber:
		s.Fg = m.bundle.Config.Style.JsonNumberFg.Color
	case TokenBool, TokenNull:
		s.Fg = m.bundle.Config.Style.JsonBoolNullFg.Color
	}
	return s
}
