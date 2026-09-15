package highlight

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// FuzzHighlight pins the no-panic contract for Highlight (G[P2] D3 recover
// gate covers Go panics; cgo SIGSEGV is out of scope). Per spec D23: fuzz
// finds bugs → seed + skip + carve, do NOT fix in this cluster.
func FuzzHighlight(f *testing.F) {
	seeds := []string{
		// happy-path
		``,
		`source logs`,
		`source logs | filter level=="error"`,
		// adversarial
		"\x00",
		"\xff\xfe",
		strings.Repeat("(", 10000),
		`"`,
		`'`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		require.NotPanics(t, func() {
			_ = Highlight(s)
		})
	})
}
