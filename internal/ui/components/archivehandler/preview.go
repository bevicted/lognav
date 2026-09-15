package archivehandler

import (
	"fmt"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/components/queryeditor/highlight"
)

// nameQueryIDGap is the column gap between the instance-name column and the
// queryId column (in display cells). A fixed minimum so the queryId never abuts
// the longest name.
const nameQueryIDGap = 2

// queryIDPrefixLen is how many leading chars of a queryId the preview shows (the
// rest is noise; a prefix is enough to correlate with the server / a resolve arg).
const queryIDPrefixLen = 8

// preview renders an archive summary as cell-native styled lines: the
// (highlighted) query, a blank gap, a metadata block (submitted + earliest
// expiry), another gap, then one styled row per instance (state + queryId
// prefix). It mirrors snapshothandler/preview.go's segment style.
func preview(bundle deps.Bundle, a *archive.Archive) ([][]list.Segment, error) {
	lines := highlight.Segments(bundle.Config, strings.Trim(a.Query, "\n"))
	lines = append(lines, nil) // blank spacer after the query

	lines = append(lines, plain("submitted:   "+formatTime(a.SubmittedAt)))
	lines = append(lines, plain("expires:     "+formatTime(a.EarliestExpiry())))
	lines = append(lines, nil) // blank spacer before the instance table

	configured := config.EffectiveInstances(bundle.Config)
	names := make([]string, len(a.Instances))
	for i := range a.Instances {
		name, err := displayNameForCRN(configured, a.Instances[i].CRN)
		if err != nil {
			return nil, err
		}
		names[i] = name
	}
	nameW := maxNameWidth(names)
	for i := range a.Instances {
		lines = append(lines, instanceRow(bundle, &a.Instances[i], names[i], nameW))
	}
	return lines, nil
}

// displayNameForCRN parses an archive's serialized CRN only when rendering it,
// then maps it through current config or produces the compact fallback label.
func displayNameForCRN(configured []config.ICLInstanceConfig, raw string) (string, error) {
	crn, err := config.CRNFromString(raw)
	if err != nil {
		return "", fmt.Errorf("parse archive instance CRN: %w", err)
	}
	return config.DisplayNameForCRN(configured, crn), nil
}

// maxNameWidth returns the widest instance-name display width across the
// archive's instances, so every queryId column starts at the same offset
// regardless of how name widths differ (e.g. "ca-tor" vs "eu-de").
func maxNameWidth(names []string) int {
	w := 0
	for _, name := range names {
		w = max(w, uniseg.StringWidth(name))
	}
	return w
}

// instanceRow renders one instance line: a state-colored label, the instance
// name padded to nameW, and the queryId prefix. An error message (when present)
// is appended. Padding the name to a common width keeps the queryId column
// aligned across rows.
func instanceRow(bundle deps.Bundle, ie *archive.InstanceEntry, displayName string, nameW int) []list.Segment {
	segs := []list.Segment{
		{Text: padState(ie.State), Style: stateStyle(bundle, ie.State)},
		{Text: padName(displayName, nameW)},
		{Text: strings.Repeat(" ", nameQueryIDGap) + queryIDPrefix(ie.QueryID)},
	}
	if ie.ErrorMessage != "" {
		segs = append(segs, list.Segment{Text: "  " + ie.ErrorMessage})
	}
	return segs
}

// padName right-pads name with spaces to a display width of nameW so the
// following column aligns. Uses display width (not byte length) so wide runes
// pad correctly.
func padName(name string, nameW int) string {
	gap := nameW - uniseg.StringWidth(name)
	if gap <= 0 {
		return name
	}
	return name + strings.Repeat(" ", gap)
}

// stateStyle maps an archive instance state to its preview color (mirrors the
// row-coloring palette: success→green, running→cyan, error/expired→red).
func stateStyle(bundle deps.Bundle, state string) uv.Style {
	st := bundle.Config.Style
	switch state {
	case archive.StateSuccess:
		return uv.Style{Fg: st.SuccessColor.Color}
	case archive.StateRunning:
		return uv.Style{Fg: st.InProgressColor.Color}
	default: // error / expired
		return uv.Style{Fg: st.ErrorColor.Color}
	}
}

// padState left-aligns the state label to a fixed width so the instance names
// align in the preview table.
func padState(state string) string {
	const w = 9 // len("running ") + slack; covers running/success/error/expired
	return fmt.Sprintf("%-*s", w, state)
}

// queryIDPrefix returns a short, stable prefix of the queryId for display.
func queryIDPrefix(id string) string {
	if len(id) <= queryIDPrefixLen {
		return id
	}
	return id[:queryIDPrefixLen]
}

// formatTime renders a timestamp; the zero time renders as a dash.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

// plain wraps text as a single unstyled list line.
func plain(text string) []list.Segment { return list.PlainItem(text) }
