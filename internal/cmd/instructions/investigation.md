# Investigation reaction procedure

Use this reaction after the Query procedure already included in this topic.

1. First use a specifically named retained selector or external snapshot path, or another relevant retained selector. If a specifically requested retained result is missing or unreadable, report that condition and ask before replacing it with a network query. When no usable retained evidence exists and the request and target scope are clear, return to the already-loaded Query procedure.
2. Inspect every non-empty selector before retrying, regardless of query status; a non-empty selector remains usable evidence after a partial failure. Never inspect an empty selector. A clean empty result (`0 logs; no snapshot created` with zero status) is terminal: report it and offer refinement. Before reading records, run `lognav snapshot inspect "$selector" -o json` and use retained per-instance state, count, and size to choose the smallest useful target and sample.
3. Unless the user explicitly requests other fields, read payload only and cap every sample at 100 records:

   ```sh
   lognav snapshot logs "$selector" --instance "$instance" | jq -c '.data.log' | head -n 100
   ```

   This removes about 87 percent of metadata overhead. For an explicit request for other fields, project `.data.log` with only those requested fields and keep the 100-record cap.

4. Reuse retained data and local projections or filters before another network query. Return to the already-loaded Query procedure only for evidence-supported changes that preserve the requested target and meaning. Ask before broadening target scope, time scope, or meaning, or before a speculative or repeated attempt. Inspect each new result before considering another query. Complete when bounded evidence answers the goal or identifies the next evidence-supported query.
