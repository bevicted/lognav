package xdg

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvOrFallback(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string

		getEnvResult string
		userHomeDir  userHomeDirF

		expectedResult string
		expectedErr    error
	}{
		{
			name:         "ok - env set",
			getEnvResult: "/my/xdg/path",
			userHomeDir: func() (string, error) {
				require.FailNow(t, "userHomeDir should not have been called")
				return "", nil
			},
			expectedResult: "/my/xdg/path/lognav",
		},
		{
			name: "ok - fallback",
			userHomeDir: func() (string, error) {
				return "/my/home", nil
			},
			expectedResult: "/my/home/my/fallback/lognav",
		},
		{
			name: "fail - UserHomeDir err",
			userHomeDir: func() (string, error) {
				return "", errors.New("UserHomeDir error")
			},
			expectedErr: errors.New("UserHomeDir error"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := "env"

			result, err := envOrFallback(
				func(key string) string {
					assert.Equal(t, env, key)
					return tc.getEnvResult
				},
				tc.userHomeDir,
				env, "/my/fallback",
			)

			assert.Equal(t, tc.expectedResult, result)
			assert.Equal(t, tc.expectedErr, err)
		})
	}
}
