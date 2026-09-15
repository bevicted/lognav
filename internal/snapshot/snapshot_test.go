package snapshot

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutoSnapshotNameContract(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	assert.Equal(t, "auto-20260814-120000.lognav", autoSnapshotName(now, 1))
	assert.Equal(t, "auto-20260814-120000-2.lognav", autoSnapshotName(now, 2))
	assert.Equal(t, "auto-20260814-120000-3.lognav", autoSnapshotName(now, 3))

	for _, tc := range []struct {
		name  string
		input string
		want  bool
	}{
		{name: "base", input: "auto-20260814-120000.lognav", want: true},
		{name: "collision", input: "auto-20260814-120000-2.lognav", want: true},
		{name: "later collision", input: "auto-20260814-120000-123.lognav", want: true},
		{name: "impossible date", input: "auto-20260230-120000.lognav", want: false},
		{name: "impossible time", input: "auto-20260814-246000.lognav", want: false},
		{name: "malformed separator", input: "auto-20260814_120000.lognav", want: false},
		{name: "signed suffix", input: "auto-20260814-120000-+2.lognav", want: false},
		{name: "padded suffix", input: "auto-20260814-120000-02.lognav", want: false},
		{name: "zero suffix", input: "auto-20260814-120000-0.lognav", want: false},
		{name: "one suffix", input: "auto-20260814-120000-1.lognav", want: false},
		{name: "overflow suffix", input: "auto-20260814-120000-18446744073709551616.lognav", want: false},
		{name: "trailing text", input: "auto-20260814-120000.lognav.bak", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isAutoSnapshotName(tc.input))
		})
	}
}

func TestCreateWip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := CreateWip(dir, os.Getpid())
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	second, err := CreateWip(dir, os.Getpid())
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	assert.NotEqual(t, first.Name(), second.Name())
	for _, wip := range []*os.File{first, second} {
		info, err := wip.Stat()
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		name := filepath.Base(wip.Name())
		assert.Equal(t, byte('.'), name[0])
		assert.Equal(t, ".wip", filepath.Ext(name))
		assert.NotEqual(t, WipSuffix, filepath.Base(name))
		assert.True(t, isWipWithPID(name, os.Getpid()))
	}
	_, err = CreateWip(dir, 0)
	require.Error(t, err)
	_, err = CreateWip(dir, -1)
	require.Error(t, err)
}

func isWipWithPID(name string, want int) bool {
	pid, ok := wipPID(name)
	return ok && pid == want
}

//nolint:paralleltest // t.Setenv and pidAlive are process-global test seams
func TestCleanStaleWip_PreservesOnlyLiveCanonicalPIDWips(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	origAlive := pidAlive
	pidAlive = func(pid int) bool { return pid == os.Getpid() }
	t.Cleanup(func() { pidAlive = origAlive })

	dir, err := Dir()
	require.NoError(t, err)
	liveWip := ".random_" + strconv.Itoa(os.Getpid()) + WipSuffix
	deadWip := ".random_" + strconv.Itoa(math.MaxInt32) + WipSuffix
	nonHiddenLiveWip := "legacy_" + strconv.Itoa(os.Getpid()) + WipSuffix
	malformed := []string{
		".random_0" + WipSuffix,
		".random_+1" + WipSuffix,
		".random_01" + WipSuffix,
		".random_-1" + WipSuffix,
		".random_999999999999999999999999" + WipSuffix,
		".random_nope" + WipSuffix,
	}
	finalized := "auto-20260814-120000.lognav"
	for _, name := range append([]string{liveWip, deadWip, nonHiddenLiveWip, finalized}, malformed...) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}

	require.NoError(t, CleanStaleWip())
	assert.FileExists(t, filepath.Join(dir, liveWip))
	assert.NoFileExists(t, filepath.Join(dir, deadWip))
	assert.NoFileExists(t, filepath.Join(dir, nonHiddenLiveWip), "non-hidden legacy WIPs are stale even when their PID is live")
	assert.FileExists(t, filepath.Join(dir, finalized))
	for _, name := range malformed {
		assert.NoFileExists(t, filepath.Join(dir, name))
	}
}
