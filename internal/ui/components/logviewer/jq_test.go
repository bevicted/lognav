package logviewer

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"
)

// TestJQSingle_DoesNotMutateInput guards the root cause behind the jq/search
// data race: gojq.RunWithContext calls normalizeNumbers, which rewrites every
// container key in place (normalize.go: `v[k] = normalizeNumbers(x)`). entry.data
// is documented immutable and read lock-free by concurrent search/filter/marshal
// workers, so a jq worker must never hand the shared map straight to gojq. Here
// the input carries json.Number leaves at every nesting level; after jqSingle the
// caller's maps/slices must still hold json.Number, proving gojq mutated only a
// private copy.
func TestJQSingle_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	q, err := gojq.Parse(".")
	require.NoError(t, err)

	nested := map[string]any{"n": json.Number("42")}
	arr := []any{json.Number("7")}
	in := map[string]any{
		"top":    json.Number("1"),
		"nested": nested,
		"arr":    arr,
	}

	_, err = jqSingle(context.Background(), q, in)
	require.NoError(t, err)

	require.IsType(t, json.Number(""), in["top"], "top-level number mutated in place")
	require.IsType(t, json.Number(""), nested["n"], "nested-map number mutated in place")
	require.IsType(t, json.Number(""), arr[0], "array number mutated in place")
}

// jqRun parses expr and runs it over in, failing the test on parse/run errors.
func jqRun(t *testing.T, expr string, in map[string]any) any {
	t.Helper()
	q, err := gojq.Parse(expr)
	require.NoError(t, err)
	out, err := jqSingle(context.Background(), q, in)
	require.NoError(t, err)
	return out
}

// TestJQSingle_ContainerPassThrough pins the ticket-19 contract: an object or
// array result is the value gojq built, handed back untouched. The 2^53+1 leaf
// is the probe — a marshal/unmarshal round trip decodes it as a float64 and
// loses the low bit, so re-marshalling to the exact digits proves no round trip
// happened.
func TestJQSingle_ContainerPassThrough(t *testing.T) {
	t.Parallel()

	in := map[string]any{
		"obj": map[string]any{"big": int64(9007199254740993), "s": "v"},
		"arr": []any{int64(9007199254740993), "x"},
	}

	obj, ok := jqRun(t, ".obj", in).(map[string]any)
	require.True(t, ok, "object result must stay a map")
	b, err := jsonutil.API.Marshal(obj)
	require.NoError(t, err)
	require.JSONEq(t, `{"big":9007199254740993,"s":"v"}`, string(b), "container round-tripped through JSON")

	arr, ok := jqRun(t, ".arr", in).([]any)
	require.True(t, ok, "array result must stay a slice")
	b, err = jsonutil.API.Marshal(arr)
	require.NoError(t, err)
	require.JSONEq(t, `[9007199254740993,"x"]`, string(b), "container round-tripped through JSON")
}

// TestJQSingle_StringBranch covers the deliberately-kept string handling: a
// JSON-encoded container in a string field is expanded (so the expanded view
// pretty-prints it), while a string that merely looks numeric — or is not JSON
// at all — is handed back as the string jq produced.
func TestJQSingle_StringBranch(t *testing.T) {
	t.Parallel()

	in := map[string]any{
		"objstr": `{"a":1}`,
		"arrstr": `[1,2]`,
		"num":    "1.50",
		"plain":  "hello world",
	}

	require.Equal(t, map[string]any{"a": float64(1)}, jqRun(t, ".objstr", in))
	require.Equal(t, []any{float64(1), float64(2)}, jqRun(t, ".arrstr", in))
	require.Equal(t, "1.50", jqRun(t, ".num", in), "numeric-looking string must not be retyped")
	require.Equal(t, "hello world", jqRun(t, ".plain", in))
}

// TestJQSingle_JoinAndEmptyCases pins the multi-value join and the
// empty-string / no-output / null cases, all unchanged by ticket 19.
func TestJQSingle_JoinAndEmptyCases(t *testing.T) {
	t.Parallel()

	in := map[string]any{
		"a":     map[string]any{"k": 1},
		"b":     map[string]any{"q": 2},
		"msg":   "hello",
		"n":     3,
		"empty": "",
	}

	// All-container outputs join into a JSON array.
	require.Equal(t,
		[]any{map[string]any{"k": float64(1)}, map[string]any{"q": float64(2)}},
		jqRun(t, ".a, .b", in))
	// A join that is not valid JSON stays the raw bracketed string.
	require.Equal(t, "[hello, 3]", jqRun(t, ".msg, .n", in))
	// Empty-string outputs are dropped; a lone survivor is a single value.
	require.Equal(t, "hello", jqRun(t, ".msg, .empty", in))
	// No output at all collapses to the empty slice.
	require.Equal(t, []any{}, jqRun(t, "empty", in))
	require.Equal(t, []any{}, jqRun(t, ".empty", in))
	// null keeps its %#v rendering, which isEmpty reads as empty.
	require.Equal(t, "<nil>", jqRun(t, ".missing", in))
	require.True(t, isEmpty(jqRun(t, ".missing", in)))
}

// TestJQSingle_ErrorSentinelStaysString guards the LOGNAV-JQERR sentinel: it is
// a string the renderer prints verbatim, never a parsed value.
func TestJQSingle_ErrorSentinelStaysString(t *testing.T) {
	t.Parallel()

	out := jqRun(t, ".msg | keys", map[string]any{"msg": "hello"})
	s, ok := out.(string)
	require.True(t, ok, "error sentinel must stay a string")
	require.True(t, strings.HasPrefix(s, "LOGNAV-JQERR: "), "unexpected sentinel: %q", s)
}

// TestJQSingle_NoRaceWithConcurrentReader reproduces race.txt directly: a jq
// worker and a search/marshal worker touching the SAME data map concurrently.
// Pre-fix, normalizeNumbers writes the map while json.Marshal reads it → DATA
// RACE (run under -race). Post-fix jqSingle clones first, so the shared map is
// read-only across all workers.
func TestJQSingle_NoRaceWithConcurrentReader(t *testing.T) {
	t.Parallel()

	q, err := gojq.Parse(".")
	require.NoError(t, err)

	in := map[string]any{
		"a": json.Number("1"),
		"b": map[string]any{"c": json.Number("2")},
		"d": []any{json.Number("3"), "x"},
	}

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = jqSingle(context.Background(), q, in) }()
		go func() { defer wg.Done(); _, _ = json.Marshal(in) }()
	}
	wg.Wait()
}
