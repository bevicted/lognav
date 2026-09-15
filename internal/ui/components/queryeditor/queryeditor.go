package queryeditor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/externaleditor"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/querytemplate"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/filehandler"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/components/queryeditor/highlight"
	"github.com/bevicted/lognav/internal/ui/components/uieditor"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/xdg"
	uv "github.com/charmbracelet/ultraviolet"
)

const (
	queryFilesDir      = "queries"
	queryFileExt       = ".dataprime"
	viewInContextQuery = `source logs around @'%s' interval 3s
| f $d.kubernetes.pod_id ~ '%s'
| orderby $m.timestamp asc
`
)

type EventHandler struct {
	OnConfirmPressed func()
}

type QueryLoadMsg struct {
	Q   string
	Err error
}

// Compile-time seam opt-ins: the authoritative list of the optional component
// seams *Model implements. Every implemented seam belongs here — a seam rename
// or signature change would otherwise silently demote its handler to an
// ordinary method and drop that input with no build error.
var (
	_ component.KeyTarget            = (*Model)(nil)
	_ component.MouseTarget          = (*Model)(nil)
	_ component.SecondaryMouseTarget = (*Model)(nil)
	_ component.DoubleClickTarget    = (*Model)(nil)
	_ component.MousePasteTarget     = (*Model)(nil)
	_ component.ScrollTarget         = (*Model)(nil)
	_ component.MouseHoverTarget     = (*Model)(nil)
	_ component.PasteTarget          = (*Model)(nil)
	_ component.FocusReleaser        = (*Model)(nil)
	_ component.ContextMenuOpener    = (*Model)(nil)
)

type Model struct {
	bundle            deps.Bundle
	drawRect          component.Rect
	ctx               context.Context
	kh                *keys.Handler
	textAreaKH        *keys.Handler
	alwaysHandleKH    *keys.Handler
	editor            *uieditor.Editor
	fhKeyKH           *keys.Handler
	fh                *filehandler.Model
	eventHandler      *EventHandler
	defaultQuery      string
	showFiles         bool
	snippets          []snippet
	snippetList       *list.Model
	snippetListKH     *keys.Handler
	showSnippets      bool
	highlightedTokens [][]highlight.Token
	logger            *slog.Logger
	poster            msgs.Poster
}

func New(ctx context.Context, bundle deps.Bundle, eh *EventHandler) (*Model, error) {
	return newAt(ctx, bundle, eh, time.Now())
}

// newAt resolves startup templates at one explicit instant for deterministic tests.
func newAt(ctx context.Context, bundle deps.Bundle, eh *EventHandler, now time.Time) (*Model, error) {
	content, err := querytemplate.Resolve(bundle.Config, now)
	if err != nil {
		return nil, fmt.Errorf("resolve startup query templates: %w", err)
	}

	ed := uieditor.New()
	ed.SetPlaceholder("dataprime query")
	ed.SetPrompt("")

	dir, err := getDirPath()
	if err != nil {
		return nil, fmt.Errorf("resolve query data dir: %w", err)
	}

	m := &Model{
		bundle:       bundle,
		ctx:          ctx,
		editor:       ed,
		eventHandler: eh,
		defaultQuery: content.DefaultQuery,
		logger:       slog.Default().With(logging.KeyComponent, "queryeditor"),
	}
	m.setQueryInEditor(m.defaultQuery)
	for _, s := range content.Snippets {
		m.snippets = append(m.snippets, snippet{Snippet: s.Snippet, Description: s.Desc})
	}
	m.snippetList = list.New(bundle)
	m.snippetList.WithItems(formatSnippetList(m.snippets))
	// Double-click-to-activate, matching the other tab-content lists
	// (instancepicker, filehandler): a single click selects, a double-click
	// inserts the snippet (via the WithActivate hook wired in keybinds.go).
	m.snippetList.WithDoubleClickActivate()
	fhOpts := filehandler.FileOpts{
		Dir: dir,
		Ext: queryFileExt,
		// Preview saved query files with Dataprime syntax highlighting, matching
		// the live query editor (both go through highlight.Segments/CellStyle).
		PreviewFn: func(path string) ([][]list.Segment, error) {
			b, err := os.ReadFile(path) // #nosec G304 -- filehandler-resolved query path (queries dir + .dataprime ext)
			if err != nil {
				return nil, err
			}
			return highlight.Segments(bundle.Config, string(b)), nil
		},
	}
	fhRead := func(r io.Reader, _ string) uv.Event {
		q, err := filehandler.ReadAll(r)
		if err != nil {
			return QueryLoadMsg{Err: err}
		}
		return QueryLoadMsg{Q: q}
	}
	fhWrite := func(w io.Writer) uv.Event {
		_, err := w.Write([]byte(m.getQuery()))
		if err != nil {
			m.logger.Error("query file write failed", logging.KeyError, err)
		}
		return nil
	}
	m.fh = filehandler.New(bundle, fhOpts, fhRead, fhWrite)
	// A click on the already-selected saved-query row loads it — the click
	// analogue of pressing the Accept keybind on the cursor row in file-browser
	// mode.
	m.fh.WithActivate(m.fh.ReadFileUnderCursor)
	bindKeyhandlersToModel(m)

	return m, nil
}

// SetPoster injects the runtime poster and forwards it to the filehandler and
// snippet list. Called once at startup from ui.Model.SetPoster.
func (m *Model) SetPoster(p msgs.Poster) {
	m.poster = p
	m.fh.SetPoster(p)
	m.snippetList.SetPoster(p)
}

// ReleaseFocus implements component.FocusReleaser: blur every input the query
// editor owns (the text editor, the snippet list, the saved-query file browser)
// without posting msgs.FocusMsg. Called by the root model when a mouse click
// switches away from this tab while one of these inputs holds focus.
func (m *Model) ReleaseFocus() {
	m.editor.Blur()
	m.snippetList.Blur()
	m.fh.Blur()
}

// requestQuit requests a clean exit. It is the quit hook wired into the Quit
// keybind; keybind actions run on the loop goroutine, hence msgs.RequestQuit.
// It stays a method because the binding needs a func value.
func (m *Model) requestQuit() {
	msgs.RequestQuit(m.poster)
}

func (m *Model) Init() {
	m.fh.Init()
}

// sizeChildren propagates the queryeditor's drawRect dimensions to the
// textarea. Called from SetRect whenever the parent assigns a new rect.
func (m *Model) sizeChildren() {
	if m.drawRect.W < 1 || m.drawRect.H < 1 {
		return
	}
	m.editor.SetWidth(m.drawRect.W)
	m.editor.SetHeight(m.drawRect.H)
}

// OnSnapshotRestore loads the restored query into the editor. Mirrors the
// previous Update arm; the root calls it directly (B1 direct-dispatch).
func (m *Model) OnSnapshotRestore(msg msgs.SnapshotRestoreMsg) {
	m.setQueryInEditor(msg.Snapshot.Query)
}

// OnFileIO forwards a filehandler IO result to the owned filehandler. The id
// guard inside the filehandler makes a non-matching IOMsg a no-op. Mirrors the
// previous Update arm.
func (m *Model) OnFileIO(msg filehandler.IOMsg) {
	m.fh.OnFileIO(msg)
}

// OnQueryLoaded installs a query loaded from a file into the editor and exits
// file-browser mode. Logs and drops the message on error. Mirrors the previous
// Update arm.
func (m *Model) OnQueryLoaded(msg QueryLoadMsg) {
	if msg.Err != nil {
		m.logger.Error("query load failed", logging.KeyError, msg.Err)
		return
	}
	m.setQueryInEditor(msg.Q)
	m.showFiles = false
	m.logger.Info("query loaded from file")
}

// OnViewInContext builds the view-in-context Dataprime query from the message's
// timestamp and pod id and installs it in the editor. Mirrors the previous
// Update arm.
func (m *Model) OnViewInContext(msg msgs.ViewInContextMsg) {
	ts := time.UnixMicro(msg.Timestamp).Format(time.RFC3339)
	m.setQueryInEditor(fmt.Sprintf(viewInContextQuery, ts, msg.PodID))
	m.logger.Info("view-in-context query generated", "timestamp", ts, "pod_id", msg.PodID)
}

// OnInjectQuery inserts the message's Dataprime clause into the current query so
// it follows the last existing filter clause, keeping the new filter grouped with
// the others (e.g. before a trailing orderby). When the query has no filter line
// the clause is appended at the end. The result is installed in the editor and
// staged (not run); an empty current query still receives the clause so the user
// reviews it on the query tab before running.
func (m *Model) OnInjectQuery(msg msgs.InjectQueryMsg) {
	q := strings.TrimRight(m.getQuery(), "\n")
	m.setQueryInEditor(insertFilterClause(q, msg.Snippet))
	m.logger.Info("query clause injected", "snippet", msg.Snippet)
}

// OnSetEditorQuery REPLACES the editor buffer with the message's query (and
// stages it in State via setQueryInEditor). Used by the collect flow to show the
// archive's stored query; the query is staged, not run.
func (m *Model) OnSetEditorQuery(msg msgs.SetEditorQueryMsg) {
	m.setQueryInEditor(msg.Query)
	m.logger.Info("query set in editor")
}

// insertFilterClause inserts clause (a single "| filter ..." line) into query
// immediately after its last filter clause line. When query has no filter line
// the clause is appended at the end; an empty query becomes clause alone. query
// must be right-trimmed of newlines and clause carries no trailing newline.
func insertFilterClause(query, clause string) string {
	if query == "" {
		return clause
	}
	lines := strings.Split(query, "\n")
	last := -1
	for i, ln := range lines {
		if isFilterClauseLine(ln) {
			last = i
		}
	}
	if last == -1 {
		return query + "\n" + clause
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:last+1]...)
	out = append(out, clause)
	out = append(out, lines[last+1:]...)
	return strings.Join(out, "\n")
}

// isFilterClauseLine reports whether a query line is a Dataprime filter clause,
// i.e. its first keyword (after an optional leading pipe) is filter or its f
// alias.
func isFilterClauseLine(line string) bool {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "|"))
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return false
	}
	return fields[0] == "filter" || fields[0] == "f"
}

// OnEditorFinished installs the result of an external $EDITOR session into the
// editor, logging any editor error. Mirrors the previous Update arm.
func (m *Model) OnEditorFinished(msg EditorFinishedMsg) {
	m.logger.Debug("external editor closed")
	if msg.err != nil {
		m.logger.Error("editor error", logging.KeyError, msg.err)
	}
	m.setQueryInEditor(msg.out)
}

// OnPaste routes a bracketed-paste event to the active mode's target: the
// filehandler in file-browser mode, the snippet list in snippets mode, and the
// query editor otherwise (inserting the pasted text and rehighlighting).
// Mirrors the previous Update paste arm. The root calls it directly.
func (m *Model) OnPaste(ev uv.Event) {
	switch {
	case m.showFiles:
		m.fh.OnPaste(ev)
	case m.showSnippets:
		m.snippetList.OnPaste(ev)
	default:
		if p, ok := ev.(uv.PasteEvent); ok {
			m.editor.InsertString(p.Content)
			m.bundle.State.SetQuery(m.getQuery())
			m.rehighlight()
		}
	}
}

// HandleKey dispatches a key event using queryeditor's five-group precedence,
// faithfully mirroring the former key-event arm:
//  1. alwaysHandleKH is checked first regardless of mode.
//  2. showFiles mode: if fh is focused, all keys route to fh.HandleKey
//     (leaf-consumes-all); otherwise fhKeyKH is tried, then fh.HandleKey
//     unconditionally (preserving current fall-through behaviour).
//  3. showSnippets mode: if snippetList is focused, all keys route to
//     snippetList.HandleKey (leaf-consumes-all); otherwise snippetListKH is
//     tried, then snippetList.HandleKey unconditionally.
//  4. editor focused: textAreaKH is tried; on a miss the key is fed to the
//     native uieditor and state/highlight are updated when the buffer changed.
//  5. default: kh is tried; on a miss KeyIgnored is returned.
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if m.alwaysHandleKH.Run(ev) {
		return component.KeyHandled
	}
	switch {
	case m.showFiles:
		if m.fh.IsFocused() {
			// Focused filehandler swallows all keys (leaf-consumes-all).
			m.fh.HandleKey(ev)
			return component.KeyHandled
		}
		if m.fhKeyKH.Run(ev) {
			return component.KeyHandled
		}
		// Unfocused fh: forward unconditionally (mirrors current fall-through).
		m.fh.HandleKey(ev)
		return component.KeyHandled

	case m.showSnippets:
		if m.snippetList.IsFocused() {
			// Focused snippetList swallows all keys (leaf-consumes-all).
			m.snippetList.HandleKey(ev)
			return component.KeyHandled
		}
		if m.snippetListKH.Run(ev) {
			return component.KeyHandled
		}
		// Unfocused snippetList: forward unconditionally (mirrors current fall-through).
		m.snippetList.HandleKey(ev)
		return component.KeyHandled

	case m.editor.Focused():
		if m.textAreaKH.Run(ev) {
			return component.KeyHandled
		}
		// Focused editor consumes the remaining key as an edit (leaf-consumes-all);
		// rehighlight only when the buffer actually changed (pure motion does not).
		if m.editor.HandleKey(ev) {
			m.bundle.State.SetQuery(m.getQuery())
			m.rehighlight()
		}
		return component.KeyHandled

	default:
		if m.kh.Run(ev) {
			return component.KeyHandled
		}
	}
	return component.KeyIgnored
}

// OnMouseClick implements component.MouseTarget. In file-browser mode it
// forwards the click to the saved-queries filehandler (always select-only —
// activation via ReadFileUnderCursor requires OnMouseDoubleClick). In
// snippet-picker mode it forwards to the snippet list (select-only too, since the
// list is in double-click-activate mode). In editor mode it focuses the editor
// and places the caret at the click, mirroring the FocusTextInput key path
// (editor.Focus + off-loop GrabFocus).
func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	if m.showFiles {
		return m.fh.OnMouseClick(x, y, btn)
	}
	if m.showSnippets {
		return m.snippetList.OnMouseClick(x, y, btn)
	}
	// editor mode: focus the editor and place the caret at the click. The editor
	// is drawn with its content origin at drawRect (placeQuery writes cells at
	// r.X/r.Y), so the click is made relative to that origin before reverse-
	// mapping it onto the soft-wrapped display layout. Focus is grabbed the same
	// way the FocusTextInput keybind does — both go through focusEditor.
	m.focusEditor()
	m.editor.SetCursorAtDisplay(x-m.drawRect.X, y-m.drawRect.Y)
	return true
}

// OnMouseDoubleClick implements component.DoubleClickTarget. In file-browser
// mode it forwards to the owned filehandler's OnMouseDoubleClick so a
// double-click on a saved-query row activates (loads) it. In snippet-picker mode
// it forwards to the snippet list's OnMouseDoubleClick so a double-click on a
// snippet row activates (inserts) it via the WithActivate hook. In editor mode
// there are no rows to activate, so the double-click falls back to OnMouseClick
// behavior: focusing the editor and placing the caret at the click position.
func (m *Model) OnMouseDoubleClick(x, y int, btn uv.MouseButton) bool {
	if m.showFiles {
		return m.fh.OnMouseDoubleClick(x, y, btn)
	}
	if m.showSnippets {
		return m.snippetList.OnMouseDoubleClick(x, y, btn)
	}
	// Editor mode: double-click behaves like a single click (focus + place caret).
	return m.OnMouseClick(x, y, btn)
}

// OnMousePaste implements component.MousePasteTarget. In file-browser and
// snippet-list modes it forwards to the active sub-component's fuzzy-filter
// paste. In editor mode a middle-click focuses the editor, places the caret at
// the clicked display position, and inserts the clipboard content there —
// re-staging the query the same way a typed edit does (SetQuery + rehighlight).
func (m *Model) OnMousePaste(x, y int, content string) bool {
	if m.showFiles {
		return m.fh.OnMousePaste(x, y, content)
	}
	if m.showSnippets {
		return m.snippetList.OnMousePaste(x, y, content)
	}
	m.focusEditor()
	m.editor.SetCursorAtDisplay(x-m.drawRect.X, y-m.drawRect.Y)
	m.editor.InsertString(content)
	m.bundle.State.SetQuery(m.getQuery())
	m.rehighlight()
	return true
}

// OnMouseScroll implements component.ScrollTarget: a vertical wheel notch pans
// the active mode's viewport (drag-at-edge, the cursor/caret stays). File-browser
// and snippet-picker modes forward to their list; the default text-editor mode
// pans the editor viewport via uieditor.ScrollViewport. Always consumed (the
// query box always has a scrollable viewport, even when shorter than the area).
func (m *Model) OnMouseScroll(x, y, lines int) bool {
	if m.showFiles {
		return m.fh.OnMouseScroll(x, y, lines)
	}
	if m.showSnippets {
		return m.snippetList.OnMouseScroll(x, y, lines)
	}
	m.editor.ScrollViewport(lines)
	return true
}

// OnMouseHover implements component.MouseHoverTarget: in file-browser and
// snippet-picker modes it tints the hovered row of the active list; the default
// text-editor mode has no row to hover. Returns true when the hovered row changed.
func (m *Model) OnMouseHover(x, y int) bool {
	if m.showFiles {
		return m.fh.OnMouseHover(x, y)
	}
	if m.showSnippets {
		return m.snippetList.OnMouseHover(x, y)
	}
	return false
}

// ClearMouseHover drops hover retained by either picker mode.
func (m *Model) ClearMouseHover() bool {
	changed := m.fh.ClearMouseHover()
	if m.snippetList.ClearMouseHover() {
		changed = true
	}
	return changed
}

// postContextMenuEditor builds the editor-mode menu (insert snippet / reset to
// default / clear) and posts it anchored at (ax, ay). SuppressCloseRelease is
// set: each item re-establishes focus itself (openSnippets / focusEditor), so the close
// must not release focus and race that grab. Off-loop via poster.Go; nil-poster
// guard mirrors filehandler (SetPoster runs at startup, not in the constructor).
func (m *Model) postContextMenuEditor(ax, ay int) {
	if m.poster == nil {
		return
	}
	items := []msgs.ContextMenuItem{
		{Label: "insert snippet", Action: m.openSnippets},
		{Label: "reset to default", Action: func() { m.setQueryInEditor(m.defaultQuery); m.focusEditor() }},
		{Label: "clear", Action: func() { m.setQueryInEditor(""); m.focusEditor() }},
	}
	msgs.PostAsync(m.poster, msgs.ShowContextMenuMsg{Items: items, Anchored: true, AnchorX: ax, AnchorY: ay, SuppressCloseRelease: true})
}

// OpenContextMenu implements component.ContextMenuOpener. File-browser mode
// delegates to the saved-queries filehandler (Rename/Delete). Editor mode opens
// the insert snippet / reset to default / clear menu anchored at the caret line,
// left column. The snippet picker has no menu.
func (m *Model) OpenContextMenu() {
	switch {
	case m.showFiles:
		m.fh.OpenContextMenu()
	case m.showSnippets:
		// no menu in the snippet picker
	default: // editor mode
		// Anchor below the on-screen caret row. The render path draws the caret at
		// the visible row CursorDisplayLine()-ScrollYOffset() (render.go), so mirror
		// that here — using the absolute display line would mis-anchor a scrolled
		// multi-line query.
		row := m.editor.CursorDisplayLine() - m.editor.ScrollYOffset()
		m.postContextMenuEditor(m.drawRect.X, m.drawRect.Y+row+1)
	}
}

// OnMouseRight implements component.SecondaryMouseTarget. File-browser mode
// forwards to the filehandler menu; editor mode moves the caret onto the clicked
// position (mirroring OnMouseClick) before opening the editor menu just below the
// pointer, so an "insert snippet" lands at the click rather than the stale caret;
// the snippet picker does not consume it. The caret move does not focus the
// editor — the menu overlay takes focus and its items re-grab it on activation.
func (m *Model) OnMouseRight(x, y int) bool {
	switch {
	case m.showFiles:
		return m.fh.OnMouseRight(x, y)
	case m.showSnippets:
		return false
	default: // editor mode
		m.editor.SetCursorAtDisplay(x-m.drawRect.X, y-m.drawRect.Y)
		m.postContextMenuEditor(x, y+1)
		return true
	}
}

// Component

func (m *Model) GetKeybinds() []keys.Binding {
	b := append([]keys.Binding(nil), m.alwaysHandleKH.GetKeybinds()...)
	if m.showFiles {
		return append(b, filehandler.DecorateHints(m.fh.GetKeybinds())...)
	}
	if m.showSnippets {
		b = append(b, querySnippetHints(m.snippetListKH.GetKeybinds())...)
		return append(b, querySnippetHints(m.snippetList.GetKeybinds())...)
	}
	b = append(b, m.kh.GetKeybinds()...)
	return append(b, m.textAreaKH.GetKeybinds()...)
}

// querySnippetHints decorates copies of bindings exposed by the existing
// snippet handlers. Dispatch order remains in HandleKey; this only controls
// hint metadata for the current GetKeybinds context.
func querySnippetHints(bindings []keys.Binding) []keys.Binding {
	hints := append([]keys.Binding(nil), bindings...)
	for i := range hints {
		switch hints[i].Context {
		case "confirm text input":
			hints[i] = hints[i].WithHint("insert", 2)
		case "hide snippets menu":
			hints[i] = hints[i].WithHint("close", 3)
		case "focus fuzzy finder":
			hints[i] = hints[i].WithHint("filter", 4)
		}
	}
	return hints
}

// SetRect stores the assigned rect, sizes the textarea to fill it, and
// propagates the rect to sub-components that own the same area (filehandler
// and snippetList).
func (m *Model) SetRect(r component.Rect) {
	m.drawRect = r
	m.sizeChildren()
	// Forward to sub-components that own their own area equal to drawRect.
	m.fh.SetRect(r)
	m.snippetList.SetRect(r)
}

// Draw dispatches to the active mode's renderer. showFiles forwards to the
// filehandler's Draw; showSnippets forwards to the cell-native snippetList;
// the default mode delegates to placeQuery, which writes editor cells
// directly onto the parent canvas at drawRect.
func (m *Model) Draw(s component.Screen) *component.Cursor {
	if m.drawRect.W < 1 || m.drawRect.H < 1 {
		return nil
	}
	switch {
	case m.showFiles:
		return m.fh.Draw(s)
	case m.showSnippets:
		return m.snippetList.Draw(s)
	default:
		return m.placeQuery(s)
	}
}

// other

func (m *Model) ShowingFiles() bool {
	return m.showFiles
}

func (m *Model) ShowingSnippets() bool {
	return m.showSnippets
}

func (m *Model) getQuery() string {
	return m.editor.Value()
}

// Query returns the editor's current buffer contents. Exported so the root ui
// package can assert the editor buffer (distinct from app State) after routing a
// message that sets it.
func (m *Model) Query() string {
	return m.getQuery()
}

func (m *Model) setQueryInEditor(q string) {
	m.editor.SetValue(q)
	m.bundle.State.SetQuery(q)
	m.rehighlight()
}

// openSnippets blurs the editor and opens the snippet picker (an in-tab mode
// that grabs focus so its fuzzy filter captures typing). Shared by the Snippets
// keybind and the editor context-menu "insert snippet" item.
func (m *Model) openSnippets() {
	m.editor.Blur()
	m.showSnippets = true
	m.logger.Debug("snippets toggled", "visible", m.showSnippets)
	m.snippetList.Focus() // posts GrabFocus off-loop via poster
}

// focusEditor focuses the text editor and grabs input focus off-loop. It is the
// single entry point for every editor focus grab: the FocusTextInput keybind,
// leaving the snippet picker (cancel or insert), a click or middle-click paste
// in editor mode, and the editor context-menu clear/reset items (paired with the
// menu's SuppressCloseRelease so the close does not race the grab).
//
// Every caller runs on the loop goroutine, so the FocusMsg post must stay
// asynchronous — hence msgs.PostAsync.
func (m *Model) focusEditor() {
	m.editor.Focus()
	msgs.PostAsync(m.poster, msgs.FocusMsg{GrabFocus: true})
}

func (m *Model) rehighlight() {
	m.highlightedTokens = highlight.Highlight(m.editor.Value())
}

type EditorFinishedMsg struct {
	out string
	err error
}

// openEditor prepares a temporary file with the query content, builds an
// ExecRequest for the user's $EDITOR, and posts it off-loop via the poster.
// On temp-file errors the EditorFinishedMsg is posted directly so the caller
// sees the error without going through the exec path.
func openEditor(ctx context.Context, p msgs.Poster, in string, includeSnippets bool, snippets []snippet) {
	sharedSnippets := make([]config.Snippet, 0, len(snippets))
	for _, s := range snippets {
		sharedSnippets = append(sharedSnippets, config.Snippet{Snippet: s.Snippet, Desc: s.Description})
	}
	command, finish, err := externaleditor.Prepare(ctx, in, includeSnippets, sharedSnippets)
	if err != nil {
		msgs.PostAsync(p, EditorFinishedMsg{err: err})
		return
	}
	req := msgs.ExecRequest{
		Cmd: command,
		Done: func(err error) uv.Event {
			if err != nil {
				_, _ = finish()
				return EditorFinishedMsg{err: err}
			}
			out, err := finish()
			return EditorFinishedMsg{out: out, err: err}
		},
	}
	msgs.PostAsync(p, req)
}

func getDirPath() (string, error) {
	p, err := xdg.GetDataPath()
	if err != nil {
		return "", err
	}
	p = filepath.Join(p, queryFilesDir)

	// ensure dir exists
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}

	return p, nil
}
