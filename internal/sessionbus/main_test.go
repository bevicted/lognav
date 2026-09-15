// Package sessionbus tests run sequentially when they create unix sockets named
// after os.Getpid(): a single go-test binary has one PID, so PID-named socket
// files would collide if integration tests ran in parallel. Unit tests that
// touch only locally-scoped values may call t.Parallel() — they are explicitly
// opted in.
package sessionbus

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
