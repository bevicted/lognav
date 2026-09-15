# Architecture Overview

lognav is a Go TUI for IBM Cloud Logs (ICL). It uses Charmbracelet
**ultraviolet** (`uv`) directly, with a hand-written runtime event loop rather
than the removed Bubbletea/Bubbles/Lipgloss/glamour stack. It supports parallel
Dataprime queries, jq filtering, snapshots, background-query archives, and
1Password-backed credentials.

**Sibling design docs:** [runtime](runtime.md) ; [components](components.md) ;
[snapshot](snapshot.md) ; [archive](./archive.md) ; [ICL](icl.md) ;
[configuration](./config.md).

## Boundaries and entry points

```text
main.go -> internal/cmd.Execute() -> runtime.Run(ctx, bundle, ui.New(...))
```

`internal/cmd` owns Cobra command wiring. Config-dependent commands explicitly
load their dependency bundle at this CLI boundary; config-independent commands
do not load configuration. The TUI and `login` load configured IAM environments;
`login --no-config` uses public defaults. The TUI enters through `runtime.Run`.
The headless `query` command owns its authentication and parallel streams; `query --first`
races selected candidates until one decoded log arrives, then cancels pending
authentication and loser streams while preserving every candidate in the
snapshot metadata. It does not construct a `ui.Model` or ultraviolet runtime. `config`, `snapshot`,
`docs`, `instruct`, `version`, and completion generation and profile installation
are CLI surfaces with their own dependencies. Completion behavior does not load
configuration.

The root `ui.Model` owns five tabs and their shared layout. In the Instances tab,
`shift+f` starts a one-shot first-response fetch: it races the enabled rows (or
all configured rows when none are selected), elects the first decoded-log
producer on the loop, and cancels the losers. Snapshot membership remains the
fetch-start candidate set; only after finalization are losing rows deselected.

| Tab       | Component         | Responsibility                                  |
| --------- | ----------------- | ----------------------------------------------- |
| Query     | `queryeditor`     | Dataprime input                                 |
| Instances | `instancepicker`  | selection and parallel fetch                    |
| Logs      | `logviewer`       | active instance logs                            |
| Snapshot  | `snapshothandler` | save and restore                                |
| Archive   | `archivehandler`  | background-query dispatch, poll, and collection |

Archive is experimental and exists only when `core.enableExperimental` is true.
It is last so the first four tab indices are stable.

## State ownership

The runtime loop is the sole writer of UI state. Components receive posted
application events through typed `On<Event>` methods reached by `ui.Model.Update`;
they do not have an `Update` method on the base component interface. See
[runtime](runtime.md) for posting and lifecycle rules.

A `LogStore` is reused for a re-fetch. Code retaining row indexes must use the
store generation as well as store identity and clear row-derived state when the
generation changes. Clear stores through `Instance.ClearStore`, not the store
directly, so saved cursor state is discarded with the rows it indexes. This
prevents a re-fetch from reopening an instance at an unrelated old row.

The active view owns optional contextual status above the key-hint row. Normal
Logs reports its adopted instance phase/timer, log span, raw cursor position, and
active jq, search, and filter modifiers; empty stores omit span and position.
It never consults another selected or pending instance. On jq parse failure, log
the error and reset jq state; do not replace displayed logs with `SetMessage`.

## Package map

| Package                                              | Boundary                                                 |
| ---------------------------------------------------- | -------------------------------------------------------- |
| `internal/cmd`                                       | Cobra CLI and command-specific orchestration             |
| `internal/config`                                    | YAML loading, validation, migration, metadata, and edits |
| `internal/icl`                                       | `net/http` Dataprime SSE client and authentication       |
| `internal/ui`                                        | root model, key routing, tabs, and shared UI state       |
| `internal/ui/runtime`                                | terminal lifecycle, event loop, poster, redraw ticker    |
| `internal/ui/components`                             | TUI components and optional input seams                  |
| `internal/ui/msgs`                                   | cross-component event types and the poster seam          |
| `internal/snapshot`                                  | snapshot containers and `.inuse` coordination            |
| `internal/archive`                                   | local background-query registry                          |
| `internal/sessionbus`                                | local peer notifications for snapshot changes            |
| `internal/deps`                                      | injected config, state, and logger bundle                |
| `internal/state`                                     | per-session mutable state                                |
| `internal/filter`                                    | filter rule vocabulary shared by state and logviewer     |
| `internal/secret`                                    | self-redacting credentials                               |
| `internal/logging`, `internal/build`, `internal/xdg` | shared infrastructure                                    |
| `docs`                                               | embedded user documentation for `lognav docs`            |

Dependencies point toward these leaf packages; components do not import the
runtime. `msgs.Poster` is the narrow seam that avoids that import cycle.
`internal/icl` stays hand-rolled rather than adopting the IBM SDK to keep the
dependency surface and streaming behavior under local control.

## Performance boundaries

The log viewer uses a 128-entry render cache, chunked concurrent storage, and
per-epoch read-only jq, filter, and search compilation shared by workers.
Maintain those ownership boundaries: workers compute off-loop and the loop
applies results after epoch checks. Each chunk carries the fetch generation; a
filter chunk also carries a filter generation, bumped when jq or rules change
without a fetch. Applying a stale filter result could overwrite visibility
calculated from newer jq output. The synchronous TUI store remains bounded by
the supported 50K-row ceiling; archive collection has its own fixed limit.
