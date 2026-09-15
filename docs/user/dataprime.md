# Dataprime

```
source logs | operator ... | operator ...
```

Chain operators with pipes (`|`). Whitespace between operators is ignored, so queries can span lines. Comments start with `//`.

```
source logs last 1h
// only 500s from example-service
| filter $l.subsystemname == 'example-service'
  && log.status_code >= 500
| orderby $m.timestamp asc
```

## Top-Level Fields

- `$m`: metadata (e.g. `$m.timestamp`)
- `$l`: labels (e.g. `$l.subsystemname`)
- `$d`: data (default, so `$d.log` == `log`)

## Data Types

| Name      | Example                         | Notes                                 |
| --------- | ------------------------------- | ------------------------------------- |
| string    | `'us-east-1'`                   | single quote only                     |
| number    | `23` `-12.32`                   |                                       |
| bool      | `true` `false`                  |                                       |
| timestamp | `@'2023-01-01T00:00Z'` `@'now'` | ISO 8601, `@'YYYY-MM-DDTHH:MM:SSZ'`   |
| interval  | `1d2h3m4s5ms6us7ns`             | any combination of units              |
| regexp    | `/^prod-.*/`                    | ruby-flavored regex                   |
| null      | `null`                          | can be used with all other data types |
| dataset   | `logs` or `spans`               |                                       |

## Expressions

### Comparison and Logic

| Type       | Operators                                              |
| ---------- | ------------------------------------------------------ |
| comparison | `==` `!=` `<` `>` `<=` `>=`                            |
| boolean    | `&&` `\|\|` `!`                                        |
| contains   | `~` (substring in field) `~~` (substring in top-level) |
| arithmetic | `+` `-` `*` `/`                                        |

### Field Access

Nested fields use dot notation:

```
log.kubernetes.pod_id
$l.subsystemname
$m.timestamp
```

### Function and Method Notation

All functions can also be called as methods. These are equivalent:

```
contains(log.msg, 'error')
log.msg.contains('error')
```

### Aliases

Use `as` to rename fields in output:

```
| groupby log.cluster_id aggregate count() as error_count
| choose log.msg as msg, log.status_code as status
```

### Membership

Use `in` to test against multiple values:

```
| filter log.status_code.in(500, 502, 503)
| filter $l.subsystemname.in('example-service', 'other-service')
```

## Sources

```
source <dataset> [<timeframe>]
```

### Parameters

#### timeframe

```
last <interval>                              // last 1h, last 3d
between <start-time> and <end-time>          // between @'2025-01-01T00:00Z' and @'2025-01-02T00:00Z'
around <time> [interval <interval>]          // around @'2025-01-02T00:02:13+01:00' interval 30m
timeshifted <interval>                       // compare with past data
```

## Text Search

### Recommended Form

- Known field: `| f $d.field ~ 's'`
- Unknown field: `| f $d ~~ 's'`

### Search Forms

Specific field:

```
| filter log.msg ~ 's'
| contains(log.msg, 's')
| log.msg.contains('s')
```

`$m`, `$l`, or `$d`; does not descend into subobjects such as `$d.log`:

```
| filter $d ~~ 's'
```

`$m`, `$l`, `$d`, or a specific field; does not descend into subobjects:

```
| find 's' in $d
| find 's' in log.msg
```

Everywhere, including `$m`, `$l`, and `$d`:

```
| lucene 's'
| wildfind 's'
```

### String Match Functions

```
log.msg.startsWith('s')
log.msg.endsWith('s')
log.msg.contains('s')
log.msg.matches(/regexp/)
log.msg.in('s1', 's2', ...)
```

## Operators

### Filtering

#### `filter` (alias: `f`)

Keep rows matching a condition.

```
| filter $l.subsystemname == 'example-service'
| f log.status_code >= 500
| f log.msg ~ 'timeout' && log.cluster_id != null
```

#### `block`

Remove rows matching a condition; inverse of `filter`.

```
| block log.status_code == 200
| block log.msg ~ 'healthcheck'
```

#### `choose`

Select specific fields, like SQL `SELECT`.

```
| choose $m.timestamp, log.msg, log.status_code
| choose log.cluster_id as cluster, log.msg as msg
```

#### `find`

Full-text search in a top-level or specific field.

```
| find 'error' in $d
| find 'timeout' in log.msg
```

#### `wildfind`

Search text across all top-level fields.

```
| wildfind 'timeout'
```

#### `lucene`

Search all fields with Lucene query syntax.

```
| lucene 'status:500 AND method:GET'
```

### Aggregation

Aggregation and `orderby` are incompatible; aggregated results cannot be sorted.

#### `groupby`

Group results, optionally with aggregate functions.

```
| groupby log.cluster_id
| groupby log.cluster_id, log.status_code
| groupby log.cluster_id aggregate count()
| groupby log.cluster_id aggregate count() as cnt, avg(log.duration) as avg_dur
| groupby log.cluster_id aggregate distinct_count(log.request_id) as requests
```

#### `countby`

Shorthand for `groupby ... aggregate count()`.

```
| countby log.status_code
```

#### `count`

Count all matching logs.

```
| count
```

#### `distinct`

Deduplicate results by field(s).

```
| distinct log.cluster_id
```

#### `dedupeby`

Keep only the first N events per unique value.

```
| dedupeby log.cluster_id
| dedupeby log.pod_id limit 3
```

#### `multigroupby`

Run multiple `groupby` queries in one pass and concatenate results.

```
| multigroupby log.cluster_id aggregate count(), log.status_code aggregate count()
```

#### `aggregate`

Aggregate the entire result set without grouping.

```
| aggregate count() as total, avg(log.duration) as avg_dur
```

#### `union`

Concatenate results from multiple queries.

```
| union (source spans last 1h | filter service == 'api')
```

### Sorting and Limiting

`orderby` is incompatible with aggregation and limited to 10,000 values.

#### `orderby` (aliases: `sortby`, `order by`, `sort by`)

Sort results.

```
| orderby $m.timestamp asc
| orderby log.duration desc
| orderby log.cluster_id asc, $m.timestamp desc
```

#### `top`

Return the highest N values by an expression.

```
| top 10 log.request_id by log.duration
```

#### `bottom`

Return the lowest N values by an expression.

```
| bottom 5 log.cluster_id as cluster by log.error_count
```

#### `limit`

Restrict result count; order is not guaranteed without `orderby`.

```
| limit 500
```

### Data Manipulation

#### `create` (aliases: `add`, `c`)

Generate a new field from an expression.

```
| create log.duration * 1000 as duration_ms
| c concat(log.method, ' ', log.path) as request
```

#### `replace`

Modify a field value in place.

```
| replace log.msg with toLowerCase(log.msg)
| replace log.status_code with if(log.status_code >= 500, 'error', 'ok')
```

#### `remove`

Delete a field from output.

```
| remove log.debug_info
| remove log.internal_metadata
```

#### `move`

Rename or relocate a field.

```
| move log.old_name to log.new_name
```

#### `convert`

Change a field's data type.

```
| convert log.status_code to number
| convert log.timestamp to timestamp
```

#### `extract`

Parse a string into structured fields with a pattern.

```
| extract log.msg into method, path using /(?P<method>\w+) (?P<path>\S+)/
```

#### `redact`

Mask or replace sensitive data.

```
| redact log.msg matching /\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}/ to '[REDACTED_IP]'
| redact log.msg matching /Bearer\s+\S+/ to '[REDACTED_TOKEN]'
```

### Advanced

#### `explode`

Expand an array field into one row per element.

```
| explode log.tags
```

#### `join`

Combine results from two datasets with left-join semantics.

```
| join (source spans last 1h | filter service == 'api') on trace_id
```

#### `enrich`

Add contextual data from a lookup table.

```
| enrich ip_lookup on log.client_ip
```

## Functions

### Aggregation Functions

| Function                         | Description                              |
| -------------------------------- | ---------------------------------------- |
| `count()`                        | count all rows                           |
| `count_if(condition)`            | count rows matching condition            |
| `distinct_count(field)`          | count unique values                      |
| `sum(field)`                     | sum of values                            |
| `avg(field)`                     | average of values                        |
| `min(field)`                     | minimum value                            |
| `max(field)`                     | maximum value                            |
| `min_by(field, by)`              | value of `field` where `by` is minimum   |
| `max_by(field, by)`              | value of `field` where `by` is maximum   |
| `percentile(field, p)`           | p-th percentile (0-100)                  |
| `stddev(field)`                  | standard deviation                       |
| `sample_stddev(field)`           | sample standard deviation                |
| `variance(field)`                | variance                                 |
| `any_value(field)`               | arbitrary value from group               |
| `distinct_count_if(field, cond)` | count unique values matching condition   |
| `approx_count_distinct(field)`   | approximate distinct count (HyperLogLog) |
| `collect(field)`                 | collect values into an array             |

### String Functions

| Function                           | Description                                    |
| ---------------------------------- | ---------------------------------------------- |
| `contains(field, str)`             | true if field contains str                     |
| `startsWith(field, str)`           | true if field starts with str                  |
| `endsWith(field, str)`             | true if field ends with str                    |
| `matches(field, regexp)`           | true if field matches regex                    |
| `concat(a, b, ...)`                | concatenate strings                            |
| `length(field)`                    | character count                                |
| `byteLength(field)`                | byte count                                     |
| `trim(field)`                      | strip leading/trailing whitespace              |
| `ltrim(field)`                     | strip leading whitespace                       |
| `rtrim(field)`                     | strip trailing whitespace                      |
| `toLowerCase(field)`               | convert to lowercase                           |
| `toUpperCase(field)`               | convert to uppercase                           |
| `substr(field, start, len)`        | extract substring                              |
| `indexOf(field, str)`              | position of first occurrence (-1 if not found) |
| `splitParts(field, delim, idx)`    | split by delimiter, return part at index       |
| `regexpSplitParts(field, re, idx)` | split by regex, return part at index           |
| `pad(field, len, char)`            | pad to length with char                        |
| `padLeft(field, len, char)`        | pad left to length                             |
| `padRight(field, len, char)`       | pad right to length                            |
| `urlEncode(field)`                 | URL-encode a string                            |
| `urlDecode(field)`                 | URL-decode a string                            |
| `encodeBase64(field)`              | encode to base64                               |
| `decodeBase64(field)`              | decode from base64                             |

### Time Functions

| Function                         | Description                                                      |
| -------------------------------- | ---------------------------------------------------------------- |
| `now()`                          | current timestamp                                                |
| `parseTimestamp(str, fmt)`       | parse string to timestamp                                        |
| `formatTimestamp(ts, fmt)`       | format timestamp to string                                       |
| `toUnixTime(ts)`                 | convert to Unix epoch                                            |
| `fromUnixTime(n)`                | convert Unix epoch to timestamp                                  |
| `addTime(ts, interval)`          | add interval to timestamp                                        |
| `subtractTime(ts, interval)`     | subtract interval from timestamp                                 |
| `diffTime(ts1, ts2)`             | difference between two timestamps                                |
| `extractTime(ts, part)`          | extract part: `year`, `month`, `day`, `hour`, `minute`, `second` |
| `roundTime(ts, interval)`        | round timestamp to nearest interval                              |
| `parseInterval(str)`             | parse string (`'1d2h3m'`) to interval                            |
| `formatInterval(interval, unit)` | format interval in specific unit (`ms`, `s`, `h`)                |
| `toInterval(ms)`                 | convert milliseconds to interval                                 |

### Numeric Functions

| Function           | Description             |
| ------------------ | ----------------------- |
| `abs(n)`           | absolute value          |
| `ceil(n)`          | round up                |
| `floor(n)`         | round down              |
| `round(n, places)` | round to decimal places |
| `sqrt(n)`          | square root             |
| `power(n, exp)`    | exponentiation          |
| `mod(n, d)`        | modulo                  |
| `log(n)`           | base-10 logarithm       |
| `log2(n)`          | base-2 logarithm        |
| `ln(n)`            | natural logarithm       |

### Conditional Functions

| Function                                             | Description                     |
| ---------------------------------------------------- | ------------------------------- |
| `if(cond, then, else)`                               | ternary conditional             |
| `case(field, v1, r1, v2, r2, ..., default)`          | switch on exact match           |
| `case_contains(field, s1, r1, s2, r2, ..., default)` | switch on substring match       |
| `case_equals(field, v1, r1, ..., default)`           | switch on equality              |
| `case_greaterthan(field, v1, r1, ..., default)`      | switch on descending thresholds |
| `case_lessthan(field, v1, r1, ..., default)`         | switch on ascending thresholds  |
| `firstNonNull(a, b, ...)`                            | return first non-null argument  |
| `in(field, v1, v2, ...)`                             | true if field equals any value  |

### Array Functions

| Function                        | Description                      |
| ------------------------------- | -------------------------------- |
| `arrayLength(arr)`              | number of elements               |
| `arrayContains(arr, val)`       | true if array contains value     |
| `arrayAppend(arr, val)`         | append element to array          |
| `arrayConcat(arr1, arr2)`       | concatenate two arrays           |
| `arrayJoin(arr, delim)`         | join elements into a string      |
| `arraySort(arr)`                | sort array elements              |
| `arrayInsertAt(arr, idx, val)`  | insert element at index          |
| `arrayRemove(arr, val)`         | remove element by value          |
| `arrayRemoveAt(arr, idx)`       | remove element at index          |
| `arrayReplaceAt(arr, idx, val)` | replace element at index         |
| `arraySplit(str, delim)`        | split string into array          |
| `isEmpty(arr)`                  | true if array is empty           |
| `cardinality(arr)`              | number of unique elements        |
| `inArray(val, arr)`             | true if value exists in array    |
| `setUnion(arr1, arr2)`          | union of two arrays              |
| `setIntersection(arr1, arr2)`   | intersection of two arrays       |
| `setDiff(arr1, arr2)`           | elements in arr1 but not arr2    |
| `isSubset(arr1, arr2)`          | true if arr1 is subset of arr2   |
| `isSuperset(arr1, arr2)`        | true if arr1 is superset of arr2 |

### UUID Functions

| Function        | Description                   |
| --------------- | ----------------------------- |
| `randomUuid()`  | generate a random UUIDv4      |
| `isUuid(field)` | true if field is a valid UUID |

### IP Functions

| Function                      | Description                    |
| ----------------------------- | ------------------------------ |
| `ipInSubnet(field, cidr)`     | true if IP is in CIDR range    |
| `ipPrefix(field, prefix_len)` | extract network prefix from IP |

### Utility Functions

| Function           | Description                                   |
| ------------------ | --------------------------------------------- |
| `recordLocation()` | cloud storage location of the record (S3 URL) |

## Pitfalls and Recommendations

### Fields with dashes

ICL has issues filtering fields containing dashes, such as request IDs with `-`. Prefer substring matching (`field ~ 'xyz'` or `$d ~~ 'xyz'`) to exact matches.

### Aggregation vs ordering

`groupby` and `orderby` are mutually exclusive; aggregated results cannot be sorted.

### Regex performance

Regexp operations are slower than other matching methods. Prefer `~` for substring and `startsWith()` or `endsWith()` for anchored matches.

### Log retention

ICL log retention is indefinite within sane bounds. Old queries may be slower, but logs do not age out.

## Examples

### Basic filtered search

```
source logs last 1h
| filter $l.subsystemname == 'example-service'
  && log.status_code >= 500
| orderby $m.timestamp asc
```

### Search across all data

```
source logs last 3h
| filter $d ~~ 'example-token'
| orderby $m.timestamp asc
```

### Multiple filters

```
source logs last 1h
| filter $l.subsystemname == 'example-service'
| filter log.status_code >= 500
| block log.msg ~ 'healthcheck'
| orderby $m.timestamp asc
```

### Count errors by status code

```
source logs last 1h
| filter $l.subsystemname == 'example-service'
| filter log.status_code >= 400
| groupby log.status_code aggregate count() as cnt
```

### View in context

Investigate a specific pod around a known timestamp:

```
source logs around @'2025-01-02T00:02:13+01:00' interval 3s
| filter $d.kubernetes.pod_id ~ 'my-pod-id'
| orderby $m.timestamp asc
```

### Trace a request ID

Use a distinctive token to search for a request ID:

```
source logs last 1d
| filter $d ~~ 'example-token'
| orderby $m.timestamp asc
```

### Field manipulation

```
source logs last 1h
| filter $l.subsystemname == 'example-service'
| create concat(log.method, ' ', log.path) as request
| choose $m.timestamp, request, log.status_code as status
| orderby $m.timestamp asc
```

### Mask sensitive data

```
source logs last 1h
| redact log.msg matching /\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}/ to '[IP]'
| orderby $m.timestamp asc
```

## External References

- [Dataprime Reference (IBM Cloud Docs)](https://cloud.ibm.com/docs/cloud-logs?topic=cloud-logs-dataprime-ref)
- [Dataprime Quick Start (IBM Cloud Docs)](https://cloud.ibm.com/docs/cloud-logs?topic=cloud-logs-dataprime-qs)
- [Dataprime Glossary (Coralogix)](https://coralogix.com/docs/dataprime/dataprime-glossary-operators-and-expressions/)
