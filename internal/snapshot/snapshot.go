package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/xdg"
)

const (
	FileExt             = ".lognav"
	WipSuffix           = FileExt + ".wip"
	autoPrefix          = "auto-"
	autoTimestampLayout = "20060102-150405"
	dirName             = "snapshots"
)

// autoSnapshotName returns candidate number for an automatic snapshot. The
// first candidate is unsuffixed; collision candidates begin at 2.
func autoSnapshotName(now time.Time, candidate uint64) string {
	name := autoPrefix + now.Format(autoTimestampLayout)
	if candidate > 1 {
		name += fmt.Sprintf("-%d", candidate)
	}
	return name + FileExt
}

// isAutoSnapshotName reports whether name is a canonical finalized automatic
// snapshot name.
func isAutoSnapshotName(name string) bool {
	stem, ok := strings.CutSuffix(name, FileExt)
	if !ok {
		return false
	}
	rest, ok := strings.CutPrefix(stem, autoPrefix)
	if !ok {
		return false
	}
	parts := strings.Split(rest, "-")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	timestamp := strings.Join(parts[:2], "-")
	parsed, err := time.ParseInLocation(autoTimestampLayout, timestamp, time.Local)
	if err != nil || parsed.Format(autoTimestampLayout) != timestamp {
		return false
	}
	if len(parts) == 2 {
		return true
	}
	candidate, err := strconv.ParseUint(parts[2], 10, 64)
	return err == nil && candidate >= 2 && strconv.FormatUint(candidate, 10) == parts[2]
}

// CreateWip exclusively creates a hidden PID-stamped work-in-progress file.
// The random token is intentionally opaque; the final underscore field lets
// stale cleanup identify the owning process.
func CreateWip(dir string, pid int) (*os.File, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("invalid wip PID %d", pid)
	}
	return os.CreateTemp(dir, fmt.Sprintf(".*_%d%s", pid, WipSuffix)) // #nosec G304 -- caller controls managed snapshot directory
}

// pidAlive reports whether a process with the given PID is currently running.
// A nil error from kill(pid, 0) means the process exists and we may signal it.
// Package var so tests can substitute it. Mirrors the liveness idiom in
// internal/sessionbus/sockets.go.
var pidAlive = func(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// Dir returns the snapshot directory path, creating it if needed.
func Dir() (string, error) {
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

type Snapshot struct {
	// ID is a v4 UUID minted once when the snapshot's state frame is first
	// written (snapshot.SaveStateFrame). It travels with the file's bytes, so a
	// byte-level copy (or an adopt) keeps the same ID; a manual save builds a new
	// snapshot and mints a fresh one, keeping IDs unique for FindByID/ResolveByID.
	// Empty on pre-identity (legacy) snapshots, which is rendered blank.
	ID     string `json:"id,omitempty"`
	Query  string `json:"query"`
	Search string `json:"search"`
	JQ     string `json:"jq"`
	// Filters is the applied filter rule set. Optional: omitted from the JSON
	// when empty, and absent in pre-redesign snapshots, which unmarshal it to a
	// nil slice ("load empty"). New field; ignored by older binaries.
	Filters                []filter.Rule          `json:"filters,omitempty"`
	InstancePickerSnapshot InstancePickerSnapshot `json:"instance_picker"`
}

type InstancePickerSnapshot struct {
	Instances []InstanceSnapshot `json:"instances"`
}

type InstanceSnapshot struct {
	CRN                 string `json:"crn"`
	State               int    `json:"state"`
	LogCount            int    `json:"log_count"`
	LogsSizeBytes       uint64 `json:"logs_size_bytes"`
	StartTimeMicro      int64  `json:"start_time_micro"`
	LastUpdateTimeMicro int64  `json:"last_update_time_micro"`
	// Message is the instance's fetch-error text (the logviewer displays it when
	// the store holds no logs). Empty for non-errored instances. Persisted so a
	// restored errored instance still surfaces why its fetch failed.
	Message string `json:"message,omitempty"`
}

// ValidateInstanceSnapshotCRNs parses every declared CRN and rejects duplicate
// identities without touching log frames. Callers use it when consuming state;
// log-frame absence and corruption stay lazy access errors.
func ValidateInstanceSnapshotCRNs(instances []InstanceSnapshot) error {
	seen := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		if _, err := config.CRNFromString(inst.CRN); err != nil {
			return fmt.Errorf("invalid snapshot instance CRN %q: %w", inst.CRN, err)
		}
		if _, ok := seen[inst.CRN]; ok {
			return fmt.Errorf("duplicate snapshot instance CRN %q", inst.CRN)
		}
		seen[inst.CRN] = struct{}{}
	}
	return nil
}

// CleanStaleWip removes .lognav.wip backing files left from crashed sessions.
// Only hidden WIPs with a canonical positive PID in their final
// underscore-delimited field can belong to a live process. All other names are
// stale and removed.
func CleanStaleWip() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), WipSuffix) {
			continue
		}
		if pid, ok := wipPID(e.Name()); ok && pidAlive(pid) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	return nil
}

// wipPID extracts the owning canonical positive PID from a hidden WIP name.
func wipPID(name string) (int, bool) {
	if !strings.HasPrefix(name, ".") {
		return 0, false
	}
	base, ok := strings.CutSuffix(name, WipSuffix)
	if !ok {
		return 0, false
	}
	i := strings.LastIndex(base, "_")
	if i <= 1 || i == len(base)-1 {
		return 0, false
	}
	return pidFromStampedName(name, WipSuffix)
}

// pidFromStampedName extracts the final underscore-delimited PID from a
// PID-stamped backing file. It rejects zero, signs, padding, overflow, and
// nonnumeric fields so malformed WIPs are eligible for stale cleanup.
func pidFromStampedName(name, suffix string) (int, bool) {
	base, ok := strings.CutSuffix(name, suffix)
	if !ok {
		return 0, false
	}
	i := strings.LastIndex(base, "_")
	if i < 0 || i == len(base)-1 {
		return 0, false
	}
	pid, err := strconv.Atoi(base[i+1:])
	if err != nil || pid <= 0 || strconv.Itoa(pid) != base[i+1:] {
		return 0, false
	}
	return pid, true
}
