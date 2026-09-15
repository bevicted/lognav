package snapshot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	inUseExt    = ".inuse"
	inUseTmpExt = ".inuse.tmp"
)

// getpid is a package var so tests can simulate a fixed PID. Mirrors the
// pidAlive seam in snapshot.go.
var getpid = os.Getpid

// ownInUsePath returns this process's <pid>.inuse path.
func ownInUsePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, strconv.Itoa(getpid())+inUseExt), nil
}

// SetOwnInUse rewrites this process's <pid>.inuse to list base(path) as the
// single in-use snapshot, or removes the file when path is empty. The write is
// atomic (temp file + rename). This process is the only writer of its own file,
// so the rewrite cannot race another writer. Called synchronously on the loop
// goroutine from setBackingPath — it is tiny, rare local IO, never a poster post.
// A failed rename leaves the .tmp behind; CleanStaleInUse sweeps it (dead-PID)
// at next startup.
func SetOwnInUse(path string) error {
	own, err := ownInUsePath()
	if err != nil {
		return err
	}
	if path == "" {
		if err := os.Remove(own); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	tmp := own + ".tmp" // <pid>.inuse.tmp
	if err := os.WriteFile(tmp, []byte(filepath.Base(path)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, own)
}

// InitOwn resets this process's .inuse file at startup, clearing any stale file
// left by a previous process that reused this PID (PID-reuse self-correction).
func InitOwn() error { return SetOwnInUse("") }

// inUsePID extracts the PID from a <pid>.inuse or <pid>.inuse.tmp filename.
// Returns (0,false) for any other name or an unparsable PID.
func inUsePID(name string) (int, bool) {
	base, ok := strings.CutSuffix(name, inUseTmpExt)
	if !ok {
		base, ok = strings.CutSuffix(name, inUseExt)
	}
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(base)
	if err != nil {
		return 0, false
	}
	return pid, true
}

// CleanStaleInUse unlinks every .inuse / .inuse.tmp file whose owning PID is
// dead. A live owner's file is left untouched. Best-effort GC; mirrors
// CleanStaleWip. Run at startup next to CleanStaleWip.
func CleanStaleInUse() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	dents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, de := range dents {
		pid, ok := inUsePID(de.Name())
		if !ok || pidAlive(pid) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, de.Name()))
	}
	return nil
}

// InUseIndex maps a snapshot's base name to the live PIDs holding it, in lock-
// directory scan order. A name no live session holds is absent, so a plain
// lookup reads as "free". The PID slices belong to the index — read them, do
// not append to them.
type InUseIndex map[string][]int

// HoldersIndex walks the lock directory ONCE and returns the whole basename →
// live-holder-PIDs map. Dead-PID files, .inuse.tmp temp files, unreadable files
// and blank lines are ignored.
//
// Every caller that asks about more than one file — the snapshot tab's listing,
// the CLI list/rm/prune paths — must build this once and then query it, because
// the single-path Holders/InUse/InUseByOther wrappers each re-read the whole
// directory AND re-run Dir()'s MkdirAll. Per row that is O(rows) scans and
// O(rows) mkdir syscalls on every refresh.
func HoldersIndex() (InUseIndex, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	dents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	ix := InUseIndex{}
	for _, de := range dents {
		name := de.Name()
		// .inuse.tmp does not end in .inuse, so the second check is belt-and-suspenders.
		if !strings.HasSuffix(name, inUseExt) || strings.HasSuffix(name, inUseTmpExt) {
			continue
		}
		pid, ok := inUsePID(name)
		if !ok || !pidAlive(pid) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- snapshot dir confined; pid-named file
		if err != nil {
			continue
		}
		for ln := range strings.SplitSeq(string(data), "\n") {
			ln = strings.TrimSpace(ln)
			// One file listing the same name twice must not list its PID twice.
			if ln == "" || slices.Contains(ix[ln], pid) {
				continue
			}
			ix[ln] = append(ix[ln], pid)
		}
	}
	return ix, nil
}

// Holders returns the live PIDs holding base(path), nil when it is free.
func (ix InUseIndex) Holders(path string) []int { return ix[filepath.Base(path)] }

// InUse reports whether any live session holds base(path).
func (ix InUseIndex) InUse(path string) bool { return len(ix.Holders(path)) > 0 }

// InUseByOther reports whether any live session OTHER than this process holds
// base(path).
func (ix InUseIndex) InUseByOther(path string) bool {
	self := getpid()
	for _, pid := range ix.Holders(path) {
		if pid != self {
			return true
		}
	}
	return false
}

// Held answers InUse for a whole set of names off one index build. Names are
// compared by base name; the returned set contains exactly those held by at
// least one live session (unheld names are absent, so a plain map lookup on the
// result reads as "is protected").
func Held(names []string) (map[string]bool, error) {
	held := make(map[string]bool, len(names))
	if len(names) == 0 {
		return held, nil // nothing to ask about: do not touch the directory
	}
	ix, err := HoldersIndex()
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if base := filepath.Base(n); ix.InUse(base) {
			held[base] = true
		}
	}
	return held, nil
}

// Holders is the single-path wrapper over HoldersIndex. Use the index directly
// when asking about more than one file.
func Holders(path string) ([]int, error) {
	ix, err := HoldersIndex()
	if err != nil {
		return nil, err
	}
	return ix.Holders(path), nil
}

// InUse reports whether any live session lists base(path).
func InUse(path string) (bool, error) {
	h, err := Holders(path)
	return len(h) > 0, err
}

// InUseByOther reports whether any live session OTHER than this process lists
// base(path).
func InUseByOther(path string) (bool, error) {
	ix, err := HoldersIndex()
	if err != nil {
		return false, err
	}
	return ix.InUseByOther(path), nil
}

// SetPidAliveForTest overrides the PID-liveness predicate and returns a restore
// func. Test-only seam (used by callers outside the snapshot package, e.g. the
// cmd tests); production code never calls it.
func SetPidAliveForTest(fn func(pid int) bool) (restore func()) {
	prev := pidAlive
	pidAlive = fn
	return func() { pidAlive = prev }
}
