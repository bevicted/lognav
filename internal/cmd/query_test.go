package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/sessionbus"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/state"
	"github.com/bevicted/lognav/internal/ui/status"
)

// These tests replace process-wide I/O, environment, and command seams.
// They must remain serial.
func TestQuery_SuccessCanBeInspectedAndStreamed(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	oldStream, oldURL, oldTTY, oldResolver, oldManager := queryStream, queryURL, isTTY, resolveQueryToken, newQueryAccountManager
	defer func() {
		queryStream, queryURL, isTTY, resolveQueryToken, newQueryAccountManager = oldStream, oldURL, oldTTY, oldResolver, oldManager
	}()
	isTTY = func() bool { return false }
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "urn:ibm:params:oauth:grant-type:apikey", r.Form.Get("grant_type"))
		assert.Equal(t, "test-key", r.Form.Get("apikey"))
		_, _ = io.WriteString(w, `{"access_token":"token","refresh_token":"rotated","expires_in":3600}`)
	}))
	defer iam.Close()
	newQueryAccountManager = func(environments map[string]config.ICLEnvironmentConfig) *icl.AccountManager {
		manager := icl.NewAccountManager(environments)
		manager.SetOIDCForTest(icl.EnvProd, iam.URL)
		return manager
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/query", r.URL.Path)
		assert.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"result":{"results":[{"user_data":"{\"message\":\"ok\"}"}]}}`+"\n\n")
	}))
	defer server.Close()
	queryURL = func(*config.CRN) string { return server.URL }

	stdout, stderr, err := runQueryCommand(t, cfg, "source logs", "query", "--instance", "test")
	require.NoError(t, err)
	assert.Contains(t, stderr, "1 logs; snapshot")
	selector := string(bytes.TrimSpace([]byte(stdout)))
	require.NotEmpty(t, selector)
	require.Regexp(t, `^auto-\d{8}-\d{6}(?:-\d+)?$`, selector)

	inspect, _, err := runQueryCommand(t, cfg, "", "snapshot", "inspect", selector, "-o", "json")
	require.NoError(t, err)
	assert.Contains(t, inspect, `"query": "source logs"`)
	logs, _, err := runQueryCommand(t, cfg, "", "snapshot", "logs", selector, "--instance", "test")
	require.NoError(t, err)
	assert.Contains(t, logs, `"message":"ok"`)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	container, err := snapshot.OpenContainerReadOnly(filepath.Join(dir, selector+snapshot.FileExt))
	require.NoError(t, err)
	defer func() { _ = container.Close() }()
	state, err := snapshot.LoadState(container)
	require.NoError(t, err)
	require.Len(t, state.InstancePickerSnapshot.Instances, 1)
	assert.Equal(t, uint64(len(logs)), state.InstancePickerSnapshot.Instances[0].LogsSizeBytes,
		"headless state size must exactly match snapshot logs output")
}

// A post-link WIP cleanup error still leaves a complete automatic snapshot.
// The command must publish its selector before returning the cleanup error.
func TestQuery_CleanupFailurePrintsPublishedSelector(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	startedAt := time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	oldStream, oldResolver, oldNow, oldPublish := queryStream, resolveQueryToken, queryNow, queryPublishAutoSnapshot
	defer func() {
		queryStream, resolveQueryToken, queryNow, queryPublishAutoSnapshot = oldStream, oldResolver, oldNow, oldPublish
	}()
	queryStream = queryOneLog
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	queryNow = func() time.Time { return startedAt }
	queryPublishAutoSnapshot = func(wipPath string, gotStartedAt time.Time, _ func(string) error) (string, error) {
		assert.Equal(t, startedAt, gotStartedAt)
		finalPath := filepath.Join(filepath.Dir(wipPath), "auto-20260814-120000.lognav")
		require.NoError(t, os.Link(wipPath, finalPath))
		return finalPath, &snapshot.RenameCleanupError{Err: errors.New("unlink failed")}
	}
	notifier := &recordingBroadcaster{}
	t.Cleanup(setSnapshotNotifier(notifier))

	stdout, stderr, err := runQueryCommand(t, cfg, "source logs", "query", "--instance", "test")
	require.Error(t, err)
	assert.Equal(t, ExitGeneral, ExitCode(err))
	var cleanupErr *snapshot.RenameCleanupError
	require.ErrorAs(t, err, &cleanupErr)
	assert.Equal(t, "auto-20260814-120000\n", stdout)
	assert.Contains(t, stderr, "WIP cleanup failed: finalize snapshot: remove published source: unlink failed")
	assert.NotContains(t, stderr, "snapshot creation failed")
	assert.Equal(t, int64(1), notifier.calls.Load(), "published snapshots retain notification ordering")

	dir, dirErr := snapshot.Dir()
	require.NoError(t, dirErr)
	assert.FileExists(t, filepath.Join(dir, "auto-20260814-120000.lognav"))
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	var wipFound bool
	for _, entry := range entries {
		wipFound = wipFound || strings.HasSuffix(entry.Name(), snapshot.WipSuffix)
	}
	assert.True(t, wipFound, "cleanup-only publication leaves its WIP for stale cleanup")
}

func TestWriteQuerySnapshot_UsesFetchStartForNameAndSaveTimeForState(t *testing.T) { //nolint:paralleltest // queryNow is a package-level clock seam
	dir := t.TempDir()
	startedAt := time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	savedAt := startedAt.Add(7 * time.Second)
	oldNow := queryNow
	queryNow = func() time.Time { return savedAt }
	t.Cleanup(func() { queryNow = oldNow })
	member := newQueryMember(config.ICLInstanceConfig{
		CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/test:alpha::"),
	}, startedAt)
	member.result.logs = []icl.Log{{Data: map[string]any{"message": "ok"}}}
	members := []queryMember{member}

	first, err := writeQuerySnapshot(t.Context(), dir, members, "source logs", startedAt)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "auto-20260814-120000.lognav"), first)
	second, err := writeQuerySnapshot(t.Context(), dir, members, "source logs", startedAt)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "auto-20260814-120000-2.lognav"), second)

	container, err := snapshot.OpenContainerReadOnly(first)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Close() })
	state, err := snapshot.LoadState(container)
	require.NoError(t, err)
	require.Len(t, state.InstancePickerSnapshot.Instances, 1)
	assert.Equal(t, savedAt.UnixMicro(), state.InstancePickerSnapshot.Instances[0].LastUpdateTimeMicro)
}

func TestQuery_SelectorFlagsPreserveSnapshotCRNOrderAndLabels(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := multiQueryTestConfig()
	direct := "crn:v1:test-cloud:public:logs:eu-gb:a/account-direct:123456789::"
	oldStream, oldResolver := queryStream, resolveQueryToken
	defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	queryStream = queryOneLog

	stdout, _, err := runQueryCommand(t, cfg, "source logs", "query",
		"--instance", "stage-a", "--instance", "prod-a",
		"--instance", direct, "--instance", cfg.ICL.Instances[0].CRN.String())
	require.NoError(t, err)
	selector := string(bytes.TrimSpace([]byte(stdout)))
	require.NotEmpty(t, selector)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	container, err := snapshot.OpenContainerReadOnly(filepath.Join(dir, selector+snapshot.FileExt))
	require.NoError(t, err)
	defer func() { require.NoError(t, container.Close()) }()
	wantCRNs := []string{cfg.ICL.Instances[2].CRN.String(), cfg.ICL.Instances[0].CRN.String(), direct}
	assert.Equal(t, []string{"i_" + wantCRNs[0], "i_" + wantCRNs[1], "i_" + wantCRNs[2], "state"}, container.GetFrameNames())
	stateFrame, err := snapshot.LoadState(container)
	require.NoError(t, err)
	gotCRNs := make([]string, len(stateFrame.InstancePickerSnapshot.Instances))
	for i, instance := range stateFrame.InstancePickerSnapshot.Instances {
		gotCRNs[i] = instance.CRN
	}
	assert.Equal(t, wantCRNs, gotCRNs)

	inspect, _, err := runQueryCommand(t, cfg, "", "snapshot", "inspect", selector, "-o", "json")
	require.NoError(t, err)
	assert.Contains(t, inspect, `"name": "stage-a"`)
	assert.Contains(t, inspect, `"name": "prod-a"`)
	assert.Contains(t, inspect, `"name": "eu-gb/12345678"`)
}

func TestQuery_SelectorWriteFailureReturnsError(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	oldStream, oldResolver := queryStream, resolveQueryToken
	defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	queryStream = queryOneLog

	err := runOneQuery(t.Context(), queryErrWriter{}, io.Discard, cfg, cfg.ICL.Instances[0], "source logs")
	require.Error(t, err)
	assert.Equal(t, ExitGeneral, ExitCode(err))
	require.ErrorContains(t, err, "write snapshot selector")
	dir, dirErr := snapshot.Dir()
	require.NoError(t, dirErr)
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Len(t, entries, 1, "the finalized snapshot remains available after selector output fails")
}

func TestRunQueryHandler_RedirectedInputRetentionAndMaxRows(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	cfg.Core.MaxAutoSnapshots = 0
	cfg.Logs.MaxRows = 42
	oldStream, oldTTY, oldResolver := queryStream, isTTY, resolveQueryToken
	defer func() { queryStream, isTTY, resolveQueryToken = oldStream, oldTTY, oldResolver }()
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	isTTY = func() bool { return false }
	var gotQuery string
	var gotMaxRows uint32
	queryStream = func(_ context.Context, _, _, query string, maxRows uint32, cb icl.QueryCallback) error {
		gotQuery, gotMaxRows = query, maxRows
		data := `{}`
		cb.OnData(&icl.StreamItem{Result: &icl.DataprimeResult{Results: []icl.DataprimeResults{{UserData: &data}}}})
		cb.OnClose()
		return nil
	}

	var stdout, stderr bytes.Buffer
	err := runQueryHandler(t.Context(), strings.NewReader("source logs"), &stdout, &stderr, cfg, queryHandlerOptions{selectors: []string{"test"}})
	require.NoError(t, err)
	assert.Equal(t, "source logs", gotQuery)
	assert.Equal(t, uint32(42), gotMaxRows)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "not retained")
}

func TestQuery_DataRejectionExitsData(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	oldStream, oldTTY, oldResolver := queryStream, isTTY, resolveQueryToken
	defer func() { queryStream, isTTY, resolveQueryToken = oldStream, oldTTY, oldResolver }()
	isTTY = func() bool { return false }
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	queryStream = func(_ context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
		message := "invalid query"
		cb.OnData(&icl.StreamItem{Error: &icl.DataprimeError{Message: &message}})
		cb.OnClose()
		return nil
	}
	stdout, _, err := runQueryCommand(t, cfg, "bad", "query", "--instance", "test")
	require.Error(t, err)
	assert.Equal(t, ExitData, ExitCode(err))
	assert.Empty(t, stdout)
}

func TestQuery_CommandOutcomeMatrices(t *testing.T) { //nolint:gocyclo,paralleltest // table exercises command outcomes through real snapshot commands
	tests := []struct {
		name         string
		authErr      func(string) error
		outcomes     map[string]queryTestOutcome
		wantExit     int
		wantSnapshot bool
		wantStates   []status.Phase
		wantMessages []string
		wantErr      []string
	}{
		{
			name: "all success",
			outcomes: map[string]queryTestOutcome{
				"prod-a":  {message: "prod log"},
				"prod-b":  {},
				"stage-a": {message: "stage log"},
			},
			wantSnapshot: true,
			wantStates:   []status.Phase{status.Success, status.Success, status.Success},
			wantMessages: []string{"", "", ""},
		},
		{
			name: "mixed",
			outcomes: map[string]queryTestOutcome{
				"prod-a":  {message: "kept"},
				"prod-b":  {streamErr: errors.New("unknown SSE line")},
				"stage-a": {warning: "limited time range"},
			},
			wantExit:     ExitGeneral,
			wantSnapshot: true,
			wantStates:   []status.Phase{status.Success, status.Error, status.Warning},
			wantMessages: []string{"", "unknown SSE line", "limited time range"},
			wantErr:      []string{"prod-b: error: unknown SSE line"},
		},
		{
			name: "all authentication unavailable",
			authErr: func(name string) error {
				if name == "stage-a" {
					return &icl.HeadlessAuthRequiredError{Env: icl.Environment("test-cloud")}
				}
				return &icl.HeadlessAuthRequiredError{Env: icl.EnvProd}
			},
			wantExit: ExitUnavailable,
			wantErr:  []string{"no noninteractive credentials", "0 logs; no snapshot created"},
		},
		{
			name: "all data rejection",
			outcomes: map[string]queryTestOutcome{
				"prod-a":  {dataRejection: "invalid Dataprime"},
				"prod-b":  {dataRejection: "invalid Dataprime"},
				"stage-a": {dataRejection: "invalid Dataprime"},
			},
			wantExit: ExitData,
			wantErr:  []string{"invalid Dataprime", "0 logs; no snapshot created"},
		},
		{
			name: "warning only",
			outcomes: map[string]queryTestOutcome{
				"prod-a":  {warning: "limited time range"},
				"prod-b":  {warning: "limited time range"},
				"stage-a": {warning: "limited time range"},
			},
			wantErr: []string{"warning: limited time range", "0 logs; no snapshot created"},
		},
		{
			name:     "all zero",
			outcomes: map[string]queryTestOutcome{"prod-a": {}, "prod-b": {}, "stage-a": {}},
			wantErr:  []string{"0 logs; no snapshot created"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setQueryTestXDG(t)
			cfg := multiQueryTestConfig()
			oldStream, oldResolver := queryStream, resolveQueryToken
			defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
			resolveQueryToken = func(_ context.Context, _ *icl.AccountManager, crn *config.CRN) (string, error) {
				if tt.authErr != nil {
					return "", tt.authErr(config.DisplayNameForCRN(config.EffectiveInstances(cfg), crn))
				}
				return config.DisplayNameForCRN(config.EffectiveInstances(cfg), crn), nil
			}
			queryStream = func(_ context.Context, token, _, _ string, _ uint32, cb icl.QueryCallback) error {
				outcome := tt.outcomes[token]
				if outcome.streamErr != nil {
					cb.OnError(outcome.streamErr)
					cb.OnClose()
					return nil //nolint:nilerr // OnError is the stream error contract; Query returns nil.
				}
				if outcome.dataRejection != "" {
					message := outcome.dataRejection
					cb.OnData(&icl.StreamItem{Error: &icl.DataprimeError{Message: &message}})
				}
				if outcome.warning != "" {
					warning := outcome.warning
					cb.OnData(&icl.StreamItem{Warning: &icl.DataprimeWarning{TimeRangeWarning: &icl.TimeRangeWarning{WarningMessage: &warning}}})
				}
				if outcome.message != "" {
					data := `{"message":"` + outcome.message + `"}`
					cb.OnData(&icl.StreamItem{Result: &icl.DataprimeResult{Results: []icl.DataprimeResults{{UserData: &data}}}})
				}
				cb.OnClose()
				return nil
			}

			stdout, stderr, err := runQueryCommand(t, cfg, "source logs", "query", "--all")
			if tt.wantExit == 0 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Equal(t, tt.wantExit, ExitCode(err))
			}
			for _, want := range tt.wantErr {
				assert.Contains(t, stderr, want)
			}

			dir, dirErr := snapshot.Dir()
			require.NoError(t, dirErr)
			entries, entriesErr := os.ReadDir(dir)
			require.NoError(t, entriesErr)
			if !tt.wantSnapshot {
				assert.Empty(t, stdout, "no snapshot outcome must keep stdout selector-pure")
				assert.Empty(t, entries)
				return
			}

			selector := string(bytes.TrimSpace([]byte(stdout)))
			require.NotEmpty(t, selector)
			assert.Equal(t, selector+"\n", stdout, "stdout contains only the selector")
			require.Len(t, entries, 1)

			inspect, inspectErr, inspectRunErr := runQueryCommand(t, cfg, "", "snapshot", "inspect", selector, "-o", "json")
			require.NoError(t, inspectRunErr, inspectErr)
			assert.Contains(t, inspect, `"query": "source logs"`)
			for _, name := range []string{"prod-a", "prod-b", "stage-a"} {
				assert.Contains(t, inspect, `"name": "`+name+`"`)
			}

			container, openErr := snapshot.OpenContainerReadOnly(filepath.Join(dir, selector+snapshot.FileExt))
			require.NoError(t, openErr)
			configured := config.EffectiveInstances(cfg)
			wantFrames := make([]string, 0, len(configured)+1)
			for _, instance := range configured {
				wantFrames = append(wantFrames, "i_"+instance.CRN.String())
			}
			wantFrames = append(wantFrames, "state")
			assert.Equal(t, wantFrames, container.GetFrameNames())
			stateFrame, stateErr := snapshot.LoadState(container)
			require.NoError(t, stateErr)
			require.Len(t, stateFrame.InstancePickerSnapshot.Instances, 3)
			for i, instance := range stateFrame.InstancePickerSnapshot.Instances {
				name := configured[i].Name
				assert.Equal(t, configured[i].CRN.String(), instance.CRN)
				assert.Equal(t, int(tt.wantStates[i]), instance.State)
				assert.Equal(t, tt.wantMessages[i], instance.Message)
				logs, logsErr := snapshot.LoadInstanceLogs(container, instance.CRN)
				require.NoError(t, logsErr)
				commandLogs, commandErrOut, commandRunErr := runQueryCommand(t, cfg, "", "snapshot", "logs", selector, "--instance", name)
				require.NoError(t, commandRunErr, commandErrOut)
				if tt.outcomes[name].message == "" {
					assert.Empty(t, logs)
					assert.Empty(t, commandLogs)
					assert.Zero(t, instance.LogsSizeBytes, "empty or failed selected members persist zero size")
				} else {
					require.Len(t, logs, 1)
					assert.Contains(t, commandLogs, tt.outcomes[name].message)
					assert.Positive(t, instance.LogsSizeBytes)
				}
			}
			require.NoError(t, container.Close())
		})
	}
}

type queryTestOutcome struct {
	message       string
	warning       string
	dataRejection string
	streamErr     error
}

func TestQuery_StreamingAndSetupErrorClassification(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	tests := []struct {
		name       string
		status     int
		body       string
		transport  bool
		wantExit   int
		wantErrMsg string
	}{
		{name: "malformed SSE JSON is general", body: "data: {not json}\n\n", wantExit: ExitGeneral, wantErrMsg: "test: error:"},
		{name: "unknown SSE line is general", body: "bogus line\n", wantExit: ExitGeneral, wantErrMsg: "unknown SSE line"},
		{name: "400 is data rejection", status: http.StatusBadRequest, wantExit: ExitData, wantErrMsg: "http 400"},
		{name: "422 is data rejection", status: http.StatusUnprocessableEntity, wantExit: ExitData, wantErrMsg: "http 422"},
		{name: "401 is unavailable", status: http.StatusUnauthorized, wantExit: ExitUnavailable, wantErrMsg: "http 401"},
		{name: "500 is unavailable", status: http.StatusInternalServerError, wantExit: ExitUnavailable, wantErrMsg: "http 500"},
		{name: "404 is general", status: http.StatusNotFound, wantExit: ExitGeneral, wantErrMsg: "http 404"},
		{name: "transport is unavailable", transport: true, wantExit: ExitUnavailable, wantErrMsg: "query: Post"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setQueryTestXDG(t)
			cfg := queryTestConfig()
			oldStream, oldURL, oldResolver := queryStream, queryURL, resolveQueryToken
			defer func() { queryStream, queryURL, resolveQueryToken = oldStream, oldURL, oldResolver }()
			resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
			queryStream = icl.Query
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			if tt.transport {
				server.Close()
			}
			defer server.Close()
			queryURL = func(*config.CRN) string { return server.URL }

			stdout, stderr, err := runQueryCommand(t, cfg, "q", "query", "--instance", "test")
			require.Error(t, err)
			assert.Equal(t, tt.wantExit, ExitCode(err))
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tt.wantErrMsg)
			assert.Contains(t, stderr, "0 logs; no snapshot created")
		})
	}
}

func TestSaveRotatedQuerySession(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", icl.SessionFile)
	before := map[icl.Environment]string{icl.EnvProd: "old"}
	after := map[icl.Environment]string{icl.EnvProd: "new"}
	require.NoError(t, saveRotatedQuerySession(path, before, after))
	got, err := icl.LoadSession(path)
	require.NoError(t, err)
	assert.Equal(t, after, got)
	if runtime.GOOS != "windows" {
		dirInfo, statErr := os.Stat(filepath.Dir(path))
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
		fileInfo, statErr := os.Stat(path)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
	}
}

func TestReadQueryInput_EditorStripsReferenceAndAllowsEmpty(t *testing.T) { //nolint:paralleltest // replaces TTY seam and EDITOR environment
	oldTTY := isTTY
	defer func() { isTTY = oldTTY }()
	isTTY = func() bool { return true }
	script := t.TempDir() + "/editor"
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '========== dataprime snippets ==========\\nignored\\n' > \"$1\"\n"), 0o700)) // #nosec G306 -- executable test editor
	t.Setenv("EDITOR", script)
	cfg := queryTestConfig()
	cfg.Core.IncludeSnippetsInEditor = true
	query, err := readQueryInput(t.Context(), bytes.NewBufferString("not used"), cfg)
	require.NoError(t, err)
	assert.Empty(t, query)
}

func TestReadQueryInputAt_SeedsEditorWithResolvedStartupContent(t *testing.T) { //nolint:paralleltest // modifies EDITOR environment
	capture := filepath.Join(t.TempDir(), "editor-seed")
	script := filepath.Join(t.TempDir(), "editor")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncp \"$1\" \"$CAPTURE\"\n"), 0o700)) // #nosec G306 -- executable test editor
	t.Setenv("EDITOR", script)
	t.Setenv("CAPTURE", capture)
	cfg := queryTestConfig()
	cfg.ICL.DefaultQuery = "source logs {{ date }} {{ timestamp }}"
	cfg.Core.ExtraSnippets = []config.Snippet{{Snippet: "extra {{ date -1 }}", Desc: "extra description"}}
	cfg.Core.DefaultSnippets = []config.Snippet{{Snippet: "built-in {{ timestamp \"1h\" }}", Desc: "built-in description"}}
	cfg.Core.IncludeDefaultSnippets = true
	at := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)

	query, err := readQueryInputAt(t.Context(), bytes.NewBufferString("not used"), cfg, at)
	require.NoError(t, err)
	assert.Equal(t, "source logs 2025-01-02 2025-01-02T03:04:05Z", query)
	seed, err := os.ReadFile(capture) // #nosec G304 -- test-controlled capture path
	require.NoError(t, err)
	assert.Contains(t, string(seed), "extra 2025-01-01")
	assert.Contains(t, string(seed), "built-in 2025-01-02T04:04:05Z")
	assert.Contains(t, string(seed), "extra description")
	assert.Contains(t, string(seed), "built-in description")
	assert.NotContains(t, string(seed), "{{ date")
	assert.NotContains(t, string(seed), "{{ timestamp")
}

func TestReadQueryInputAt_TemplateErrorIncludesSource(t *testing.T) {
	cfg := queryTestConfig()
	cfg.ICL.DefaultQuery = "{{ timestamp \"not-a-duration\" }}"

	_, err := readQueryInputAt(t.Context(), bytes.NewBuffer(nil), cfg, time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "icl.defaultQuery")
	assert.Contains(t, err.Error(), "not-a-duration")
}

func TestReadQueryInput_RedirectedStdinRemainsVerbatim(t *testing.T) { //nolint:paralleltest // replaces TTY seam
	oldTTY := isTTY
	defer func() { isTTY = oldTTY }()
	isTTY = func() bool { return false }
	input := "{{ date -1 }}\n{{ timestamp }}"

	query, err := readQueryInput(t.Context(), bytes.NewBufferString(input), queryTestConfig())
	require.NoError(t, err)
	assert.Equal(t, input, query)
}

func TestQuery_CancellationDoesNotFinalize(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	oldStream, oldResolver := queryStream, resolveQueryToken
	defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	started := make(chan struct{})
	queryStream = func(ctx context.Context, _, _, _ string, _ uint32, _ icl.QueryCallback) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runOneQuery(ctx, io.Discard, io.Discard, cfg, cfg.ICL.Instances[0], "") }()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestReadQueryStdin_CancellationStopsOpenPipeRead(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = writeEnd.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := readQueryStdin(ctx, readEnd)
		done <- err
	}()
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.NoError(t, readEnd.Close(), "cancellation does not take ownership of the command input pipe")
}

func TestQuery_CancellationRemovesSnapshotsAtEveryLifecycleBarrier(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	phases := []struct {
		name               string
		duringRetention    bool
		duringNotification bool
	}{
		{name: "after snapshot create"},
		{name: "after snapshot logs"},
		{name: "after snapshot state"},
		{name: "after snapshot close"},
		{name: "after snapshot rename"},
		{name: "during retention", duringRetention: true},
		{name: "after retention"},
		{name: "during notification", duringNotification: true},
		{name: "after notification"},
		{name: "before snapshot outcome"},
	}
	for _, phase := range phases {
		t.Run(phase.name, func(t *testing.T) {
			setQueryTestXDG(t)
			cfg := queryTestConfig()
			oldStream, oldResolver, oldCheckpoint, oldRetain, oldNotifier := queryStream, resolveQueryToken, querySnapshotCheckpoint, queryRetainSnapshots, snapshotNotifier
			defer func() {
				queryStream, resolveQueryToken, querySnapshotCheckpoint, queryRetainSnapshots, snapshotNotifier = oldStream, oldResolver, oldCheckpoint, oldRetain, oldNotifier
			}()
			resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
			queryStream = queryOneLog
			reached := make(chan struct{})
			release := make(chan struct{})
			querySnapshotCheckpoint = func(got string) {
				if got == phase.name && !phase.duringRetention {
					close(reached)
					<-release
				}
			}
			if phase.duringRetention {
				queryRetainSnapshots = func(uint8) ([]snapshot.Entry, error) {
					close(reached)
					<-release
					return nil, nil
				}
			}
			if phase.duringNotification {
				snapshotNotifier = func() sessionbus.Broadcaster { return queryBlockingNotifier{reached: reached} }
			}
			ctx, cancel := context.WithCancel(t.Context())
			var stdout bytes.Buffer
			done := make(chan error, 1)
			go func() { done <- runOneQuery(ctx, &stdout, io.Discard, cfg, cfg.ICL.Instances[0], "source logs") }()
			<-reached
			cancel()
			close(release)
			require.ErrorIs(t, <-done, context.Canceled)
			assert.Empty(t, stdout.String(), "cancellation must not print a selector")
			dir, err := snapshot.Dir()
			require.NoError(t, err)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			assert.Empty(t, entries, "cancellation must remove both .wip and final artifacts")
		})
	}
}

type queryBlockingNotifier struct {
	reached chan<- struct{}
}

func (n queryBlockingNotifier) Broadcast(ctx context.Context, _ string, _ any) error {
	close(n.reached)
	<-ctx.Done()
	return ctx.Err()
}

func queryOneLog(_ context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
	data := `{"message":"ok"}`
	cb.OnData(&icl.StreamItem{Result: &icl.DataprimeResult{Results: []icl.DataprimeResults{{UserData: &data}}}})
	cb.OnClose()
	return nil
}

func TestQuery_MultiEnvironmentSchedulingAndPartialSnapshot(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := multiQueryTestConfig()
	oldStart, oldResolver := queryStreamStart, resolveQueryToken
	defer func() { queryStreamStart, resolveQueryToken = oldStart, oldResolver }()

	prodAuthStarted := make(chan struct{})
	stageAuthStarted := make(chan struct{})
	prodStartedGateReached := make(chan struct{})
	releaseProdStarted := make(chan struct{})
	prodBAuthStarted := make(chan struct{})
	twoStreamsActive := make(chan struct{})
	releaseStreams := make(chan struct{})
	var activeMu sync.Mutex
	activeStreams := 0

	resolveQueryToken = func(_ context.Context, _ *icl.AccountManager, crn *config.CRN) (string, error) {
		switch crn.String() {
		case "crn:v1:bluemix:public:logs:us-south:a/account-a:instance::":
			close(prodAuthStarted)
			<-stageAuthStarted // Both environment resolvers must begin before either can finish.
			return config.DisplayNameForCRN(config.EffectiveInstances(cfg), crn), nil
		case "crn:v1:test-cloud:public:logs:us-south:a/account-s:instance::":
			close(stageAuthStarted)
			<-prodAuthStarted
			return config.DisplayNameForCRN(config.EffectiveInstances(cfg), crn), nil
		case "crn:v1:bluemix:public:logs:us-south:a/account-b:instance::":
			close(prodBAuthStarted)
			return "", errors.New("bad production credential")
		default:
			t.Fatalf("unexpected CRN %q", crn)
			return "", nil
		}
	}
	queryStreamStart = func(_ context.Context, token, _, _ string, _ uint32, cb icl.QueryCallback, started func()) error {
		if token == "prod-a" {
			close(prodStartedGateReached)
			<-releaseProdStarted
		}
		started()
		activeMu.Lock()
		activeStreams++
		if activeStreams == 2 {
			close(twoStreamsActive)
		}
		activeMu.Unlock()
		<-releaseStreams
		if token == "prod-a" {
			data := `{"message":"kept"}`
			cb.OnData(&icl.StreamItem{Result: &icl.DataprimeResult{Results: []icl.DataprimeResults{{UserData: &data}}}})
		} else {
			warning := "limited time range"
			cb.OnData(&icl.StreamItem{Warning: &icl.DataprimeWarning{TimeRangeWarning: &icl.TimeRangeWarning{WarningMessage: &warning}}})
		}
		cb.OnClose()
		return nil
	}

	// --all retains effective-config order, which lets the scheduling assertions
	// focus on per-environment auth sequencing.
	done := make(chan struct{})
	var stdout string
	var err error
	go func() {
		stdout, _, err = runQueryCommand(t, cfg, "q", "query", "--all")
		close(done)
	}()
	<-prodStartedGateReached
	select {
	case <-prodBAuthStarted:
		t.Fatal("prod-b auth began before prod-a called started")
	default:
	}
	close(releaseProdStarted)
	<-prodBAuthStarted
	<-twoStreamsActive
	close(releaseStreams)
	<-done
	require.Error(t, err)
	assert.Equal(t, ExitGeneral, ExitCode(err))
	selector := string(bytes.TrimSpace([]byte(stdout)))
	require.NotEmpty(t, selector, "partial results remain scriptable")

	dir, dirErr := snapshot.Dir()
	require.NoError(t, dirErr)
	container, openErr := snapshot.OpenContainerReadOnly(filepath.Join(dir, selector+snapshot.FileExt))
	require.NoError(t, openErr)
	defer func() { require.NoError(t, container.Close()) }()
	configured := config.EffectiveInstances(cfg)
	assert.Equal(t, []string{"i_" + configured[0].CRN.String(), "i_" + configured[1].CRN.String(), "i_" + configured[2].CRN.String(), "state"}, container.GetFrameNames())
	stateFrame, stateErr := snapshot.LoadState(container)
	require.NoError(t, stateErr)
	require.Len(t, stateFrame.InstancePickerSnapshot.Instances, 3)
	assert.Equal(t, []string{configured[0].CRN.String(), configured[1].CRN.String(), configured[2].CRN.String()}, []string{
		stateFrame.InstancePickerSnapshot.Instances[0].CRN,
		stateFrame.InstancePickerSnapshot.Instances[1].CRN,
		stateFrame.InstancePickerSnapshot.Instances[2].CRN,
	})
	assert.Equal(t, int(status.Error), stateFrame.InstancePickerSnapshot.Instances[1].State)
	assert.Contains(t, stateFrame.InstancePickerSnapshot.Instances[1].Message, "bad production credential")
	assert.Equal(t, int(status.Warning), stateFrame.InstancePickerSnapshot.Instances[2].State)
	assert.Equal(t, "limited time range", stateFrame.InstancePickerSnapshot.Instances[2].Message)
	logs, logsErr := snapshot.LoadInstanceLogs(container, configured[1].CRN.String())
	require.NoError(t, logsErr)
	assert.Empty(t, logs)
}

// TestQuery_FirstRaceElectsOneWinner verifies concurrent callbacks are gated
// before result and tee accumulation, while canceled candidates remain in state.
func TestQuery_FirstRaceElectsOneWinner(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := multiQueryTestConfig()
	instances := []config.ICLInstanceConfig{cfg.ICL.Instances[0], cfg.ICL.Instances[2]}
	oldStream, oldResolver := queryStream, resolveQueryToken
	defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
	resolveQueryToken = teeTokenResolver
	started := make(chan string, len(instances))
	winner := make(chan struct{})
	loserCanceled := make(chan struct{})
	queryStream = func(ctx context.Context, token, _, _ string, _ uint32, cb icl.QueryCallback) error {
		started <- token
		if token == "prod-a" {
			<-winner
			cb.OnData(teeLog("winner-first"))
			cb.OnData(teeLog("winner-later"))
			cb.OnClose()
			return nil
		}
		<-ctx.Done()
		cb.OnData(teeLog("loser-after-cancel"))
		cb.OnError(errors.New("loser-after-cancel"))
		cb.OnClose()
		close(loserCanceled)
		return ctx.Err()
	}

	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runQueryWithOptions(t.Context(), &stdout, &stderr, cfg, instances, "q", queryRunOptions{first: true, tee: true})
	}()
	for range instances {
		<-started
	}
	close(winner)
	require.NoError(t, <-done)
	<-loserCanceled
	assertNDJSONMessages(t, stdout.String(), []string{"winner-first", "winner-later"})
	assert.NotContains(t, stderr.String(), "stage-a: error")

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	container, err := snapshot.OpenContainerReadOnly(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)
	defer func() { require.NoError(t, container.Close()) }()
	winnerLogs, err := snapshot.LoadInstanceLogs(container, instances[0].CRN.String())
	require.NoError(t, err)
	assert.Len(t, winnerLogs, 2)
	loserLogs, err := snapshot.LoadInstanceLogs(container, instances[1].CRN.String())
	require.NoError(t, err)
	assert.Empty(t, loserLogs)
	state, err := snapshot.LoadState(container)
	require.NoError(t, err)
	require.Len(t, state.InstancePickerSnapshot.Instances, 2)
	assert.Equal(t, int(status.Cancelled), state.InstancePickerSnapshot.Instances[1].State)
}

func TestQuery_FirstWinnerFailureRetainsPartialResult(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	oldStream, oldResolver := queryStream, resolveQueryToken
	defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
	resolveQueryToken = teeTokenResolver
	queryStream = func(_ context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
		cb.OnData(teeLog("kept-before-failure"))
		return errors.New("winner failed")
	}

	var stdout, stderr bytes.Buffer
	err := runQueryWithOptions(t.Context(), &stdout, &stderr, cfg, cfg.ICL.Instances, "q", queryRunOptions{first: true})
	require.Error(t, err)
	assert.Equal(t, ExitGeneral, ExitCode(err))
	assert.Contains(t, err.Error(), "winner failed")
	assert.NotEmpty(t, stdout.String())
	assert.Contains(t, stderr.String(), "1 logs; snapshot")
}

func TestRunQueryHandler_FirstImplicitlySelectsAll(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := multiQueryTestConfig()
	oldStream, oldResolver := queryStream, resolveQueryToken
	defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
	resolveQueryToken = teeTokenResolver
	started := make(chan struct{}, len(config.EffectiveInstances(cfg)))
	queryStream = func(_ context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
		started <- struct{}{}
		cb.OnClose()
		return nil
	}

	var stdout, stderr bytes.Buffer
	err := runQueryHandler(t.Context(), strings.NewReader("q"), &stdout, &stderr, cfg, queryHandlerOptions{first: true})
	require.NoError(t, err)
	for range config.EffectiveInstances(cfg) {
		<-started
	}
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "0 logs; no snapshot created")
}

func TestQuery_FirstNoWinnerOutcomeMatrices(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	tests := []struct {
		name     string
		authErr  func(string) error
		outcomes map[string]queryTestOutcome
		wantExit int
		wantErr  []string
	}{
		{
			name: "mixed empty and failure",
			outcomes: map[string]queryTestOutcome{
				"prod-a":  {},
				"prod-b":  {streamErr: errors.New("stream failed")},
				"stage-a": {},
			},
			wantExit: ExitGeneral,
			wantErr:  []string{"prod-b: error: stream failed", "0 logs; no snapshot created"},
		},
		{
			name: "all data errors",
			outcomes: map[string]queryTestOutcome{
				"prod-a":  {dataRejection: "invalid Dataprime"},
				"prod-b":  {dataRejection: "invalid Dataprime"},
				"stage-a": {dataRejection: "invalid Dataprime"},
			},
			wantExit: ExitData,
			wantErr:  []string{"invalid Dataprime", "0 logs; no snapshot created"},
		},
		{
			name: "all unavailable",
			authErr: func(name string) error {
				if name == "stage-a" {
					return &icl.HeadlessAuthRequiredError{Env: icl.Environment("test-cloud")}
				}
				return &icl.HeadlessAuthRequiredError{Env: icl.EnvProd}
			},
			wantExit: ExitUnavailable,
			wantErr:  []string{"no noninteractive credentials", "0 logs; no snapshot created"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setQueryTestXDG(t)
			cfg := multiQueryTestConfig()
			oldStream, oldResolver := queryStream, resolveQueryToken
			defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
			resolveQueryToken = func(_ context.Context, _ *icl.AccountManager, crn *config.CRN) (string, error) {
				name := config.DisplayNameForCRN(config.EffectiveInstances(cfg), crn)
				if tt.authErr != nil {
					return "", tt.authErr(name)
				}
				return name, nil
			}
			queryStream = func(_ context.Context, token, _, _ string, _ uint32, cb icl.QueryCallback) error {
				outcome := tt.outcomes[token]
				if outcome.streamErr != nil {
					cb.OnError(outcome.streamErr)
					cb.OnClose()
					return nil //nolint:nilerr // OnError is the stream error contract; Query returns nil.
				}
				if outcome.dataRejection != "" {
					message := outcome.dataRejection
					cb.OnData(&icl.StreamItem{Error: &icl.DataprimeError{Message: &message}})
				}
				cb.OnClose()
				return nil
			}

			stdout, stderr, err := runQueryCommand(t, cfg, "source logs", "query", "--first")
			require.Error(t, err)
			assert.Equal(t, tt.wantExit, ExitCode(err))
			assert.Empty(t, stdout)
			for _, want := range tt.wantErr {
				assert.Contains(t, stderr, want)
			}

			dir, dirErr := snapshot.Dir()
			require.NoError(t, dirErr)
			entries, entriesErr := os.ReadDir(dir)
			require.NoError(t, entriesErr)
			assert.Empty(t, entries)
		})
	}
}

func TestQueryFirstRaceArbitratesConcurrentCallbacks(t *testing.T) {
	t.Parallel()

	instances := []config.ICLInstanceConfig{
		{Name: "one", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/one:instance::")},
		{Name: "two", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/two:instance::")},
	}
	members := []queryMember{newQueryMember(instances[0], time.Now()), newQueryMember(instances[1], time.Now())}
	first := newQueryFirstRace(t.Context(), func() {}, members)
	for index := range members {
		members[index].first, members[index].index = first, index
		_, started := first.streamContext(index)
		require.True(t, started)
	}
	ready := make(chan struct{}, len(members))
	release := make(chan struct{})
	var callbacks sync.WaitGroup
	for index := range members {
		callbacks.Add(1)
		go func(index int) {
			defer callbacks.Done()
			ready <- struct{}{}
			<-release
			members[index].OnData(teeLog(members[index].instance.Name))
		}(index)
	}
	for range members {
		<-ready
	}
	close(release)
	callbacks.Wait()

	winner := first.winnerIndex()
	require.Contains(t, []int{0, 1}, winner)
	assert.Len(t, members[winner].resultSnapshot().logs, 1)
	assert.Empty(t, members[1-winner].resultSnapshot().logs)
}

func TestQueryFirstRaceDoesNotRestartCancelledLoser(t *testing.T) {
	t.Parallel()

	instances := []config.ICLInstanceConfig{
		{Name: "winner", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/winner:instance::")},
		{Name: "loser", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/loser:instance::")},
	}
	members := []queryMember{newQueryMember(instances[0], time.Now()), newQueryMember(instances[1], time.Now())}
	first := newQueryFirstRace(t.Context(), func() {}, members)
	for index := range members {
		members[index].first, members[index].index = first, index
	}

	loserCtx, started := first.streamContext(1)
	require.True(t, started)
	require.Equal(t, status.InProgress, members[1].view().Phase)
	require.True(t, first.acceptData(0, 1))
	require.ErrorIs(t, loserCtx.Err(), context.Canceled)
	assert.Equal(t, status.Cancelled, members[1].view().Phase)
	assert.Equal(t, []status.Phase{status.AuthInProgress, status.InProgress, status.Cancelled}, members[1].phaseHistory())
}

func TestQuery_FirstCancellationJoinsWorkers(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := multiQueryTestConfig()
	instances := []config.ICLInstanceConfig{cfg.ICL.Instances[0], cfg.ICL.Instances[2]}
	oldStream, oldResolver := queryStream, resolveQueryToken
	defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
	resolveQueryToken = teeTokenResolver
	started := make(chan struct{}, len(instances))
	queryStream = func(ctx context.Context, _, _, _ string, _ uint32, _ icl.QueryCallback) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- runQueryWithOptions(ctx, io.Discard, io.Discard, cfg, instances, "q", queryRunOptions{first: true})
	}()
	for range instances {
		<-started
	}
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	if !os.IsNotExist(err) {
		require.NoError(t, err)
		assert.Empty(t, entries)
	}
}

func TestSelectedQueryInstances(t *testing.T) {
	t.Parallel()
	cfg := multiQueryTestConfig()
	direct := "crn:v1:test-cloud:public:logs:eu-gb:a/account-direct:123456789::"

	for _, tt := range []struct {
		name      string
		all       bool
		selectors []string
		empty     bool
		want      []string
		wantErr   string
	}{
		{name: "all", all: true, want: []string{"prod-a", "prod-b", "stage-a"}},
		{name: "instances preserve flag order", selectors: []string{"stage-a", "prod-a"}, want: []string{"stage-a", "prod-a"}},
		{name: "configured and direct CRNs", selectors: []string{cfg.ICL.Instances[1].CRN.String(), direct}, want: []string{"prod-b", "eu-gb/12345678"}},
		{name: "all and duplicate selectors", all: true, selectors: []string{"stage-a", "prod-a", "stage-a", cfg.ICL.Instances[0].CRN.String(), direct, direct}, want: []string{"prod-a", "prod-b", "stage-a", "eu-gb/12345678"}},
		{name: "unconfigured direct CRN", selectors: []string{"prod-a", "crn:v1:unknown-cloud:public:logs:us-south:a/account:instance::"}, wantErr: `instance "us-south/instance" uses unconfigured ICL environment "unknown-cloud"`},
		{name: "empty configured all", all: true, empty: true, wantErr: "no query targets resolved"},
		{name: "no targets", wantErr: "at least one target selector"},
		{name: "unknown configured instance", selectors: []string{"missing"}, wantErr: `instance "missing" is not a configured name or valid ICL CRN`},
		{name: "invalid direct CRN", selectors: []string{"not-a-crn"}, wantErr: `instance "not-a-crn" is not a configured name or valid ICL CRN`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			caseCfg := *cfg
			if tt.empty {
				caseCfg.ICL.Instances = []config.ICLInstanceConfig{}
			}
			instances, err := selectedQueryInstances(&caseCfg, tt.all, tt.selectors)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, ExitUsage, ExitCode(err))
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			got := make([]string, len(instances))
			for i, instance := range instances {
				got[i] = instance.Name
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRunQueryHandler_InvalidSelectorsHaveNoInputOrAuthSideEffects(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := multiQueryTestConfig()
	oldResolver := resolveQueryToken
	defer func() { resolveQueryToken = oldResolver }()
	called := false
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) {
		called = true
		return "", nil
	}
	for _, options := range []queryHandlerOptions{
		{},
		{selectors: []string{"prod-a", "missing", "prod-a"}},
		{selectors: []string{"not-a-crn"}},
		{selectors: []string{"prod-a", "crn:v1:unknown-cloud:public:logs:us-south:a/account:instance::"}},
	} {
		err := runQueryHandler(t.Context(), errReader{}, io.Discard, io.Discard, cfg, options)
		require.Error(t, err)
		assert.Equal(t, ExitUsage, ExitCode(err))
	}
	assert.False(t, called)
}

func TestRunQueryHandler_EmptyEffectiveAllHasNoInputOrAuthSideEffects(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := config.New()
	oldResolver := resolveQueryToken
	defer func() { resolveQueryToken = oldResolver }()
	called := false
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) {
		called = true
		return "", nil
	}

	err := runQueryHandler(t.Context(), errReader{}, io.Discard, io.Discard, cfg, queryHandlerOptions{all: true})
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCode(err))
	require.ErrorContains(t, err, "no query targets resolved")
	assert.False(t, called)
}

func TestCompleteQueryInstance(t *testing.T) {
	t.Parallel()
	complete := completeQueryInstance(func(*cobra.Command) (deps.Bundle, error) {
		return deps.New(multiQueryTestConfig(), state.New()), nil
	})
	got, directive := complete(nil, nil, "prod")
	assert.Equal(t, []string{"prod-a", "prod-b"}, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

func TestQuery_MultiCancellationJoinsWorkers(t *testing.T) { //nolint:paralleltest // mutates command seams and XDG environment
	setQueryTestXDG(t)
	cfg := multiQueryTestConfig()
	oldStream, oldResolver := queryStream, resolveQueryToken
	defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
	resolveQueryToken = func(_ context.Context, _ *icl.AccountManager, crn *config.CRN) (string, error) {
		return config.DisplayNameForCRN(config.EffectiveInstances(cfg), crn), nil
	}
	started := make(chan struct{}, 3)
	queryStream = func(ctx context.Context, _, _, _ string, _ uint32, _ icl.QueryCallback) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runQuery(ctx, io.Discard, io.Discard, cfg, config.EffectiveInstances(cfg), "q") }()
	for range 3 {
		<-started
	}
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	dir, dirErr := snapshot.Dir()
	require.NoError(t, dirErr)
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestQueryMemberExitMatrices(t *testing.T) {
	t.Parallel()
	member := func(failure queryFailure) queryMember {
		result := queryResult{}
		if failure != queryFailureNone {
			result.failed(errors.New("failure"), failure)
		}
		return queryMember{result: result}
	}
	for _, tt := range []struct {
		name    string
		members []queryMember
		want    int
	}{
		{name: "success including warnings and zero", members: []queryMember{{result: queryResult{warnings: []string{"warn"}}}, member(queryFailureNone)}, want: 0},
		{name: "mixed", members: []queryMember{member(queryFailureNone), member(queryFailureUnavailable)}, want: ExitGeneral},
		{name: "all data rejection", members: []queryMember{member(queryFailureData), member(queryFailureData)}, want: ExitData},
		{name: "all unavailable", members: []queryMember{member(queryFailureUnavailable), member(queryFailureUnavailable)}, want: ExitUnavailable},
		{name: "unclassified", members: []queryMember{member(queryFailureGeneral)}, want: ExitGeneral},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := queryMembersError(tt.members)
			if tt.want == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.want, ExitCode(err))
		})
	}
}

func TestQuery_UnknownInstanceDoesNotReadInput(t *testing.T) { //nolint:paralleltest // mutates XDG environment
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	root := queryRoot(cfg)
	root.SetArgs([]string{"query", "--instance", "missing"})
	root.SetIn(errReader{})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCode(err))
}

func TestQuery_NoCredentialsIsUnavailableWithoutPasscode(t *testing.T) { //nolint:paralleltest // mutates TTY seam and XDG environment
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	setQueryEnvironmentCredentials(cfg, "bluemix", "", "")
	oldTTY, oldResolver := isTTY, resolveQueryToken
	defer func() { isTTY, resolveQueryToken = oldTTY, oldResolver }()
	isTTY = func() bool { return false }
	_, stderr, err := runQueryCommand(t, cfg, "", "query", "--instance", "test")
	require.Error(t, err)
	assert.Equal(t, ExitUnavailable, ExitCode(err))
	assert.Contains(t, err.Error(), "no noninteractive credentials")
	assert.Contains(t, stderr, "querying test")
}

func TestQuery_AuthenticationSourcesAndRefreshPersistence(t *testing.T) { //nolint:paralleltest // mutates command seams, auth executable, and XDG environment
	tests := []struct {
		name        string
		configure   func(t *testing.T, cfg *config.Config)
		wantGrant   string
		wantAPIKey  string
		wantSession map[icl.Environment]string
		wantExit    int
	}{
		{
			name:       "configured API key",
			wantGrant:  "urn:ibm:params:oauth:grant-type:apikey",
			wantAPIKey: "test-key",
			wantSession: map[icl.Environment]string{
				icl.EnvProd: "rotated-refresh",
			},
		},
		{
			name: "environment API key takes precedence",
			configure: func(t *testing.T, _ *config.Config) {
				t.Setenv("LOGNAV_IC_API_KEY", "environment-key")
			},
			wantGrant:  "urn:ibm:params:oauth:grant-type:apikey",
			wantAPIKey: "environment-key",
			wantSession: map[icl.Environment]string{
				icl.EnvProd: "rotated-refresh",
			},
		},
		{
			name: "1Password API key",
			configure: func(t *testing.T, cfg *config.Config) {
				setQueryEnvironmentCredentials(cfg, "bluemix", "", "")
				setQueryEnvironmentCredentials(cfg, "bluemix", "", "op://vault/item/field")
				op := t.TempDir() + "/op"
				require.NoError(t, os.WriteFile(op, []byte("#!/bin/sh\nprintf 'one-password-key\\n'\n"), 0o700)) // #nosec G306 -- executable test helper
				t.Setenv("PATH", t.TempDir()+":"+filepath.Dir(op))
			},
			wantGrant:  "urn:ibm:params:oauth:grant-type:apikey",
			wantAPIKey: "one-password-key",
			wantSession: map[icl.Environment]string{
				icl.EnvProd: "rotated-refresh",
			},
		},
		{
			name: "valid persisted refresh token rotates",
			configure: func(t *testing.T, cfg *config.Config) {
				setQueryEnvironmentCredentials(cfg, "bluemix", "", "")
				path, err := icl.SessionPath()
				require.NoError(t, err)
				require.NoError(t, icl.SaveSession(path, map[icl.Environment]string{icl.EnvProd: "persisted-refresh"}))
			},
			wantGrant: "refresh_token",
			wantSession: map[icl.Environment]string{
				icl.EnvProd: "rotated-refresh",
			},
		},
		{
			name: "persisted refresh token takes precedence over 1Password",
			configure: func(t *testing.T, cfg *config.Config) {
				setQueryEnvironmentCredentials(cfg, "bluemix", "", "")
				setQueryEnvironmentCredentials(cfg, "bluemix", "", "op://vault/item/field")
				op := filepath.Join(t.TempDir(), "op")
				require.NoError(t, os.WriteFile(op, []byte("#!/bin/sh\nprintf 'one-password-key\\n'\n"), 0o700)) // #nosec G306 -- executable test helper
				t.Setenv("PATH", t.TempDir()+":"+filepath.Dir(op))
				path, err := icl.SessionPath()
				require.NoError(t, err)
				require.NoError(t, icl.SaveSession(path, map[icl.Environment]string{icl.EnvProd: "persisted-refresh"}))
			},
			wantGrant: "refresh_token",
			wantSession: map[icl.Environment]string{
				icl.EnvProd: "rotated-refresh",
			},
		},
		{
			name: "IAM rejected refresh remains headless",
			configure: func(t *testing.T, cfg *config.Config) {
				setQueryEnvironmentCredentials(cfg, "bluemix", "", "")
				path, err := icl.SessionPath()
				require.NoError(t, err)
				require.NoError(t, icl.SaveSession(path, map[icl.Environment]string{icl.EnvProd: "expired-refresh"}))
			},
			wantGrant:   "refresh_token",
			wantExit:    ExitUnavailable,
			wantSession: map[icl.Environment]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setQueryTestXDG(t)
			t.Setenv("LOGNAV_IC_API_KEY", "")
			cfg := queryTestConfig()
			if tt.configure != nil {
				tt.configure(t, cfg)
			}
			var gotGrant, gotAPIKey string
			iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.NoError(t, r.ParseForm())
				gotGrant, gotAPIKey = r.Form.Get("grant_type"), r.Form.Get("apikey")
				if tt.wantExit != 0 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"errorCode":"BXNIM0407E","errorMessage":"Refresh token expired"}`)
					return
				}
				_, _ = io.WriteString(w, `{"access_token":"token","refresh_token":"rotated-refresh","expires_in":3600}`)
			}))
			defer iam.Close()

			oldStream, oldManager := queryStream, newQueryAccountManager
			defer func() { queryStream, newQueryAccountManager = oldStream, oldManager }()
			queryStream = func(_ context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
				cb.OnClose()
				return nil
			}
			newQueryAccountManager = func(environments map[string]config.ICLEnvironmentConfig) *icl.AccountManager {
				manager := icl.NewAccountManager(environments)
				manager.SetOIDCForTest(icl.EnvProd, iam.URL)
				return manager
			}

			stdout, _, err := runQueryCommand(t, cfg, "", "query", "--instance", "test")
			assert.Empty(t, stdout)
			if tt.wantExit != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantExit, ExitCode(err))
				assert.Contains(t, err.Error(), "no noninteractive credentials")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantGrant, gotGrant)
			assert.Equal(t, tt.wantAPIKey, gotAPIKey)
			path, pathErr := icl.SessionPath()
			require.NoError(t, pathErr)
			session, loadErr := icl.LoadSession(path)
			require.NoError(t, loadErr)
			assert.Equal(t, tt.wantSession, session)
		})
	}
}

// TestExecute_QuerySignalHelper is a subprocess body. It uses Execute rather
// than root.Execute so the signal registration and explicit 130/143 mapping
// are tested with the command's real stdin.
func TestExecute_QuerySignalHelper(t *testing.T) { //nolint:paralleltest // subprocess helper mutates package seams
	phase := os.Getenv("LOGNAV_QUERY_SIGNAL_HELPER")
	if phase == "" {
		return
	}
	ready := os.Getenv("LOGNAV_QUERY_SIGNAL_READY")
	markReady := func() { require.NoError(t, os.WriteFile(ready, []byte("ready"), 0o600)) } // #nosec G703 -- test-controlled helper readiness file
	cfg := queryTestConfig()
	isTTY = func() bool { return false }
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	queryStream = queryOneLog
	newExecuteRoot = func() *cobra.Command {
		root := queryRoot(cfg)
		args := []string{"query", "--instance", "test"}
		if strings.HasPrefix(phase, "tee") {
			args = append(args, "--tee")
		}
		root.SetArgs(args)
		root.SetIn(os.Stdin)
		root.SetOut(os.Stdout)
		root.SetErr(os.Stderr)
		return root
	}
	switch phase {
	case "input":
		queryInputReadStarted = markReady
	case "stream":
		queryStream = func(ctx context.Context, _, _, _ string, _ uint32, _ icl.QueryCallback) error {
			markReady()
			<-ctx.Done()
			return ctx.Err()
		}
	case "tee":
		queryStream = func(ctx context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
			cb.OnData(teeLog("emitted"))
			markReady()
			<-ctx.Done()
			return ctx.Err()
		}
	case "finalization", "tee-finalization":
		// A checkpoint has no context parameter, so record the query stream's
		// context and wait on it once the snapshot is finalized but before its
		// outcome commits. The tee checkpoint is after retention, notification,
		// and retained-name resolution, immediately before finalizeQuerySnapshot
		// releases its cleanup defer.
		queryContexts := make(chan context.Context, 1)
		queryStream = func(ctx context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
			queryContexts <- ctx
			return queryOneLog(ctx, "", "", "", 0, cb)
		}
		checkpoint := "after snapshot rename"
		if phase == "tee-finalization" {
			checkpoint = "before snapshot outcome"
		}
		querySnapshotCheckpoint = func(got string) {
			if got == checkpoint {
				markReady()
				queryCtx := <-queryContexts
				<-queryCtx.Done()
			}
		}
	default:
		t.Fatalf("unknown helper phase %q", phase)
	}
	os.Exit(ExitCode(Execute()))
}

func TestExecute_QuerySignalCancellation(t *testing.T) { //nolint:paralleltest // spawns signal subprocesses and uses process-global signal delivery
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM semantics differ on Windows")
	}
	for _, phase := range []string{"input", "stream", "tee", "finalization", "tee-finalization"} {
		for _, signalCase := range []struct {
			name string
			sig  os.Signal
			code int
		}{
			{name: "SIGINT", sig: os.Interrupt, code: 130},
			{name: "SIGTERM", sig: syscall.SIGTERM, code: 143},
		} {
			t.Run(phase+"/"+signalCase.name, func(t *testing.T) {
				dataHome := t.TempDir()
				ready := filepath.Join(t.TempDir(), "ready")
				child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestExecute_QuerySignalHelper$") // #nosec G204,G702 -- invokes the current test binary
				child.Env = append(os.Environ(),
					"LOGNAV_QUERY_SIGNAL_HELPER="+phase,
					"LOGNAV_QUERY_SIGNAL_READY="+ready,
					"LOGNAV_IC_API_KEY=",
					"XDG_DATA_HOME="+dataHome,
					"XDG_STATE_HOME="+t.TempDir(),
					"XDG_CONFIG_HOME="+t.TempDir(),
				)
				var stdout, stderr bytes.Buffer
				child.Stdout, child.Stderr = &stdout, &stderr
				if phase == "input" {
					stdin, err := child.StdinPipe()
					require.NoError(t, err)
					t.Cleanup(func() { _ = stdin.Close() })
				} else {
					child.Stdin = bytes.NewReader(nil)
				}
				require.NoError(t, child.Start())
				require.Eventually(t, func() bool {
					_, err := os.Stat(ready)
					return err == nil
				}, time.Second, 10*time.Millisecond, "child did not reach %s", phase)
				require.NoError(t, child.Process.Signal(signalCase.sig))
				done := make(chan error, 1)
				go func() { done <- child.Wait() }()
				require.Eventually(t, func() bool {
					select {
					case err := <-done:
						require.Error(t, err)
						return true
					default:
						return false
					}
				}, time.Second, 10*time.Millisecond, "child did not exit promptly")
				assert.Equal(t, signalCase.code, child.ProcessState.ExitCode())
				switch phase {
				case "tee":
					assertNDJSONMessages(t, stdout.String(), []string{"emitted"})
					assert.NotContains(t, stdout.String(), ".tmp")
				case "tee-finalization":
					assertNDJSONMessages(t, stdout.String(), []string{"ok"})
					assert.NotContains(t, stdout.String(), ".tmp")
				default:
					assert.Empty(t, stdout.String(), "cancellation must never print a selector")
				}
				entries, err := os.ReadDir(filepath.Join(dataHome, "lognav", "snapshots"))
				if !os.IsNotExist(err) {
					require.NoError(t, err)
					assert.Empty(t, entries, "cancellation must leave no final snapshot or .wip")
				}
			})
		}
	}
}

func setQueryEnvironmentCredentials(cfg *config.Config, cname, key, opRef string) {
	environment := cfg.ICL.Environments[cname]
	environment.APIKey = key
	environment.APIKeyOpRef = opRef
	cfg.ICL.Environments[cname] = environment
}

func queryTestConfig() *config.Config {
	cfg := config.New()
	cfg.ICL.Environments = map[string]config.ICLEnvironmentConfig{
		"bluemix":    {IAMURL: "https://iam.example/identity", APIKey: "test-key"},
		"test-cloud": {IAMURL: "https://iam.example/test"},
	}
	cfg.ICL.Instances = []config.ICLInstanceConfig{{
		Name: "test", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account:instance::"),
	}}
	return cfg
}

func multiQueryTestConfig() *config.Config {
	cfg := queryTestConfig()
	setQueryEnvironmentCredentials(cfg, "test-cloud", "test-stage-key", "")
	cfg.ICL.Instances = []config.ICLInstanceConfig{
		{Name: "prod-a", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account-a:instance::")},
		{Name: "prod-b", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account-b:instance::")},
		{Name: "stage-a", CRN: config.MustCRNFromString("crn:v1:test-cloud:public:logs:us-south:a/account-s:instance::")},
	}
	return cfg
}

func queryRoot(cfg *config.Config) *cobra.Command {
	return newRootCmd(func(bool) (deps.Bundle, error) { return deps.New(cfg, state.New()), nil })
}

func runQueryCommand(t *testing.T, cfg *config.Config, input string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := queryRoot(cfg)
	var out, errOut bytes.Buffer
	root.SetIn(bytes.NewBufferString(input))
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func setQueryTestXDG(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, assert.AnError }

type queryErrWriter struct{}

func (queryErrWriter) Write([]byte) (int, error) { return 0, assert.AnError }
