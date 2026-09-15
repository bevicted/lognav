# Timeline

The timeline is a read-only histogram of log severity over time. Press `t` in
the log viewer to open or close it. Press `enter` to move the log cursor to the
first record in the selected bucket.

Each row is a time bucket, oldest first. Its cells use the same severity colors
and glyphs as the log viewer. On a narrow terminal, lower-severity cells are
trimmed first.

## Navigation

| Key                | Action                              |
| ------------------ | ----------------------------------- |
| `j`, `down`        | Next bucket                         |
| `k`, `up`          | Previous bucket                     |
| `ctrl+d`, `ctrl+u` | Half page down or up                |
| `pgdn`, `pgup`     | Full page down or up                |
| `g`, `G`           | First or last bucket                |
| `enter`            | Move to the first log in the bucket |
| `esc`, `t`         | Close the timeline                  |
| `q`                | Quit lognav                         |

## Behavior

The timeline divides the earliest-to-latest log range into
`logs.timelineBuckets` buckets (default `100`). Empty buckets are hidden by
default; set `logs.timelineHideEmptyBuckets: false` to show them and preserve a
uniform time axis.

It is a snapshot of the current log store. If logs continue streaming, close
and reopen the timeline to include them.

While open, the contextual row above the key hints shows the instance phase,
`Bucket` position among visible buckets, `Time` as a UTC start through the
bucket's end-exclusive boundary, and the selected bucket's `Logs` count. Empty
buckets hidden by the default setting do not count toward the displayed bucket
position. On narrow screens the `Bucket`, `Time`, and `Logs` heads become
`B`, `T`, and `L`; if necessary the Time pill is removed, then the remaining
row clips at the right edge.

The default opener is `keys.timeline: ["t"]`. Use `lognav config describe
logs.timelineBuckets`, `lognav config describe logs.timelineHideEmptyBuckets`,
and `lognav config describe keys.timeline` to change these defaults.
