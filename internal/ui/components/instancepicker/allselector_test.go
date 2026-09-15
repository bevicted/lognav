package instancepicker

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedNamedInstances replaces the model's instances with the given named set and
// resets the inner list's items to plain name rows, so the fuzzy finder filters
// on instance names. It returns the instances keyed by name for assertions. All
// seeded instances start in the zero (Disabled) state.
func seedNamedInstances(t *testing.T, m *Model, names ...string) map[string]*Instance {
	t.Helper()
	bundle := depstest.NewTest(t)
	insts := make(Instances, len(names))
	items := make([][]list.Segment, len(names))
	byName := make(map[string]*Instance, len(names))
	for i, n := range names {
		inst := NewInstance(bundle, n, "http://example.com", "crn:"+n, "prod", "%.2fs")
		insts[i] = inst
		items[i] = list.PlainItem(n)
		byName[n] = inst
	}
	m.instances = insts
	m.list.WithItems(items)
	return byName
}

// TestInstancePicker_All_TogglesOnlyFilteredInstances pins the core behavior: with
// a fuzzy filter active, pressing the All key ('a') toggles only the instances
// currently visible under the filter and leaves the hidden ones untouched.
func TestInstancePicker_All_TogglesOnlyFilteredInstances(t *testing.T) {
	t.Parallel()
	m, _ := newTestModelWithKeybinds(t)
	by := seedNamedInstances(t, m, "us-east", "us-south", "eu-de", "jp-tok")

	// Filter to "us-" so only us-east and us-south are visible.
	m.list.OnPaste(uv.PasteEvent{Content: "us-"})
	require.Equal(t, 2, m.list.VisibleLen(), "filter must narrow to the two us-* instances")

	// All start Disabled; 'a' enables only the filtered subset.
	require.False(t, m.list.IsFocused(), "list must be unfocused so 'a' routes to the kh handler")
	m.HandleKey(keystest.PressRuneUV(t, 'a'))

	assert.True(t, by["us-east"].IsEnabled(), "filtered us-east must be enabled")
	assert.True(t, by["us-south"].IsEnabled(), "filtered us-south must be enabled")
	assert.False(t, by["eu-de"].IsEnabled(), "hidden eu-de must stay disabled")
	assert.False(t, by["jp-tok"].IsEnabled(), "hidden jp-tok must stay disabled")
}

// TestInstancePicker_All_DeselectsOnlyFilteredLeavesOthers mirrors the user's
// example: everything selected, filter to "us-", press 'a' → only the visible
// us-* are deselected; the hidden instances keep their selection.
func TestInstancePicker_All_DeselectsOnlyFilteredLeavesOthers(t *testing.T) {
	t.Parallel()
	m, _ := newTestModelWithKeybinds(t)
	by := seedNamedInstances(t, m, "us-east", "us-south", "eu-de", "jp-tok")

	// Start with everything selected.
	m.instances.SetAll(true)
	require.True(t, m.instances.AreAllEnabled())

	// Filter to "us-" then press 'a' — only the visible us-* get deselected.
	m.list.OnPaste(uv.PasteEvent{Content: "us-"})
	require.Equal(t, 2, m.list.VisibleLen(), "filter must narrow to the two us-* instances")
	m.HandleKey(keystest.PressRuneUV(t, 'a'))

	assert.False(t, by["us-east"].IsEnabled(), "filtered us-east must be deselected")
	assert.False(t, by["us-south"].IsEnabled(), "filtered us-south must be deselected")
	assert.True(t, by["eu-de"].IsEnabled(), "hidden eu-de must stay selected")
	assert.True(t, by["jp-tok"].IsEnabled(), "hidden jp-tok must stay selected")
}

// TestInstancePicker_All_NoFilter_TogglesEveryInstance verifies the unfiltered
// path is unchanged: 'a' toggles every instance.
func TestInstancePicker_All_NoFilter_TogglesEveryInstance(t *testing.T) {
	t.Parallel()
	m, _ := newTestModelWithKeybinds(t)
	by := seedNamedInstances(t, m, "us-east", "us-south", "eu-de")

	require.Equal(t, 3, m.list.VisibleLen(), "no filter: all instances visible")
	m.HandleKey(keystest.PressRuneUV(t, 'a'))

	for n, inst := range by {
		assert.True(t, inst.IsEnabled(), "no-filter 'a' must enable %s", n)
	}
}
