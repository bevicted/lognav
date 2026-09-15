// Package filter defines the rule vocabulary for the logviewer's filter stage:
// the only pipeline stage that hides logs. A Rule is a value type so it can be
// defensively copied via slices.Clone (see state.Manager.SetFilters/Filters)
// and persisted directly in the snapshot record. This is a leaf package — it
// imports nothing from internal/, so both internal/state and the logviewer can
// depend on it without an import cycle.
package filter

// Type identifies how a Rule decides whether to keep or drop a log.
type Type uint8

const (
	// Unknown is the zero value: the trailing "Add rule" placeholder row in the
	// filter menu. It is never applied to logs and never persisted to a snapshot.
	Unknown Type = iota
	// Include keeps logs whose term matches (search Aho-Corasick engine).
	Include
	// Exclude drops logs whose term matches (search Aho-Corasick engine).
	Exclude
	// HasField keeps logs where the rule's jq expression yields a non-empty result.
	HasField
	// LacksField drops logs where the rule's jq expression yields a non-empty result.
	LacksField
)

// Label returns the human-readable chip label for the rule type, or "" for
// Unknown and any out-of-range value (the menu renders a blank chip column).
func (t Type) Label() string {
	switch t {
	case Include:
		return "include"
	case Exclude:
		return "exclude"
	case HasField:
		return "has field"
	case LacksField:
		return "lacks field"
	default:
		return ""
	}
}

// Rule is one filter clause: keep/drop logs by Type, parameterized by Value
// (a search term for Include/Exclude, a jq expression for HasField/LacksField).
// Rules combine with pure AND — a log is visible iff it passes every applied rule.
type Rule struct {
	Type  Type   `json:"type"`
	Value string `json:"value"`
}
