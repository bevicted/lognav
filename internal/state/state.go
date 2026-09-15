package state

import (
	"slices"
	"sync"

	"github.com/bevicted/lognav/internal/filter"
)

// Manager holds the lognav UI's shared mutable state. Every read/write
// acquires Manager.mu; paired writes (e.g. snapshot restore writing
// LastFetchedQuery, Search, and JQ together) stay atomic within one
// critical section.
//
// Construct via New(); the Manager struct remains exported so callers can hold
// *Manager references while state stays isolated through dependency injection.
type Manager struct {
	mu sync.RWMutex

	query            string
	lastFetchedQuery string
	search           string
	jq               string
	filters          []filter.Rule
	instances        []InstanceInfo
}

// InstanceInfo represents the state of a single instance.
type InstanceInfo struct {
	Name     string
	Enabled  bool
	State    string
	LogCount int
}

// New constructs an empty Manager.
func New() *Manager { return &Manager{} }

// --- getters --------------------------------------------------------------

func (m *Manager) Query() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.query
}

func (m *Manager) LastFetchedQuery() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastFetchedQuery
}

func (m *Manager) Search() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.search
}

func (m *Manager) JQ() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.jq
}

// Instances returns a defensive copy so callers cannot mutate the internal
// slice.
func (m *Manager) Instances() []InstanceInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.instances)
}

// Filters returns a defensive copy of the applied filter rules so callers
// (the off-loop filter recompute) cannot mutate the internal slice. Shallow
// clone suffices since filter.Rule is a value type. Mirrors Instances().
func (m *Manager) Filters() []filter.Rule {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.filters)
}

// --- setters --------------------------------------------------------------

func (m *Manager) SetQuery(q string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.query = q
}

func (m *Manager) SetLastFetchedQuery(q string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastFetchedQuery = q
}

func (m *Manager) SetSearch(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.search = s
}

func (m *Manager) SetJQ(j string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jq = j
}

func (m *Manager) SetInstances(infos []InstanceInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.instances = slices.Clone(infos)
}

// SetFilters stores a defensive copy of rules under Lock so the menu's working
// copy and the off-loop recompute's snapshot never alias. Mirrors SetInstances().
func (m *Manager) SetFilters(rules []filter.Rule) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.filters = slices.Clone(rules)
}

// --- paired-write helpers -------------------------------------------------

// RestoreSnapshot atomically sets LastFetchedQuery, Search, JQ, and Filters
// from a snapshot record. The filters slice is cloned (matching SetFilters) so
// the snapshot record cannot later alias internal state.
func (m *Manager) RestoreSnapshot(lastFetchedQuery, search, jq string, filters []filter.Rule) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastFetchedQuery = lastFetchedQuery
	m.search = search
	m.jq = jq
	m.filters = slices.Clone(filters)
}
