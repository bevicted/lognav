package logviewer

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterMenuKeybind_PostsShowWithStateRules(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "foo"}})
	m := New(bundle)
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)

	m.HandleKey(keystest.PressRuneUV(t, 'f'))

	var show msgs.ShowFilterMenuMsg
	found := false
	for _, ev := range fp.Posted {
		if s, ok := ev.(msgs.ShowFilterMenuMsg); ok {
			show, found = s, true
		}
	}
	require.True(t, found, "f should post ShowFilterMenuMsg")
	require.Len(t, show.Rules, 1)
	assert.Equal(t, "foo", show.Rules[0].Value)
}
