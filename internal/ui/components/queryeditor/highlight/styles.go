package highlight

import (
	"github.com/bevicted/lognav/internal/config"
	uv "github.com/charmbracelet/ultraviolet"
)

// TokenCellStyle returns a uv.Style for a token type using styles from cfg.
func TokenCellStyle(cfg *config.Config, t TokenType) uv.Style {
	var s uv.Style
	switch t {
	case TokenKeyword:
		s.Fg = cfg.Style.DpKeywordFg.Color
		s.Attrs |= uv.AttrBold
	case TokenOperator:
		s.Fg = cfg.Style.DpOperatorFg.Color
	case TokenString:
		s.Fg = cfg.Style.DpStringFg.Color
	case TokenNumber:
		s.Fg = cfg.Style.DpNumberFg.Color
	case TokenComment:
		s.Fg = cfg.Style.DpCommentFg.Color
		s.Attrs |= uv.AttrItalic
	case TokenFunction:
		s.Fg = cfg.Style.DpFunctionFg.Color
	case TokenVariable:
		s.Fg = cfg.Style.DpVariableFg.Color
	case TokenType_:
		s.Fg = cfg.Style.DpTypeFg.Color
	case TokenBoolNull:
		s.Fg = cfg.Style.DpBoolNullFg.Color
	case TokenError:
		s.Underline = uv.UnderlineCurly
		s.UnderlineColor = cfg.Style.DpErrorFg.Color
	}
	return s
}
