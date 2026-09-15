package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/sessionbus"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/state"
	"github.com/bevicted/lognav/internal/ui/status"
)

// initSnapshot builds the `lognav snapshot` command group for managing the
// snapshot files the TUI writes to snapshot.Dir(). Snapshot state is CRN-keyed;
// the loaded config supplies current display names for its user-facing output.
func defaultSnapshotBundle(_ *cobra.Command) (deps.Bundle, error) {
	return deps.New(config.New(), state.New()), nil
}

func initSnapshot(loadBundle func(*cobra.Command) (deps.Bundle, error)) *cobra.Command {
	snap := &cobra.Command{
		Use:     "snapshot",
		Aliases: []string{"snap", "snapshots"},
		Short:   "Manage saved snapshots",
		Long:    "Snapshot instances are keyed by full ICL CRN. `inspect` and `logs` load current config to display names; `list`, `rm`, `prune`, `path`, `adopt`, and `clip` do not load config. Non-empty version 1 snapshots without persisted size metadata are rejected. `latest` is reserved and cannot be used as a snapshot name.",
		Example: "  lognav snapshot list\n  lognav snapshot inspect latest\n  lognav snapshot clip incident",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	snap.AddCommand(
		newSnapshotList(),
		newSnapshotInspect(loadBundle),
		newSnapshotLogs(loadBundle),
		newSnapshotRm(),
		newSnapshotPrune(),
		newSnapshotPath(),
		newSnapshotAdopt(),
		newSnapshotClip(),
	)
	return snap
}

// listRow is the JSON shape for `list -o json` (stable keys, always present).
type listRow struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Modified  string `json:"modified"`
	Instances int    `json:"instances"`
	Logs      int    `json:"logs"`
	Query     string `json:"query"`
	Search    string `json:"search"`
	JQ        string `json:"jq"`
	SizeBytes int64  `json:"size_bytes"`
	InUse     bool   `json:"in_use"`
	InUseBy   []int  `json:"in_use_by"`
}

// shortID returns the first 7 chars of a snapshot ID (git-style), or the whole
// string when shorter. A legacy/empty ID renders blank — never panics on id[:7].
func shortID(id string) string {
	if len(id) >= 7 {
		return id[:7]
	}
	return id
}

// maxAmbiguousCandidates caps how many candidates an ambiguity error lists, so
// an extremely short prefix does not flood the terminal. The error value itself
// (snapshot.AmbiguousIDError.Matches) stays uncapped; this is presentation only.
const maxAmbiguousCandidates = 10

// formatAmbiguous renders an *snapshot.AmbiguousIDError as a one-line, capped
// candidate list: "<base>: a3f9c21 my-bug, a3f9e04 staging, …; use more characters".
func formatAmbiguous(amb *snapshot.AmbiguousIDError) string {
	shown := amb.Matches
	truncated := false
	if len(shown) > maxAmbiguousCandidates {
		shown = shown[:maxAmbiguousCandidates]
		truncated = true
	}
	parts := make([]string, 0, len(shown))
	for _, m := range shown {
		parts = append(parts, fmt.Sprintf("%s %s", shortID(m.ID), m.Entry.Name))
	}
	list := strings.Join(parts, ", ")
	if truncated {
		list += ", …"
	}
	return fmt.Sprintf("%s: %s; use more characters", amb.Error(), list)
}

// idPrefixHelp is appended to the Long help of every subcommand that resolves a
// snapshot, so the ID-prefix selector is discoverable.
const idPrefixHelp = "An ID may be given as any unambiguous prefix of at least 4 characters, including hyphens; see `snapshot list` for IDs."

func newSnapshotList() *cobra.Command {
	c := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List snapshots",
		Long:    "List saved snapshots newest first. Text columns are NAME, ID, KIND, MODIFIED, INST, LOGS, and IN USE; ID is a 7-character UUID prefix and queries are omitted, so use `inspect` for them. Names exactly matching `auto-YYYYMMDD-HHMMSS[-N].lognav` (with N >= 2) have kind `auto`; every other finalized snapshot has kind `manual`.\n\nJSON output is an array, including `[]` for an empty directory. Every row includes the full query, search, jq, size, and full ID data for scripts, plus `in_use` and a non-null `in_use_by` PID array.",
		Example: "  lognav snapshot list\n  lognav snapshot list -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := getOutputFormat(cmd)
			if err != nil {
				return err
			}
			entries, err := snapshot.Scan()
			if err != nil {
				return err
			}
			rows := gatherListRows(entries)
			out := cmd.OutOrStdout()
			if format == outputJSON {
				return writeListJSON(out, rows)
			}
			// Empty prints the header with no rows (docker-ps idiom), matching
			// stdout stays pipe-clean and no prose needs parsing.
			return renderListText(out, rows)
		},
	}
	addOutputFlag(c)
	c.ValidArgsFunction = cobra.NoFileCompletions
	return c
}

// listEntry is one gathered snapshot: the JSON row plus the two facts only the
// text table needs. summaryOK is carried because the two renderers degrade
// differently — the table prints "?" for the counts a failed summary could not
// produce, while JSON leaves those fields at their zero value. modTime is the
// unformatted timestamp, because the table shows local time and JSON RFC3339.
type listEntry struct {
	row       listRow
	modTime   time.Time
	summaryOK bool
}

// gatherListRows reads every scanned entry once — one Summarize each, off ONE
// shared lock-directory index — so both output formats render from identical
// data. The index is built once per invocation, not once per row: asking
// snapshot.Holders per entry would rescan the lock directory once per entry.
// A failed index is not fatal; it degrades to "held by nobody", exactly as a
// failed per-row Holders call did.
func gatherListRows(entries []snapshot.Entry) []listEntry {
	inUse, _ := snapshot.HoldersIndex()
	rows := make([]listEntry, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, gatherListRow(e, inUse))
	}
	return rows
}

// gatherListRow summarizes one entry's state frame and looks its in-use holders
// up in the caller's index. A summary error is not fatal: it leaves the summary
// fields zero/empty and clears summaryOK, and each renderer decides how to show
// that. Query/Search/JQ carry the FULL value (JSON has no width limit); the text
// table omits the query entirely — use `inspect` to read it.
func gatherListRow(e snapshot.Entry, inUse snapshot.InUseIndex) listEntry {
	le := listEntry{
		modTime: e.ModTime,
		row: listRow{
			Name:      e.Name,
			Kind:      e.Kind.String(),
			Modified:  e.ModTime.Format(time.RFC3339),
			SizeBytes: e.Size,
		},
	}
	if s, err := snapshot.Summarize(e.Path); err == nil {
		le.summaryOK = true
		le.row.ID = s.ID
		le.row.Instances = len(s.Instances)
		le.row.Logs = s.TotalLogs
		le.row.Query = s.Query
		le.row.Search = s.Search
		le.row.JQ = s.JQ
	}
	inUseBy := inUse.Holders(e.Path)
	if inUseBy == nil {
		inUseBy = []int{}
	}
	le.row.InUseBy = inUseBy
	le.row.InUse = len(inUseBy) > 0
	return le
}

// writeListJSON emits the gathered rows as a JSON array. The slice is non-nil
// so an empty snapshot dir prints [] rather than null.
func writeListJSON(out io.Writer, rows []listEntry) error {
	jsonRows := make([]listRow, 0, len(rows))
	for _, r := range rows {
		jsonRows = append(jsonRows, r.row)
	}
	return writeJSON(out, jsonRows)
}

// renderListText prints the snapshot table. The query is intentionally NOT a
// column — a real Dataprime query never fits one line; use `inspect <name>`.
// The ID is shortened and the timestamp localized here, not at gather time; the
// JSON row keeps both in full.
func renderListText(out io.Writer, rows []listEntry) error {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tKIND\tMODIFIED\tINST\tLOGS\tIN USE")
	for _, r := range rows {
		kind := r.row.Kind
		inst, logs, id := "?", "?", ""
		if r.summaryOK {
			inst = strconv.Itoa(r.row.Instances)
			logs = strconv.Itoa(r.row.Logs)
			id = shortID(r.row.ID)
		}
		inUse := ""
		if r.row.InUse {
			inUse = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.row.Name, id, kind, r.modTime.Local().Format("2006-01-02 15:04"), inst, logs, inUse)
	}
	return tw.Flush()
}

// inspectJSON is the JSON shape for `inspect -o json`.
type inspectJSON struct {
	Name               string                 `json:"name"`
	ID                 string                 `json:"id"`
	Kind               string                 `json:"kind"`
	Path               string                 `json:"path"`
	Modified           string                 `json:"modified"`
	Query              string                 `json:"query"`
	Search             string                 `json:"search"`
	JQ                 string                 `json:"jq"`
	Instances          []snapshotInstanceView `json:"instances"`
	TotalLogs          int                    `json:"total_logs"`
	TotalLogsSizeBytes uint64                 `json:"total_logs_size_bytes"`
	InUseBy            []int                  `json:"in_use_by"`
}

// snapshotInstanceView is a display projection. Name is deliberately derived
// from the current config; it is never present in snapshot state.
type snapshotInstanceView struct {
	snapshot.InstanceSnapshot
	Name string `json:"name"`
}

func snapshotInstanceViews(cfg *config.Config, instances []snapshot.InstanceSnapshot) ([]snapshotInstanceView, error) {
	if cfg == nil {
		cfg = config.New()
	}
	effective := config.EffectiveInstances(cfg)
	seen := make(map[string]struct{}, len(instances))
	views := make([]snapshotInstanceView, 0, len(instances))
	for _, inst := range instances {
		crn, err := config.CRNFromString(inst.CRN)
		if err != nil {
			return nil, fmt.Errorf("invalid snapshot instance CRN %q: %w", inst.CRN, err)
		}
		if _, ok := seen[inst.CRN]; ok {
			return nil, fmt.Errorf("duplicate snapshot instance CRN %q", inst.CRN)
		}
		seen[inst.CRN] = struct{}{}
		views = append(views, snapshotInstanceView{
			InstanceSnapshot: inst,
			Name:             config.DisplayNameForCRN(effective, crn),
		})
	}
	return views, nil
}

func displayNames(views []snapshotInstanceView) []string {
	names := make([]string, 0, len(views))
	for _, view := range views {
		names = append(names, view.Name)
	}
	return names
}

// resolveSnapshotInstance accepts an exact stored CRN or one unambiguous
// current display name. CRNs are listed on ambiguity so callers can select the
// explicit identity without ever choosing an arbitrary colliding label.
func resolveSnapshotInstance(views []snapshotInstanceView, selector string) (string, error) {
	for _, view := range views {
		if selector == view.CRN {
			return view.CRN, nil
		}
	}
	var matches []string
	for _, view := range views {
		if selector == view.Name {
			matches = append(matches, view.CRN)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("snapshot instance %q not found (available: %s)", selector, strings.Join(displayNames(views), ", "))
	default:
		return "", fmt.Errorf("snapshot instance name %q is ambiguous; use one of: %s", selector, strings.Join(matches, ", "))
	}
}

func newSnapshotInspect(loaders ...func(*cobra.Command) (deps.Bundle, error)) *cobra.Command {
	loadBundle := defaultSnapshotBundle
	if len(loaders) > 0 {
		loadBundle = loaders[0]
	}
	c := &cobra.Command{
		Use:               "inspect <name|id-prefix|path>",
		Short:             "Inspect a snapshot (query, jq, per-instance breakdown)",
		Long:              "Show a snapshot's saved query, search, jq, full UUID, in-use PIDs, and per-instance state, log count, native-NDJSON size, and elapsed fetch time. Text renders IEC sizes and an exact-byte total. JSON exposes `id`, non-null `in_use_by`, per-instance `logs_size_bytes`, and checked `total_logs_size_bytes`.\n\nAn external path is useful for inspecting a downloaded snapshot before adopting it. " + idPrefixHelp,
		Example:           "  lognav snapshot inspect incident\n  lognav snapshot inspect latest -o json\n  lognav snapshot inspect --no-config ~/Downloads/demo.lognav",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSnapshotNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			bundle, err := loadBundle(cmd)
			if err != nil {
				return err
			}
			format, err := getOutputFormat(cmd)
			if err != nil {
				return err
			}
			return runSnapshotInspect(cmd.OutOrStdout(), bundle.Config, args[0], format)
		},
	}
	addOutputFlag(c)
	return c
}

// runSnapshotInspect resolves, summarizes, and renders one snapshot.
func runSnapshotInspect(out io.Writer, cfg *config.Config, target string, format outputFormat) error {
	e, err := resolveNameOrPath(target)
	if err != nil {
		return WithExit(ExitNoInput, err)
	}
	s, err := snapshot.Summarize(e.Path)
	if err != nil {
		return err
	}
	instances, err := snapshotInstanceViews(cfg, s.Instances)
	if err != nil {
		return err
	}
	inUseBy, holdersErr := snapshot.Holders(e.Path)
	if holdersErr != nil {
		inUseBy = nil
	}
	if inUseBy == nil {
		inUseBy = []int{}
	}
	if format == outputJSON {
		return writeJSON(out, inspectJSON{
			Name: e.Name, ID: s.ID, Kind: e.Kind.String(), Path: e.Path,
			Modified: e.ModTime.Format(time.RFC3339), Query: s.Query, Search: s.Search,
			JQ: s.JQ, Instances: instances, TotalLogs: s.TotalLogs, TotalLogsSizeBytes: s.TotalLogsSizeBytes,
			InUseBy: inUseBy,
		})
	}
	return renderInspectText(out, e, s, instances, inUseBy)
}

func renderInspectText(out io.Writer, e snapshot.Entry, s snapshot.Summary, instances []snapshotInstanceView, inUseBy []int) error {
	kind := e.Kind.String()
	// Labels are left-padded to a fixed width so every value lines up in one
	// column; "In use by:" is the widest label and sets that width.
	const labelWidth = 11
	indent := strings.Repeat(" ", labelWidth)
	fmt.Fprintf(out, "%-*s%s\n", labelWidth, "Name:", e.Name)
	fmt.Fprintf(out, "%-*s%s\n", labelWidth, "ID:", s.ID)
	fmt.Fprintf(out, "%-*s%s\n", labelWidth, "Kind:", kind)
	fmt.Fprintf(out, "%-*s%s\n", labelWidth, "Path:", e.Path)
	fmt.Fprintf(out, "%-*s%s\n", labelWidth, "Saved:", e.ModTime.Local().Format("2006-01-02 15:04:05"))
	inUse := "none"
	if len(inUseBy) > 0 {
		inUse = joinPids(inUseBy)
	}
	fmt.Fprintf(out, "%-*s%s\n", labelWidth, "In use by:", inUse)
	fmt.Fprintf(out, "%-*s%s\n", labelWidth, "Query:", indentMultiline(s.Query, indent))
	if s.Search != "" {
		fmt.Fprintf(out, "%-*s%s\n", labelWidth, "Search:", s.Search)
	}
	if s.JQ != "" {
		fmt.Fprintf(out, "%-*s%s\n", labelWidth, "JQ:", s.JQ)
	}
	fmt.Fprintf(out, "Instances (%d):\n", len(instances))
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tSTATE\tLOGS\tSIZE\tELAPSED")
	for _, inst := range instances {
		fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\t%s\n",
			inst.Name, status.Phase(inst.State).String(), inst.LogCount, formatIECSize(inst.LogsSizeBytes), elapsed(inst.InstanceSnapshot))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "Total logs: %d\n", s.TotalLogs)
	fmt.Fprintf(out, "Total size: %s\n", formatExactIECSize(s.TotalLogsSizeBytes))
	return nil
}

// formatIECSize formats bytes using IEC binary prefixes without a dependency.
func formatIECSize(size uint64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	value := float64(size)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func formatExactIECSize(size uint64) string {
	return fmt.Sprintf("%s (%d bytes)", formatIECSize(size), size)
}

// elapsed renders an instance's fetch duration, clamping negatives to 0.
func elapsed(inst snapshot.InstanceSnapshot) string {
	d := max(time.Duration(inst.LastUpdateTimeMicro-inst.StartTimeMicro)*time.Microsecond, 0)
	return d.Round(time.Millisecond).String()
}

func indentMultiline(s, pad string) string {
	return strings.ReplaceAll(strings.Trim(s, "\n"), "\n", "\n"+pad)
}

//nolint:gocyclo // selector resolution keeps the CLI's related exit cases together.
func newSnapshotLogs(loaders ...func(*cobra.Command) (deps.Bundle, error)) *cobra.Command {
	loadBundle := defaultSnapshotBundle
	if len(loaders) > 0 {
		loadBundle = loaders[0]
	}
	c := &cobra.Command{
		Use:               "logs <name|id-prefix|path>",
		Short:             "Stream one instance's stored logs to stdout",
		Long:              "Output is NDJSON. Each compact line is the ICL-native log object, with no timestamp or severity prefix, wrapper, or duplicated metadata. Its byte count exactly matches that instance's `logs_size_bytes` from `inspect`.\n\nAn instance selector may be an unambiguous current display name or an exact full CRN. It is inferred only when exactly one instance has logs. A colliding display label is rejected and lists CRNs to use. " + idPrefixHelp,
		Example:           "  lognav snapshot logs incident --instance prod-eu\n  lognav snapshot logs --no-config latest | jq .metadata.severity\n  lognav snapshot logs ~/Downloads/demo.lognav | jq .",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSnapshotNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			bundle, err := loadBundle(cmd)
			if err != nil {
				return err
			}
			instance, err := cmd.Flags().GetString("instance")
			if err != nil {
				return err
			}
			e, err := resolveNameOrPath(args[0])
			if err != nil {
				return WithExit(ExitNoInput, err)
			}
			c2, err := snapshot.OpenContainerReadOnly(e.Path)
			if err != nil {
				return err
			}
			defer func() { _ = c2.Close() }()

			snap, err := snapshot.LoadState(c2)
			if err != nil {
				return err
			}
			views, err := snapshotInstanceViews(bundle.Config, snap.InstancePickerSnapshot.Instances)
			if err != nil {
				return err
			}
			if instance == "" {
				// Every instance gets a log frame at save time (empty ones hold
				// an empty array), so auto-selection keys off the non-empty set
				// rather than frame count.
				nonEmpty, err := snapshot.NonEmptyInstanceCRNs(c2)
				if err != nil {
					return err
				}
				switch len(nonEmpty) {
				case 0:
					return WithExit(ExitNoInput, errors.New("snapshot has no instance logs"))
				case 1:
					instance = nonEmpty[0]
				default:
					names := make([]string, 0, len(nonEmpty))
					for _, crn := range nonEmpty {
						name, rerr := resolveSnapshotInstance(views, crn)
						if rerr != nil {
							return rerr
						}
						for _, view := range views {
							if view.CRN == name {
								names = append(names, view.Name)
								break
							}
						}
					}
					return WithExit(ExitUsage, fmt.Errorf(
						"snapshot has %d non-empty instances; choose one with --instance: %s",
						len(nonEmpty), strings.Join(names, ", "),
					))
				}
			}
			instance, err = resolveSnapshotInstance(views, instance)
			if err != nil {
				return WithExit(ExitNoInput, err)
			}

			out := cmd.OutOrStdout()
			emit := logEmitter(out)
			if err := snapshot.StreamInstanceLogs(c2, instance, emit); err != nil {
				if errors.Is(err, snapshot.ErrInstanceNotFound) {
					return WithExit(ExitNoInput, fmt.Errorf("%w (available: %s)", err, strings.Join(displayNames(views), ", ")))
				}
				return err
			}
			return nil
		},
	}
	c.Flags().StringP("instance", "i", "", "instance to stream (required when more than one instance has logs)")
	_ = c.RegisterFlagCompletionFunc("instance", completeInstanceNamesWithConfig(loadBundle))
	return c
}

// logEmitter returns a per-log writer that streams each stored log as one
// NDJSON line in its ICL-native shape: the decoded Log.Data, i.e. the user data
// alongside its labels and metadata. No timestamp/severity prefix and no Log
// wrapper are added, so the record is written as ICL returned it (no `.data.data`
// nesting and no second, parsed metadata copy). Each line is flushed immediately
// so piped consumers see a stream.
func logEmitter(out io.Writer) func(icl.Log) error {
	return func(l icl.Log) error {
		b, err := jsonutil.API.Marshal(l.Data)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%s\n", b)
		return err
	}
}

// resolveRmTargets resolves each token (name-first, ID-prefix fallback), deduping
// the delete set by resolved path. Unresolvable/ambiguous tokens become formatted
// failure lines rather than aborting (unix-rm).
func resolveRmTargets(tokens []string) (targets []snapshot.Entry, failures []string) {
	seenPath := map[string]bool{}
	for _, tok := range tokens {
		e, err := snapshot.ResolveNameOrID(tok)
		if err != nil {
			var amb *snapshot.AmbiguousIDError
			if errors.As(err, &amb) {
				failures = append(failures, fmt.Sprintf("could not resolve %s: %s", tok, formatAmbiguous(amb)))
			} else {
				failures = append(failures, fmt.Sprintf("could not resolve %s: %v", tok, err))
			}
			continue
		}
		if seenPath[e.Path] {
			continue
		}
		seenPath[e.Path] = true
		targets = append(targets, e)
	}
	return targets, failures
}

// deleteRmTargets deletes each target, skipping (and reporting to errOut) any held
// by a live session. didMutate is true only when at least one real removal occurred.
// The lock directory is scanned once for the whole invocation; a scan failure is
// reported per target, as the per-target Holders call used to.
func deleteRmTargets(out, errOut io.Writer, targets []snapshot.Entry) (didMutate bool, skipped []string, errs []error) {
	inUse, herr := snapshot.HoldersIndex()
	for _, e := range targets {
		if herr != nil {
			errs = append(errs, fmt.Errorf("in-use check %s: %w", e.Name, herr))
			continue
		}
		holders := inUse.Holders(e.Path)
		if len(holders) > 0 {
			fmt.Fprintf(errOut, "skipped %s (in use by running lognav pid %s)\n", e.Name, joinPids(holders))
			skipped = append(skipped, e.Name)
			continue
		}
		rmErr := os.Remove(e.Path)
		if rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove %s: %w", e.Name, rmErr))
			continue
		}
		if rmErr == nil { // only a real removal mutates -> gates the broadcast
			didMutate = true
		}
		fmt.Fprintf(out, "Deleted %s\n", e.Name)
	}
	return didMutate, skipped, errs
}

func newSnapshotRm() *cobra.Command {
	return &cobra.Command{
		Use:               "rm <name|id-prefix>...",
		Aliases:           []string{"delete"},
		Short:             "Delete snapshots",
		Long:              "Deletion does not prompt. Selectors are resolved independently: unresolved or ambiguous selectors are reported and skipped, as are snapshots held by a running lognav PID. Other resolved, free targets are still deleted; duplicate selectors delete once.\n\nAn unresolved or ambiguous selector returns exit 66, which takes precedence over an in-use skip's exit 69 when both occur. " + idPrefixHelp,
		Example:           "  lognav snapshot rm old older\n  lognav snapshot delete latest\n  lognav snapshot rm valid missing",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeSnapshotNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			tokens := slices.Clone(args)
			slices.Sort(tokens)
			tokens = slices.Compact(tokens) // drop literal duplicate tokens

			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()

			targets, failures := resolveRmTargets(tokens)
			didMutate, skipped, errs := deleteRmTargets(out, errOut, targets)

			for _, f := range failures {
				fmt.Fprintln(errOut, f)
			}
			if didMutate {
				// synchronous — do not move into a goroutine; see spec §5
				notifySnapshotsDirty(func(f string, a ...any) { fmt.Fprintf(errOut, f, a...) })
			}
			if joined := errors.Join(errs...); joined != nil {
				return joined
			}
			// Exit precedence: a bad argument (ExitNoInput) outranks a transient
			// in-use lock (ExitUnavailable) — check failures FIRST.
			if len(failures) > 0 {
				return WithExit(ExitNoInput, fmt.Errorf("%d snapshot(s) could not be resolved", len(failures)))
			}
			if len(skipped) > 0 {
				return WithExit(ExitUnavailable, fmt.Errorf("skipped %d in-use snapshot(s): %s", len(skipped), strings.Join(skipped, ", ")))
			}
			return nil
		},
	}
}

// pruneTarget is one file slated for deletion (name for output, path for the
// remove, size for the freed-bytes tally).
type pruneTarget struct {
	name string
	path string
	size int64
}

func newSnapshotPrune() *cobra.Command {
	var (
		auto, manual, dryRun bool
		olderThan            string
		keep                 int
	)
	c := &cobra.Command{
		Use:     "prune",
		Short:   "Prune stale files (and selected snapshots)",
		Long:    "Remove orphaned in-progress `.wip` backing files left by dead sessions. A live session's `.wip` is never touched. `--older-than` and `--keep` apply only to snapshots selected by a kind flag; without `--auto` or `--manual`, they do nothing. In-use files are skipped in real and dry runs. Text reports them; JSON writes a `skipped` array alongside deleted names, count, freed bytes, and dry-run status.",
		Example: "  lognav snapshot prune\n  lognav snapshot prune --auto --older-than 7d\n  lognav snapshot prune --auto --manual --keep 10 --dry-run",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := getOutputFormat(cmd)
			if err != nil {
				return err
			}
			var cutoffSet bool
			var cutoff time.Time
			if cmd.Flags().Changed("older-than") {
				d, err := parseOlderThan(olderThan)
				if err != nil {
					return WithExit(ExitUsage, err)
				}
				cutoff = time.Now().Add(-d)
				cutoffSet = true
			}
			keepSet := cmd.Flags().Changed("keep")
			if keepSet && keep < 0 {
				return WithExit(ExitUsage, fmt.Errorf("--keep must be >= 0, got %d", keep))
			}

			targets, err := pruneTargets(auto, manual, cutoffSet, cutoff, keepSet, keep)
			if err != nil {
				return err
			}
			didDelete, perr := runPrune(cmd.OutOrStdout(), format, targets, dryRun)
			if didDelete {
				// synchronous — do not move into a goroutine; see spec §5
				notifySnapshotsDirty(func(f string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), f, a...) })
			}
			return perr
		},
	}
	c.Flags().BoolVarP(&auto, "auto", "a", false, "include finalized automatic snapshots")
	c.Flags().BoolVarP(&manual, "manual", "m", false, "include manual snapshots")
	c.Flags().StringVarP(&olderThan, "older-than", "O", "", "filter selected set to entries older than a duration (e.g. 72h, 7d)")
	c.Flags().IntVarP(&keep, "keep", "k", 0, "protect the newest N of the selected set")
	c.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "print what would be deleted without deleting")
	_ = c.RegisterFlagCompletionFunc("older-than", cobra.NoFileCompletions)
	_ = c.RegisterFlagCompletionFunc("keep", cobra.NoFileCompletions)
	addOutputFlag(c)
	c.ValidArgsFunction = cobra.NoFileCompletions
	return c
}

// pruneTargets builds the deletion set: the always-on stale baseline plus the
// snapshots selected by --auto/--manual then narrowed by older-than and keep.
// The baseline is orphaned .wip only; finalized automatic snapshots are deleted
// only when --auto is given.
func pruneTargets(auto, manual, cutoffSet bool, cutoff time.Time, keepSet bool, keep int) ([]pruneTarget, error) {
	stale, err := snapshot.StaleFiles()
	if err != nil {
		return nil, err
	}
	var targets []pruneTarget
	for _, p := range stale {
		size := int64(0)
		if fi, err := os.Stat(p); err == nil {
			size = fi.Size()
		}
		targets = append(targets, pruneTarget{name: filepath.Base(p), path: p, size: size})
	}

	if auto || manual {
		sel, err := selectFinalized(auto, manual, cutoffSet, cutoff, keepSet, keep)
		if err != nil {
			return nil, err
		}
		for _, e := range sel {
			targets = append(targets, pruneTarget{name: e.Name, path: e.Path, size: e.Size})
		}
	}
	return targets, nil
}

// selectFinalized scans snapshots and keeps the kinds named by auto/manual,
// then narrows by older-than cutoff and the keep-newest count. Automatic
// snapshots exactly match the automatic filename contract (KindAuto); they are
// selected by --auto, not the stale baseline (which is orphaned .wip only).
func selectFinalized(auto, manual, cutoffSet bool, cutoff time.Time, keepSet bool, keep int) ([]snapshot.Entry, error) {
	entries, err := snapshot.Scan()
	if err != nil {
		return nil, err
	}
	var sel []snapshot.Entry
	for _, e := range entries {
		if (auto && e.Kind == snapshot.KindAuto) || (manual && e.Kind == snapshot.KindManual) {
			sel = append(sel, e)
		}
	}
	if cutoffSet {
		sel = slices.DeleteFunc(sel, func(e snapshot.Entry) bool {
			return !e.ModTime.Before(cutoff) // keep only strictly-older entries
		})
	}
	if keepSet {
		if keep >= len(sel) {
			sel = nil
		} else {
			sel = sel[keep:] // entries are newest-first; protect the first keep
		}
	}
	return sel, nil
}

// pruneJSON is the JSON shape for `prune -o json`.
type pruneJSON struct {
	Deleted    []string `json:"deleted"`
	Skipped    []string `json:"skipped"`
	Count      int      `json:"count"`
	FreedBytes int64    `json:"freed_bytes"`
	DryRun     bool     `json:"dry_run"`
}

func runPrune(out io.Writer, format outputFormat, targets []pruneTarget, dryRun bool) (didDelete bool, err error) {
	// Non-nil so an empty/dry-run prune emits "deleted": [] not null (spec:
	// JSON arrays are never null; keeps `jq '.deleted | length'` working).
	deleted := []string{}
	skipped := []string{}
	var freed int64
	var errs []error
	// One lock-directory scan for the whole prune, not one per target.
	inUse, ierr := snapshot.HoldersIndex()
	for _, t := range targets {
		// Skip in-use files in BOTH dry-run and real runs so dry-run's report
		// matches what a real prune would actually do. Fail SAFE: on a scan
		// error, treat every file as in-use and skip it (mirrors rm and the TUI
		// guards). Practically unreachable — Dir() already succeeded above.
		if ierr != nil || inUse.InUse(t.path) {
			skipped = append(skipped, t.name)
			continue
		}
		if !dryRun {
			if rerr := os.Remove(t.path); rerr != nil {
				if errors.Is(rerr, fs.ErrNotExist) {
					continue
				}
				errs = append(errs, fmt.Errorf("remove %s: %w", t.name, rerr))
				continue
			}
			didDelete = true
		}
		deleted = append(deleted, t.name)
		freed += t.size
	}

	if format == outputJSON {
		if jerr := writeJSON(out, pruneJSON{Deleted: deleted, Skipped: skipped, Count: len(deleted), FreedBytes: freed, DryRun: dryRun}); jerr != nil {
			return didDelete, jerr
		}
		return didDelete, errors.Join(errs...)
	}
	verb := "Deleted"
	if dryRun {
		verb = "Would delete"
	}
	for _, n := range deleted {
		fmt.Fprintf(out, "%s %s\n", verb, n)
	}
	fmt.Fprintf(out, "%d file(s), %d bytes freed\n", len(deleted), freed)
	if len(skipped) > 0 {
		fmt.Fprintf(out, "skipped %d in-use file(s): %s\n", len(skipped), strings.Join(skipped, ", "))
	}
	return didDelete, errors.Join(errs...)
}

// parseOlderThan parses a Go duration (72h, 90m) or the standalone <n>d days
// form (7d -> 168h). The two forms do not compose. Negative/zero/overflow are
// rejected.
func parseOlderThan(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return 0, fmt.Errorf("--older-than must be positive: %q", s)
		}
		return d, nil
	}
	if d, ok, err := parseDaysDuration(s); ok {
		return d, err
	}
	return 0, fmt.Errorf("invalid --older-than %q (want a Go duration like 72h, or Nd like 7d)", s)
}

// parseDaysDuration handles the standalone <n>d days form. The bool reports
// whether s matched the Nd shape (so the caller can fall through to its generic
// error otherwise); the error is non-nil only for a matched-but-invalid value.
func parseDaysDuration(s string) (time.Duration, bool, error) {
	days, ok := strings.CutSuffix(s, "d")
	if !ok {
		return 0, false, nil
	}
	n, atoiErr := strconv.Atoi(days)
	if atoiErr != nil {
		return 0, false, nil //nolint:nilerr // non-Nd input: let the caller emit its generic error
	}
	if n <= 0 {
		return 0, true, fmt.Errorf("--older-than must be positive: %q", s)
	}
	const maxDays = int(math.MaxInt64 / int64(24*time.Hour))
	if n > maxDays {
		return 0, true, fmt.Errorf("--older-than too large: %q", s)
	}
	return time.Duration(n) * 24 * time.Hour, true, nil
}

func newSnapshotPath() *cobra.Command {
	return &cobra.Command{
		Use:               "path [<name|id-prefix>|latest]",
		Short:             "Print the snapshot directory, or a snapshot's path",
		Long:              "Output is exactly one path. External paths are not accepted. " + idPrefixHelp,
		Example:           "  lognav snapshot path\n  lognav snapshot path latest",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeSnapshotNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				dir, err := snapshot.Dir()
				if err != nil {
					return err
				}
				fmt.Fprintln(out, dir)
				return nil
			}
			e, err := snapshot.ResolveNameOrID(args[0])
			if err != nil {
				return WithExit(ExitNoInput, err)
			}
			fmt.Fprintln(out, e.Path)
			return nil
		},
	}
}

// completeSnapshotNames offers managed names, latest, and full accepted IDs.
// Inspect and logs also accept an external path, so they retain filesystem
// completion. For variadic rm, every alias of an already-selected snapshot is
// omitted. IDs are offered only when unique because adopted copies can share
// one. Scan and metadata errors are suppressed for shell completion.
func completeSnapshotNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	isRm := cmd != nil && cmd.Name() == "rm"
	allowPaths := cmd != nil && (cmd.Name() == "inspect" || cmd.Name() == "logs")
	if !isRm && len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	entries, err := snapshot.Scan()
	if err != nil {
		return nil, completionDirective(allowPaths)
	}
	ids, idCounts := snapshotCompletionIDs(entries)
	chosen := chosenSnapshotNames(args, isRm)
	out := make([]string, 0, 1+len(entries)*2)
	if !chosen[latestSentinel] {
		out = append(out, latestSentinel)
	}
	for _, entry := range entries {
		if chosen[entry.Name] {
			continue
		}
		out = append(out, entry.Name)
		if id := ids[entry.Path]; idCounts[strings.ToLower(id)] == 1 && !chosen[id] {
			out = append(out, id)
		}
	}
	return out, completionDirective(allowPaths)
}

// snapshotCompletionIDs returns each readable snapshot ID and its case-insensitive count.
func snapshotCompletionIDs(entries []snapshot.Entry) (map[string]string, map[string]int) {
	ids := make(map[string]string, len(entries))
	idCounts := make(map[string]int, len(entries))
	for _, entry := range entries {
		summary, err := snapshot.Summarize(entry.Path)
		if err != nil || summary.ID == "" {
			continue
		}
		ids[entry.Path] = summary.ID
		idCounts[strings.ToLower(summary.ID)]++
	}
	return ids, idCounts
}

// chosenSnapshotNames returns all selected arguments and, for rm, their resolved names.
func chosenSnapshotNames(args []string, isRm bool) map[string]bool {
	chosen := make(map[string]bool, len(args))
	for _, arg := range args {
		chosen[arg] = true
		if !isRm {
			continue
		}
		if entry, err := snapshot.ResolveNameOrID(arg); err == nil {
			chosen[entry.Name] = true
		}
	}
	return chosen
}

func completionDirective(allowPaths bool) cobra.ShellCompDirective {
	if allowPaths {
		return cobra.ShellCompDirectiveDefault
	}
	return cobra.ShellCompDirectiveNoFileComp
}

// completeInstanceNamesWithConfig offers the current display names of the
// snapshot members for --instance. Exact CRNs remain valid selectors but are
// intentionally not completion noise. Any error yields no completions.
func completeInstanceNamesWithConfig(loadBundle func(*cobra.Command) (deps.Bundle, error)) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		e, err := resolveNameOrPath(args[0])
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		c, err := snapshot.OpenContainerReadOnly(e.Path)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		defer func() { _ = c.Close() }()
		snap, err := snapshot.LoadState(c)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		bundle, err := loadBundle(cmd)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		views, err := snapshotInstanceViews(bundle.Config, snap.InstancePickerSnapshot.Instances)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var names []string
		for _, name := range displayNames(views) {
			if strings.HasPrefix(name, prefix) {
				names = append(names, name)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeInstanceNames is retained as a default-config test seam.
func completeInstanceNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeInstanceNamesWithConfig(defaultSnapshotBundle)(cmd, args, toComplete)
}

// adoptDestName normalizes a source basename to a discoverable snapshot name:
// it returns the name unchanged when it already ends in FileExt, otherwise it
// appends FileExt. The extension is normalized only for discoverability by
// Scan/list — the magic header, not the extension, is the validation gate.
func adoptDestName(base string) string {
	if strings.HasSuffix(base, snapshot.FileExt) {
		return base
	}
	return base + snapshot.FileExt
}

// aggregateExit collapses the per-file exit codes from one adopt invocation
// into a single process code: 0 when nothing failed, the shared code when every
// failure is the same class, otherwise ExitGeneral for a mixed set.
func aggregateExit(codes []int) int {
	distinct := map[int]bool{}
	for _, c := range codes {
		if c != ExitOK {
			distinct[c] = true
		}
	}
	switch len(distinct) {
	case 0:
		return ExitOK
	case 1:
		for c := range distinct {
			return c
		}
	}
	return ExitGeneral
}

// transferSnapshot moves (or copies, when doCopy) src onto destPath. Move uses
// os.Rename, falling back to copy-then-remove across a device boundary (EXDEV).
// Copy and the fallback go through atomicCopyFile (temp-in-dest-dir + rename),
// so an existing destination is replaced atomically.
func transferSnapshot(src, destPath string, doCopy bool) error {
	if doCopy {
		return atomicCopyFile(src, destPath)
	}
	if err := os.Rename(src, destPath); err != nil {
		if !isCrossDevice(err) {
			return err
		}
		if cerr := atomicCopyFile(src, destPath); cerr != nil {
			return cerr
		}
		return os.Remove(src) // remove source after a successful cross-device copy
	}
	return nil
}

// isCrossDevice reports whether err is a cross-filesystem rename (EXDEV).
func isCrossDevice(err error) bool {
	var le *os.LinkError
	if errors.As(err, &le) {
		return errors.Is(le.Err, syscall.EXDEV)
	}
	return errors.Is(err, syscall.EXDEV)
}

// adoptConfirmPrompt prints a summary of the sources and asks the user to
// confirm. Returns (true, nil) on confirmation, (false, nil) on decline, and
// (false, err) on read failure.
func adoptConfirmPrompt(out io.Writer, args []string) (bool, error) {
	for _, src := range args {
		if s, serr := snapshot.Summarize(src); serr == nil {
			fmt.Fprintf(out, "  %s — %d instance(s), %d log(s)\n", filepath.Base(src), len(s.Instances), s.TotalLogs)
		} else {
			fmt.Fprintf(out, "  %s\n", filepath.Base(src))
		}
	}
	return confirmYesNo(out, fmt.Sprintf("Adopt %d snapshot file(s)? [y]/n", len(args)))
}

// validateAdoptName checks --name flag constraints before any I/O. Returns a
// WithExit(ExitUsage, …) error on violation, nil otherwise.
func validateAdoptName(name string, numArgs int) error {
	if numArgs > 1 {
		return WithExit(ExitUsage, errors.New("--name cannot be used with multiple sources"))
	}
	if strings.TrimSpace(name) == "" {
		return WithExit(ExitUsage, errors.New("--name must be a plain filename"))
	}
	base, ok := sessionbus.SanitizeBasename(name)
	if !ok {
		return WithExit(ExitUsage, errors.New("--name must be a plain filename (no /, \\ or ..)"))
	}
	if strings.TrimSuffix(base, snapshot.FileExt) == latestSentinel {
		return WithExit(ExitUsage, errors.New(`"latest" is a reserved snapshot name`))
	}
	return nil
}

func newSnapshotAdopt() *cobra.Command {
	var doCopy, force, yes bool
	var name string
	c := &cobra.Command{
		Use:     "adopt <path>...",
		Short:   "Adopt external snapshot files into the snapshot directory",
		Long:    "In-progress `.wip` files cannot be adopted. On a TTY, lognav shows a summary and asks `[y]/n`. `--name` applies to one source only and cannot be `latest`.",
		Example: "  lognav snapshot adopt ~/Downloads/incident.lognav\n  lognav snapshot adopt ~/Downloads/incident.lognav --name incident-prod\n  lognav snapshot adopt ~/Downloads/demo-a.lognav ~/Downloads/demo-b.lognav -y\n  lognav snapshot adopt ~/Downloads/old.lognav --force",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("name") {
				if err := validateAdoptName(name, len(args)); err != nil {
					return err
				}
			}
			dir, err := snapshot.Dir()
			if err != nil {
				return WithExit(ExitGeneral, err)
			}
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()

			if !yes && !force && isTTY() {
				ok, perr := adoptConfirmPrompt(out, args)
				if perr != nil {
					return WithExit(ExitGeneral, perr)
				}
				if !ok {
					fmt.Fprintln(out, "Aborted.")
					return nil // ExitOK — declined is a deliberate no-op
				}
			}

			return adoptAll(out, errOut, dir, name, args, doCopy, force)
		},
	}
	c.Flags().BoolVarP(&doCopy, "copy", "c", false, "copy instead of move (leave the source in place)")
	c.Flags().BoolVarP(&force, "force", "f", false, "skip magic-header check, overwrite existing, ignore missing paths, skip the prompt (never overwrites an in-use destination)")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt (keeps magic + collision guards)")
	c.Flags().StringVarP(&name, "name", "n", "", "name for the adopted snapshot (defaults to the source filename)")
	_ = c.RegisterFlagCompletionFunc("name", cobra.NoFileCompletions)
	return c
}

// adoptAll processes all adopt sources, broadcasts snapshots_dirty if any file
// was successfully transferred, and returns an aggregated exit error.
func adoptAll(out, errOut io.Writer, dir, name string, args []string, doCopy, force bool) error {
	var errs []error
	var codes []int
	var didMutate bool
	for _, src := range args {
		code, ferr := adoptOne(out, errOut, dir, src, name, doCopy, force)
		if ferr == nil && code == ExitOK {
			didMutate = true
		} else if ferr != nil {
			errs = append(errs, ferr)
			codes = append(codes, code)
		}
	}
	if didMutate {
		// synchronous — do not move into a goroutine; see spec §5
		notifySnapshotsDirty(func(f string, a ...any) { fmt.Fprintf(errOut, f, a...) })
	}
	if joined := errors.Join(errs...); joined != nil {
		return WithExit(aggregateExit(codes), joined)
	}
	return nil
}

// adoptOne processes one source path. On success — including a src==dest no-op
// and a --force-ignored missing path — it returns (ExitOK, nil) and may print a
// line to out. On failure it prints to errOut and returns the per-file exit
// code with the error.
func adoptOne(out, errOut io.Writer, dir, src, nameOverride string, doCopy, force bool) (int, error) {
	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if force {
				return ExitOK, nil // --force ignores missing paths
			}
			fmt.Fprintf(errOut, "adopt %s: does not exist\n", src)
			return ExitNoInput, fmt.Errorf("%s: does not exist", src)
		}
		fmt.Fprintf(errOut, "adopt %s: %v\n", src, err)
		return ExitGeneral, err
	}
	if info.IsDir() {
		fmt.Fprintf(errOut, "adopt %s: is a directory, not a snapshot\n", src)
		return ExitNoInput, fmt.Errorf("%s: is a directory", src)
	}

	base := filepath.Base(src)
	if strings.HasSuffix(base, snapshot.WipSuffix) {
		fmt.Fprintf(errOut, "adopt %s: refusing to adopt an in-progress .wip backing file\n", src)
		return ExitNoInput, fmt.Errorf("%s: in-progress .wip file", src)
	}

	destName := adoptDestName(base)
	if nameOverride != "" {
		destName = adoptDestName(nameOverride)
	}
	destPath := filepath.Join(dir, destName)

	absSrc, err := filepath.Abs(src)
	if err != nil {
		fmt.Fprintf(errOut, "adopt %s: %v\n", src, err)
		return ExitGeneral, err
	}
	if filepath.Clean(absSrc) == destPath {
		fmt.Fprintf(out, "%s already in the snapshot directory\n", destName)
		return ExitOK, nil
	}

	if !force {
		if verr := snapshot.VerifyFileMagic(src); verr != nil {
			fmt.Fprintf(errOut, "adopt %s: not a lognav snapshot (%v)\n", src, verr)
			return ExitNoInput, fmt.Errorf("%s: %w", src, verr)
		}
	}

	if code, cerr := checkDestCollision(errOut, src, destName, destPath, force); cerr != nil {
		return code, cerr
	}

	if terr := transferSnapshot(src, destPath, doCopy); terr != nil {
		fmt.Fprintf(errOut, "adopt %s: %v\n", src, terr)
		return ExitGeneral, terr
	}
	fmt.Fprintf(out, "Adopted %s\n", destName)
	return ExitOK, nil
}

// checkDestCollision checks whether the destination path already exists.
// If it is held by a live lognav process, it always blocks (even with --force).
// If it exists but is not in use, it blocks unless --force.
// Returns (ExitOK, nil) when the caller may proceed.
func checkDestCollision(errOut io.Writer, src, destName, destPath string, force bool) (int, error) {
	_, statErr := os.Stat(destPath)
	if statErr != nil {
		if !errors.Is(statErr, fs.ErrNotExist) {
			fmt.Fprintf(errOut, "adopt %s: stat destination %s: %v\n", src, destName, statErr)
			return ExitGeneral, fmt.Errorf("stat %s: %w", destName, statErr)
		}
		return ExitOK, nil // destination does not exist — safe to write
	}
	// Destination exists: in-use check first (always blocks, even --force),
	// then plain collision (gated by --force).
	holders, herr := snapshot.Holders(destPath)
	if herr != nil {
		// Fail safe: cannot confirm the destination is free, so treat it as
		// in-use and refuse to overwrite (mirrors runPrune's in-use-check stance).
		fmt.Fprintf(errOut, "adopt %s: cannot verify destination %s is free: %v\n", src, destName, herr)
		return ExitUnavailable, fmt.Errorf("%s: in-use check failed: %w", destName, herr)
	}
	if len(holders) > 0 {
		fmt.Fprintf(errOut, "adopt %s: destination %s in use by running lognav (pid %s)\n", src, destName, joinPids(holders))
		return ExitUnavailable, fmt.Errorf("%s: destination in use", destName)
	}
	if !force {
		fmt.Fprintf(errOut, "adopt %s: snapshot %s already exists\n", src, destName)
		return ExitGeneral, fmt.Errorf("%s already exists", destName)
	}
	return ExitOK, nil
}

// joinPids formats holder PIDs as a comma-separated string (no brackets).
func joinPids(pids []int) string {
	strs := make([]string, len(pids))
	for i, p := range pids {
		strs[i] = strconv.Itoa(p)
	}
	return strings.Join(strs, ", ")
}

// atomicCopyFile copies src to destPath by writing a 0o600 temp file in the
// destination directory, fsyncing it, and renaming it onto destPath (atomic
// replace on the same filesystem). The temp file is removed on any error.
func atomicCopyFile(src, destPath string) error {
	in, err := os.Open(src) // #nosec G304 -- caller-provided snapshot path
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".adopt-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		cleanup()
		return err
	}
	return nil
}
