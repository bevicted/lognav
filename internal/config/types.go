package config

import (
	"errors"
	"fmt"
	"image/color"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// Color is an alias for ConfigColor for backwards compatibility.
type Color = ConfigColor

// ConfigColor wraps color.Color with YAML marshaling support.
// Accepts ANSI named colors ("red", "bright-blue") and hex strings ("#FF00FF").
// A nil Color or noColor{} means "unset" — use terminal default.
type ConfigColor struct {
	color.Color
}

// noColor is the "explicitly unset" sentinel: an opaque black whose presence
// (vs a nil Color) distinguishes an explicit terminal-default from an
// unconfigured field. It mirrors lipgloss.NoColor's RGBA contract.
type noColor struct{}

// RGBA reports opaque black, matching the lipgloss.NoColor contract.
func (noColor) RGBA() (r, g, b, a uint32) { return 0x0, 0x0, 0x0, 0xFFFF }

// IsSet returns true if the color is explicitly configured (not nil or noColor).
func (c ConfigColor) IsSet() bool {
	if c.Color == nil {
		return false
	}
	_, ok := c.Color.(noColor)
	return !ok
}

// parseHexColor converts a "#RGB" or "#RRGGBB" string to an opaque color.RGBA.
// The caller validates the length (4 or 7) before calling. On an invalid hex
// nibble it returns noColor{} (an unset sentinel), exactly mirroring the former
// lipgloss.Color(s), which returned lipgloss.NoColor{} on a parse error — so a
// valid-length-but-invalid-character hex falls back to the terminal default
// (IsSet false) rather than rendering as opaque black.
func parseHexColor(s string) color.Color {
	bad := false
	hb := func(b byte) byte {
		switch {
		case b >= '0' && b <= '9':
			return b - '0'
		case b >= 'a' && b <= 'f':
			return b - 'a' + 10
		case b >= 'A' && b <= 'F':
			return b - 'A' + 10
		}
		bad = true
		return 0
	}
	c := color.RGBA{A: 0xff}
	switch len(s) {
	case 7:
		c.R = hb(s[1])<<4 + hb(s[2])
		c.G = hb(s[3])<<4 + hb(s[4])
		c.B = hb(s[5])<<4 + hb(s[6])
	case 4:
		c.R = hb(s[1]) * 17
		c.G = hb(s[2]) * 17
		c.B = hb(s[3]) * 17
	}
	if bad {
		return noColor{}
	}
	return c
}

// ansiNames maps lowercase ANSI color names to x/ansi color constants.
var ansiNames = map[string]color.Color{
	"black":          ansi.Black,
	"red":            ansi.Red,
	"green":          ansi.Green,
	"yellow":         ansi.Yellow,
	"blue":           ansi.Blue,
	"magenta":        ansi.Magenta,
	"cyan":           ansi.Cyan,
	"white":          ansi.White,
	"bright-black":   ansi.BrightBlack,
	"bright-red":     ansi.BrightRed,
	"bright-green":   ansi.BrightGreen,
	"bright-yellow":  ansi.BrightYellow,
	"bright-blue":    ansi.BrightBlue,
	"bright-magenta": ansi.BrightMagenta,
	"bright-cyan":    ansi.BrightCyan,
	"bright-white":   ansi.BrightWhite,
}

// UnmarshalYAML parses a color from YAML. Accepts ANSI names or hex strings.
// Surrounding whitespace is trimmed first: goccy hands a block-mapping scalar
// the raw node bytes including a trailing newline (e.g. "red\n" when a sibling
// key follows on the next line), which would otherwise fail the name lookup.
func (c *ConfigColor) UnmarshalYAML(b []byte) error {
	if len(b) == 0 {
		return nil
	}

	// Strip surrounding whitespace, then quotes.
	s := strings.TrimSpace(string(b))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}

	if s == "" {
		return nil
	}

	// Hex color: accept #RGB (4 chars) or #RRGGBB (7 chars)
	if s[0] == '#' {
		if len(s) != 4 && len(s) != 7 {
			return fmt.Errorf("unknown color name %q; use a hex color (#RRGGBB) or an ANSI name (red, bright-blue, etc.)", s)
		}
		c.Color = parseHexColor(s)
		return nil
	}

	// ANSI name
	name := strings.ToLower(s)
	if col, ok := ansiNames[name]; ok {
		c.Color = col
		return nil
	}

	return fmt.Errorf("unknown color name %q; use a hex color (#RRGGBB) or an ANSI name (red, bright-blue, etc.)", s)
}

// String returns a human-readable representation of the color.
func (c ConfigColor) String() string {
	if !c.IsSet() {
		return "<unset>"
	}
	// Reverse lookup ANSI name
	for name, col := range ansiNames {
		if c.Color == col {
			return name
		}
	}
	// Hex fallback
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// MarshalYAML emits the ANSI name or hex form produced by String; an unset
// color marshals as a YAML null.
func (c ConfigColor) MarshalYAML() (any, error) {
	if !c.IsSet() {
		return nil, nil
	}
	return c.String(), nil
}

// ColorRule colorizes a substring match in rendered log lines.
// A rule with both Fg and Bg unset is valid: it overrides any prior rule
// with the same Match (default or earlier extra) and is then dropped from
// the engine's effective list, which lets users disable individual default
// rules without flipping IncludeDefaultColorRules.
type ColorRule struct {
	Match string      `yaml:"match" desc:"substring to colorize (case-sensitive, plain substring match)"`
	Fg    ConfigColor `yaml:"fg"    desc:"foreground color for matched substring (optional if bg is set)"`
	Bg    ConfigColor `yaml:"bg"    desc:"background color for matched substring (optional if fg is set)"`
}

// ColorRuleList is a slice of ColorRule. The list is resolved at engine init
// via upsert-by-Match (later wins) — duplicate Match strings within or across
// the default/extra slices are accepted and resolved by order, not flagged
// at config load.
type ColorRuleList []ColorRule

// UnmarshalYAML decodes a ColorRule and rejects entries with an empty Match.
// Any combination of Fg/Bg presence (including neither) is accepted.
// Errors are anchored to the YAML token so line/column info is included.
func (r *ColorRule) UnmarshalYAML(node ast.Node) error {
	type raw ColorRule
	var v raw
	if err := yaml.Unmarshal([]byte(node.String()), &v); err != nil {
		return err
	}
	if v.Match == "" {
		return errors.New(yaml.FormatErrorWithToken("extraColorRules entry: 'match' must be a non-empty string", node.GetToken(), false, true))
	}
	*r = ColorRule(v)
	return nil
}

// NotifyStyle selects the mechanism used to deliver a fetch-complete
// notification (gated by Core.NotifyOnFetchDone). Beyond the plain audible
// "bell", the osc* styles emit a desktop-notification escape sequence that only
// the supporting terminals render (osc9: iTerm2/WezTerm/Ghostty/kitty; osc777:
// foot/urxvt/WezTerm/Ghostty; osc99: kitty/foot). Inside tmux the runtime
// additionally DCS-passthrough-wraps the osc* sequences, which the user's tmux
// must permit via `allow-passthrough on`.
type NotifyStyle string

const (
	NotifyStyleBell   NotifyStyle = "bell"
	NotifyStyleOSC9   NotifyStyle = "osc9"
	NotifyStyleOSC777 NotifyStyle = "osc777"
	NotifyStyleOSC99  NotifyStyle = "osc99"
)

// UnmarshalYAML parses a NotifyStyle, rejecting unknown values so a typo fails
// loudly at load instead of silently degrading to the default. An empty value
// is left as-is so the newConfig default survives.
func (n *NotifyStyle) UnmarshalYAML(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" {
		return nil
	}
	switch NotifyStyle(s) {
	case NotifyStyleBell, NotifyStyleOSC9, NotifyStyleOSC777, NotifyStyleOSC99:
		*n = NotifyStyle(s)
		return nil
	default:
		return fmt.Errorf("unknown notifyStyle %q; use one of: bell, osc9, osc777, osc99", s)
	}
}

type versionOnly struct {
	Value int `yaml:"version"`
}

// CRN holds a validated ICL instance CRN. The full validated string is kept
// verbatim in raw so String/MarshalYAML round-trip losslessly; only the
// segments actually read elsewhere are exposed as fields.
type CRN struct {
	raw        string // the validated canonical CRN string, verbatim
	CName      string // segment 2 — maps to the ICL environment
	Location   string // segment 5
	ScopeID    string // account ID (the part after "a/")
	InstanceID string // segment 7
}

func CRNFromString(crn string) (*CRN, error) {
	segments := strings.Split(crn, ":")
	if len(segments) != 10 {
		return nil, errors.New("a CRN must have 10 segments")
	}

	crnSegment := segments[0]
	version := segments[1]
	cname := segments[2]
	serviceName := segments[4]
	location := segments[5]
	scope := segments[6]
	instanceID := segments[7]

	if crnSegment != "crn" {
		return nil, errors.New("first segment must be \"crn\"")
	}

	if version != "v1" {
		return nil, errors.New("only version 1 CRN is supported")
	}

	if cname == "" {
		return nil, errors.New("CName is required")
	}

	if serviceName != "logs" {
		return nil, errors.New("not an ICL instance CRN")
	}

	scopePrefix, scopeID, found := strings.Cut(scope, "/")
	if !found {
		return nil, errors.New("scope prefix missing")
	}
	if scopePrefix != "a" {
		return nil, errors.New("only account scope type is supported")
	}

	return &CRN{
		raw:        crn,
		CName:      cname,
		Location:   location,
		ScopeID:    scopeID,
		InstanceID: instanceID,
	}, nil
}

func MustCRNFromString(crn string) *CRN {
	parsed, err := CRNFromString(crn)
	if err != nil {
		panic(err)
	}
	return parsed
}

func (c *CRN) UnmarshalYAML(node ast.Node) error {
	if node.Type() != ast.StringType {
		return errors.New(yaml.FormatErrorWithToken("CRN must be a string", node.GetToken(), false, true))
	}

	crn, err := CRNFromString(node.GetToken().Value)
	if err != nil {
		return errors.New(yaml.FormatErrorWithToken(err.Error(), node.GetToken(), false, true))
	}
	*c = *crn
	return nil
}

func (c *CRN) String() string {
	return c.raw
}

func (c *CRN) MarshalYAML() (any, error) {
	return c.String(), nil
}
