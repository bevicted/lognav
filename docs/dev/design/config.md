# Configuration

`internal/config` owns lognav's versioned YAML configuration. It returns a
`*config.Config` through `deps.Bundle`; there are no global config or state
singletons. This keeps components and tests isolated.

**Siblings:** [overview](overview.md) ; [ICL](icl.md). The user-facing reference
is the running CLI: `lognav config describe`, `lognav config get`, and
`lognav config show`.

## Loading and validation

Configuration has four layers: public built-ins from `config.New()`, optional
Homebrew defaults, optional `$XDG_CONFIG_HOME/lognav/system.yaml`, then
`$XDG_CONFIG_HOME/lognav/config.yaml`. The XDG paths fall back to
`~/.config/lognav/`. Homebrew defaults use the generic
`config.PackageConfigPath` string stamped with Go `-X`; ordinary builds leave
it empty. All file layers are optional when missing, but a present layer must
be readable and valid. Homebrew and system files are read-only. Mappings merge
recursively while scalars and sequences replace lower values, so an omitted
user list inherits and `[]` intentionally clears it. `config path` only
resolves the sparse editable user path, never the Homebrew or system path; it
does not create, read, or validate any file.

The parser rejects unknown fields. Every loaded configuration is validated,
including custom leaf decoders and the effective ICL instance set. Reject missing
instance names or CRNs, duplicate effective names or CRNs, and names containing
`/`. Unknown and removed keys must fail loudly rather than being silently
stripped.

The file has a `version` header. Version 1 remains the current configuration
version for this extraction. The obsolete instance fields `defaultInstances`,
`extraInstances`, and `includeDefaultInstances` were intentionally removed
without migration or aliases; strict parsing rejects them. This is an approved
breaking extraction exception, not a general permission for future silent
schema changes. An unsupported explicit version is an error. A missing header
is accepted without rewriting on reads; edits creating a file write version 1.

## Metadata and read surfaces

`desc` tags on fields in `layout.go` supply descriptions. Reflected `Config`
field types and shape supply types and structure, `config.New`/`newConfig` supply
defaults, and the passed config supplies current values. This live metadata drives
`config describe` and its JSON output, so no generated Markdown reference can
drift from the configuration shape.

`config show` renders the effective display-only configuration. `config get`
reads one simple dotted key from the same view. Both include merged values and
redact API keys without changing live credentials. Metadata `default` remains
only the public built-in value, while `current` is the merged value.
`icl.instances` is the writable ICL
instance list. `icl.environments` is a writable map leaf keyed by CRN CName;
its records contain `iamURL`, `apiKey`, and `apiKeyOpRef`. 1Password references
remain visible, while nested API keys are redacted.

`EffectiveInstances` is the definition of the active instance set: the
configured `icl.instances` list. It returns a new slice so callers cannot
mutate config through its backing storage. Runtime identity is the CRN; names
are display labels.

Shell completion derives `config get`, `describe`, `set`, and `unset` keys
from this metadata, including the writable `icl.instances` and
`icl.environments` leaves. Map entries do not become dotted editable keys.

## Edits and defaults

`config set` and `config unset` operate only on configurable leaves in the user
file. They edit the YAML AST so comments, unrelated keys, and on-disk secret
values survive. The proposed document is strictly parsed and validated against
the Homebrew and system base before a secure write; neither reads nor edits
write either lower file. `unset` removes only a user override, exposing an
inherited system, Homebrew, or public value. `instances: null` is invalid; use `[]` for no
instances. This edit path intentionally works even when the old target value
makes a full runtime load fail, so it can be repaired.

`config.New()` is the single source of defaults for metadata, tests, and missing
files. Keep defaults and field tags in `layout.go`; dependency injection carries
the resulting config and per-session `state.Manager` to consumers. Tests use
`depstest.NewTest(t)` for a bundle or `statetest.NewTestManager(t)` for state,
and run `t.Parallel()` unless they share OS-level state.

## Startup templates

Default query and enabled snippet text are resolved once at query-editor startup
from one captured local time. Persisted config, redirected input, saved queries,
restored snapshots, injected clauses, and external-editor results remain
literal. This prevents user data from being unexpectedly re-templated.
