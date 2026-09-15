package snapshot_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bevicted/lognav/internal/snapshot"
)

func TestWipSuffixExported(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ".lognav.wip", snapshot.WipSuffix)
	assert.Equal(t, snapshot.FileExt+".wip", snapshot.WipSuffix)
}
