# Contributing to lognav

This guide covers the local workflow, layout, and conventions for contributing
code. For how to _use_ lognav, see [user documentation](user/documentation.md).
For the architecture, see [dev/design/](dev/design/).

## Quickstart / verification loop

Lead with these. The recipes are defined in the `justfile`.

```bash
just check        # fmt + build + lint + test -- the fast local iteration loop
just ci           # the pre-push gate: build + fmt-check + lint + vuln
                  # + test-race + fuzz + cov-check + bin-size
```

Individual recipes:

```bash
just build                # go build -trimpath -o lognav .
just test                 # go test with coverage profile
just run <subcommand>     # build to a temp path (stamps VCS version) and exec
```

Run `just check` while iterating; run `just ci` before you push. The TUI
(`just run` with no subcommand) waits for input and does not exit on its own --
prefer `just build` + `just test` for validation, and kill the process if you
must start it.

## Project layout

The public surface is just `main.go`; everything else lives under `internal/`.
The one exception is the root `docs` package -- an `embed.FS` holder for the
reference manual, intentionally left importable so the `lognav docs` command
can open versioned documentation links.

For the package map (including the headless query CLI and generic
`internal/sessionbus` peer notifications), see
[dev/design/overview.md](dev/design/overview.md).

## Coding guidelines

- Prefer small, focused changes over large rewrites.
- Add a doc comment to any non-trivial function; update doc comments when you
  change the behavior they describe.
- Prefer stdlib for string/slice handling (`strings`, `slices`) over
  reimplementing it.
- Always `just build` after a change to verify compilation.
- When finishing, run `just fmt` then `just lint` and resolve **every** report
  before declaring the work done.
- Component constructors follow `New(ctx context.Context, bundle deps.Bundle,
...args)` -- context first, the `deps.Bundle` (named `bundle`) second,
  component-specific args last. Components without a `ctx` take `bundle` first.

## Testing

- Table-driven tests grouped under `t.Run` subtests; standardize struct fields
  on `name`, `input`/`args`, `want`, `wantErr`.
- `require` for setup and invariant checks that must stop the test; `assert` for
  business-logic assertions against the system under test.
- `t.Parallel()` by default. The exception is tests touching OS-level shared
  state (e.g. PID-named files) -- document the constraint with a comment.
- Any package whose tests spawn goroutines adds `<pkg>/main_test.go` with
  `goleak.VerifyTestMain(m)`.
- Isolate state through dependency injection, not singletons: `depstest.NewTest(t)`
  for a full `deps.Bundle`, or `statetest.NewTestManager(t)` for a `state.Manager`
  alone.
- No `time.Sleep` for synchronization -- use `require.Eventually` /
  `require.EventuallyWithT` or channels. Prefer `t.Context`, `t.Setenv`,
  `t.TempDir`, `t.Chdir`.

## The concurrency invariant

The unbuffered runtime channel has one receiver: the loop. `PostCritical` on the
loop self-deadlocks. `Post` never blocks, but drops when no receiver is ready and
cannot reliably deliver on-loop. New on-loop must-deliver posts use
`msgs.PostAsync` or `msgs.RequestQuit`, never a handwritten `poster.Go` wrapper.
Off-loop workers and existing `poster.Go` bodies doing real work may call
`PostCritical` directly. Full contract: [dev/design/runtime.md](dev/design/runtime.md).

## Commit messages

Conventional Commits with parenthesized scope: `type(scope): summary`.

```
feat(logviewer): add search dialog
fix(jq): handle empty application
docs: update config reference
```

## CI

`just ci` is the gate. CI runs bash/`just` -- there is no GitHub Actions. When you add a new quality gate, expose it as a `justfile` recipe so CI
picks it up.

## Benchmarks & profiling

Hot-path benchmarks live in `*_bench_test.go` next to the code they cover.

- `just bench` -- run all benchmarks.
- `just bench-check` -- compare current runs against `bench/baseline.txt` (via
  benchstat).
- `just update-baseline` -- regenerate `bench/baseline.txt`.
- Diagnostics: `just cpuprof`, `just memprof`, `just trace`, `just race`.
- `just install-remote` -- `go install` from the module path.

**`bench/` is a data directory only -- do not add `.go` files there.**

Regenerate `bench/baseline.txt` only after an intentional perf change you have
reviewed. Benchmark numbers are advisory (they depend on the capture machine),
not authoritative.

## Using lognav

For product behavior (commands, config, Dataprime, workflows), see the
[full documentation index](user/documentation.md).
