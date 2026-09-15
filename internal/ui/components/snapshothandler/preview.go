package snapshothandler

import (
	"strings"
	"time"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/instancepicker"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/components/queryeditor/highlight"
	"github.com/bevicted/lognav/internal/ui/status"
)

// loadPreview reads the state frame and renders a summary for preview display.
func loadPreview(bundle deps.Bundle, c *snapshot.Container) ([][]list.Segment, error) {
	snap, err := snapshot.LoadState(c)
	if err != nil {
		return nil, err
	}
	if err := snapshot.ValidateInstanceSnapshotCRNs(snap.InstancePickerSnapshot.Instances); err != nil {
		return nil, err
	}
	return preview(bundle, &snap)
}

// preview renders a snapshot summary as cell-native styled lines: the query,
// a blank gap, then one row per instance. Instance rows are built with
// instancepicker.InstanceRowSegments — the same formatter the live picker uses
// — so the preview is an exact replica of the instance picker's table.
func preview(bundle deps.Bundle, s *snapshot.Snapshot) ([][]list.Segment, error) {
	// Highlight the query with the same Dataprime syntax coloring as the live
	// query editor and the query-file preview (highlight.Segments/CellStyle).
	lines := highlight.Segments(bundle.Config, strings.Trim(s.Query, "\n"))
	lines = append(lines, nil, nil) // two blank spacer lines (matches the old "\n\n\n")

	instances := s.InstancePickerSnapshot.Instances
	names := make([]string, len(instances))
	for i, inst := range instances {
		crn, err := config.CRNFromString(inst.CRN)
		if err != nil {
			return nil, err
		}
		names[i] = config.DisplayNameForCRN(config.EffectiveInstances(bundle.Config), crn)
	}

	// Measure column widths (mirrors instancepicker.refreshList).
	var longestName, longestTime int
	times := make([]string, len(instances))
	for i, inst := range instances {
		if len(names[i]) > longestName {
			longestName = len(names[i])
		}
		d := time.Duration(inst.LastUpdateTimeMicro-inst.StartTimeMicro) * time.Microsecond
		times[i] = instancepicker.FormatElapsed(bundle.Config.Style.ElapsedFetchTimeFormat, d)
		if len(times[i]) > longestTime {
			longestTime = len(times[i])
		}
	}

	labelWidth := status.MaxLabelWidth(bundle)
	for i, inst := range instances {
		state := status.Phase(inst.State)
		lines = append(lines, instancepicker.InstanceRowSegments(
			state.Label(bundle), state.LabelStyle(bundle), labelWidth,
			names[i], longestName, inst.LogCount, longestTime, times[i],
		))
	}

	return lines, nil
}
