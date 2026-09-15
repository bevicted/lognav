package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/rivo/uniseg"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/ui/status"
)

type queryTerminalCapabilities struct {
	Repaint bool
	Color   bool
}

// queryStderrCapabilities is separate from stdin's editor TTY check and never
// inspects stdout: progress belongs only to stderr.
var queryStderrCapabilities = func(out io.Writer) queryTerminalCapabilities {
	file, ok := out.(*os.File)
	if !ok || !term.IsTerminal(file.Fd()) {
		return queryTerminalCapabilities{}
	}
	return queryTerminalCapabilities{Repaint: true, Color: os.Getenv("TERM") != "dumb"}
}

type queryMemberView struct {
	Name    string
	Phase   status.Phase
	Count   int
	Started time.Time
	Updated time.Time
	Live    bool
}

// queryProgressReporter owns stderr progress output. Query workers only mutate
// member state; they never write output, so a slow diagnostic sink cannot hold
// up authentication or stream cancellation.
type queryProgressReporter struct {
	stderr   io.Writer
	style    config.Style
	members  []queryMember
	now      func() time.Time
	caps     queryTerminalCapabilities
	interval time.Duration

	stop               chan struct{}
	done               chan struct{}
	once               sync.Once
	rows               int
	progressSuppressed bool
}

func newQueryProgressReporter(stderr io.Writer, cfg *config.Config, members []queryMember, suppressProgress bool) *queryProgressReporter {
	return &queryProgressReporter{
		stderr: stderr, style: cfg.Style, members: members, now: queryNow,
		caps: queryStderrCapabilities(stderr), interval: cfg.Core.RedrawInterval(), stop: make(chan struct{}), done: make(chan struct{}),
		progressSuppressed: suppressProgress,
	}
}

// Start begins periodic terminal repainting. Redirected output intentionally
// has no ticker: it is emitted once at completion as bounded plain text.
func (r *queryProgressReporter) Start() {
	if r.progressSuppressed || !r.caps.Repaint {
		return
	}
	go func() {
		defer close(r.done)
		r.repaint()
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				r.repaint()
			}
		}
	}()
}

// Finish stops repainting. On terminals it leaves the final table in place
// before later issue and snapshot lines are appended.
func (r *queryProgressReporter) Finish() {
	if r.progressSuppressed {
		return
	}
	if r.caps.Repaint {
		r.once.Do(func() { close(r.stop) })
		<-r.done
		r.repaint()
		return
	}
}

// WriteFinalTable writes the single redirected-stderr table after transition
// lines. Terminal output was already repainted by Finish.
func (r *queryProgressReporter) WriteFinalTable() {
	if !r.progressSuppressed && !r.caps.Repaint {
		_, _ = io.WriteString(r.stderr, r.table())
	}
}

func (r *queryProgressReporter) repaint() {
	text := r.table()
	if r.rows > 0 {
		_, _ = fmt.Fprintf(r.stderr, "\x1b[%dA\r\x1b[J", r.rows)
	}
	_, _ = io.WriteString(r.stderr, text)
	r.rows = len(r.members)
}

func (r *queryProgressReporter) table() string {
	return formatQueryMemberTable(r.style, r.views(), r.now(), r.caps.Color)
}

func (r *queryProgressReporter) views() []queryMemberView {
	views := make([]queryMemberView, len(r.members))
	for i := range r.members {
		views[i] = r.members[i].view()
	}
	return views
}

// WriteTransitions emits each recorded member phase once. It is intentionally
// deferred until completion for redirected stderr so fast streams cannot make
// output unbounded or race a slow writer.
func (r *queryProgressReporter) WriteTransitions() {
	if r.progressSuppressed || r.caps.Repaint {
		return
	}
	for i := range r.members {
		for _, phase := range r.members[i].phaseHistory() {
			queryWriteStderrf(r.stderr, "querying %s: %s", r.members[i].instance.Name, queryPhaseLabel(r.style, phase))
		}
	}
}

func (r *queryProgressReporter) WriteMemberIssues() {
	r.WriteMemberIssuesFor(r.members)
}

// WriteMemberIssuesFor writes diagnostics for the members that determine the
// final command outcome while the progress table still shows every candidate.
func (r *queryProgressReporter) WriteMemberIssuesFor(members []queryMember) {
	for _, member := range members {
		result := member.resultSnapshot()
		if result.err != nil {
			queryWriteStderrf(r.stderr, "%s: error: %v", member.instance.Name, result.err)
		}
		if len(result.warnings) > 0 {
			queryWriteStderrf(r.stderr, "%s: warning: %s", member.instance.Name, strings.Join(result.warnings, "; "))
		}
	}
}

func (r *queryProgressReporter) SnapshotOutcome(format string, args ...any) {
	queryWriteStderrf(r.stderr, format, args...)
}

// queryWriteStderrf emits one sanitized diagnostic row. Dynamic command input
// must pass through here so it cannot inject terminal controls or new rows.
func queryWriteStderrf(stderr io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintln(stderr, sanitizeStderrText(fmt.Sprintf(format, args...)))
}

// formatQueryMemberTable is the pure line/table formatter shared by terminal
// repaint and redirected diagnostics. Layout matches the picker: configured
// phase labels, grapheme-cell widths, a five-cell count, dynamic timer width,
// and two-space separators.
func formatQueryMemberTable(style config.Style, members []queryMemberView, now time.Time, color bool) string {
	labelWidth, nameWidth, timerWidth := queryMaxLabelWidth(style), 0, 0
	labels := make([]string, len(members))
	names := make([]string, len(members))
	timers := make([]string, len(members))
	for i, member := range members {
		labels[i] = queryPhaseLabel(style, member.Phase)
		names[i] = sanitizeStderrText(member.Name)
		nameWidth = max(nameWidth, uniseg.StringWidth(names[i]))
		timers[i] = formatQueryElapsed(style.ElapsedFetchTimeFormat, member, now)
		timerWidth = max(timerWidth, uniseg.StringWidth(timers[i]))
	}

	var out strings.Builder
	for i, member := range members {
		label := labels[i]
		paddedLabel := label + strings.Repeat(" ", max(labelWidth-uniseg.StringWidth(label), 0))
		if color && member.Phase.ColorForStyle(style).IsSet() {
			paddedLabel = ansi.Style{}.ForegroundColor(member.Phase.ColorForStyle(style).Color).Styled(paddedLabel)
		}
		out.WriteString(paddedLabel)
		out.WriteString("  ")
		out.WriteString(names[i])
		out.WriteString(strings.Repeat(" ", max(nameWidth-uniseg.StringWidth(names[i]), 0)))
		fmt.Fprintf(&out, "  %5d  ", member.Count)
		out.WriteString(strings.Repeat(" ", max(timerWidth-uniseg.StringWidth(timers[i]), 0)))
		out.WriteString(timers[i])
		out.WriteByte('\n')
	}
	return out.String()
}

func queryPhaseLabel(style config.Style, phase status.Phase) string {
	return sanitizeStderrText(phase.LabelForStyle(style))
}

func queryMaxLabelWidth(style config.Style) int {
	width := 0
	for _, phase := range []status.Phase{
		status.Disabled, status.Cancelled, status.Enabled, status.InProgress,
		status.Error, status.Warning, status.Success, status.AuthInProgress, status.Watching,
	} {
		width = max(width, uniseg.StringWidth(queryPhaseLabel(style, phase)))
	}
	return width
}

func formatQueryElapsed(format string, member queryMemberView, now time.Time) string {
	end := member.Updated
	if member.Live {
		end = now
	}
	if end.Before(member.Started) || member.Started.IsZero() {
		end = member.Started
	}
	return sanitizeStderrText(fmt.Sprintf(format, end.Sub(member.Started).Seconds()))
}
