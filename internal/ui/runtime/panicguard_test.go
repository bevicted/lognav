package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

// capturingLogger returns a JSON logger writing into buf, plus a mutex-guarded
// read accessor (the poster logs from a spawned goroutine).
func capturingLogger() (*slog.Logger, func() string) {
	var mu sync.Mutex
	buf := &bytes.Buffer{}
	lg := slog.New(slog.NewJSONHandler(&lockedWriter{mu: &mu, buf: buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return lg, func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// panicDeepInWorker panics several frames down so the test can assert the logged
// stack reaches the ORIGIN frame, not just the guard. The index comes in as a
// parameter so the out-of-range access is a runtime fault, not one static
// analysis folds away.
func panicDeepInWorker() { panicDeeper(7) }
func panicDeeper(i int)  { s := make([]int, 1); _ = s[i] }

func TestPosterGo_RecoversAndLogsPanic(t *testing.T) {
	t.Parallel()

	lg, read := capturingLogger()

	events := make(chan uv.Event)
	p := newEventPoster(t.Context(), events)
	p.logger = lg

	p.Go(func(context.Context) { panicDeepInWorker() })

	// A panicking worker must still release its WaitGroup slot, or shutdown hangs.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := p.Wait(waitCtx); err != nil {
		t.Fatalf("Wait after panicking worker: %v (WaitGroup slot leaked)", err)
	}

	out := read()
	if !strings.Contains(out, "recovered panic") {
		t.Fatalf("panic was not logged; got: %s", out)
	}
	if !strings.Contains(out, "poster goroutine") {
		t.Errorf("missing `where` attribution; got: %s", out)
	}
	if !strings.Contains(out, "index out of range [7] with length 1") {
		t.Errorf("missing panic value; got: %s", out)
	}
	// The whole point of recovering at the goroutine top frame: the origin
	// frames are still on the stack, so the trace must name the real fault site.
	if !strings.Contains(out, "panicDeeper") {
		t.Errorf("logged stack does not reach the origin frame; got: %s", out)
	}
}

func TestPosterGo_SurvivesPanicAndKeepsServingLaterWork(t *testing.T) {
	t.Parallel()

	lg, _ := capturingLogger()

	p := newEventPoster(t.Context(), make(chan uv.Event))
	p.logger = lg

	p.Go(func(context.Context) { panic("first worker dies") })

	// The contained panic must not poison the poster: subsequent work still runs.
	done := make(chan struct{})
	p.Go(func(context.Context) { close(done) })

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poster stopped running work after a worker panicked")
	}
}

func TestLogPanic_EmitsStructuredFields(t *testing.T) {
	t.Parallel()

	lg, read := capturingLogger()
	func() {
		defer func() {
			if r := recover(); r != nil {
				logPanic(lg, "unit", r)
			}
		}()
		panicDeepInWorker()
	}()

	var rec map[string]any
	line := strings.TrimSpace(read())
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("log line is not valid JSON (%v): %s", err, line)
	}
	for _, k := range []string{"where", "panic", "stack", "component"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("missing field %q in %v", k, rec)
		}
	}
	if rec["component"] != "runtime" {
		t.Errorf("component = %v, want runtime", rec["component"])
	}
}

func TestLogPanic_NilLoggerDoesNotPanic(t *testing.T) {
	// Not parallel: swaps the process-wide slog default.
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	lg, read := capturingLogger()
	slog.SetDefault(lg)

	logPanic(nil, "nil-logger", "boom") // must fall back, not nil-deref

	if !strings.Contains(read(), "recovered panic") {
		t.Error("nil logger did not fall back to slog.Default()")
	}
}

func TestRecoverLoop_RestoresTerminalLogsAndRepanics(t *testing.T) {
	t.Parallel()

	lg, read := capturingLogger()
	term := &bytes.Buffer{}
	rt := &runtime{
		scr:    uv.NewTerminalScreen(term, os.Environ()),
		logger: lg,
		// Mirrors the field session: any-event mouse tracking was on, and the
		// unreset ?1003h is what left the shell echoing SGR mouse reports.
		enableMouse: true,
		enableHover: true,
		// prevState nil: recoverLoop must skip term.Restore rather than nil-deref
		// (no real TTY in tests).
	}

	// Production order: Run enters the modes before the loop can panic. Reset the
	// buffer afterwards so the assertions see only what the GUARD emitted.
	rt.enterModes()
	term.Reset()

	repanicked := func() (r any) {
		defer func() { r = recover() }()
		func() {
			defer rt.recoverLoop()
			panicDeepInWorker()
		}()
		return nil
	}()

	// The loop guard must NOT swallow: the loop goroutine owns all UI state, so
	// continuing on a half-updated model would trade a crash for silent corruption.
	if repanicked == nil {
		t.Fatal("recoverLoop swallowed the panic; it must re-panic")
	}

	// Terminal restore is the difference between a readable trace and the shell
	// left in raw mode echoing mouse reports.
	seq := term.String()
	if seq == "" {
		t.Fatal("recoverLoop wrote no terminal-restore sequences")
	}
	if !strings.Contains(seq, "\x1b[?1049l") { // exit alt-screen
		t.Errorf("alt-screen not exited; sequences: %q", seq)
	}
	// The specific omission that made the field crash wreck the terminal: mouse
	// tracking left enabled, so the shell kept printing SGR reports on every
	// pointer move until the user reset it by hand.
	if !strings.Contains(seq, "\x1b[?1003l") {
		t.Errorf("any-event mouse tracking not disabled; sequences: %q", seq)
	}

	out := read()
	if !strings.Contains(out, "recovered panic") || !strings.Contains(out, "loop goroutine") {
		t.Errorf("panic not logged with loop attribution; got: %s", out)
	}
	if !strings.Contains(out, "panicDeeper") {
		t.Errorf("logged stack does not reach the origin frame; got: %s", out)
	}
}

// tmpPanicEvent is an arbitrary posted-app-event type; dispatch's default arm
// routes it to model.Update, which is where a real component panic originates.
type tmpPanicEvent struct{}

// TestLoop_PanicInDispatchIsGuarded drives the REAL loop -> dispatch path into a
// panic (nil model => nil-pointer deref inside Update, standing in for a
// component handler blowing up) and asserts the guard catches it there, not just
// when invoked directly. Together with TestRun_InstallsLoopPanicGuard this makes
// the manual F12 harness redundant for regression purposes.
func TestLoop_PanicInDispatchIsGuarded(t *testing.T) {
	t.Parallel()

	lg, read := capturingLogger()
	term := &bytes.Buffer{}
	rt := &runtime{
		scr:         uv.NewTerminalScreen(term, os.Environ()),
		logger:      lg,
		events:      make(chan uv.Event, 1),
		runCtx:      t.Context(),
		enableMouse: true,
		enableHover: true,
		w:           80,
		h:           24,
		// model stays nil: Update dereferences it, producing a genuine panic
		// from inside dispatch rather than a synthetic one.
	}
	rt.poster = newEventPoster(rt.runCtx, rt.events)
	rt.enterModes()
	term.Reset()

	rt.events <- tmpPanicEvent{} // buffered: loop picks it up without a sender

	repanicked := func() (r any) {
		defer func() { r = recover() }()
		func() {
			defer rt.recoverLoop()
			rt.loop()
		}()
		return nil
	}()

	if repanicked == nil {
		t.Fatal("panic from dispatch was swallowed; the loop guard must re-panic")
	}
	seq := term.String()
	if !strings.Contains(seq, "\x1b[?1049l") || !strings.Contains(seq, "\x1b[?1003l") {
		t.Errorf("terminal not restored after a dispatch panic; sequences: %q", seq)
	}
	out := read()
	if !strings.Contains(out, "recovered panic") || !strings.Contains(out, "loop goroutine") {
		t.Errorf("dispatch panic not logged; got: %s", out)
	}
	// The stack must reach the dispatch frame, or the log is useless for triage.
	if !strings.Contains(out, "dispatch") {
		t.Errorf("logged stack does not reach the dispatch frame; got: %s", out)
	}
}
