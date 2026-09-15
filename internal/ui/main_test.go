package ui

import (
	"testing"

	"go.uber.org/goleak"
)

// Per spec D9: TestMain must live in each test package.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
