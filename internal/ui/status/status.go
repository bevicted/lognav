// Package status provides phase enumerations for components that report
// their lifecycle state (auth, instance, snapshot, etc.).
package status

import (
	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

// Phase is the lifecycle state of a component (auth, instance, snapshot, etc.).
type Phase int

const (
	Disabled Phase = iota
	Cancelled
	Enabled
	InProgress
	Error
	Warning
	Success
	AuthInProgress
	Watching
	ReadOnly
)

func (p Phase) String() string {
	switch p {
	case Disabled:
		return "disabled"
	case Cancelled:
		return "cancelled"
	case Enabled:
		return "enabled"
	case InProgress:
		return "in progress"
	case Error:
		return "error"
	case Warning:
		return "warn"
	case Success:
		return "success"
	case AuthInProgress:
		return "authenticating"
	case Watching:
		return "watching"
	case ReadOnly:
		return "read only"
	default:
		return "unknown"
	}
}

// Label returns the display label for the phase, read from the bundle config.
func (p Phase) Label(bundle deps.Bundle) string {
	return p.LabelForStyle(bundle.Config.Style)
}

// LabelForStyle returns the configured display label without requiring a TUI
// bundle. It keeps terminal and non-terminal progress renderers on one label
// contract.
func (p Phase) LabelForStyle(style config.Style) string {
	switch p {
	case Disabled:
		return style.DisabledLabel
	case Cancelled:
		return style.CancelledLabel
	case Enabled:
		return style.EnabledLabel
	case InProgress:
		return style.InProgressLabel
	case Error:
		return style.ErrorLabel
	case Warning:
		return style.WarningLabel
	case Success:
		return style.SuccessLabel
	case AuthInProgress:
		return style.AuthInProgressLabel
	case Watching:
		return style.WatchingLabel
	case ReadOnly:
		return style.ReadOnlyLabel
	default:
		return "?"
	}
}

// LabelStyle returns the uv.Style for a phase label: the phase color as the
// foreground on the default background (mirrors how the instance name used to
// be colored). Disabled and Cancelled render grey. The list renderer applies
// reverse-video to the cursor row, so no background or selection styling is
// set here.
func (p Phase) LabelStyle(bundle deps.Bundle) uv.Style {
	return uv.Style{Fg: p.ColorForStyle(bundle.Config.Style).Color}
}

// ColorForStyle returns the configured phase color without constructing a uv
// style, so non-TUI renderers can apply the same configured colors.
func (p Phase) ColorForStyle(style config.Style) config.Color {
	switch p {
	case Disabled:
		return style.DisabledColor
	case Cancelled:
		return style.CancelledColor
	case Enabled:
		return style.EnabledColor
	case InProgress:
		return style.InProgressColor
	case Error:
		return style.ErrorColor
	case Warning:
		return style.WarningColor
	case Success:
		return style.SuccessColor
	case AuthInProgress:
		return style.AuthInProgressColor
	case Watching:
		return style.WatchingColor
	case ReadOnly:
		return style.ReadOnlyColor
	default:
		return config.Color{}
	}
}

// MaxLabelWidth returns the display width of the widest phase label. The label
// column is padded to this width so it stays stable regardless of which phases
// are present. Labels are config-customizable and may contain wide graphemes
// (emoji), so width is measured in display cells via uniseg.StringWidth — len()
// would count bytes (a single ZWJ emoji is 25 bytes, 2 cells) and blow the
// column out. The padding in instancepicker.InstanceRowSegments uses the same
// measure so the rendered column matches this width exactly.
func MaxLabelWidth(bundle deps.Bundle) int {
	return MaxLabelWidthForStyle(bundle.Config.Style)
}

// MaxLabelWidthForStyle returns the display width reserved for every configured
// phase label. It is usable by non-TUI progress renderers without uv cells.
func MaxLabelWidthForStyle(style config.Style) int {
	w := 0
	for _, p := range []Phase{
		Disabled, Cancelled, Enabled, InProgress,
		Error, Warning, Success, AuthInProgress, Watching, ReadOnly,
	} {
		w = max(w, uniseg.StringWidth(p.LabelForStyle(style)))
	}
	return w
}
