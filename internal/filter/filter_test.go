package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bevicted/lognav/internal/filter"
)

func TestType_Label(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		typ  filter.Type
		want string
	}{
		{"unknown is blank", filter.Unknown, ""},
		{"include", filter.Include, "include"},
		{"exclude", filter.Exclude, "exclude"},
		{"has field", filter.HasField, "has field"},
		{"lacks field", filter.LacksField, "lacks field"},
		{"out-of-range is blank", filter.Type(99), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.typ.Label())
		})
	}
}

func TestType_IotaOrder(t *testing.T) {
	t.Parallel()
	// Unknown must be the zero value so a zero Rule is the trailing
	// placeholder (never applied/persisted).
	assert.Equal(t, filter.Unknown, filter.Type(0))
	assert.Equal(t, filter.Include, filter.Type(1))
	assert.Equal(t, filter.Exclude, filter.Type(2))
	assert.Equal(t, filter.HasField, filter.Type(3))
	assert.Equal(t, filter.LacksField, filter.Type(4))
}

func TestRule_IsValueType(t *testing.T) {
	t.Parallel()
	// Rule must be a value type (no pointer/slice/map fields) so slices.Clone
	// in state.SetFilters/Filters gives a safe defensive copy.
	a := filter.Rule{Type: filter.Include, Value: "foo"}
	b := a
	b.Value = "bar"
	assert.Equal(t, "foo", a.Value, "copying a Rule must not alias")
}
