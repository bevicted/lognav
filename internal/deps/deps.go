// Package deps holds lognav's shared dependency bundle. Every component
// constructor takes a deps.Bundle by value; the bundle's fields point at
// the application's long-lived shared state (config, state manager, …).
//
// The bundle replaces the singletons that lived in config/ and state/
// before clusters A.4.1 and A.4.2. A test-only NewTest helper lives in
// deps/depstest/ so production deps does not import "testing".
package deps

import (
	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/state"
)

// Bundle groups the shared dependencies passed to every component
// constructor.
//
// Value semantics: Bundle is intentionally a value type so callers do not
// need to worry about nil pointers or aliased state. Every field is a
// pointer to a long-lived object. Copying a Bundle yields a new struct
// that points at the same underlying objects.
//
//   - Config: read-only after startup; threaded by value through all
//     constructors.
//   - State:  concurrency-safe read/write. The Manager exposes its own
//     RWMutex; callers do not need external synchronization.
//
// Pointer methods would change this contract; do not add `*Bundle`
// receivers without first reconsidering the value-semantics design.
type Bundle struct {
	Config *config.Config
	State  *state.Manager
}

// New constructs a Bundle from a fully-loaded *config.Config and a
// freshly-constructed *state.Manager. cmd/root.go calls this once at
// startup; tests use deps/depstest.NewTest(t) instead. Panics if either
// argument is nil to surface startup-wiring mistakes immediately.
func New(cfg *config.Config, mgr *state.Manager) Bundle {
	if cfg == nil || mgr == nil {
		panic("deps.New: cfg and mgr must be non-nil")
	}
	return Bundle{Config: cfg, State: mgr}
}
