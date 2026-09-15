// Package icl's query tests spin httptest servers (which run goroutines);
// goleak verifies they are all torn down.
package icl

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
