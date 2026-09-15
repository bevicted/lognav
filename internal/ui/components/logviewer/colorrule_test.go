package logviewer

import (
	"testing"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shared string constants for logviewer package tests.
const (
	matchError = "error"
	matchWarn  = "warn"
	fieldMsg   = "msg"
	matchABC   = "abc"
)

func TestResolveColorRules(t *testing.T) {
	t.Parallel()

	red := config.ConfigColor{Color: ansi.Red}
	yellow := config.ConfigColor{Color: ansi.Yellow}
	blue := config.ConfigColor{Color: ansi.Blue}

	tests := []struct {
		name            string
		defaults        config.ColorRuleList
		extras          config.ColorRuleList
		includeDefaults bool
		want            config.ColorRuleList
	}{
		{
			name:            "defaults enabled, no extras",
			defaults:        config.ColorRuleList{{Match: matchError, Fg: red}},
			extras:          nil,
			includeDefaults: true,
			want:            config.ColorRuleList{{Match: matchError, Fg: red}},
		},
		{
			name:            "defaults disabled, no extras",
			defaults:        config.ColorRuleList{{Match: matchError, Fg: red}},
			extras:          nil,
			includeDefaults: false,
			want:            config.ColorRuleList{},
		},
		{
			name:            "defaults disabled, extras present, one no-color filtered",
			defaults:        config.ColorRuleList{{Match: matchError, Fg: red}},
			extras:          config.ColorRuleList{{Match: matchWarn, Fg: yellow}, {Match: "drop"}},
			includeDefaults: false,
			want:            config.ColorRuleList{{Match: matchWarn, Fg: yellow}},
		},
		{
			name:            "defaults enabled, extras append new with colors",
			defaults:        config.ColorRuleList{{Match: matchError, Fg: red}},
			extras:          config.ColorRuleList{{Match: matchWarn, Fg: yellow}},
			includeDefaults: true,
			want:            config.ColorRuleList{{Match: matchError, Fg: red}, {Match: matchWarn, Fg: yellow}},
		},
		{
			name:            "defaults enabled, extras override default Match in place",
			defaults:        config.ColorRuleList{{Match: matchError, Fg: red}, {Match: matchWarn, Fg: yellow}},
			extras:          config.ColorRuleList{{Match: matchError, Fg: blue}},
			includeDefaults: true,
			want:            config.ColorRuleList{{Match: matchError, Fg: blue}, {Match: matchWarn, Fg: yellow}},
		},
		{
			name:            "defaults enabled, extra disables default by no colors",
			defaults:        config.ColorRuleList{{Match: matchError, Fg: red}, {Match: matchWarn, Fg: yellow}},
			extras:          config.ColorRuleList{{Match: matchError}},
			includeDefaults: true,
			want:            config.ColorRuleList{{Match: matchWarn, Fg: yellow}},
		},
		{
			name:            "defaults enabled, no-color extra with new Match is noop",
			defaults:        config.ColorRuleList{{Match: matchError, Fg: red}},
			extras:          config.ColorRuleList{{Match: "noop"}},
			includeDefaults: true,
			want:            config.ColorRuleList{{Match: matchError, Fg: red}},
		},
		{
			name:            "extras list contains two same Match, later wins in earlier position",
			defaults:        nil,
			extras:          config.ColorRuleList{{Match: "x", Fg: red}, {Match: "y", Fg: yellow}, {Match: "x", Fg: blue}},
			includeDefaults: true,
			want:            config.ColorRuleList{{Match: "x", Fg: blue}, {Match: "y", Fg: yellow}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveColorRules(tt.defaults, tt.extras, tt.includeDefaults)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewColorRuleEngineEmpty(t *testing.T) {
	t.Parallel()
	e := newColorRuleEngine(nil)
	require.NotNil(t, e)
	assert.False(t, e.enabled)
	assert.Empty(t, e.styles)
}

func TestNewColorRuleEngineNonEmpty(t *testing.T) {
	t.Parallel()
	rules := config.ColorRuleList{
		{Match: matchError, Fg: config.ConfigColor{Color: ansi.Red}},
		{Match: "ok", Bg: config.ConfigColor{Color: ansi.Green}},
	}
	e := newColorRuleEngine(rules)
	require.NotNil(t, e)
	assert.True(t, e.enabled)
	require.Len(t, e.styles, 2)
	assert.Equal(t, ansi.Red, e.styles[0].Fg)
	assert.Nil(t, e.styles[0].Bg)
	assert.Nil(t, e.styles[1].Fg)
	assert.Equal(t, ansi.Green, e.styles[1].Bg)
}

func TestColorRuleEngineScan(t *testing.T) {
	t.Parallel()

	red := config.ConfigColor{Color: ansi.Red}
	green := config.ConfigColor{Color: ansi.Green}
	blue := config.ConfigColor{Color: ansi.Blue}

	type call struct {
		rules config.ColorRuleList
		line  string
	}

	tests := []struct {
		name string
		call call
		want []matchSpan
	}{
		{
			name: "disabled engine returns nil",
			call: call{rules: nil, line: "error here"},
			want: nil,
		},
		{
			name: "empty line returns nil",
			call: call{rules: config.ColorRuleList{{Match: matchError, Fg: red}}, line: ""},
			want: nil,
		},
		{
			name: "single non-overlapping match",
			call: call{
				rules: config.ColorRuleList{{Match: matchError, Fg: red}},
				line:  "saw error here",
			},
			want: []matchSpan{{Start: 4, End: 9, Style: uv.Style{Fg: ansi.Red}}},
		},
		{
			name: "no match returns nil",
			call: call{
				rules: config.ColorRuleList{{Match: "panic", Fg: red}},
				line:  "saw error here",
			},
			want: nil,
		},
		{
			name: "case sensitive: 'Error' does not match 'error'",
			call: call{
				rules: config.ColorRuleList{{Match: matchError, Fg: red}},
				line:  "Error condition",
			},
			want: nil,
		},
		{
			name: "longer match wins on overlap at same start",
			call: call{
				rules: config.ColorRuleList{
					{Match: matchError, Fg: red},
					{Match: "errors", Fg: green},
				},
				line: "errors found",
			},
			want: []matchSpan{{Start: 0, End: 6, Style: uv.Style{Fg: ansi.Green}}},
		},
		{
			name: "same-start same-length tie: first rule wins",
			call: call{
				rules: config.ColorRuleList{
					{Match: matchABC, Fg: red},
					{Match: matchABC, Fg: blue},
				},
				line: matchABC,
			},
			// Test passes raw rules with duplicate Match directly to engine,
			// bypassing resolveColorRules. patternIdx 0 = red, 1 = blue.
			// Tie breaks by patternIdx asc, so red wins.
			want: []matchSpan{{Start: 0, End: 3, Style: uv.Style{Fg: ansi.Red}}},
		},
		{
			name: "adjacent non-overlapping matches kept",
			call: call{
				rules: config.ColorRuleList{
					{Match: "ab", Fg: red},
					{Match: "cd", Fg: blue},
				},
				line: "abcd",
			},
			want: []matchSpan{
				{Start: 0, End: 2, Style: uv.Style{Fg: ansi.Red}},
				{Start: 2, End: 4, Style: uv.Style{Fg: ansi.Blue}},
			},
		},
		{
			name: "wide rune line: column offsets correct",
			call: call{
				rules: config.ColorRuleList{{Match: "x", Fg: red}},
				// "日本x" — 日 (width 2) + 本 (width 2) + x (width 1).
				// "x" starts at column 4.
				line: "日本x",
			},
			want: []matchSpan{{Start: 4, End: 5, Style: uv.Style{Fg: ansi.Red}}},
		},
		{
			name: "multiple distinct patterns interleaved kept in source order",
			call: call{
				rules: config.ColorRuleList{
					{Match: "a", Fg: red},
					{Match: "b", Fg: blue},
				},
				line: "a b a",
			},
			want: []matchSpan{
				{Start: 0, End: 1, Style: uv.Style{Fg: ansi.Red}},
				{Start: 2, End: 3, Style: uv.Style{Fg: ansi.Blue}},
				{Start: 4, End: 5, Style: uv.Style{Fg: ansi.Red}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newColorRuleEngine(tt.call.rules)
			lineBytes := []byte(tt.call.line)
			got := e.scan(lineBytes, byteToCol(lineBytes))
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestColorRuleEngine_Scan_PrecomputedColMap_UsesProvidedMap proves scan
// reads the caller's cm and does NOT rebuild internally. Uses a synthetic
// cm where each byte maps to its index * 2 (doubling) so a rebuilt
// mapping would produce different Start/End values.
func TestColorRuleEngine_Scan_PrecomputedColMap_UsesProvidedMap(t *testing.T) {
	t.Parallel()
	rules := config.ColorRuleList{
		{Match: matchABC, Fg: config.ConfigColor{Color: ansi.Red}},
	}
	e := newColorRuleEngine(rules)
	require.True(t, e.enabled)
	line := []byte(matchABC)
	// Synthetic doubling cm: each byte maps to its index * 2.
	cm := colMap{0, 2, 4, 6}
	spans := e.scan(line, cm)
	require.Len(t, spans, 1)
	assert.Equal(t, 0, spans[0].Start)
	assert.Equal(t, 6, spans[0].End) // 3 * 2, not 3
}

// TestColorRuleEngine_Scan_Disabled_ReturnsNil proves the disabled-engine
// fast path returns nil before touching cm. Passing nil cm in this state
// is safe.
func TestColorRuleEngine_Scan_Disabled_ReturnsNil(t *testing.T) {
	t.Parallel()
	e := newColorRuleEngine(nil)
	require.False(t, e.enabled)
	assert.Nil(t, e.scan([]byte("anything"), nil))
}

func TestApplyMatchSpans(t *testing.T) {
	t.Parallel()

	const sideBar = 1
	const total = 12
	canvas := uv.NewScreenBuffer(total, 1)
	for x := range total {
		canvas.SetCell(x, 0, &uv.Cell{Content: " ", Width: 1, Style: uv.Style{}})
	}

	spans := []matchSpan{
		{Start: 0, End: 5, Style: uv.Style{Fg: ansi.Red}},         // "error" at columns 0..5 of the line
		{Start: 6, End: 8, Style: uv.Style{Bg: ansi.BrightWhite}}, // bg-only span at 6..8
	}

	applyMatchSpans(canvas, 0 /*row*/, 0 /*colOffset*/, 0 /*xOffset*/, sideBar, total, spans)

	// Span 1: cells [1..6) get Fg=Red, Bg unchanged (nil).
	for x := 1; x < 6; x++ {
		cell := canvas.CellAt(x, 0)
		require.NotNil(t, cell)
		assert.Equal(t, ansi.Red, cell.Style.Fg, "cell %d should have Red Fg", x)
		assert.Nil(t, cell.Style.Bg, "cell %d Bg should be untouched", x)
	}

	// Cell at column 6 sits between spans → unchanged.
	c := canvas.CellAt(6, 0)
	require.NotNil(t, c)
	assert.Nil(t, c.Style.Fg)
	assert.Nil(t, c.Style.Bg)

	// Span 2: cells [7..9) get Bg=BrightWhite, Fg unchanged.
	for x := 7; x < 9; x++ {
		cell := canvas.CellAt(x, 0)
		require.NotNil(t, cell)
		assert.Nil(t, cell.Style.Fg, "cell %d Fg should be untouched", x)
		assert.Equal(t, ansi.BrightWhite, cell.Style.Bg, "cell %d should have BrightWhite Bg", x)
	}
}

func TestApplyMatchSpansClipping(t *testing.T) {
	t.Parallel()

	const sideBar = 1
	const total = 6
	canvas := uv.NewScreenBuffer(total, 1)
	for x := range total {
		canvas.SetCell(x, 0, &uv.Cell{Content: " ", Width: 1, Style: uv.Style{}})
	}

	// xOffset=2 scrolls the line right; span at columns [0,4) should land
	// at canvas columns [-2+1, 4-2+1) = [-1, 3). Cells with x<sideBar (i.e. <1) are clipped.
	spans := []matchSpan{{Start: 0, End: 4, Style: uv.Style{Fg: ansi.Red}}}
	applyMatchSpans(canvas, 0 /*row*/, 0 /*colOffset*/, 2 /*xOffset*/, sideBar, total, spans)

	// Cells [1..3) should be Red. Cells outside that range untouched.
	assert.Equal(t, ansi.Red, canvas.CellAt(1, 0).Style.Fg)
	assert.Equal(t, ansi.Red, canvas.CellAt(2, 0).Style.Fg)
	assert.Nil(t, canvas.CellAt(0, 0).Style.Fg, "sidebar column should not be touched")
	assert.Nil(t, canvas.CellAt(3, 0).Style.Fg, "post-span column should not be touched")
}

// TestNew_BuildsIndependentPerModelRuleEngine proves each Model builds its own
// colorRuleEngine from its own bundle.Config — the regression the former
// sync.Once package-global masked (the first New froze the rule set for the process).
func TestNew_BuildsIndependentPerModelRuleEngine(t *testing.T) {
	t.Parallel()

	red := config.ConfigColor{Color: ansi.Red}
	yellow := config.ConfigColor{Color: ansi.Yellow}

	bundle1 := depstest.NewTest(t)
	bundle1.Config.Logs.IncludeDefaultColorRules = false
	bundle1.Config.Logs.ExtraColorRules = config.ColorRuleList{{Match: matchError, Fg: red}}

	bundle2 := depstest.NewTest(t)
	bundle2.Config.Logs.IncludeDefaultColorRules = false
	bundle2.Config.Logs.ExtraColorRules = config.ColorRuleList{{Match: matchWarn, Fg: yellow}}

	m1 := New(bundle1)
	m2 := New(bundle2)

	require.NotNil(t, m1.ruleEngine)
	require.NotNil(t, m2.ruleEngine)
	require.NotSame(t, m1.ruleEngine, m2.ruleEngine)

	// Each engine reflects only its own bundle's config: m1 matches "error"
	// but not "warn"; m2 the reverse.
	assert.Len(t, m1.ruleEngine.scan([]byte(matchError), byteToCol([]byte(matchError))), 1)
	assert.Empty(t, m1.ruleEngine.scan([]byte(matchWarn), byteToCol([]byte(matchWarn))))
	assert.Empty(t, m2.ruleEngine.scan([]byte(matchError), byteToCol([]byte(matchError))))
	assert.Len(t, m2.ruleEngine.scan([]byte(matchWarn), byteToCol([]byte(matchWarn))), 1)
}
