package instancepicker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
)

// TestStartCollect_DelegatesToStartCollect proves the exported root seam
// StartCollect forwards to the unexported startCollect: the busy refusal
// (errCollectBusy while a fetch/watch is running) surfaces identically through
// the wrapper. This is the symbol the root ui.Model routes ArchiveCollectMsg to.
func TestStartCollect_DelegatesToStartCollect(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	am, closeSrv := oidcTokenServer(t, "inst-a")
	defer closeSrv()
	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "inst-a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	m := newCollectModel(t, am, Instances{a})

	arch := &archive.Archive{Name: "x", Instances: []archive.InstanceEntry{{CRN: testCRN("a"), QueryID: "q"}}}

	m.fetching = true
	require.ErrorIs(t, m.StartCollect(arch), errCollectBusy,
		"StartCollect must delegate to startCollect (busy refusal surfaces through the wrapper)")
}

// TestResolveInstanceToken_DelegatesToResolver proves the exported root seam
// ResolveInstanceToken forwards to instances.resolveInstanceToken using the
// model's own AccountManager: a known instance with an unconfigured manager errors
// (token fetch fails) and an unknown name errors (instance lookup fails) — the same
// two-branch behavior the underlying resolver has. This is the resolver the root
// injects into the archive handler via SetTokenResolver.
func TestResolveInstanceToken_DelegatesToResolver(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	inst := NewInstance(bundle, "keep", "https://keep.invalid", testCRN("keep"), icl.EnvProd, "%.2f")
	am := testAccountManager()
	am.SetOIDCForTest(icl.EnvProd, "http://unused.invalid")
	m := newCollectModel(t, am, Instances{inst})

	_, _, err := m.ResolveInstanceToken(t.Context(), inst.CRN)
	require.Error(t, err, "known instance with an unconfigured manager must error (token fetch fails)")

	_, _, err = m.ResolveInstanceToken(t.Context(), "unknown")
	assert.Error(t, err, "unknown instance name must error via the delegated resolver")
}
