package logviewer

import "testing"

// BenchmarkTokenize_SingleLine measures the cost of tokenizing one
// average-sized JSON log record (flat keys only). Fixture is inline —
// no count knob — because it operates on a single record.
func BenchmarkTokenize_SingleLine(b *testing.B) {
	const sample = `{"msg":"event 42 at offset 17","code":200,"id":"req-00000042","tag":"lvl2"}`
	data := []byte(sample)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = tokenize(data)
	}
}

// BenchmarkTokenize_MultiLine walks the full benchLogCount-sized store
// and tokenizes every record's serialized bytes. The serialization step
// happens BEFORE the timed region (entry.Bytes(false) outside b.Loop())
// so the bench measures tokenization only, not JSON marshaling.
func BenchmarkTokenize_MultiLine(b *testing.B) {
	store := newBenchStore(b, benchLogCount)
	// Pre-serialize via the package-internal entry slice — same-package
	// access bypasses the public ReadLog API and gives us the exact
	// byte form that production's getRenderCacheEntry feeds to tokenize.
	bytesPerLog := make([][]byte, benchLogCount)
	for i := range benchLogCount {
		bytesPerLog[i] = store.logs[i].Bytes(false)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		for _, raw := range bytesPerLog {
			_ = tokenize(raw)
		}
	}
}
