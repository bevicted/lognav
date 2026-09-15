package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithExit_WrapsErrorWithCode(t *testing.T) {
	t.Parallel()
	cause := errors.New("boom")
	err := WithExit(ExitConfig, cause)

	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitConfig, ee.Code)
	assert.Equal(t, cause, ee.Unwrap())
}

func TestExitError_Error_FormatsCodeAndCause(t *testing.T) {
	t.Parallel()
	err := WithExit(ExitUsage, errors.New("bad input"))
	assert.Contains(t, err.Error(), "bad input")
}

func TestExitError_Unwrap_ReturnsCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("root cause")
	err := WithExit(ExitNoInput, cause)
	assert.ErrorIs(t, err, cause)
}

func TestExitError_ErrorsAs_ExtractsCode(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("layer: %w", WithExit(ExitUnavailable, errors.New("svc down")))
	var ee *ExitError
	require.ErrorAs(t, wrapped, &ee)
	assert.Equal(t, ExitUnavailable, ee.Code)
}

// TestExitCode_ConstantMatrix pins the exit-code constants documented in
// docs/user/exit-codes.md per G[P2] D8. Divergence between code and docs
// breaks this test, not just the docs.
func TestExitCode_ConstantMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input error
		want  int
	}{
		{"nil_returns_ExitOK", nil, ExitOK},
		{"plain_returns_ExitGeneral", errors.New("anything"), ExitGeneral},
		{"WithExit_ExitUsage", WithExit(ExitUsage, errors.New("u")), ExitUsage},
		{"WithExit_ExitConfig", WithExit(ExitConfig, errors.New("c")), ExitConfig},
		{"WithExit_ExitNoInput", WithExit(ExitNoInput, errors.New("ni")), ExitNoInput},
		{"WithExit_ExitUnavailable", WithExit(ExitUnavailable, errors.New("ua")), ExitUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ExitCode(tc.input))
		})
	}
}
