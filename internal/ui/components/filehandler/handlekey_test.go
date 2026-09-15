package filehandler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleKey_Loading_ReturnsKeyIgnored verifies that all key presses are
// silently dropped while the model is in the loading state.
func TestHandleKey_Loading_ReturnsKeyIgnored(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)
	m.loading = true

	ev := keystest.PressKeyUV(t, uv.KeyEnter)
	r := m.HandleKey(ev)
	assert.Equal(t, component.KeyIgnored, r, "loading state must return KeyIgnored")
}

// TestHandleKey_BoundKey_ReturnsKeyHandled verifies that a key bound to m.kh
// (the Accept/Enter key = ReadFileUnderCursor) is consumed as KeyHandled when
// the list is not focused.
func TestHandleKey_BoundKey_ReturnsKeyHandled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.dat"), []byte("x"), 0o600))

	m, fp, _, _ := newTestModel(t, dir)
	// Populate the list so ReadFileUnderCursor has an item.
	m.ListFiles()
	msg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(msg)
	assert.False(t, m.list.IsFocused(), "list must not be focused")

	// Enter is the Accept key, bound to ReadFileUnderCursor.
	ev := keystest.PressKeyUV(t, uv.KeyEnter)
	r := m.HandleKey(ev)
	assert.Equal(t, component.KeyHandled, r, "Accept key must return KeyHandled via m.kh")
}

// TestHandleKey_DeleteAcceptsForwardDelete verifies that the default legacy
// "del" binding matches ultraviolet's KeyDelete event (Fn+Backspace on macOS).
func TestHandleKey_DeleteAcceptsForwardDelete(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.dat")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	m, fp, _, _ := newTestModel(t, dir)
	m.ListFiles()
	msg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(msg)

	r := m.HandleKey(keystest.PressKeyUV(t, uv.KeyDelete))
	assert.Equal(t, component.KeyHandled, r)
	assert.NoFileExists(t, path)
}

// TestHandleKey_ListFocused_RoutesAndConsumsKey verifies that when the list is
// focused, all key presses are routed to m.list.HandleKey and KeyHandled is
// returned unconditionally (leaf-consumes-all rule).
func TestHandleKey_ListFocused_RoutesAndConsumsKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)
	m.list.Focus()
	require.True(t, m.list.IsFocused(), "list must be focused")

	// Use an arbitrary key — focused list must consume everything.
	ev := keystest.PressRuneUV(t, 'j')
	r := m.HandleKey(ev)
	assert.Equal(t, component.KeyHandled, r, "focused list must return KeyHandled unconditionally")
}

// TestHandleKey_UnboundKey_ForwardedToList verifies that an unbound key (not
// in m.kh) is forwarded to the list when the list is not focused, mirroring
// the Update arm's fallback to m.list.Update.
func TestHandleKey_UnboundKey_ForwardedToList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)
	assert.False(t, m.list.IsFocused())

	// F9 is not bound in any handler; the list also won't handle it, so
	// we expect KeyIgnored propagated from m.list.HandleKey.
	ev := keystest.PressKeyUV(t, uv.KeyF9)
	r := m.HandleKey(ev)
	// The list returns KeyIgnored for unbound keys, which HandleKey propagates.
	assert.Equal(t, component.KeyIgnored, r, "unbound key not handled by list must propagate KeyIgnored")
}
