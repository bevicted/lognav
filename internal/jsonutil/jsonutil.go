package jsonutil

import "github.com/bytedance/sonic"

// API is the shared sonic encoder/decoder configured with sorted map keys
// for deterministic JSON output.
var API = sonic.Config{
	SortMapKeys: true,
}.Froze()
