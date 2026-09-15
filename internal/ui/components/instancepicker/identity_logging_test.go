package instancepicker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/logviewer"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

const (
	identityLogCRN  = "crn:v1:bluemix:public:logs:us-south:a/account:instance::"
	identityLogName = "production-logs"
)

func TestNewInstance_UsesCRNForStoreChunkIdentity(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetSearch("needle")
	inst := NewInstance(bundle, identityLogName, "https://example.invalid", identityLogCRN, icl.EnvProd, "%.2f")
	poster := &fakePoster{}
	inst.Store.SetPoster(poster)
	inst.Store.StartStream(1)
	inst.Store.HandleLogStreamMsg(&msgs.LogStreamMsg{
		CRN:  identityLogCRN,
		ID:   inst.Store.GetQueryID(),
		Logs: []icl.Log{{Data: map[string]any{"message": "needle"}}},
	})
	inst.Store.HandleLogStreamDoneMsg(&msgs.LogStreamDoneMsg{CRN: identityLogCRN, ID: inst.Store.GetQueryID()})

	for _, event := range poster.events() {
		if chunk, ok := event.(logviewer.SearchChunkMsg); ok {
			assert.Equal(t, identityLogCRN, chunk.Instance)
			return
		}
	}
	t.Fatal("expected a search chunk")
}

func TestIdentityFailuresLogDisplayName(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	assertDisplayOnly := func(t *testing.T) {
		t.Helper()
		assert.Contains(t, buf.String(), "instance="+identityLogName)
		assert.NotContains(t, buf.String(), identityLogCRN)
		buf.Reset()
	}

	t.Run("flush", func(t *testing.T) {
		bundle := depstest.NewTest(t)
		m := newTestModel(t, &fakePoster{})
		m.bundle = bundle
		inst := NewInstance(bundle, identityLogName, "https://example.invalid", identityLogCRN, icl.EnvProd, "%.2f")
		m.instances = Instances{inst}
		m.fetchEpoch = 1
		m.pendingFlushes = 1

		m.OnInstanceFlushed(InstanceFlushedMsg{crn: identityLogCRN, err: errors.New("compress failed"), epoch: 1})
		assertDisplayOnly(t)
	})

	t.Run("lazy load", func(t *testing.T) {
		bundle := depstest.NewTest(t)
		m := newTestModel(t, &fakePoster{})
		m.bundle = bundle
		inst := NewInstance(bundle, identityLogName, "https://example.invalid", identityLogCRN, icl.EnvProd, "%.2f")
		m.instances = Instances{inst}

		m.openDurableFrame(identityLogCRN, inst.Name, t.TempDir())
		assertDisplayOnly(t)
	})

	t.Run("auth", func(t *testing.T) {
		bundle := depstest.NewTest(t)
		m := newTestModel(t, &fakePoster{})
		m.bundle = bundle
		inst := NewInstance(bundle, identityLogName, "https://example.invalid", identityLogCRN, icl.EnvProd, "%.2f")
		m.instances = Instances{inst}

		m.OnMemberAuthFailed(MemberAuthFailedMsg{CRN: identityLogCRN, Err: errors.New("auth failed")})
		assertDisplayOnly(t)
	})

	t.Run("dispatch", func(t *testing.T) {
		previousSubmit := dispatchSubmitFn
		t.Cleanup(func() { dispatchSubmitFn = previousSubmit })
		dispatchSubmitFn = func(context.Context, string, string, string) (string, error) {
			return "", errors.New("submit failed")
		}
		iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"access_token":"token","expires_in":3600}`))
		}))
		t.Cleanup(iam.Close)

		bundle := depstest.NewTest(t)
		inst := NewInstance(bundle, identityLogName, "https://example.invalid", identityLogCRN, icl.EnvProd, "%.2f")
		inst.Enable()
		am := testAccountManagerWithAPIKey("api-key")
		am.SetOIDCForTest(icl.EnvProd, iam.URL)
		m := newDispatchModel(t, &fakePoster{}, am, Instances{inst})
		m.bundle.State.SetQuery("source logs")

		m.dispatchArchive()
		assertDisplayOnly(t)
	})
}
