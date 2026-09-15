// Package archive is the local registry for IBM Cloud Logs background queries dispatched
// from lognav. Each archive is one JSON file holding all instances' queryIds + per-instance
// state; collecting an archive produces a normal .lognav snapshot. Filesystem-only, no
// network, mirroring internal/snapshot's management split.
package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/xdg"
)

const (
	FileExt = ".lognav-archive"
	// TTL is the background-query result retention (Coralogix ~30d); used only as a
	// display/GC heuristic from the saved submittedAt, never for correctness.
	TTL = 30 * 24 * time.Hour

	StateRunning = "running"
	StateSuccess = "success"
	StateError   = "error" // subsumes server "cancelled" and not-found for v1
	StateExpired = "expired"
)

// InstanceEntry is one instance's background query within an archive.
type InstanceEntry struct {
	CRN          string    `json:"crn"`
	QueryID      string    `json:"queryId"`
	State        string    `json:"state"`
	LastPolledAt time.Time `json:"lastPolledAt,omitzero"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
}

// Archive is one dispatched background query across instances.
type Archive struct {
	Name        string          `json:"name"`
	Query       string          `json:"query"`
	SubmittedAt time.Time       `json:"submittedAt"`
	Instances   []InstanceEntry `json:"instances"`
}

// IsReady reports whether every instance reached a terminal state (success/error/expired).
func (a *Archive) IsReady() bool {
	for i := range a.Instances {
		if a.Instances[i].State == StateRunning {
			return false
		}
	}
	return len(a.Instances) > 0
}

// EarliestExpiry is SubmittedAt + TTL (submit-loop skew is sub-second; a single top-level
// timestamp is sufficient for a 30d heuristic).
func (a *Archive) EarliestExpiry() time.Time { return a.SubmittedAt.Add(TTL) }

// IsExpired reports whether the archive is past its TTL relative to now.
func (a *Archive) IsExpired(now time.Time) bool { return now.After(a.EarliestExpiry()) }

// dirFn is overridable in tests.
var dirFn = defaultDir

// Dir returns the archive registry directory (created if missing), parallel to the snapshot
// dir but separate so snapshot Scan/.inuse/coloring stay uncontaminated.
func Dir() (string, error) { return dirFn() }

// dirName is the archive subdir under the XDG data root, parallel to
// snapshot.Dir()'s "snapshots".
const dirName = "archives"

func defaultDir() (string, error) {
	// Mirror snapshot.Dir(): same XDG_DATA_HOME root, distinct subdir. Using
	// xdg.GetDataPath (not os.UserConfigDir) keeps archives beside snapshots even
	// on Linux, where XDG_CONFIG_HOME and XDG_DATA_HOME differ.
	p, err := xdg.GetDataPath()
	if err != nil {
		return "", err
	}
	p = filepath.Join(p, dirName)
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}

// PathFor returns the on-disk path for an archive name.
func PathFor(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name+FileExt), nil
}

// Save atomically replaces the archive selected by a.Name. It is used to update
// existing archives after a poll.
func Save(a *Archive) error {
	path, err := PathFor(a.Name)
	if err != nil {
		return err
	}
	return saveAt(path, a, false)
}

// Create atomically reserves a selector for a newly dispatched archive. If the
// requested selector already exists, it appends a numeric suffix and updates
// a.Name. Existing archives are never overwritten.
func Create(a *Archive) error {
	base := a.Name
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		path, err := PathFor(name)
		if err != nil {
			return err
		}
		archiveCopy := *a
		archiveCopy.Name = name
		if err := saveAt(path, &archiveCopy, true); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return err
		}
		a.Name = name
		return nil
	}
}

// saveAt writes a to path through a unique temporary file. When noClobber is
// set, the final link atomically fails if path already exists.
func saveAt(path string, a *Archive, noClobber bool) error {
	data, err := jsonutil.API.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".archive-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if noClobber {
		return os.Link(tmpPath, path)
	}
	return os.Rename(tmpPath, path)
}

// Load reads an archive file.
func Load(path string) (*Archive, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- archive dir confined
	if err != nil {
		return nil, err
	}
	var wire struct {
		Instances []struct {
			Name json.RawMessage `json:"name"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	for _, instance := range wire.Instances {
		if len(instance.Name) > 0 {
			return nil, errors.New("archive instance names are unsupported")
		}
	}
	var a Archive
	if err := jsonutil.API.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}
