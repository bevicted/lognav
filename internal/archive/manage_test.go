package archive

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findEntry returns the scanned entry with the given name, failing the test if
// it is absent. A small test helper replacing the dropped archive.Resolve.
func findEntry(t *testing.T, name string) Entry {
	t.Helper()
	entries, err := Scan()
	require.NoError(t, err)
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no archive entry named %q", name)
	return Entry{}
}

func TestDefaultNameUniquifiesWithPID(t *testing.T) {
	// Not t.Parallel(): overrides the package-level getpid seam.
	prev := getpid
	getpid = func() int { return 4821 }
	t.Cleanup(func() { getpid = prev })

	now := time.Date(2026, 6, 20, 15, 4, 5, 0, time.UTC)
	name := DefaultName(now)
	// Timestamp prefix preserved; PID appended so two dispatches in the same
	// wall-clock second from different processes do not collide.
	assert.True(t, strings.HasPrefix(name, "2026-06-20-15-04-05_"), "got %q", name)
	assert.Equal(t, fmt.Sprintf("2026-06-20-15-04-05_%d", 4821), name)
}

// TestCreate_SameSecondDefaultNamesRetainBoth proves dispatches with the same
// timestamp and PID receive distinct selectors instead of atomically replacing
// the first archive. Scan then exposes both selectors normally.
func TestCreate_SameSecondDefaultNamesRetainBoth(t *testing.T) {
	// Not t.Parallel(): overrides getpid and dirFn package seams.
	withTempDir(t)
	prev := getpid
	getpid = func() int { return 4821 }
	t.Cleanup(func() { getpid = prev })

	now := time.Date(2026, 6, 20, 15, 4, 5, 0, time.UTC)
	first := &Archive{Name: DefaultName(now), Query: "first", SubmittedAt: now}
	second := &Archive{Name: DefaultName(now), Query: "second", SubmittedAt: now}
	require.Equal(t, first.Name, second.Name, "same-second defaults start from the same base selector")

	require.NoError(t, Create(first))
	require.NoError(t, Create(second))
	assert.Equal(t, "2026-06-20-15-04-05_4821", first.Name)
	assert.Equal(t, "2026-06-20-15-04-05_4821_2", second.Name)
	assert.Equal(t, "first", findEntry(t, first.Name).Archive.Query)
	assert.Equal(t, "second", findEntry(t, second.Name).Archive.Query)

	entries, err := Scan()
	require.NoError(t, err)
	assert.Len(t, entries, 2, "both archive selectors remain listable")
}

func TestScanNewestFirst(t *testing.T) {
	// Not t.Parallel(): withTempDir mutates the package-level dirFn var; parallel
	// tests that also call withTempDir would race on that var.
	withTempDir(t)
	old := &Archive{Name: "old", SubmittedAt: time.Unix(1000, 0), Instances: []InstanceEntry{{QueryID: "aaaa1111"}}}
	recent := &Archive{Name: "recent", SubmittedAt: time.Unix(2000, 0), Instances: []InstanceEntry{{QueryID: "bbbb2222"}}}
	require.NoError(t, Save(old))
	require.NoError(t, Save(recent))

	entries, err := Scan()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "recent", entries[0].Name) // newest first
	assert.Equal(t, "old", entries[1].Name)
}

func TestCleanStaleExpired(t *testing.T) {
	// Not t.Parallel(): withTempDir mutates the package-level dirFn var; parallel
	// tests that also call withTempDir would race on that var.
	withTempDir(t)
	base := time.Unix(0, 0).UTC()
	require.NoError(t, Save(&Archive{Name: "fresh", SubmittedAt: base}))
	require.NoError(t, Save(&Archive{Name: "stale", SubmittedAt: base}))
	// "stale" stays old because now is far past TTL for both; make fresh recent:
	fresh := findEntry(t, "fresh")
	fresh.Archive.SubmittedAt = base.Add(TTL) // not yet expired at now below
	require.NoError(t, Save(fresh.Archive))

	require.NoError(t, CleanStaleExpired(base.Add(TTL+time.Hour)))
	entries, err := Scan()
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	assert.Contains(t, names, "fresh")
	assert.NotContains(t, names, "stale")
}
