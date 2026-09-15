package keys

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contexts maps a binding slice to its ordered Context strings for compact
// assertions.
func contexts(binds []Binding) []string {
	out := make([]string, len(binds))
	for i, b := range binds {
		out[i] = b.Context
	}
	return out
}

func TestGroupByCategory_OrdersSectionsAndSortsWithin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []Binding
		want  []string
	}{
		{
			name: "sections ordered this-view, global, navigation; sorted within",
			input: []Binding{
				{Context: "move to top", Category: CatNavigation},
				{Context: "exit program", Category: CatGlobal},
				{Context: "toggle mark on log"},           // CatThisView (zero value)
				{Context: "copy entire log to clipboard"}, // CatThisView
				{Context: "move to bottom", Category: CatNavigation},
				{Context: "show tab to the left", Category: CatGlobal},
			},
			want: []string{
				"copy entire log to clipboard",
				"toggle mark on log",
				"exit program",
				"show tab to the left",
				"move to bottom",
				"move to top",
			},
		},
		{
			name: "single section keeps its order",
			input: []Binding{
				{Context: "open context menu"},
				{Context: "focus search bar"},
			},
			want: []string{
				"focus search bar",
				"open context menu",
			},
		},
		{
			name:  "no bindings yields empty",
			input: nil,
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := GroupByCategory(tt.input)
			assert.Equal(t, tt.want, contexts(got))
		})
	}
}

func TestGroupByCategory_PreservesActionsAndCount(t *testing.T) {
	t.Parallel()

	in := []Binding{
		{Context: "exit program", Category: CatGlobal, Action: func() {}},
		{Context: "move to top", Category: CatNavigation, Action: func() {}},
		{Context: "select Nth tab", Category: CatGlobal}, // show-only, nil action
	}
	got := GroupByCategory(in)

	require.Len(t, got, len(in), "grouping must not add or drop rows")
	for _, b := range got {
		if b.Context == "select Nth tab" {
			assert.Nil(t, b.Action, "show-only binding keeps its nil action")
			continue
		}
		assert.NotNil(t, b.Action, "real binding keeps its action through grouping")
		assert.NotEqual(t, CatThisView, b.Category, "category is preserved")
	}
}
