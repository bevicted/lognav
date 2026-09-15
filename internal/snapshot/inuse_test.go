package snapshot

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeInUse writes a <pid>.inuse file listing the given basenames.
func writeInUse(t *testing.T, pid int, bases ...string) {
	t.Helper()
	dir, err := Dir()
	require.NoError(t, err)
	var sb strings.Builder
	for _, b := range bases {
		sb.WriteString(b)
		sb.WriteByte('\n')
	}
	body := sb.String()
	require.NoError(t, os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+inUseExt), []byte(body), 0o600))
}

//nolint:paralleltest // t.Setenv + pidAlive/getpid overrides; cannot parallelize
func TestInUse_SetReadRemove(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	origPid, origAlive := getpid, pidAlive
	getpid = func() int { return 4821 }
	pidAlive = func(pid int) bool { return pid == 4821 || pid == 9000 }
	t.Cleanup(func() { getpid, pidAlive = origPid, origAlive })

	require.NoError(t, SetOwnInUse("/snap/auto-20260814-120000.lognav"))
	inUse, err := InUse("auto-20260814-120000.lognav")
	require.NoError(t, err)
	assert.True(t, inUse)

	other, err := InUseByOther("auto-20260814-120000.lognav")
	require.NoError(t, err)
	assert.False(t, other)

	writeInUse(t, 9000, "incident.lognav")
	other, err = InUseByOther("incident.lognav")
	require.NoError(t, err)
	assert.True(t, other)

	require.NoError(t, SetOwnInUse(""))
	inUse, err = InUse("auto-20260814-120000.lognav")
	require.NoError(t, err)
	assert.False(t, inUse)
}

//nolint:paralleltest // t.Setenv + pidAlive/getpid overrides; cannot parallelize
func TestInUse_DeadPidIgnoredAndGCd(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	origAlive := pidAlive
	pidAlive = func(pid int) bool { return pid == 4821 }
	t.Cleanup(func() { pidAlive = origAlive })

	writeInUse(t, 111, "dead.lognav")
	writeInUse(t, 4821, "live.lognav")

	inUse, err := InUse("dead.lognav")
	require.NoError(t, err)
	assert.False(t, inUse)
	inUse, err = InUse("live.lognav")
	require.NoError(t, err)
	assert.True(t, inUse)

	require.NoError(t, CleanStaleInUse())
	dir, err := Dir()
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "111"+inUseExt))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(dir, "4821"+inUseExt))
	require.NoError(t, err)
}

//nolint:paralleltest // t.Setenv + getpid override; cannot parallelize
func TestInitOwn_ResetsReusedPidFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	origPid := getpid
	getpid = func() int { return 4821 }
	t.Cleanup(func() { getpid = origPid })

	writeInUse(t, 4821, "stale.lognav")
	require.NoError(t, InitOwn())

	dir, err := Dir()
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "4821"+inUseExt))
	require.ErrorIs(t, err, os.ErrNotExist)
}

//nolint:paralleltest // t.Setenv + pidAlive/getpid overrides; cannot parallelize
func TestInUse_Holders(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	origPid, origAlive := getpid, pidAlive
	getpid = func() int { return 1 }
	pidAlive = func(pid int) bool { return pid == 4821 || pid == 9000 }
	t.Cleanup(func() { getpid, pidAlive = origPid, origAlive })

	writeInUse(t, 4821, "shared.lognav")
	writeInUse(t, 9000, "shared.lognav")
	h, err := Holders("/x/shared.lognav")
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{4821, 9000}, h)
}

// TestInUse_Held pins the batched form used by retention: one scan answers
// the whole set, dead holders do not count, and unheld names stay absent.
//
//nolint:paralleltest // t.Setenv + pidAlive/getpid overrides; cannot parallelize
func TestInUse_Held(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	origPid, origAlive := getpid, pidAlive
	getpid = func() int { return 1 }
	pidAlive = func(pid int) bool { return pid == 4821 }
	t.Cleanup(func() { getpid, pidAlive = origPid, origAlive })

	writeInUse(t, 4821, "held_a.lognav", "held_b.lognav")
	writeInUse(t, 111, "dead_holder.lognav") // dead PID: does not protect

	held, err := Held([]string{"held_a.lognav", "free.lognav", "dead_holder.lognav", "/x/held_b.lognav"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"held_a.lognav": true, "held_b.lognav": true}, held)

	empty, err := Held(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

//nolint:paralleltest // t.Setenv + pidAlive/getpid overrides; cannot parallelize
func TestInUse_IgnoresTmpFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	origPid, origAlive := getpid, pidAlive
	getpid = func() int { return 1 }
	pidAlive = func(int) bool { return true } // all "live"
	t.Cleanup(func() { getpid, pidAlive = origPid, origAlive })

	dir, err := Dir()
	require.NoError(t, err)
	// A leftover temp file (crashed mid-write) must NOT count as a claim.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "777"+inUseTmpExt), []byte("a_partial.lognav\n"), 0o600))

	inUse, err := InUse("a_partial.lognav")
	require.NoError(t, err)
	assert.False(t, inUse, ".inuse.tmp must be ignored by readers")
}
