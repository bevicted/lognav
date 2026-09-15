package snapshot

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"
)

// ErrRenameDestExists is returned by RenameNoClobber when dst already exists.
var ErrRenameDestExists = errors.New("destination already exists")

// RenameCleanupError reports that a no-clobber link was published but removing
// its source failed. The destination is complete and must be treated as
// published; the source remains present.
type RenameCleanupError struct {
	Err error
}

func (e *RenameCleanupError) Error() string {
	return fmt.Sprintf("remove published source: %v", e.Err)
}

func (e *RenameCleanupError) Unwrap() error { return e.Err }

var renameRemove = os.Remove

// RenameNoClobber publishes src at dst, failing atomically (never overwriting)
// if dst exists. os.Rename clobbers on POSIX, which would let two concurrent
// publishers destroy a snapshot; os.Link returns EEXIST atomically instead,
// then the source is unlinked. src and dst are in the same managed dir (same
// fs), so Link cannot hit EXDEV.
//
// If source cleanup fails after the link succeeds, dst remains published and
// src remains present. The returned *RenameCleanupError makes that partial
// cleanup outcome explicit.
func RenameNoClobber(src, dst string) error {
	if err := os.Link(src, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrRenameDestExists
		}
		return err
	}
	if err := renameRemove(src); err != nil {
		return &RenameCleanupError{Err: err}
	}
	return nil
}

// PublishAutoSnapshot publishes a synced, closed WIP under the canonical
// automatic name derived from startedAt. beforeAttempt runs for every candidate
// immediately before its no-clobber attempt, allowing callers to preclaim the
// exact destination. Only destination-exists failures retry.
func PublishAutoSnapshot(wipPath string, startedAt time.Time, beforeAttempt func(candidatePath string) error) (string, error) {
	dir := filepath.Dir(wipPath)
	for candidate := uint64(1); ; candidate++ {
		finalPath := filepath.Join(dir, autoSnapshotName(startedAt, candidate))
		if beforeAttempt != nil {
			if err := beforeAttempt(finalPath); err != nil {
				return "", err
			}
		}
		err := RenameNoClobber(wipPath, finalPath)
		if err == nil {
			return finalPath, nil
		}
		var cleanupErr *RenameCleanupError
		if errors.As(err, &cleanupErr) {
			return finalPath, cleanupErr
		}
		if !IsExistErr(err) {
			return "", err
		}
		if candidate == math.MaxUint64 {
			return "", errors.New("automatic snapshot suffix overflow")
		}
	}
}

// IsExistErr reports whether err is the dest-exists sentinel.
func IsExistErr(err error) bool { return errors.Is(err, ErrRenameDestExists) }
