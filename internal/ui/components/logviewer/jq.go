package logviewer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/itchyny/gojq"
)

type JQChunkMsg struct {
	Instance string
	// ID is the LogStore queryID captured at spawn time. HandleJQChunk rejects
	// any chunk whose ID no longer matches the store's queryID — a chunk that
	// finished computing against pre-Clear data must not index the emptied logs.
	ID      uint64
	Offset  int
	Results []any
	Err     error
}

func jqBatch(ctx context.Context, q *gojq.Query, s []map[string]any) ([]any, error) {
	results := make([]any, len(s))

	for i := range s {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			result, err := jqSingle(ctx, q, s[i])
			if err != nil {
				return nil, err
			}
			results[i] = result
		}
	}

	return results, nil
}

// jqSingle runs query over one log's data and returns the value stored as that
// entry's jq output. A single object/array output is returned as-is: it is
// already the value the renderer wants, so it is never serialised and reparsed.
func jqSingle(ctx context.Context, query *gojq.Query, in map[string]any) (any, error) {
	var values []any
	// gojq.RunWithContext calls normalizeNumbers, which rewrites every container
	// key IN PLACE (normalize.go: `v[k] = normalizeNumbers(x)`). `in` aliases the
	// shared, documented-immutable entry.data map that concurrent search/filter/
	// marshal workers read lock-free — handing it to gojq races those readers and
	// silently corrupts the originals. Clone first so gojq only ever mutates a
	// private copy.
	iter := query.RunWithContext(ctx, cloneJSONValue(in))
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			var haltErr *gojq.HaltError
			if errors.As(err, &haltErr) && haltErr.Value() == nil {
				break
			}
			// not a "real" error, object could not be parsed, which can happen
			return fmt.Sprintf("LOGNAV-JQERR: %s", err), nil
		}

		// The empty output is dropped, as before: of all the renderings below
		// only a string value ever rendered to the empty string.
		if s, isString := v.(string); isString && s == "" {
			continue
		}
		values = append(values, v)
	}

	if len(values) == 1 {
		return jqValue(values[0])
	}

	// Zero or many outputs keep the original render-join-reparse: the bracketed
	// ", "-separated list, and the "[]" that no output collapses to, are what the
	// renderer and the filters have always seen.
	parts := make([]string, len(values))
	for i, v := range values {
		s, err := jqValueString(v)
		if err != nil {
			return nil, err
		}
		parts[i] = s
	}
	joined := fmt.Sprintf("[%s]", strings.Join(parts, ", "))

	var parsed any
	if err := jsonutil.API.Unmarshal([]byte(joined), &parsed); err == nil {
		return parsed, nil
	}
	return joined, nil
}

// jqValue converts one gojq output value into an entry's jq output.
//
// Objects and arrays pass through untouched — storing the container gojq
// already built saves a marshal and an unmarshal per log per jq run.
//
// The string branch keeps its parse-or-keep-raw expansion so a JSON-encoded
// container held in a string field still expands and pretty-prints in the
// expanded view, but it now only replaces the string when the parse yields a
// container: a string that merely looks numeric stays the string jq produced.
//
// Remaining scalars keep the old render-then-reparse conversion, so numbers,
// booleans and null read exactly as they did before.
func jqValue(v any) (any, error) {
	switch v := v.(type) {
	case map[string]any, map[string]string, []any:
		return v, nil

	case string:
		var parsed any
		if err := jsonutil.API.Unmarshal([]byte(v), &parsed); err == nil && isJSONContainer(parsed) {
			return parsed, nil
		}
		return v, nil

	default:
		s, err := jqValueString(v)
		if err != nil {
			return nil, err
		}
		var parsed any
		if err := jsonutil.API.Unmarshal([]byte(s), &parsed); err == nil {
			return parsed, nil
		}
		return s, nil
	}
}

// jqValueString renders one gojq output value as the string the multi-value
// join concatenates.
func jqValueString(v any) (string, error) {
	switch v := v.(type) {
	case map[string]any, map[string]string:
		b, err := jsonutil.API.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(b), nil

	case string:
		return v, nil

	case int:
		return strconv.Itoa(v), nil

	default:
		return fmt.Sprintf("%#v", v), nil
	}
}

func isJSONContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

// cloneJSONValue deep-copies the JSON-shaped container structure of v
// (map[string]any / []any), recursing through every level while sharing scalar
// leaves by value. This is exactly the set of containers gojq's normalizeNumbers
// descends into and mutates, so a clone fully isolates the caller's data from
// gojq's in-place rewrites. Scalars (numbers, strings, bool, nil) are immutable
// values, so copying the enclosing container is sufficient.
func cloneJSONValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = cloneJSONValue(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = cloneJSONValue(x)
		}
		return out
	default:
		return v
	}
}

func isEmpty(a any) bool {
	if a == nil {
		return true
	}

	switch a := a.(type) {
	case string:
		return a == "" || a == "<nil>"
	case []any:
		return len(a) == 0
	case map[any]any:
		return len(a) == 0
	case int:
		return a == 0
	default:
		return false
	}
}
