package list

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bevicted/lognav/internal/deps/depstest"
)

// TestBindKeyhandlersToModel_RegistersExpectedActions verifies that
// bindKeyhandlersToModel registers bindings for every context that the list
// component is documented to handle. Contexts are the stable identifiers per
// T7 convention (b.Context, not b.Action.Name()).
func TestBindKeyhandlersToModel_RegistersExpectedActions(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	bindings := m.GetKeybinds()
	assert.NotEmpty(t, bindings)

	// Build a set of registered contexts for O(1) lookup.
	contexts := make(map[string]struct{}, len(bindings))
	for _, b := range bindings {
		contexts[b.Context] = struct{}{}
	}

	// alwaysHandle — fire regardless of focus state.
	expectedAlways := []string{
		"confirm text input",
		"move cursor to the previous item",
		"move cursor to the next item",
	}
	for _, ctx := range expectedAlways {
		assert.Contains(t, contexts, ctx, "alwaysHandle binding %q must be registered", ctx)
	}

	// regular — fire only when the textinput is not focused.
	expectedRegular := []string{
		"move to top",
		"move one page up", // MovePageUp and MoveHalfPageUp share this Context string
		"move one line up",
		"move one line down",
		"move half page down",
		"move page down",
		"move to bottom",
		"focus fuzzy finder",
	}
	for _, ctx := range expectedRegular {
		assert.Contains(t, contexts, ctx, "regular binding %q must be registered", ctx)
	}

	// ti — fire only when the textinput is focused.
	expectedTI := []string{
		"cancel text input",
	}
	for _, ctx := range expectedTI {
		assert.Contains(t, contexts, ctx, "ti binding %q must be registered", ctx)
	}
}
