package msgs

import (
	"testing"

	"github.com/bevicted/lognav/internal/filter"
)

func TestShowFilterMenuMsg_CarriesRules(t *testing.T) {
	t.Parallel()
	rules := []filter.Rule{{Type: filter.Include, Value: "foo"}}
	msg := ShowFilterMenuMsg{Rules: rules}
	if len(msg.Rules) != 1 || msg.Rules[0].Value != "foo" {
		t.Fatalf("ShowFilterMenuMsg did not carry rules: %+v", msg.Rules)
	}
}

func TestFilterApplied_IsAnEmptySignal(t *testing.T) {
	t.Parallel()
	_ = FilterAppliedMsg{}
}
