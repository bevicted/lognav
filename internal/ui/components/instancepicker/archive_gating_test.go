package instancepicker

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/state"
)

func pickerWithExperimental(t *testing.T, enabled bool) *Model {
	t.Helper()
	cfg := config.New()
	cfg.Core.EnableExperimental = enabled
	return New(t.Context(), deps.New(cfg, state.New()))
}

func hasBindingContext(m *Model, context string) bool {
	for _, b := range m.kh.GetKeybinds() {
		if b.Context == context {
			return true
		}
	}
	return false
}

// First fetch owns shift+f in every configuration. Archive dispatch remains
// implemented but no longer has that user-facing binding.
func TestBindKeyhandlers_FirstFetchReplacesArchiveDispatch(t *testing.T) {
	t.Parallel()
	for _, experimental := range []bool{false, true} {
		t.Run("experimental="+strconv.FormatBool(experimental), func(t *testing.T) {
			t.Parallel()
			m := pickerWithExperimental(t, experimental)
			assert.True(t, hasBindingContext(m, "race enabled instances until the first logs arrive"))
			assert.False(t, hasBindingContext(m, "dispatch enabled instances' query as background (archive) queries"))
		})
	}
}
