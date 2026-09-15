package sessionbus

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/bevicted/lognav/internal/logging"
)

// BroadcastTimeout bounds a single per-target POST so a wedged but live peer
// cannot hang a CLI invocation. 500ms is ample for a local unix-socket round
// trip yet short enough that N stuck peers cost at most N*500ms.
const BroadcastTimeout = 500 * time.Millisecond

// Broadcaster sends a notify method to every live session except this process.
// The single method keeps callers (rename worker, CLI, claim/release sites)
// depending on a tiny seam that tests fake. The real Client is stateless (a
// per-call unix HTTP client) so it owns no goroutine/fd and needs no Close.
type Broadcaster interface {
	Broadcast(ctx context.Context, method string, payload any) error
}

// Client is the production Broadcaster. Fields are function seams so tests can
// substitute discovery and the per-target call without real sockets.
type Client struct {
	self    int
	list    func() ([]Socket, error)
	callOne func(ctx context.Context, pid int, method string, payload any) error
	logger  *slog.Logger
}

// NewClient returns a Broadcaster backed by real socket discovery + unix transport.
func NewClient() *Client {
	return &Client{
		self:    os.Getpid(),
		list:    ListSockets,
		callOne: realCall,
		logger:  slog.Default().With(logging.KeyComponent, "sessionbus"),
	}
}

// realCall dials the session owning pid and POSTs method+payload.
// ctx is already bounded by Broadcast's per-target timeout.
func realCall(ctx context.Context, pid int, method string, payload any) error {
	s, err := GetSocketFromPID(pid)
	if err != nil {
		return err
	}
	_, err = CallSession(ctx, s.Path, method, payload)
	return err
}

// Broadcast POSTs method+payload to every live session except self,
// synchronously, with a bounded per-target timeout. Per-target failures are
// logged and do not abort the remaining sends; Broadcast returns nil so a
// caller's exit code never depends on peer reachability.
func (c *Client) Broadcast(ctx context.Context, method string, payload any) error {
	socks, err := c.list()
	if err != nil {
		return err
	}
	logger := c.logger
	if logger == nil {
		logger = slog.Default().With(logging.KeyComponent, "sessionbus")
	}
	for _, s := range socks {
		if s.PID == c.self {
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, BroadcastTimeout)
		if err := c.callOne(tctx, s.PID, method, payload); err != nil {
			logger.Warn("broadcast to session failed", "pid", s.PID, logging.KeyError, err)
		}
		cancel()
	}
	return nil
}
