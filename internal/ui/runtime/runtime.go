package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/sessionbus"
	"github.com/bevicted/lognav/internal/ui"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// tickMsg is the spine's shared redraw tick (droppable). Receiving it forces a
// redraw so live RenderTimer values advance. It carries no data.
type tickMsg struct{}

// runtime holds the spine state owned by the single loop goroutine.
type runtime struct {
	model  *ui.Model
	scr    *uv.TerminalScreen
	poster *eventPoster
	events chan uv.Event
	//nolint:containedctx // the loop goroutine owns runCtx to gate teardown, PostCritical, and the $EDITOR hand-off; not a per-call injection.
	runCtx      context.Context
	logger      *slog.Logger
	prevState   *term.State
	cr          cancelreader.CancelReader
	readDone    chan struct{}
	enableMouse bool
	enableHover bool
	w, h        int
	quit        bool

	// notifyPayload is the pre-rendered escape sequence written on msgs.NotifyMsg
	// (fetch-complete notification). Computed once in Run from Core.NotifyStyle and
	// $TMUX (style + tmux-ness are fixed for the session); empty disables the write.
	notifyPayload string

	// Shared redraw ticker (R4). tickerRunning/tickerStop are owned by the loop
	// goroutine (mutated only in syncTicker, which runs on-loop). The ticker
	// goroutine posts the droppable tickMsg off-loop. tickInterval is the
	// configured redraw cadence (Core.RedrawIntervalMs, clamped), read-only after
	// Run sets it.
	tickerRunning bool
	tickerStop    chan struct{}
	tickInterval  time.Duration

	// resizeDirty is set when a uv.WindowSizeEvent was handled in the current
	// drain batch. After the batch's draw it triggers a second re-armed full
	// clear+repaint to work around uv's lazy curbuf resize on a grow (see
	// reclearAfterResize). Owned by the loop goroutine.
	resizeDirty bool

	// ctrl+c double-press quit tracking. ctrl+c is the Keys.Clear binding, so a
	// lone press belongs to the model; the loop counts presses instead and exits
	// on the second one inside ctrlCWindow (derived from Core.DoubleClickMs in
	// Run). Owned by the loop goroutine. now is the wall clock, overridable in
	// tests; a directly-constructed runtime leaves it nil and falls back to
	// time.Now (see clock).
	ctrlCWindow time.Duration
	ctrlCAt     time.Time
	ctrlCArmed  bool
	now         func() time.Time
}

// newRuntime builds the loop-owned state from config. Split out of Run because
// Run cannot be called from a test at all (its first statement needs a TTY),
// which would otherwise leave every config-derived tuning value — redraw
// cadence, notify payload, ctrl+c quit window — unreachable from tests.
// Everything with a lifetime (runCtx, poster, reader) is still wired by Run.
func newRuntime(
	bundle deps.Bundle,
	model *ui.Model,
	scr *uv.TerminalScreen,
	prevState *term.State,
	logger *slog.Logger,
	inTmux bool,
) *runtime {
	return &runtime{
		model:         model,
		scr:           scr,
		events:        make(chan uv.Event), // OWNED, UNBUFFERED, NEVER CLOSED
		logger:        logger,
		prevState:     prevState,
		enableMouse:   bundle.Config.Core.EnableMouse,
		enableHover:   bundle.Config.Core.EnableHover,
		tickInterval:  redrawInterval(bundle.Config.Core.RedrawIntervalMs),
		notifyPayload: notifyPayload(bundle.Config.Core.NotifyStyle, inTmux),
		ctrlCWindow:   doublePressWindow(bundle.Config.Core.DoubleClickMs),
		now:           time.Now,
	}
}

// clock reads the current time through the injectable now hook, falling back to
// time.Now for a directly-constructed runtime (tests that set no clock).
func (rt *runtime) clock() time.Time {
	if rt.now == nil {
		return time.Now()
	}
	return rt.now()
}

// redrawInterval converts the configured Core.RedrawIntervalMs to a duration,
// clamping to a sane range. 0 (unset) falls back to the 33ms (~30fps) default;
// values are bounded to [8ms, 1000ms] so a misconfiguration can neither spin the
// loop nor stall live updates.
func redrawInterval(ms uint16) time.Duration {
	return (config.Core{RedrawIntervalMs: ms}).RedrawInterval()
}

// doublePressWindow converts the configured Core.DoubleClickMs to the ctrl+c
// double-press window, sharing the mouse gesture's timing so both "press twice"
// interactions feel the same. 0 disables the MOUSE double-click, but must never
// make quitting impossible, so here it falls back to the same 400ms default;
// other values are clamped to [50ms, 2000ms] as they are for the mouse.
func doublePressWindow(ms uint16) time.Duration {
	switch {
	case ms == 0:
		ms = 400
	case ms < 50:
		ms = 50
	case ms > 2000:
		ms = 2000
	}
	return time.Duration(ms) * time.Millisecond
}

// notifyFetchDoneText is the message shown by the osc* desktop-notification
// styles. The bell style carries no text (it is a plain audible beep).
const notifyFetchDoneText = "lognav: fetch complete"

// tmuxWrap DCS-passthrough-wraps an OSC escape sequence when running inside tmux
// (which otherwise strips OSC; the user's tmux must also have allow-passthrough
// on). Outside tmux the sequence is returned unchanged.
func tmuxWrap(seq string, inTmux bool) string {
	if inTmux {
		return ansi.TmuxPassthrough(seq)
	}
	return seq
}

// notifyPayload renders the fetch-complete notification escape sequence for the
// configured style, wrapping the osc* desktop notifications in tmux DCS
// passthrough when running inside tmux. The bell is a raw BEL left unwrapped so
// tmux routes it through its own bell-action. An unknown style (UnmarshalYAML
// rejects these, so this is only the zero value) falls back to the bell.
func notifyPayload(style config.NotifyStyle, inTmux bool) string {
	switch style {
	case config.NotifyStyleOSC9:
		return tmuxWrap(ansi.Notify(notifyFetchDoneText), inTmux)
	case config.NotifyStyleOSC777:
		// OSC 777 ; notify ; <title> ; <body> BEL. Hand-rolled: charmbracelet/x/ansi
		// ships no OSC 777 helper. The title must not contain ';' (the receiver
		// splits at the first one), so keep it the bare app name.
		return tmuxWrap("\x1b]777;notify;lognav;fetch complete\x07", inTmux)
	case config.NotifyStyleOSC99:
		return tmuxWrap(ansi.DesktopNotification(notifyFetchDoneText), inTmux)
	case config.NotifyStyleBell:
		return "\a"
	default:
		return "\a"
	}
}

// Run hosts ui.Model on the hand-written ultraviolet runtime spine and
// replaces bubbletea's Program.Run. It owns raw mode, the screen, the
// reader on an owned never-closed channel, the winch goroutine, the event
// loop (dispatching uv events directly to the model's typed entry points), the
// sessionbus server lifecycle, and the LIFO teardown.
func Run(ctx context.Context, bundle deps.Bundle, model *ui.Model) error {
	logger := slog.Default()

	prevState, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return fmt.Errorf("make raw: %w", err)
	}

	scr := uv.NewTerminalScreen(os.Stdout, os.Environ())
	rt := newRuntime(bundle, model, scr, prevState, logger, os.Getenv("TMUX") != "")
	// Installed before enterModes so ANY panic from here on restores the terminal
	// and lands in the log file (panicguard.go). Teardown below is unreachable on
	// a panic — recoverLoop re-panics by design.
	defer rt.recoverLoop()
	rt.enterModes()
	if c := bundle.Config.Style.Bg; c.IsSet() {
		_ = scr.SetBackgroundColor(c.Color)
	}
	if c := bundle.Config.Style.Fg; c.IsSet() {
		_ = scr.SetForegroundColor(c.Color)
	}
	_ = scr.Flush()

	runCtx, cancelRun := context.WithCancel(ctx)
	rt.runCtx = runCtx
	rt.poster = newEventPoster(runCtx, rt.events)

	if err := rt.startReader(); err != nil {
		rt.exitModes()
		_ = term.Restore(os.Stdin.Fd(), prevState)
		cancelRun()
		return fmt.Errorf("cancel reader: %w", err)
	}

	// Must-deliver send (never drop logs or stream events) — backed by PostCritical.
	send := func(m uv.Event) { _ = rt.poster.PostCritical(runCtx, m) }
	model.SetSend(send)
	model.SetPoster(rt.poster) // R4: expose poster.Go to components
	// Cross-session notifier. It is ordinary runtime infrastructure and MUST
	// follow SetPoster so the instancepicker notifyDirty closure binds a non-nil
	// poster.
	model.SetBroadcaster(sessionbus.NewClient())
	session := sessionbus.NewServer(send)
	session.Start(runCtx)

	rt.startWinch() // injects the initial WindowSizeEvent + each resize (winch_unix.go)

	// Seed initial state BEFORE the loop. ApplyEnv ingests the process
	// environment (a channel send would deadlock — no consumer yet); Init runs
	// directly (its IO is off-loop via the poster, harmless before the loop).
	rt.model.ApplyEnv(os.Environ())
	rt.model.Init()
	// Seed the shared ticker once so a launch-into-fetch (e.g. snapshot resume or
	// a resumed snapshot fetch starts the redraw ticker immediately.
	rt.syncTicker(rt.model.IsAnimating())

	rt.loop()

	// Teardown — LIFO (master §5.4).
	cancelRun()
	// Explicitly stop the shared ticker (harmless: cancelRun already cancels its
	// goroutine via ctx.Done; this just closes the stop chan before Wait joins it).
	rt.syncTicker(false)
	rt.stopReader()
	rt.exitModes()
	_ = term.Restore(os.Stdin.Fd(), prevState)
	rt.drainPoster(model.CancelQueries, 2*time.Second)

	heldBacking := model.HeldManagedBacking() // capture before Close clears it

	var errOut error
	if cerr := model.Close(); cerr != nil { // releases the .inuse guard
		logger.Error("ui close failed", logging.KeyComponent, "runtime", logging.KeyError, cerr)
		errOut = cerr
	}
	// Release-notify peers (synchronous — poster is drained, so no poster.Go).
	// Runs AFTER Close so peers re-scanning Holders no longer see our (now-removed)
	// claim and recolor the snapshot locked -> free. A direct Broadcast touches no
	// poster, so it is safe after drainPoster/Wait (a poster.Go here would be a
	// use-after-drain hazard).
	if heldBacking {
		// Block-scoped so defer cancel() fires here (not at Run return) — panic-safe
		// without leaking the timeout ctx until the function unwinds.
		func() {
			notifyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if berr := model.BroadcastSnapshotsDirtySync(notifyCtx); berr != nil {
				logger.Warn("exit release-notify failed", logging.KeyComponent, "runtime", logging.KeyError, berr)
			}
		}()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if cerr := session.Close(shutdownCtx); cerr != nil {
		logger.Error("sessionbus server close failed", logging.KeyComponent, "runtime", logging.KeyError, cerr)
	}
	cancel()
	return errOut
}

// mouseModeFor selects the terminal mouse tracking mode. With mouse disabled no
// mode is set (set=false). With mouse enabled the default is MouseModeDrag
// (?1002h button-event tracking — clicks, wheel, and motion WHILE a button is
// held). The experimental hover feature needs bare-hover motion (pointer moves
// with no button held), which only ?1003h any-event tracking reports, so hover
// upgrades the mode to MouseModeMotion. The upgrade is gated on enableHover so
// the any-event motion firehose (one report per pointer cell, each forcing a
// redraw) is confined to sessions that opted in.
func mouseModeFor(enableMouse, enableHover bool) (uv.MouseMode, bool) {
	if !enableMouse {
		return uv.MouseModeNone, false
	}
	if enableHover {
		return uv.MouseModeMotion, true
	}
	return uv.MouseModeDrag, true
}

// enterModes buffers alt-screen, bracketed paste, mouse (if enabled), focus
// events, and keyboard enhancements, then flushes them. Reused by resume.
func (rt *runtime) enterModes() {
	_ = rt.scr.EnterAltScreen()
	_ = rt.scr.EnableBracketedPaste()
	if mode, set := mouseModeFor(rt.enableMouse, rt.enableHover); set {
		_ = rt.scr.SetMouseMode(mode)
	}
	_, _ = rt.scr.WriteString(ansi.SetModeFocusEvent)
	_ = rt.scr.SetKeyboardEnhancements(&uv.KeyboardEnhancements{DisambiguateEscapeCodes: true})
	_ = rt.scr.Flush()
}

// exitModes shows the cursor (Reset only restores it when it was not hidden,
// so a defocused-input HideCursor would otherwise leave the real terminal
// cursor hidden on exit), resets focus-event mode (untracked by Reset), then
// Reset()s the screen (exits alt-screen, disables mouse/paste/enhancements)
// and flushes. Reused by suspend and teardown.
func (rt *runtime) exitModes() {
	_ = rt.scr.ShowCursor()
	_, _ = rt.scr.WriteString(ansi.ResetModeFocusEvent)
	_ = rt.scr.Reset()
	_ = rt.scr.Flush()
}

// startReader opens a fresh cancelable reader on os.Stdin and streams events
// onto the owned channel until runCtx is cancelled or stopReader is called.
// Called once at startup and again on resume after an external process.
func (rt *runtime) startReader() error {
	cr, err := uv.NewCancelReader(os.Stdin)
	if err != nil {
		return err
	}
	rt.cr = cr
	reader := uv.NewTerminalReader(cr, os.Getenv("TERM"))
	rt.readDone = make(chan struct{})
	go func() {
		defer close(rt.readDone)
		_ = reader.StreamEvents(rt.runCtx, rt.events)
	}()
	return nil
}

// stopReader cancels the current reader and waits for its goroutine to exit,
// releasing os.Stdin so an external process (or shutdown) can claim it. The
// reader must be stopped around a $EDITOR hand-off — otherwise two readers
// race os.Stdin and the cancelreader is left unusable afterward.
func (rt *runtime) stopReader() {
	if rt.cr == nil {
		return
	}
	rt.cr.Cancel()
	select {
	case <-rt.readDone:
	case <-time.After(500 * time.Millisecond):
	}
	_ = rt.cr.Close()
}

// drainPoster cancels in-flight model work, then waits (bounded by timeout) for
// every tracked poster goroutine to exit. Cancelling FIRST is what keeps a
// quit-during-fetch snappy: each ICL query runs on a poster goroutine whose ctx
// is background-derived (StartQuery, by design, so Toggle->CancelQuery still
// works), NOT a child of runCtx — so cancelRun alone cannot abort a streaming
// query, and Wait would otherwise block the full timeout while the network read
// drains. cancelQueries aborts those queries so their goroutines (and the
// per-batch jq/search/IO workers they feed) return at once and Wait joins them in
// milliseconds. The timeout stays as a backstop for a genuinely wedged goroutine.
func (rt *runtime) drainPoster(cancelQueries func(), timeout time.Duration) {
	if cancelQueries != nil {
		cancelQueries()
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = rt.poster.Wait(waitCtx)
}

// loop is the single-threaded consumer (the sole writer of UI state). It
// drains all queued events after each wake, coalescing them into one draw.
func (rt *runtime) loop() {
	for !rt.quit {
		select {
		case <-rt.runCtx.Done():
			rt.quit = true
		case ev := <-rt.events:
			dirty := rt.dispatch(ev)
		drain:
			for {
				select {
				case ev2 := <-rt.events:
					if rt.dispatch(ev2) {
						dirty = true
					}
				default:
					break drain
				}
			}
			rt.syncTicker(rt.model.IsAnimating())
			if dirty && !rt.quit {
				rt.draw()
				rt.reclearAfterResize()
			}
		}
	}
}

// syncTicker starts/stops the shared redraw ticker (rt.tickInterval, from
// Core.RedrawIntervalMs) to match the model's
// animation predicate (a fetch in progress). Idle => stopped (no wakeups). The
// ticker goroutine is a tracked poster.Go (goleak-clean: exits on stop or runCtx).
// It posts the DROPPABLE tickMsg (never PostCritical, never on the loop goroutine
// — that would deadlock the unbuffered events channel while loop() is mid-dispatch
// and not receiving). syncTicker itself runs ON the loop goroutine (sole writer of
// tickerRunning/tickerStop) and must not post anything: it only starts/stops the
// ticker goroutine.
func (rt *runtime) syncTicker(want bool) {
	switch {
	case want && !rt.tickerRunning:
		rt.tickerStop = make(chan struct{})
		stop := rt.tickerStop
		rt.tickerRunning = true
		// Guard interval: a directly-constructed runtime (e.g. tests) may leave
		// tickInterval zero, which would panic time.NewTicker.
		iv := rt.tickInterval
		if iv <= 0 {
			iv = redrawInterval(0)
		}
		rt.poster.Go(func(ctx context.Context) {
			t := time.NewTicker(iv)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					rt.poster.Post(tickMsg{})
				case <-stop:
					return
				case <-ctx.Done():
					return
				}
			}
		})
	case !want && rt.tickerRunning:
		close(rt.tickerStop)
		rt.tickerRunning = false
	}
}

// dispatch routes one uv event directly (no coerce shim) and reports whether a
// redraw is needed. Terminal input has a typed entry point on the model;
// everything else (posted app events: msgs.*, instancepicker.*, logviewer.*,
// filehandler.*) flows through the default arm to model.Update. Ships
// mark-all-dirty (every handled event redraws) except quit; the
// visible-state-change refinement is deferred (master §5.2).
func (rt *runtime) dispatch(ev uv.Event) bool {
	switch e := ev.(type) {
	case msgs.QuitMsg:
		// Off-loop quit request posted by a keybind action (R5a B2a).
		rt.quit = true
		return false
	case tickMsg:
		// Shared redraw tick: mark dirty so live timers advance. Carries no
		// state, so it does not route to the model.
		return true
	case msgs.NotifyMsg:
		// Fetch-complete notification posted off-loop by the instancepicker — a
		// bell or a desktop-notification OSC, pre-rendered in Run.
		rt.writeSeq(rt.notifyPayload)
		return false
	case uv.WindowSizeEvent:
		rt.w, rt.h = e.Width, e.Height
		_ = rt.scr.Resize(e.Width, e.Height)
		rt.model.OnResize(e.Width, e.Height)
		rt.resizeDirty = true
		return true
	case uv.KeyPressEvent:
		if rt.trackCtrlC(e) {
			return false
		}
		rt.model.HandleKey(e)
		return true
	case uv.PasteEvent, uv.PasteStartEvent, uv.PasteEndEvent:
		rt.model.OnPaste(e)
		return true
	case uv.MouseClickEvent, uv.MouseMotionEvent, uv.MouseWheelEvent, uv.MouseReleaseEvent:
		// OnMouse returns whether the event needs a redraw: clicks/wheel always,
		// a motion only when the hover changed (gates idle-sweep repaints).
		return rt.model.OnMouse(e)
	case msgs.ExecRequest:
		return rt.runExec(e)
	default:
		// Posted app events (msgs.*, instancepicker.*, logviewer.*,
		// filehandler.IOMsg, etc.) reach the model's typed dispatcher.
		rt.model.Update(ev)
		return true
	}
}

// trackCtrlC advances the ctrl+c double-press gesture for one key event and
// reports whether that press quit the program (in which case the event is
// consumed and never reaches the model). A lone ctrl+c is only armed and falls
// through: ctrl+c is the Keys.Clear binding, so the first press belongs to
// whatever input has focus.
//
// This lives on the loop rather than in a model keybind so it is the raw-mode
// hard-exit guarantee (raw mode delivers no SIGINT): pressing ctrl+c twice
// always exits, whatever the model does — or fails to do — with the key.
func (rt *runtime) trackCtrlC(e uv.KeyPressEvent) bool {
	if e.Keystroke() != "ctrl+c" {
		// Anything typed in between breaks the pair, the same way a non-left
		// click breaks a double-click (ui.resetClickTracking).
		rt.ctrlCArmed = false
		return false
	}
	now := rt.clock()
	if rt.ctrlCArmed && now.Sub(rt.ctrlCAt) <= rt.ctrlCWindow {
		rt.quit = true
		return true
	}
	rt.ctrlCArmed = true
	rt.ctrlCAt = now
	return false
}

// runExec runs a msgs.ExecRequest hand-off and reports that a redraw is needed.
// It MUST run on the loop goroutine: terminal release/restore is serialized with
// the screen, and suspend/resume stop and restart the input reader so the child
// has sole ownership of os.Stdin. The Done result goes straight to the model's
// normal posted-event entry point, ui.Model.Update — called, not posted: we are
// mid-dispatch on the loop, so posting it would deadlock.
func (rt *runtime) runExec(e msgs.ExecRequest) bool {
	rt.suspend()
	// Wire the child to the real TTY (bubbletea's ExecProcess did this);
	// without it $EDITOR has no terminal and never appears.
	if e.Cmd.Stdin == nil {
		e.Cmd.Stdin = os.Stdin
	}
	if e.Cmd.Stdout == nil {
		e.Cmd.Stdout = os.Stdout
	}
	if e.Cmd.Stderr == nil {
		e.Cmd.Stderr = os.Stderr
	}
	runErr := e.Cmd.Run()
	rt.resume()
	if done := e.Done(runErr); done != nil {
		rt.model.Update(done)
	}
	return true
}

// writeSeq writes a pre-rendered control sequence to the screen writer and
// flushes it. An empty payload is a no-op (the feature is disabled). Used for the
// notification and progress-start sequences — both are cell-free control
// sequences that do not move the cursor, so they cannot desync the diff renderer
// and need no frame redraw.
func (rt *runtime) writeSeq(payload string) {
	if payload == "" {
		return
	}
	_, _ = rt.scr.WriteString(payload)
	_ = rt.scr.Flush()
}

// newComposeBuffer returns a per-frame uv compose buffer with GraphemeWidth
// width-measurement, matching lipgloss.NewCanvas's behavior (lipgloss forces
// ansi.GraphemeWidth; raw uv.NewScreenBuffer defaults to ansi.WcWidth, which
// mis-measures emoji/ZWJ/combining sequences and would drift overlay offsets).
func newComposeBuffer(w, h int) uv.ScreenBuffer {
	buf := uv.NewScreenBuffer(w, h)
	buf.Method = ansi.GraphemeWidth
	return buf
}

// draw composes the model into a fresh uv.ScreenBuffer and presents it.
//
// uv's cursor plumbing needs three calls to land the cursor in the same frame.
// The cursor is set BEFORE Display so Display's internal Flush queues the
// MoveTo into the renderer's buffer. But Display does not emit it: the renderer
// only writes into screen.buf on Render(), and only Flush() writes screen.buf
// to the terminal. So after Display we Render() (renderer buffer -> screen.buf)
// then Flush() (screen.buf -> terminal). Without the trailing Flush the cursor
// is positioned solely by the content write (end of line) and the real move
// arrives a frame late.
func (rt *runtime) draw() {
	if rt.w < 1 || rt.h < 1 {
		return
	}
	buf := newComposeBuffer(rt.w, rt.h)
	cur := rt.model.DrawTo(buf) // ScreenBuffer (value) satisfies component.Screen
	rt.applyCursor(cur)
	_ = rt.scr.Display(buf) // ScreenBuffer (value) satisfies uv.Drawable
	_ = rt.scr.Render()
	_ = rt.scr.Flush()
}

// reclearAfterResize repaints the screen a second time after a resize batch's
// draw, with a re-armed full clear, then clears resizeDirty. It is the workaround
// for a uv grow artifact: uv.TerminalScreen.Resize marks the renderer for a full
// clear but resizes the renderer's curbuf (its model of the physical screen) only
// LAZILY, at the end of the FIRST Render after the resize — AFTER that render's
// clearUpdate already ran against the old-width curbuf. On a grow this leaves
// curbuf's newly-exposed columns marked blank even though clearUpdate physically
// painted the prior frame there, so the next (clear=false) frame diffs those
// columns as already-blank and skips them, leaving stale content (the tmux-zoom
// stale-region report). The first draw has since resized curbuf to the new size,
// so re-arming the clear (scr.Resize with the current dims) and repainting now
// runs clearUpdate against a correctly-sized curbuf, leaving curbuf and the
// physical screen consistent. No-op outside a resize batch.
func (rt *runtime) reclearAfterResize() {
	if !rt.resizeDirty || rt.quit {
		rt.resizeDirty = false
		return
	}
	rt.resizeDirty = false
	if rt.w < 1 || rt.h < 1 {
		return
	}
	_ = rt.scr.Resize(rt.w, rt.h)
	rt.draw()
}

// applyCursor records cursor visibility + position on the screen. The caller
// flushes afterward to emit the show/hide + MoveTo.
func (rt *runtime) applyCursor(cur *component.Cursor) {
	if cur == nil {
		_ = rt.scr.HideCursor()
		return
	}
	_ = rt.scr.ShowCursor()
	_ = rt.scr.SetCursorPosition(cur.X, cur.Y)
}

// suspend releases the terminal so an external process owns the real TTY: it
// stops the input reader (so the child has sole ownership of os.Stdin), exits
// the screen modes, and restores the pre-TUI terminal state.
func (rt *runtime) suspend() {
	rt.stopReader()
	rt.exitModes()
	_ = term.Restore(os.Stdin.Fd(), rt.prevState)
}

// resume re-acquires raw mode + screen modes after an external process, forces
// a full repaint (Resize invalidates the renderer's diff buffer), and restarts
// the input reader. If the reader cannot be restarted the TUI cannot receive
// input, so quit.
func (rt *runtime) resume() {
	_, _ = term.MakeRaw(os.Stdin.Fd())
	rt.enterModes()
	if rt.w > 0 && rt.h > 0 {
		_ = rt.scr.Resize(rt.w, rt.h)
	}
	if err := rt.startReader(); err != nil {
		rt.logger.Error("restart reader after exec failed",
			logging.KeyComponent, "runtime", logging.KeyError, err)
		rt.quit = true
	}
}
