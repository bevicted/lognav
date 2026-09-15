//go:build tools

// Pin build-tool dependencies (e.g. benchstat used by `just bench-check`)
// so every developer installs the same version. The //go:build tools tag
// keeps this file out of regular builds; it shares package main with the
// repo-root main.go but is never compiled together with it.
package main

import (
	// benchstat (the library) pins its module; benchmath is a subpackage of
	// the same module used by `cmd/benchstat`, blank-imported here so its
	// own dependencies (github.com/aclements/go-moremath) land in go.sum.
	_ "golang.org/x/perf/benchmath"
	_ "golang.org/x/perf/benchstat"
)
