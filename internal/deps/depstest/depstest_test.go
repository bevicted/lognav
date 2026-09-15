package depstest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
)

func TestNewTest_ReturnsBundleWithDefaultConfig(t *testing.T) {
	t.Parallel()
	b := depstest.NewTest(t)
	require.NotNil(t, b.Config)
	assert.Equal(t, config.CurrentVersion, b.Config.Version, "default Config.Version matches config.CurrentVersion (set by config.New)")
}

func TestNewTest_FreshPerCall(t *testing.T) {
	t.Parallel()
	a := depstest.NewTest(t)
	b := depstest.NewTest(t)
	assert.NotSame(t, a.Config, b.Config, "each NewTest call returns an independent *Config")
}
