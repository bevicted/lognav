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
5. An interactive IAM passcode, in the TUI only

If IAM rejects a saved refresh token, lognav clears it and continues to a
configured API key or 1Password reference. A configured credential
lookup/exchange failure is returned as an error. When no configured credential
remains, the TUI asks for a passcode; the headless `lognav query` command
returns an authentication-required error and does not prompt.

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

`LOGNAV_IC_API_KEY` overrides the configured API key only for the production
`bluemix` environment. Configure API keys and 1Password references in the
matching `icl.environments` entry. Use `lognav config describe icl.environments`
for field details.

> Agents must never ask users to paste API keys, access tokens, refresh tokens, or passcodes.

Credential installation is user-managed; do not pass credentials in chat or
command arguments.

## Login and saved sessions

Run `lognav login` for the browser/passcode flow. It uses terminal stdin,
prints the passcode URL, and reads the pasted passcode without echo. It does
not use configured API keys, 1Password, or an IBM Cloud CLI session. See
`lognav login --help` for environment selection and browser options.

A successful passcode exchange stores a refresh token in plaintext
`$XDG_STATE_HOME/lognav/session.json` (normally
`~/.local/state/lognav/session.json`). lognav creates the directory with mode
`0700` and replaces the file with mode `0600` on Unix-like systems. This is not
encrypted storage or a keychain. Concurrent lognav processes can overwrite each
other's session updates.

`lognav logout` removes saved session credentials, not configured API keys or
1Password references.
