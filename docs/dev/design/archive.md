# Archive

> **Experimental and opt-in.** Archive is available only when
> `core.enableExperimental` is true. Otherwise no Archive tab or dispatch
> keybind is registered and startup does not clean the registry.

An **archive** is lognav's local registry for one Dataprime background query
submitted across instances. It records server query IDs and their local state;
it is neither the server result nor a snapshot. Collecting an archive creates a
normal [snapshot](snapshot.md).

`internal/archive` owns local registry files, `internal/icl` owns remote
background-query requests, `instancepicker` dispatches and collects, and
`archivehandler` displays and polls. See [ICL](icl.md) for the remote API and
[snapshot](snapshot.md) for the collected file lifecycle.

## Registry

Each archive is one JSON `<name>.lognav-archive` file in the XDG data
`archives/` directory, separate from snapshot files. It contains the archive
name, query, submission time, and per-instance records:

```text
CRN, queryId, state, lastPolledAt, optional errorMessage
```

The registry stores full CRNs as identity and never persists display names.
States are `running`, `success`, `error`, and `expired`; cancelled server work
is `error`. An archive is ready only when it has at least one instance and none
is running. Collection is intentionally not recorded, so a ready archive can be
collected again while the server result exists.

Registry writes are atomic. New archive names begin as a timestamp and PID, and
creation reserves a collision-free name rather than overwriting another archive.
`Scan` ignores unreadable entries and orders valid entries by newest submission.

The 30-day `TTL`, computed from `submittedAt`, drives display and best-effort
startup cleanup only. Server not-found status is authoritative: a clock-based
expiry estimate must not discard a result that the server still has.

## Dispatch

Dispatch submits the current query as one ICL background query per enabled
instance. It is refused while a fetch or watch is active, and no-selection is a
distinct refusal. A dispatch does not clear displayed logs or open a `.wip`
snapshot container.

Each successful submit supplies a server `queryId`. Only successful instances
are saved in the new registry file; failures are visible in the dispatch result
but are not registry members. The UI switches to the Archive tab after a saved
dispatch.

## Poll and collect

Entering an in-progress archive polls only `running` entries. Poll resolves the
full archived CRN directly, so it can check a valid archived instance that is no
longer in current configuration. Terminal entries are frozen. A successful
status becomes `success`, a non-success terminal status becomes `error`, and
server not-found becomes `expired`. Auth, transport, and other status failures
preserve the current state, because they do not prove that a result expired.

Entering a ready, unexpired archive collects it. Collection first requires every
archived CRN to be in the effective configuration; otherwise it changes neither
query state nor files. It then restores the archive query, selects exactly the
archive instances, and downloads each server result by its `queryId` into the
ordinary snapshot `.wip` and finalization lifecycle.

The background server can retain up to 1,000,000 rows, but collection is
separately fixed at 50,000 rows per instance for both callback delivery and the
local store. A collected instance at that ceiling carries a truncation message
in its snapshot state. Even an all-error collect is finalized so the resulting
snapshot preserves per-instance error messages. The source archive remains
unchanged and re-collectable.

## UI behavior

The Archive tab colors expired and all-error archives as errors, running
archives as in progress, and ready archives with any successful instance as
success. Its preview shows the query, submission and estimated expiry, and
per-instance state with a query ID prefix. Polling refreshes the registry view
so changed states are visible.

Network and registry writes run off the UI loop. On-loop notifications use the
posting contract in [runtime](runtime.md); no archive-specific posting pattern
changes that contract.
