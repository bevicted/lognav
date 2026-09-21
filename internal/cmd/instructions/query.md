# Query procedure

Use this procedure to select configured targets, construct Dataprime, dispatch
a query, and capture its outcome. Construct and dispatch Dataprime only through
a Query procedure present in the currently loaded topic. Its presence permits
but does not require execution, and is an agent-behavior prerequisite, not CLI
access control.

1. Use existing terminal output and request context to select only targets
   relevant to the request. Otherwise run `lognav config get icl.instances -o
json`, select the matching targets, and pass each with repeatable
   `--instance` flags in one query command. If no target can be determined, ask
   which configured instance to use before querying. Use `--all` only when the
   user requests every configured target. If the user confirms that the target
   is unknown, use `--first` for one discovery query with contextual filters;
   inspect its result, then restrict subsequent queries to the discovered
   instance. Do not emulate `--first` by enumerating configured instances.
2. If the user supplies a Dataprime query without requesting changes, use it
   unchanged. If the user requests a modification, change only what the request
   requires and preserve the rest. Otherwise construct a query for the actual
   request. Strongly recommend an explicit range because omitting it delegates
   the range to API-level defaults. Use a relative range such as `source logs
last 15m`, or a known bounded precise range such as `source logs between
@'2025-01-02T00:00:00Z' and @'2025-01-02T01:00:00Z'`; known bounded ranges are
   more efficient than broad relative ranges. Put one pipe operator on each line
   for readability, not because the parser requires it. Dataprime strings use
   single quotes.
3. Use `$m` for metadata, `$l` for labels, and `$d` for data. When the user names
   an app, service, or system that produces the logs, filter the full name with
   `| f $l.subsystemname == 'service'`. Use documented payload access `$d.log`.
   Never infer or invent a field name from a natural-language label. If a user
   data field is unknown, use `$d ~~ 'text'`; use `field ~ 'text'` only when that
   field is grounded in the user's query, `lognav docs dataprime --print`, or
   observed evidence. For a dashed value in `$d`, ICL can omit a complete-value
   match; search a distinctive dash-free segment instead. Do not apply this
   workaround to labels; preserve the full label value. Do not add a Dataprime
   `limit`. Advanced operations such as aggregation belong in `lognav docs
   dataprime --print`.
4. Protect `$d` from shell expansion with a quoted heredoc, then send the query
   through stdin. Capture selector stdout before checking status because partial
   outcomes can retain a selector:

   ```sh
   query=$(cat <<'DATAPRIME'
   source logs last 15m
   | filter $d ~~ 'search text'
   DATAPRIME
   )
   query_status=0
   selector=$(printf '%s\n' "$query" | lognav query --instance "$instance") || query_status=$?
   ```

   Read member diagnostics and the final outcome from stderr. Report the
   selector when present, `query_status`, member diagnostics, and the final
   outcome. Snapshot inspection is outside this procedure.

5. Treat `0 logs; no snapshot created` with a zero status as a clean empty
   result. If ICL reports out of memory, an allowed follow-up may narrow the
   query without changing its meaning when that is safe; otherwise explain the
   situation and ask for guidance. If authentication prevents the query, ask
   the user to run `lognav login`; credential installation remains user-managed.
   The topic's completion procedure decides whether to continue.
