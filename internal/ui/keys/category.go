package keys

import (
	"slices"
	"strings"
)

// Category buckets a keybind into one of the sectioned help-menu groups. The
// zero value is CatThisView so that uncategorized component feature bindings
// fall into the "Local" section without an explicit tag.
type Category int

const (
	// CatThisView holds the active tab's context actions — answers the user's
	// "what can I do here?" question, so it sorts first.
	CatThisView Category = iota
	// CatGlobal holds bindings available regardless of the active tab (quit,
	// tab switching, help).
	CatGlobal
	// CatNavigation holds cursor/page movement, shared across components.
	CatNavigation
)

// String returns the section title shown as the help-menu header.
func (c Category) String() string {
	switch c {
	case CatGlobal:
		return "Global"
	case CatNavigation:
		return "Navigation"
	default:
		return "Local"
	}
}

// categoryOrder is the help-menu section order: actions first (most valuable to
// "how do I proceed?"), then global, then movement (users try the arrows).
var categoryOrder = []Category{CatThisView, CatGlobal, CatNavigation}

// GroupByCategory reorders binds into help-menu sections — Local, then Global,
// then Navigation — each sorted alphabetically by description. Sections
// are contiguous so the per-row colored category column reads as a block; the
// column itself (rendered by the help overlay) replaces any separator row. The
// returned slice stays index-aligned with the help overlay's rows, so the root
// resolves a focused row to its action by the same index.
func GroupByCategory(binds []Binding) []Binding {
	out := make([]Binding, 0, len(binds))
	for _, cat := range categoryOrder {
		group := make([]Binding, 0, len(binds))
		for _, b := range binds {
			if b.Category == cat {
				group = append(group, b)
			}
		}
		slices.SortStableFunc(group, func(a, b Binding) int {
			return strings.Compare(strings.ToLower(a.Context), strings.ToLower(b.Context))
		})
		out = append(out, group...)
	}
	return out
}
