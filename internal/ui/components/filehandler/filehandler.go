package filehandler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
)

const (
	WipMarker = ".wip"
	filePerm  = 0o644
)

// Compile-time seam opt-ins: the authoritative list of the optional component
// seams *Model implements, including the two whose handlers live in
// contextmenu.go. Every implemented seam belongs here — a seam rename or
// signature change would otherwise silently demote its handler to an ordinary
// method and drop that input with no build error. NOTE this list is the
// filehandler's own; the tabs that wrap it (snapshothandler, archivehandler)
// hold it rather than embedding it, so each of them opts into these seams
// separately and pins its own list.
var (
	_ component.KeyTarget            = (*Model)(nil)
	_ component.MouseTarget          = (*Model)(nil)
	_ component.SecondaryMouseTarget = (*Model)(nil)
	_ component.DoubleClickTarget    = (*Model)(nil)
	_ component.MousePasteTarget     = (*Model)(nil)
	_ component.ScrollTarget         = (*Model)(nil)
	_ component.MouseHoverTarget     = (*Model)(nil)
	_ component.PasteTarget          = (*Model)(nil)
	_ component.ContextMenuOpener    = (*Model)(nil)
)

type Model struct {
	bundle   deps.Bundle
	drawRect component.Rect
	logger   *slog.Logger
	id       uint64
	kh       *keys.Handler
	list     *list.Model
	opts     FileOpts
	read     ReadF
	write    WriteF
	loading  bool
	previews map[string][][]list.Segment
	poster   msgs.Poster
	// pendingSelect, when non-empty, names a row to move the cursor onto the next
	// time a ListOP populates the list (ListFiles is async — the items land when
	// the ListOP IOMsg is applied, not when ListFiles returns). Consumed once: it
	// is cleared after the next list refresh whether or not the name matched.
	pendingSelect string
}

type (
	ReadF  func(r io.Reader, path string) uv.Event
	WriteF func(w io.Writer) uv.Event
)

// PreviewFunc returns the preview for the file at the given path as lines of
// cell-native styled segments (no ANSI). Each inner slice is one display line;
// a nil/empty inner slice renders as a blank line. Used to customize preview
// rendering for specific file formats.
type PreviewFunc func(path string) ([][]list.Segment, error)

var renameNoClobber = snapshot.RenameNoClobber

// noPreview is the placeholder shown when a file has no preview or its
// PreviewFunc errored.
func noPreview() [][]list.Segment {
	return [][]list.Segment{list.PlainItem("No preview available.")}
}

type FileOpts struct {
	Dir       string
	Ext       string
	PreviewFn PreviewFunc
	// OnDelete, if set, is consulted on the loop goroutine before an explicit
	// user delete spawns its remove worker. A non-nil error aborts the delete
	// (file held by another instance). Returning nil permits it (and lets the
	// owner run eviction side-effects). Receives the full basename incl. Ext.
	OnDelete func(name string) error
	// Validator, when set, is called with the proposed (extension-normalized)
	// name before a rename commits; a non-nil error aborts and is shown to the
	// user. Leaving it nil disables the check (e.g. .dataprime saved queries).
	Validator func(name string) error
	// OnRenamed, if set, is invoked off-loop after a successful rename with the
	// old and new full basenames (incl. Ext). Snapshot wiring backs it with a
	// sessionbus broadcast + a self-post; query wiring leaves it nil.
	OnRenamed func(ctx context.Context, oldBase, newBase string)
	// OnDeleted, if set, is invoked off-loop after a successful explicit delete
	// (DeleteFile) with the full basename (incl. Ext). Snapshot wiring backs it
	// with a snapshots_dirty broadcast so peers drop the file from their list
	// (spec §3.3). Query wiring leaves it nil.
	OnDeleted func(ctx context.Context, base string)
	// NewRowStyler, if set, is the single hook that colors listed files' rows.
	// ListFiles calls it ONCE per listing, before the per-file loop, and calls the
	// RowStyler it returns once per listed file (a nil RowStyler leaves every row
	// unstyled). The two-step shape is load-bearing: it is where wiring that needs
	// a per-listing index builds it — snapshot wiring scans the .inuse directory
	// once here instead of once per row. The filehandler itself knows nothing about
	// what the style means — snapshot wiring maps lock ownership to its own
	// success/warning colors, archive wiring maps archive state to its palette.
	// Both calls happen inside ListFiles' worker, i.e. OFF the loop goroutine.
	// Query wiring leaves it nil (every row unstyled).
	NewRowStyler func() RowStyler
}

// RowStyler maps a listed file's full basename (incl. Ext) to its row style;
// the zero uv.Style means unstyled. Built per listing by FileOpts.NewRowStyler.
type RowStyler func(fullBasename string) uv.Style

// handlerID hands each Model a process-unique tag so an IOMsg posted by one
// filehandler is ignored by sibling handlers (the id-epoch filter). A monotonic
// counter is enough — the tag only needs to be distinct per live handler.
var handlerID atomic.Uint64

func New(bundle deps.Bundle, opts FileOpts, readF ReadF, writeF WriteF) *Model {
	m := &Model{
		bundle:   bundle,
		logger:   slog.Default().With(logging.KeyComponent, "filehandler"),
		id:       handlerID.Add(1),
		list:     list.New(bundle).ShowIndex(true).WithDoubleClickActivate(),
		opts:     opts,
		read:     readF,
		write:    writeF,
		previews: map[string][][]list.Segment{},
	}
	bindKeyhandlersToModel(m)
	m.list.WithFuzzyConfirmAction(m.ReadFileUnderCursor)

	return m
}

// SetPoster injects the runtime poster used to spawn tracked goroutines for
// the filehandler's IO methods (ListFiles, ReadFile, WriteFile, preview,
// DeleteFile, RenameFile) and to deliver their results via
// PostCritical. The filehandler is constructed in its owning component's New()
// before the runtime supplies a poster; SetPoster is called once at startup
// (threaded ui.Model -> owning component -> filehandler) so every filehandler
// whose IO can run has a non-nil poster before any user IO action.
func (m *Model) SetPoster(p msgs.Poster) {
	m.poster = p
	m.list.SetPoster(p)
}

// WithActivate registers the action run when the user clicks the already-
// selected file row (the click analogue of pressing Enter on the cursor row).
// It is a thin passthrough to the inner list's WithActivate so owning components
// (snapshothandler, queryeditor) can wire their load action without reaching into
// the unexported list field. The action runs on the loop goroutine.
func (m *Model) WithActivate(a keys.Action) *Model {
	m.list.WithActivate(a)
	return m
}

// WithConfirmAction overrides the Enter (fuzzy-confirm) action on the inner list,
// replacing the default ReadFileUnderCursor wired in New. Owning components whose
// Enter must do something other than read-and-restore the file (e.g. the archive
// handler, which routes Enter to collect/poll/expired branches on the loop
// goroutine) wire their own confirm here. Like the inner list's
// WithFuzzyConfirmAction, the action runs ON the loop goroutine, so any post it
// triggers must go off-loop via poster.Go.
func (m *Model) WithConfirmAction(a keys.Action) *Model {
	m.list.WithFuzzyConfirmAction(a)
	return m
}

// Init kicks off the initial directory listing. ListFiles is void (it spawns a
// tracked poster goroutine).
func (m *Model) Init() {
	m.ListFiles()
}

// OnFileIO applies a filehandler IO result (the seam the owning component calls
// for a filehandler.IOMsg). It guards on the id, logs warnings/errors, and
// dispatches list/preview/read/write/move/delete completion. Non-matching ids
// are a no-op.
//
//nolint:gocyclo // IO-result dispatch; each case is a distinct op with no shared logic to extract.
func (m *Model) OnFileIO(msg IOMsg) {
	if msg.id != m.id {
		return
	}

	if msg.wrn != nil {
		m.logger.Warn("filehandler warning", "warning", msg.wrn)
	}

	if msg.err != nil {
		if msg.op == Preview {
			m.logger.Error("filehandler preview error", logging.KeyError, msg.err)
			m.previews[msg.files[0]] = noPreview()
			return
		}
		if msg.op == MoveOP {
			m.logger.Warn("rename failed", logging.KeyError, msg.err)
			msgs.PostAsync(m.poster, msgs.ShowDialogMsg{
				Title:   "Cannot rename",
				Message: msg.err.Error(),
				Buttons: []msgs.DialogButton{{Label: "OK"}},
			})
			return
		}
		m.logger.Error("filehandler error", logging.KeyError, msg.err)
		return
	}

	switch msg.op {
	case ListOP:
		items := make([][]list.Segment, len(msg.files))
		for i, f := range msg.files {
			var style uv.Style
			if i < len(msg.styles) {
				style = msg.styles[i]
			}
			items[i] = []list.Segment{{Text: f, Style: style}}
		}
		m.list.WithItems(items)
		// Apply a pending name-addressed selection now that the rows exist (consume
		// it once, whether or not it matched). WithItems clamps the cursor but does
		// not re-target it by name, so this is the only point a just-refreshed list
		// can land the cursor on a specific row.
		if m.pendingSelect != "" {
			m.list.SelectByName(m.pendingSelect)
			m.pendingSelect = ""
		}
		for _, f := range msg.files {
			if _, ok := m.previews[f]; !ok {
				m.preview(f) // void: spawns its own tracked goroutine
			}
		}
	case Preview:
		m.previews[msg.files[0]] = msg.preview

	case ReadOP:
		m.loading = false

	case WriteOP, MoveOP, DeleteOP:
		m.ListFiles() // void: spawns its own tracked goroutine
		// WriteOP/MoveOP carry exactly one name; a rotation DeleteOP carries the
		// whole batch it removed.
		for _, f := range msg.files {
			delete(m.previews, f)
		}
	}
}

// InvalidatePreview drops the cached preview for a file that changed on disk
// outside the filehandler's own IO ops — e.g. a manual snapshot save, which
// os.Renames over an existing name in ui.saveSnapshot and so never goes through
// the WriteOP arm that would have invalidated the cache. Without this the
// basename-keyed cache (no freshness check) would serve the previous file's
// preview until restart. The configured extension is stripped so name may be
// passed with or without it; the next ListFiles recomputes the dropped entry.
// Runs on the loop goroutine (single writer of m.previews), like OnFileIO.
func (m *Model) InvalidatePreview(name string) {
	delete(m.previews, strings.TrimSuffix(name, m.opts.Ext))
}

// OnPaste forwards a bracketed-paste event to the inner list when it is
// focused (mirroring the previous Update paste arm). Callers (queryeditor,
// snapshothandler) forward paste here directly.
func (m *Model) OnPaste(ev uv.Event) {
	if m.list.IsFocused() {
		m.list.OnPaste(ev)
	}
}

// HandleKey dispatches a key event through filehandler's precedence:
// When loading, all keys are dropped. When the list is focused, all keys are
// routed to m.list.HandleKey and KeyHandled is returned unconditionally
// (focused list is a leaf-consumes-all target). In the default branch, m.kh
// is tried first; on a miss, the key is forwarded to m.list.HandleKey so the
// list can handle navigation keys even when not focused (mirroring the current
// Update arm which always falls through to m.list.Update on a kh miss).
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if m.loading {
		return component.KeyIgnored
	}
	if m.list.IsFocused() {
		// Focused list swallows all keys (leaf-consumes-all rule).
		m.list.HandleKey(ev)
		return component.KeyHandled
	}
	if m.kh.Run(ev) {
		return component.KeyHandled
	}
	// Unfocused default: forward to list for navigation keys.
	return m.list.HandleKey(ev)
}

// Component

func (m *Model) GetKeybinds() []keys.Binding {
	return append(m.list.GetKeybinds(), m.kh.GetKeybinds()...)
}

// SetRect stores the assigned rect. The list's rect is applied lazily in Draw
// using absolute screen coordinates so the list draws directly onto the shared
// buffer.
func (m *Model) SetRect(r component.Rect) {
	m.drawRect = r
}

// OnMouseClick implements component.MouseTarget. The inner list occupies the
// left half of drawRect (Draw uses listW = W/2); clicks in the right half are
// the preview pane and are ignored. Left-half clicks forward to the list, whose
// own OnMouseClick accounts for its 2-row fuzzy-input header.
//
// Note: the inner list's drawRect is set inside Draw (not in SetRect), so Draw
// must have been called at least once before clicking for the list's hit-test
// to use the correct rect.
func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	listW := m.drawRect.W / 2
	if x >= m.drawRect.X+listW {
		return false
	}
	return m.list.OnMouseClick(x, y, btn)
}

// OnMouseDoubleClick implements component.DoubleClickTarget. A double-click in
// the left (list) half moves the cursor to the clicked row and fires the
// activate hook (the row activation that single-click-activate mode triggered on
// the cursor row). A double-click in the right (preview) half returns false,
// mirroring OnMouseClick's half-split.
func (m *Model) OnMouseDoubleClick(x, y int, btn uv.MouseButton) bool {
	listW := m.drawRect.W / 2
	if x >= m.drawRect.X+listW {
		return false
	}
	return m.list.OnMouseDoubleClick(x, y, btn)
}

// OnMousePaste implements component.MousePasteTarget: a middle-click in the
// left (list) half forwards to the inner list's fuzzy-filter paste; clicks in
// the right (preview) half are ignored. Mirrors OnMouseClick's half-split.
func (m *Model) OnMousePaste(x, y int, content string) bool {
	listW := m.drawRect.W / 2
	if x >= m.drawRect.X+listW {
		return false
	}
	return m.list.OnMousePaste(x, y, content)
}

// OnMouseScroll implements component.ScrollTarget: a vertical wheel notch pans
// the inner file list (drag-at-edge via list.ScrollViewport), regardless of
// which half the pointer is over (the preview pane has no independent scroll).
// Always consumed.
func (m *Model) OnMouseScroll(x, y, lines int) bool {
	return m.list.OnMouseScroll(x, y, lines)
}

// OnMouseHover implements component.MouseHoverTarget: a hover in the left (list)
// half tints the row under the pointer; moving into the right (preview) half
// clears any active list hover so no stale tint lingers (a y of -1 forces the
// list's RowAtY miss → clear). Mirrors OnMouseClick's half-split. Returns true
// when the hovered row (or has-hover state) changed.
func (m *Model) OnMouseHover(x, y int) bool {
	listW := m.drawRect.W / 2
	if x >= m.drawRect.X+listW {
		return m.list.ClearMouseHover()
	}
	return m.list.OnMouseHover(x, y)
}

// ClearMouseHover drops hover retained by the owned list.
func (m *Model) ClearMouseHover() bool {
	return m.list.ClearMouseHover()
}

// Draw renders the list+preview split cell-natively onto the shared buffer.
// The left half (listW = W/2) is owned by the inner list; the right half holds
// a DrawBox left-edge separator followed by the preview text. drawRect is split
// 50/50 horizontally between list and preview.
func (m *Model) Draw(s component.Screen) *component.Cursor {
	r := m.drawRect
	if r.W < 1 || r.H < 1 {
		return nil
	}
	listW := r.W / 2
	previewW := r.W - listW
	m.list.SetRect(component.Rect{X: r.X, Y: r.Y, W: listW, H: r.H})
	cur := m.list.Draw(s) // list draws straight onto the shared buffer

	prevRect := component.Rect{X: r.X + listW, Y: r.Y, W: previewW, H: r.H}
	canvas.DrawBox(s, prevRect, canvas.BorderSet{Left: "│"}, uv.Style{})
	previewItem := m.list.GetItemUnderCursor()
	pv, ok := m.previews[previewItem]
	if !ok {
		pv = noPreview()
	}
	inner := canvas.Pad(prevRect, 0, 0, 0, 1) // leave the left separator column
	maxX := inner.X + inner.W
	for i, segs := range pv {
		if i >= inner.H {
			break
		}
		col := inner.X
		for _, seg := range segs {
			col = canvas.PlaceText(s, col, inner.Y+i, seg.Text, seg.Style, maxX)
			if col >= maxX {
				break
			}
		}
	}
	return cur
}

// custom

// fullBasename returns name with opts.Ext appended if missing — the form used as
// the guard key (which keys on the full basename incl. extension).
func (m *Model) fullBasename(name string) string {
	if strings.HasSuffix(name, m.opts.Ext) {
		return name
	}
	return name + m.opts.Ext
}

func (m *Model) getFilePath(filename string) string {
	if !strings.HasSuffix(filename, m.opts.Ext) {
		filename = fmt.Sprint(filename, m.opts.Ext)
	}
	return filepath.Join(m.opts.Dir, filename)
}

func (m *Model) getFiles() ([]os.FileInfo, error) {
	dir, err := os.Open(m.opts.Dir)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	entries, err := dir.Readdir(-1)
	if err != nil {
		return nil, err
	}

	filtered := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), m.opts.Ext+WipMarker) || !strings.HasSuffix(e.Name(), m.opts.Ext) {
			continue
		}
		filtered = append(filtered, e)
	}
	// Newest first, and STABLE: entries sharing a ModTime keep the order readdir
	// returned them in (coarse-timestamp filesystems collapse mtimes, and an
	// unstable sort would reshuffle those rows between listings).
	slices.SortStableFunc(filtered, func(a, b os.FileInfo) int {
		return b.ModTime().Compare(a.ModTime())
	})

	return filtered, nil
}

// ListFiles spawns a tracked goroutine that reads the directory and posts the
// resulting ListOP IOMsg (carrying the file names) via PostCritical. The IOMsg
// carries m.id so the Update arm's id-epoch filter keeps it.
func (m *Model) ListFiles() {
	id := m.id
	m.poster.Go(func(ctx context.Context) {
		msg := IOMsg{
			id:    id,
			op:    ListOP,
			files: []string{},
		}

		files, err := m.getFiles()
		if err != nil {
			_ = m.poster.PostCritical(ctx, msg.withErr(err))
			return
		}

		// Built once for the whole listing, not once per row: this is where the
		// snapshot wiring's single .inuse directory scan happens.
		var styler RowStyler
		if m.opts.NewRowStyler != nil {
			styler = m.opts.NewRowStyler()
		}

		for _, f := range files {
			name := f.Name()
			pos := strings.LastIndex(name, m.opts.Ext)
			msg.files = append(msg.files, name[:pos])

			var style uv.Style
			if styler != nil {
				style = styler(name)
			}
			msg.styles = append(msg.styles, style)
		}

		_ = m.poster.PostCritical(ctx, msg)
	})
}

type CloseF func() error

func (m *Model) getReader(filename string) (io.Reader, CloseF, error) {
	f, err := os.OpenFile(m.getFilePath(filename), os.O_RDONLY, filePerm)
	if err != nil {
		return nil, nil, err
	}

	return f, f.Close, nil
}

// preview spawns a tracked goroutine that builds the preview for filename and
// posts a Preview IOMsg via PostCritical. The IOMsg carries m.id for the
// id-epoch filter.
func (m *Model) preview(filename string) {
	id := m.id
	m.poster.Go(func(ctx context.Context) {
		msg := IOMsg{
			id:    id,
			op:    Preview,
			files: []string{filename},
		}

		if m.opts.PreviewFn != nil {
			if p, err := m.opts.PreviewFn(m.getFilePath(filename)); err == nil {
				msg.preview = p
				_ = m.poster.PostCritical(ctx, msg)
				return
			}
		}

		// Fall back to raw file content.
		r, closeF, err := m.getReader(filename)
		if err != nil {
			_ = m.poster.PostCritical(ctx, msg.withErr(err))
			return
		}
		defer func() { _ = closeF() }()

		content, err := ReadAll(r)
		if err != nil {
			_ = m.poster.PostCritical(ctx, msg.withErr(err))
			return
		}
		// Preview rendering reads msg.preview (cell-native segments), so the raw
		// fallback must convert its content into segments here; otherwise
		// OnFileIO stores a nil preview and the pane renders blank.
		msg.preview = rawPreview(content)

		_ = m.poster.PostCritical(ctx, msg)
	})
}

// rawPreview converts raw file content into one preview line per text line as
// unstyled segments — the fallback preview when no PreviewFn is configured.
func rawPreview(content string) [][]list.Segment {
	lines := strings.Split(content, "\n")
	out := make([][]list.Segment, len(lines))
	for i, line := range lines {
		out[i] = list.PlainItem(line)
	}
	return out
}

// ReadFile sets m.loading on-loop (so it stays set until the read completes),
// then spawns one tracked goroutine that performs the read and, in order:
//  1. PostCritical the read-result (whatever m.read returns; no id, consumed
//     upstream cross-component — e.g. queryeditor's QueryLoadMsg).
//  2. PostCritical the terminal ReadOP IOMsg (carries m.id; clears m.loading).
//
// Both posts happen in the same goroutine, sequentially, so loading stays true
// for the whole read and the terminal IOMsg always follows the read-result.
func (m *Model) ReadFile(filename string) {
	m.loading = true
	id := m.id
	m.poster.Go(func(ctx context.Context) {
		// The terminal ReadOP IOMsg (clears m.loading) is posted on every path,
		// including the getReader error path, so loading never stays stuck true.
		defer func() {
			_ = m.poster.PostCritical(ctx, IOMsg{id: id, op: ReadOP, files: []string{filename}})
		}()

		path := m.getFilePath(filename)
		r, closeF, err := m.getReader(filename)
		if err != nil {
			_ = m.poster.PostCritical(ctx, m.ioErr(err))
			return
		}
		defer func() { _ = closeF() }()

		result := m.read(r, path)
		_ = m.poster.PostCritical(ctx, result)
	})
}

func (m *Model) ReadFileUnderCursor() {
	if m.list.Len() < 1 {
		return
	}
	m.ReadFile(m.list.GetItemUnderCursor())
}

// WriteFile spawns one tracked goroutine that runs writeAtomic first, then, in
// order:
//  1. PostCritical the write-result (whatever m.write returns).
//  2. PostCritical the terminal WriteOP IOMsg (carries m.id; drives the
//     WriteOP -> ListFiles refresh).
//
// Both posts happen in the same goroutine, sequentially, so the WriteOP-driven
// ListFiles refresh only runs after the file exists on disk.
func (m *Model) WriteFile(filename string) {
	id := m.id
	m.poster.Go(func(ctx context.Context) {
		result := m.writeAtomic(filename)
		_ = m.poster.PostCritical(ctx, result)
		_ = m.poster.PostCritical(ctx, IOMsg{id: id, op: WriteOP, files: []string{filename}})
	})
}

// writeAtomic writes to a temp file then renames to the final path.
// This prevents corruption when two concurrent writes target the same file.
func (m *Model) writeAtomic(filename string) uv.Event {
	finalPath := m.getFilePath(filename)

	tmp, err := os.CreateTemp(m.opts.Dir, ".write-*.tmp")
	if err != nil {
		return m.ioErr(err)
	}
	tmpPath := tmp.Name()

	result := m.write(tmp)
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return m.ioErr(err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return m.ioErr(err)
	}
	return result
}

func (m *Model) WriteFileUnderCursor() {
	if m.list.Len() < 1 {
		return
	}
	m.WriteFile(m.list.GetItemUnderCursor())
}

// DeleteFile spawns a tracked goroutine that removes the file and posts a
// DeleteOP IOMsg (carrying m.id) via PostCritical. On success, OnDeleted (if
// set) is invoked with the full basename so peers can drop it from their list.
func (m *Model) DeleteFile(filename string) {
	id := m.id
	base := m.fullBasename(filename)
	m.poster.Go(func(ctx context.Context) {
		msg := IOMsg{id: id, op: DeleteOP, files: []string{filename}}
		if err := os.Remove(m.getFilePath(filename)); err != nil {
			_ = m.poster.PostCritical(ctx, msg.withErr(err))
			return
		}
		_ = m.poster.PostCritical(ctx, msg)
		if m.opts.OnDeleted != nil {
			m.opts.OnDeleted(ctx, base) // spec §3.3: peers drop it from their list
		}
	})
}

func (m *Model) DeleteFileUnderCursor() {
	if m.list.Len() < 1 {
		return
	}
	cursor := m.list.GetItemUnderCursor() // extension-stripped
	if m.opts.OnDelete != nil {
		if err := m.opts.OnDelete(m.fullBasename(cursor)); err != nil {
			m.logger.Warn("delete refused", "file", cursor, logging.KeyError, err)
			msgs.PostAsync(m.poster, msgs.ShowDialogMsg{
				Title:   "Cannot delete",
				Message: err.Error(),
				Buttons: []msgs.DialogButton{{Label: "OK"}},
			})
			return
		}
	}
	m.DeleteFile(cursor)
}

// RenameFile renames the file off-loop and posts a MoveOP IOMsg. It uses an
// atomic no-clobber rename (snapshot.RenameNoClobber) so colliding with an
// existing destination returns snapshot.ErrRenameDestExists on the IOMsg rather
// than silently overwriting. A source-cleanup failure still creates the
// destination, so it is reported as a warning and completes the rename
// lifecycle; the source remains listed for later cleanup.
func (m *Model) RenameFile(oldFilename, newFilename string) {
	id := m.id
	oldBase := m.fullBasename(oldFilename)
	newBase := m.fullBasename(newFilename)
	m.poster.Go(func(ctx context.Context) {
		err := renameNoClobber(m.getFilePath(oldFilename), m.getFilePath(newFilename))
		var cleanupErr *snapshot.RenameCleanupError
		var warning error
		if errors.As(err, &cleanupErr) {
			warning = err
			err = nil
		}
		if snapshot.IsExistErr(err) {
			err = fmt.Errorf("%q already exists", newBase)
		}
		_ = m.poster.PostCritical(ctx, IOMsg{id: id, op: MoveOP, err: err, wrn: warning, files: []string{oldFilename}})
		if err == nil && m.opts.OnRenamed != nil {
			m.opts.OnRenamed(ctx, oldBase, newBase)
		}
	})
}

func (m *Model) IsFocused() bool {
	return m.list.IsFocused()
}

// Blur drops the file browser's input focus without posting msgs.FocusMsg. The
// caller owns the model's focus gate. Used when focus is taken away out-of-band
// (e.g. a click that switches tabs).
func (m *Model) Blur() {
	m.list.Blur()
}

// GetItemUnderCursor returns the name of the file currently under the cursor,
// without the file extension.
func (m *Model) GetItemUnderCursor() string {
	return m.list.GetItemUnderCursor()
}

// SelectByName moves the list cursor onto the row whose basename (sans extension,
// as the list stores it) equals name, returning whether a match was found against
// the CURRENTLY loaded rows. Pass the basename WITHOUT the extension, matching how
// ListFiles stores item text and GetItemUnderCursor reports it. For a row that may
// not be listed yet (a just-written file), use SelectByNameAfterRefresh instead.
func (m *Model) SelectByName(name string) bool {
	return m.list.SelectByName(name)
}

// SelectByNameAfterRefresh refreshes the directory listing and, once the new rows
// are populated (ListFiles is async — the items land on the resulting ListOP
// IOMsg, not when this returns), moves the cursor onto the row named name. Pass the
// basename WITHOUT the extension. Used to land the cursor on a file that was just
// written to disk outside the list (e.g. a freshly dispatched archive), where a
// synchronous SelectByName would miss because the row is not listed yet.
func (m *Model) SelectByNameAfterRefresh(name string) {
	m.pendingSelect = name
	m.ListFiles()
}

func ReadAll(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
