package secret_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/secret"
)

func TestSecretString_LogValueRedacts(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("ctx", "tok", secret.String("real-token-value"))
	assert.Contains(t, buf.String(), "[REDACTED]")
	assert.NotContains(t, buf.String(), "real-token-value")
}

func TestSecretString_StringerRedacts(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "[REDACTED]", secret.String("real").String())
	assert.Equal(t, "[REDACTED]", fmt.Sprintf("%v", secret.String("real")))
}

func TestSecretString_EmptyAlsoRedacts(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("ctx", "tok", secret.String(""))
	assert.Contains(t, buf.String(), "[REDACTED]")
}

func TestSecretString_StdlibJSONMarshalsRaw(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(secret.String("x"))
	require.NoError(t, err)
	assert.Equal(t, `"x"`, string(b))
}

func TestSecretString_SonicJSONMarshalsRaw(t *testing.T) {
	t.Parallel()
	b, err := jsonutil.API.Marshal(secret.String("x"))
	require.NoError(t, err)
	assert.Equal(t, `"x"`, string(b))
}
