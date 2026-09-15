package list

import (
	"cmp"
	"fmt"
	"image/color"
	"log/slog"
	"slices"
	"strings"

	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/logging"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	uiinput "github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/numeric"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/lithammer/fuzzysearch/fuzzy"
	"github.com/rivo/uniseg"
)

// Segment is a styled text span within a list item.
type Segment struct {
	Text  string
	Style uv.Style
}

// PlainItem returns a single unstyled segment for plain text items.
func PlainItem(text string) []Segment {
	return []Segment{{Text: text}}
}

// ItemText concatenates all segment text values into a plain string.
func ItemText(item []Segment) string {
	if len(item) == 1 {
		return item[0].Text
	}
	var sb strings.Builder
	for _, s := range item {
		sb.WriteString(s.Text)
	}
	return sb.String()
}

type Model struct {
	bundle              deps.Bundle
	drawRect            component.Rect
	logger              *slog.Logger
	kh                  *keyhandler
	items               [][]Segment
	itemTexts           []string
	fuzziedItems        fuzzy.Ranks
	cursor              int
	yOffset             int
	hoverRow            int // display index of the row under the mouse pointer, -1 = none
	showItemIdx         bool
	doubleClickActivate bool
	input               *uiinput.Input
	onFuzzyConfirm      keys.Action
	onActivate          keys.Action
	poster              msgs.Poster
}

// Compile-time seam opt-ins: the authoritative list of the optional component
// seams *Model implements (signature drift fails the build). Every implemented
// seam belongs here — a seam rename or signature change would otherwise
// silently demote its handler to an ordinary method and drop that input with no
// build error. DoubleClickTarget is implemented unconditionally; whether a
// double-click activates is a per-list opt-in (WithDoubleClickActivate).
var (
	_ component.KeyTarget         = (*Model)(nil)
	_ component.MouseTarget       = (*Model)(nil)
	_ component.DoubleClickTarget = (*Model)(nil)
	_ component.MousePasteTarget  = (*Model)(nil)
	_ component.ScrollTarget      = (*Model)(nil)
	_ component.MouseHoverTarget  = (*Model)(nil)
	_ component.PasteTarget       = (*Model)(nil)
)

// builder

func New(bundle deps.Bundle) *Model {
	input := uiinput.New()
	input.SetPlaceholder("fuzzyfind")
	input.SetPrompt("> ")

	m := &Model{
		bundle:   bundle,
		logger:   slog.Default().With(logging.KeyComponent, "list"),
		input:    input,
		hoverRow: -1,
	}
	bindKeyhandlersToModel(m)
	return m
}

func (m *Model) WithItems(items [][]Segment) *Model {
	m.items = items
	m.itemTexts = make([]string, len(items))
	for i, item := range items {
		m.itemTexts[i] = ItemText(item)
	}
	m.input.SetValue("")
	m.cursor = numeric.Clamp(m.cursor, 0, len(items)-1)
	return m
}

func (m *Model) WithFuzzyConfirmAction(a keys.Action) *Model {
	m.onFuzzyConfirm = a
	return m
}

// WithActivate registers the action run when the user clicks the already-
// selected row (the click analogue of pressing Enter on the cursor row). The
// action runs on the loop goroutine (same as the Enter keybind action); if it
// needs to post an event it must do so off-loop via poster.Go — a bare on-loop
// PostCritical would deadlock the unbuffered events channel mid-dispatch.
func (m *Model) WithActivate(a keys.Action) *Model {
	m.onActivate = a
	return m
}

// WithDoubleClickActivate switches the list to double-click-to-activate mode:
// a single left-click only moves the cursor (select only), and activation
// requires OnMouseDoubleClick (two clicks within Core.DoubleClickMs on the same
// cell). Use this for list instances embedded in tab content (e.g. instancepicker,
// queryeditor) that participate in the root double-click routing; leave it unset
// for the help overlay and other callers that retain the original single-click-
// activates two-state behavior.
func (m *Model) WithDoubleClickActivate() *Model {
	m.doubleClickActivate = true
	return m
}

func (m *Model) ShowIndex(b bool) *Model {
	m.showItemIdx = b
	return m
}

// SetPoster injects the runtime poster used to post events off-loop (R5a).
func (m *Model) SetPoster(p msgs.Poster) {
	m.poster = p
}

// requestQuit requests a clean exit. It is the quit hook wired into the Quit
// keybind; keybind actions run on the loop goroutine, hence msgs.RequestQuit.
// It stays a method because the binding needs a func value.
func (m *Model) requestQuit() {
	msgs.RequestQuit(m.poster)
}

// Component

func (m *Model) Init() {}

// OnPaste inserts bracketed-paste content into the fuzzy-filter input and
// re-filters. PasteStart/PasteEnd carry no content and are ignored. Callers
// (filehandler, instancepicker) forward paste here directly.
func (m *Model) OnPaste(ev uv.Event) {
	if p, ok := ev.(uv.PasteEvent); ok {
		m.input.InsertText(p.Content)
		m.refilter()
	}
}

// HandleKey dispatches a key event using the list's three-group precedence:
// alwaysHandle → (when focused) input group then fuzzy-input feed → regular.
// Returns KeyHandled when an action ran or the focused input consumed the key;
// KeyIgnored on fall-through.
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if m.kh.alwaysHandle.Run(ev) {
		return component.KeyHandled
	}

	if m.input.Focused() {
		if m.kh.ti.Run(ev) {
			return component.KeyHandled
		}

		if m.input.HandleKey(ev) {
			m.refilter()
		}
		return component.KeyHandled
	}

	if m.kh.regular.Run(ev) {
		return component.KeyHandled
	}

	return component.KeyIgnored
}

// Component

func (m *Model) GetKeybinds() []keys.Binding {
	return append(
		m.kh.alwaysHandle.GetKeybinds(),
		append(m.kh.regular.GetKeybinds(), m.kh.ti.GetKeybinds()...)...,
	)
}

func (m *Model) SetRect(r component.Rect) {
	m.drawRect = r
}

func (m *Model) Draw(s component.Screen) *component.Cursor {
	tiRect, listRect := m.subRects()

	if tiRect.W >= 1 && tiRect.H >= 1 {
		m.input.SetWidth(max(0, tiRect.W-m.input.PromptWidth()-1))
		m.input.Draw(s, component.Rect{X: tiRect.X, Y: tiRect.Y, W: tiRect.W, H: 1})
		uicanvas.DrawBox(s, tiRect, uicanvas.BorderSet{Bottom: "─"}, uv.Style{})
	}

	var items [][]Segment
	if m.fuzziedItems == nil {
		items = m.items
	} else {
		items = make([][]Segment, 0, len(m.fuzziedItems))
		for _, rank := range m.fuzziedItems {
			items = append(items, m.items[rank.OriginalIndex])
		}
	}
	m.placeItems(s, listRect, items)

	return nil // cursor is uiinput's reverse-video cell
}

// custom

// subRects splits drawRect into the input area (top 2 rows) and the items
// area (the rest). When H <= 2 the items area collapses to height 0.
func (m *Model) subRects() (tiRect, listRect component.Rect) {
	r := m.drawRect
	if r.H <= 2 {
		return r, component.Rect{X: r.X, Y: r.Y + r.H, W: r.W, H: 0}
	}
	tiRect = component.Rect{X: r.X, Y: r.Y, W: r.W, H: 2}
	listRect = component.Rect{X: r.X, Y: r.Y + 2, W: r.W, H: r.H - 2}
	return tiRect, listRect
}

// SubRects exposes the input/items split (see subRects) so callers that own
// finer mouse routing — e.g. instancepicker, which routes by clicked column
// within an item row — can detect the fuzzy-input header and read the items
// area's content origin without reimplementing the layout.
func (m *Model) SubRects() (tiRect, listRect component.Rect) {
	return m.subRects()
}

// RowAtY maps an absolute screen row y to the visible display index of the item
// rendered there. ok is false when y falls in the fuzzy-input header, below the
// last visible item, or the list is empty. The returned index is a display
// index suitable for SetCursor (GetCursor maps it back to the original index
// when fuzzy filtering is active). It is the single row hit test: OnMouseClick,
// OnMouseDoubleClick and OnMouseHover all route through it.
func (m *Model) RowAtY(y int) (idx int, ok bool) {
	_, listRect := m.subRects()
	if listRect.H < 1 {
		return 0, false
	}
	row := y - listRect.Y
	if row < 0 {
		return 0, false
	}
	idx = m.yOffset + row
	if idx >= m.VisibleLen() {
		return 0, false
	}
	return idx, true
}

// SelectedRowY returns the absolute screen row where the cursor's item is drawn
// — the reverse of RowAtY for the current cursor. ok is false when the list has
// no visible items or the items area has zero height. Callers use it to anchor a
// popup (e.g. a context menu) at the selected row; it stays correct under fuzzy
// filtering because the cursor is a display index (DisplayCursor), guaranteed on
// screen by SetCursor's yOffset reclamp.
func (m *Model) SelectedRowY() (y int, ok bool) {
	if m.VisibleLen() < 1 {
		return 0, false
	}
	_, listRect := m.subRects()
	if listRect.H < 1 {
		return 0, false
	}
	return listRect.Y + (m.cursor - m.yOffset), true
}

// refilter re-evaluates the fuzzy filter against the current input value.
// It clears fuzziedItems when the input is empty; otherwise it ranks and sorts
// the matches, preserving a non-nil empty slice when there are no results (so
// the list shows zero items rather than all items).
func (m *Model) refilter() {
	v := m.input.Value()
	if v == "" {
		m.fuzziedItems = nil
		return
	}
	matches := fuzzy.RankFindFold(v, m.itemTexts)
	if matches == nil {
		m.fuzziedItems = fuzzy.Ranks{}
		return
	}
	slices.SortFunc(matches, func(a, b fuzzy.Rank) int {
		return cmp.Compare(a.Distance, b.Distance)
	})
	m.fuzziedItems = matches
	m.cursor = numeric.Clamp(m.cursor, 0, len(m.fuzziedItems)-1)
}

// placeItems writes item rows directly to the parent canvas at listRect using
// cell-native rendering. Cursor row gets reverse video; alternate rows get the
// configured background color.
//
//nolint:gocyclo // hot rendering path with cursor/marker/segment branches; extraction adds call overhead.
func (m *Model) placeItems(s component.Screen, listRect component.Rect, items [][]Segment) {
	w, h := listRect.W, listRect.H
	if w < 1 || h < 1 {
		return
	}

	for row := range h {
		idx := m.yOffset + row
		if idx >= len(items) {
			break
		}

		var baseBg color.Color
		if idx&1 == 1 && m.bundle.Config.Style.EverySecondListItemBg.IsSet() {
			baseBg = m.bundle.Config.Style.EverySecondListItemBg.Color
		}
		if baseBg != nil {
			uicanvas.FillRowAt(s, listRect.X, listRect.Y+row, w, uv.Style{Bg: baseBg})
		}

		// Step 1: Place segments grapheme-by-grapheme.
		col := 0

		// Render index prefix directly to avoid allocating a new segment slice.
		if m.showItemIdx {
			prefix := fmt.Sprintf("%3d. ", idx+1)
			gr := uniseg.NewGraphemes(prefix)
			for gr.Next() {
				grapheme := gr.Str()
				gw := gr.Width()
				if gw == 0 {
					continue
				}
				if col+gw > w {
					break
				}
				s.SetCell(listRect.X+col, listRect.Y+row, &uv.Cell{
					Content: grapheme,
					Width:   gw,
					Style:   uv.Style{Bg: baseBg},
				})
				col += gw
			}
		}

		for _, seg := range items[idx] {
			cellStyle := seg.Style
			if cellStyle.Bg == nil {
				cellStyle.Bg = baseBg
			}

			gr := uniseg.NewGraphemes(seg.Text)
			for gr.Next() {
				grapheme := gr.Str()
				gw := gr.Width()
				if gw == 0 {
					continue
				}
				if col+gw > w {
					break
				}
				s.SetCell(listRect.X+col, listRect.Y+row, &uv.Cell{
					Content: grapheme,
					Width:   gw,
					Style:   cellStyle,
				})
				col += gw
			}
			if col >= w {
				break
			}
		}

		// Step 2: Cursor highlight — reverse video. Clearing AttrFaint is a
		// harmless safety net (phase labels color via foreground, not faint);
		// any faint cell still inverts to full contrast under the cursor.
		if idx == m.cursor {
			for x := range w {
				cell := s.CellAt(listRect.X+x, listRect.Y+row)
				if cell != nil {
					cell.Style.Attrs &^= uv.AttrFaint
					cell.Style.Attrs |= uv.AttrReverse
				}
			}
		}

		// Step 3: Hover tint — bg wash on the hovered, non-cursor row.
		if idx == m.hoverRow && idx != m.cursor && m.bundle.Config.Style.HoverRowBg.IsSet() {
			hoverBg := m.bundle.Config.Style.HoverRowBg.Color
			for x := range w {
				if cell := s.CellAt(listRect.X+x, listRect.Y+row); cell != nil {
					cell.Style.Bg = hoverBg
				}
			}
		}
	}
}

func (m *Model) IsFocused() bool {
	return m.input.Focused()
}

func (m *Model) Focus() {
	m.input.Focus()
	msgs.PostAsync(m.poster, msgs.FocusMsg{GrabFocus: true})
}

func (m *Model) Unfocus() {
	m.input.Blur()
	msgs.PostAsync(m.poster, msgs.FocusMsg{GrabFocus: false})
}

// Blur drops the fuzzy input focus without posting msgs.FocusMsg (unlike
// Unfocus). The caller owns the model's focus gate. Used when focus is taken
// away out-of-band (e.g. a click that switches tabs).
func (m *Model) Blur() {
	m.input.Blur()
}

func (m *Model) ClearFuzzy() {
	m.input.SetValue("")
	m.fuzziedItems = nil
}

// VisibleLen returns the number of items currently visible: the filtered
// match count when fuzzy filtering is active, otherwise the total item count.
func (m *Model) VisibleLen() int {
	if m.fuzziedItems != nil {
		return len(m.fuzziedItems)
	}
	return len(m.items)
}

// VisibleIndices returns the original item indices currently visible, in display
// order: the fuzzy-matched OriginalIndex of each rank when filtering is active,
// or every index 0..len(items)-1 when no filter is applied. Callers use it to
// scope bulk operations to exactly the items the user sees.
func (m *Model) VisibleIndices() []int {
	if m.fuzziedItems == nil {
		out := make([]int, len(m.items))
		for i := range m.items {
			out[i] = i
		}
		return out
	}
	out := make([]int, len(m.fuzziedItems))
	for i, rank := range m.fuzziedItems {
		out[i] = rank.OriginalIndex
	}
	return out
}

// listAreaHeight returns the height of the items area (drawRect minus the
// input's 2 rows), clamped to at least 1 to avoid zero-step scrolling.
func (m *Model) listAreaHeight() int {
	h := m.drawRect.H - 2
	if h < 1 {
		return 1
	}
	return h
}

// SetCursor moves the cursor to an absolute display index, clamping to the
// visible range and reclamping yOffset so the cursor stays on screen. It is the
// absolute counterpart to MoveCursor.
func (m *Model) SetCursor(idx int) {
	n := m.VisibleLen()
	if n == 0 {
		return
	}
	m.cursor = numeric.Clamp(idx, 0, n-1)
	h := m.listAreaHeight()
	if m.cursor < m.yOffset {
		m.yOffset = m.cursor
	} else if m.cursor >= m.yOffset+h {
		m.yOffset = m.cursor - h + 1
	}
}

// SelectByName moves the cursor to the first item whose display text equals
// name, returning whether a match was found. It is the name-addressed counterpart
// to SetCursor (which takes a display index): callers that know an item's text but
// not its position — e.g. selecting a just-created file row by basename — use this.
// The match is against the full item text (ItemText), so it works whether the row
// is one plain segment or several styled spans. When a fuzzy filter is active the
// scan is over the visible (filtered) rows so the resolved index is a display index
// SetCursor understands; an unmatched name leaves the cursor unchanged.
func (m *Model) SelectByName(name string) bool {
	for disp, orig := range m.VisibleIndices() {
		if m.itemTexts[orig] == name {
			m.SetCursor(disp)
			return true
		}
	}
	return false
}

// OnMouseClick implements component.MouseTarget. A click in the top 2-row fuzzy
// input focuses it and places the input caret at the clicked column.
//
// For the item rows the behavior is mode-dependent:
//   - Default mode (doubleClickActivate == false): two-state — a click on a
//     non-cursor row selects it; a click on the cursor row runs the activate
//     hook. This preserves the original behavior for callers such as the help
//     overlay that route all clicks through OnMouseClick.
//   - Double-click-activate mode (doubleClickActivate == true): select-only —
//     every item-row click calls SetCursor(idx), never activating, regardless of
//     whether the clicked row is already the cursor. Activation requires
//     OnMouseDoubleClick instead.
//
// Screen rows map to display indices through RowAtY (the same hit test
// OnMouseDoubleClick uses); the activate hook reads GetCursor() for the logical
// index.
func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	tiRect, _ := m.subRects()
	if tiRect.H >= 1 && y >= tiRect.Y && y < tiRect.Y+tiRect.H {
		if !m.input.Focused() {
			m.Focus()
		}
		m.input.SetCursor(m.input.RuneIndexAtViewportX(x - tiRect.X))
		return true
	}
	idx, ok := m.RowAtY(y)
	if !ok {
		return false
	}
	// Default two-state: cursor row activates, non-cursor row selects.
	// In double-click-activate mode every click is select-only (never activates).
	if !m.doubleClickActivate && idx == m.cursor {
		if m.onActivate != nil {
			m.onActivate()
		}
	} else {
		m.SetCursor(idx)
	}
	return true
}

// OnMouseDoubleClick implements component.DoubleClickTarget. A double-click in
// the top 2-row fuzzy input delegates to OnMouseClick for the header case
// (focus + place caret). A double-click on a valid item row moves the cursor to
// that row and fires the activate hook; a double-click outside any item row
// returns false. This method is unconditional — it does not gate on
// doubleClickActivate, because it is only reached via the double-click route
// that opt-in callers wire up.
func (m *Model) OnMouseDoubleClick(x, y int, btn uv.MouseButton) bool {
	tiRect, _ := m.subRects()
	if tiRect.H >= 1 && y >= tiRect.Y && y < tiRect.Y+tiRect.H {
		// Header region — delegate to the single-click handler (focus + caret).
		return m.OnMouseClick(x, y, btn)
	}
	idx, ok := m.RowAtY(y)
	if !ok {
		return false
	}
	m.SetCursor(idx)
	if m.onActivate != nil {
		m.onActivate()
	}
	return true
}

// OnMousePaste implements component.MousePasteTarget: a middle-click on the
// fuzzy-input header focuses it, places the caret at the clicked offset, inserts
// the clipboard content there, and re-filters (mirroring the OnPaste path).
// Clicks on item rows are ignored (they are not text inputs). Returns true when
// the content was inserted.
func (m *Model) OnMousePaste(x, y int, content string) bool {
	tiRect, _ := m.subRects()
	if tiRect.H < 1 || y < tiRect.Y || y >= tiRect.Y+tiRect.H {
		return false
	}
	if !m.input.Focused() {
		m.Focus()
	}
	m.input.SetCursor(m.input.RuneIndexAtViewportX(x - tiRect.X))
	m.input.InsertText(content)
	m.refilter()
	return true
}

// MoveCursor moves the cursor by n display rows, saturating at both ends of the
// visible range. It is the relative counterpart to SetCursor and delegates to it,
// so the clamp and the yOffset reclamp live in exactly one place.
func (m *Model) MoveCursor(n int) {
	m.SetCursor(m.cursor + n)
}

// ScrollViewport pans the list by n display rows (n > 0 scrolls toward later
// items, n < 0 toward earlier ones) WITHOUT moving the selection, then drags the
// cursor to the nearest viewport edge if the pan pushed it off-screen (vim-style
// drag-at-edge). yOffset is clamped to [0, max(0, VisibleLen()-h)]. It is the
// pure-pan counterpart to MoveCursor (which moves the cursor and lets yOffset
// follow); here yOffset leads and the cursor follows only at the edge.
func (m *Model) ScrollViewport(n int) {
	if n == 0 {
		return
	}
	h := m.listAreaHeight()
	maxOff := max(0, m.VisibleLen()-h)
	m.yOffset = numeric.Clamp(m.yOffset+n, 0, maxOff)
	if m.cursor < m.yOffset {
		m.cursor = m.yOffset
	} else if m.cursor >= m.yOffset+h {
		m.cursor = m.yOffset + h - 1
	}
}

// OnMouseScroll implements component.ScrollTarget: a vertical wheel notch over
// the list pans the viewport by lines rows (ScrollViewport) and is always
// consumed. Pointer position is irrelevant — the whole list scrolls as one.
func (m *Model) OnMouseScroll(_, _, lines int) bool {
	m.ScrollViewport(lines)
	return true
}

// OnMouseHover implements component.MouseHoverTarget: it records the item row
// under the pointer so the render pass can tint it (Style.HoverRowBg). It maps y
// to a display index via RowAtY; moving onto the fuzzy header or past the last
// item clears the hover. x is ignored (a whole row is tinted) and the
// cursor/selection is never moved — hover is purely visual. Returns true when the
// hovered row (or the has-hover state) changed, so the caller can gate redraws to
// real moves.
func (m *Model) OnMouseHover(_, y int) bool {
	idx, ok := m.RowAtY(y)
	if !ok {
		return m.ClearMouseHover()
	}
	if m.hoverRow == idx {
		return false
	}
	m.hoverRow = idx
	return true
}

// ClearMouseHover drops any active hover, returning true only when it actually
// cleared (so a no-op move off the rows reports no change).
func (m *Model) ClearMouseHover() bool {
	if m.hoverRow < 0 {
		return false
	}
	m.hoverRow = -1
	return true
}

func (m *Model) Len() int {
	return len(m.items)
}

func (m *Model) AddItem(item []Segment) {
	m.items = append(m.items, item)
	m.itemTexts = append(m.itemTexts, ItemText(item))
	h := m.listAreaHeight()
	if len(m.items) > h {
		m.yOffset = len(m.items) - h
		if m.cursor < m.yOffset {
			m.cursor = m.yOffset
		}
	}
}

func (m *Model) Clear() {
	m.WithItems(nil)
}

func (m *Model) SetItemAtIndex(i int, v []Segment) {
	m.items[i] = v
	m.itemTexts[i] = ItemText(v)
}

func (m *Model) SetItemUnderCursor(v []Segment) {
	if m.Len() < 1 {
		return
	}
	m.SetItemAtIndex(m.GetCursor(), v)
}

func (m *Model) GetCursor() int {
	if len(m.fuzziedItems) < 1 {
		return m.cursor
	}
	return m.fuzziedItems[m.cursor].OriginalIndex
}

// DisplayCursor returns the cursor's position as a display index — its row in
// the currently visible (fuzzied) order — the counterpart to GetCursor, which
// maps back to the original item index. Callers that do their own click routing
// use it to test whether a clicked display row (see RowAtY) is the cursor row,
// which stays correct while a fuzzy filter reorders items.
func (m *Model) DisplayCursor() int {
	return m.cursor
}

func (m *Model) GetItemUnderCursor() string {
	if m.Len() < 1 {
		return ""
	}
	return m.itemTexts[m.GetCursor()]
}
