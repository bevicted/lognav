package runtime

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bevicted/lognav/internal/jsonutil"
)

// reproEnv switches the re-executed child into crash mode.
const reproEnv = "LOGNAV_MAPRACE_REPRO"

// TestPanicGuard_CannotCatchConcurrentMapFatal pins down the documented limit of
// the panic guards (panicguard.go): they catch panics, and a racing map marshal
// is NOT a panic.
//
// This reproduces the 2026-08-01 field crash. A poster goroutine marshals a
// map[string]any with sonic (SortMapKeys, as jsonutil.API is configured) while
// another goroutine mutates it — the exact shape produced when the pre-e71d4fb
// jqSingle handed entry.data straight to gojq (whose normalizeNumbers rewrites
// container keys in place) while CompressInstanceLogs marshalled those same
// aliased maps off-loop with no lock held. The field trace and this repro agree
// frame-for-frame down to the instruction offsets:
//
//	sonic/internal/encoder/alg.IteratorStart  mapiter.go:204 +0xd4
//	sonic/internal/encoder/vm.Execute         vm.go:213      +0x114c
//	sonic/internal/encoder/vm.EncodeTypedPointer stbus.go:38 +0x144
//
// The fault surfaces as `fatal error: concurrent map iteration and map write`,
// a runtime throw: no deferred function runs, so neither recoverLoop nor the
// poster guard fires and the terminal is still left dirty. If anyone later
// assumes the guards make the TUI crash-proof, this test is the counter-example.
//
// The race is run in a re-executed child so the crash is observed rather than
// suffered. A child that happens not to trip the detector skips (timing), but it
// has never done so in practice — the detector fires within ~0.3s.
func TestPanicGuard_CannotCatchConcurrentMapFatal(t *testing.T) {
	if os.Getenv(reproEnv) == "1" {
		raceMarshalUntilFatal()
		return // unreachable in practice; the child dies inside the call above
	}

	//nolint:gosec // G204: os.Args[0] is this test binary re-executing itself with fixed literal flags; no external input reaches the command line.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=TestPanicGuard_CannotCatchConcurrentMapFatal", "-test.count=1")
	cmd.Env = append(os.Environ(), reproEnv+"=1")
	out, err := cmd.CombinedOutput()
	got := string(out)

	if err == nil {
		t.Skipf("child did not trip the map-race detector this run; output:\n%s", got)
	}

	// The fault must be a runtime THROW, not a panic — that is the whole point.
	if !strings.Contains(got, "fatal error: concurrent map iteration and map write") {
		t.Fatalf("child died, but not with the expected map-race fatal:\n%s", got)
	}
	// ...and it must come through sonic's sorted-key map iterator, the frame the
	// field trace showed.
	if !strings.Contains(got, "mapiter.go") {
		t.Errorf("fault did not surface through sonic's map iterator:\n%s", got)
	}
	// A `fatal error` runs no defers, so the poster guard must be absent from the
	// output. If this ever starts appearing, Go changed its throw semantics and
	// panicguard.go's caveat needs revisiting.
	if strings.Contains(got, "recovered panic") {
		t.Errorf("guard unexpectedly recovered a runtime throw; revisit panicguard.go:\n%s", got)
	}
}

// raceMarshalUntilFatal drives the sonic-marshal / map-write race that killed the
// field session. Runs only in the re-executed child.
func raceMarshalUntilFatal() {
	const payloads = 24
	maps := make([]map[string]any, payloads)
	for i := range maps {
		m := map[string]any{}
		for k := range 40 {
			m[string(rune('a'+k%26))+string(rune('a'+k/26))] = k
		}
		maps[i] = m
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writer: the normalizeNumbers-equivalent in-place key rewrite.
	wg.Go(func() {
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			m := maps[n%payloads]
			m["grow"+string(rune('a'+n%26))] = n
			delete(m, "grow"+string(rune('a'+(n+13)%26)))
		}
	})

	// Readers: CompressInstanceLogs' sonic.Marshal of the aliased maps.
	for range 8 {
		wg.Go(func() {
			for n := range 200000 {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = jsonutil.API.Marshal(maps[n%payloads])
			}
		})
	}

	time.AfterFunc(20*time.Second, func() { close(stop) })
	wg.Wait()
}
