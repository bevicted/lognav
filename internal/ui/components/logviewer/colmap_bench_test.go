package logviewer

import (
	"strings"
	"testing"
)

const benchLineASCII = 200

// benchWideLine returns ~200 runes mixing CJK, emoji, and ASCII so the
// wide bench exercises non-trivial rune-width handling. The chosen
// 7-rune pattern (2 CJK, 1 emoji, 4 ASCII) yields roughly 14 bytes per
// chunk; we build at least 200 runes' worth.
func benchWideLine() []byte {
	const pattern = "你好🌟abcXYZ" // 7 runes per chunk
	const targetRunes = 200
	var sb strings.Builder
	runes := 0
	for runes < targetRunes {
		sb.WriteString(pattern)
		runes += 7
	}
	return []byte(sb.String())
}

// BenchmarkByteToCol_ASCII measures byteToCol over a 200-byte all-ASCII
// line — the fast path (1 byte == 1 rune == 1 column).
func BenchmarkByteToCol_ASCII(b *testing.B) {
	line := []byte(strings.Repeat("a", benchLineASCII))
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = byteToCol(line)
	}
}

// BenchmarkByteToCol_Wide measures byteToCol over a mixed CJK/emoji/
// ASCII line — the slow path (variable byte/rune/column width per
// position).
func BenchmarkByteToCol_Wide(b *testing.B) {
	line := benchWideLine()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = byteToCol(line)
	}
}
