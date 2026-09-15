package logviewer

import (
	"iter"
	"slices"
	"strings"
)

// The cursor primitives return their settled log index for callers that need
// it, while the contextual row reads the cursor directly from this model.

// logsCursor at y is log's logLine.
type logsCursor struct {
	// x and y are the indices of the lines the cursor is on on-screen
	// e.g.: x=1, y=4 => cursor is on the 2nd column, 5th row

	// horizontal pos, width based
	x int
	// vertical pos, height based
	y int

	// index of the log the cursor is on
	// e.g.: 4 => 5th log
	log int
	// index of the line of the log the cursor is on
	// e.g.: 4 => 5th line of the log
	logLine int
}

// CursorUp moves the cursor up by n lines and returns the settled log index.
func (m *Model) CursorUp(n int) int {
	m.cursor.y -= n

	next, stop := iter.Pull(m.IterLogs(m.cursor.log-1, -1))
	defer stop()

	for n > 0 {
		leftOverInEntry := m.cursor.logLine

		// just need to move inside the current log
		if leftOverInEntry >= n {
			m.cursor.logLine -= n
			break
		}

		nextEntryIndex, ok := next()
		// No more logs, meaning we are at the first log
		if !ok {
			m.cursor.logLine = 0
			break
		}

		entry := m.getRenderCacheEntry(nextEntryIndex)

		n -= leftOverInEntry + 1
		m.cursor.logLine = len(entry) - 1
		m.cursor.log = nextEntryIndex
	}

	m.updateDisplay()
	return m.cursor.log
}

// CursorDown moves the cursor down by n lines and returns the settled log
// index.
func (m *Model) CursorDown(n int) int {
	m.cursor.y += n

	next, stop := iter.Pull(m.IterLogs(m.cursor.log+1, 1))
	defer stop()

	currentEntry := m.getRenderCacheEntry(m.cursor.log)

	for n > 0 {
		leftOverInEntry := len(currentEntry) - m.cursor.logLine - 1

		// just need to move inside the current log
		if leftOverInEntry >= n {
			m.cursor.logLine += n
			break
		}

		nextEntryIndex, ok := next()
		// No more logs, meaning we are at the last log
		if !ok {
			m.cursor.logLine = len(currentEntry) - 1
			break
		}

		n -= leftOverInEntry + 1
		m.cursor.logLine = 0
		m.cursor.log = nextEntryIndex

		currentEntry = m.getRenderCacheEntry(nextEntryIndex)
	}

	m.updateDisplay()
	return m.cursor.log
}

// CursorToTop moves the cursor to the first log and returns the settled log
// index.
func (m *Model) CursorToTop() int {
	m.cursor.log = 0
	m.cursor.logLine = 0
	m.cursor.y = 0

	m.updateDisplay()
	return m.cursor.log
}

// CursorToBottom moves the cursor to the last log and returns the settled log
// index.
func (m *Model) CursorToBottom() int {
	last := m.store.GetLogCount() - 1
	entry := m.getRenderCacheEntry(last)

	m.cursor.log = last
	m.cursor.logLine = len(entry) - 1

	_, logsR, _ := m.subRects()
	h := logsR.H
	m.cursor.y = h - 1

	m.updateDisplay()
	return m.cursor.log
}

// ScrollViewport pans the log viewport by n display rows WITHOUT recentering,
// keeping the cursor on its logical line (cursor.log/cursor.logLine) until the
// pan would push that line past the scrolloff margin, after which the cursor is
// dragged onto the line now sitting at the margin (vim <C-e>/<C-y> drag-at-edge).
// n > 0 scrolls toward later logs (content moves up), n < 0 toward earlier logs.
// Because the viewport is anchored solely by cursor.y, a pure pan that leaves the
// cursor visible is just a cursor.y shift (no logical move); only the
// drag/clamp phases change the logical cursor.
func (m *Model) ScrollViewport(n int) {
	if m.store == nil || m.store.GetLogCount() < 1 || n == 0 {
		return
	}
	_, logsR, _ := m.subRects()
	h := logsR.H
	if h < 1 {
		return
	}
	scrollOff := min(max(int(m.bundle.Config.Logs.Scrolloff), 0), max(0, (h-1)/2))
	if m.clampViewportPan(n, h) {
		return
	}

	if n > 0 { // scroll toward later logs: cursor rides toward the top margin
		if targetY := m.cursor.y - n; targetY >= scrollOff {
			m.cursor.y = targetY // phase 1: logical line fixed
			m.updateDisplay()
			return
		}
		// phase 2: the line that lands on the top margin after the pan is the one
		// currently `scrollOff+n` rows down (content shifts up by n).
		if logIdx, logLine, ok := m.logIdxAtRow(scrollOff + n); ok {
			m.cursor.log, m.cursor.logLine, m.cursor.y = logIdx, logLine, scrollOff
			m.updateDisplay()
		}
		return
	}

	// n < 0: scroll toward earlier logs: cursor rides toward the bottom margin
	k := -n
	bottomMargin := h - 1 - scrollOff
	if targetY := m.cursor.y + k; targetY <= bottomMargin {
		m.cursor.y = targetY // phase 1: logical line fixed
		m.updateDisplay()
		return
	}
	// phase 2: the line that lands on the bottom margin is the one currently
	// `bottomMargin-k` rows up (content shifts down by k).
	if logIdx, logLine, ok := m.logIdxAtRow(bottomMargin - k); ok {
		m.cursor.log, m.cursor.logLine, m.cursor.y = logIdx, logLine, bottomMargin
		m.updateDisplay()
	}
}

// clampViewportPan handles vertical pans that would cross a content boundary.
// If an end is already visible, the pan is ignored so repeated touchpad events
// cannot expose empty rows. Larger pans are clamped to the first or last line.
func (m *Model) clampViewportPan(n, h int) bool {
	if n > 0 {
		if _, _, ok := m.logIdxAtVirtualRow(h); !ok {
			return true
		}
		if _, _, ok := m.logIdxAtVirtualRow(h - 1 + n); !ok {
			m.CursorToBottom()
			return true
		}
		return false
	}

	if _, _, ok := m.logIdxAtVirtualRow(-1); !ok {
		return true
	}
	if _, _, ok := m.logIdxAtVirtualRow(n); !ok {
		m.CursorToTop()
		return true
	}
	return false
}

func (m *Model) ScrollHorizontally(n int) {
	m.xOffset += n
	m.updateDisplay()
	m.logger.Debug("horizontal scroll", "offset", m.xOffset)
}

func (m *Model) moveToFirstChar() {
	m.xOffset = 0
	m.cursor.x = 0
	l := m.GetCurrentLine()
	m.ScrollHorizontally(len(l) - len(strings.TrimLeft(l, " ")))
}

// centerOnMatch centers the match at search[logIdx][matchIdx], updates the
// 1-based currentMatch counter the contextual row shows, and returns the
// (logIdx, true) pair used by CenterPrevMatch and CenterNextMatch.
func (m *Model) centerOnMatch(logIdx, matchIdx int, match SearchMatch) (int, bool) {
	m.Center(logIdx, match.line, match.start)
	m.currentMatch = m.store.state.search.MatchIndex(logIdx, matchIdx, m.store.state.filtered) + 1
	return logIdx, true
}

// CenterPrevMatch centers the previous search match (or marked log) and returns
// its log index with ok=true. When no prior match exists, it returns (0, false).
func (m *Model) CenterPrevMatch() (int, bool) {
	if m.store.GetLogCount() < 1 {
		return 0, false
	}

	_, logsR, _ := m.subRects()
	w := logsR.W
	for logIdx := range m.IterLogs(m.cursor.log, -1) {
		isNotUnderCursor := logIdx != m.cursor.log
		if m.IsMarked(logIdx) && isNotUnderCursor {
			m.Center(logIdx, 0, 0)
			return m.cursor.log, true
		}

		for matchIdx, match := range slices.Backward(m.store.state.search[logIdx]) {
			if isNotUnderCursor || match.line < m.cursor.logLine {
				return m.centerOnMatch(logIdx, matchIdx, match)
			}

			// same target but it's either a later match or the screen can't scroll further to the left
			if match.line > m.cursor.logLine || m.xOffset < 1 {
				continue
			}

			center := m.xOffset + (w >> 1)
			if match.start < center {
				return m.centerOnMatch(logIdx, matchIdx, match)
			}
		}
	}

	return 0, false
}

// CenterNextMatch centers the next search match (or marked log) and returns its
// log index with ok=true. When no further match exists, it returns (0, false).
func (m *Model) CenterNextMatch() (int, bool) {
	if m.store.GetLogCount() < 1 {
		return 0, false
	}

	_, logsR, _ := m.subRects()
	w := logsR.W
	for logIdx := range m.IterLogs(m.cursor.log, 1) {
		isNotUnderCursor := logIdx != m.cursor.log
		if m.IsMarked(logIdx) && isNotUnderCursor {
			m.Center(logIdx, 0, 0)
			return m.cursor.log, true
		}

		for matchIdx, match := range m.store.state.search[logIdx] {
			if isNotUnderCursor || match.line > m.cursor.logLine {
				return m.centerOnMatch(logIdx, matchIdx, match)
			}

			// same target but it's a previous match
			if match.line < m.cursor.logLine {
				continue
			}

			center := m.xOffset + (w >> 1)
			if match.start > center {
				return m.centerOnMatch(logIdx, matchIdx, match)
			}
		}
	}

	return 0, false
}

func (m *Model) Center(logIdx, logLine, horizontal int) {
	_, logsR, _ := m.subRects()
	w, h := logsR.W, logsR.H

	m.cursor.log = logIdx
	m.cursor.logLine = logLine
	m.cursor.x = horizontal
	m.xOffset = horizontal - (w >> 1)
	m.cursor.y = h >> 1
	m.updateDisplay()
}

// moveCursorOutOfFilter moves the cursor off a now-filtered log and returns the
// settled log index. It calls CursorDown/CursorUp only for their cursor
// side-effects and discards their intermediate return values.
func (m *Model) moveCursorOutOfFilter() int {
	if !m.store.state.filtered[m.cursor.log] {
		return m.cursor.log
	}

	logBefore := m.cursor.log
	lines := m.getRenderCacheEntry(m.cursor.log)
	// Only the final cursor position is returned.
	m.CursorDown(len(lines) - m.cursor.logLine)
	if m.cursor.log == logBefore {
		m.CursorUp(m.cursor.logLine + 1)
	}
	return m.cursor.log
}
