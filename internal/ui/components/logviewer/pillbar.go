package logviewer

import (
	"fmt"
	"strconv"
	"strings"

	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/timeline"
	uv "github.com/charmbracelet/ultraviolet"
)

// InstanceStatus is the cycle-safe display value supplied by instancepicker for
// the store currently adopted by the logviewer.
type InstanceStatus struct {
	Name       string
	Phase      string
	Timer      string
	PhaseStyle uv.Style
}

// SetInstanceStatusLookup injects the on-loop picker lookup. It is called only
// while drawing and is keyed by the adopted store's CRN, never picker cursor.
func (m *Model) SetInstanceStatusLookup(lookup func(string) (InstanceStatus, bool)) {
	m.statusLookup = lookup
}

// StatusVariants implements component.StatusProvider. The variants encode the
// normal Logs overflow policy instead of asking the shared renderer to infer it.
func (m *Model) StatusVariants() []component.StatusVariant {
	if m.timelineActive {
		return m.timelineStatusVariants()
	}

	instance, ok := m.currentInstanceStatus()
	full := m.statusPills(instance, ok, false, false, false)
	noTimer := m.statusPills(instance, ok, true, false, false)
	shortQueries := m.statusPills(instance, ok, true, true, false)
	shortHeads := m.statusPills(instance, ok, true, true, true)
	return []component.StatusVariant{full, noTimer, shortQueries, shortHeads}
}

// timelineStatusVariants delegates the selected bucket's display state while
// keeping the root-facing provider on logviewer. Timeline pills are deliberately
// display-only, so normal modifier actions cannot survive while it is active.
func (m *Model) timelineStatusVariants() []component.StatusVariant {
	selected, ok := m.timeline.Status()
	if !ok {
		return []component.StatusVariant{{}}
	}

	instance, hasInstance := m.currentInstanceStatus()
	full := m.timelineStatusPills(instance, hasInstance, selected, false, true)
	shortHeads := m.timelineStatusPills(instance, hasInstance, selected, true, true)
	noTime := m.timelineStatusPills(instance, hasInstance, selected, true, false)
	return []component.StatusVariant{full, shortHeads, noTime}
}

func (m *Model) timelineStatusPills(instance InstanceStatus, hasInstance bool, selected timeline.SelectedStatus, shortHeads, showTime bool) component.StatusVariant {
	var pills component.StatusVariant
	if hasInstance {
		pills = append(pills, component.StatusPill{
			Head:       instance.Name,
			Value:      instance.Phase,
			ValueStyle: instance.PhaseStyle,
		})
	}
	pills = append(pills, component.StatusPill{
		Head:  statusHead("Bucket", shortHeads),
		Value: fmt.Sprintf("%d/%d", selected.Position, selected.Count),
	})
	if showTime {
		pills = append(pills, component.StatusPill{
			Head:  statusHead("Time", shortHeads),
			Value: fmt.Sprintf("%s..%s UTC", selected.StartUTC, selected.EndUTC),
		})
	}
	pills = append(pills, component.StatusPill{
		Head:  statusHead("Logs", shortHeads),
		Value: strconv.Itoa(selected.Total),
	})
	return pills
}

func (m *Model) currentInstanceStatus() (InstanceStatus, bool) {
	if m.store == nil || m.statusLookup == nil {
		return InstanceStatus{}, false
	}
	return m.statusLookup(m.store.GetInstance())
}

func (m *Model) statusPills(instance InstanceStatus, hasInstance, hideTimer, capQueries, shortHeads bool) component.StatusVariant {
	var pills component.StatusVariant
	if hasInstance {
		value := instance.Phase
		if !hideTimer && instance.Timer != "" {
			value += " " + instance.Timer
		}
		pills = append(pills, component.StatusPill{Head: instance.Name, Value: value, ValueStyle: instance.PhaseStyle})
	}
	if m.store != nil && m.store.GetLogCount() > 0 {
		earliest, latest := m.store.GetTimeRange()
		pills = append(pills,
			component.StatusPill{Head: statusHead("Span", shortHeads), Value: formatSpan(latest - earliest)},
			component.StatusPill{Head: statusHead("Log", shortHeads), Value: fmt.Sprintf("%d/%d", m.cursor.log+1, m.store.GetLogCount())},
		)
	}
	if jq := m.bundle.State.JQ(); jq != "" {
		if capQueries {
			jq = uicanvas.TruncateFront(jq, 12)
		}
		pills = append(pills, component.StatusPill{Head: statusHead("JQ", shortHeads), Value: jq, Action: func() { m.openPillEditor(pillJQ) }})
	}
	if search := m.bundle.State.Search(); search != "" {
		if capQueries {
			search = uicanvas.TruncateFront(search, 12)
		}
		cur, total := m.searchCounter()
		pills = append(pills, component.StatusPill{Head: statusHead("Search", shortHeads), Value: fmt.Sprintf("%s %d/%d", search, cur, total), Action: func() { m.openPillEditor(pillSearch) }})
	}
	if rules := m.bundle.State.Filters(); len(rules) > 0 {
		pills = append(pills, component.StatusPill{Head: statusHead("Filter", shortHeads), Value: fmt.Sprintf("%d:%d", len(rules), m.hiddenCount()), Action: func() { m.openPillEditor(pillFilter) }})
	}
	return pills
}

func statusHead(head string, short bool) string {
	if short {
		return string([]rune(head)[0])
	}
	return head
}

// formatSpan renders an absolute log span in one compact unit.
func formatSpan(spanMicro int64) string {
	if spanMicro <= 0 {
		return "0ms"
	}
	unit, suffix := int64(1000), "ms"
	switch {
	case spanMicro >= 3_600_000_000:
		unit, suffix = 3_600_000_000, "h"
	case spanMicro >= 60_000_000:
		unit, suffix = 60_000_000, "m"
	case spanMicro >= 1_000_000:
		unit, suffix = 1_000_000, "s"
	}
	value := float64(spanMicro) / float64(unit)
	precision := 0
	if value < 10 {
		precision = 1
	}
	text := strconv.FormatFloat(value, 'f', precision, 64)
	text = strings.TrimSuffix(text, ".0")
	return text + suffix
}

func (m *Model) searchCounter() (cur, total int) {
	if m.store == nil {
		return 0, 0
	}
	total = m.store.FilteredMatchTotal()
	return min(m.currentMatch, total), total
}

func (m *Model) hiddenCount() int {
	if m.store == nil {
		return 0
	}
	return len(m.store.state.filtered)
}

type pillKind int

const (
	pillJQ pillKind = iota
	pillSearch
	pillFilter
)

func (m *Model) openPillEditor(k pillKind) {
	switch k {
	case pillJQ:
		m.openJQDialog()
	case pillSearch:
		m.openSearchDialog()
	case pillFilter:
		m.openFilterMenu()
	}
}
