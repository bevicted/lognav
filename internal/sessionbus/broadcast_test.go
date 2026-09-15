package sessionbus

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCaller records calls instead of dialing real sockets.
type fakeCaller struct {
	mu    sync.Mutex
	calls []int // PIDs called
}

func (f *fakeCaller) call(_ context.Context, pid int, _ string, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, pid)
	return nil
}

func TestBroadcastSkipsOwnPID(t *testing.T) {
	t.Parallel()
	self := 4242
	fc := &fakeCaller{}
	b := &Client{
		self:    self,
		list:    func() ([]Socket, error) { return []Socket{{PID: self}, {PID: 99}, {PID: 100}}, nil },
		callOne: fc.call,
	}
	// Payload type is irrelevant to PID fan-out; use struct{}{} so this test does
	// not depend on SnapshotsDirtyParams (defined later in Task 4).
	require.NoError(t, b.Broadcast(context.Background(), "snapshots_dirty", struct{}{}))
	assert.ElementsMatch(t, []int{99, 100}, fc.calls, "must POST to every live PID except self")
}

func TestBroadcastReturnsDiscoveryError(t *testing.T) {
	t.Parallel()
	b := &Client{
		self: 4242,
		list: func() ([]Socket, error) { return nil, errors.New("socket dir missing") },
		callOne: func(_ context.Context, _ int, _ string, _ any) error {
			t.Fatal("callOne must not run when discovery fails")
			return nil
		},
	}
	require.Error(t, b.Broadcast(context.Background(), "snapshots_dirty", struct{}{}))
}

func TestBroadcastAbsorbsPerTargetFailure(t *testing.T) {
	t.Parallel()
	b := &Client{
		self: 4242,
		list: func() ([]Socket, error) { return []Socket{{PID: 99}, {PID: 100}}, nil },
		callOne: func(_ context.Context, _ int, _ string, _ any) error {
			return errors.New("peer unreachable")
		},
		logger: slog.Default(),
	}
	// Per-target failures are logged, never propagated, so a caller's exit code
	// never depends on peer reachability.
	require.NoError(t, b.Broadcast(context.Background(), "snapshots_dirty", struct{}{}))
}
