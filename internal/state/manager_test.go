package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/state"
	"github.com/bevicted/lognav/internal/state/statetest"
)

func TestManager_QuerySetAndGet(t *testing.T) {
	t.Parallel()
	m := statetest.NewTestManager(t)
	m.SetQuery("source.application_name:foo")
	assert.Equal(t, "source.application_name:foo", m.Query())
}

func TestManager_RestoreSnapshot_SetsAllFields(t *testing.T) {
	t.Parallel()
	m := statetest.NewTestManager(t)
	rules := []filter.Rule{{Type: filter.HasField, Value: ".data.status"}}
	m.RestoreSnapshot("last-fetched", "s", "j", rules)
	assert.Equal(t, "last-fetched", m.LastFetchedQuery())
	assert.Equal(t, "s", m.Search())
	assert.Equal(t, "j", m.JQ())
	assert.Equal(t, rules, m.Filters())
}

func TestManager_RestoreSnapshot_StoresFiltersClone(t *testing.T) {
	t.Parallel()
	m := statetest.NewTestManager(t)
	rules := []filter.Rule{{Type: filter.Include, Value: "foo"}}
	m.RestoreSnapshot("q", "s", "j", rules)
	rules[0].Value = "mutated"
	assert.Equal(t, "foo", m.Filters()[0].Value, "RestoreSnapshot must clone the filters slice")
}

func TestManager_RestoreSnapshot_NilFiltersLoadsEmpty(t *testing.T) {
	t.Parallel()
	m := statetest.NewTestManager(t)
	m.RestoreSnapshot("q", "s", "j", nil)
	assert.Empty(t, m.Filters())
}

func TestManager_InstancesReturnsCopy(t *testing.T) {
	t.Parallel()
	m := statetest.NewTestManager(t)
	m.SetInstances([]state.InstanceInfo{{Name: "a"}})

	got := m.Instances()
	got[0].Name = "mutated"

	again := m.Instances()
	assert.Equal(t, "a", again[0].Name, "internal slice must not be mutated by callers")
}

func TestManager_FiltersDefaultsEmpty(t *testing.T) {
	t.Parallel()
	m := statetest.NewTestManager(t)
	assert.Empty(t, m.Filters(), "a fresh Manager has no filters")
}

func TestManager_SetAndGetFilters(t *testing.T) {
	t.Parallel()
	m := statetest.NewTestManager(t)
	rules := []filter.Rule{
		{Type: filter.Include, Value: "foo"},
		{Type: filter.Exclude, Value: "noise"},
	}
	m.SetFilters(rules)
	assert.Equal(t, rules, m.Filters())
}

func TestManager_SetFilters_StoresClone_CallerCannotMutate(t *testing.T) {
	t.Parallel()
	m := statetest.NewTestManager(t)
	rules := []filter.Rule{{Type: filter.Include, Value: "foo"}}
	m.SetFilters(rules)

	// Mutating the caller's slice after SetFilters must not affect stored state.
	rules[0].Value = "mutated"

	got := m.Filters()
	assert.Equal(t, "foo", got[0].Value, "SetFilters must store a clone, not alias the caller slice")
}

func TestManager_Filters_ReturnsClone_CallerCannotMutate(t *testing.T) {
	t.Parallel()
	m := statetest.NewTestManager(t)
	m.SetFilters([]filter.Rule{{Type: filter.Include, Value: "foo"}})

	got := m.Filters()
	got[0].Value = "mutated"

	again := m.Filters()
	assert.Equal(t, "foo", again[0].Value, "Filters must return a clone so callers cannot mutate internal state")
}
