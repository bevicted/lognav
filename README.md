# lognav

[lognav](https://github.com/bevicted/lognav) is a terminal UI and CLI for
searching IBM Cloud Logs. It supports parallel Dataprime queries, jq filtering,
snapshots, and optional API-key or 1Password authentication.

## Install from source

```sh
git clone https://github.com/bevicted/lognav.git
cd lognav
go build -trimpath -o lognav .
./lognav completion --install
```

Alternatively, install the public module with `go install -trimpath
github.com/bevicted/lognav@latest` after a release is available.

## Configure an instance

lognav has no configured instances by default. Create the file printed by
`./lognav config path` and add an IBM Cloud Logs instance:

```yaml
version: 1
icl:
  instances:
    - name: production
      crn: "crn:v1:bluemix:public:logs:us-south:a/<account-id>:<instance-id>::"
```

Replace the placeholder CRN values with the instance's IBM Cloud Resource Name.
Run `./lognav` and complete the IAM browser/passcode prompt, or use `./lognav login`
first. See [the embedded setup guide](docs/README.md) and run `./lognav --help` or
`./lognav config describe` to discover commands and configuration fields. If you
installed with `go install`, use `lognav` instead of `./lognav`.

## License

lognav is licensed under the [Apache License 2.0](LICENSE).
