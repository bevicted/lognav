package logviewer

import (
	"slices"

	ahocorasick "github.com/petar-dambovaliev/aho-corasick"

	"github.com/bevicted/lognav/internal/config"
	uv "github.com/charmbracelet/ultraviolet"
)

// matchSpan is one resolved colorization span on a single rendered line.
// Start/End are display-column offsets (display width), End exclusive.
// Style is pre-resolved at engine init so the render path avoids per-cell
// color lookups.
type matchSpan struct {
	Start int
	End   int
	Style uv.Style
}

// colorRuleEngine holds the AC automaton and the per-pattern pre-resolved
// styles. It is built once from the merged color-rule list at Model
// construction time and owned by a single logviewer.Model.
type colorRuleEngine struct {
	ac      ahocorasick.AhoCorasick
	styles  []uv.Style
	enabled bool
}

// newColorRuleEngine builds an engine from a resolved rule list. An empty
// list yields a disabled engine whose scan is a no-op.
func newColorRuleEngine(rules config.ColorRuleList) *colorRuleEngine {
	if len(rules) == 0 {
		return &colorRuleEngine{}
	}
	patterns := make([]string, len(rules))
	styles := make([]uv.Style, len(rules))
	for i, r := range rules {
		patterns[i] = r.Match
		styles[i] = uv.Style{Fg: r.Fg.Color, Bg: r.Bg.Color}
	}
	acb := ahocorasick.NewAhoCorasickBuilder(ahocorasick.Opts{})
	return &colorRuleEngine{
		ac:      acb.Build(patterns),
		styles:  styles,
		enabled: true,
	}
}

// scan finds and resolves all color-rule matches on a single rendered line.
// Returns spans in line-source order with overlaps resolved by:
//  1. longest match wins
//  2. on same length+start, the lower patternIdx (earlier rule) wins
//
// cm MUST be non-nil when e.enabled; the disabled-engine branch returns nil
// at the top check before any cm access (so passing nil cm in disabled mode
// is safe). The same-package single caller (getRenderCacheEntry) always
// satisfies this precondition.
func (e *colorRuleEngine) scan(line []byte, cm colMap) []matchSpan {
	if !e.enabled || len(line) == 0 {
		return nil
	}

	type rawHit struct {
		patternIdx int
		start, end int // byte offsets at first, then column offsets
	}
	var hits []rawHit

	it := e.ac.IterOverlappingByte(line)
	for m := it.Next(); m != nil; m = it.Next() {
		hits = append(hits, rawHit{
			patternIdx: m.Pattern(),
			start:      m.Start(),
			end:        m.End(),
		})
	}
	if len(hits) == 0 {
		return nil
	}

	for i := range hits {
		hits[i].start = cm.MapByte(hits[i].start)
		hits[i].end = cm.MapByte(hits[i].end)
	}

	// Overlap resolution: sort by (start asc, length desc, patternIdx asc)
	// then sweep: accept hits that start at or after nextStart. Stable sort
	// preserves input order on equal keys for deterministic dedup.
	slices.SortStableFunc(hits, func(a, b rawHit) int {
		if a.start != b.start {
			return a.start - b.start
		}
		la := a.end - a.start
		lb := b.end - b.start
		if la != lb {
			return lb - la
		}
		return a.patternIdx - b.patternIdx
	})

	out := make([]matchSpan, 0, len(hits))
	nextStart := -1
	for _, h := range hits {
		if h.start < nextStart {
			continue
		}
		out = append(out, matchSpan{
			Start: h.start,
			End:   h.end,
			Style: e.styles[h.patternIdx],
		})
		nextStart = h.end
	}
	return out
}

// resolveColorRules builds the effective rule list by upserting defaults
// (when enabled) then extras into a single slice keyed by Match, then
// dropping any rule that has neither Fg nor Bg set. The drop step lets a
// no-color extra disable a default with the same Match.
func resolveColorRules(defaults, extras config.ColorRuleList, includeDefaults bool) config.ColorRuleList {
	out := config.ColorRuleList{}
	upsert := func(r config.ColorRule) {
		for i := range out {
			if out[i].Match == r.Match {
				out[i] = r
				return
			}
		}
		out = append(out, r)
	}
	if includeDefaults {
		for _, r := range defaults {
			upsert(r)
		}
	}
	for _, r := range extras {
		upsert(r)
	}
	filtered := out[:0]
	for _, r := range out {
		if r.Fg.Color == nil && r.Bg.Color == nil {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}
