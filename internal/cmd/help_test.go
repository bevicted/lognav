package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func commandHelp(t *testing.T, args ...string) string {
	t.Helper()
	root := newRootCmd(failingSetup())
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(append(args, "--help"))
	require.NoError(t, root.Execute())
	return output.String()
}

func TestLongHelpOmitsGeneratedHelpDetails(t *testing.T) {
	t.Parallel()

	root := newRootCmd(failingSetup())
	tests := []struct {
		args     []string
		dontWant []string
	}{
		{nil, []string{"--adopt moves", "--no-config launches", "Run `lognav completion"}},
		{[]string{"version"}, []string{"Text is the default; JSON is available"}},
		{[]string{"config", "show"}, []string{"Show the complete effective configuration", "Use `config edit`, `set`, or `unset`"}},
		{[]string{"config", "get"}, []string{"Get exactly one effective configuration value", "use `config show`"}},
		{[]string{"config", "describe"}, []string{"Pass a dotted key to narrow output", "Use this command as the configuration field reference"}},
		{[]string{"query"}, []string{"Select targets with `--all`", "`--first` races targets", "Positional arguments are rejected", "With `--tee`, stdout is native-shape NDJSON"}},
		{[]string{"login"}, []string{"Repeat `--environment`", "--no-open leaves opening it to you"}},
		{[]string{"docs"}, []string{"--no-open skips the browser", "--print writes only", "--greppable writes every"}},
		{[]string{"completion"}, []string{"Generate the autocompletion script", "Install completion for the Bash"}},
		{[]string{"instruct"}, []string{"There is no verbosity flag"}},
		{[]string{"snapshot"}, []string{"Managed selectors are", "Use `path` to print"}},
		{[]string{"snapshot", "inspect"}, []string{"Accepts a managed name"}},
		{[]string{"snapshot", "logs"}, []string{"with multiple non-empty instances it is required", "Accepts a managed name"}},
		{[]string{"snapshot", "prune"}, []string{"`--older-than` accepts a Go duration"}},
		{[]string{"snapshot", "adopt"}, []string{"copy with `-c`", "`-y/--yes`", "`--force` also"}},
		{[]string{"snapshot", "clip"}, []string{"with `--path`"}},
	}

	for _, tt := range tests {
		cmd := root
		if len(tt.args) > 0 {
			var err error
			cmd, _, err = root.Find(tt.args)
			require.NoError(t, err)
		}
		for _, unwanted := range tt.dontWant {
			assert.NotContains(t, cmd.Long, unwanted, strings.Join(tt.args, " "))
		}
	}
}

func TestCoreCommandHelpContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "root launch and adoption",
			want: []string{
				"Optionally open a managed snapshot name, `latest`, or an external .lognav path.",
				"Use `lognav -- <name>` when a snapshot name conflicts with a subcommand.",
				"`--adopt` cannot be used with a name or `latest`",
				"fails rather than overwriting an existing snapshot.",
				"config's `path`, `set`, `unset`, and `edit` remain available when user.yaml is invalid.",
				"lognav --adopt ~/Downloads/demo.lognav",
			},
		},
		{
			name: "completion installation",
			args: []string{"completion"},
			want: []string{
				"lognav completion --install",
				"lognav completion zsh --install",
				"Bash requires the bash-completion package.",
				"PowerShell installation requires --profile",
				"Start a new shell session after installation.",
			},
		},
		{
			name: "version formats",
			args: []string{"version"},
			want: []string{
				"config and snapshot formats it supports.",
				"`lognav --version` and `lognav -v` print the text form without reading user.yaml.",
			},
		},
		{
			name: "instruct offline routing",
			args: []string{"instruct"},
			want: []string{
				"load exactly one smallest matching topic from `config`, `query`, or `investigation`; a topic can contain one or more procedures.",
				"`investigation` already includes the Query procedure, then adds retained-evidence analysis; do not load both.",
				"never reads config, checks credentials, reads snapshots, or contacts the network.",
				"lognav instruct investigation",
			},
		},
		{
			name: "config behavior",
			args: []string{"config"},
			want: []string{
				"Configuration reads merge public defaults, optional read-only Homebrew defaults, optional system YAML, and sparse `user.yaml` overrides.",
				"remain available when user.yaml is missing or invalid.",
			},
		},
		{
			name: "config show effective only",
			args: []string{"config", "show"},
			want: []string{
				"display-only and cannot be edited or round-tripped as user.yaml.",
				"merged values, redacted secrets, and computed values",
				"lognav config show -o json",
			},
		},
		{
			name: "config get effective value",
			args: []string{"config", "get"},
			want: []string{
				"Missing keys are errors.",
				"`icl.instances` is the writable list of configured targets",
			},
		},
		{
			name: "config describe field reference",
			args: []string{"config", "describe"},
			want: []string{
				"Output includes types, defaults, and current values",
			},
		},
		{
			name: "config set safe write",
			args: []string{"config", "set"},
			want: []string{
				"rejected values do not change the file.",
				"An invalid value can be replaced in an otherwise invalid file",
				"preserving comments and other fields.",
			},
		},
		{
			name: "config unset defaults",
			args: []string{"config", "unset"},
			want: []string{
				"inherited system, Homebrew, or public default applies afterward.",
				"An absent field is a no-op.",
				"An invalid value can be removed from an otherwise invalid file",
			},
		},
		{
			name: "config path safe lookup",
			args: []string{"config", "path"},
			want: []string{
				"Output is exactly one sparse editable user.yaml path and a newline, never the read-only package defaults path.",
				"does not load, validate, or create the file or its parent directory",
			},
		},
		{
			name: "config edit editor contract",
			args: []string{"config", "edit"},
			want: []string{
				"simple whitespace-separated arguments such as `code --wait`",
				"quote parsing and shell expansion are not supported.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := commandHelp(t, tt.args...)
			for _, want := range tt.want {
				assert.Contains(t, output, want)
			}
		})
	}
}

func TestSnapshotHelpContracts(t *testing.T) {
	t.Parallel()

	group := commandHelp(t, "snapshot")
	for _, want := range []string{
		"Snapshot instances are keyed by full ICL CRN",
		"`inspect` and `logs` load current config to display names",
		"do not load config",
		"Non-empty version 1 snapshots without persisted size metadata are rejected.",
		"`latest` is reserved",
	} {
		assert.Contains(t, group, want)
	}

	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"snapshot", "list"}, []string{
			"List saved snapshots newest first.",
			"ID is a 7-character UUID prefix",
			"auto-YYYYMMDD-HHMMSS[-N].lognav",
			"kind `auto`; every other finalized snapshot has kind `manual`",
			"JSON output is an array, including `[]` for an empty directory.",
			"non-null `in_use_by` PID array",
			"lognav snapshot list -o json",
		}},
		{[]string{"snapshot", "inspect"}, []string{
			"full UUID, in-use PIDs",
			"native-NDJSON size",
			"checked `total_logs_size_bytes`",
			"An external path is useful for inspecting a downloaded snapshot before adopting it.",
			"lognav snapshot inspect latest -o json",
		}},
		{[]string{"snapshot", "logs"}, []string{
			"Each compact line is the ICL-native log object",
			"no timestamp or severity prefix, wrapper, or duplicated metadata.",
			"exactly one instance has logs",
			"colliding display label is rejected and lists CRNs",
			"lognav snapshot logs ~/Downloads/demo.lognav | jq .",
		}},
		{[]string{"snapshot", "rm"}, []string{
			"Deletion does not prompt.",
			"resolved independently",
			"Other resolved, free targets are still deleted",
			"exit 66, which takes precedence over an in-use skip's exit 69",
			"lognav snapshot rm valid missing",
		}},
		{[]string{"snapshot", "prune"}, []string{
			"orphaned in-progress `.wip` backing files",
			"A live session's `.wip` is never touched.",
			"apply only to snapshots selected by a kind flag",
			"In-use files are skipped in real and dry runs.",
			"lognav snapshot prune --auto --older-than 7d",
		}},
		{[]string{"snapshot", "path"}, []string{
			"Output is exactly one path.",
			"External paths are not accepted.",
			"lognav snapshot path latest",
		}},
		{[]string{"snapshot", "adopt"}, []string{
			"In-progress `.wip` files cannot be adopted.",
			"lognav shows a summary and asks `[y]/n`.",
			"`--name` applies to one source only and cannot be `latest`.",
			"lognav snapshot adopt ~/Downloads/old.lognav --force",
		}},
		{[]string{"snapshot", "clip"}, []string{
			"An ID may be given as any unambiguous prefix",
			"default copies the file object",
			"other platforms copy the absolute path string.",
			"Clipboard failures exit 1.",
			"External paths are not accepted.",
		}},
	}
	for _, tt := range tests {
		output := commandHelp(t, tt.args...)
		for _, want := range tt.want {
			assert.Contains(t, output, want, strings.Join(tt.args, " "))
		}
	}
}

func TestQueryHelpScriptContracts(t *testing.T) {
	t.Parallel()

	output := commandHelp(t, "query")
	for _, want := range []string{
		"configured names and full ICL CRNs that resolve to the same CRN run once.",
		"With `--first` and no other selector, all effective configured instances are selected.",
		"only the winner emits records",
		"winner alone determines diagnostics and exit status",
		"All selectors are validated before input is read or authentication starts.",
		"Redirected stdin is sent verbatim, including an empty query.",
		"seeded with the resolved `icl.defaultQuery` and, when enabled, resolved snippets",
		"Without `--tee`, stdout contains only the retained snapshot selector when logs were saved.",
		"Automatic retention runs before selector output, so stdout is empty when it does not retain the snapshot.",
		"Capture it before checking status: a partial result can print a selector and still fail.",
		"`--tee` records have no instance wrapper or selector; records from concurrent targets can interleave, writes synchronously backpressure a slow consumer",
		"A later target failure can leave valid partial NDJSON already consumed.",
		"Progress, member diagnostics, and the final snapshot outcome go to stderr.",
		"Redirected stderr has finite plain output without ANSI control sequences.",
		"`--tee` suppresses progress but retains diagnostics and the final outcome on stderr.",
		"`0 logs; no snapshot created`",
		"A retained snapshot includes every selected target, including failed and zero-result states.",
		"Cancellation leaves no finalized snapshot.",
		"65 when every member rejects the Dataprime query; 69 when every member is unavailable or cannot authenticate.",
		"Headless query never prompts for a passcode.",
	} {
		assert.Contains(t, output, want)
	}
}

func TestAuthenticationAndDocsHelpContracts(t *testing.T) {
	t.Parallel()

	login := commandHelp(t, "login")
	for _, want := range []string{
		"uses configured IAM discovery endpoints but deliberately omits configured API keys, 1Password references, and an IBM Cloud CLI session.",
		"Terminal stdin is required",
		"passcodes are read with echo disabled and cannot be passed as an argument or redirected.",
		"Each successful environment is saved immediately in the plaintext session file",
		"concurrent lognav processes can overwrite one another's updates.",
		"cached access token, then saved refresh token, then configured API key or 1Password reference.",
		"If IAM rejects a saved refresh token, lognav clears it and continues to a configured credential.",
		"A configured credential failure returns immediately.",
		"`lognav query` never prompts.",
	} {
		assert.Contains(t, login, want)
	}

	logout := commandHelp(t, "logout")
	assert.Contains(t, logout, "Configured API keys and 1Password references are not removed")
	assert.Contains(t, logout, "succeeds when no session file exists.")

	docs := commandHelp(t, "docs")
	for _, want := range []string{
		"A named topic is lowercased and must match an embedded user-page filename; an unknown topic is a usage error before a URL is printed or browser launched.",
		"With `--print`, no topic selects the index and no URL is emitted.",
		"`--greppable` includes every embedded user page and blank line in stable filename and line order.",
		"It accepts no topic and cannot be combined with `--print`.",
	} {
		assert.Contains(t, docs, want)
	}
}
