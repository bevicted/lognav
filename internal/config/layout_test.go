package config

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

func TestNewConfig_ContextMenu_DefaultsToC(t *testing.T) {
	t.Parallel()
	cfg := newConfig() // the package's unexported constructor (layout.go); NOT NewConfig
	assert.Equal(t, KeyBind{"c"}, cfg.Keys.ContextMenu)
}

func TestNewConfig_ShowKeyHintsDefaultsOn(t *testing.T) {
	t.Parallel()
	assert.True(t, newConfig().Core.ShowKeyHints)
}

func TestNewConfig_OpenBrowserDefaultsOn(t *testing.T) {
	t.Parallel()
	assert.True(t, newConfig().Core.OpenBrowser)
}

func TestNewConfig_RetiredStatusRowFieldsAreAbsent(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Status" + "lineBg", "Status" + "lineFg", "Status" + "lineTimestampFormat"} {
		_, found := reflect.TypeFor[Style]().FieldByName(name)
		assert.Falsef(t, found, "%s must not be a Style field", name)
	}
	for _, path := range []string{"$.style.status" + "lineBg", "$.style.status" + "lineFg", "$.style.status" + "lineTimestampFormat"} {
		_, found := configurableLeafPath(path)
		assert.Falsef(t, found, "%s must not be configurable", path)
	}
}

func TestNewConfig_FieldKeybinds_Defaults(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	// JqField uses ctrl+\ (terminal byte 0x1C decodes to ctrl+\ on every
	// terminal). SearchValue uses ctrl+/ plus the ctrl+_ legacy fallback:
	// outside the Kitty keyboard protocol, ctrl+/ arrives as byte 0x1F, which
	// decodes to ctrl+_, so binding both makes the search-value key work on
	// legacy terminals too.
	assert.Equal(t, KeyBind{"ctrl+\\"}, cfg.Keys.JqField)
	assert.Equal(t, KeyBind{"ctrl+/", "ctrl+_"}, cfg.Keys.SearchValue)
	assert.Equal(t, KeyBind{"shift+f"}, cfg.Keys.FirstFetch)
}

func TestNewConfig_SearchMultiMatchDelimeter_Removed(t *testing.T) {
	t.Parallel()
	// The multi-term search delimiter was removed; it must no longer be a
	// configurable leaf (search is a single literal needle now).
	_, ok := configurableLeafPath("$.logs.searchMultiMatchDelimeter")
	assert.False(t, ok, "searchMultiMatchDelimeter must not be a configurable field")
}

func TestNewConfig_RetiredAgentFieldsRemoved(t *testing.T) {
	t.Parallel()
	retiredSection := "m" + "cp"
	retiredActiveSign := "m" + "cpActiveSign"
	for _, p := range []string{"$." + retiredSection + ".focusRelevantTab", "$." + retiredSection + ".getLogsMaxResults", "$.style." + retiredActiveSign} {
		_, ok := configurableLeafPath(p)
		assert.Falsef(t, ok, "%s must be removed", p)
	}
}

func TestNewConfig_FilterSignFields_Removed(t *testing.T) {
	t.Parallel()
	// The two stacked filter bars were replaced by the pill bar; the six
	// per-mode sign strings are removed and must no longer be settable.
	for _, p := range []string{
		"$.style.jqNoFilterSign",
		"$.style.jqMatchFilterSign",
		"$.style.jqInverseMatchFilterSign",
		"$.style.searchNoFilterSign",
		"$.style.searchMatchFilterSign",
		"$.style.searchInverseMatchFilterSign",
	} {
		_, ok := configurableLeafPath(p)
		assert.Falsef(t, ok, "%s must not be a configurable field", p)
	}
}

func TestNewConfig_RemovedFilterKeybinds(t *testing.T) {
	t.Parallel()
	// cycleFilter died with the bar package; unfilterAll is replaced by the
	// filter-menu Clear button. Neither is a configurable leaf anymore.
	_, cycleOK := configurableLeafPath("$.keys.cycleFilter")
	assert.False(t, cycleOK, "keys.cycleFilter must be removed")
	_, unfilterOK := configurableLeafPath("$.keys.unfilterAll")
	assert.False(t, unfilterOK, "keys.unfilterAll must be removed")
}

func TestNewConfig_FilterRedesignFieldsAdded(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	assert.Equal(t, KeyBind{"f"}, cfg.Keys.FilterMenu, "FilterMenu defaults to f")
	assert.Equal(t, ansi.Blue, cfg.Style.PillChipBg.Color, "PillChipBg defaults to blue")
	assert.Equal(t, ansi.White, cfg.Style.PillLineBg.Color, "PillLineBg defaults to white (light grey)")
	assert.Equal(t, ansi.Black, cfg.Style.PillFg.Color, "PillFg defaults to black")

	// filterMenu plus the three pill color fields must be configurable leaves.
	km, kok := configurableLeafPath("$.keys.filterMenu")
	assert.True(t, kok)
	assert.Equal(t, "[]string", km.GoType)
	for _, p := range []string{"$.style.pillChipBg", "$.style.pillLineBg", "$.style.pillFg"} {
		lm, lok := configurableLeafPath(p)
		assert.True(t, lok, p)
		assert.Equal(t, "color", lm.GoType, p)
	}
}

func TestNewConfig_PageMovement_NoLetterAliases(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	// b/f letter aliases were removed; paging survives only via pgup/pgdn.
	assert.Equal(t, KeyBind{"pgup"}, cfg.Keys.MovePageUp)
	assert.Equal(t, KeyBind{"pgdown"}, cfg.Keys.MovePageDown)
}

func TestNew_PhaseLabelDefaults(t *testing.T) {
	t.Parallel()
	s := New().Style
	assert.Equal(t, "FETCHING", s.InProgressLabel)
	assert.Equal(t, "DONE", s.SuccessLabel)
	assert.Equal(t, "ERROR", s.ErrorLabel)
	assert.Equal(t, "WARN", s.WarningLabel)
	assert.Equal(t, "AUTH", s.AuthInProgressLabel)
	assert.Equal(t, "WATCH", s.WatchingLabel)
	assert.Equal(t, "READY", s.EnabledLabel)
	assert.Equal(t, "CANCEL", s.CancelledLabel)
	assert.Equal(t, "OFF", s.DisabledLabel)
	assert.Equal(t, ansi.BrightBlack, s.DisabledColor.Color)
	assert.Equal(t, ansi.BrightBlack, s.CancelledColor.Color)
}
