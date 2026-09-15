package msgstest

import (
	"context"
	"sync"

	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
)

// FakePoster is a test double for msgs.Poster that records every posted event.
// Go runs its function inline so posts from off-loop bodies land synchronously.
type FakePoster struct {
	mu     sync.Mutex
	Posted []uv.Event
}

func (f *FakePoster) Post(ev uv.Event)                                  { f.record(ev) }
func (f *FakePoster) PostCritical(_ context.Context, ev uv.Event) error { f.record(ev); return nil }
func (f *FakePoster) Go(fn func(context.Context))                       { fn(context.Background()) }

func (f *FakePoster) record(ev uv.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Posted = append(f.Posted, ev)
}

var _ msgs.Poster = (*FakePoster)(nil)
