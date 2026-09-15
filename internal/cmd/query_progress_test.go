package cmd

import (
	"bytes"
	"context"
	"errors"
	"image/color"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/status"
)

func TestFormatQueryMemberTable_UsesConfiguredGraphemeLayoutAndElapsed(t *testing.T) {
	t.Parallel()
	cfg := queryTestConfig()
	cfg.Style.AuthInProgressLabel = "AUTH"
	cfg.Style.InProgressLabel = "FETCH"
	cfg.Style.SuccessLabel = "DONE"
	cfg.Style.ElapsedFetchTimeFormat = "%.1fs"
	now := time.Unix(100, 0)
	got := formatQueryMemberTable(cfg.Style, []queryMemberView{
		{Name: "short", Phase: status.AuthInProgress, Count: 7, Started: now.Add(-1500 * time.Millisecond), Live: true},
		{Name: "wide-\U0001f469\u200d\U0001f4bb-name", Phase: status.Success, Count: 123456, Started: now.Add(-2 * time.Second), Updated: now.Add(-500 * time.Millisecond)},
	}, now, false)

	assert.NotContains(t, got, "\x1b")
	assert.Equal(t, "AUTH    short             7  1.5s\nDONE    wide-\U0001f469\u200d\U0001f4bb-name  123456  1.5s\n", got)
	cfg.Style.SuccessColor = config.ConfigColor{Color: color.RGBA{R: 1, G: 2, B: 3, A: 255}}
	assert.Contains(t, formatQueryMemberTable(cfg.Style, []queryMemberView{{Name: "wide-\U0001f469\u200d\U0001f4bb-name", Phase: status.Success, Started: now, Updated: now}}, now, true), "\x1b[38;2;1;2;3mDONE")
}

func TestQueryProgressReporter_RepaintsTerminalScreenInPlace(t *testing.T) {
	t.Parallel()
	cfg := queryTestConfig()
	cfg.Core.RedrawIntervalMs = 1 // clamped to 8ms
	cfg.Style.ElapsedFetchTimeFormat = "%.0fs"
	first := newQueryMember(cfg.ICL.Instances[0], time.Unix(10, 0))
	secondInstance := cfg.ICL.Instances[0]
	secondInstance.Name = "wide-\U0001f469\u200d\U0001f4bb"
	second := newQueryMember(secondInstance, time.Unix(10, 0))
	var now atomic.Int64
	now.Store(time.Unix(11, 0).UnixNano())
	var out progressBuffer
	reporter := newQueryProgressReporter(&out, cfg, []queryMember{first, second}, false)
	reporter.caps = queryTerminalCapabilities{Repaint: true, Color: true}
	reporter.now = func() time.Time { return time.Unix(0, now.Load()) }
	reporter.Start()
	require.Eventually(t, func() bool {
		rows := progressScreenRows(out.String())
		return len(rows) == 2 && strings.Contains(strings.Join(rows, "\n"), "AUTH") && strings.Contains(strings.Join(rows, "\n"), "1s")
	}, time.Second, time.Millisecond)

	now.Store(time.Unix(12, 0).UnixNano())
	require.Eventually(t, func() bool {
		return strings.Contains(strings.Join(progressScreenRows(out.String()), "\n"), "2s")
	}, time.Second, time.Millisecond)

	reporter.members[0].mu.Lock()
	reporter.members[0].phase = status.InProgress
	reporter.members[0].started = time.Unix(12, 0)
	reporter.members[0].updated = time.Unix(12, 0)
	reporter.members[0].history = append(reporter.members[0].history, status.InProgress)
	reporter.members[0].mu.Unlock()
	data := `{"message":"one"}`
	reporter.members[0].OnData(&icl.StreamItem{Result: &icl.DataprimeResult{Results: []icl.DataprimeResults{{UserData: &data}}}})
	now.Store(time.Unix(13, 0).UnixNano())
	require.Eventually(t, func() bool {
		rows := progressScreenRows(out.String())
		return len(rows) == 2 && strings.Contains(strings.Join(rows, "\n"), "FETCHING") && strings.Contains(strings.Join(rows, "\n"), "    1")
	}, time.Second, time.Millisecond)

	reporter.members[0].mu.Lock()
	reporter.members[0].phase = status.Success
	reporter.members[0].updated = time.Unix(13, 0)
	reporter.members[0].history = append(reporter.members[0].history, status.Success)
	reporter.members[0].mu.Unlock()
	reporter.Finish()

	got := out.String()
	rows := progressScreenRows(got)
	assert.Contains(t, got, "\x1b[2A\r\x1b[J", "later ticks move to the first table row and erase the prior two-row table")
	assert.Len(t, rows, 2, "the virtual screen height must not accumulate a table per repaint")
	assert.Contains(t, strings.Join(rows, "\n"), "DONE", "the final repaint replaces the prior state")
	assert.Contains(t, strings.Join(rows, "\n"), "    1", "the final repaint retains the advanced count")
	assert.Contains(t, strings.Join(rows, "\n"), "1s", "the terminal timer advances and freezes at completion")
	assert.NotContains(t, got, "\a")
	assert.NotContains(t, got, "\x1b]")
}

// progressScreen applies the terminal operations emitted by the reporter. It
// models cursor position, CR, CSI cursor-up, and erase-display rather than
// treating the final write payload as the final screen.
type progressScreen struct {
	rows        [][]rune
	row, column int
}

func progressScreenRows(output string) []string {
	var screen progressScreen
	for i := 0; i < len(output); {
		switch output[i] {
		case '\r':
			screen.column = 0
			i++
		case '\n':
			screen.row++
			screen.column = 0 // TTY output uses ONLCR for line-oriented stderr.
			i++
		case '\x1b':
			if i+1 < len(output) && output[i+1] == '[' {
				end := i + 2
				for end < len(output) && (output[end] < 0x40 || output[end] > 0x7e) {
					end++
				}
				if end < len(output) {
					screen.csi(output[i+2:end], output[end])
					i = end + 1
					continue
				}
			}
			i++
		default:
			r, size := utf8.DecodeRuneInString(output[i:])
			if r >= ' ' && r != 0x7f {
				screen.put(r)
			}
			i += size
		}
	}
	return screen.textRows()
}

func (s *progressScreen) csi(params string, final byte) {
	count := 1
	if params != "" {
		if parsed, err := strconv.Atoi(strings.Split(params, ";")[0]); err == nil {
			count = parsed
		}
	}
	switch final {
	case 'A':
		s.row = max(s.row-count, 0)
	case 'J':
		if len(s.rows) > s.row {
			s.rows[s.row] = s.rows[s.row][:min(s.column, len(s.rows[s.row]))]
		}
		s.rows = s.rows[:min(s.row+1, len(s.rows))]
	}
}

func (s *progressScreen) put(r rune) {
	for len(s.rows) <= s.row {
		s.rows = append(s.rows, nil)
	}
	for len(s.rows[s.row]) < s.column {
		s.rows[s.row] = append(s.rows[s.row], ' ')
	}
	if s.column == len(s.rows[s.row]) {
		s.rows[s.row] = append(s.rows[s.row], r)
	} else {
		s.rows[s.row][s.column] = r
	}
	s.column++
}

func (s *progressScreen) textRows() []string {
	rows := make([]string, len(s.rows))
	for i, row := range s.rows {
		rows[i] = strings.TrimRight(string(row), " ")
	}
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	return rows
}

type progressBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *progressBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *progressBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestQueryProgressReporter_SanitizesDynamicText(t *testing.T) {
	t.Parallel()
	cfg := queryTestConfig()
	cfg.Style.AuthInProgressLabel = "\x1b[31mAU\nTH\a\x1b[0m"
	cfg.Style.InProgressLabel = "\x1b[2JFETCH\rING"
	cfg.Style.ErrorLabel = "ERR\u009bOR"
	cfg.Style.ElapsedFetchTimeFormat = "\x1b[31m%.1fs\n\a\x1b[0m"
	now := time.Unix(100, 0)
	first := newQueryMember(config.ICLInstanceConfig{Name: "first\n\x1b[2J\a"}, now.Add(-time.Second))
	first.mu.Lock()
	first.phase = status.Error
	first.updated = now
	first.history = []status.Phase{status.AuthInProgress, status.InProgress, status.Error}
	first.mu.Unlock()
	first.result.failed(errors.New("bad\n\x1b[2Jerror\a"), queryFailureGeneral)
	first.result.mu.Lock()
	first.result.warnings = []string{"warn\n\x1b[31mone\a", "warn\r\u0085two"}
	first.result.mu.Unlock()
	second := newQueryMember(config.ICLInstanceConfig{Name: "wide-\U0001f469\u200d\U0001f4bb\nsecond\u0085"}, now)
	members := []queryMember{first, second}

	table := formatQueryMemberTable(cfg.Style, []queryMemberView{first.view(), second.view()}, now, false)
	assert.Equal(t, 2, strings.Count(table, "\n"), "sanitized names, labels, and elapsed text keep one table row per member")
	assertPlainStderr(t, table)
	assert.Contains(t, table, "wide-\U0001f469\u200d\U0001f4bb second", "printable wide graphemes survive sanitization")

	var redirected progressBuffer
	plain := newQueryProgressReporter(&redirected, cfg, members, false)
	plain.caps = queryTerminalCapabilities{}
	plain.now = func() time.Time { return now }
	plain.WriteTransitions()
	plain.WriteFinalTable()
	plain.WriteMemberIssues()
	plain.SnapshotOutcome("0 logs; no snapshot created")
	got := redirected.String()
	assertPlainStderr(t, got)
	assert.Equal(t, 9, strings.Count(got, "\n"), "four transitions, two final member rows, two issue rows, and one outcome row")
	assert.Contains(t, got, "wide-\U0001f469\u200d\U0001f4bb second")

	var terminal progressBuffer
	tty := newQueryProgressReporter(&terminal, cfg, members, false)
	tty.caps = queryTerminalCapabilities{Repaint: true, Color: true}
	tty.now = func() time.Time { return now }
	tty.repaint()
	rows := progressScreenRows(terminal.String())
	assert.Len(t, rows, 2, "TTY repaint also has exactly one row per member")
	assertPlainStderr(t, strings.Join(rows, "\n"))
	assert.Contains(t, strings.Join(rows, "\n"), "wide-\U0001f469\u200d\U0001f4bb second")
	tty.WriteMemberIssues()
	tty.SnapshotOutcome("0 logs; no snapshot created")
	terminalText := terminal.String()
	assert.NotContains(t, terminalText, "\x1b[2J", "configured text cannot add a terminal erase command")
	plainTerminalText := ansi.Strip(terminalText)
	assertPlainStderr(t, plainTerminalText)
	assert.Equal(t, 5, strings.Count(plainTerminalText, "\n"), "two table rows, two issue rows, and one outcome row")
}

func assertPlainStderr(t *testing.T, text string) {
	t.Helper()
	assert.NotContains(t, text, "\x1b")
	for _, r := range text {
		if r != '\n' {
			assert.Falsef(t, unicode.IsControl(r), "unexpected control %U in %q", r, text)
		}
	}
}

func TestQueryProgressReporter_BoundsRepeatedStreamErrors(t *testing.T) {
	t.Parallel()
	cfg := queryTestConfig()
	member := newQueryMember(cfg.ICL.Instances[0], time.Unix(10, 0))
	member.startQuery()

	message := "stream failure"
	item := &icl.StreamItem{Error: &icl.DataprimeError{Message: &message}}
	for range 100_000 {
		member.OnData(item)
	}
	member.OnError(context.DeadlineExceeded)
	for i := range 32 {
		member.OnError(errors.New("distinct failure " + strconv.Itoa(i)))
	}
	member.OnClose()

	result := member.resultSnapshot()
	require.Error(t, result.err)
	assert.Equal(t, queryFailureData, result.failure, "an explicit Dataprime error keeps the data-rejection exit classification")
	assert.Contains(t, result.err.Error(), "stream failure (repeated 100000 times)")
	assert.Contains(t, result.err.Error(), context.DeadlineExceeded.Error())
	assert.Contains(t, result.err.Error(), "26 additional error occurrences omitted")
	assert.Less(t, len(result.err.Error()), queryErrorMaxDistinct*(queryErrorMaxMessageBytes+100))
	_, recursive := result.err.(interface{ Unwrap() []error })
	assert.False(t, recursive, "the member summary must not retain an errors.Join tree")

	var out progressBuffer
	reporter := newQueryProgressReporter(&out, cfg, []queryMember{member}, false)
	reporter.WriteMemberIssues()
	assert.Contains(t, out.String(), "stream failure (repeated 100000 times)")
	assert.Less(t, len(out.String()), queryErrorMaxDistinct*(queryErrorMaxMessageBytes+100))

	queryErr := queryMembersError([]queryMember{member})
	require.Error(t, queryErr)
	assert.Equal(t, ExitData, ExitCode(queryErr))
	assert.Less(t, len(queryErr.Error()), queryErrorMaxDistinct*(queryErrorMaxMessageBytes+100))
}

func TestQueryProgressReporter_RedirectedOutputIsFiniteAndPlain(t *testing.T) {
	t.Parallel()
	cfg := queryTestConfig()
	cfg.Style.ElapsedFetchTimeFormat = "%.2fs"
	member := newQueryMember(cfg.ICL.Instances[0], time.Unix(10, 0))
	member.startQuery()
	member.OnError(errors.New("bad stream"))
	var out progressBuffer
	reporter := newQueryProgressReporter(&out, cfg, []queryMember{member}, false)
	reporter.caps = queryTerminalCapabilities{}
	reporter.Finish()
	reporter.WriteTransitions()
	reporter.WriteFinalTable()
	reporter.WriteMemberIssues()
	reporter.SnapshotOutcome("0 logs; no snapshot created")

	got := out.String()
	assertPlainStderr(t, got)
	assert.Equal(t, 1, strings.Count(got, "ERROR     test"), "exactly one final aligned table")
	assert.Contains(t, got, "querying test: AUTH")
	assert.Contains(t, got, "querying test: FETCHING")
	assert.Contains(t, got, "querying test: ERROR")
	assert.Contains(t, got, "test: error: bad stream")
	assert.True(t, strings.HasSuffix(got, "0 logs; no snapshot created\n"))
}
