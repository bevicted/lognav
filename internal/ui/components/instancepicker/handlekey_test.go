package instancepicker

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestModelWithKeybinds constructs a *Model with keybinds bound,
// suitable for HandleKey tests. A fakePoster is injected so that
// list.Focus/Unfocus do not panic on a nil poster and so HandleKey's
// off-loop emit (InstanceSelectMsg) is recorded; the poster is returned so
// callers can assert on the posted events.
func newTestModelWithKeybinds(t *testing.T) (*Model, *fakePoster) {
	t.Helper()
	m := New(t.Context(), depstest.NewTest(t))
	fp := &fakePoster{}
	m.SetPoster(fp)
	return m, fp
}

func TestInstancePicker_HandleKey_AlwaysHandleHit(t *testing.T) {
	t.Parallel()
	// ctrl+s is bound to alwaysHandleKH (Snapshot key).
	tests := []struct {
		name  string
		input uv.KeyPressEvent
		want  component.KeyResult
	}{
		{
			name:  "alwaysHandle ctrl+s fires",
			input: keystest.PressCtrlUV(t, 's'),
			want:  component.KeyHandled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, fp := newTestModelWithKeybinds(t)
			// No instances loaded → no InstanceSelectMsg side effect.
			require.False(t, m.list.IsFocused(), "setup: list must not be focused")
			got := m.HandleKey(tt.input)
			assert.Equal(t, tt.want, got)

			// The snapshot action posts ManualSaveSnapshotMsg off-loop via the poster.
			var found bool
			for _, ev := range fp.events() {
				if _, ok := ev.(msgs.ManualSaveSnapshotMsg); ok {
					found = true
					break
				}
			}
			assert.True(t, found, "poster must record a ManualSaveSnapshotMsg when snapshot key is pressed")
		})
	}
}

func TestInstancePicker_HandleKey_KHKey(t *testing.T) {
	t.Parallel()
	// "q" is bound to m.kh (Quit). With list unfocused, m.kh is consulted
	// after the alwaysHandleKH miss.
	tests := []struct {
		name  string
		input uv.KeyPressEvent
		want  component.KeyResult
	}{
		{
			name:  "kh quit key handled",
			input: keystest.PressRuneUV(t, 'q'),
			want:  component.KeyHandled,
		},
		{
			name:  "unbound key still handled (list fall-through)",
			input: keystest.PressRuneUV(t, 'z'),
			// Even unbound keys are KeyHandled because the unfocused fall-through
			// always routes to list.HandleKey, which returns KeyHandled for
			// navigation keys. 'z' is NOT a list navigation key so the list returns
			// KeyIgnored, but the instancepicker's fall-through returns KeyHandled
			// unconditionally (matching the former Update arm that always called
			// list.Update regardless of what it consumed).
			want: component.KeyHandled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newTestModelWithKeybinds(t)
			require.False(t, m.list.IsFocused(), "setup: list must not be focused")
			got := m.HandleKey(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInstancePicker_HandleKey_ListFocused_RoutesToList(t *testing.T) {
	t.Parallel()
	// When the list is focused, all keys route to list.HandleKey (leaf-consumes-all).
	// An InstanceSelectMsg is emitted alongside the list's cmd.
	tests := []struct {
		name  string
		input uv.KeyPressEvent
		want  component.KeyResult
	}{
		{
			name:  "focused list consumes navigation key",
			input: keystest.PressRuneUV(t, 'j'),
			want:  component.KeyHandled,
		},
		{
			name:  "focused list consumes unbound key too (leaf-consumes-all)",
			input: keystest.PressRuneUV(t, 'z'),
			want:  component.KeyHandled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, fp := newTestModelWithKeybinds(t)
			// Add a dummy instance so the InstanceSelectMsg construction is safe.
			bundle := depstest.NewTest(t)
			inst := NewInstance(bundle, "test-inst", "http://example.com", testCRN("test"), "prod", "%.2fs")
			m.instances = Instances{inst}
			m.list.Focus()
			require.True(t, m.list.IsFocused(), "setup: list must be focused")
			got := m.HandleKey(tt.input)
			assert.Equal(t, tt.want, got)

			// The focused-list path emits an InstanceSelectMsg off-loop (via the
			// poster) to keep the statusline/logviewer in sync with the cursor.
			// Assert the fakePoster recorded an InstanceSelectMsg naming the
			// instance under the (focused) cursor.
			assert.True(t, postedInstanceSelect(fp, "test-inst"),
				"poster must record an InstanceSelectMsg for the focused instance")
		})
	}
}

// postedInstanceSelect reports whether the fake poster recorded an
// InstanceSelectMsg whose Name == name (the HandleKey path now emits it
// off-loop instead of returning it as a cmd).
func postedInstanceSelect(fp *fakePoster, name string) bool {
	for _, ev := range fp.events() {
		if ism, ok := ev.(InstanceSelectMsg); ok && ism.Name == name {
			return true
		}
	}
	return false
}
