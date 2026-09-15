package icl

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClampCtx_NoCallerDeadlineUsesQueryTimeout(t *testing.T) {
	t.Parallel()
	before := time.Now()
	ctx, cancel := clampCtx(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	elapsed := deadline.Sub(before)
	// Expect close to queryTimeout (5m). Allow generous slack for slow CI.
	assert.InDelta(t, queryTimeout.Seconds(), elapsed.Seconds(), 5.0)
}

func TestClampCtx_ShorterCallerDeadlineWins(t *testing.T) {
	t.Parallel()
	short := 50 * time.Millisecond
	parent, cancelParent := context.WithTimeout(context.Background(), short)
	defer cancelParent()
	parentDeadline, _ := parent.Deadline()

	ctx, cancel := clampCtx(parent)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, parentDeadline, deadline, 5*time.Millisecond,
		"shorter caller deadline must be preserved")
}

func TestClampCtx_CancelReleasesContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := clampCtx(context.Background())
	cancel()
	select {
	case <-ctx.Done():
		// ok
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ctx not Done after cancel")
	}
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}
