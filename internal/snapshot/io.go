package snapshot

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/jsonutil"
)

const (
	frameState          = "state"
	frameInstancePrefix = "i_"
)

// CopyWithState writes a snapshot to dst driven by the instances DECLARED in
// snap. For each declared instance it copies the i_<full-crn> frame verbatim from
// src (raw stored bytes — no decode/re-encode) when present, else writes an
// empty frame; orphan src frames whose instance is not declared in snap are
// skipped. A nil src writes an empty frame for every declared instance (the
// no-durable-logs degrade path). The state frame is written last, from snap —
// and gets a fresh UUID when snap.ID is empty (the manual-save case), since the
// result is a new snapshot rather than a byte copy of src.
//
// The manual-save path uses this instead of rebuilding logs: log frames are
// immutable after a fetch, so the durable backing frame is authoritative and is
// reused byte-for-byte; only the metadata (the state frame) may have changed.
func CopyWithState(dst, src *Container, snap Snapshot) error {
	for _, inst := range snap.InstancePickerSnapshot.Instances {
		name := frameInstancePrefix + inst.CRN
		if src != nil {
			data, err := src.ReadFrame(name)
			if err == nil {
				if aerr := dst.AppendFrame(name, data); aerr != nil {
					return fmt.Errorf("copy frame %q: %w", inst.CRN, aerr)
				}
				continue
			}
			if !errors.Is(err, ErrFrameDoesNotExist) {
				return fmt.Errorf("read frame %q: %w", inst.CRN, err)
			}
			// frame absent in src → fall through to an empty frame
		}
		if err := SaveInstanceFrame(dst, inst.CRN, nil); err != nil {
			return fmt.Errorf("write empty frame %q: %w", inst.CRN, err)
		}
	}
	return SaveStateFrame(dst, snap)
}

// EnsureInstanceFrames writes one empty log frame for each declared instance
// missing from c. Existing frames, including frames with logs, are left intact.
// Callers use it before SaveStateFrame so every state member has a corresponding
// frame even when its query returned no logs or failed.
func EnsureInstanceFrames(c *Container, instances []InstanceSnapshot) error {
	names := c.GetFrameNames()
	existing := make(map[string]struct{}, len(names))
	for _, name := range names {
		existing[name] = struct{}{}
	}
	for _, inst := range instances {
		name := frameInstancePrefix + inst.CRN
		if _, ok := existing[name]; ok {
			continue
		}
		if err := SaveInstanceFrame(c, inst.CRN, nil); err != nil {
			return fmt.Errorf("write empty frame %q: %w", inst.CRN, err)
		}
		existing[name] = struct{}{}
	}
	return nil
}

// SaveInstanceFrame prepares, compresses, and writes one instance's logs.
func SaveInstanceFrame(c *Container, instance string, logs []icl.Log) error {
	_, err := SaveInstanceFrameWithSize(c, instance, logs)
	return err
}

// SaveInstanceFrameWithSize prepares, compresses, and writes one instance's
// logs. It returns the exact number of native-shape NDJSON bytes snapshot logs
// emits so state writers can persist the size without re-encoding logs.
func SaveInstanceFrameWithSize(c *Container, instance string, logs []icl.Log) (uint64, error) {
	compressed, logsSizeBytes, err := PrepareInstanceFrame(logs)
	if err != nil {
		return 0, fmt.Errorf("prepare logs for %s: %w", instance, err)
	}
	if err := c.AppendFrame(frameInstancePrefix+instance, compressed); err != nil {
		return 0, fmt.Errorf("write log frame for %s: %w", instance, err)
	}
	return logsSizeBytes, nil
}

// PrepareInstanceFrame returns a compressed log frame and its exact native
// NDJSON output size. Each record is the deterministic compact encoding of
// Log.Data followed by one newline, exactly as snapshot logs writes it.
func PrepareInstanceFrame(logs []icl.Log) ([]byte, uint64, error) {
	var logsSizeBytes uint64
	for _, log := range logs {
		data, err := jsonutil.API.Marshal(log.Data)
		if err != nil {
			return nil, 0, err
		}
		lineSize := uint64(len(data)) + 1
		if logsSizeBytes > ^uint64(0)-lineSize {
			return nil, 0, errors.New("native NDJSON size overflows uint64")
		}
		logsSizeBytes += lineSize
	}
	compressed, err := gzipLogsJSON(logs)
	if err != nil {
		return nil, 0, err
	}
	return compressed, logsSizeBytes, nil
}

// CompressInstanceLogs prepares a compressed instance frame off-loop and
// returns its native NDJSON size with it.
func CompressInstanceLogs(logs []icl.Log) ([]byte, uint64, error) {
	return PrepareInstanceFrame(logs)
}

// WriteCompressedInstanceFrame writes pre-compressed instance log data to the container.
func WriteCompressedInstanceFrame(c *Container, instance string, compressed []byte) error {
	if err := c.AppendFrame(frameInstancePrefix+instance, compressed); err != nil {
		return fmt.Errorf("write log frame for %s: %w", instance, err)
	}
	return nil
}

// SaveStateFrame validates, marshals, and writes the state frame to the container.
func SaveStateFrame(c *Container, snap Snapshot) error {
	if err := validateLogSizes(snap.InstancePickerSnapshot.Instances); err != nil {
		return err
	}
	if snap.ID == "" {
		snap.ID = uuid.NewString()
	}
	stateJSON, err := jsonutil.API.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	stateData, err := gzipBytes(stateJSON)
	if err != nil {
		return fmt.Errorf("compress state: %w", err)
	}
	return c.AppendFrame(frameState, stateData)
}

// LoadInstanceLogs reads and decompresses a single instance's logs from the container.
func LoadInstanceLogs(c *Container, instance string) ([]icl.Log, error) {
	data, err := gunzipRead(c, frameInstancePrefix+instance)
	if err != nil {
		return nil, fmt.Errorf("read logs for %s: %w", instance, err)
	}
	var logs []icl.Log
	if err := jsonutil.API.Unmarshal(data, &logs); err != nil {
		return nil, fmt.Errorf("unmarshal logs for %s: %w", instance, err)
	}
	return logs, nil
}

// LoadState reads just the snapshot state (without logs) from the container.
func LoadState(c *Container) (Snapshot, error) {
	data, err := gunzipRead(c, frameState)
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := jsonutil.API.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, err
	}
	if err := validateLogSizes(snap.InstancePickerSnapshot.Instances); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// validateLogSizes enforces the version 1 state contract without loading log
// frames. Pre-size non-empty version 1 snapshots therefore fail closed.
func validateLogSizes(instances []InstanceSnapshot) error {
	for _, inst := range instances {
		if inst.LogCount == 0 && inst.LogsSizeBytes != 0 {
			return fmt.Errorf("snapshot instance %q has zero logs but non-zero logs_size_bytes", inst.CRN)
		}
		if inst.LogCount != 0 && inst.LogsSizeBytes == 0 {
			return fmt.Errorf("snapshot instance %q has non-zero logs but zero logs_size_bytes", inst.CRN)
		}
	}
	return nil
}

// streaming via sonic regressed at p=0.05 vs ReadAll+Unmarshal
// (see BenchmarkGunzipUnmarshal_Large vs BenchmarkGunzipReadAllUnmarshal_Large);
// ReadAll path kept.
func gunzipRead(c *Container, name string) ([]byte, error) {
	raw, err := c.ReadFrame(name)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer gz.Close() //nolint:errcheck // reader close on cleanup, error not actionable
	return io.ReadAll(gz)
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gzipLogsJSON marshals logs as a single JSON array and gzip-compresses the result.
func gzipLogsJSON(logs []icl.Log) ([]byte, error) {
	data, err := jsonutil.API.Marshal(logs)
	if err != nil {
		return nil, err
	}
	return gzipBytes(data)
}

// ErrInstanceNotFound is returned when a snapshot has no frame for the
// requested instance.
var ErrInstanceNotFound = errors.New("instance frame not found")

// InstanceCRNs returns the instance CRNs stored in the container (the i_
// frame names with their prefix stripped), in container order.
func InstanceCRNs(c *Container) []string {
	var names []string
	for _, n := range c.GetFrameNames() {
		if s, ok := strings.CutPrefix(n, frameInstancePrefix); ok {
			names = append(names, s)
		}
	}
	return names
}

// NonEmptyInstanceCRNs returns the CRNs of instances that hold at least one
// log, in container frame order. Empty instances still get a log frame written
// at save time (an empty JSON array), so InstanceNames alone cannot distinguish
// them; emptiness is read from the state frame's per-instance LogCount. The
// result is the frame-ordered subset whose LogCount > 0.
func NonEmptyInstanceCRNs(c *Container) ([]string, error) {
	snap, err := LoadState(c)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(snap.InstancePickerSnapshot.Instances))
	for _, inst := range snap.InstancePickerSnapshot.Instances {
		counts[inst.CRN] = inst.LogCount
	}
	var names []string
	for _, n := range InstanceCRNs(c) {
		if counts[n] > 0 {
			names = append(names, n)
		}
	}
	return names, nil
}

// StreamInstanceLogs reads the i_<full-crn> frame and invokes fn once per log,
// decoding the gzipped JSON array element-by-element so memory stays flat
// regardless of instance size. Returns ErrInstanceNotFound if the frame is
// absent; a non-nil fn error stops iteration and is returned. (LoadInstanceLogs
// stays for the bulk TUI-restore path; this is the streaming CLI variant.)
func StreamInstanceLogs(c *Container, instance string, fn func(icl.Log) error) error {
	raw, err := c.ReadFrame(frameInstancePrefix + instance)
	if err != nil {
		if errors.Is(err, ErrFrameDoesNotExist) {
			return fmt.Errorf("%w: %s", ErrInstanceNotFound, instance)
		}
		return err
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer gz.Close() //nolint:errcheck // reader close on cleanup, error not actionable

	dec := json.NewDecoder(gz)
	if _, err := dec.Token(); err != nil { // consume opening '['
		return err
	}
	for dec.More() {
		var log icl.Log
		if err := dec.Decode(&log); err != nil {
			return err
		}
		if err := fn(log); err != nil {
			return err
		}
	}
	return nil
}
