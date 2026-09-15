package archive

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withTempDir(t *testing.T) {
	t.Helper()
	d := t.TempDir()
	prev := dirFn
	dirFn = func() (string, error) { return d, nil }
	t.Cleanup(func() { dirFn = prev })
}

func TestSaveLoadRoundTrip(t *testing.T) {
	// Not t.Parallel(): withTempDir mutates the package-level dirFn var; parallel
	// tests that also call withTempDir would race on that var.
	withTempDir(t)
	a := &Archive{
		Name: "2026-06-20-15-04-05", Query: "source logs last 7d", SubmittedAt: time.Unix(1_750_000_000, 0).UTC(),
		Instances: []InstanceEntry{{CRN: "crn:v1:bluemix:public:logs:ca-tor:a/account:instance::", QueryID: "abc", State: StateRunning}},
	}
	require.NoError(t, Save(a))
	p, err := PathFor(a.Name)
	require.NoError(t, err)
	got, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, a.Query, got.Query)
	assert.Equal(t, "crn:v1:bluemix:public:logs:ca-tor:a/account:instance::", got.Instances[0].CRN)

	raw, err := os.ReadFile(p) // #nosec G304 -- PathFor confines this test file to its temp registry
	require.NoError(t, err)
	var wire struct {
		Instances []map[string]any `json:"instances"`
	}
	require.NoError(t, json.Unmarshal(raw, &wire))
	require.Len(t, wire.Instances, 1)
	assert.NotContains(t, wire.Instances[0], "name", "archive instance JSON is CRN-only")
}

func TestLoad_RejectsLegacyPerInstanceName(t *testing.T) {
	// Not t.Parallel(): withTempDir mutates the package-level dirFn var.
	withTempDir(t)
	path, err := PathFor("legacy")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(`{
  "name": "legacy",
  "instances": [{"name": "old-label", "crn": "crn:v1:bluemix:public:logs:us-south:a/account:instance::"}]
}`), 0o600))

	_, err = Load(path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance names are unsupported")
}

func TestIsReady(t *testing.T) {
	t.Parallel()
	a := &Archive{Instances: []InstanceEntry{{State: StateRunning}, {State: StateSuccess}}}
	assert.False(t, a.IsReady())
	a.Instances[0].State = StateError
	assert.True(t, a.IsReady())
	assert.False(t, (&Archive{}).IsReady()) // no instances
}

func TestExpiry(t *testing.T) {
	t.Parallel()
	a := &Archive{SubmittedAt: time.Unix(0, 0).UTC()}
	assert.True(t, a.IsExpired(time.Unix(0, 0).Add(TTL+time.Hour)))
	assert.False(t, a.IsExpired(time.Unix(0, 0).Add(time.Hour)))
}
