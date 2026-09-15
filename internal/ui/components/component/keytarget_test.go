package component

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeyResult_ZeroValueIsIgnored(t *testing.T) {
	t.Parallel()
	var r KeyResult
	assert.Equal(t, KeyIgnored, r)
}
