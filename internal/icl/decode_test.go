package icl

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strPtr is a test helper that returns a pointer to a string literal.
func strPtr(s string) *string { return &s }

// kvField is a test helper for constructing key/value field literals.
func kvField(k, v string) KeyValue {
	return KeyValue{Key: strPtr(k), Value: strPtr(v)}
}

func TestNewMetadata_SkipsNilKeyOrValue(t *testing.T) {
	t.Parallel()
	fields := []KeyValue{
		{Key: nil, Value: strPtr("v")},
		{Key: strPtr("k"), Value: nil},
		kvField("logid", "abc"),
	}
	m, err := newMetadata(fields)
	require.NoError(t, err)
	assert.Equal(t, "abc", m.ID)
}

func TestUnpackResults_SkipsNilKeyOrValue(t *testing.T) {
	t.Parallel()
	fields := []KeyValue{
		{Key: nil, Value: strPtr("v")},
		{Key: strPtr("k1"), Value: nil},
		kvField("k2", "v2"),
	}
	m := unpackResults(fields)
	assert.Equal(t, map[string]any{"k2": "v2"}, m)
}

func TestNewLogResp_NilUserDataReturnsError(t *testing.T) {
	t.Parallel()
	result := DataprimeResults{UserData: nil}
	_, err := newLogResp(result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UserData")
}

func TestNewMetadata_ParseIntErrorReturnsError(t *testing.T) {
	t.Parallel()
	fields := []KeyValue{kvField("timestampMicros", "not-an-int")}
	_, err := newMetadata(fields)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timestampMicros")
}

func TestNewMetadata_ValidFieldsPopulateAllKnownKeys(t *testing.T) {
	t.Parallel()
	fields := []KeyValue{
		kvField("logid", "log-1"),
		kvField("priorityclass", "warning"),
		kvField("severity", "info"),
		kvField("timestampMicros", "1700000000000000"),
	}
	m, err := newMetadata(fields)
	require.NoError(t, err)
	assert.Equal(t, "log-1", m.ID)
	assert.Equal(t, int64(1700000000000000), m.TSMicro)
}

func TestDecodeStreamItem_WarningErrorAndGoodResult(t *testing.T) {
	t.Parallel()
	item := &StreamItem{
		Warning: &DataprimeWarning{
			TimeRangeWarning: &TimeRangeWarning{WarningMessage: strPtr("range clipped")},
		},
		Errors: []APIError{
			{Code: strPtr("bad_request"), Message: strPtr("something broke"), MoreInfo: strPtr("see docs")},
		},
		Result: &DataprimeResult{
			Results: []DataprimeResults{
				{
					UserData: strPtr(`{"msg":"hello"}`),
					Metadata: []KeyValue{kvField("logid", "log-1")},
				},
			},
		},
	}

	logs, warns, errs := DecodeStreamItem(item)

	require.Len(t, logs, 1)
	assert.Equal(t, "log-1", logs[0].Metadata.ID)
	require.Len(t, warns, 1)
	assert.Equal(t, "range clipped", warns[0])
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "bad_request")
	assert.Contains(t, errs[0], "something broke")
	assert.Contains(t, errs[0], "see docs")
}

func TestDecodeStreamItem_NilUserDataAppendsErrAndContinues(t *testing.T) {
	t.Parallel()
	item := &StreamItem{
		Result: &DataprimeResult{
			Results: []DataprimeResults{{UserData: nil}},
		},
	}

	logs, warns, errs := DecodeStreamItem(item)

	assert.Empty(t, logs)
	assert.Empty(t, warns)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "UserData")
}

func TestDecodeStreamItem_NilItemReturnsEmpty(t *testing.T) {
	t.Parallel()
	logs, warns, errs := DecodeStreamItem(nil)
	assert.Empty(t, logs)
	assert.Empty(t, warns)
	assert.Empty(t, errs)
}
