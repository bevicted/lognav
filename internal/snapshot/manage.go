// Package-level additions for CLI snapshot management.
package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// RetainAutoSnapshots keeps the newest limit finalized auto snapshots and
// removes older unheld ones. It targets only names that exactly match the
// canonical automatic filename contract; manual and .wip files are never
// considered. In-use snapshots remain even when that leaves more than limit
// files. Removed entries are returned; deletion failures are joined and do not
// stop later removals.
func RetainAutoSnapshots(limit uint8) ([]Entry, error) {
	entries, err := Scan()
	if err != nil {
		return nil, err
	}
	inUse, err := HoldersIndex()
	if err != nil {
		return nil, err
	}

	var (
		removed []Entry
		errs    []error
		kept    uint8
	)
	for _, entry := range entries {
		if !isAutoSnapshotName(filepath.Base(entry.Path)) {
			continue
		}
		if kept < limit {
			kept++
			continue
		}
		if inUse.InUse(entry.Path) {
			continue
		}
		if err := removeFile(entry.Path); err != nil {
			errs = append(errs, fmt.Errorf("remove auto snapshot %q: %w", entry.Path, err))
			continue
		}
		removed = append(removed, entry)
	}
	return removed, errors.Join(errs...)
}

var removeFile = os.Remove

// Kind classifies a snapshot by its automatic-finalization filename.
type Kind uint8

const (
	KindManual Kind = iota
	KindAuto
)

func (k Kind) String() string {
	if k == KindAuto {
		return "auto"
	}
	return "manual"
}

// Entry is one snapshot file's listing metadata (no container open).
type Entry struct {
	Name    string // basename minus the trailing ".lognav"
	Path    string // absolute path
	Kind    Kind   // exact auto-finalization name -> KindAuto, otherwise KindManual
	ModTime time.Time
	Size    int64
}

// Scan lists finalized snapshot files in Dir(): every *.lognav except
// *.lognav.wip, classified and sorted newest-first by ModTime.
func Scan() ([]Entry, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	dents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, de := range dents {
		name := de.Name()
		if de.IsDir() || strings.HasSuffix(name, WipSuffix) || !strings.HasSuffix(name, FileExt) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue // entry vanished between ReadDir and Info; skip
		}
		kind := KindManual
		if isAutoSnapshotName(name) {
			kind = KindAuto
		}
		entries = append(entries, Entry{
			Name:    strings.TrimSuffix(name, FileExt),
			Path:    filepath.Join(dir, name),
			Kind:    kind,
			ModTime: info.ModTime(),
			Size:    info.Size(),
		})
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		return b.ModTime.Compare(a.ModTime) // newest first
	})
	return entries, nil
}

// Summary is the state-frame content needed to describe a snapshot.
type Summary struct {
	ID                 string
	Query              string
	Search             string
	JQ                 string
	Instances          []InstanceSnapshot
	TotalLogs          int    // sum of per-instance LogCount
	TotalLogsSizeBytes uint64 // checked sum of per-instance LogsSizeBytes
}

// Summarize opens the snapshot read-only and reads just its state frame.
func Summarize(path string) (Summary, error) {
	c, err := OpenContainerReadOnly(path)
	if err != nil {
		return Summary{}, err
	}
	defer func() { _ = c.Close() }()

	snap, err := LoadState(c)
	if err != nil {
		return Summary{}, err
	}
	var totalLogsSizeBytes uint64
	totalLogs := 0
	for _, inst := range snap.InstancePickerSnapshot.Instances {
		totalLogs += inst.LogCount
		if totalLogsSizeBytes > ^uint64(0)-inst.LogsSizeBytes {
			return Summary{}, errors.New("snapshot total logs_size_bytes overflows uint64")
		}
		totalLogsSizeBytes += inst.LogsSizeBytes
	}
	return Summary{
		ID:                 snap.ID,
		Query:              snap.Query,
		Search:             snap.Search,
		JQ:                 snap.JQ,
		Instances:          snap.InstancePickerSnapshot.Instances,
		TotalLogs:          totalLogs,
		TotalLogsSizeBytes: totalLogsSizeBytes,
	}, nil
}

// ErrSnapshotNotFound is returned by Resolve when no snapshot matches.
var ErrSnapshotNotFound = errors.New("snapshot not found")

// Resolve maps a CLI name argument to an Entry. The reserved word "latest"
// returns the newest entry; otherwise it strips one trailing ".lognav" and
// exact-matches an Entry.Name from Scan(). A real file literally named "latest"
// is therefore unreachable by name (still visible via Scan/list).
func Resolve(name string) (Entry, error) {
	entries, err := Scan()
	if err != nil {
		return Entry{}, err
	}
	if name == "latest" {
		if len(entries) == 0 {
			return Entry{}, fmt.Errorf("%w: latest (no snapshots)", ErrSnapshotNotFound)
		}
		return entries[0], nil
	}
	target := strings.TrimSuffix(name, FileExt)
	for _, e := range entries {
		if e.Name == target {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("%w: %s", ErrSnapshotNotFound, name)
}

// FindByID scans managed snapshots and returns the Entry whose state-frame ID
// matches id. Used for notify-loss recovery: a session whose backing was
// renamed by another process re-resolves the file by its stable UUID. An empty
// id never matches. Returns ErrSnapshotNotFound when no file matches.
func FindByID(id string) (Entry, error) {
	if id == "" {
		return Entry{}, fmt.Errorf("%w: empty id", ErrSnapshotNotFound)
	}
	entries, err := Scan()
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		sum, err := Summarize(e.Path)
		if err != nil {
			continue // unreadable/partial; skip
		}
		if sum.ID == id {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("%w: id %s", ErrSnapshotNotFound, id)
}

// MinIDPrefixLen is the shortest ID prefix the CLI will match (git-style floor).
// It sits below the first canonical UUID hyphen (index 8), so any prefix at the
// floor is pure lowercase hex.
const MinIDPrefixLen = 4

// IDMatch pairs a snapshot Entry with the full state-frame ID read during the
// scan, so an ambiguity renderer can print shortID(ID)+Name without re-reading
// the file (Entry itself carries no ID).
type IDMatch struct {
	Entry Entry
	ID    string
}

// AmbiguousIDError reports an ID prefix that matched more than one snapshot.
// Matches is newest-first (Scan order) and carries every match (uncapped —
// display truncation is the caller's concern). Callers use errors.As to format.
type AmbiguousIDError struct {
	Prefix  string
	Matches []IDMatch
}

func (e *AmbiguousIDError) Error() string {
	return fmt.Sprintf("ID prefix %q is ambiguous (%d matches)", e.Prefix, len(e.Matches))
}

// ResolveByID matches prefix against every managed snapshot's state-frame ID.
// The prefix is folded to lower and compared (HasPrefix) against the canonical
// hyphenated lowercase UUID string. Returns the single matching Entry, or
// ErrSnapshotNotFound when none match (including a prefix shorter than
// MinIDPrefixLen and empty-ID legacy snapshots), or *AmbiguousIDError when two
// or more match. It scans every entry (must confirm uniqueness); the ID read
// during the scan is retained in each IDMatch, so no file is Summarized twice.
func ResolveByID(prefix string) (Entry, error) {
	if len(prefix) < MinIDPrefixLen {
		return Entry{}, fmt.Errorf("%w: %s", ErrSnapshotNotFound, prefix)
	}
	lp := strings.ToLower(prefix)
	entries, err := Scan()
	if err != nil {
		return Entry{}, err
	}
	var matches []IDMatch
	for _, e := range entries {
		sum, err := Summarize(e.Path)
		if err != nil || sum.ID == "" {
			continue // unreadable/partial or legacy empty-ID: never a match
		}
		if strings.HasPrefix(strings.ToLower(sum.ID), lp) {
			matches = append(matches, IDMatch{Entry: e, ID: sum.ID})
		}
	}
	switch len(matches) {
	case 0:
		return Entry{}, fmt.Errorf("%w: id prefix %s", ErrSnapshotNotFound, prefix)
	case 1:
		return matches[0].Entry, nil
	default:
		return Entry{}, &AmbiguousIDError{Prefix: prefix, Matches: matches}
	}
}

// ResolveNameOrID resolves arg first by exact name (and the "latest" sentinel)
// via Resolve, then — only on a name miss with len(arg) >= MinIDPrefixLen — by ID
// prefix via ResolveByID. Name always wins. An ambiguous ID prefix surfaces the
// *AmbiguousIDError unchanged. A both-missed result wraps ErrSnapshotNotFound (so
// errors.Is keeps working for callers) and, when an ID prefix was actually tried,
// carries the "tried name, then ID prefix" message.
func ResolveNameOrID(arg string) (Entry, error) {
	e, err := Resolve(arg)
	if err == nil {
		return e, nil
	}
	if !errors.Is(err, ErrSnapshotNotFound) {
		return Entry{}, err // e.g. Dir() failure — surface as-is
	}
	if len(arg) < MinIDPrefixLen {
		return Entry{}, err // too short to be an ID prefix: plain not-found
	}
	got, idErr := ResolveByID(arg)
	if idErr == nil {
		return got, nil
	}
	var amb *AmbiguousIDError
	if errors.As(idErr, &amb) {
		return Entry{}, idErr // ambiguity is its own classified error
	}
	if !errors.Is(idErr, ErrSnapshotNotFound) {
		return Entry{}, idErr // non-not-found (e.g. a Scan IO error): surface as-is
	}
	return Entry{}, fmt.Errorf("no snapshot matches %q (tried name, then ID prefix): %w", arg, ErrSnapshotNotFound)
}

// StaleFiles returns paths of orphaned in-progress backing files: *.lognav.wip
// whose embedded PID is dead (or whose name predates PID stamping / is
// unparsable). A wip owned by a live session is never returned. These are
// crashed-mid-fetch leftovers — NOT snapshots. Used by `snapshot prune` as the
// always-on baseline. Finalized automatic snapshots are intentionally excluded:
// they are real (openable) auto snapshots and are pruned only via the --auto
// selector.
func StaleFiles() ([]string, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	dents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, de := range dents {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, WipSuffix) {
			continue
		}
		if pid, ok := wipPID(name); ok && pidAlive(pid) {
			continue // owned by a live session
		}
		// The error is intentionally swallowed (falls open → treated as stale):
		// the filename-PID guard just above is the primary protection for live
		// wips; this .inuse check is only a belt-and-suspenders for wip names
		// with a dead/unparsable PID.
		if inUse, _ := InUse(filepath.Join(dir, name)); inUse {
			continue // a live session lists it in its .inuse guard
		}
		stale = append(stale, filepath.Join(dir, name))
	}
	return stale, nil
}
