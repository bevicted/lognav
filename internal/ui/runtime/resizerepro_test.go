package runtime

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// vtGrid is a minimal terminal-grid model that applies the subset of ANSI that
// uv's TerminalScreen emits in alt-screen (fullscreen) mode: absolute cursor
// moves (CUP/H), erase-display (ED/J), erase-line (EL/K), CR, LF, and printable
// runes. SGR, private modes (?...h/l), and cursor show/hide are ignored. It lets
// a test assert what is *physically on screen* after a byte stream, which is the
// only way to catch the "renderer thinks a cell is blank but it isn't" bug.
type vtGrid struct {
	w, h   int
	cells  [][]rune
	cx, cy int
}

func newVT(w, h int) *vtGrid {
	g := &vtGrid{w: w, h: h, cells: make([][]rune, h)}
	for y := range g.cells {
		g.cells[y] = make([]rune, w)
		for x := range g.cells[y] {
			g.cells[y][x] = ' '
		}
	}
	return g
}

func (g *vtGrid) fillFrom(x, y int) { // erase from (x,y) to end of screen
	for cx := x; cx < g.w; cx++ {
		g.cells[y][cx] = ' '
	}
	for cy := y + 1; cy < g.h; cy++ {
		for cx := range g.cells[cy] {
			g.cells[cy][cx] = ' '
		}
	}
}

func isCSIFinal(c rune) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func vtAtoi(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, d := range s {
		if d < '0' || d > '9' {
			return def
		}
		n = n*10 + int(d-'0')
	}
	return n
}

func (g *vtGrid) apply(s string) {
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		switch c := r[i]; {
		case c == '\x1b' && i+1 < len(r) && r[i+1] == '[':
			i = g.applyCSI(r, i+2)
		case c == '\r':
			g.cx = 0
		case c == '\n':
			if g.cy < g.h-1 {
				g.cy++
			}
		case c >= 0x20:
			g.put(c)
		}
	}
}

func (g *vtGrid) put(c rune) {
	if g.cy >= 0 && g.cy < g.h && g.cx >= 0 && g.cx < g.w {
		g.cells[g.cy][g.cx] = c
	}
	g.cx++
}

// applyCSI consumes one CSI sequence whose parameters start at index `start`
// and returns the index of its final byte (so the apply loop's i++ advances past
// it). Out-of-range returns len(r)-1.
func (g *vtGrid) applyCSI(r []rune, start int) int {
	i := start
	for i < len(r) && !isCSIFinal(r[i]) {
		i++
	}
	if i >= len(r) {
		return len(r) - 1
	}
	g.csi(string(r[start:i]), r[i])
	return i
}

func (g *vtGrid) csi(params string, final rune) {
	if strings.HasPrefix(params, "?") {
		return // private mode (e.g. ?25l, ?1049h) — ignore
	}
	switch final {
	case 'H', 'f':
		g.moveCursor(params)
	case 'J':
		g.eraseDisplay(vtAtoi(params, 0))
	case 'K':
		g.eraseLine(vtAtoi(params, 0))
	}
}

func (g *vtGrid) moveCursor(params string) { // 1-based; default 1;1
	col := 1
	parts := strings.Split(params, ";")
	row := vtAtoi(parts[0], 1)
	if len(parts) >= 2 {
		col = vtAtoi(parts[1], 1)
	}
	g.cy, g.cx = row-1, col-1
}

func (g *vtGrid) eraseDisplay(mode int) {
	switch mode {
	case 0:
		g.fillFrom(g.cx, g.cy)
	case 2:
		for y := range g.cells {
			for x := range g.cells[y] {
				g.cells[y][x] = ' '
			}
		}
	}
}

func (g *vtGrid) eraseLine(mode int) {
	switch mode {
	case 0:
		for x := g.cx; x < g.w; x++ {
			g.cells[g.cy][x] = ' '
		}
	case 1:
		for x := 0; x <= g.cx && x < g.w; x++ {
			g.cells[g.cy][x] = ' '
		}
	case 2:
		for x := range g.cells[g.cy] {
			g.cells[g.cy][x] = ' '
		}
	}
}

func (g *vtGrid) region(x0, y0, x1, y1 int) string {
	var b strings.Builder
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			b.WriteRune(g.cells[y][x])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// frame fills a w*h compose buffer: [0,split) = left, [split,w) = right.
func frame(w, h, split int, left, right string) uv.ScreenBuffer {
	buf := uv.NewScreenBuffer(w, h)
	buf.Method = ansi.GraphemeWidth
	for y := range h {
		for x := range w {
			g := left
			if x >= split {
				g = right
			}
			buf.SetCell(x, y, uv.NewCell(ansi.GraphemeWidth, g))
		}
	}
	return buf
}

func render(scr *uv.TerminalScreen, buf uv.ScreenBuffer) {
	_ = scr.Display(buf)
	_ = scr.Render()
	_ = scr.Flush()
}

const (
	vtH  = 6
	vtW1 = 10
	vtW2 = 20
)

// runResizeScenario drives the screen through:
//
//	draw tabA @ narrow -> GROW + draw tabA @ wide (reclear=fix) -> draw tabB
//
// where tabB has content only in [0,vtW1) and blanks in [vtW1,vtW2). It returns
// the physical grid. With reclear=false this mirrors the naive runtime (one
// draw per resize) and reproduces the stale-region bug; with reclear=true it
// mirrors the workaround (re-arm a full clear once curbuf is correctly sized).
func runResizeScenario(t *testing.T, reclear bool) *vtGrid {
	t.Helper()
	g := newVT(vtW2, vtH)
	var sink strings.Builder
	scr := uv.NewTerminalScreen(&sink, nil)
	_ = scr.EnterAltScreen()
	_ = scr.Flush()
	g.apply(sink.String())

	tap := func() { g.apply(sink.String()); sink.Reset() }

	// Narrow tabA.
	sink.Reset()
	_ = scr.Resize(vtW1, vtH)
	render(scr, frame(vtW1, vtH, vtW1, "Q", "Q"))
	tap()

	// Grow; redraw tabA across the full new width.
	_ = scr.Resize(vtW2, vtH)
	render(scr, frame(vtW2, vtH, vtW2, "Q", "Q"))
	if reclear {
		// Re-arm a full clear now that the renderer's curbuf has been resized to
		// the new width by the draw above, then repaint so curbuf matches the
		// physical screen.
		_ = scr.Resize(vtW2, vtH)
		render(scr, frame(vtW2, vtH, vtW2, "Q", "Q"))
	}
	tap()

	// Switch to tabB: 'I' in [0,vtW1), blank in [vtW1,vtW2).
	render(scr, frame(vtW2, vtH, vtW1, "I", " "))
	tap()
	return g
}

// TestResize_NaiveGrow_LeavesStaleRegion documents the uv root cause: one draw
// per resize leaves the newly-exposed columns stale after a later content change.
func TestResize_NaiveGrow_LeavesStaleRegion(t *testing.T) {
	t.Parallel()
	g := runResizeScenario(t, false)
	right := g.region(vtW1, 0, vtW2, vtH)
	if !strings.Contains(right, "Q") {
		t.Fatalf("expected stale 'Q' in the grown region to document the bug, got:\n%s", right)
	}
	t.Logf("naive grow leaves stale right region:\n%s", right)
}

// TestResize_ReclearGrow_NoStaleRegion verifies the workaround: re-arming a full
// clear after the first post-grow draw leaves no stale content.
func TestResize_ReclearGrow_NoStaleRegion(t *testing.T) {
	t.Parallel()
	g := runResizeScenario(t, true)
	right := g.region(vtW1, 0, vtW2, vtH)
	if strings.Contains(right, "Q") {
		t.Fatalf("STALE: grown region still shows previous frame after reclear:\n%s", right)
	}
	// Left region must show the new tab.
	left := g.region(0, 0, vtW1, vtH)
	if !strings.Contains(left, "I") {
		t.Fatalf("left region not repainted to new tab:\n%s", left)
	}
}
