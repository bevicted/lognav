// Package msgs's tests do not currently spawn goroutines, but goleak is
// enabled here per AGENTS.md so any future goroutine-spawning tests are
// covered without separate setup.
package msgs

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
