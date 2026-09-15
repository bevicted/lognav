package deps_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/state"
)

func TestNew_StoresConfigPointer(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	b := deps.New(cfg, state.New())
	assert.Same(t, cfg, b.Config, "Bundle.Config should be the same pointer passed to New")
}

func TestNew_NilArgs_Panics(t *testing.T) {
	t.Parallel()

	cfg := config.New()
	mgr := state.New()

	tests := []struct {
		name string
		cfg  *config.Config
		mgr  *state.Manager
	}{
		{name: "nil cfg", cfg: nil, mgr: mgr},
		{name: "nil mgr", cfg: cfg, mgr: nil},
		{name: "both nil", cfg: nil, mgr: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.PanicsWithValue(t, "deps.New: cfg and mgr must be non-nil", func() {
				deps.New(tc.cfg, tc.mgr)
			})
		})
	}
}

// TestBundle_ValueCopyAliasesUnderlyingConfig documents the Bundle's
// value-copy semantics: copying a Bundle yields an independent struct
// that points at the same underlying *config.Config. This is the
// intended contract; the test is design documentation, not language
// verification.
func TestBundle_ValueCopyAliasesUnderlyingConfig(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	a := deps.New(cfg, state.New())
	b := a
	require.Same(t, a.Config, b.Config)
	a.Config.Version = 42
	assert.Equal(t, 42, b.Config.Version)
}
