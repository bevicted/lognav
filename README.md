# lognav

[lognav](https://github.com/bevicted/lognav) is a terminal UI and CLI for
searching IBM Cloud Logs. It supports parallel Dataprime queries, jq filtering,
snapshots, and optional API-key or 1Password authentication.

## Install

With Go:

```sh
go install -trimpath github.com/bevicted/lognav@latest
lognav completion --install
```

Or build from source:

```sh
git clone https://github.com/bevicted/lognav.git
cd lognav
mkdir -p bin
go build -trimpath -o bin/lognav .
./bin/lognav completion --install
```

## Configure an instance

lognav has no configured instances by default. Use `./bin/lognav config status`
to find and diagnose the `user.yaml` file, then add an IBM Cloud Logs instance:

```yaml
version: 1
icl:
  instances:
    - name: production
      crn: "crn:v1:bluemix:public:logs:us-south:a/<account-id>:<instance-id>::"
```

Replace the placeholder CRN values with the instance's IBM Cloud Resource Name.

Existing `config.yaml` files are not loaded or changed. If `user.yaml` does not
exist, rename `config.yaml` manually. If both files exist, reconcile them
manually without overwriting either file.

`config status` reports `homebrew`, `system`, and `user` file layers as
`valid`, `missing`, or `error`, followed by effective configuration validation.
Use `./bin/lognav config status -o json` for scripts; the editable path is the
`path` of the `user` record in `files`.

Run `./bin/lognav` and complete the IAM browser/passcode prompt, or use
`./bin/lognav login` first. See [the embedded setup guide](docs/README.md) and run
`./bin/lognav --help` or `./bin/lognav config describe` to discover commands and
configuration fields. If you installed with `go install`, use `lognav` instead of
`./bin/lognav`.

## License

lognav is licensed under the [Apache License 2.0](LICENSE).
