# Workflows

Logs has a contextual pill row above the optional key-hint line. It shows the
adopted instance phase and timer, log span and position, plus active jq, search,
and filter modifiers. Click an active modifier pill to open its existing editor.
Other tabs reclaim the contextual row. The key-hint line shows the most useful
actions for the active context; click a hint to run the same action as its key.
Use `?` for the complete active-context keybinding list. Set
`core.showKeyHints: false` to hide the hint line and return its row to tab
content. The examples below use the default keys.

## TUI fetch

1. Press `e` to edit the Dataprime query.
2. Press `enter` to open the Instances tab.
3. Select instances with `space`, then press `f` to fetch. When the target
   region is unknown, press `shift+f` instead: with no selection it races every
   configured instance, keeps the first instance to return a log selected, and
   cancels the rest. The snapshot still records every candidate and its result.
4. Open the Logs tab to search, filter, and inspect the result.
5. Press `ctrl+s` to save or restore a snapshot.

Press `t` in the log viewer for the timeline.

## Snapshots

Snapshots keep the query, fetched-candidate state, and logs. A first fetch keeps
all raced candidates in its snapshot even though its losing rows are deselected afterward.
Use `lognav snapshot --help` for the command reference.

```sh
lognav snapshot list
lognav snapshot inspect latest
lognav snapshot clip incident
```

Automatic snapshots use names such as `auto-20260814-120000.lognav`. Manual
snapshots use descriptive names such as `incident.lognav` or `demo.lognav`.

Commands that take a managed snapshot selector accept an exact name or the
reserved `latest`; `inspect`, `logs`, `rm`, `path`, and `clip` also accept an
unambiguous UUID prefix of at least four characters. `list` takes no selector.
`snapshot inspect` and `snapshot logs` also accept an external `.lognav` path.
Open a downloaded file in place, or adopt it into the managed snapshot directory:

```sh
lognav ~/Downloads/incident.lognav
lognav --adopt ~/Downloads/incident.lognav
```

Use `lognav snapshot adopt --help` for collision, overwrite, and copy behavior.

## Headless and agent use

For scripts, pipe a Dataprime query to `lognav query` and select targets with
`--instance` or `--all`. When the target is confirmed unknown, use `--first` to
race effective configured instances until the first decoded log:

```sh
printf '%s\n' 'source logs last 15m' | lognav query --first
```

`lognav query --help` defines stdout, stderr, snapshot, `--tee`, and exit-status
behavior. Run `lognav instruct` to give a local agent the offline `config`,
`query`, or `investigation` onboarding path.
