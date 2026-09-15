package snapshot

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/jsonutil"
)

const (
	benchGzipSmall      = 1 << 10 // 1 KiB
	benchGzipLarge      = 1 << 20 // 1 MiB
	benchGunzipLogCount = 5000
)

// newJSONLikeBytes returns size bytes of deterministic pseudo-JSON
// (key/value pairs with mixed numeric and string values). Compresses
// well so gunzip work is non-trivial.
func newJSONLikeBytes(r *rand.Rand, size int) []byte {
	var buf bytes.Buffer
	for buf.Len() < size {
		fmt.Fprintf(&buf,
			`{"i":%d,"id":"req-%08d","msg":"hello world payload"}`+"\n",
			r.IntN(1<<20), r.IntN(1<<24),
		)
	}
	out := buf.Bytes()
	if len(out) > size {
		out = out[:size]
	}
	return out
}

// newGzippedBuffer compresses size bytes of pseudo-JSON via gzip.Writer
// (default compression, mtime zeroed for byte-identical output) and
// returns the encoded byte slice. Per D17, BenchmarkGunzipRead_*
// constructs a fresh bytes.NewReader(buf) per b.Loop() iteration so
// iteration N>1 does not decompress a drained source.
func newGzippedBuffer(tb testing.TB, size int) []byte {
	tb.Helper()
	payload := newJSONLikeBytes(rand.New(rand.NewPCG(42, 0)), size) //nolint:gosec // benchmark fixture; crypto-quality randomness unnecessary
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.ModTime = time.Time{}
	if _, err := gz.Write(payload); err != nil {
		tb.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		tb.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// BenchmarkGunzipRead_Small measures gzip decompression of a 1 KiB
// payload. Bypasses the snapshot Container's ReadFrame (D17) — the
// timed region runs only gzip.NewReader + io.ReadAll.
func BenchmarkGunzipRead_Small(b *testing.B) {
	compressed := newGzippedBuffer(b, benchGzipSmall)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		gz, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			b.Fatalf("gzip.NewReader: %v", err)
		}
		if _, err := io.ReadAll(gz); err != nil {
			b.Fatalf("ReadAll: %v", err)
		}
		_ = gz.Close()
	}
}

// BenchmarkGunzipRead_Large measures gzip decompression of a 1 MiB
// payload — the more realistic snapshot frame size.
func BenchmarkGunzipRead_Large(b *testing.B) {
	compressed := newGzippedBuffer(b, benchGzipLarge)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		gz, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			b.Fatalf("gzip.NewReader: %v", err)
		}
		if _, err := io.ReadAll(gz); err != nil {
			b.Fatalf("ReadAll: %v", err)
		}
		_ = gz.Close()
	}
}

// newGzippedLogsBuffer compresses a synthetic []icl.Log JSON payload of
// logCount records. Used by BenchmarkGunzipUnmarshal_Large and
// BenchmarkGunzipReadAllUnmarshal_Large — payload shape mirrors what
// snapshot frames actually contain so the gate decision reflects
// production cost.
func newGzippedLogsBuffer(tb testing.TB, logCount int) []byte {
	tb.Helper()
	r := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for reproducible benchmark data
	logs := make([]icl.Log, logCount)
	for i := range logs {
		logs[i] = icl.Log{
			Data: map[string]any{
				"msg":  fmt.Sprintf("event %d at offset %d", i, r.IntN(1<<16)),
				"code": r.IntN(500),
				"id":   fmt.Sprintf("req-%08d", i),
				"tag":  fmt.Sprintf("lvl%d", i%20),
			},
			Metadata: icl.Metadata{
				Severity: icl.SeverityInfo,
				TSMicro:  int64(1_000_000 + i),
			},
		}
	}
	payload, err := jsonutil.API.Marshal(logs)
	if err != nil {
		tb.Fatalf("marshal logs: %v", err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.ModTime = time.Time{} // F.1 D10 — byte-identical encoding across runs
	if _, err := gz.Write(payload); err != nil {
		tb.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		tb.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// BenchmarkGunzipUnmarshal_Large measures streaming decompression + decode
// end-to-end: gzip.NewReader(bytes.NewReader(compressed)) ->
// sonic.NewDecoder(gz).Decode(&logs) -> drain. Fresh bytes.NewReader per
// iteration (F.1 D17 carried forward). Close happens in each path so an
// in-bench b.Fatalf does NOT leak a file descriptor (D11).
func BenchmarkGunzipUnmarshal_Large(b *testing.B) {
	compressed := newGzippedLogsBuffer(b, benchGunzipLogCount)
	b.ReportAllocs()
	for b.Loop() {
		var logs []icl.Log
		gz, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			b.Fatalf("gzip.NewReader: %v", err)
		}
		if err := jsonutil.API.NewDecoder(gz).Decode(&logs); err != nil {
			_ = gz.Close()
			b.Fatalf("Decode: %v", err)
		}
		if _, err := io.Copy(io.Discard, gz); err != nil { //nolint:gosec // benchmark drain of bounded test data; not a decompression bomb risk
			_ = gz.Close()
			b.Fatalf("drain: %v", err)
		}
		if err := gz.Close(); err != nil {
			b.Fatalf("Close: %v", err)
		}
	}
}

// BenchmarkGunzipReadAllUnmarshal_Large is the gate sibling: same payload,
// same gzip read shape, but uses io.ReadAll + jsonutil.API.Unmarshal — the
// pre-F.2 code path. D4 lands streaming only if _Unmarshal_Large's ns/op
// 95% CI is strictly below this bench's lower bound (benchstat sibling-
// bench CI comparison; not a paired p-value test).
func BenchmarkGunzipReadAllUnmarshal_Large(b *testing.B) {
	compressed := newGzippedLogsBuffer(b, benchGunzipLogCount)
	b.ReportAllocs()
	for b.Loop() {
		var logs []icl.Log
		gz, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			b.Fatalf("gzip.NewReader: %v", err)
		}
		data, err := io.ReadAll(gz)
		if err != nil {
			_ = gz.Close()
			b.Fatalf("ReadAll: %v", err)
		}
		if err := gz.Close(); err != nil {
			b.Fatalf("Close: %v", err)
		}
		if err := jsonutil.API.Unmarshal(data, &logs); err != nil {
			b.Fatalf("Unmarshal: %v", err)
		}
	}
}
