package cmd

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/docs"
	"github.com/bevicted/lognav/internal/build"
)

func TestDocsURL(t *testing.T) {
	t.Parallel()

	const repo = "https://github.com/bevicted/lognav"

	tests := []struct {
		name  string
		ref   string
		topic string
		want  string
	}{
		{"no topic opens the index", "v0.1.1", "", repo + "/blob/v0.1.1/docs/user/documentation.md"},
		{"topic opens that page", "ed33a7ea2bde", "authentication", repo + "/blob/ed33a7ea2bde/docs/user/authentication.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, docsURL(repo, tt.ref, tt.topic))
		})
	}
}

func TestEmbeddedUserDocuments(t *testing.T) {
	t.Parallel()

	filesystem := fstest.MapFS{
		"README.md":        {Data: []byte("overview\n")},
		"user/zebra.md":    {Data: []byte("zebra\n")},
		"user/alpha.md":    {Data: []byte("alpha\n")},
		"user/ignored.txt": {Data: []byte("ignored\n")},
	}

	got, err := embeddedUserDocuments(filesystem)
	require.NoError(t, err)
	assert.Equal(t, []embeddedDocument{
		{topic: "alpha", path: "user/alpha.md"},
		{topic: "zebra", path: "user/zebra.md"},
	}, got)
}

func TestResolveDocumentationTopic(t *testing.T) {
	t.Parallel()

	filesystem := fstest.MapFS{
		"user/documentation.md":  {Data: []byte("index\n")},
		"user/authentication.md": {Data: []byte("authentication\n")},
	}
	tests := []struct {
		name    string
		topic   string
		want    embeddedDocument
		wantErr string
	}{
		{"bare topic selects index", "", embeddedDocument{topic: "documentation", path: "user/documentation.md"}, ""},
		{"named topic selects page", "authentication", embeddedDocument{topic: "authentication", path: "user/authentication.md"}, ""},
		{"unknown topic fails", "missing", embeddedDocument{}, `unknown documentation topic "missing"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveDocumentationTopic(filesystem, tt.topic)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWriteGreppableDocumentation(t *testing.T) {
	t.Parallel()

	filesystem := fstest.MapFS{
		"README.md":               {Data: []byte("overview\n\n")},
		"user/zebra.md":           {Data: []byte("\nlast\n")},
		"user/alpha.md":           {Data: []byte("alpha")},
		"user/empty.md":           {Data: []byte{}},
		"user/not-a-document.txt": {Data: []byte("ignored\n")},
	}
	var output bytes.Buffer

	require.NoError(t, writeGreppableDocumentation(&output, filesystem))
	assert.Equal(t, "alpha.md:1:alpha\n"+
		"zebra.md:1:\n"+
		"zebra.md:2:last\n", output.String())
}

// TestDocs_OutputModesAndBrowserLaunch verifies that embedded output never
// launches a browser while normal and --no-open invocations retain URL behavior.
// It overrides the package-var openBrowser, so it must not run in parallel.
//
//nolint:paralleltest // overrides package-var openBrowser
func TestDocs_OutputModesAndBrowserLaunch(t *testing.T) {
	greppableOutput := renderGreppableDocumentation(t, docs.Docs)
	tests := []struct {
		name       string
		args       []string
		wantOutput string
		wantLaunch int
		wantURL    bool
	}{
		{"default launches the browser", []string{"authentication"}, "", 1, true},
		{"no-open lowercases a valid topic and skips the launch", []string{"AUTHENTICATION", "--no-open"}, "", 0, true},
		{"named print skips the launch", []string{"authentication", "--print"}, readEmbeddedDocumentation(t, "user/authentication.md"), 0, false},
		{"bare print skips the launch", []string{"--print"}, readEmbeddedDocumentation(t, "user/documentation.md"), 0, false},
		{"greppable skips the launch", []string{"--greppable"}, greppableOutput, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opened int
			original := openBrowser
			openBrowser = func(context.Context, string) error { opened++; return nil }
			t.Cleanup(func() { openBrowser = original })

			cmd := initDocs()
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs(tt.args)

			require.NoError(t, cmd.Execute())
			assert.Equal(t, tt.wantLaunch, opened)
			assert.Empty(t, stderr.String())
			if tt.wantURL {
				assert.Contains(t, stdout.String(), "/docs/user/authentication.md")
			} else {
				assert.Equal(t, tt.wantOutput, stdout.String())
			}
		})
	}
}

func TestDocsHelpDescribesGreppableOutputAndTopics(t *testing.T) {
	t.Parallel()

	cmd := initDocs()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--help"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, output.String(), "Print embedded user documentation as filename:line:text")
	assert.Contains(t, output.String(), "--greppable")
	legacyGrepFlag := "--" + "grep"
	assert.NotContains(t, output.String(), legacyGrepFlag+" |")

	documents, err := embeddedUserDocuments(docs.Docs)
	require.NoError(t, err)
	for _, document := range documents {
		assert.Contains(t, output.String(), document.topic)
	}
}

// TestDocsUsageErrors verifies invalid modes and topics do not write URLs or launch browsers.
// It overrides the package-var openBrowser, so it must not run in parallel.
//
//nolint:paralleltest // overrides package-var openBrowser
func TestDocsUsageErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"removed config topic", []string{"config"}, "unknown documentation topic"},
		{"removed commands topic", []string{"commands", "--print"}, "unknown documentation topic"},
		{"unknown ordinary topic", []string{"missing"}, "unknown documentation topic"},
		{"unknown no-open topic", []string{"missing", "--no-open"}, "unknown documentation topic"},
		{"unknown print topic", []string{"missing", "--print"}, "unknown documentation topic"},
		{"greppable rejects topic", []string{"config", "--greppable"}, "--greppable does not accept a topic"},
		{"print and greppable conflict", []string{"--print", "--greppable"}, "--print and --greppable cannot be used together"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opened int
			original := openBrowser
			openBrowser = func(context.Context, string) error { opened++; return nil }
			t.Cleanup(func() { openBrowser = original })

			cmd := initDocs()
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			require.Error(t, err)
			assert.Equal(t, ExitUsage, ExitCode(err))
			require.ErrorContains(t, err, tt.wantErr)
			assert.Zero(t, opened)
			assert.NotContains(t, stdout.String(), "https://")
			assert.NotContains(t, stdout.String(), "docs/user/")
		})
	}

	cmd := initDocs()
	assert.Nil(t, cmd.Flags().Lookup("grep"))
	legacyGrepFlag := "--" + "grep"
	cmd.SetArgs([]string{legacyGrepFlag})
	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorContains(t, err, "unknown flag: "+legacyGrepFlag)
}

func TestDocsCompletionUsesEmbeddedTopics(t *testing.T) {
	t.Parallel()

	documents, err := embeddedUserDocuments(docs.Docs)
	require.NoError(t, err)
	want := make([]string, len(documents))
	for i, document := range documents {
		want[i] = document.topic
	}

	got, directive := completeDocumentationTopics(nil, nil, "")
	assert.Equal(t, want, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	got, directive = completeDocumentationTopics(nil, nil, "auth")
	assert.Equal(t, []string{"authentication"}, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	for _, command := range []string{"docs", "man", "manual"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			root := newRootCmd(failingSetup())
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs([]string{"__complete", command, ""})
			require.NoError(t, root.Execute())
			for _, topic := range want {
				assert.Contains(t, output.String(), topic)
			}
		})
	}
}

// TestDocsAliasCompletionBypassesMalformedConfig verifies aliases do not load
// configuration during hidden completion.
//
//nolint:paralleltest // t.Setenv modifies process-global XDG_CONFIG_HOME
func TestDocsAliasCompletionBypassesMalformedConfig(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	configPath := filepath.Join(xdgHome, "lognav", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o700))
	require.NoError(t, os.WriteFile(configPath, []byte("version: 1\ncore:\n  enableMouse: not-a-bool\n"), 0o600))

	for _, alias := range []string{"man", "manual"} {
		t.Run(alias, func(t *testing.T) {
			root := newRootCmd(defaultSetup)
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs([]string{"__completeNoDesc", alias, ""})

			require.NoError(t, root.Execute())
			assert.Contains(t, output.String(), "authentication")
		})
	}
}

func readEmbeddedDocumentation(t *testing.T, path string) string {
	t.Helper()
	contents, err := fs.ReadFile(docs.Docs, path)
	require.NoError(t, err)
	return string(contents)
}

func TestDocsBrowserOpenerFailureIsDiagnosticOnly(t *testing.T) { //nolint:paralleltest // overrides process-global slog and openBrowser
	originalBrowser := openBrowser
	openBrowser = func(context.Context, string) error { return errors.New("opener failed") }
	t.Cleanup(func() { openBrowser = originalBrowser })

	originalLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	cmd := initDocs()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"authentication"})

	require.NoError(t, cmd.Execute())
	assert.Equal(t, docsURL(build.RepoURL(), build.VersionRef(), "authentication")+"\n", stdout.String())
	assert.Empty(t, stderr.String())
	assert.Contains(t, logs.String(), `"msg":"open documentation browser"`)
	assert.Contains(t, logs.String(), `"error":"opener failed"`)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestDocsURLWriteFailureSkipsBrowser(t *testing.T) { //nolint:paralleltest // overrides package-var openBrowser
	var opened int
	original := openBrowser
	openBrowser = func(context.Context, string) error { opened++; return nil }
	t.Cleanup(func() { openBrowser = original })

	cmd := initDocs()
	cmd.SetOut(failingWriter{})
	cmd.SetArgs([]string{"authentication"})

	err := cmd.Execute()
	require.Error(t, err)
	require.ErrorContains(t, err, "write failed")
	assert.Zero(t, opened)
}

func renderGreppableDocumentation(t *testing.T, filesystem fs.FS) string {
	t.Helper()
	var output bytes.Buffer
	require.NoError(t, writeGreppableDocumentation(&output, filesystem))
	return output.String()
}
