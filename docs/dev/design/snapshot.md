# Snapshots

A snapshot is a `.lognav` binary container holding session state and logs.
`internal/snapshot` owns its format and filesystem management. The TUI, the
headless `query` command, and archive collection all produce the same format.
Snapshot identity and instance routing use full CRNs; configured names are only
display labels.

**Siblings:** [overview](overview.md) ; [runtime](runtime.md) ;
[archive](./archive.md) ; [ICL](icl.md). User workflows are in
[user documentation](../../user/documentation.md).

## Container format

Every container starts with an 8-byte magic and a big-endian `uint32` version.
The current and only supported version is `1`. It then contains append-only
named frames:

```text
uint16 name length | name bytes | uint32 content length | content bytes
```

Frame names longer than 65,535 bytes and content over 1 GiB are rejected. A
writer cannot append a duplicate frame. Existing containers are opened
read-only; known in-progress `.wip` files may use lenient parsing, which accepts
a trailing partial frame. Finalized files use strict parsing. Frame payloads are
fully decompressed before JSON decoding; keep this buffered path unless a
measured workload justifies a streaming replacement.

Version 1 contains one gzipped JSON `i_<full-crn>` log-array frame for every
snapshot member and one gzipped JSON `state` frame written last. The last state
frame makes an incomplete file distinguishable from a finalized snapshot.
Every declared member has a log frame, including zero-result, failed, and
cancelled members.

The state frame contains a `Snapshot`:

- v4 UUID `id`, query, search, jq, and filter rules;
- per-instance full CRN, phase, count, native-NDJSON byte size, timestamps, and
  optional error message.

`logs_size_bytes` is the exact compact native-NDJSON output size: each
`icl.Log.Data` encoding plus its newline, not the compressed frame size or the
larger in-memory log wrapper. Zero logs require zero bytes and nonzero logs
require nonzero bytes. This rejects incompatible pre-size nonempty version-1
state rather than reporting a false size.

A state-frame UUID is minted when absent. A byte-level copy preserves its UUID;
a manual save creates a new state frame and therefore a new UUID. UUID uniqueness
is required for name-independent recovery and ID-prefix selection.

## Snapshot names and finalization

Finalized automatic snapshots have the strict grammar
`auto-YYYYMMDD-HHMMSS[-N].lognav`, where `YYYYMMDD-HHMMSS` is the local
fetch-start time and an optional collision suffix `N` is a canonical integer of
at least 2. The first automatic snapshot for a second has no suffix; collisions
use `-2`, `-3`, and later suffixes. Only names matching this grammar are `auto`;
every other finalized `.lognav` file is `manual`. These are lifecycle kinds.

Manual snapshots have user-selected descriptive basenames such as `incident` or
`demo`, with `.lognav` added when omitted. TUI manual save and rename reserve the
case-sensitive `auto-` namespace for automatic snapshots, as well as `latest`.

Every producer creates its WIP exclusively with mode `0600`. A WIP has the
hidden grammar `.<random>_<pid>.lognav.wip`: `os.CreateTemp` supplies the random
portion and the final canonical positive PID supports stale-WIP cleanup. WIP
names are not finalized names.

A TUI fetch opens a WIP backing container before streaming. Instance frames are
appended as instances settle; the state frame is written only after all queries
and pending frame compression finish. A new fetch discards an unfinalized
predecessor, preventing a late write from becoming part of the new fetch.

Before publication, a complete WIP is flushed, synced, and closed. Automatic
publication uses `RenameNoClobber`, which hard-links the WIP to a candidate and
never replaces an existing destination. The TUI must preclaim each candidate
before its publication attempt; a claim failure aborts publication. A collision
retries the next canonical suffix. If publication links the destination but
cannot remove the source WIP, the destination is published and remains claimed,
the cleanup error is reported, and the PID-stamped WIP remains for stale cleanup.
Other state-writing, preclaim, or publication failures create no destination,
clear the TUI claim, and leave the result in memory as unsaved. An all-empty
ordinary fetch removes its WIP and creates no snapshot. Archive collection is an
exception: it follows the same framing and finalization path but finalizes even
when every member errors, preserving the error-bearing state.

Automatic retention runs only after successful publication while the selected
destination remains claimed. It retains the configured number of strictly
automatic files by `ModTime`, newest first, and never removes a file held by a
live session. Manual files, other names, and WIP files are not automatic
retention candidates. A protected file may therefore leave more files than the
configured limit.

The headless `query` command uses the same format and automatic name lifecycle.
It writes one frame for every selected member, including empty or failed members,
writes state last, syncs before publication, runs retention, and notifies peers.
An all-empty or cancelled query removes its WIP rather than finalizing a partial
artifact.

## Restore and manual save

Restore atomically applies query, search, jq, and filters. It resets configured
rows before restoring matching CRNs. Snapshot CRNs absent from current
configuration remain available as read-only rows for lazy frame access, but
cannot participate in selection, fetch, watch, archive dispatch, or collection.
A subsequent fetch, watch, collect, restore, or backing eviction removes those
transient rows. Manual save preserves their declared CRNs and frames.

Manual save is refused while a fetch is active and never overwrites. The name
must be a safe basename, cannot be `latest` or begin with `auto-`, cannot name
an existing file, and cannot replace a file held by another live session. The
final no-clobber rename is the race backstop if a destination appears after
those checks.

Manual save copies declared log frames from the finalized backing container
verbatim and writes state last. It writes a synced WIP before its no-clobber
rename. Missing or unreadable backing data degrades to empty
frames, while a crash-left `.wip` is cleaned at startup. Copying durable frames
rather than rebuilding from resident stores prevents state counts and persisted
logs from diverging.

## In-use and peer updates

A session reading a managed snapshot keeps `<pid>.inuse`, an atomically rewritten
file naming its backing basename. Live-PID checks make this an advisory,
crash-safe guard: lognav's deletion, pruning, and adoption operations refuse
held snapshots, but external tools can still alter files. Startup removes
dead-PID `.inuse` files and stale PID-stamped `.wip` files; it does not remove
artifacts owned by a live process.

Renames are no-clobber and are allowed for a holder. The holder updates its
backing path and `.inuse` claim. If a best-effort peer rename notification is
lost and the old path is absent, the holder may resolve its stable UUID among
managed snapshots. Only an absent path triggers this recovery; corruption and
decode errors remain errors.

Live TUI sessions exchange local Unix-socket notifications through
`internal/sessionbus`:

| Method             | Parameters                       | Effect                            |
| ------------------ | -------------------------------- | --------------------------------- |
| `snapshot_renamed` | sanitized `{from, to}` basenames | holders update their backing path |
| `snapshots_dirty`  | `{}`                             | snapshot lists refresh            |

Snapshot finalization, manual save, rename, delete, CLI mutation, and clean
release notify peers. Broadcast is synchronous per caller with a 500 ms bound
per peer; peer failures are logged and do not make a local operation fail.
Notifications carry only sanitized basenames, preventing a peer from redirecting
a backing path outside the snapshot directory.

## CLI boundary

`lognav snapshot` manages local files and makes no ICL requests. `inspect` and
`logs` load configuration only to render current display names and resolve
instance selectors; `--no-config` uses built-in names. Other filesystem commands
remain usable with invalid configuration.

Commands that take a managed snapshot selector accept an exact name or `latest`;
`inspect`, `logs`, `rm`, `path`, and `clip` also accept an unambiguous UUID
prefix of at least four characters, with an exact name taking precedence. `list`
takes no selector.
`logs` streams the stored native NDJSON shape. `rm` and `prune` skip in-use
snapshots. `adopt` validates or explicitly force-accepts an external container,
rejects `.wip` input, and does not overwrite an in-use destination.
