# Archive background queries

Archive support is experimental and off by default. Enable
`core.enableExperimental` to show the Archive tab. Use
`lognav config describe core.enableExperimental` for the setting.

An ICL background query runs on the server after lognav moves on. A lognav
archive is the local registry entry that records those background queries; it
is not the query result itself.

## Use an archive

1. In the Instances tab, select the instances and prepare the query.
2. Use the Archive tab to manage background queries. Dispatch is refused while
   a normal fetch or watch is running.
3. In the Archive tab, press `enter` on an in-progress entry to poll it.
4. Press `enter` on a ready entry to collect it into a normal `.lognav`
   snapshot.

lognav does not poll archives automatically. Collect requires every archived
resource to still be configured. If one is missing, collection is refused
before authentication or data requests begin. Re-collecting a ready archive
creates another snapshot.

## Limits and results

An ICL background query can hold up to 1,000,000 rows per instance. Archive
collection is separate: lognav always collects at most 50,000 rows per instance,
regardless of `logs.maxRows`. A collected instance that reaches that limit is
marked with a warning.

Collection keeps failed, cancelled, or expired instances in the snapshot with
an empty result and its status message. The resulting snapshot can be browsed,
filtered, saved, and shared like a normal fetch result.
