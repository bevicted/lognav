# IBM Cloud Logs Client

`internal/icl` is the IBM Cloud Logs (ICL) boundary. It owns Dataprime HTTP,
stream decoding, and IAM credentials; it has no TUI dependency. The UI and
headless `query` command supply caller context, callbacks, and presentation.

**Siblings:** [overview](overview.md) ; [runtime](runtime.md) ;
[archive](./archive.md) ; [snapshot](snapshot.md) ; [configuration](./config.md).

## Synchronous queries

`Query` POSTs Dataprime requests to `/v1/query` and consumes its SSE response.
The default `logs.maxRows` is `0`. Requests use it as `metadata.limit`: `0`
maps to `50000`, while a positive value is sent unchanged for ICL to validate. The TUI
local store remains bounded to the supported 50,000-row synchronous ceiling.

The request context has a five-minute maximum, without an HTTP client-wide
timeout, so cancellation remains controlled by the caller. This preserves
long-lived SSE behavior while still bounding a query.

`QueryCallback` is the streaming contract:

- SSE comments are ignored. Empty events are keep-alives; `data:` event lines
  form one decoded item.
- Setup failures, including non-2xx responses, are returned and do not call the
  callback. The caller reports them through `OnError`.
- Receive and parse failures call `OnError`; the streaming path then calls
  `OnClose` exactly once. A clean EOF also closes exactly once.
- Non-2xx reasons are made suitable for terminal display: the ICL error
  envelope is decoded when possible and control characters are removed.

The client is hand-written `net/http`, rather than the IBM SDK, to keep the
streaming protocol, cancellation, and consumed wire model under this boundary.

## Authentication

An `AccountManager` resolves credentials independently for every configured
`icl.environments` CName. Within an environment it derives the account from the
full CRN. A usable cached access token for that account is returned first.
Otherwise, a persisted refresh token is tried before the configured API key and
1Password API-key reference. Production `bluemix` alone may be overridden by
`LOGNAV_IC_API_KEY`.

Refresh exchanges omit the optional IAM `account` form field, matching IBM's
SDK refresh flow; the resulting access token is still cached under the target
account ID. A resolved 1Password API key is cached for the remainder of the
process, so a successful environment lookup is reused. A rejected IAM refresh
grant clears that refresh token and continues through the configured credential sources;
transport and parse failures return an error and leave it available for a later
retry. A configured credential lookup or exchange failure is returned
immediately. The TUI may request a passcode when no noninteractive credential
remains. `GetAuthTokenNoPasscode`, used by headless callers, instead stops with
`HeadlessAuthRequiredError` when passcode authentication would be needed.

Refresh tokens are a plaintext JSON map by environment in `session.json`.
`SessionPath` only resolves its location. `SaveSession` creates or repairs the
parent as `0700` and atomically replaces a `0600` file after write and sync.
This protects replacement from partial writes, not concurrent process updates
or at-rest disclosure.

## Background queries

An ICL **background query** is server work used by the local [archive](./archive.md)
registry. It is not a synchronous query and it is not a collected snapshot.
The endpoints are:

| Operation | Endpoint                                         | Result                                |
| --------- | ------------------------------------------------ | ------------------------------------- |
| Submit    | `POST /api/v1/dataprime/background-query`        | server `queryId`                      |
| Status    | `POST /api/v1/dataprime/background-query/status` | running, success, error, or not found |
| Data      | `POST /api/v1/dataprime/background-query/data`   | finished NDJSON result                |
| Cancel    | `POST /api/v1/dataprime/background-query/cancel` | server data is deleted                |

Submit uses top-level camelCase `query`, `syntax`, `startDate`, and `endDate`.
The data endpoint returns NDJSON batches and is decoded with `encoding/json`.
Its `userData` field is camelCase, unlike the synchronous `user_data` field.
Batches are converted to the normal stream item shape before callback delivery.

A finished server result can contain up to 1,000,000 rows. `FetchBackgroundData`
accepts an optional delivery cap; it trims the final batch and treats reaching
the cap as successful completion. It uses a 30-minute maximum context because a
finished result can be large. A successful download calls `OnClose` once; setup,
HTTP, and NDJSON errors are returned for the caller to report through `OnError`.
A non-success data response must therefore not become a silent zero-row result.

Status maps successful termination to success and every other terminal state to
error. A genuine HTTP 404, or a successful response explicitly saying the query
does not exist, is not found and is the expiry signal. Other HTTP and transport
errors remain transient errors; treating their incidental text as expiry would
permanently freeze a query that might still be fetched.
