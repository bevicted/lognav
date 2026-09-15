package msgs

import "github.com/bevicted/lognav/internal/filter"

// ShowFilterMenuMsg triggers the filter-menu overlay. Rules carries the
// currently-applied filter rules so the menu opens prefilled. Posted off-loop by
// the logviewer `f` keybind and the filter-pill click; consumed by the root
// ui.Model, which instantiates the filtermenu overlay.
type ShowFilterMenuMsg struct {
	Rules []filter.Rule
}

// FilterAppliedMsg is a bare signal that the applied filter rules changed
// (state.Filters is the source of truth). The root routes it to
// logviewer.OnFilterApplied, which recomputes the filtered set.
type FilterAppliedMsg struct{}
