package canvas

import (
	"image/color"

	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/icl"
)

// Hardcoded gutter severity initials.
const (
	gutterCritical = "C"
	gutterError    = "E"
	gutterWarning  = "W"
	gutterInfo     = "I"
	gutterDebug    = "D"
	gutterVerbose  = "V"
	gutterUnknown  = "?"
)

// GutterSeverityStyle returns the background color and one-rune glyph used to
// render a log of the given severity in the gutter/timeline. Shared by the
// logviewer's gutter cell and the timeline's flamegraph row so the two views
// can't drift.
func GutterSeverityStyle(bundle deps.Bundle, s icl.Severity) (color.Color, string) {
	switch s {
	case icl.SeverityCritical:
		return bundle.Config.Style.CriticalColor.Color, gutterCritical
	case icl.SeverityError:
		return bundle.Config.Style.ErrorColor.Color, gutterError
	case icl.SeverityWarning:
		return bundle.Config.Style.WarningColor.Color, gutterWarning
	case icl.SeverityInfo:
		return bundle.Config.Style.InfoColor.Color, gutterInfo
	case icl.SeverityDebug:
		return bundle.Config.Style.DebugColor.Color, gutterDebug
	case icl.SeverityVerbose:
		return bundle.Config.Style.VerboseColor.Color, gutterVerbose
	default:
		return bundle.Config.Style.UnknownColor.Color, gutterUnknown
	}
}
