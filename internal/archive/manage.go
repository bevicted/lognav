package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is a scanned archive summary (newest first by SubmittedAt).
type Entry struct {
	Name    string
	Path    string
	Archive *Archive
}

// Scan lists all archive files, newest first.
func Scan() ([]Entry, error) {
	d, err := Dir()
	if err != nil {
		return nil, err
	}
	dents, err := os.ReadDir(d)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range dents {
		if de.IsDir() || !strings.HasSuffix(de.Name(), FileExt) {
			continue
		}
		p := filepath.Join(d, de.Name())
		a, err := Load(p)
		if err != nil {
			continue // skip unreadable
		}
		out = append(out, Entry{Name: strings.TrimSuffix(de.Name(), FileExt), Path: p, Archive: a})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Archive.SubmittedAt.After(out[j].Archive.SubmittedAt) })
	return out, nil
}

// getpid is a package var so tests can simulate a fixed PID. Mirrors the
// snapshot package's getpid seam.
var getpid = os.Getpid

// DefaultName builds the base selector for a new archive. Create atomically
// reserves this timestamp-and-PID name and appends a numeric suffix when it is
// already taken, so same-second dispatches cannot overwrite each other. now is
// injected for testability; the PID comes from the overridable getpid seam.
func DefaultName(now time.Time) string {
	return fmt.Sprintf("%s_%d", now.Format("2006-01-02-15-04-05"), getpid())
}

// CleanStaleExpired removes archive files past TTL (computed from the saved submittedAt; no
// network). Best-effort; mirrors snapshot.CleanStaleWip. Run at startup.
func CleanStaleExpired(now time.Time) error {
	entries, err := Scan()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Archive.IsExpired(now) {
			_ = os.Remove(e.Path)
		}
	}
	return nil
}
