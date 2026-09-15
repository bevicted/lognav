package config

import (
	"image/color"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matchError is the canonical color-rule match string reused across tests
// (extracted to satisfy goconst).
const matchError = "error"

func TestConfigColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    color.Color
		wantErr string
	}{
		{name: "hex color", input: `"#001234"`, want: parseHexColor("#001234")},
		{name: "ansi red", input: `"red"`, want: ansi.Red},
		{name: "ansi bright-blue", input: `"bright-blue"`, want: ansi.BrightBlue},
		{name: "ansi white", input: `"white"`, want: ansi.White},
		{name: "ansi black", input: `"black"`, want: ansi.Black},
		{name: "ansi bright-black", input: `"bright-black"`, want: ansi.BrightBlack},
		{name: "ansi green", input: `"green"`, want: ansi.Green},
		{name: "ansi yellow", input: `"yellow"`, want: ansi.Yellow},
		{name: "ansi blue", input: `"blue"`, want: ansi.Blue},
		{name: "ansi magenta", input: `"magenta"`, want: ansi.Magenta},
		{name: "ansi cyan", input: `"cyan"`, want: ansi.Cyan},
		{name: "ansi bright-red", input: `"bright-red"`, want: ansi.BrightRed},
		{name: "ansi bright-green", input: `"bright-green"`, want: ansi.BrightGreen},
		{name: "ansi bright-yellow", input: `"bright-yellow"`, want: ansi.BrightYellow},
		{name: "ansi bright-magenta", input: `"bright-magenta"`, want: ansi.BrightMagenta},
		{name: "ansi bright-cyan", input: `"bright-cyan"`, want: ansi.BrightCyan},
		{name: "ansi bright-white", input: `"bright-white"`, want: ansi.BrightWhite},
		{name: "empty", input: ``, want: nil},
		{name: "unknown name", input: `"purple"`, wantErr: "unknown color name"},
		{name: "invalid hex length", input: `"#01"`, wantErr: "unknown color name"},
		// Valid length, invalid nibble: lipgloss.Color swallowed this to
		// NoColor (unset) without erroring; parseHexColor must preserve that
		// rather than render opaque black. See TestConfigColorIsSet.
		{name: "invalid hex nibble", input: `"#GGGGGG"`, want: noColor{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c ConfigColor
			err := c.UnmarshalYAML([]byte(tt.input))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, c.Color)
		})
	}
}

func TestNotifyStyle_UnmarshalYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    NotifyStyle
		wantErr string
	}{
		{name: "bell", input: `"bell"`, want: NotifyStyleBell},
		{name: "osc9", input: `"osc9"`, want: NotifyStyleOSC9},
		{name: "osc777", input: `"osc777"`, want: NotifyStyleOSC777},
		{name: "osc99", input: `"osc99"`, want: NotifyStyleOSC99},
		{name: "unquoted", input: `osc9`, want: NotifyStyleOSC9},
		// Empty is left untouched so the newConfig default survives a present-but-blank key.
		{name: "empty keeps zero value", input: ``, want: ""},
		{name: "unknown rejected", input: `"osc-9"`, wantErr: "unknown notifyStyle"},
		{name: "bogus rejected", input: `"flash"`, wantErr: "unknown notifyStyle"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var n NotifyStyle
			err := n.UnmarshalYAML([]byte(tt.input))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, n)
		})
	}
}

// TestNotifyStyle_DefaultIsOSC9 pins the corporate-macOS default: osc9 reaches
// the widest set of macOS terminals (iTerm2/WezTerm/Ghostty/kitty).
func TestNotifyStyle_DefaultIsOSC9(t *testing.T) {
	t.Parallel()
	assert.Equal(t, NotifyStyleOSC9, newConfig().Core.NotifyStyle)
}

func TestConfigColorIsSet(t *testing.T) {
	t.Parallel()

	assert.True(t, ConfigColor{Color: ansi.Red}.IsSet())
	assert.True(t, ConfigColor{Color: parseHexColor("#001234")}.IsSet())
	assert.False(t, ConfigColor{}.IsSet())
	assert.False(t, ConfigColor{Color: noColor{}}.IsSet())

	// End-to-end: a valid-length but invalid-nibble hex unmarshals without
	// error and reports unset (terminal default), matching the former
	// lipgloss.Color behavior — not opaque black with IsSet true.
	var badHex ConfigColor
	require.NoError(t, badHex.UnmarshalYAML([]byte(`"#GGGGGG"`)))
	assert.False(t, badHex.IsSet())
}

func TestConfigColorYAMLRoundtrip(t *testing.T) {
	t.Parallel()

	s := struct {
		C ConfigColor `yaml:"c"`
	}{}
	require.NoError(t,
		yaml.UnmarshalWithOptions(
			[]byte(`c: "#001234"`),
			&s,
			yaml.DisallowUnknownField(),
		),
	)
	assert.Equal(t, parseHexColor("#001234"), s.C.Color)
}

func TestColorRuleUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    ColorRule
		wantErr string
	}{
		{
			name:  "valid fg only",
			input: "match: \"error\"\nfg: \"red\"\n",
			want:  ColorRule{Match: matchError, Fg: ConfigColor{ansi.Red}},
		},
		{
			name:  "valid fg and bg",
			input: "match: \"ERROR\"\nfg: \"white\"\nbg: \"red\"\n",
			want:  ColorRule{Match: "ERROR", Fg: ConfigColor{ansi.White}, Bg: ConfigColor{ansi.Red}},
		},
		{
			name:  "valid bg only",
			input: "match: \"warn\"\nbg: \"yellow\"\n",
			want:  ColorRule{Match: "warn", Bg: ConfigColor{ansi.Yellow}},
		},
		{
			name:  "unquoted fg before match (block trailing newline)",
			input: "fg: red\nmatch: error\n",
			want:  ColorRule{Match: matchError, Fg: ConfigColor{ansi.Red}},
		},
		{
			name:  "no colors is valid (disable marker)",
			input: "match: \"error\"\n",
			want:  ColorRule{Match: matchError},
		},
		{
			name:    "empty match errors",
			input:   "match: \"\"\nfg: \"red\"\n",
			wantErr: "'match' must be a non-empty string",
		},
		{
			name:    "missing match errors",
			input:   "fg: \"red\"\n",
			wantErr: "'match' must be a non-empty string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r ColorRule
			err := yaml.Unmarshal([]byte(tt.input), &r)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, r)
		})
	}
}

func TestLogsConfigColorRuleDefaults(t *testing.T) {
	t.Parallel()
	c := newConfig()
	require.True(t, c.Logs.IncludeDefaultColorRules, "include defaults should be true by default")
	require.Len(t, c.Logs.DefaultColorRules, 1, "expected exactly one default color rule")
	rule := c.Logs.DefaultColorRules[0]
	assert.Equal(t, matchError, rule.Match)
	assert.Equal(t, ansi.Red, rule.Fg.Color)
	assert.Nil(t, rule.Bg.Color)
	assert.Empty(t, c.Logs.ExtraColorRules)
}

func TestCRNFromString_ConfiguredCName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		crn     string
		wantErr bool
		cname   string
		scopeID string
	}{
		{
			name:    "prod CRN",
			crn:     "crn:v1:bluemix:public:logs:us-south:a/account-id:instance-id::",
			cname:   "bluemix",
			scopeID: "account-id",
		},
		{
			name:    "custom CName",
			crn:     "crn:v1:test-cloud:public:logs:us-south:a/account-id:instance-id::",
			cname:   "test-cloud",
			scopeID: "account-id",
		},
		{
			name:    "empty CName",
			crn:     "crn:v1::public:logs:us-south:a/account-id:instance-id::",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CRNFromString(tt.crn)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.CName != tt.cname {
				t.Errorf("CName = %q, want %q", got.CName, tt.cname)
			}
			if got.ScopeID != tt.scopeID {
				t.Errorf("ScopeID = %q, want %q", got.ScopeID, tt.scopeID)
			}
		})
	}
}

// TestCRN_StringRoundTrip asserts String() returns the original CRN verbatim,
// including segments not exposed as fields (CType, ResourceType, Resource) --
// the property that lets config marshal/unmarshal round-trip without corruption.
func TestCRN_StringRoundTrip(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"crn:v1:bluemix:public:logs:us-south:a/account-id:instance-id::",
		"crn:v1:test-cloud:public:logs:eu-de:a/acct:inst:typ:res",
	} {
		c, err := CRNFromString(s)
		require.NoError(t, err)
		assert.Equal(t, s, c.String(), "String must return the original CRN verbatim")
	}
}
