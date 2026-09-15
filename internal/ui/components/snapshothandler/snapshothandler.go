package snapshothandler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/sessionbus"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/filehandler"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
)

// Compile-time assertions pinning EVERY seam this wrapper forwards. The
// wrapper holds its *filehandler.Model rather than embedding it, so opting into
// a seam is a deliberate per-tab decision (see docs/dev/design/components.md).
// The cost of that choice is that a rename or
// signature change on a seam silently turns the forwarder into an ordinary
// method and the tab loses the input, with no build error. This block is what
// turns that into a build failure, so it must list every seam the forwarders
// below implement, not just the obvious two.
var (
	_ component.Component            = (*Model)(nil)
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
	bundle         deps.Bundle
	kh             *keys.Handler
	alwaysHandleKH *keys.Handler
	fh             *filehandler.Model
	logger         *slog.Logger
	poster         msgs.Poster
	evict          func(basename string)  // injected by ui; clears backing iff it is the current one
	broadcaster    sessionbus.Broadcaster // injected by ui; nil disables cross-session notify
}

// validateSnapshotRename accepts only a safe finalized basename outside the
// reserved latest and automatic namespaces.
func validateSnapshotRename(name string) error {
	base, ok := sessionbus.SanitizeBasename(name)
	if !ok {
		return errors.New("enter a plain filename (no /, \\ or ..)")
	}
	if base == "latest"+snapshot.FileExt {
		return errors.New(`"latest" is a reserved snapshot name`)
	}
	if strings.HasPrefix(base, "auto-") {
		return errors.New(`names beginning with "auto-" are reserved for automatic snapshots`)
	}
	return nil
}

// New constructs a Model for managing snapshots. It replaces the former
// NewSnapshotHandler (stutter elimination).
func New(bundle deps.Bundle) *Model {
	dir, err := snapshot.Dir()
	if err != nil {
		panic(err)
	}

	logger := slog.Default().With(logging.KeyComponent, "snapshothandler")
	m := &Model{bundle: bundle, logger: logger}

	fhOpts := filehandler.FileOpts{
		Dir: dir,
		Ext: snapshot.FileExt,
		PreviewFn: func(path string) ([][]list.Segment, error) {
			c, err := snapshot.OpenContainerReadOnly(path)
			if err != nil {
				return nil, err
			}
			defer func() { _ = c.Close() }()
			return loadPreview(bundle, c)
		},
		// OnDelete guards explicit delete: it uses InUseByOther so the user can
		// delete (and evict) their OWN backing.
		OnDelete:  m.guardDelete,
		Validator: validateSnapshotRename,
		// OnRenamed runs inside RenameFile's poster.Go body = OFF-LOOP, so the
		// broadcast and the self-post are the allowed direct must-deliver paths.
		// The closure captures m, so a later SetBroadcaster/SetPoster is visible.
		OnRenamed: func(ctx context.Context, oldBase, newBase string) {
			if m.broadcaster != nil {
				_ = m.broadcaster.Broadcast(ctx, sessionbus.MethodSnapshotRenamed,
					sessionbus.SnapshotRenamedParams{From: oldBase, To: newBase})
			}
			// Self-correct via the same loop handler remote sessions use.
			if m.poster != nil {
				_ = m.poster.PostCritical(ctx, msgs.SnapshotRenamedMsg{From: oldBase, To: newBase})
			}
		},
		// OnDeleted runs inside DeleteFile's poster.Go body = OFF-LOOP. spec §3.3:
		// a successful TUI delete of an unheld snapshot tells peers to drop it.
		OnDeleted: func(ctx context.Context, _ string) {
			if m.broadcaster != nil {
				_ = m.broadcaster.Broadcast(ctx, sessionbus.MethodSnapshotsDirty, sessionbus.SnapshotsDirtyParams{})
			}
		},
		// NewRowStyler colors each listed snapshot by lock ownership: held by this
		// session → green (success), held only by another live session → yellow
		// (warning), free → default. self-priority: when both self and others hold
		// it, the row is green.
		NewRowStyler: m.newSnapshotRowStyler,
	}
	fhRead := func(_ io.Reader, path string) uv.Event {
		c, err := snapshot.OpenContainerReadOnly(path)
		if err != nil {
			return msgs.SnapshotRestoreMsg{Err: err}
		}
		defer func() { _ = c.Close() }()

		s, err := snapshot.LoadState(c)
		if err != nil {
			return msgs.SnapshotRestoreMsg{Err: err}
		}
		if err := snapshot.ValidateInstanceSnapshotCRNs(s.InstancePickerSnapshot.Instances); err != nil {
			return msgs.SnapshotRestoreMsg{Err: err}
		}

		logger.Info("snapshot restored", "path", path)
		return msgs.SnapshotRestoreMsg{Snapshot: s, BackingPath: path}
	}
	// Write is never triggered from snapshothandler keybinds; the real save
	// logic lives in ui.saveSnapshot. This no-op satisfies the filehandler
	// WriteF contract.
	fhWrite := func(_ io.Writer) uv.Event { return nil }

	m.fh = filehandler.New(bundle, fhOpts, fhRead, fhWrite)
	// A click on the already-selected snapshot row restores it — the click
	// analogue of pressing the Accept (load) keybind on the cursor row.
	m.fh.WithActivate(m.fh.ReadFileUnderCursor)
	bindKeyhandlersToModel(m)

	return m
}

// SetPoster injects the runtime poster and forwards it to the filehandler,
// which uses it to spawn tracked goroutines for its IO methods. Called once at
// startup from ui.Model.SetPoster.
func (m *Model) SetPoster(p msgs.Poster) {
	m.poster = p
	m.fh.SetPoster(p)
}

// newSnapshotRowStyler is the filehandler NewRowStyler hook. It scans the lock
// directory ONCE per listing and returns the per-row colorer, which is then a
// pure map lookup — asking snapshot.Holders per row would rescan the directory
// (and re-run its MkdirAll) once per row on every refresh.
//
// The returned styler maps a snapshot file (full basename incl. FileExt) to its
// row color by lock ownership — this process holds it → SuccessColor (green),
// only other live session(s) hold it → WarningColor (yellow), otherwise the zero
// style (unstyled). Self-priority: when both self and others hold the file,
// green wins. A failed scan fails safe to a nil styler, i.e. every row unstyled.
// Runs OFF the loop goroutine (inside ListFiles' worker); it only reads the
// immutable config palette and scans the lock directory.
func (m *Model) newSnapshotRowStyler() filehandler.RowStyler {
	ix, err := snapshot.HoldersIndex()
	if err != nil {
		m.logger.Warn("in-use scan failed", logging.KeyError, err)
		return nil
	}
	self := os.Getpid()
	return func(fullBasename string) uv.Style {
		holders := ix.Holders(fullBasename)
		if len(holders) == 0 {
			return uv.Style{}
		}
		if slices.Contains(holders, self) {
			// self-priority when both self and others hold it
			return uv.Style{Fg: m.bundle.Config.Style.SuccessColor.Color}
		}
		return uv.Style{Fg: m.bundle.Config.Style.WarningColor.Color}
	}
}

// guardDelete is the explicit-delete gate: refuse if another live instance holds
// name; otherwise evict our own backing (no-op unless it is the current one) and
// permit. Runs on the loop goroutine.
func (m *Model) guardDelete(name string) error {
	other, err := snapshot.InUseByOther(name)
	if err != nil {
		return fmt.Errorf("in-use check failed for %q: %w", name, err)
	}
	if other {
		return fmt.Errorf("%q is in use by another lognav instance", name)
	}
	if m.evict != nil {
		m.evict(name)
	}
	return nil
}

// SetEvictor injects the callback the explicit-delete guard uses to drop this
// session's backing when the user deletes their own current backing file. Wired
// by ui to instancepicker.EvictIfCurrentBacking. Runs on the loop goroutine.
func (m *Model) SetEvictor(evict func(basename string)) { m.evict = evict }

// SetBroadcaster injects the cross-session notifier used by the rename/delete
// hooks. Wired once at startup by ui.Model (which owns the single Broadcaster).
// A nil broadcaster disables cross-session notification (the hooks no-op).
func (m *Model) SetBroadcaster(b sessionbus.Broadcaster) { m.broadcaster = b }

// requestQuit requests a clean exit. It is the quit hook wired into the Quit
// keybind; keybind actions run on the loop goroutine, hence msgs.RequestQuit.
// It stays a method because the binding needs a func value.
func (m *Model) requestQuit() {
	msgs.RequestQuit(m.poster)
}

func (m *Model) Init() {
	m.fh.Init()
}

// OnSaveSnapshot refreshes the snapshot file list after an automatic save.
// ListFiles is void (spawns a tracked poster goroutine). Mirrors the previous
// Update arm; the root calls it directly (B1 direct-dispatch).
func (m *Model) OnSaveSnapshot(_ msgs.SaveSnapshotMsg) {
	m.fh.ListFiles()
}

// OnManualSaveDone refreshes the snapshot file list after a manual save. The
// save writes via os.Rename in ui.saveSnapshot, bypassing the filehandler's own
// WriteOP, so an overwrite of an existing name would otherwise keep its stale
// cached preview — drop it first so the refresh recomputes it. ListFiles is void
// (spawns a tracked poster goroutine).
func (m *Model) OnManualSaveDone(msg msgs.ManualSaveDoneMsg) {
	m.fh.InvalidatePreview(msg.Name)
	m.fh.ListFiles()
}

// OnSnapshotsDirty refreshes the list + coloring after a cross-session change.
func (m *Model) OnSnapshotsDirty(_ msgs.SnapshotsDirtyMsg) { m.fh.ListFiles() }

// OnSnapshotRenamed refreshes the list display for a rename (the backing
// self-correction is instancepicker's job). Drops both cached previews.
func (m *Model) OnSnapshotRenamed(msg msgs.SnapshotRenamedMsg) {
	m.fh.InvalidatePreview(msg.From)
	m.fh.InvalidatePreview(msg.To)
	m.fh.ListFiles()
}

// OnFileIO forwards a filehandler IO result to the owned filehandler. The id
// guard inside the filehandler makes a non-matching IOMsg a no-op. Mirrors the
// previous Update arm.
func (m *Model) OnFileIO(msg filehandler.IOMsg) {
	m.fh.OnFileIO(msg)
}

// OnPaste forwards a bracketed-paste event to the owned filehandler. Mirrors
// the previous Update paste arm. The root calls it directly.
func (m *Model) OnPaste(ev uv.Event) {
	m.fh.OnPaste(ev)
}

// HandleKey dispatches a key event through snapshothandler's precedence:
//  1. alwaysHandleKH (snapshot save shortcut — fires regardless of fh focus).
//  2. m.kh (quit) only when fh is NOT focused, so typing `q` into the fuzzy
//     filter still filters instead of quitting.
//  3. Unconditional fall-through to m.fh.HandleKey — all unconsumed keys are
//     forwarded to the filehandler regardless of whether it is focused, matching
//     the original m.fh.Update(msg) at :113.
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if m.alwaysHandleKH.Run(ev) {
		return component.KeyHandled
	}
	if !m.fh.IsFocused() && m.kh.Run(ev) {
		return component.KeyHandled
	}
	return m.fh.HandleKey(ev)
}

// OnMouseClick implements component.MouseTarget by forwarding the click to the
// owned filehandler. Clicks always select (move the cursor); activation
// (ReadFileUnderCursor — the snapshot restore) requires OnMouseDoubleClick.
func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	return m.fh.OnMouseClick(x, y, btn)
}

// OnMouseDoubleClick implements component.DoubleClickTarget by forwarding the
// double-click to the owned filehandler. A double-click on a left-half snapshot
// row moves the cursor to that row and fires the restore action (the activate
// hook); a right-half (preview) double-click returns false, matching
// filehandler's half-split.
func (m *Model) OnMouseDoubleClick(x, y int, btn uv.MouseButton) bool {
	return m.fh.OnMouseDoubleClick(x, y, btn)
}

// OnMousePaste implements component.MousePasteTarget by forwarding the
// middle-click to the owned filehandler (which routes the left-half list's
// fuzzy-filter paste).
func (m *Model) OnMousePaste(x, y int, content string) bool {
	return m.fh.OnMousePaste(x, y, content)
}

// OnMouseScroll implements component.ScrollTarget by forwarding a vertical wheel
// notch to the owned filehandler (which pans the snapshot list).
func (m *Model) OnMouseScroll(x, y, lines int) bool {
	return m.fh.OnMouseScroll(x, y, lines)
}

// OnMouseHover implements component.MouseHoverTarget by forwarding to the owned
// filehandler (which tints the hovered left-half snapshot row).
func (m *Model) OnMouseHover(x, y int) bool {
	return m.fh.OnMouseHover(x, y)
}

// ClearMouseHover drops hover retained by the owned filehandler.
func (m *Model) ClearMouseHover() bool {
	return m.fh.ClearMouseHover()
}

// OpenContextMenu implements component.ContextMenuOpener by delegating to the
// owned filehandler, which opens the Rename/Delete menu anchored at the selected
// snapshot row.
func (m *Model) OpenContextMenu() { m.fh.OpenContextMenu() }

// OnMouseRight implements component.SecondaryMouseTarget by forwarding the
// right-click to the owned filehandler, which opens the Rename/Delete menu at the
// pointer.
func (m *Model) OnMouseRight(x, y int) bool { return m.fh.OnMouseRight(x, y) }

// Component

func (m *Model) GetKeybinds() []keys.Binding {
	b := append([]keys.Binding(nil), m.alwaysHandleKH.GetKeybinds()...)
	return append(b, filehandler.DecorateHints(m.fh.GetKeybinds())...)
}

func (m *Model) SetRect(r component.Rect) {
	m.fh.SetRect(r)
}

func (m *Model) Draw(s component.Screen) *component.Cursor {
	return m.fh.Draw(s)
}
