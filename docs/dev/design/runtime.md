# Runtime and Concurrency

`internal/ui/runtime` drives ultraviolet directly. It owns the terminal, input
reader, event loop, redraw ticker, and peer-notification server. This is the
canonical posting and concurrency contract; other documents link here.

**Siblings:** [overview](overview.md) ; [components](components.md).

## Ownership and lifecycle

`runtime.Run` creates an owned, never-closed **unbuffered** `chan uv.Event`.
The loop goroutine is its only receiver and its sole writer of UI state. It
routes terminal input to typed `ui.Model` methods and posted application events
to `ui.Model.Update`, draining a batch before one draw. `msgs.ExecRequest` is
handled on-loop; its completion calls `Update` directly rather than posting.

The runtime owns raw mode, the alternate screen, terminal reader, resize
handling, and the redraw ticker. A resize gets a second full repaint to avoid
ultraviolet's stale-region grow artifact. The ticker runs only while the model
is animating and posts a droppable tick from an off-loop worker.

Teardown cancels the runtime, stops the ticker and reader, restores the
terminal, cancels model queries, then waits for poster workers before closing
the model. This ordering prevents workers from writing resources being closed.
After the model releases a managed snapshot claim, a direct bounded peer
notification is safe; do not start a poster worker after the poster has drained.
Every resource owner follows the idempotent `Close() error` convention: log and
return cleanup failures, and use `errors.Join` when closing multiple resources.

## Posting contract

`EventPoster` is async ingress to the loop. Components receive its narrower
`msgs.Poster` interface because components cannot import the runtime.

| Method                  | Contract                                                                                                                                 |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `Post(ev)`              | Best effort and never blocking. It drops when no receiver is ready or shutdown begins. Use only for droppable work such as redraw ticks. |
| `PostCritical(ctx, ev)` | Must deliver. It blocks until the loop receives the event or either context cancels.                                                     |
| `Go(fn)`                | Runs `fn` on a tracked worker bound to the runtime context. `Wait` joins those workers at shutdown.                                      |

The event channel is unbuffered and the loop is not receiving while it dispatches
or draws. Therefore `PostCritical` on the loop goroutine self-deadlocks. `Post`
does **not** self-deadlock because it never blocks, but it commonly drops
on-loop and cannot provide delivery.

Rules:

1. New on-loop must-deliver posts from `HandleKey`, `On<Event>`, `Update`, or a
   keybind action use `msgs.PostAsync(p, ev)`. Use `msgs.RequestQuit(p)` for a
   quit request. These helpers are nil-safe and move `PostCritical` to a tracked
   worker.
2. Do not write a new `poster.Go(func(ctx) { _ = poster.PostCritical(ctx, ev) })`
   wrapper. `msgs.PostAsync` is the single named form of that rule.
3. Existing `poster.Go` bodies that perform real off-loop IO, queries, snapshot
   work, or chunk computation stay off-loop and may call `PostCritical` directly
   with their worker context. Stream callbacks and other off-loop workers may do
   the same.
4. When both producer and consumer run on the loop, inject and call a callback
   instead of posting. It avoids a worker hop, event round trip, and extra draw.
5. Do not hold a lock across `PostCritical` when `Draw` also takes that lock.
   Compute under the lock, release it, then post.

Tests for this hazard need a poster whose `Go` starts a real goroutine and whose
`PostCritical` blocks. `msgs/msgstest.FakePoster` runs `Go` inline and cannot
expose the deadlock.

## Message routing

The root names each event type in one owner. Root events have direct handling;
component families claim their own typed events and unclaimed events are logged.
Key precedence is focused overlay or input, tab selector, regular bindings, then
the active component. Optional input seams and overlay ownership are documented
in [components](components.md).

Two `ctrl+c` presses within the configured window exit at the runtime layer.
The first remains available to the focused input as the Clear binding. This
keeps a raw-mode exit path independent of component key handling.

## Failure containment

The loop panic guard restores the terminal, logs the panic, and re-panics: a
partially updated sole UI writer cannot safely continue. `poster.Go` instead
logs and contains a worker panic so one failed query or chunk does not discard
the session. These guards cannot recover runtime-fatal failures such as
concurrent map access; prevent those races rather than relying on recovery.
