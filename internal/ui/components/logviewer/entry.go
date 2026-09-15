package logviewer

import (
	"bytes"
	"strings"
	"sync"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/jsonutil"
)

// Should never be changed.
type RawLog map[string]any

type entry struct {
	metadata icl.Metadata

	// wildcard data that can be modified by the user, uses `data` as base
	modifiedData any

	// original data provided by icl, which should not be modified
	data RawLog

	// getWidth returns the current line-wrap column count for expanded entries.
	// Owned by the parent LogStore; nil during tests where wrapping is irrelevant.
	getWidth func() int

	mu *sync.RWMutex
}

func newEntry(getWidth func() int, mu *sync.RWMutex, l icl.Log) entry {
	return entry{
		metadata: l.Metadata,
		data:     l.Data,
		getWidth: getWidth,
		mu:       mu,
	}
}

// Marshals entry.
func (e entry) marshal(expand bool) ([]byte, error) {
	var v any
	v, modified, unlock := e.GetData()
	defer unlock()

	if modified != nil {
		if s, isString := modified.(string); isString {
			if expand {
				return []byte(s), nil
			}
			return []byte(strings.ReplaceAll(s, "\n", "\\n")), nil
		}
		v = modified
	}

	if expand {
		return jsonutil.API.MarshalIndent(v, "", "    ")
	}
	return jsonutil.API.Marshal(v)
}

// Marshals entry, wraps if expanded.
func (e entry) Bytes(expand bool) []byte {
	b, err := e.marshal(expand)
	if err != nil {
		return []byte(err.Error())
	}
	if expand {
		w := 0
		if e.getWidth != nil {
			w = e.getWidth()
		}
		b = wrapLinesAt(b, w)
	}
	return b
}

// Calls Bytes, splits result into lines.
func (e entry) ByteLines(expand bool) [][]byte {
	return bytes.Split(e.Bytes(expand), []byte{'\n'})
}

// Returns entry's data, modifiedData as well mutex unlock fn.
// Read locks mutex, caller owns unlock.
func (e entry) GetData() (RawLog, any, func()) {
	e.mu.RLock()
	return e.data, e.modifiedData, e.mu.RUnlock
}

// Sets modifiedData to v.
func (e entry) WithData(v any) entry {
	return entry{
		metadata:     e.metadata,
		modifiedData: v,
		data:         e.data,
		getWidth:     e.getWidth,
		mu:           e.mu,
	}
}
