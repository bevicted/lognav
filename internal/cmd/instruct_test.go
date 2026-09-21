package cmd

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/docs"
	"github.com/bevicted/lognav/internal/deps"
)

func runInstruct(t *testing.T, setup func(bool) (deps.Bundle, error), args ...string) (string, error) {
	t.Helper()
	root := newRootCmd(setup)
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(append([]string{"instruct"}, args...))
	err := root.Execute()
	return output.String(), err
}

func TestInstructWayfinderRoutesOneSmallestCommand(t *testing.T) {
	t.Parallel()

	output, err := runInstruct(t, failingSetup())
	require.NoError(t, err)
	assert.Equal(t, instructionWayfinder, output)
	assert.Contains(t, output, "`lognav docs <topic> --print`")
	assert.Contains(t, output, "`lognav docs --greppable | rg 'terms'`")
	assert.Contains(t, output, "print the matching filename's topic")
	assert.Contains(t, output, "`lognav instruct config`")
	assert.Contains(t, output, "`lognav instruct query`")
	assert.Contains(t, output, "`lognav instruct investigation`")
	assert.Contains(t, output, "one smallest matching topic")
	assert.Contains(t, output, "topic can contain one or more procedures")
	assert.Contains(t, output, "Do not load redundant topics")
	assert.Contains(t, output, "`investigation` already includes the Query procedure")
	assert.Contains(t, output, "independent requested outcome")
	assert.Contains(t, output, "Ask the user before acting when the goal remains ambiguous")
}

func TestInstructTopicsAreExactAndComplete(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"config", "query", "investigation"}, instructionTopicNames())
	wantHeadings := map[string]string{
		"config":        "# Configuration instructions",
		"query":         "# Query procedure",
		"investigation": "# Query procedure",
	}
	for _, topic := range instructionTopicNames() {
		t.Run(topic, func(t *testing.T) {
			t.Parallel()
			output, err := runInstruct(t, failingSetup(), topic)
			require.NoError(t, err)
			assert.Contains(t, output, wantHeadings[topic])
		})
	}

	for _, args := range [][]string{{"setup"}, {"Setup"}, {"query", "extra"}, {"query", "-v"}, {"-v"}} {
		output, err := runInstruct(t, failingSetup(), args...)
		require.Error(t, err)
		assert.Empty(t, output)
		assert.Equal(t, ExitUsage, ExitCode(err))
	}
}

func TestInstructCompletionReturnsExactTopics(t *testing.T) {
	t.Parallel()

	got, directive := completeInstructionTopics(nil, nil, "")
	assert.Equal(t, []string{"config", "query", "investigation"}, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	got, directive = completeInstructionTopics(nil, nil, "in")
	assert.Equal(t, []string{"investigation"}, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	got, directive = completeInstructionTopics(nil, []string{"query"}, "")
	assert.Empty(t, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

func TestInstructBypassesMalformedConfig(t *testing.T) { //nolint:paralleltest // changes XDG_CONFIG_HOME
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	path := filepath.Join(xdgHome, "lognav", "user.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("version: 1\ncore:\n  enableMouse: not-a-bool\n"), 0o600))

	for _, args := range [][]string{{}, {"config"}, {"query"}, {"investigation"}} {
		output, err := runInstruct(t, defaultSetup, args...)
		require.NoError(t, err)
		assert.NotEmpty(t, output)
	}
}

func TestInstructBypassesConfigLoadAndIsDeterministic(t *testing.T) {
	t.Parallel()

	setupCalled := false
	setup := func(bool) (deps.Bundle, error) {
		setupCalled = true
		return deps.Bundle{}, errors.New("configuration must not load")
	}
	first, err := runInstruct(t, setup, "investigation")
	require.NoError(t, err)
	second, err := runInstruct(t, setup, "investigation")
	require.NoError(t, err)
	assert.False(t, setupCalled)
	assert.Equal(t, first, second)

	root := newRootCmd(failingSetup())
	var version bytes.Buffer
	root.SetOut(&version)
	root.SetErr(&version)
	root.SetArgs([]string{"-v"})
	require.NoError(t, root.Execute())
	assert.Contains(t, version.String(), "lognav version")
}

func TestInstructContentContracts(t *testing.T) {
	t.Parallel()

	config, err := renderInstruction("config")
	require.NoError(t, err)
	assert.Contains(t, config, "lognav config path")
	assert.Contains(t, config, "user.yaml")
	assert.Contains(t, config, "exact prior bytes")
	assert.Contains(t, config, "restore the exact prior bytes")
	assert.Contains(t, config, "lognav config describe <dotted-key>")

	queryProcedure := instructionFragment(t, "instructions/query.md")
	queryCompletion := instructionFragment(t, "instructions/query-completion.md")
	investigationReaction := instructionFragment(t, "instructions/investigation.md")

	query, err := renderInstruction("query")
	require.NoError(t, err)
	assert.Equal(t, queryProcedure+"\n\n"+queryCompletion+"\n", query)
	assert.Equal(t, 1, strings.Count(query, queryProcedure))
	assert.Contains(t, query, "--instance` flags")
	assert.Contains(t, query, "configured instance")
	assert.Contains(t, query, "--first` for one discovery query")
	assert.Contains(t, query, "source logs\nlast 15m")
	assert.Contains(t, query, "Never infer or\n   invent a field name")
	assert.Contains(t, query, "$d ~~ 'text'")
	assert.Contains(t, query, "quoted heredoc")
	assert.Contains(t, query, "selector=$(printf")
	assert.Contains(t, query, "out of memory")
	assert.Contains(t, query, "lognav login")
	assert.NotContains(t, query, "char_to_region_map")
	assert.NotContains(t, query, "subsystemname")
	assert.NotContains(t, query, "$d.log")
	assert.NotContains(t, query, investigationReaction)

	investigation, err := renderInstruction("investigation")
	require.NoError(t, err)
	assert.Equal(t, queryProcedure+"\n\n"+investigationReaction+"\n", investigation)
	assert.Equal(t, 1, strings.Count(investigation, queryProcedure))
	assert.NotContains(t, investigation, queryCompletion)
	assert.Contains(t, investigation, "specifically named retained selector or external snapshot path")
	assert.Contains(t, investigation, "missing or unreadable")
	assert.Contains(t, investigation, "ask before replacing it with a network query")
	assert.Contains(t, investigation, "Inspect every non-empty selector before retrying, regardless of query status")
	assert.Contains(t, investigation, "Never inspect an empty selector")
	assert.Contains(t, investigation, "clean empty result")
	assert.Contains(t, investigation, "state, count, and size")
	assert.Contains(t, investigation, "jq -c '.data.log'")
	assert.Contains(t, investigation, "about 87 percent")
	assert.Contains(t, investigation, "cap every sample at 100 records")
	assert.Contains(t, investigation, "Reuse retained data and local projections or filters")
	assert.Contains(t, investigation, "evidence-supported changes that preserve the requested target and meaning")
	assert.Contains(t, investigation, "Ask before broadening target scope, time scope, or meaning")
	assert.Contains(t, investigation, "Inspect each new result before considering another query")
	assert.NotContains(t, investigation, "instruct query")

	for _, copiedQueryRule := range []string{
		"repeatable `--instance` flags",
		"quoted heredoc",
		"selector=$(printf",
		"Never infer or invent a field name",
		"Do not add a Dataprime `limit`",
	} {
		assert.NotContains(t, investigationReaction, copiedQueryRule)
	}

	allInstructions := strings.Join([]string{config, query, investigation}, "\n")
	for _, forbidden := range []string{
		"| limit",
		"incident_id",
		"incidentID",
		"incidentid",
		"request_id",
		"Time range is REQUIRED",
		"MUST start a new line",
		"20x",
		"-vv",
		"instruct setup",
	} {
		assert.NotContains(t, allInstructions, forbidden)
	}
}

func TestRenderInstructionReportsUnavailableFragment(t *testing.T) {
	t.Parallel()

	output, err := renderInstructionFragments([]string{"instructions/query.md", "instructions/missing.md"})
	require.Error(t, err)
	assert.Empty(t, output)
	assert.ErrorContains(t, err, `"instructions/missing.md"`)
}

func instructionFragment(t *testing.T, path string) string {
	t.Helper()

	contents, err := fs.ReadFile(instructionFiles, path)
	require.NoError(t, err)
	return strings.TrimSpace(string(contents))
}

func TestAgentInstructionAuthenticationDocumentationContract(t *testing.T) {
	t.Parallel()

	authentication, err := docs.Docs.ReadFile("user/authentication.md")
	require.NoError(t, err)
	assert.Contains(t, string(authentication), "never ask users to paste API keys, access tokens, refresh tokens, or passcodes")
	assert.Contains(t, string(authentication), "Credential installation is user-managed")
	assert.Contains(t, string(authentication), "`lognav login`")
}
