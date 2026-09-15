package snapshot

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs all snapshot tests under goleak, catching any goroutine
// leaked by gzip readers, sonic decoders, or the new gunzipDecode path.
// F.1 left snapshot/ without this gate because gunzipRead is
// single-goroutine; F.2 D22 adds it for the new streaming surface.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
