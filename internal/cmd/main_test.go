package cmd

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain must live in each test package — Go has no facility to share
// a single TestMain across packages, so the per-package main_test.go
// files in this cluster are by language rule, not duplication.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
