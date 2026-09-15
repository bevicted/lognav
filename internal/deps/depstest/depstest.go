// Package depstest provides a deps.Bundle builder for tests. Lives
// outside production deps/ so deps/ does not import "testing".
package depstest

import (
	"testing"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/state"
)

// NewTest returns a deps.Bundle whose Config and State are freshly
// constructed defaults, so tests using NewTest may safely call
// t.Parallel(). Each call yields independent Config and *state.Manager
// instances — there is no shared mutable state between tests. The
// parameter is testing.TB so benchmarks (which receive *testing.B) can
// also use the helper.
func NewTest(tb testing.TB) deps.Bundle {
	tb.Helper()
	return deps.New(config.New(), state.New())
}
