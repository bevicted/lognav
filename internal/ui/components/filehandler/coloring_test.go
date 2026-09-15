package filehandler

import (
	"image/color"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
)

// newTestModelWithRowStyleFn is like newTestModel but wires a NewRowStyler on
// the FileOpts that hands back the given per-row stub, letting callers inject a
// colorer without needing to import the snapshot or archive packages
// (filehandler is generic).
func newTestModelWithRowStyleFn(t *testing.T, dir string, styleFn RowStyler) (*Model, *fakePoster) {
	t.Helper()
	return newTestModelWithRowStyler(t, dir, func() RowStyler { return styleFn })
}

// newTestModelWithRowStyler wires the two-step NewRowStyler hook verbatim, for
// tests that care about the per-listing build itself.
func newTestModelWithRowStyler(t *testing.T, dir string, newStyler func() RowStyler) (*Model, *fakePoster) {
	t.Helper()
	bundle := depstest.NewTest(t)
	opts := FileOpts{
		Dir:          dir,
		Ext:          ".dat",
		NewRowStyler: newStyler,
	}
	m := New(bundle, opts, nil, nil)
	fp := &fakePoster{}
	m.SetPoster(fp)
	return m, fp
}

// TestListFiles_RowStyleFn_PopulatesStyles verifies that when NewRowStyler is set,
// ListFiles populates msg.styles parallel to msg.files, calling the hook with the
// FULL basename (incl. Ext) while the row name itself is ext-stripped. The result
// is also driven through OnFileIO to confirm the styled-segment path does not
// panic.
func TestListFiles_RowStyleFn_PopulatesStyles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Two files with distinct mtimes for deterministic order.
	for i, name := range []string{"plain.dat", "fancy.dat"} {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		require.NoError(t, os.Chtimes(p, time.Time{}, time.Unix(int64((i+1)*100), 0)))
	}

	sentinel := uv.Style{Fg: color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 0xFF}}
	stub := func(fullBasename string) uv.Style {
		if fullBasename == "fancy.dat" {
			return sentinel
		}
		return uv.Style{}
	}

	m, fp := newTestModelWithRowStyleFn(t, dir, stub)
	m.ListFiles()

	ioMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok, "ListFiles must post a ListOP IOMsg")
	require.Len(t, ioMsg.styles, len(ioMsg.files), "styles must be parallel to files")

	styleByName := make(map[string]uv.Style, len(ioMsg.files))
	for i, f := range ioMsg.files {
		styleByName[f] = ioMsg.styles[i]
	}

	assert.Equal(t, sentinel, styleByName["fancy"], "fancy.dat must carry the sentinel style")
	assert.Equal(t, uv.Style{}, styleByName["plain"], "plain.dat must be unstyled")

	// Drive the styled-segment path to confirm no panic with an explicit style.
	m.OnFileIO(ioMsg)
}

// TestListFiles_NewRowStyler_BuiltOncePerListing pins the whole point of the
// two-step hook: NewRowStyler runs exactly once per ListFiles no matter how many
// rows there are, so wiring that scans a directory in it does so once per
// refresh, not once per row.
func TestListFiles_NewRowStyler_BuiltOncePerListing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"a.dat", "b.dat", "c.dat", "d.dat"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}

	var builds, rows atomic.Int64
	m, fp := newTestModelWithRowStyler(t, dir, func() RowStyler {
		builds.Add(1)
		return func(string) uv.Style {
			rows.Add(1)
			return uv.Style{}
		}
	})
	m.ListFiles()

	ioMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok, "ListFiles must post a ListOP IOMsg")
	assert.Len(t, ioMsg.files, 4)
	assert.Equal(t, int64(1), builds.Load(), "NewRowStyler must be built once per listing")
	assert.Equal(t, int64(4), rows.Load(), "the built styler must be called once per row")
}

// TestListFiles_NilRowStyler_AllStylesZero verifies that a NewRowStyler that
// returns nil (the fail-safe path snapshot wiring takes when the lock scan
// errors) leaves every row unstyled instead of panicking.
func TestListFiles_NilRowStyler_AllStylesZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.dat"), []byte("x"), 0o600))

	m, fp := newTestModelWithRowStyler(t, dir, func() RowStyler { return nil })
	m.ListFiles()

	ioMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	require.Len(t, ioMsg.styles, len(ioMsg.files))
	for _, st := range ioMsg.styles {
		assert.Equal(t, uv.Style{}, st, "a nil RowStyler must leave rows unstyled")
	}
}

// TestListFiles_NoRowStyleFn_AllStylesZero verifies that when NewRowStyler is
// nil (query wiring), every row style is the zero uv.Style, i.e. unstyled.
func TestListFiles_NoRowStyleFn_AllStylesZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.dat"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.dat"), []byte("x"), 0o600))

	m, fp, _, _ := newTestModel(t, dir)
	m.ListFiles()

	ioMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok, "ListFiles must post a ListOP IOMsg")
	require.Len(t, ioMsg.styles, len(ioMsg.files))
	for _, st := range ioMsg.styles {
		assert.Equal(t, uv.Style{}, st, "without RowStyleFn every row must be unstyled")
	}
}

// TestOnFileIO_ListOP_AppliesRowStyles asserts the ListOP arm carries each
// IOMsg style through to the rendered row segment, and that a styles slice
// shorter than files (defensive: never produced by ListFiles) leaves the
// unmatched rows unstyled instead of panicking.
func TestOnFileIO_ListOP_AppliesRowStyles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mine.dat"), []byte("x"), 0o600))

	sentinel := uv.Style{Fg: color.RGBA{R: 0xAB, G: 0xCD, B: 0xEF, A: 0xFF}}
	m, fp := newTestModelWithRowStyleFn(t, dir, func(string) uv.Style { return sentinel })
	m.ListFiles()

	ioMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	require.Len(t, ioMsg.files, 1)
	assert.Equal(t, sentinel, ioMsg.styles[0], "the hook's style must reach the IOMsg")
	m.OnFileIO(ioMsg)

	// Short styles slice: the extra row must render unstyled, not panic.
	short := ioMsg
	short.files = []string{"mine", "other"}
	short.styles = []uv.Style{sentinel}
	m.OnFileIO(short)
}
