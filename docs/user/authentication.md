# Authentication

lognav authenticates with IBM Cloud IAM for each configured ICL environment. An
instance CRN selects its environment. Public defaults configure the production
`bluemix` environment; add another `icl.environments` entry when an instance
uses a different CName.

## Credential order

For an instance account, lognav uses the first available source in this order:

1. A valid cached access token
2. A saved refresh token
3. A configured API key
4. A configured 1Password API-key reference
5. An interactive IAM passcode, in the TUI or `lognav login`

If IAM rejects a saved refresh token, lognav clears it and continues to a
configured API key or 1Password reference. A configured credential
lookup/exchange failure is returned as an error. When no configured credential
remains, the TUI opens the passcode URL in the default browser and asks for the
passcode. Set `core.openBrowser: false` to keep the URL in the dialog without
opening it. The headless `lognav query` command returns an
authentication-required error and does not prompt.

Access tokens are cached per account. A refresh token is shared by accounts in
the same environment.

## API keys and configuration

A user API key inherits the access assigned to its user identity, including
applicable policies in other accounts unless cross-account access is restricted.
It does not need to come from a personal account. See IBM's documentation for
[user API keys](https://cloud.ibm.com/docs/iam?topic=iam-userapikey),
[cross-account access](https://cloud.ibm.com/docs/account?topic=account-cross-acct),
and [service ID API keys](https://cloud.ibm.com/docs/iam?topic=iam-serviceidapikeys).
Service ID API keys inherit the service ID's access instead.

Every configured environment defaults `apiKeyEnvVar` to `IC_API_KEY`. Set an
environment's `icl.environments.<cname>.apiKeyEnvVar` to use a custom variable
instead; it replaces `IC_API_KEY`, even when `IC_API_KEY` is set. Set the
selector to an explicit empty string to disable environment-variable lookup for
that environment. An unset or empty selected variable leaves the configured API
key and remaining credential order available. Configure API keys and 1Password
references in the matching `icl.environments` entry. Use `lognav config describe
icl.environments` for field details.

> Agents must never ask users to paste API keys, access tokens, refresh tokens, or passcodes.

Credential installation is user-managed; do not pass credentials in chat or
command arguments.

## Login and saved sessions

Run `lognav login` to authenticate before starting the TUI. No argument and
`lognav login auto` use the same credential order as the TUI, including the
selected environment variable, configured API key, and 1Password reference.
Use `lognav login refresh`, `lognav login env`, `lognav login api-key`, or
`lognav login 1password` to require only that credential source. Use
`lognav login passcode` to bypass saved and configured credentials and start
the browser/passcode flow. A forced mode never
falls back to another source; an unavailable or rejected selected credential is
an error. Credential values are never accepted as command arguments. An IBM
Cloud CLI session is not used. If passcode authentication is required, login
uses terminal stdin, prints the passcode URL, and reads the pasted passcode
without echo. It opens the URL when `core.openBrowser` is enabled, which is the
default; `--no-open` overrides that setting. A terminal is required only for
this passcode flow. On success, login reports the credential source it used.
See `lognav login --help` for environment selection.

A successful exchange that returns a refresh token stores it in plaintext
`$XDG_STATE_HOME/lognav/session.json` (normally
`~/.local/state/lognav/session.json`). lognav creates the directory with mode
`0700` and replaces the file with mode `0600` on Unix-like systems. This is not
encrypted storage or a keychain. Concurrent lognav processes can overwrite each
other's session updates.

`lognav logout` removes saved session credentials, not configured API keys or
1Password references.
