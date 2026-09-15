package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPublicFlagsHaveShorthands keeps every public application flag available
// through a one-character pflag shorthand. Cobra's generated help flag is the
// only public flag it does not define here.
func TestPublicFlagsHaveShorthands(t *testing.T) {
	t.Parallel()

	root := newRootCmd(noopSetup(t))
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Hidden {
			return
		}
		cmd.InitDefaultHelpFlag()
		checkFlags := func(flags *pflag.FlagSet) {
			flags.VisitAll(func(flag *pflag.Flag) {
				if flag.Hidden {
					return
				}
				if flag.Name == "help" {
					assert.Equal(t, "h", flag.Shorthand, cmd.CommandPath())
					return
				}
				assert.NotEmptyf(t, flag.Shorthand, "%s --%s", cmd.CommandPath(), flag.Name)
			})
		}
		checkFlags(cmd.NonInheritedFlags())
		checkFlags(cmd.PersistentFlags())

		shorthands := map[string]string{}
		cmd.Flags().VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden || flag.Shorthand == "" {
				return
			}
			previous, duplicate := shorthands[flag.Shorthand]
			assert.Falsef(t, duplicate && previous != flag.Name, "%s -%s is assigned to both --%s and --%s", cmd.CommandPath(), flag.Shorthand, previous, flag.Name)
			shorthands[flag.Shorthand] = flag.Name
		})
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
}

func TestNewFlagShorthandsAppearInHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "root", want: []string{"-N, --no-config", "-A, --adopt"}},
		{name: "completion", args: []string{"completion"}, want: []string{"-i, --install", "-p, --profile"}},
		{name: "completion shell", args: []string{"completion", "bash"}, want: []string{"-d, --no-descriptions"}},
		{name: "login", args: []string{"login"}, want: []string{"-e, --environment", "-n, --no-open"}},
		{name: "docs", args: []string{"docs"}, want: []string{"-n, --no-open", "-p, --print", "-g, --greppable"}},
		{name: "query", args: []string{"query"}, want: []string{"-f, --first", "-t, --tee"}},
		{name: "snapshot clip", args: []string{"snapshot", "clip"}, want: []string{"-p, --path"}},
		{name: "snapshot logs", args: []string{"snapshot", "logs"}, want: []string{"-i, --instance"}},
		{name: "snapshot prune", args: []string{"snapshot", "prune"}, want: []string{"-a, --auto", "-m, --manual", "-O, --older-than", "-k, --keep", "-d, --dry-run"}},
		{name: "snapshot adopt", args: []string{"snapshot", "adopt"}, want: []string{"-n, --name"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output := commandHelp(t, tt.args...)
			for _, want := range tt.want {
				assert.Contains(t, output, want)
			}
		})
	}
}

func TestNewFlagShorthandsParseLikeLongForms(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		target []string
		long   []string
		short  []string
		flags  []string
	}{
		{name: "root adoption", long: []string{"--adopt"}, short: []string{"-A"}, flags: []string{adoptFlag}},
		{name: "persistent no config on descendant", target: []string{"snapshot", "list"}, long: []string{"snapshot", "list", "--no-config"}, short: []string{"snapshot", "list", "-N"}, flags: []string{noConfigFlag}},
		{name: "completion values", target: []string{"completion", "bash"}, long: []string{"completion", "bash", "--install", "--profile", "/tmp/profile"}, short: []string{"completion", "bash", "-i", "-p", "/tmp/profile"}, flags: []string{completionInstallFlag, completionProfileFlag}},
		{name: "login repeated values", target: []string{"login"}, long: []string{"login", "--environment", "staging", "--environment", "bluemix", "--no-open"}, short: []string{"login", "-e", "staging", "-e", "bluemix", "-n"}, flags: []string{"environment", "no-open"}},
		{name: "docs output modes", target: []string{"docs"}, long: []string{"docs", "--no-open", "--print", "--greppable"}, short: []string{"docs", "-n", "-p", "-g"}, flags: []string{"no-open", "print", "greppable"}},
		{name: "query selector, first, and tee", target: []string{"query"}, long: []string{"query", "--instance", "prod", "--instance", "stage", "--first", "--tee"}, short: []string{"query", "-i", "prod", "-i", "stage", "-f", "-t"}, flags: []string{"instance", "first", "tee"}},
		{name: "snapshot clip", target: []string{"snapshot", "clip"}, long: []string{"snapshot", "clip", "--path"}, short: []string{"snapshot", "clip", "-p"}, flags: []string{"path"}},
		{name: "snapshot logs instance", target: []string{"snapshot", "logs"}, long: []string{"snapshot", "logs", "--instance", "prod"}, short: []string{"snapshot", "logs", "-i", "prod"}, flags: []string{"instance"}},
		{name: "snapshot prune values", target: []string{"snapshot", "prune"}, long: []string{"snapshot", "prune", "--auto", "--manual", "--older-than", "7d", "--keep", "2", "--dry-run"}, short: []string{"snapshot", "prune", "-a", "-m", "-O", "7d", "-k", "2", "-d"}, flags: []string{"auto", "manual", "older-than", "keep", "dry-run"}},
		{name: "snapshot adopt name", target: []string{"snapshot", "adopt"}, long: []string{"snapshot", "adopt", "--name", "incident"}, short: []string{"snapshot", "adopt", "-n", "incident"}, flags: []string{"name"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, parsedFlagValues(t, tt.target, tt.long, tt.flags), parsedFlagValues(t, tt.target, tt.short, tt.flags))
		})
	}
}

func parsedFlagValues(t *testing.T, target, args, names []string) map[string]string {
	t.Helper()

	root := newRootCmd(noopSetup(t))
	root.SetArgs(append(args, "--help"))
	require.NoError(t, root.Execute())

	cmd := root
	if len(target) > 0 {
		var err error
		cmd, _, err = root.Find(target)
		require.NoError(t, err)
	}
	values := make(map[string]string, len(names))
	for _, name := range names {
		flag := cmd.Flags().Lookup(name)
		require.NotNilf(t, flag, "%s --%s", cmd.CommandPath(), name)
		values[name] = flag.Value.String()
	}
	return values
}
