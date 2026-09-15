// Package statetest provides test-only helpers for state.Manager.
package statetest

import (
	"testing"

	"github.com/bevicted/lognav/internal/state"
)

// NewTestManager returns a fresh state.Manager scoped to t. Each call
// yields an independent *state.Manager so tests can run with t.Parallel()
// without interfering with each other.
//
// The helper exists so AGENTS.md guidance points at one named entry-point,
// and future test-only setup (logging shims, seeded state, cleanup hooks)
// can be added here without changing every test call site.
func NewTestManager(t *testing.T) *state.Manager {
	t.Helper()
	return state.New()
}
