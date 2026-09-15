package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sampleStruct struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestWriteJSON_ValidJSON_RoundTrips(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	in := sampleStruct{Name: "alpha", Count: 42}
	require.NoError(t, writeJSON(&buf, in))

	var out sampleStruct
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, in, out)
}

func TestWriteJSON_TrailingNewline(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, sampleStruct{Name: "a", Count: 1}))
	assert.True(t, strings.HasSuffix(buf.String(), "\n"))
}

func TestWriteJSON_SonicAwareGolden(t *testing.T) {
	t.Parallel()
	in := sampleStruct{Name: "beta", Count: 7}
	golden, err := json.MarshalIndent(in, "", "  ")
	require.NoError(t, err)
	golden = append(golden, '\n')

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, in))
	assert.Equal(t, string(golden), buf.String())
}

func TestWriteJSON_NilValue_ProducesNull(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, nil))
	assert.Equal(t, "null\n", buf.String())
}
