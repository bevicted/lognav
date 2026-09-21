# lognav

lognav is a terminal UI and CLI for searching IBM Cloud Logs (ICL). It is a
work in progress; configuration, keybindings, and snapshot compatibility can
change before the first major release.

## Features

- Parallel queries across IBM Cloud Logs instances
- Dataprime queries, jq filtering, search, and a severity timeline
- Local snapshots that can be saved, opened, and shared
- Headless queries for scripts and agents
- Browser/passcode login, API-key, and optional 1Password authentication

## Install

Build from the public source repository:

```sh
git clone https://github.com/bevicted/lognav.git
cd lognav
mkdir -p bin
go build -trimpath -o bin/lognav .
./bin/lognav completion --install
```

After a release is available, install the public module instead:

```sh
go install -trimpath github.com/bevicted/lognav@latest
lognav completion --install
```

`lognav completion --install` detects Bash, Zsh, or Fish from `$SHELL`. Run
`lognav completion --help` to select a shell or profile explicitly. Start a new
shell session after installation.

## Quick start

Configure at least one IBM Cloud Logs instance before querying. `icl.instances`
is a writable list; use `[]` when you intentionally want no instances. Save this
in the `user.yaml` path printed by `./bin/lognav config path`:

```yaml
version: 1
icl:
  instances:
    - name: production
      crn: "crn:v1:bluemix:public:logs:us-south:a/<account-id>:<instance-id>::"
```

Replace the placeholder CRN values with your IBM Cloud Logs instance values.

Existing `config.yaml` files are not loaded or changed. If `user.yaml` does not
exist, rename `config.yaml` manually. If both files exist, reconcile them
manually without overwriting either file.

An API key is not required: start lognav and use the IAM browser/passcode flow
when prompted.

```sh
./bin/lognav
```

To authenticate before opening the TUI, use the standalone login command:

```sh
./bin/lognav login
```

The quick-start commands use the `./bin/lognav` binary built above. If you
installed with `go install`, use `lognav` instead.

See [Authentication](user/authentication.md) for saved-session behavior and
optional API-key or 1Password configuration. Edit the rest of your configuration
with `lognav config edit`.

## AI agents

A local AI agent can help configure lognav, run queries, or investigate results.
Start the agent, load lognav's offline instruction router into its context, then
prompt it with what you need:

```text
! lognav instruct
```

The router tells the agent which focused instruction topic to load before it
acts.

## Help and documentation

Press `?` in the TUI for context-sensitive keybindings.

- [User documentation index](user/documentation.md)
- Commands: `lognav --help`, then `lognav <command> --help`
- Configuration: `lognav config describe`, `lognav config get`, and `lognav config show`
- [Dataprime reference](user/dataprime.md)
- Embedded documentation: `lognav docs <topic> --print` or `lognav docs --greppable | rg 'text'`

For development, see [CONTRIBUTING](CONTRIBUTING.md) and
[design documentation](dev/design/).

## Community

Questions, bugs, feature requests, and discussion belong in the
[lognav repository](https://github.com/bevicted/lognav).

## License

lognav is licensed under the [Apache License 2.0](../LICENSE).

## Additional resources

- [jq cheat sheet](https://cht.sh/jq)
- [Dataprime examples](https://cloud.ibm.com/docs/cloud-logs?topic=cloud-logs-dataprime-qs)
- [Dataprime reference](https://cloud.ibm.com/docs/cloud-logs?topic=cloud-logs-dataprime-ref)
- [Dataprime nvim parser](https://github.com/smrtrfszm/dataprime.nvim)
