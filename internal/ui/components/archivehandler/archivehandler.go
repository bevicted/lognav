// Package archivehandler is the Archive tab component. It wires the generic
// filehandler (LHS list / RHS preview) for the internal/archive registry: each
// row is one dispatched background query, colored by its aggregate state
// (ready/in-progress/error-or-expired), previewed with per-instance status, and
// actioned on Enter (collect a ready archive, poll an in-progress one, or notice
// an expired one).
//
// Like snapshothandler it HOLDS a *filehandler.Model and forwards the
// component.Component interface to it (no embed). The archive-specific behavior
// is the per-row RowStyler coloring, the PreviewFn, and the on-loop Enter
// routing (collect/poll/expired).
package archivehandler

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/filehandler"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// Compile-time assertion that *Model satisfies the Component interface (the set
// tabs.New requires; Phase 8 adds the archive handler as the 5th tab). The
// optional input seams it also forwards are pinned further down, next to the
// forwarders themselves.
var _ component.Component = (*Model)(nil)

// TokenResolver resolves one instance's bearer token + base URL by full CRN,
// WITHOUT disturbing any displayed logs (the store-preserving path). It is
// injected by the root and backed by the instancepicker's AccountManager +
// CRN-derived URL; the archive handler is a separate package and cannot reach
// instancepicker's unexported authManager directly. Off-loop only
// (GetAuthToken makes network calls). A nil resolver disables polling.
type TokenResolver func(ctx context.Context, crn string) (token, url string, err error)

// pollStatusFn is the status-fetch seam pollArchive runs OFF-LOOP. It is a
// package var (not a direct icl. call) only so tests can stub the status fetch
// deterministically — the AccountManager token internals are otherwise
// unreachable from a test in this package. Production wiring is the real
// icl.GetBackgroundQueryStatus; never reassign it outside tests.
var pollStatusFn = icl.GetBackgroundQueryStatus

// pollSaveFn is the archive-write seam pollArchive runs OFF-LOOP (mirrors
// pollStatusFn). Tests override it to assert a re-save without touching disk.
var pollSaveFn = archive.Save

type Model struct {
	bundle   deps.Bundle
	fh       *filehandler.Model
	kh       *keys.Handler // Quit; fires only when the fuzzy filter is not focused
	logger   *slog.Logger
	poster   msgs.Poster
	resolver TokenResolver // injected by ui; nil disables polling
	// cache holds the archives decoded by one archive.Scan() per refresh cycle,
	// shared by the row-style and preview hooks (see cache.go). The zero value is
	// ready to use.
	cache archiveCache
}

// New constructs the Archive tab handler, wiring the generic filehandler against
// the archive registry directory. It mirrors snapshothandler.New: resolve the dir
// first (archive.Dir() returns (string, error) — panic on a failure that prevents
// the tab from existing at all), then build the FileOpts literal.
func New(_ context.Context, bundle deps.Bundle) *Model {
	dir, err := archive.Dir()
	if err != nil {
		panic(err)
	}

	logger := slog.Default().With(logging.KeyComponent, "archivehandler")
	m := &Model{bundle: bundle, logger: logger}

	fhOpts := filehandler.FileOpts{
		Dir: dir,
		Ext: archive.FileExt,
		// Both hooks read the per-refresh decode cache, so each archive file is
		// read and unmarshalled once per refresh instead of once per hook.
		PreviewFn: m.previewFor,
		// NewRowStyler supplies the archive-state palette (ready/in-progress/
		// error-or-expired) straight to the generic file browser. Its per-listing
		// call is where the whole registry is decoded, in one directory walk.
		NewRowStyler: m.newArchiveRowStyler,
	}
	// Read is never the Enter action for archives (Enter is routed on-loop via
	// WithConfirmAction below to collect/poll/expired). This no-op satisfies the
	// filehandler ReadF contract; the filehandler still needs a non-nil ReadF.
	fhRead := func(_ io.Reader, _ string) uv.Event { return nil }
	// Write is never triggered from the archive tab; the no-op satisfies WriteF.
	fhWrite := func(_ io.Writer) uv.Event { return nil }

	m.fh = filehandler.New(bundle, fhOpts, fhRead, fhWrite)
	// Route Enter (and a click on the already-selected row) to the on-loop archive
	// action router instead of the default read-and-restore.
	m.fh.WithConfirmAction(m.onEnter)
	m.fh.WithActivate(m.onEnter)
	bindKeyhandlersToModel(m)

	return m
}

// requestQuit requests a clean exit. It is the Quit keybind action (the Archive
// tab owns its own Quit binding, like every other tab — the generic filehandler
// binds only file ops). Keybind actions run on the loop goroutine, hence
// msgs.RequestQuit. It stays a method because the binding needs a func value.
func (m *Model) requestQuit() {
	msgs.RequestQuit(m.poster)
}

// SetPoster injects the runtime poster and forwards it to the filehandler. Called
// once at startup from ui.Model.SetPoster (Phase 8).
func (m *Model) SetPoster(p msgs.Poster) {
	m.poster = p
	m.fh.SetPoster(p)
}

// SetTokenResolver injects the store-preserving auth resolver used by pollArchive.
// Wired by the root (Phase 8) to the instancepicker's AccountManager-backed
// resolver. A nil resolver disables polling (the poll worker no-ops per instance).
func (m *Model) SetTokenResolver(r TokenResolver) { m.resolver = r }

// newArchiveRowStyler is the filehandler NewRowStyler hook: it decodes the whole
// registry in ONE directory walk (archive.Scan) and returns the per-row colorer
// that reads that decode. Each archive's state lives in its own file, so the
// alternative was a read + unmarshal per row for the color and another per row
// for the preview; the fill makes it one decode per file per listing, shared by
// both hooks. Runs off-loop inside the filehandler's ListFiles worker.
func (m *Model) newArchiveRowStyler() filehandler.RowStyler {
	m.cache.fill()
	return m.archiveRowStyle
}

// archiveRowStyle is the per-row filehandler.RowStyler: it maps an archive file's
// aggregate state to a row color. The filehandler passes the FULL on-disk basename
// including the extension, so strip the ext to get the cache key. The archive
// itself comes from the listing's decode (see newArchiveRowStyler); a file the
// scan missed falls back to a single read inside the cache. An unreadable archive
// yields the zero style (unstyled), the same row appearance it had before. Colors
// only (no dim/attributes; they cause rendering issues).
func (m *Model) archiveRowStyle(fullBasename string) uv.Style {
	a, err := m.cache.get(trimExt(fullBasename))
	if err != nil {
		return uv.Style{}
	}
	var c config.Color
	switch {
	case a.IsExpired(time.Now()):
		c = m.bundle.Config.Style.ErrorColor
	case !a.IsReady():
		c = m.bundle.Config.Style.InProgressColor
	case archiveAllErrored(a):
		c = m.bundle.Config.Style.ErrorColor
	default:
		c = m.bundle.Config.Style.SuccessColor // ready: all terminal, collectable
	}
	return uv.Style{Fg: c.Color} // ConfigColor embeds color.Color
}

// previewFor is the filehandler PreviewFn: it renders the archive at path from
// the listing's decode (the row-style hook already read and unmarshalled it), so
// showing a preview costs no second read of the same file. A file the listing's
// scan missed falls back to a single read inside the cache, exactly as before.
func (m *Model) previewFor(path string) ([][]list.Segment, error) {
	a, err := m.cache.get(baseOf(path))
	if err != nil {
		return nil, err
	}
	return preview(m.bundle, a)
}

// trimExt strips the archive file extension. The filehandler passes the FULL
// on-disk basename incl. ext to RowStyler/GetItemUnderCursor, but
// archive.PathFor re-appends it, so callers must strip first.
func trimExt(fullBasename string) string {
	return strings.TrimSuffix(fullBasename, archive.FileExt)
}

// archiveAllErrored reports whether every instance of a ready archive reached a
// non-success terminal state (error or expired) — i.e. nothing is collectable, so
// the row is colored red even though it is technically "ready". A single success
// keeps it green (collectable; the errored instances surface in the preview).
func archiveAllErrored(a *archive.Archive) bool {
	if len(a.Instances) == 0 {
		return false
	}
	for i := range a.Instances {
		if a.Instances[i].State == archive.StateSuccess {
			return false
		}
	}
	return true
}

// onEnter routes the Enter / activate action for the row under the cursor. It runs
// ON the loop goroutine (the list confirm/activate action), so it MUST NOT post
// directly: the collect-trigger + the expired notice go off-loop via m.emit, and
// the poll path spawns its own off-loop worker via pollArchive.
func (m *Model) onEnter() {
	name := m.fh.GetItemUnderCursor()
	if name == "" {
		return
	}
	base := trimExt(name)
	p, err := archive.PathFor(base)
	if err != nil {
		m.logger.Warn("archive path resolve failed", "name", base, logging.KeyError, err)
		return
	}
	a, err := archive.Load(p)
	if err != nil {
		m.logger.Warn("archive load failed", "name", base, logging.KeyError, err)
		return
	}
	switch {
	case a.IsExpired(time.Now()):
		// Expired: no-op + a notice. Cannot collect (the server has dropped the data).
		m.emit(msgs.ShowDialogMsg{
			Title:   "Archive",
			Message: "this archive has expired — its results are no longer collectable",
			Buttons: []msgs.DialogButton{{Label: "OK"}},
		})
	case a.IsReady():
		// Ready + not expired: trigger collect. The root (Phase 8) handles
		// ArchiveCollectMsg by calling instancepicker.StartCollect + switching tab.
		m.emit(ArchiveCollectMsg{Archive: a})
	default:
		// In-progress: poll the non-terminal instances (Task 17). pollArchive's
		// on-loop prologue only reads + spawns the off-loop worker.
		m.pollArchive(name)
	}
}

// emit posts ev through msgs.PostAsync: onEnter runs on the loop goroutine.
func (m *Model) emit(ev uv.Event) {
	msgs.PostAsync(m.poster, ev)
}

// ListFiles refreshes the archive list (and recolors every row). Void: it spawns a
// tracked poster goroutine inside the filehandler. The root calls it on tab entry
// (multi-session staleness) and the poll-done handler calls it to recolor.
//
// Guarded against a nil poster: the root's WithOnLeave hook can fire a tab-entry
// refresh before SetPoster is wired (and the filehandler's ListFiles dereferences
// the poster unconditionally), so skip the refresh until the poster exists — the
// next tab entry, once wired, lists the files anyway.
func (m *Model) ListFiles() {
	if m.poster == nil {
		return
	}
	m.fh.ListFiles()
}

// SelectArchive refreshes the archive list and moves the cursor onto the row named
// name (the archive basename WITHOUT extension, e.g. ArchiveDispatchedMsg.Saved).
// The selection is deferred until the refresh's rows are populated (ListFiles is
// async). The root calls this on a successful dispatch so the just-created archive
// is selected when it switches to the Archive tab. No-op until the poster is wired
// (the filehandler's ListFiles dereferences the poster), matching ListFiles.
func (m *Model) SelectArchive(name string) {
	if m.poster == nil {
		return
	}
	m.fh.SelectByNameAfterRefresh(name)
}

// OnFileIO forwards a filehandler IO result to the owned filehandler. The id guard
// inside the filehandler makes a non-matching IOMsg a no-op.
func (m *Model) OnFileIO(msg filehandler.IOMsg) { m.fh.OnFileIO(msg) }

// Component interface (forwarded to m.fh).

func (m *Model) Init()                                     { m.fh.Init() }
func (m *Model) SetRect(r component.Rect)                  { m.fh.SetRect(r) }
func (m *Model) Draw(s component.Screen) *component.Cursor { return m.fh.Draw(s) }

// GetKeybinds advertises the filehandler's bindings plus the Archive tab's own
// Quit binding (mirroring how every other tab surfaces Quit in the help menu).
func (m *Model) GetKeybinds() []keys.Binding {
	return append(m.fh.GetKeybinds(), m.kh.GetKeybinds()...)
}

// Optional input-seam forwarding (mirrors snapshothandler) so the archive tab
// participates in the same key/mouse routing as the other file tabs.
//
// The assertions pin EVERY seam the forwarders below implement. The wrapper
// holds its *filehandler.Model rather than embedding it, so each seam is a
// deliberate per-tab opt-in (see docs/dev/design/components.md); the price is
// that a seam rename or signature
// change would demote a forwarder to an ordinary method and kill that input on
// this tab with no build error. Keep the list exhaustive.
var (
	_ component.KeyTarget            = (*Model)(nil)
	_ component.MouseTarget          = (*Model)(nil)
	_ component.MousePasteTarget     = (*Model)(nil)
	_ component.ScrollTarget         = (*Model)(nil)
	_ component.MouseHoverTarget     = (*Model)(nil)
	_ component.PasteTarget          = (*Model)(nil)
	_ component.DoubleClickTarget    = (*Model)(nil)
	_ component.SecondaryMouseTarget = (*Model)(nil)
	_ component.ContextMenuOpener    = (*Model)(nil)
	_ component.FocusReleaser        = (*Model)(nil)
)

// HandleKey dispatches a key through the Archive tab's precedence, mirroring
// snapshothandler: the Quit binding (m.kh) fires only when the fuzzy filter is
// NOT focused, so typing `q` into the filter still filters; all other keys fall
// through to the filehandler. Without the m.kh arm the Archive tab would have no
// Quit binding at all and a bare `q` would be silently dropped (the filehandler
// binds only file ops, and its list never binds Quit).
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if !m.fh.IsFocused() && m.kh.Run(ev) {
		return component.KeyHandled
	}
	return m.fh.HandleKey(ev)
}

// ReleaseFocus implements component.FocusReleaser: it blurs the fuzzy filter
// without posting msgs.FocusMsg. The root calls this when a mouse click switches
// away from the Archive tab while its filter is focused (the focus-race path in
// ui.onMouseClick) — without it the list input would stay focused after the
// switch, so on return the filehandler's focused branch would swallow every key
// (including Quit) even though the root's focus gate has been cleared.
func (m *Model) ReleaseFocus() { m.fh.Blur() }

func (m *Model) OnPaste(ev uv.Event) { m.fh.OnPaste(ev) }

func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	return m.fh.OnMouseClick(x, y, btn)
}

func (m *Model) OnMouseDoubleClick(x, y int, btn uv.MouseButton) bool {
	return m.fh.OnMouseDoubleClick(x, y, btn)
}

func (m *Model) OnMousePaste(x, y int, content string) bool { return m.fh.OnMousePaste(x, y, content) }

func (m *Model) OnMouseScroll(x, y, lines int) bool { return m.fh.OnMouseScroll(x, y, lines) }
func (m *Model) OnMouseHover(x, y int) bool         { return m.fh.OnMouseHover(x, y) }
func (m *Model) ClearMouseHover() bool              { return m.fh.ClearMouseHover() }
func (m *Model) OnMouseRight(x, y int) bool         { return m.fh.OnMouseRight(x, y) }
func (m *Model) OpenContextMenu()                   { m.fh.OpenContextMenu() }
